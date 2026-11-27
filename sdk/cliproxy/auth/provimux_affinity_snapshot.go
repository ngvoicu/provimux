package auth

// Provimux layer: session affinity's session cache, copied out of memory and put back after a
// restart.
//
// Nothing here runs on a request. Capture copies the SessionCache groups of a
// SessionAffinitySelector under the cache's read lock, in the cache's own eviction order, and
// returns them; the caller encrypts and writes them after the lock is released. Restore puts them
// back into a selector that has served nothing yet, in the same order, with the same keys and
// selection sequence, each with what was left of its own lifetime. After that the bindings live in
// memory by upstream's rules alone.
//
// Only the session cache is kept. The LCP matcher (conversations matched by their prefix) is not:
// a conversation the matcher was holding is bound again after a restart, as a new one would be.
//
// An entry names its account by a token the caller derives from the account's stable vendor
// identity (never a file name); restore puts it back only on the account that has that token now.
//
// Whether two builds mean the same thing by an entry is not assumed: every snapshot carries a pin,
// sample requests with what the writing build's selector made of them (the keys, the binding, the
// eviction order, the lifetime left after a refresh, an expiry, an eviction). The reading build
// runs the same samples through its own selector. Where a sample of one kind of conversation
// differs (a "claude:" or "msg:" key, say), the entries holding a key of that kind are left out;
// where a sample of the rules every entry follows differs (order, lifetimes, expiry, eviction, the
// model key, what an entry holds), the snapshot is ignored.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ProvimuxAffinityFormat is the version of the snapshot's contents.
const ProvimuxAffinityFormat = 2

// ProvimuxAffinityState is what one snapshot holds. Times are Unix nanoseconds.
type ProvimuxAffinityState struct {
	Format    int                       `json:"format"`
	Pin       ProvimuxAffinityPin       `json:"pin"`
	WrittenAt int64                     `json:"written_at"`
	TTL       int64                     `json:"ttl"`
	Sessions  []ProvimuxAffinitySession `json:"sessions,omitempty"`
}

// ProvimuxAffinitySession is one SessionCache group, as upstream holds it. Sessions are listed in
// the cache's eviction order (oldest first).
type ProvimuxAffinitySession struct {
	Account   string   `json:"account"`
	Keys      []string `json:"keys"`
	ExpiresAt int64    `json:"expires_at"`
	SessionID string   `json:"session_id,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	Sequence  uint64   `json:"sequence,omitempty"`
}

// ProvimuxAffinityRestored counts what a restore put back and what it left out.
type ProvimuxAffinityRestored struct {
	Sessions        int `json:"sessions"`
	Expired         int `json:"expired"`
	UnknownAccounts int `json:"unknown_accounts"`
	// OtherBuild: entries of a kind of conversation this build names or keeps differently.
	OtherBuild int `json:"other_build"`
	// OtherBuildKinds are those kinds (the prefix of the conversation's identity, e.g. "msg").
	OtherBuildKinds []string `json:"other_build_kinds,omitempty"`
}

func provimuxAffinitySelector(selector Selector) (*SessionAffinitySelector, error) {
	affinity, ok := selector.(*SessionAffinitySelector)
	if !ok || affinity == nil || affinity.cache == nil {
		return nil, fmt.Errorf("session affinity is not in use")
	}
	return affinity, nil
}

// ProvimuxAffinityCapture copies the selector's session cache. account maps an auth ID and the
// entry's last use to the account's token; an entry it refuses (no stable identity, or an
// identity that changed after that use) is left out. The cache's read lock is held only while
// copying.
func ProvimuxAffinityCapture(selector Selector, account func(authID string, lastUse time.Time) (string, bool), now time.Time) (*ProvimuxAffinityState, error) {
	affinity, errSelector := provimuxAffinitySelector(selector)
	if errSelector != nil {
		return nil, errSelector
	}
	pin, errPin := ProvimuxAffinityCurrentPin()
	if errPin != nil {
		return nil, errPin
	}
	cache := affinity.cache
	cache.mu.RLock()
	ttl := cache.ttl
	groups := make([]sessionEntry, 0, len(cache.groups))
	if cache.evictionOrder != nil {
		for element := cache.evictionOrder.Front(); element != nil; element = element.Next() {
			primaryKey, _ := element.Value.(string)
			if group, ok := cache.groups[primaryKey]; ok {
				group.aliases = append([]string(nil), group.aliases...)
				groups = append(groups, group)
			}
		}
	}
	cache.mu.RUnlock()

	state := &ProvimuxAffinityState{Format: ProvimuxAffinityFormat, Pin: pin, WrittenAt: now.UnixNano(), TTL: int64(ttl)}
	for _, group := range groups {
		if !now.Before(group.expiresAt) || len(group.aliases) == 0 {
			continue
		}
		token, known := account(group.authID, group.expiresAt.Add(-ttl))
		if !known {
			continue
		}
		state.Sessions = append(state.Sessions, ProvimuxAffinitySession{
			Account: token, Keys: group.aliases, ExpiresAt: group.expiresAt.UnixNano(),
			SessionID: group.binding.sessionID, Provider: group.binding.provider, Sequence: group.binding.sequence,
		})
	}
	return state, nil
}

// ProvimuxAffinityRestore puts a snapshot's bindings into a selector that has served nothing yet.
// accounts maps a token to the auth ID that has it now. An entry lives until the earlier of its
// own expiry and its last use plus the TTL configured now; a snapshot written in the future, older
// than the TTL, of another format, or whose pin this build does not reproduce restores nothing.
func ProvimuxAffinityRestore(selector Selector, state *ProvimuxAffinityState, accounts map[string]string, now time.Time) (ProvimuxAffinityRestored, error) {
	var out ProvimuxAffinityRestored
	affinity, errSelector := provimuxAffinitySelector(selector)
	if errSelector != nil {
		return out, errSelector
	}
	if state == nil {
		return out, nil
	}
	if state.Format != ProvimuxAffinityFormat {
		return out, fmt.Errorf("snapshot of another format (%d)", state.Format)
	}
	differs, errPin := ProvimuxAffinityCheckPin(state.Pin)
	if errPin != nil {
		return out, errPin
	}
	for kind := range differs {
		out.OtherBuildKinds = append(out.OtherBuildKinds, kind)
	}
	sort.Strings(out.OtherBuildKinds)
	cache := affinity.cache
	ttl := cache.ttl
	written := time.Unix(0, state.WrittenAt)
	if written.After(now) {
		return out, fmt.Errorf("snapshot written in the future (%s)", written.UTC().Format(time.RFC3339))
	}
	if now.Sub(written) >= ttl {
		return out, fmt.Errorf("snapshot older than session-affinity-ttl")
	}
	oldTTL := time.Duration(state.TTL)
	if oldTTL <= 0 {
		return out, fmt.Errorf("snapshot without a ttl")
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ensureInitializedLocked()
	for _, session := range state.Sessions {
		if provimuxAffinityAnyKind(session.Keys, differs) {
			out.OtherBuild++
			continue
		}
		authID, known := accounts[session.Account]
		if !known {
			out.UnknownAccounts++
			continue
		}
		expires := provimuxAffinityExpiry(time.Unix(0, session.ExpiresAt), oldTTL, ttl, written)
		if !expires.After(now) || len(session.Keys) == 0 {
			out.Expired++
			continue
		}
		binding := sessionBinding{sessionID: session.SessionID, provider: session.Provider, sequence: session.Sequence}
		if binding.sequence > cache.bindingSequence {
			cache.bindingSequence = binding.sequence
		}
		// In the snapshot's order, which is the old cache's eviction order: the new cache's
		// eviction order is the same.
		cache.replaceAliasGroupsLocked(authID, expires, append([]string(nil), session.Keys...), binding)
		out.Sessions++
	}
	return out, nil
}

// provimuxAffinityExpiry is the earlier of an entry's own expiry and its last use plus the TTL
// configured now, and never later than the snapshot's time plus that TTL.
func provimuxAffinityExpiry(expires time.Time, oldTTL, ttl time.Duration, written time.Time) time.Time {
	if lastUse := expires.Add(-oldTTL); lastUse.Add(ttl).Before(expires) {
		expires = lastUse.Add(ttl)
	}
	if latest := written.Add(ttl); expires.After(latest) {
		expires = latest
	}
	return expires
}

// ProvimuxAffinityPin is what a build makes of fixed sample requests: the samples themselves and,
// for each, the selector's results. It travels in the snapshot.
type ProvimuxAffinityPin struct {
	Layout  string                      `json:"layout"`
	Samples []ProvimuxAffinityPinSample `json:"samples"`
	Results []string                    `json:"results"`
}

// ProvimuxAffinityPinSample runs on a fresh selector (round-robin, TTL one hour, two accounts
// pin-a and pin-b of Provider): its steps in order.
type ProvimuxAffinityPinSample struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// Rule: the sample shows a rule every entry follows; a difference voids the whole snapshot.
	// Otherwise it shows how one kind of conversation is named; a difference leaves out the
	// entries of the kinds its keys have.
	Rule  bool                      `json:"rule,omitempty"`
	Steps []ProvimuxAffinityPinStep `json:"steps"`
}

// ProvimuxAffinityPinStep is one step of a sample. Op: "pick" (Enrich then Pick, as a request
// does), "pick-raw" (Pick alone: the first-message hashes, used when no identity was derived),
// "show-capacity" (the cache's default limit), "age-ms" (every entry gets Value milliseconds of
// lifetime left), "ok" and "fail" (the result of the last pick, a 429 for fail), "age" (every entry gets
// Value × TTL of lifetime left), "capacity" (the cache holds at most Value keys).
type ProvimuxAffinityPinStep struct {
	Op       string            `json:"op"`
	Model    string            `json:"model,omitempty"`
	Format   string            `json:"format,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Value    float64           `json:"value,omitempty"`
}

var (
	provimuxAffinityPinOnce  sync.Once
	provimuxAffinityPinValue ProvimuxAffinityPin
	provimuxAffinityPinErr   error
)

// ProvimuxAffinityCurrentPin is this build's pin: its samples and its results, computed once.
func ProvimuxAffinityCurrentPin() (ProvimuxAffinityPin, error) {
	provimuxAffinityPinOnce.Do(func() {
		samples := provimuxAffinityPinSamples()
		results, errRun := provimuxAffinityRunPin(samples)
		provimuxAffinityPinValue = ProvimuxAffinityPin{Layout: provimuxAffinityEntryLayout(), Samples: samples, Results: results}
		provimuxAffinityPinErr = errRun
	})
	return provimuxAffinityPinValue, provimuxAffinityPinErr
}

// ProvimuxAffinityCheckPin runs a snapshot's samples through this build and compares the results
// with the ones the writing build recorded. It returns the kinds of conversation named differently
// (their entries are left out), or an error when a rule differs (the snapshot is ignored).
func ProvimuxAffinityCheckPin(pin ProvimuxAffinityPin) (map[string]bool, error) {
	if pin.Layout != provimuxAffinityEntryLayout() {
		return nil, fmt.Errorf("snapshot of a build whose session-cache entry holds other fields")
	}
	if len(pin.Samples) == 0 || len(pin.Samples) != len(pin.Results) {
		return nil, fmt.Errorf("snapshot without a usable pin")
	}
	results, errRun := provimuxAffinityRunPin(pin.Samples)
	if errRun != nil {
		return nil, errRun
	}
	differs := map[string]bool{}
	for i := range results {
		if results[i] == pin.Results[i] {
			continue
		}
		kinds := provimuxAffinityResultKinds(pin.Results[i])
		if pin.Samples[i].Rule || len(kinds) == 0 {
			return nil, fmt.Errorf("snapshot of a build whose session affinity keeps conversations differently (sample %q)", pin.Samples[i].Name)
		}
		for _, kind := range kinds {
			differs[kind] = true
		}
	}
	return differs, nil
}

// provimuxAffinityKind is the kind of a session-cache key "<provider>::<identity>::<model>": the
// identity up to its first colon ("claude", "header", "pck", "msg", "derived", ...).
func provimuxAffinityKind(key string) string {
	parts := strings.Split(key, "::")
	if len(parts) < 3 {
		return key
	}
	identity := strings.Join(parts[1:len(parts)-1], "::")
	kind, _, _ := strings.Cut(identity, ":")
	return kind
}

func provimuxAffinityAnyKind(keys []string, kinds map[string]bool) bool {
	if len(kinds) == 0 {
		return false
	}
	for _, key := range keys {
		if kinds[provimuxAffinityKind(key)] {
			return true
		}
	}
	return false
}

// provimuxAffinityResultKinds lists the kinds of the keys a sample's result shows.
func provimuxAffinityResultKinds(result string) []string {
	seen := map[string]bool{}
	var kinds []string
	for _, line := range strings.Split(result, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "pin-") {
			continue
		}
		for _, key := range strings.Split(fields[1], ",") {
			if kind := provimuxAffinityKind(key); kind != "" && !seen[kind] {
				seen[kind] = true
				kinds = append(kinds, kind)
			}
		}
	}
	return kinds
}

// provimuxAffinityEntryLayout is what a session-cache entry holds, as the compiler sees it.
// Types only: no value is read through reflection.
func provimuxAffinityEntryLayout() string {
	describe := func(typ reflect.Type) string {
		parts := make([]string, 0, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			parts = append(parts, typ.Field(i).Name+":"+typ.Field(i).Type.String())
		}
		return typ.String() + "{" + strings.Join(parts, ";") + "}"
	}
	return describe(reflect.TypeOf(sessionEntry{})) + "\n" + describe(reflect.TypeOf(sessionBinding{}))
}

// provimuxAffinityRunPin runs samples, each on a fresh selector. The selector logs every pick at
// info level; those lines describe samples, not traffic, so they are not logged (this runs at
// start, before the server exists, a few milliseconds).
func provimuxAffinityRunPin(samples []ProvimuxAffinityPinSample) (results []string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("session affinity: a pin sample failed: %v", recovered)
		}
	}()
	results = make([]string, len(samples))
	for i, sample := range samples {
		results[i] = provimuxAffinityRunSample(sample)
	}
	return results, nil
}

// provimuxQuietLogKey marks a context whose selector lines go nowhere: the pin's samples run real
// selectors, and their lines are not the gateway's. The process's log level is never touched (it
// would drop every other goroutine's lines while the samples run).
type provimuxQuietLogKey struct{}

var (
	provimuxQuietContext = context.WithValue(context.Background(), provimuxQuietLogKey{}, true)
	provimuxQuietLogger  = func() *log.Logger {
		logger := log.New()
		logger.SetOutput(io.Discard)
		logger.SetLevel(log.PanicLevel)
		return logger
	}()
)

// provimuxQuietSelectorLog is the selector's log entry for a context of the pin's samples; nil for
// any other.
func provimuxQuietSelectorLog(ctx context.Context) *log.Entry {
	if quiet, _ := ctx.Value(provimuxQuietLogKey{}).(bool); quiet {
		return log.NewEntry(provimuxQuietLogger)
	}
	return nil
}

func provimuxAffinityRunSample(sample ProvimuxAffinityPinSample) string {
	const ttl = time.Hour
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: ttl})
	defer selector.Stop()
	provider := sample.Provider
	authProvider := provider
	if authProvider == "" || authProvider == "mixed" {
		authProvider = "claude"
	}
	auths := []*Auth{{ID: "pin-a", Provider: authProvider, Status: StatusActive}, {ID: "pin-b", Provider: authProvider, Status: StatusActive}}
	cache := selector.cache
	var out strings.Builder
	var last struct {
		authID string
		model  string
		opts   cliproxyexecutor.Options
	}
	for _, step := range sample.Steps {
		switch step.Op {
		case "pick", "pick-raw":
			metadata := make(map[string]any, len(step.Metadata))
			for key, value := range step.Metadata {
				metadata[key] = value
			}
			headers := http.Header{}
			for key, value := range step.Headers {
				headers.Set(key, value)
			}
			body := []byte(step.Body)
			req := cliproxyexecutor.Request{Model: step.Model, Payload: body, Format: sdktranslator.Format(step.Format), Metadata: map[string]any{}}
			opts := cliproxyexecutor.Options{Headers: headers, OriginalRequest: body, SourceFormat: sdktranslator.Format(step.Format), Metadata: metadata}
			if step.Op == "pick" {
				_, opts = cliproxysession.Enrich(req, opts)
			}
			picked, errPick := selector.Pick(provimuxQuietContext, provider, step.Model, opts, auths)
			last.model, last.opts, last.authID = step.Model, opts, ""
			if errPick != nil {
				fmt.Fprintf(&out, "pick error %v\n", errPick)
			} else if picked != nil {
				last.authID = picked.ID
				fmt.Fprintf(&out, "pick %s\n", picked.ID)
			}
		case "ok", "fail":
			result := Result{AuthID: last.authID, Provider: provider, Model: last.model, Success: step.Op == "ok", Options: last.opts}
			if step.Op == "fail" {
				result.Error = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
			}
			selector.OnResult(result)
			fmt.Fprintf(&out, "%s\n", step.Op)
		case "age":
			cache.mu.Lock()
			now := time.Now()
			var groups []sessionEntry
			for element := cache.evictionOrder.Front(); element != nil; element = element.Next() {
				if group, ok := cache.groups[element.Value.(string)]; ok {
					groups = append(groups, group)
				}
			}
			for _, group := range groups {
				cache.replaceAliasGroupsLocked(group.authID, now.Add(time.Duration(step.Value*float64(ttl))), group.aliases, group.binding, group)
			}
			cache.mu.Unlock()
			fmt.Fprintf(&out, "age %.2f\n", step.Value)
		case "capacity":
			cache.mu.Lock()
			cache.maxEntries = int(step.Value)
			cache.mu.Unlock()
			fmt.Fprintf(&out, "capacity %d\n", int(step.Value))
		case "show-capacity":
			cache.mu.RLock()
			fmt.Fprintf(&out, "default capacity %d\n", cache.maxEntries)
			cache.mu.RUnlock()
		case "age-ms":
			cache.mu.Lock()
			now := time.Now()
			var groups []sessionEntry
			for element := cache.evictionOrder.Front(); element != nil; element = element.Next() {
				if group, ok := cache.groups[element.Value.(string)]; ok {
					groups = append(groups, group)
				}
			}
			for _, group := range groups {
				cache.replaceAliasGroupsLocked(group.authID, now.Add(time.Duration(step.Value)*time.Millisecond), group.aliases, group.binding, group)
			}
			cache.mu.Unlock()
			fmt.Fprintf(&out, "age %.0fms\n", step.Value)
		default:
			fmt.Fprintf(&out, "unknown step %q\n", step.Op)
		}
		// The cache after each step: every group in eviction order, with its keys, binding and
		// the lifetime it has left in hundredths of the TTL.
		cache.mu.RLock()
		now := time.Now()
		for element := cache.evictionOrder.Front(); element != nil; element = element.Next() {
			group, ok := cache.groups[element.Value.(string)]
			if !ok {
				continue
			}
			left := math.Round(float64(group.expiresAt.Sub(now))/float64(ttl)*100) / 100
			fmt.Fprintf(&out, "  %s %s session=%s provider=%s seq=%d left=%.2f\n", group.authID, strings.Join(group.aliases, ","),
				group.binding.sessionID, group.binding.provider, group.binding.sequence, left)
		}
		fmt.Fprintf(&out, "  entries=%d\n", len(cache.entries))
		cache.mu.RUnlock()
	}
	return out.String()
}

// provimuxAffinityPinSamples: every way the selector names a conversation in its session cache.
// Explicit ids from each header and body field upstream reads (with parents, forks, subagents and
// the prompt-cache key with its conversation alias), the execution session, the derived identity
// and the first-message hashes of each request format; then what an entry does over time: reuse
// and refresh, a result, an expiry, a failure, eviction at capacity.
func provimuxAffinityPinSamples() []ProvimuxAffinityPinSample {
	const model = "Pin-Model(high)"
	pick := func(headers map[string]string, body string, metadata map[string]string) ProvimuxAffinityPinStep {
		return ProvimuxAffinityPinStep{Op: "pick", Model: model, Format: string(sdktranslator.FormatOpenAI), Headers: headers, Body: body, Metadata: metadata}
	}
	pickFormat := func(format sdktranslator.Format, body string) ProvimuxAffinityPinStep {
		return ProvimuxAffinityPinStep{Op: "pick", Model: model, Format: string(format), Body: body}
	}
	h := func(pairs ...string) map[string]string {
		out := map[string]string{}
		for i := 0; i+1 < len(pairs); i += 2 {
			out[pairs[i]] = pairs[i+1]
		}
		return out
	}
	chat := `{"model":"m","messages":[{"role":"system","content":"pin system"},{"role":"user","content":"pin first question"},{"role":"assistant","content":"pin answer"},{"role":"user","content":"pin second question"}]}`
	chatOne := `{"model":"m","messages":[{"role":"user","content":"pin only question"}]}`
	claudeBody := `{"model":"m","system":[{"type":"text","text":"pin system"}],"messages":[{"role":"user","content":"pin first question"},{"role":"assistant","content":"pin answer"},{"role":"user","content":"pin second question"}]}`
	geminiBody := `{"systemInstruction":{"parts":[{"text":"pin system"}]},"contents":[{"role":"user","parts":[{"text":"pin first question"}]},{"role":"model","parts":[{"text":"pin answer"}]},{"role":"user","parts":[{"text":"pin second question"}]}]}`
	responsesBody := `{"model":"m","instructions":"pin system","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"pin first question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pin answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"pin second question"}]}]}`
	claudeUserID := `{"model":"m","metadata":{"user_id":"{\"device_id\":\"pin-device\",\"account_uuid\":\"\",\"session_id\":\"pin-claude-meta\"}"},"messages":[{"role":"user","content":"pin"}]}`
	scope := map[string]string{cliproxyexecutor.CallerScopeMetadataKey: "pin-scope"}

	single := func(name, provider string, steps ...ProvimuxAffinityPinStep) ProvimuxAffinityPinSample {
		return ProvimuxAffinityPinSample{Name: name, Provider: provider, Steps: steps}
	}
	rule := func(name, provider string, steps ...ProvimuxAffinityPinStep) ProvimuxAffinityPinSample {
		return ProvimuxAffinityPinSample{Name: name, Provider: provider, Rule: true, Steps: steps}
	}
	twice := func(name, provider string, step ProvimuxAffinityPinStep) ProvimuxAffinityPinSample {
		return single(name, provider, step, ProvimuxAffinityPinStep{Op: "ok"}, step)
	}
	otherModel := ProvimuxAffinityPinStep{Op: "pick", Model: "pin-other-model", Format: string(sdktranslator.FormatOpenAI), Headers: h("X-Session-Id", "pin-generic"), Body: chatOne}
	s1 := pick(h("X-Session-Id", "pin-s1"), chatOne, nil)
	s2 := pick(h("X-Session-Id", "pin-s2"), chatOne, nil)
	s3 := pick(h("X-Session-Id", "pin-s3"), chatOne, nil)
	samples := []ProvimuxAffinityPinSample{
		twice("claude code header", "claude", pick(h("X-Claude-Code-Session-Id", "pin-cc"), chatOne, nil)),
		twice("claude code subagent", "claude", pick(h("X-Claude-Code-Session-Id", "pin-cc", "X-Claude-Code-Agent-Id", "pin-agent"), chatOne, nil)),
		twice("claude code subagent of a subagent", "claude", pick(h("X-Claude-Code-Session-Id", "pin-cc", "X-Claude-Code-Agent-Id", "pin-agent", "X-Claude-Code-Parent-Agent-Id", "pin-parent-agent"), chatOne, nil)),
		twice("claude metadata user_id", "claude", pick(nil, claudeUserID, nil)),
		twice("codex session", "codex", pick(h("Session-Id", "pin-codex"), chatOne, nil)),
		twice("codex session and thread", "codex", pick(h("Session-Id", "pin-codex", "Thread-Id", "pin-thread"), chatOne, nil)),
		twice("codex parent thread", "codex", pick(h("Session-Id", "pin-codex-child", "X-Codex-Parent-Thread-Id", "pin-codex"), chatOne, nil)),
		twice("codex fork", "codex", pick(h("Session-Id", "pin-codex-fork", "X-Codex-Turn-Metadata", `{"forked_from_thread_id":"pin-codex"}`), chatOne, nil)),
		twice("codex guardian subagent", "codex", pick(h("Session-Id", "pin-codex", "X-Openai-Subagent", "guardian"), chatOne, nil)),
		twice("antigravity session", "mixed", pick(h("X-Http-Session-Id", "pin-agy"), chatOne, nil)),
		twice("session header", "mixed", pick(h("X-Session-Id", "pin-generic"), chatOne, nil)),
		twice("session header with parent", "mixed", pick(h("X-Session-Id", "pin-child", "X-Parent-Session-Id", "pin-parent"), chatOne, nil)),
		twice("opencode affinity", "mixed", pick(h("X-Session-Affinity", "pin-opencode"), chatOne, nil)),
		twice("slot session", "mixed", pick(h("X-Slot-Session-Id", "pin-slot", "X-Parent-Slot-Session-Id", "pin-slot-parent"), chatOne, nil)),
		twice("task header", "mixed", pick(h("X-Task-Id", "pin-task", "X-Parent-Task-Id", "pin-task-parent"), chatOne, nil)),
		twice("conversation header", "mixed", pick(h("X-Conversation-Id", "pin-conversation"), chatOne, nil)),
		twice("thread header", "mixed", pick(h("X-Thread-Id", "pin-thread"), chatOne, nil)),
		twice("pi request id and prompt cache key", "mixed", pick(h("X-Client-Request-Id", "pin-pi"), `{"model":"m","prompt_cache_key":"pin-pi","messages":[{"role":"user","content":"pin"}]}`, nil)),
		twice("prompt cache key with conversation", "codex", pick(nil, `{"model":"m","prompt_cache_key":"pin-pck","conversation":{"id":"pin-conv"},"input":"pin"}`, nil)),
		twice("prompt cache key alone", "codex", pick(nil, `{"model":"m","prompt_cache_key":"pin-pck","input":"pin"}`, nil)),
		twice("body session id", "mixed", pick(nil, `{"model":"m","session_id":"pin-body-session","parent_session_id":"pin-body-parent","messages":[{"role":"user","content":"pin"}]}`, nil)),
		twice("body thread and conversation ids", "mixed", pick(nil, `{"model":"m","thread_id":"pin-body-thread","conversation_id":"pin-body-conversation","messages":[{"role":"user","content":"pin"}]}`, nil)),
		twice("body chat and task ids", "mixed", pick(nil, `{"model":"m","chat_id":"pin-chat","task_id":"pin-task","messages":[{"role":"user","content":"pin"}]}`, nil)),
		twice("gemini cached content", "mixed", ProvimuxAffinityPinStep{Op: "pick", Model: model, Format: string(sdktranslator.FormatGemini), Body: `{"cachedContent":"cachedContents/pin","contents":[{"role":"user","parts":[{"text":"pin"}]}]}`}),
		twice("execution session", "mixed", pick(nil, chatOne, map[string]string{cliproxyexecutor.ExecutionSessionMetadataKey: "pin-execution"})),
		twice("a long id is bounded", "mixed", pick(h("X-Session-Id", strings.Repeat("pin-long-", 28)[:250]), chatOne, nil)),
		twice("explicit id with a client key", "mixed", pick(h("X-Session-Id", "pin-scoped"), chatOne, scope)),
		twice("derived identity given", "mixed", pick(nil, chatOne, map[string]string{cliproxyexecutor.DerivedSessionIDMetadataKey: "pin-derived"})),
		twice("no id: chat messages", "mixed", pickFormat(sdktranslator.FormatOpenAI, chat)),
		twice("no id: one chat message", "mixed", pickFormat(sdktranslator.FormatOpenAI, chatOne)),
		twice("no id: claude messages", "claude", pickFormat(sdktranslator.FormatClaude, claudeBody)),
		twice("no id: gemini contents", "mixed", pickFormat(sdktranslator.FormatGemini, geminiBody)),
		twice("no id: responses input", "codex", pickFormat(sdktranslator.FormatOpenAIResponse, responsesBody)),
		twice("no id, nothing derived: chat message hashes", "mixed", ProvimuxAffinityPinStep{Op: "pick-raw", Model: model, Body: chat}),
		twice("no id, nothing derived: claude message hashes", "claude", ProvimuxAffinityPinStep{Op: "pick-raw", Model: model, Body: claudeBody}),
		twice("no id, nothing derived: gemini message hashes", "mixed", ProvimuxAffinityPinStep{Op: "pick-raw", Model: model, Body: geminiBody}),
		twice("no id, nothing derived: responses message hashes", "codex", ProvimuxAffinityPinStep{Op: "pick-raw", Model: model, Body: responsesBody}),
		twice("no id, nothing derived: one message hash", "mixed", ProvimuxAffinityPinStep{Op: "pick-raw", Model: model, Body: chatOne}),
		rule("model key", "mixed", otherModel, ProvimuxAffinityPinStep{Op: "ok"}, otherModel),
		rule("recency: reuse and a result reorder the cache", "mixed", s1, s2, s3, s1, ProvimuxAffinityPinStep{Op: "ok"}, s2, ProvimuxAffinityPinStep{Op: "ok"}),
		rule("refresh: a hit and a result renew the lifetime", "mixed", s1, ProvimuxAffinityPinStep{Op: "age", Value: 0.5}, s1,
			ProvimuxAffinityPinStep{Op: "age", Value: 0.25}, ProvimuxAffinityPinStep{Op: "ok"}),
		rule("expiry: an entry past its lifetime is not used", "mixed", s1, s2, s3, ProvimuxAffinityPinStep{Op: "age", Value: -0.01}, s1, s1, s2),
		rule("expiry: an entry one millisecond past its lifetime is not used", "mixed", s1, s2, s3, ProvimuxAffinityPinStep{Op: "age-ms", Value: -1}, s1, s1, s2),
		rule("capacity: the default limit", "mixed", ProvimuxAffinityPinStep{Op: "show-capacity"}),
		rule("failure: a 429 releases the binding", "mixed", s1, ProvimuxAffinityPinStep{Op: "fail"}, s1, s1),
		rule("capacity: the oldest group is evicted", "mixed", ProvimuxAffinityPinStep{Op: "capacity", Value: 2}, s1, s2, s1, s3, s2),
	}
	// Unusual ids for every kind of key: case, surrounding space, a control character, longer than
	// upstream accepts, just under its bound, non-ASCII, empty after trimming.
	jsonString := func(value string) string {
		data, _ := json.Marshal(value)
		return string(data)
	}
	kinds := []struct {
		name, provider string
		step           func(id string) ProvimuxAffinityPinStep
	}{
		{"claude code header", "claude", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Claude-Code-Session-Id", id), chatOne, nil) }},
		{"claude metadata user_id", "claude", func(id string) ProvimuxAffinityPinStep {
			userID, _ := json.Marshal(map[string]string{"device_id": "pin-device", "account_uuid": "", "session_id": id})
			return pick(nil, `{"model":"m","metadata":{"user_id":`+jsonString(string(userID))+`},"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"metadata user_id", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","metadata":{"user_id":`+jsonString(id)+`},"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"codex session", "codex", func(id string) ProvimuxAffinityPinStep { return pick(h("Session-Id", id), chatOne, nil) }},
		{"codex fork", "codex", func(id string) ProvimuxAffinityPinStep {
			return pick(h("Session-Id", "pin-codex-fork", "X-Codex-Turn-Metadata", `{"forked_from_thread_id":`+jsonString(id)+`}`), chatOne, nil)
		}},
		{"antigravity session", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Http-Session-Id", id), chatOne, nil) }},
		{"session header", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Session-Id", id), chatOne, nil) }},
		{"parent session header", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(h("X-Session-Id", "pin-child", "X-Parent-Session-Id", id), chatOne, nil)
		}},
		{"opencode affinity", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Session-Affinity", id), chatOne, nil) }},
		{"slot session", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Slot-Session-Id", id), chatOne, nil) }},
		{"task header", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Task-Id", id), chatOne, nil) }},
		{"conversation header", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Conversation-Id", id), chatOne, nil) }},
		{"thread header", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Thread-Id", id), chatOne, nil) }},
		{"pi request id", "mixed", func(id string) ProvimuxAffinityPinStep { return pick(h("X-Client-Request-Id", id), chatOne, nil) }},
		{"prompt cache key", "codex", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","prompt_cache_key":`+jsonString(id)+`,"input":"pin"}`, nil)
		}},
		{"conversation of a prompt cache key", "codex", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","prompt_cache_key":"pin-pck","conversation":{"id":`+jsonString(id)+`},"input":"pin"}`, nil)
		}},
		{"body session id", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","session_id":`+jsonString(id)+`,"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"body thread id", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","thread_id":`+jsonString(id)+`,"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"body conversation id", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","conversation_id":`+jsonString(id)+`,"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"body chat id", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","chat_id":`+jsonString(id)+`,"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"body task id", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, `{"model":"m","task_id":`+jsonString(id)+`,"messages":[{"role":"user","content":"pin"}]}`, nil)
		}},
		{"gemini cached content", "mixed", func(id string) ProvimuxAffinityPinStep {
			return ProvimuxAffinityPinStep{Op: "pick", Model: model, Format: string(sdktranslator.FormatGemini), Body: `{"cachedContent":` + jsonString(id) + `,"contents":[{"role":"user","parts":[{"text":"pin"}]}]}`}
		}},
		{"execution session", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, chatOne, map[string]string{cliproxyexecutor.ExecutionSessionMetadataKey: id})
		}},
		{"derived identity given", "mixed", func(id string) ProvimuxAffinityPinStep {
			return pick(nil, chatOne, map[string]string{cliproxyexecutor.DerivedSessionIDMetadataKey: id})
		}},
	}
	variants := []struct{ name, id string }{
		{"mixed case", "Pin-Mixed-CASE-Id"},
		{"surrounding space", "  pin-Spaced-id  "},
		{"a control character", "pin-tab\tid"},
		{"longer than accepted", strings.Repeat("Pin-Long-", 40)},
		{"just under the bound", strings.Repeat("Pin-Bound-", 26)[:250]},
		{"non-ASCII", "pin-Ünïcødé-会话-ид"},
		{"empty after trimming", " \t "},
	}
	for _, kind := range kinds {
		for _, variant := range variants {
			samples = append(samples, twice(kind.name+": "+variant.name, kind.provider, kind.step(variant.id)))
		}
	}
	return samples
}

// ProvimuxAccountIdentity returns the identity of the account behind an auth as its vendor names it, or false when
// the gateway does not hold one (its bindings are then neither written nor restored). It is the account marker's
// rule (provimux_account_marker.go, answered-by.md 2.2): exactly the credentials whose marker begins with "a." have
// one. Those whose marker begins with "s." (a Kimi login, an AI Studio connection, a credential a plugin made or
// held only in memory, a Claude login without an organisation id or with an account id the gateway made up, a
// Codex login without a user, a login missing the fields its vendor's rule reads) have none: their identity would
// be the login file itself, which another account's login can take.
func ProvimuxAccountIdentity(auth *Auth) (string, bool) {
	basis, identity := provimuxAccountIdentity(auth)
	if basis != provimuxBasisAccount || len(identity) == 0 {
		return "", false
	}
	return string(identity), true
}
