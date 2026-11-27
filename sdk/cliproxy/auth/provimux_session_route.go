package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"weak"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: session-route (.github/provimux/design/session-route.md 3 and 4). The book is a note about upstream's
// session bindings, never their source: every reader asks upstream's cache first (through the seam). A client key sees
// and releases only a conversation the book has seen that key use since the gateway started (decided by the lead on
// 2026-11-27). Releasing removes the binding from upstream's cache and nothing else: nothing is sent, no account's
// state changes. The automatic return (the rule) is not in this release. This file calls the seam and exported names
// only.

// provimuxSessionBookLimit is the number of conversations the book keeps; the least recently answered go first.
const provimuxSessionBookLimit = 4096

// provimuxSessionScopeLimit is the number of client keys (caller scopes) kept per conversation.
const provimuxSessionScopeLimit = 4

// provimuxSessionRouteClock is the clock of session-route: the book's times, the show's "now" and the availability it
// judges. Tests set it (SetProvimuxSessionRouteClockForTest); nothing here sleeps or waits for it.
var provimuxSessionRouteClock atomic.Pointer[func() time.Time]

// provimuxSessionRouteNow is the one clock of session-route.
func provimuxSessionRouteNow() time.Time {
	if clock := provimuxSessionRouteClock.Load(); clock != nil {
		return (*clock)()
	}
	return time.Now()
}

// SetProvimuxSessionRouteClockForTest replaces session-route's clock and returns the function that puts the previous
// one back. For tests only.
func SetProvimuxSessionRouteClockForTest(clock func() time.Time) (restore func()) {
	previous := provimuxSessionRouteClock.Load()
	if clock == nil {
		provimuxSessionRouteClock.Store(nil)
	} else {
		provimuxSessionRouteClock.Store(&clock)
	}
	return func() { provimuxSessionRouteClock.Store(previous) }
}

// ProvimuxRoute is one account that serves a name, exactly as a row of GET /v1/models?include=accounts gives it; or,
// for an account that list does not show now (disabled, or no longer serving the name), only {"listed": false}.
type ProvimuxRoute struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
	Host     string `json:"host,omitempty"`
	Account  string `json:"account,omitempty"`
	// Unlisted: the list does not show this account now; nothing else of it is given.
	Unlisted bool `json:"-"`
}

// MarshalJSON writes a listed route as the list's row and an unlisted one as {"listed":false}.
func (r ProvimuxRoute) MarshalJSON() ([]byte, error) {
	if r.Unlisted {
		return []byte(`{"listed":false}`), nil
	}
	type row ProvimuxRoute
	return json.Marshal(row(r))
}

// UnmarshalJSON reads either form.
func (r *ProvimuxRoute) UnmarshalJSON(data []byte) error {
	var probe struct {
		Listed *bool `json:"listed"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.Listed != nil && !*probe.Listed {
		*r = ProvimuxRoute{Unlisted: true}
		return nil
	}
	type row ProvimuxRoute
	var value row
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*r = ProvimuxRoute(value)
	return nil
}

// provimuxListed reports whether GET /v1/models?include=accounts shows auth for the model named model now: the list
// gives every enabled credential under each model ID the registry holds for it.
func provimuxListed(auth *Auth, model string) bool {
	if auth == nil || auth.Disabled {
		return false
	}
	model = strings.TrimSpace(model)
	for _, info := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if info != nil && info.ID == model {
			return true
		}
	}
	return false
}

// provimuxRouteOf is auth's row for model: the fields of include=accounts and nothing else, or {"listed":false}.
func provimuxRouteOf(auth *Auth, model string) ProvimuxRoute {
	if !provimuxListed(auth, model) {
		return ProvimuxRoute{Unlisted: true}
	}
	return ProvimuxRoute{Provider: auth.Provider, Kind: auth.AuthKind(), Priority: auth.Priority(), Host: auth.BaseURLHost(), Account: auth.ProvimuxAccountMarker()}
}

// provimuxRoutesOf are the listed rows of auths for model, in the order of include=accounts: highest priority first.
func provimuxRoutesOf(auths []*Auth, model string) []ProvimuxRoute {
	out := make([]ProvimuxRoute, 0, len(auths))
	for _, auth := range auths {
		if route := provimuxRouteOf(auth, model); !route.Unlisted {
			out = append(out, route)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Account < out[j].Account
	})
	return out
}

// ProvimuxSessionBound is SessionRoute.bound.
type ProvimuxSessionBound struct {
	Route        ProvimuxRoute  `json:"route"`
	Since        *string        `json:"since"`
	Origin       string         `json:"origin"`
	FellBackFrom *ProvimuxRoute `json:"fell_back_from"`
	FellBackAt   *string        `json:"fell_back_at"`
}

// ProvimuxSessionPosition is SessionRoute.position. HigherPriorityFreeAt was added by the lead on 2026-11-27: when a
// higher-priority route is expected free, one time for the name, never per account.
type ProvimuxSessionPosition struct {
	BelowFirst           bool    `json:"below_first"`
	HigherPriorityFree   bool    `json:"higher_priority_free"`
	HigherPriorityFreeAt *string `json:"higher_priority_free_at"`
}

// ProvimuxSessionRule is SessionRoute.rule (design section 15): whether the rule runs, and the operator's time. The
// rule is not in this release: never in effect, no time.
type ProvimuxSessionRule struct {
	InEffect   bool     `json:"in_effect"`
	ColdAfterS *float64 `json:"cold_after_s"`
}

// ProvimuxSessionRoute is the answer of POST /v1/provimux/session-route (SessionRoute in session-route/openapi.yaml).
type ProvimuxSessionRoute struct {
	ObservedAt       string                   `json:"observed_at"`
	Affinity         string                   `json:"affinity"`
	Conversation     string                   `json:"conversation"`
	Model            string                   `json:"model"`
	RoutedModel      *string                  `json:"routed_model"`
	Binding          *string                  `json:"binding"`
	Bound            *ProvimuxSessionBound    `json:"bound"`
	Position         *ProvimuxSessionPosition `json:"position"`
	First            []ProvimuxRoute          `json:"first"`
	Rule             ProvimuxSessionRule      `json:"rule"`
	WebsocketMayHold bool                     `json:"websocket_may_hold"`
	Subagent         bool                     `json:"subagent"`
}

// ProvimuxSessionRelease is the answer of POST /v1/provimux/session-route/release (Release in the contract).
type ProvimuxSessionRelease struct {
	ObservedAt       string          `json:"observed_at"`
	Affinity         string          `json:"affinity"`
	Released         bool            `json:"released"`
	Conversation     string          `json:"conversation"`
	Model            string          `json:"model"`
	RoutedModel      *string         `json:"routed_model"`
	Was              *ProvimuxRoute  `json:"was"`
	Next             []ProvimuxRoute `json:"next"`
	WebsocketMayHold bool            `json:"websocket_may_hold"`
}

// provimuxConversation is what owns a book entry: the binding key's parts as received, kept apart. Upstream joins
// them into one string with a delimiter a session may hold; the book never does (session-route G1, finding 1).
type provimuxConversation struct {
	Provider string
	Session  string
	ModelKey string
}

// ProvimuxSessionBookEntry is what the book knows of one conversation for one name. Readers get copies.
type ProvimuxSessionBookEntry struct {
	Provider    string    // the provider part of the binding key ("mixed" for a name served by several providers)
	Session     string    // the bounded session identity
	ModelKey    string    // the name without a thinking suffix
	Key         string    // upstream's key string, only ever handed to upstream's cache
	RouteModel  string    // the name as Pick received it
	Parent      string    // the parent conversation, for a sub-agent
	Socket      bool      // a turn of it came on a connection pinned to its login (a Codex WebSocket)
	Current     string    // the account the book last saw answer under the binding; "" when it has none
	Since       time.Time // when Current first answered in this stretch
	Previous    string    // the account that answered before Current, in this stretch
	PreviousAt  time.Time // when Previous stopped
	LastSuccess time.Time // the last successful answer under the binding
	Scopes      []string  // the caller scopes seen using it (at most provimuxSessionScopeLimit)
	LastTouched time.Time // the last answer of any account for it
	TTL         time.Duration
	PruneAt     time.Time // from then on the entry leaves once upstream holds no binding for it
	// Life numbers the binding the book holds now (design section 15.11): made at the answer after which the book
	// first holds it, dropped at its removal by anyone the book can see (a release, a failure upstream removes it
	// for, a new account or cache instance, the account registered again). 0: the book holds no binding of it.
	Life      uint64
	lifeCache weak.Pointer[SessionCache]
}

func (e *ProvimuxSessionBookEntry) owner() provimuxConversation {
	return provimuxConversation{Provider: e.Provider, Session: e.Session, ModelKey: e.ModelKey}
}

func (e *ProvimuxSessionBookEntry) hasScope(scope string) bool {
	if scope == "" {
		return false
	}
	for _, s := range e.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func (e *ProvimuxSessionBookEntry) addScope(scope string) {
	if scope == "" || e.hasScope(scope) {
		return
	}
	if len(e.Scopes) >= provimuxSessionScopeLimit {
		// The oldest key loses sight of it: the safe side of "who may".
		e.Scopes = append(e.Scopes[:0], e.Scopes[1:]...)
	}
	e.Scopes = append(e.Scopes, scope)
}

// clearBinding forgets the binding and its stretch: after a release, the next binding is a new conversation's.
func (e *ProvimuxSessionBookEntry) clearBinding() {
	e.Current, e.Since, e.Previous, e.PreviousAt, e.LastSuccess = "", time.Time{}, "", time.Time{}, time.Time{}
	e.endLife()
}

// endLife ends the binding's life only: which account answered, and since when, stays history (a fallback that
// follows is still shown as one).
func (e *ProvimuxSessionBookEntry) endLife() {
	e.Life, e.lifeCache = 0, weak.Pointer[SessionCache]{}
}

// ProvimuxSessionBook is the book of one manager. It observes the manager's results through AddHook while
// subscribed, never takes part in a selection, and holds its manager weakly: a manager nothing else holds is not kept
// alive by its book.
type ProvimuxSessionBook struct {
	NoopHook
	manager weak.Pointer[Manager]

	mu          sync.Mutex
	entries     map[provimuxConversation]*ProvimuxSessionBookEntry
	nextPrune   time.Time
	salt        [32]byte
	unsubscribe func()
	subscribed  atomic.Bool
	lives       uint64 // the last life number given; under mu

	releasedByClient atomic.Int64
}

// NewProvimuxSessionBook makes an empty book for m. It observes nothing until Subscribe.
func NewProvimuxSessionBook(m *Manager) *ProvimuxSessionBook {
	b := &ProvimuxSessionBook{entries: make(map[provimuxConversation]*ProvimuxSessionBookEntry)}
	if m != nil {
		b.manager = weak.Make(m)
	}
	// crypto/rand.Read never returns an error: it fills the buffer or ends the process.
	_, _ = rand.Read(b.salt[:])
	return b
}

// Manager is the manager the book observes, or nil once nothing else holds it.
func (b *ProvimuxSessionBook) Manager() *Manager {
	if b == nil {
		return nil
	}
	return b.manager.Value()
}

// Subscribe adds the book to its manager's result hooks. Idempotent.
func (b *ProvimuxSessionBook) Subscribe() {
	m := b.Manager()
	if m == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unsubscribe == nil {
		b.unsubscribe = m.AddHook(b)
		b.subscribed.Store(true)
	}
}

// Unsubscribe removes the book from its manager's hooks and forgets every entry: nothing is observed or kept.
func (b *ProvimuxSessionBook) Unsubscribe() {
	if b == nil {
		return
	}
	b.mu.Lock()
	unsubscribe := b.unsubscribe
	b.unsubscribe = nil
	b.subscribed.Store(false)
	b.entries = make(map[provimuxConversation]*ProvimuxSessionBookEntry)
	b.nextPrune = time.Time{}
	b.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

// Subscribed reports whether the book observes its manager's results now.
func (b *ProvimuxSessionBook) Subscribed() bool { return b != nil && b.subscribed.Load() }

// ReleasedByClient is the number of bindings client keys released since the book was made.
func (b *ProvimuxSessionBook) ReleasedByClient() int64 {
	if b == nil {
		return 0
	}
	return b.releasedByClient.Load()
}

// OnResult notes an answer (session-route.md 3). A successful one: the caller scope that used the conversation, and,
// when upstream's cache holds the result's key bound to the account that answered, for that session, that account;
// a binding the book did not hold before starts a life. A result whose key the cache does not hold for its account
// (an answer in flight at a release, a transient failover that kept the binding, a stale result) notes the caller
// only, never a binding. A failed one for which upstream removes the binding right after the hooks ends that
// binding's life. Where session affinity does not run, the book stops observing and forgets what it held.
func (b *ProvimuxSessionBook) OnResult(_ context.Context, res Result) {
	if b == nil {
		return
	}
	affinity, mode := provimuxSeamAffinity(b.Manager())
	if mode != provimuxAffinityOn {
		b.Unsubscribe()
		return
	}
	if res.AuthID == "" {
		return
	}
	key, ok := provimuxSeamResultKey(res)
	if !ok {
		return
	}
	owner := provimuxConversation{Provider: key.Provider, Session: key.Session, ModelKey: key.ModelKey}
	binding, bound := provimuxSeamLookup(affinity, key.Key)
	holds := bound && binding.AuthID == res.AuthID && binding.Session == key.Session
	if !res.Success {
		if holds && provimuxSeamFailureRemoves(affinity, res) {
			b.mu.Lock()
			if entry := b.entries[owner]; entry != nil && entry.Current == res.AuthID {
				entry.endLife()
			}
			b.mu.Unlock()
		}
		return
	}
	scope := ""
	if res.Options.Metadata != nil {
		scope, _ = res.Options.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
		scope = strings.TrimSpace(scope)
	}
	if !holds && scope == "" {
		return
	}
	ttl := provimuxSeamTTL(affinity)
	now := provimuxSessionRouteNow()

	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(affinity, now)
	entry := b.entries[owner]
	if entry == nil {
		b.evictLocked()
		entry = &ProvimuxSessionBookEntry{Provider: key.Provider, Session: key.Session, ModelKey: key.ModelKey, Key: key.Key, RouteModel: key.RouteModel}
		b.entries[owner] = entry
	}
	entry.LastTouched = now
	entry.TTL = ttl
	entry.PruneAt = now.Add(ttl)
	if b.nextPrune.IsZero() || entry.PruneAt.Before(b.nextPrune) {
		b.nextPrune = entry.PruneAt
	}
	entry.addScope(scope)
	if key.Parent != "" {
		entry.Parent = key.Parent
	}
	if key.Socket {
		entry.Socket = true
	}
	if key.RouteModel != "" {
		entry.RouteModel = key.RouteModel
	}
	if !holds {
		return
	}
	switch {
	case entry.Current == "":
		entry.Current, entry.Since, entry.Previous, entry.PreviousAt = res.AuthID, now, "", time.Time{}
		b.newLifeLocked(entry, binding.Cache)
	case entry.Current != res.AuthID:
		entry.Previous, entry.PreviousAt = entry.Current, now
		entry.Current, entry.Since = res.AuthID, now
		b.newLifeLocked(entry, binding.Cache)
	case entry.Life == 0 || entry.lifeCache.Value() != binding.Cache:
		// Removed (by upstream, or by a new cache when the routing settings changed) and made again on the same
		// account: another life.
		entry.Since, entry.Previous, entry.PreviousAt = now, "", time.Time{}
		b.newLifeLocked(entry, binding.Cache)
	case !entry.LastSuccess.IsZero() && !now.Before(entry.LastSuccess.Add(ttl)):
		// The binding ended by its own lifetime and was made again on the same account: a new stretch for since,
		// the same life for expect (design section 15.11).
		entry.Since, entry.Previous, entry.PreviousAt = now, "", time.Time{}
	}
	entry.LastSuccess = now
}

// newLifeLocked gives entry a new life number, in cache.
func (b *ProvimuxSessionBook) newLifeLocked(entry *ProvimuxSessionBookEntry, cache *SessionCache) {
	b.lives++
	entry.Life = b.lives
	entry.lifeCache = weak.Make(cache)
}

// OnAuthRegistered ends the life of every binding the book holds on an account registered again: upstream drops the
// bindings of a removed account (Manager.Remove, InvalidateAuth), and one made after the account came back is another.
func (b *ProvimuxSessionBook) OnAuthRegistered(_ context.Context, auth *Auth) {
	if b == nil || auth == nil || auth.ID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.entries {
		if entry.Current == auth.ID {
			entry.endLife()
		}
	}
}

// pruneLocked drops the entries whose binding expired: once their time is past (their last answer plus the cache's
// TTL) and upstream's cache holds no binding for them. An entry whose binding upstream still holds (others refresh
// it) gets another TTL. It runs only when the earliest such time is past, so it costs nothing in between.
func (b *ProvimuxSessionBook) pruneLocked(affinity *SessionAffinitySelector, now time.Time) {
	if b.nextPrune.IsZero() || now.Before(b.nextPrune) {
		return
	}
	next := time.Time{}
	for owner, entry := range b.entries {
		if now.Before(entry.PruneAt) {
			if next.IsZero() || entry.PruneAt.Before(next) {
				next = entry.PruneAt
			}
			continue
		}
		if binding, ok := provimuxSeamLookup(affinity, entry.Key); ok && binding.Session == entry.Session {
			entry.PruneAt = now.Add(entry.TTL)
			if next.IsZero() || entry.PruneAt.Before(next) {
				next = entry.PruneAt
			}
			continue
		}
		delete(b.entries, owner)
	}
	b.nextPrune = next
}

// evictLocked drops the least recently answered entry when the book is full.
func (b *ProvimuxSessionBook) evictLocked() {
	if len(b.entries) < provimuxSessionBookLimit {
		return
	}
	var oldestOwner provimuxConversation
	var oldest time.Time
	found := false
	for owner, entry := range b.entries {
		if !found || entry.LastTouched.Before(oldest) {
			oldestOwner, oldest, found = owner, entry.LastTouched, true
		}
	}
	delete(b.entries, oldestOwner)
}

// Entries returns a copy of every entry, in a stable order: for tests.
func (b *ProvimuxSessionBook) Entries() []ProvimuxSessionBookEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ProvimuxSessionBookEntry, 0, len(b.entries))
	for _, entry := range b.entries {
		copied := *entry
		copied.Scopes = append([]string(nil), entry.Scopes...)
		out = append(out, copied)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		if out[i].ModelKey != out[j].ModelKey {
			return out[i].ModelKey < out[j].ModelKey
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// provimuxResolved is a conversation as a client key names it: whether the key was seen using it, the book's entry,
// and upstream's binding now.
type provimuxResolved struct {
	affinity   *SessionAffinitySelector
	mode       string
	seen       bool
	entry      ProvimuxSessionBookEntry
	bound      bool
	binding    provimuxSeamBinding
	auth       *Auth
	clientType string
}

// resolve finds the conversation named by headers and body for the routed name, among those scope was seen using.
// The book's entries are found by the conversation's parts, never by upstream's joined string; the binding found is
// shown only while it belongs to that conversation (upstream's binding was last chosen for that session).
func (b *ProvimuxSessionBook) resolve(scope string, headers http.Header, body []byte, routed string) provimuxResolved {
	var r provimuxResolved
	m := b.Manager()
	r.affinity, r.mode = provimuxSeamAffinity(m)
	if r.mode != provimuxAffinityOn {
		return r
	}
	session, clientType := provimuxSeamNamedSession(headers, body)
	modelKey := provimuxSeamModelKey(routed)
	scope = strings.TrimSpace(scope)
	if session == "" || modelKey == "" || scope == "" {
		return r
	}
	r.clientType = clientType
	var candidates []ProvimuxSessionBookEntry
	b.mu.Lock()
	b.pruneLocked(r.affinity, provimuxSessionRouteNow())
	for owner, entry := range b.entries {
		if owner.Session == session && owner.ModelKey == modelKey && entry.hasScope(scope) {
			copied := *entry
			copied.Scopes = nil
			candidates = append(candidates, copied)
		}
	}
	b.mu.Unlock()
	if len(candidates) == 0 {
		return r
	}
	r.seen = true
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].LastTouched.Equal(candidates[j].LastTouched) {
			return candidates[i].LastTouched.After(candidates[j].LastTouched)
		}
		return candidates[i].Provider < candidates[j].Provider
	})
	r.entry = candidates[0]
	for _, candidate := range candidates {
		binding, ok := provimuxSeamLookup(r.affinity, candidate.Key)
		if !ok || binding.Session != candidate.Session {
			continue
		}
		auth, okAuth := m.GetByID(binding.AuthID)
		if !okAuth || auth == nil {
			// An account no longer registered: upstream drops such a binding itself at the next request.
			continue
		}
		r.entry, r.bound, r.binding, r.auth = candidate, true, binding, auth
		break
	}
	return r
}

// lifeToken is the token for expect: it names one life of one conversation's binding (design section 15.11), and
// nothing else: not the account, not upstream's sequence.
func (b *ProvimuxSessionBook) lifeToken(owner provimuxConversation, life uint64) string {
	h := sha256.New()
	h.Write(b.salt[:])
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], life)
	h.Write(number[:])
	for _, part := range []string{owner.Provider, owner.Session, owner.ModelKey} {
		binary.BigEndian.PutUint64(number[:], uint64(len(part)))
		h.Write(number[:])
		h.Write([]byte(part))
	}
	return "b." + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil)))[:16]
}

// heldLife is the life the book holds for the binding found, or 0 where the book does not hold it (no answer seen on
// it yet).
func heldLife(entry ProvimuxSessionBookEntry, binding provimuxSeamBinding) uint64 {
	if entry.Life == 0 || entry.Current != binding.AuthID || entry.lifeCache.Value() != binding.Cache {
		return 0
	}
	return entry.Life
}

// ProvimuxSessionModelName is the name as the answers give it: the name, without a thinking suffix.
func ProvimuxSessionModelName(model string) string {
	model = strings.TrimSpace(model)
	if parsed := thinking.ParseSuffix(model); strings.TrimSpace(parsed.ModelName) != "" {
		return strings.TrimSpace(parsed.ModelName)
	}
	return model
}

func provimuxTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func provimuxTimePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := provimuxTime(t)
	return &s
}

// provimuxRoutedName is routed_model: the routed name when the request path routes the asked name to another, else
// nil.
func provimuxRoutedName(asked, routed string) *string {
	asked, routed = ProvimuxSessionModelName(asked), ProvimuxSessionModelName(routed)
	if routed == "" || routed == asked {
		return nil
	}
	return &routed
}

// Show answers POST /v1/provimux/session-route for a name the request path routes as it is.
func (b *ProvimuxSessionBook) Show(scope string, headers http.Header, body []byte, model string) ProvimuxSessionRoute {
	return b.ShowRouted(scope, headers, body, model, model)
}

// ShowRouted answers POST /v1/provimux/session-route for a client key's caller scope: asked is the name the client
// sent, routed the name the request path routes it to (auto resolved). It changes nothing: no binding is made, moved
// or refreshed. Every call answers from the state at that moment.
func (b *ProvimuxSessionBook) ShowRouted(scope string, headers http.Header, body []byte, asked, routed string) ProvimuxSessionRoute {
	r := b.resolve(scope, headers, body, routed)
	return b.show(r, asked, routed, provimuxSessionRouteNow())
}

func (b *ProvimuxSessionBook) show(r provimuxResolved, asked, routed string, now time.Time) ProvimuxSessionRoute {
	out := ProvimuxSessionRoute{
		ObservedAt:   provimuxTime(now),
		Affinity:     r.mode,
		Conversation: "not-seen",
		Model:        ProvimuxSessionModelName(asked),
		RoutedModel:  provimuxRoutedName(asked, routed),
		First:        []ProvimuxRoute{},
		Rule:         ProvimuxSessionRule{InEffect: false, ColdAfterS: nil},
	}
	if r.mode != provimuxAffinityOn {
		out.Conversation = "not-bound"
		return out
	}
	if !r.seen {
		return out
	}
	out.Subagent = r.entry.Parent != ""
	out.WebsocketMayHold = r.entry.Socket || r.clientType == "codex"
	if !r.bound {
		out.Conversation = "not-bound"
		return out
	}
	m := b.Manager()
	name := ProvimuxSessionModelName(r.entry.RouteModel)
	out.Conversation = "bound"
	if life := heldLife(r.entry, r.binding); life != 0 {
		token := b.lifeToken(r.entry.owner(), life)
		out.Binding = &token
	}

	route := provimuxRouteOf(r.auth, name)
	bound := &ProvimuxSessionBound{Route: route, Origin: "not-known"}
	if r.entry.Current == r.binding.AuthID && !r.entry.Since.IsZero() {
		bound.Since = provimuxTimePtr(r.entry.Since)
		bound.Origin = "first"
		if r.entry.Previous != "" {
			bound.Origin = "fallback"
			bound.FellBackAt = provimuxTimePtr(r.entry.PreviousAt)
			previous, _ := m.GetByID(r.entry.Previous)
			fellBackFrom := provimuxRouteOf(previous, name)
			bound.FellBackFrom = &fellBackFrom
		}
	}
	out.Bound = bound

	_, available := provimuxSeamRoutes(m, r.entry.Provider, r.entry.RouteModel, now)
	out.First = provimuxRoutesOf(provimuxSeamHighest(available), name)
	if !route.Unlisted {
		position := b.position(r, name, now)
		out.Position = &position
	}
	return out
}

// position judges the binding against the accounts the list shows for its name now (session-route.md 6.1): whether
// one of a higher priority than the bound one serves it, whether such a one is free, and, when none is, when the first
// recorded cooldown among them ends. Facts about the name, never per account.
func (b *ProvimuxSessionBook) position(r provimuxResolved, name string, now time.Time) ProvimuxSessionPosition {
	m := b.Manager()
	serving, available := provimuxSeamRoutes(m, r.entry.Provider, r.entry.RouteModel, now)
	boundPriority := r.auth.Priority()
	var position ProvimuxSessionPosition
	for _, auth := range serving {
		if auth.Priority() > boundPriority && provimuxListed(auth, name) {
			position.BelowFirst = true
		}
	}
	for _, auth := range available {
		if auth.Priority() > boundPriority && provimuxListed(auth, name) {
			position.HigherPriorityFree = true
		}
	}
	if !position.HigherPriorityFree {
		var earliest time.Time
		for _, auth := range serving {
			if auth.Priority() <= boundPriority || !provimuxListed(auth, name) {
				continue
			}
			view, ok := m.ProvimuxRouteCooldowns(auth, []string{r.entry.RouteModel}, now)[r.entry.RouteModel]
			if !ok || !view.RetryAt.After(now) {
				continue
			}
			if earliest.IsZero() || view.RetryAt.Before(earliest) {
				earliest = view.RetryAt
			}
		}
		position.HigherPriorityFreeAt = provimuxTimePtr(earliest)
	}
	return position
}

// Release answers POST /v1/provimux/session-route/release for a name the request path routes as it is.
func (b *ProvimuxSessionBook) Release(scope string, headers http.Header, body []byte, model, expect string) (ProvimuxSessionRelease, *ProvimuxSessionRoute) {
	return b.ReleaseRouted(scope, headers, body, model, model, expect)
}

// ReleaseRouted answers POST /v1/provimux/session-route/release for a client key's caller scope. Without expect it
// removes whatever is bound for the conversation. With expect (any non-empty string: the handler refuses an empty
// one), it removes the binding only while the life the token names lasts (design section 15.11): the life is checked
// and the binding removed under the book's lock, in that order, so no answer can start a new life between them;
// otherwise nothing is removed and conflict is the state now (409). A binding that is gone answers released: false.
// It removes the binding and every alias of it from upstream's cache, and nothing else.
func (b *ProvimuxSessionBook) ReleaseRouted(scope string, headers http.Header, body []byte, asked, routed, expect string) (ProvimuxSessionRelease, *ProvimuxSessionRoute) {
	r := b.resolve(scope, headers, body, routed)
	now := provimuxSessionRouteNow()
	out := ProvimuxSessionRelease{ObservedAt: provimuxTime(now), Affinity: r.mode, Conversation: "not-bound",
		Model: ProvimuxSessionModelName(asked), RoutedModel: provimuxRoutedName(asked, routed), Next: []ProvimuxRoute{}}
	if r.mode != provimuxAffinityOn {
		return out, nil
	}
	if !r.seen {
		out.Conversation = "not-seen"
		return out, nil
	}
	out.WebsocketMayHold = r.entry.Socket || r.clientType == "codex"
	if !r.bound {
		return out, nil
	}
	if hook := provimuxSeamBeforeRemove; hook != nil {
		hook()
	}
	owner := r.entry.owner()
	var removedAuth string
	conflict := false
	b.mu.Lock()
	entry := b.entries[owner]
	if expect != "" {
		if entry == nil || entry.Life == 0 || b.lifeToken(owner, entry.Life) != expect {
			conflict = true
		}
	}
	if !conflict && entry != nil {
		held, heldCache := entry.Current, entry.lifeCache.Value()
		still := func(binding provimuxSeamBinding) bool {
			if binding.Session != owner.Session {
				return false
			}
			if expect != "" && (binding.AuthID != held || binding.Cache != heldCache) {
				return false
			}
			removedAuth = binding.AuthID
			return true
		}
		if provimuxSeamRelease(r.affinity, entry.Key, still) {
			entry.clearBinding()
		} else if expect != "" {
			if _, present := provimuxSeamLookup(r.affinity, entry.Key); present {
				conflict = true // bound now, but not in the life the token names
			}
		}
	}
	b.mu.Unlock()
	if conflict {
		state := b.show(b.resolve(scope, headers, body, routed), asked, routed, provimuxSessionRouteNow())
		return ProvimuxSessionRelease{}, &state
	}
	if removedAuth == "" {
		return out, nil
	}
	b.releasedByClient.Add(1)
	m := b.Manager()
	name := ProvimuxSessionModelName(r.entry.RouteModel)
	was := ProvimuxRoute{Unlisted: true}
	if auth, ok := m.GetByID(removedAuth); ok {
		was = provimuxRouteOf(auth, name)
	}
	_, available := provimuxSeamRoutes(m, r.entry.Provider, r.entry.RouteModel, now)
	out.Released = true
	out.Was = &was
	out.Next = provimuxRoutesOf(provimuxSeamHighest(available), name)
	return out, nil
}
