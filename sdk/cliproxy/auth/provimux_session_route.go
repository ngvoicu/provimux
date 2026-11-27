package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: session-route (.github/provimux/design/session-route.md 3 and 4). The book is a note about upstream's
// session bindings, never their source: every reader asks upstream's cache first (through the seam). A client key sees
// and releases only a conversation the book has seen that key use since the gateway started (decided by the lead on
// 2026-11-27). Releasing removes the binding from upstream's cache and nothing else: nothing is sent, no account's
// state changes. This file calls the seam and exported names only.

// provimuxSessionBookLimit is the number of conversations the book keeps; the least recently answered go first.
const provimuxSessionBookLimit = 4096

// provimuxSessionScopeLimit is the number of client keys (caller scopes) kept per conversation.
const provimuxSessionScopeLimit = 4

// provimuxColdMargin is the margin past a cache's lifetime after which it is taken as cold (session-route.md 5 b).
const provimuxColdMargin = 60 * time.Second

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

// provimuxSessionLifetime is the lifetime the gateway takes a binding's cache to have, and its basis
// (session-route.md 6.2). Until the rule's package (G2) adds the conversation's own answers and the figures, it is
// row 3 alone: cold-after, one hour unless set.
var provimuxSessionLifetime = func(_ ProvimuxSessionBookEntry) (seconds int64, basis string, entry *string) {
	return 3600, "setting", nil
}

// provimuxSessionRule is what the gateway does by itself with a binding (SessionRoute.rule). The rule is G2's: until
// it exists it is not in effect and every binding stays.
var provimuxSessionRule = func(_ ProvimuxSessionBookEntry, _ ProvimuxSessionPosition, _ bool) (inEffect bool, will string) {
	return false, "stay"
}

// ProvimuxRoute is one account that serves a name, exactly as a row of GET /v1/models?include=accounts gives it.
type ProvimuxRoute struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
	Host     string `json:"host,omitempty"`
	Account  string `json:"account,omitempty"`
}

// provimuxRouteOf is auth's row: the fields of include=accounts, and nothing else.
func provimuxRouteOf(auth *Auth) ProvimuxRoute {
	return ProvimuxRoute{Provider: auth.Provider, Kind: auth.AuthKind(), Priority: auth.Priority(), Host: auth.BaseURLHost(), Account: auth.ProvimuxAccountMarker()}
}

// provimuxRoutesOf are the rows of auths in the order of include=accounts: highest priority first.
func provimuxRoutesOf(auths []*Auth) []ProvimuxRoute {
	out := make([]ProvimuxRoute, 0, len(auths))
	for _, auth := range auths {
		if auth != nil {
			out = append(out, provimuxRouteOf(auth))
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

// ProvimuxSessionCache is SessionRoute.cache.
type ProvimuxSessionCache struct {
	LifetimeS int64   `json:"lifetime_s"`
	Basis     string  `json:"basis"`
	Entry     *string `json:"entry"`
	LastUseAt string  `json:"last_use_at"`
	Cold      bool    `json:"cold"`
	ColdAt    string  `json:"cold_at"`
}

// ProvimuxSessionRule is SessionRoute.rule.
type ProvimuxSessionRule struct {
	InEffect bool   `json:"in_effect"`
	Will     string `json:"will"`
}

// ProvimuxSessionRoute is the answer of POST /v1/provimux/session-route (SessionRoute in session-route/openapi.yaml).
type ProvimuxSessionRoute struct {
	ObservedAt       string                   `json:"observed_at"`
	Affinity         string                   `json:"affinity"`
	Conversation     string                   `json:"conversation"`
	Model            string                   `json:"model"`
	Binding          *string                  `json:"binding"`
	Bound            *ProvimuxSessionBound    `json:"bound"`
	Position         *ProvimuxSessionPosition `json:"position"`
	First            []ProvimuxRoute          `json:"first"`
	Cache            *ProvimuxSessionCache    `json:"cache"`
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
	Was              *ProvimuxRoute  `json:"was"`
	Next             []ProvimuxRoute `json:"next"`
	WebsocketMayHold bool            `json:"websocket_may_hold"`
}

// ProvimuxSessionBookEntry is what the book knows of one conversation for one name. Readers get copies.
type ProvimuxSessionBookEntry struct {
	Key         string    // the binding's key in upstream's cache, as Pick made it
	Provider    string    // its provider part ("mixed" for a name served by several providers)
	Session     string    // the bounded session identity
	ModelKey    string    // the name without a thinking suffix
	RouteModel  string    // the name as Pick received it
	Parent      string    // the parent conversation, for a sub-agent
	Current     string    // the account the book last saw answer under the binding; "" when it has none
	Since       time.Time // when Current first answered in this stretch
	Previous    string    // the account that answered before Current, in this stretch
	PreviousAt  time.Time // when Previous stopped
	LastSuccess time.Time // the last successful answer under the binding
	Scopes      []string  // the caller scopes seen using it (at most provimuxSessionScopeLimit)
	LastTouched time.Time // the last answer of any account for it
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

func (e *ProvimuxSessionBookEntry) clearBinding() {
	e.Current, e.Since, e.Previous, e.PreviousAt, e.LastSuccess = "", time.Time{}, "", time.Time{}, time.Time{}
}

// ProvimuxSessionBook is the book of one manager. It observes the manager's results through AddHook and never takes
// part in a selection.
type ProvimuxSessionBook struct {
	NoopHook
	manager *Manager

	mu          sync.Mutex
	entries     map[string]*ProvimuxSessionBookEntry
	salt        [32]byte
	unsubscribe func()

	releasedByClient atomic.Int64
}

// NewProvimuxSessionBook makes an empty book for m. It observes nothing until Subscribe.
func NewProvimuxSessionBook(m *Manager) *ProvimuxSessionBook {
	b := &ProvimuxSessionBook{manager: m, entries: make(map[string]*ProvimuxSessionBookEntry)}
	if _, errRand := rand.Read(b.salt[:]); errRand != nil {
		// A binding token only needs to differ per binding; without randomness it still does.
		copy(b.salt[:], strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	return b
}

// Subscribe adds the book to its manager's result hooks. Idempotent.
func (b *ProvimuxSessionBook) Subscribe() {
	if b == nil || b.manager == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unsubscribe == nil {
		b.unsubscribe = b.manager.AddHook(b)
	}
}

// Unsubscribe removes the book from its manager's hooks.
func (b *ProvimuxSessionBook) Unsubscribe() {
	if b == nil {
		return
	}
	b.mu.Lock()
	unsubscribe := b.unsubscribe
	b.unsubscribe = nil
	b.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

// Manager is the manager the book observes.
func (b *ProvimuxSessionBook) Manager() *Manager {
	if b == nil {
		return nil
	}
	return b.manager
}

// ReleasedByClient is the number of bindings client keys released since the book was made.
func (b *ProvimuxSessionBook) ReleasedByClient() int64 {
	if b == nil {
		return 0
	}
	return b.releasedByClient.Load()
}

// OnResult notes a successful answer: the caller scope that used the conversation, and, when upstream's cache holds
// the result's key bound to the account that answered, that account (session-route.md 3). A result whose key the
// cache does not hold for its account (an answer in flight at a release, a transient failover that kept the binding,
// a stale result) notes the caller only, never a binding.
func (b *ProvimuxSessionBook) OnResult(_ context.Context, res Result) {
	if b == nil || !res.Success || res.AuthID == "" {
		return
	}
	affinity, mode := provimuxSeamAffinity(b.manager)
	if mode != provimuxAffinityOn {
		return
	}
	key, ok := provimuxSeamResultKey(res)
	if !ok {
		return
	}
	binding, bound := provimuxSeamLookup(affinity, key.Key)
	holds := bound && binding.AuthID == res.AuthID
	scope := ""
	if res.Options.Metadata != nil {
		scope, _ = res.Options.Metadata[cliproxyexecutor.CallerScopeMetadataKey].(string)
		scope = strings.TrimSpace(scope)
	}
	if !holds && scope == "" {
		return
	}
	now := provimuxSessionRouteNow()

	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[key.Key]
	if entry == nil {
		b.evictLocked()
		entry = &ProvimuxSessionBookEntry{Key: key.Key, Provider: key.Provider, Session: key.Session, ModelKey: key.ModelKey, RouteModel: key.RouteModel}
		b.entries[key.Key] = entry
	}
	entry.LastTouched = now
	entry.addScope(scope)
	if key.Parent != "" {
		entry.Parent = key.Parent
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
	case !entry.LastSuccess.IsZero() && binding.TTL > 0 && !now.Before(entry.LastSuccess.Add(binding.TTL)):
		// The binding expired between the two answers: a new stretch, whatever account it is now.
		entry.Current, entry.Since, entry.Previous, entry.PreviousAt = res.AuthID, now, "", time.Time{}
	case entry.Current != res.AuthID:
		entry.Previous, entry.PreviousAt = entry.Current, now
		entry.Current, entry.Since = res.AuthID, now
	}
	entry.LastSuccess = now
}

// evictLocked drops the least recently answered entry when the book is full.
func (b *ProvimuxSessionBook) evictLocked() {
	if len(b.entries) < provimuxSessionBookLimit {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, entry := range b.entries {
		if oldestKey == "" || entry.LastTouched.Before(oldest) || (entry.LastTouched.Equal(oldest) && key < oldestKey) {
			oldestKey, oldest = key, entry.LastTouched
		}
	}
	delete(b.entries, oldestKey)
}

// Entries returns a copy of every entry, in key order: for tests and the operator's list (G2).
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
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
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

// resolve finds the conversation named by headers and body for model, among those scope was seen using.
func (b *ProvimuxSessionBook) resolve(scope string, headers http.Header, body []byte, model string) provimuxResolved {
	var r provimuxResolved
	r.affinity, r.mode = provimuxSeamAffinity(b.manager)
	if r.mode != provimuxAffinityOn {
		return r
	}
	session, clientType := provimuxSeamNamedSession(headers, body)
	modelKey := provimuxSeamModelKey(model)
	scope = strings.TrimSpace(scope)
	if session == "" || modelKey == "" || scope == "" {
		return r
	}
	r.clientType = clientType
	var candidates []ProvimuxSessionBookEntry
	b.mu.Lock()
	for _, entry := range b.entries {
		if entry.Session == session && entry.ModelKey == modelKey && entry.hasScope(scope) {
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
		return candidates[i].Key < candidates[j].Key
	})
	r.entry = candidates[0]
	for _, candidate := range candidates {
		binding, ok := provimuxSeamLookup(r.affinity, candidate.Key)
		if !ok {
			continue
		}
		auth, okAuth := b.manager.GetByID(binding.AuthID)
		if !okAuth || auth == nil {
			// An account no longer registered: upstream drops such a binding itself at the next request.
			continue
		}
		r.entry, r.bound, r.binding, r.auth = candidate, true, binding, auth
		break
	}
	return r
}

// bindingToken is the binding's token for expect: it stands for this binding (its key and its stretch) on this account.
func (b *ProvimuxSessionBook) bindingToken(r provimuxResolved) string {
	since := ""
	if r.entry.Current == r.binding.AuthID && !r.entry.Since.IsZero() {
		since = strconv.FormatInt(r.entry.Since.UnixNano(), 10)
	}
	h := sha256.New()
	h.Write(b.salt[:])
	h.Write([]byte(r.entry.Key))
	h.Write([]byte{0})
	h.Write([]byte(r.binding.AuthID))
	h.Write([]byte{0})
	h.Write([]byte(since))
	return "b." + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil)))[:16]
}

// ProvimuxSessionModelName is the name as the answers give it: the name asked about, without a thinking suffix.
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

// Show answers POST /v1/provimux/session-route for a client key's caller scope. It changes nothing: no binding is
// made, moved or refreshed, and the book is only read. Every call answers from the state at that moment.
func (b *ProvimuxSessionBook) Show(scope string, headers http.Header, body []byte, model string) ProvimuxSessionRoute {
	r := b.resolve(scope, headers, body, model)
	return b.show(r, model, provimuxSessionRouteNow())
}

func (b *ProvimuxSessionBook) show(r provimuxResolved, model string, now time.Time) ProvimuxSessionRoute {
	out := ProvimuxSessionRoute{
		ObservedAt:   provimuxTime(now),
		Affinity:     r.mode,
		Conversation: "not-seen",
		Model:        ProvimuxSessionModelName(model),
		First:        []ProvimuxRoute{},
		Rule:         ProvimuxSessionRule{InEffect: false, Will: "stay"},
	}
	if r.mode != provimuxAffinityOn {
		out.Conversation = "not-bound"
		return out
	}
	if !r.seen {
		return out
	}
	out.Subagent = r.entry.Parent != ""
	if !r.bound {
		out.Conversation = "not-bound"
		out.Rule.InEffect, _ = provimuxSessionRule(r.entry, ProvimuxSessionPosition{}, false)
		return out
	}
	out.Conversation = "bound"
	out.WebsocketMayHold = r.clientType == "codex"
	token := b.bindingToken(r)
	out.Binding = &token

	bound := &ProvimuxSessionBound{Route: provimuxRouteOf(r.auth), Origin: "not-known"}
	if r.entry.Current == r.binding.AuthID && !r.entry.Since.IsZero() {
		bound.Since = provimuxTimePtr(r.entry.Since)
		bound.Origin = "first"
		if r.entry.Previous != "" {
			bound.Origin = "fallback"
			bound.FellBackAt = provimuxTimePtr(r.entry.PreviousAt)
			if previous, ok := b.manager.GetByID(r.entry.Previous); ok && previous != nil {
				route := provimuxRouteOf(previous)
				bound.FellBackFrom = &route
			}
		}
	}
	out.Bound = bound

	position, first := b.position(r, now)
	out.Position = &position
	out.First = first

	lifetime, basis, entryName := provimuxSessionLifetime(r.entry)
	lastUse := r.binding.ExpiresAt.Add(-r.binding.TTL)
	if r.entry.Current == r.binding.AuthID && r.entry.LastSuccess.After(lastUse) {
		lastUse = r.entry.LastSuccess
	}
	coldAt := lastUse.Add(time.Duration(lifetime)*time.Second + provimuxColdMargin)
	cold := now.After(coldAt)
	out.Cache = &ProvimuxSessionCache{LifetimeS: lifetime, Basis: basis, Entry: entryName, LastUseAt: provimuxTime(lastUse), Cold: cold, ColdAt: provimuxTime(coldAt)}
	out.Rule.InEffect, out.Rule.Will = provimuxSessionRule(r.entry, position, cold)
	return out
}

// position judges the binding against the accounts that serve its name now (session-route.md 6.1): the first routes
// (the highest available priority), whether an account of a higher priority than the bound one is free, and, when
// none is, when the first recorded cooldown among the higher-priority accounts ends. Both are facts about the name,
// never per account.
func (b *ProvimuxSessionBook) position(r provimuxResolved, now time.Time) (ProvimuxSessionPosition, []ProvimuxRoute) {
	serving, available := provimuxSeamRoutes(b.manager, r.entry.Provider, r.entry.RouteModel, now)
	boundPriority := r.auth.Priority()
	var position ProvimuxSessionPosition
	for _, auth := range serving {
		if auth.Priority() > boundPriority {
			position.BelowFirst = true
		}
	}
	for _, auth := range available {
		if auth.Priority() > boundPriority {
			position.HigherPriorityFree = true
		}
	}
	if !position.HigherPriorityFree {
		var earliest time.Time
		for _, auth := range serving {
			if auth.Priority() <= boundPriority {
				continue
			}
			view, ok := b.manager.ProvimuxRouteCooldowns(auth, []string{r.entry.RouteModel}, now)[r.entry.RouteModel]
			if !ok || !view.RetryAt.After(now) {
				continue
			}
			if earliest.IsZero() || view.RetryAt.Before(earliest) {
				earliest = view.RetryAt
			}
		}
		position.HigherPriorityFreeAt = provimuxTimePtr(earliest)
	}
	return position, provimuxRoutesOf(provimuxSeamHighest(available))
}

// Release answers POST /v1/provimux/session-route/release for a client key's caller scope. With expect, it releases
// only while the binding is still the one the token stands for; otherwise conflict is the state now (409). It removes
// the binding and every alias of it from upstream's cache, compared with the account it judged, and nothing else.
func (b *ProvimuxSessionBook) Release(scope string, headers http.Header, body []byte, model, expect string) (ProvimuxSessionRelease, *ProvimuxSessionRoute) {
	expect = strings.TrimSpace(expect)
	for attempt := 0; ; attempt++ {
		r := b.resolve(scope, headers, body, model)
		now := provimuxSessionRouteNow()
		out := ProvimuxSessionRelease{ObservedAt: provimuxTime(now), Affinity: r.mode, Conversation: "not-bound", Next: []ProvimuxRoute{}}
		if r.mode != provimuxAffinityOn {
			return out, nil
		}
		if !r.seen {
			out.Conversation = "not-seen"
			return out, nil
		}
		if !r.bound {
			return out, nil
		}
		if expect != "" && expect != b.bindingToken(r) {
			state := b.show(r, model, now)
			return ProvimuxSessionRelease{}, &state
		}
		if !provimuxSeamRelease(r.affinity, r.entry.Key, r.binding.AuthID, time.Time{}) {
			// The binding changed between the look and the removal: with expect that is a conflict; without, look
			// again (at most three times, then answer the state as it is).
			if expect != "" {
				again := b.resolve(scope, headers, body, model)
				if !again.bound {
					return out, nil
				}
				state := b.show(again, model, provimuxSessionRouteNow())
				return ProvimuxSessionRelease{}, &state
			}
			if attempt >= 2 {
				return out, nil
			}
			continue
		}
		b.mu.Lock()
		if entry := b.entries[r.entry.Key]; entry != nil {
			entry.clearBinding()
		}
		b.mu.Unlock()
		b.releasedByClient.Add(1)
		was := provimuxRouteOf(r.auth)
		_, available := provimuxSeamRoutes(b.manager, r.entry.Provider, r.entry.RouteModel, now)
		out.Released = true
		out.Was = &was
		out.Next = provimuxRoutesOf(provimuxSeamHighest(available))
		out.WebsocketMayHold = r.clientType == "codex"
		return out, nil
	}
}
