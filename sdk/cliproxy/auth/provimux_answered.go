package auth

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Provimux: the whole statement of who answered (.github/provimux/design/answered-by.md 5). The conductor states who
// answered once per call (publishAnsweredAuthMetadata) through cliproxyexecutor.AnsweredAuthCallbackMetadataKey,
// which takes the statement's first three values. A caller that also installs ProvimuxAnswerCallbackMetadataKey
// receives the same statement whole, Provimux's values included, in the same call and right after.

// ProvimuxAnswerCallbackMetadataKey carries an optional func(logging.AuthStatement): the whole statement of the
// response a call returns, or the zero statement where nobody is named. It is called only where
// AnsweredAuthCallbackMetadataKey's callback is, and right after it.
const ProvimuxAnswerCallbackMetadataKey = "provimux_answer_callback"

// ProvimuxPluginCredentialMark is the metadata key that marks a credential a plugin made or changed (the lead's rule
// of 2026-11-27): the plugin host sets it where it makes a credential from a plugin's data, so a plugin's login and an
// update a plugin returns carry it from the moment they exist, and it is stored with them. A native login that writes
// the credential's record anew sets it to false, the same way for every provider and every store
// (ProvimuxLoginClearsPluginMark).
const ProvimuxPluginCredentialMark = "provimux_plugin_credential"

// ProvimuxLoginClearsPluginMark is called by every token store where it saves a credential: a save of a login (one
// with creation intent) whose storage is not the plugin host's records that a native login wrote it, so a mark an
// earlier plugin left in the same record does not outlive it. Any other save is left as it is.
func ProvimuxLoginClearsPluginMark(ctx context.Context, auth *Auth) {
	if auth == nil || !HasAuthCreationIntent(ctx) {
		return
	}
	if made, ok := auth.Storage.(interface{ ProvimuxPluginMade() bool }); ok && made.ProvimuxPluginMade() {
		return
	}
	if auth.Metadata == nil {
		if auth.Storage == nil {
			return // nothing is written from this record's metadata: a store refuses it as it did
		}
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[ProvimuxPluginCredentialMark] = false
}

// provimuxEntryHooks are the functions that decide an answer's entry, set once by sdk/cliproxy while the program
// initialises (SetProvimuxAnswerEntry): kind names the closed-list kind of a built-in executor, "" for any other, and
// own says whether an executor is one of the gateway's built-in ones at all (the closed list, and AI Studio's, which
// names no entry) (answered-by.md, rule A.1 as decided in part (5)); vouch says whether no plugin made or changed
// the credential (A.3); entry gives the entry of what that credential's attempt sent
// (answered-by.md 4.3, steps 4 and 5). Without them no answer names an entry.
type provimuxEntryHooks struct {
	kind  func(exec ProviderExecutor) string
	own   func(exec ProviderExecutor) bool
	vouch func(auth *Auth) bool
	entry func(auth *Auth, sent cliproxyexecutor.ProvimuxSentRequest) string
}

var provimuxAnswerEntry atomic.Pointer[provimuxEntryHooks]

// SetProvimuxAnswerEntry sets the functions that decide an answer's entry. The first call holds and later calls do
// nothing: sdk/cliproxy makes it while the program initialises, before any code that imports the gateway runs, so no
// embedding can replace them.
func SetProvimuxAnswerEntry(kind func(exec ProviderExecutor) string, own func(exec ProviderExecutor) bool, vouch func(auth *Auth) bool, entry func(auth *Auth, sent cliproxyexecutor.ProvimuxSentRequest) string) {
	if kind == nil || own == nil || vouch == nil || entry == nil {
		return
	}
	provimuxAnswerEntry.CompareAndSwap(nil, &provimuxEntryHooks{kind: kind, own: own, vouch: vouch, entry: entry})
}

// provimuxForeign holds, per manager and provider, that an executor which is not one of the gateway's built-in ones
// (a plugin's, the plugin-refresh wrapper, anything other code registers) was once put in place for the provider. It
// is set once, never cleared: from then on the provider's answers name no entry until the process ends (answered-by.md,
// part (5)). The gateway as shipped has one manager for its life, so this is per provider for the life of the process.
var provimuxForeign sync.Map // provimuxForeignKey -> struct{}

type provimuxForeignKey struct {
	m        *Manager
	provider string
}

// provimuxRegistering is called by Manager.RegisterExecutorWithRevision, the one place every executor registration
// passes, before the executor is put in place: so no call can be served by it before its provider is marked. It looks
// only at what is being registered, never at what was there or when: the gateway putting its own built-in executor
// back, however often, never marks anything.
func provimuxRegistering(m *Manager, provider string, exec ProviderExecutor) {
	if hooks := provimuxAnswerEntry.Load(); hooks != nil && hooks.own(exec) {
		return
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if _, marked := provimuxForeign.LoadOrStore(provimuxForeignKey{m: m, provider: provider}, struct{}{}); !marked && origin.ShippedGateway() {
		log.Infof("answered-by: a plugin took part for provider %q (an executor that is not one of the gateway's own was put in place); its answers name no providers.dev entry until the gateway restarts", provider)
	}
}

// provimuxForeignTookPart reports whether an executor not the gateway's own was ever put in place for any provider an
// answer of auth by exec may have been served under: the credential's own key, its scheduling alias, exec's.
func (m *Manager) provimuxForeignTookPart(auth *Auth, exec ProviderExecutor) bool {
	key := strings.ToLower(strings.TrimSpace(executorKeyFromAuth(auth)))
	for _, provider := range []string{key, canonicalSchedulingProvider(key), strings.ToLower(strings.TrimSpace(exec.Identifier()))} {
		if _, marked := provimuxForeign.Load(provimuxForeignKey{m: m, provider: provider}); marked {
			return true
		}
	}
	return false
}

type provimuxAttemptKey struct{}

// provimuxWithManager says, on an attempt's context, which manager runs it: the conductor calls
// newUpstreamAttemptContext right after it picked the attempt's credential and executor. An inner context of the same
// attempt (a retry after a refresh) keeps the first. Outside the gateway as shipped it adds nothing.
func provimuxWithManager(ctx context.Context, m *Manager) context.Context {
	if ctx == nil || m == nil || provimuxAnswerEntry.Load() == nil || !logging.ProvimuxAnsweredBy() || !origin.ShippedGateway() {
		return ctx
	}
	if earlier, ok := ctx.Value(provimuxAttemptKey{}).(*Manager); ok && earlier == m {
		return ctx
	}
	return context.WithValue(ctx, provimuxAttemptKey{}, m)
}

// provimuxServingExecutor returns the executor in place for auth, looked up the way the conductor picks it.
func (m *Manager) provimuxServingExecutor(auth *Auth) ProviderExecutor {
	key := executorKeyFromAuth(auth)
	exec, ok := m.Executor(key)
	if !ok {
		exec, ok = m.Executor(canonicalSchedulingProvider(key))
	}
	if !ok {
		return nil
	}
	return exec
}

// ProvimuxAnsweredItem is one item of what a credential's answers for a name said of themselves (answered-by.md 4.4
// as amended on 2026-11-27): an entry, "" for answers that named none, and when the last answer that said so was
// written to its client.
type ProvimuxAnsweredItem struct {
	Entry string
	At    time.Time
}

// provimuxAnsweredHooks are the record's functions, set by sdk/cliproxy (SetProvimuxAnswered): name gives the row an
// answer for a called name stands on ("" for none); read gives the items of one client (all clients with all) for a
// credential, its marker and a name.
type provimuxAnsweredHooks struct {
	name func(authID, called string) string
	read func(client string, all bool, authID, marker, name string) []ProvimuxAnsweredItem
}

var provimuxAnsweredRecord atomic.Pointer[provimuxAnsweredHooks]

// SetProvimuxAnswered sets the record's functions; nil for either removes both.
func SetProvimuxAnswered(name func(authID, called string) string, read func(client string, all bool, authID, marker, name string) []ProvimuxAnsweredItem) {
	if name == nil || read == nil {
		provimuxAnsweredRecord.Store(nil)
		return
	}
	provimuxAnsweredRecord.Store(&provimuxAnsweredHooks{name: name, read: read})
}

// ProvimuxAnswered returns what the answers of credential authID (whose marker is marker) for name said to the client
// whose reference is client (ProvimuxClientRef; "" for callers without a key), newest first. nil without a marker, with
// models.answered-by off, or without a record.
func ProvimuxAnswered(client, authID, marker, name string) []ProvimuxAnsweredItem {
	return provimuxAnsweredRead(client, false, authID, marker, name)
}

// ProvimuxAnsweredAll is ProvimuxAnswered over every client's record together: the management list's.
func ProvimuxAnsweredAll(authID, marker, name string) []ProvimuxAnsweredItem {
	return provimuxAnsweredRead("", true, authID, marker, name)
}

func provimuxAnsweredRead(client string, all bool, authID, marker, name string) []ProvimuxAnsweredItem {
	hooks := provimuxAnsweredRecord.Load()
	if hooks == nil || marker == "" || name == "" || authID == "" || !ProvimuxRecordOn() {
		return nil
	}
	return hooks.read(client, all, authID, marker, name)
}

// ProvimuxRecordOn reports whether the record of past answers exists: models.answered-by on, in a process Provimux's
// own main started (the lead's amendment of 2026-11-27 (2), part 3). Off, the lists carry no `answered`.
func ProvimuxRecordOn() bool { return logging.ProvimuxAnsweredBy() && origin.ShippedGateway() }

// ProvimuxClientRef is what the record keeps of a client key: a keyed hash under the markers' key, never the key.
// "" for a caller without a key (they share one record) and without the markers' key.
func ProvimuxClientRef(clientKey string) string {
	return provimuxRef("k", "provimux answered client v1", clientKey)
}

// ProvimuxCredentialRef is what the record keeps of a credential's id: a keyed hash, as the client's.
func ProvimuxCredentialRef(authID string) string {
	return provimuxRef("c", "provimux answered credential v1", authID)
}

func provimuxRef(basis, context, value string) string {
	if value == "" {
		return ""
	}
	key := provimuxMarkerKey.Load()
	if key == nil || len(*key) == 0 {
		return ""
	}
	return provimuxMarkerOf(*key, basis, []byte(context+"\x00"+value))
}

type provimuxCalledNameKey struct{}

// WithProvimuxCalledName says, on a call's context, the name its client called where the route passes the manager
// another (a speech call routed to its executor's model): the record stands under the name called.
func WithProvimuxCalledName(ctx context.Context, name string) context.Context {
	if ctx == nil || name == "" {
		return ctx
	}
	return context.WithValue(ctx, provimuxCalledNameKey{}, name)
}

// publishProvimuxAnswer completes the statement the conductor has just made for the response it returns, and hands it
// to ProvimuxAnswerCallbackMetadataKey. It is called only from publishAnsweredAuthMetadata, after its callback, with
// the same attempt context, credential and statement; for an empty statement it hands over the empty statement.
//
// A statement that names an account gains what its attempt sent, and its entry, only where Provimux made every part
// (the lead's amendment of 2026-11-27 (2)): the executor that answers is one of the gateway's built-in executors, which
// its own service registered (A.1); its attempt sent exactly one request that could have produced the answer, over a
// client the gateway built, and the model was read from the bytes handed to the transport
// (cliproxyexecutor.ProvimuxSent: A.2, A.4, A.5, A.6); the gateway's loader made the credential (A.3, in the entry
// function). Where any part is missing, neither. It also carries, for the record and never on the wire, the credential
// and the row the answer stands on; the record is written where the statement is written to the client, never here.
func publishProvimuxAnswer(attemptCtx context.Context, meta map[string]any, auth *Auth, statement logging.AuthStatement) {
	callback, ok := meta[ProvimuxAnswerCallbackMetadataKey].(func(logging.AuthStatement))
	if !ok || callback == nil {
		return
	}
	if statement.Provimux.Account != "" && auth != nil {
		// The sent model goes with the entry (answered-by.md, the amendment of 2026-11-27 (2), A: "An answer names an
		// entry (and X-Provimux-Model-Sent, -Model, -Model-Basis) only when all of this holds"); within that, the
		// entry alone is left out where the map has no row for what was sent (section 4).
		if sent, okSent := provimuxSentByBuiltin(attemptCtx, auth); okSent {
			statement.Provimux.ModelSent = sent.Model
			statement.Provimux.Entry = provimuxAnswerEntry.Load().entry(auth, sent)
		}
		if hooks := provimuxAnsweredRecord.Load(); hooks != nil && origin.ShippedGateway() {
			called, _ := attemptCtx.Value(provimuxCalledNameKey{}).(string)
			if called == "" {
				called = stringMetadataValue(meta, cliproxyexecutor.RequestedModelMetadataKey)
			}
			statement.Provimux.Credential = auth.ID
			statement.Provimux.Name = hooks.name(auth.ID, called)
		}
	}
	callback(statement)
}

// provimuxSentByBuiltin returns what the attempt sent when every part is the gateway's own as shipped: the process is
// one Provimux's own main started (part 3), the executor the attempt was handed to for auth is one of the built-in
// executors and no executor that is not the gateway's own was ever put in place for its provider in this process (rule
// A.1, as decided in parts (5) and (6)), the gateway's loader made the credential (A.3), and the attempt's one candidate request is certain.
func provimuxSentByBuiltin(attemptCtx context.Context, auth *Auth) (cliproxyexecutor.ProvimuxSentRequest, bool) {
	hooks := provimuxAnswerEntry.Load()
	if hooks == nil || attemptCtx == nil || !origin.ShippedGateway() || !hooks.vouch(auth) {
		return cliproxyexecutor.ProvimuxSentRequest{}, false
	}
	m, ok := attemptCtx.Value(provimuxAttemptKey{}).(*Manager)
	if !ok {
		return cliproxyexecutor.ProvimuxSentRequest{}, false
	}
	exec := m.provimuxServingExecutor(auth)
	if exec == nil || m.provimuxForeignTookPart(auth, exec) {
		return cliproxyexecutor.ProvimuxSentRequest{}, false
	}
	kind := hooks.kind(exec)
	if kind == "" {
		return cliproxyexecutor.ProvimuxSentRequest{}, false
	}
	return cliproxyexecutor.ProvimuxSent(attemptCtx, kind)
}
