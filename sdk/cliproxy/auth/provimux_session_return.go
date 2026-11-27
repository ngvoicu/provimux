package auth

import (
	"sync/atomic"
	"time"
)

// Provimux: session-route, the rule (.github/provimux/design/session-route.md 15). At a conversation's next request,
// inside upstream's selection (SessionAffinitySelector.Pick, one line before the binding is looked up), the binding is
// dropped when the operator has set cold-after, the client named the conversation, it is bound to an account below
// the highest-priority account this request may use, and no message of it was routed to an account for cold-after.
// That is read from the rule's own note: the time a request of the conversation last came to selection. A message
// refused before selection reaches no provider and leaves no note. A provider's prompt cache is refreshed when a
// request reads it, not while the answer is written, so the time since the last routed message is what tells whether
// the cache can still be warm. Whatever is not certain leaves the binding alone. No book, no result hook, no usage
// record, no timer, nothing from a file.
//
// The names of upstream's this file uses: the selector's cache, rebindOnNever and its own field provimux; the cache's
// mu, entries, ttl, maxEntries, ensureInitializedLocked and removeAliasGroupLocked; an entry's authID, expiresAt and
// aliases; sameSessionEntryGroup; authPriority; the manager's Selector, HomeEnabled and hasPluginScheduler.

// provimuxReturnNoteLimit is the number of keys the table of notes holds; at that size it is emptied (15.2, K7). The
// marks of returns (for part one's expect) are kept apart and never emptied while their binding stands.
const provimuxReturnNoteLimit = 16384

// provimuxSessionReturn is the selector's own part of the rule, guarded by the selector's cache's mu; the zero value is
// ready. seen is K7: when a request of each key last came to selection, under the value observed. returned is the
// number of the rule's last return of a binding each key was in.
type provimuxSessionReturn struct {
	observed *provimuxReturnValue // the value in use when the notes were written
	seen     map[string]time.Time
	returned map[string]uint64
	returns  uint64 // the last return number given by this selector
}

// provimuxReturnValue is cold-after as committed for the gateway's manager. A new value is stored only when one of
// the two changes, so its identity says "changed".
type provimuxReturnValue struct {
	coldAfter time.Duration
	manager   *Manager
}

var (
	provimuxReturnCurrent atomic.Pointer[provimuxReturnValue] // nil: not set
	// provimuxReturned counts the bindings the rule dropped since startup: raised in the step that drops one.
	provimuxReturned     atomic.Int64
	provimuxReturnedLast atomic.Pointer[time.Time]
	provimuxReturnSince  = time.Now()
)

// ProvimuxSetSessionReturn puts coldAfter in force for m, where the service commits a configuration and for the first
// configuration of a start; 0 puts the rule out of force. When the value changes (set, changed, removed, set again), a
// new observation starts: every selector empties its notes at its next call, so the first message of each
// conversation after that is not judged. An unchanged value keeps the notes.
func ProvimuxSetSessionReturn(m *Manager, coldAfter time.Duration) {
	if m == nil || coldAfter <= 0 {
		provimuxReturnCurrent.Store(nil)
		return
	}
	if previous := provimuxReturnCurrent.Load(); previous != nil && previous.manager == m && previous.coldAfter == coldAfter {
		return
	}
	provimuxReturnCurrent.Store(&provimuxReturnValue{coldAfter: coldAfter, manager: m})
}

// ProvimuxSessionReturnAgeForTest moves every note of the rule on m's selector d into the past, as if d had passed
// since each message. For tests only: the tests of other packages that go through a real request need it, as Pick
// takes its own time.
func ProvimuxSessionReturnAgeForTest(m *Manager, d time.Duration) {
	s, ok := m.Selector().(*SessionAffinitySelector)
	if !ok {
		return
	}
	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()
	for key, seen := range s.provimux.seen {
		s.provimux.seen[key] = seen.Add(-d)
	}
}

// provimuxReturnRuns reports whether the rule judges on s, m's selector: not Home, no plugin scheduler, s does not
// refuse to move a bound conversation (rebind-on never), and cold-after is shorter than s's binding lifetime.
func provimuxReturnRuns(m *Manager, s *SessionAffinitySelector, coldAfter time.Duration) bool {
	return !s.rebindOnNever && coldAfter < s.cache.ttl && !m.HomeEnabled() && !m.hasPluginScheduler()
}

// provimuxReturn is the rule (15.2), called by Pick right before it looks the binding up. named is K2 (the client
// named the conversation and it is neither a sub-agent nor a fork); cacheKey and fallbackKey are Pick's keys;
// available is the list Pick chooses from for this request; now is the time Pick took. It returns nothing: it either
// dropped the binding or did not, and Pick's own lines decide everything after it. Unset, it is one atomic load and a
// return: nothing locked, nothing written. Set, every request that passes K2 is noted, judged or not, so a note is
// always the conversation's last message routed to an account; the judgement comes first, so it reads the note of
// the message before this one.
func (s *SessionAffinitySelector) provimuxReturn(named bool, cacheKey, fallbackKey string, available []*Auth, now time.Time) {
	value := provimuxReturnCurrent.Load()
	if value == nil || !named { // K1 (set), K2
		return
	}
	runs := provimuxReturnRuns(value.manager, s, value.coldAfter) // K1 (in effect)
	cache := s.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ensureInitializedLocked()
	if s.provimux.observed != value || len(s.provimux.seen) >= provimuxReturnNoteLimit {
		s.provimux.observed, s.provimux.seen = value, make(map[string]time.Time) // a new observation
	}
	if runs {
		s.provimuxJudgeLocked(cache, cacheKey, fallbackKey, available, now, value.coldAfter)
	}
	s.provimux.seen[cacheKey] = now // K7
}

// provimuxJudgeLocked is K3 to K6 and the comparison, under the cache's write lock: it drops the binding, or not.
func (s *SessionAffinitySelector) provimuxJudgeLocked(cache *SessionCache, cacheKey, fallbackKey string, available []*Auth, now time.Time, coldAfter time.Duration) {
	entry, ok := cache.entries[cacheKey] // K3: a live binding
	if !ok || entry.authID == "" || !now.Before(entry.expiresAt) {
		return
	}
	if fallbackKey != "" && fallbackKey != cacheKey { // K4: the second key is in the same binding
		if other, okOther := cache.entries[fallbackKey]; okOther && other.authID != "" && now.Before(other.expiresAt) && !sameSessionEntryGroup(other, entry) {
			return
		}
	}
	boundFound, boundPriority, highest, seenAny := false, 0, 0, false // K5: a higher account this request may use
	for _, auth := range available {
		if auth == nil {
			continue
		}
		priority := authPriority(auth)
		if !seenAny || priority > highest {
			highest, seenAny = priority, true
		}
		if auth.ID == entry.authID {
			boundFound, boundPriority = true, priority
		}
	}
	if !boundFound || boundPriority >= highest {
		return
	}
	var last time.Time // K6: the conversation's last routed message, through any key of its binding
	for _, key := range append([]string{cacheKey}, entry.aliases...) {
		if seen := s.provimux.seen[key]; seen.After(last) {
			last = seen
		}
	}
	if last.IsZero() || now.Sub(last) < coldAfter {
		return
	}
	cache.removeAliasGroupLocked(entry)
	s.provimux.returns++
	s.provimux.markReturnedLocked(cache, entry.aliases)
	provimuxReturned.Add(1)
	at := now
	provimuxReturnedLast.Store(&at)
}

// markReturnedLocked marks keys with the selector's latest return number. The marks are kept while their key has a
// binding: when they reach the cache's capacity, the marks of keys the cache no longer holds are dropped.
func (r *provimuxSessionReturn) markReturnedLocked(cache *SessionCache, keys []string) {
	if r.returned == nil {
		r.returned = make(map[string]uint64)
	}
	if len(r.returned)+len(keys) > cache.maxEntries {
		for key := range r.returned {
			if _, live := cache.entries[key]; !live {
				delete(r.returned, key)
			}
		}
	}
	for _, key := range keys {
		r.returned[key] = r.returns
	}
}

// returnedOfLocked is the number of the rule's last return of a binding key was in, or 0; under the cache's lock.
func (r *provimuxSessionReturn) returnedOfLocked(key string) uint64 {
	return r.returned[key]
}

// ProvimuxSessionReturnState is the answer of GET /v8/management/provimux/session-return (SessionReturnState in
// session-route/openapi.yaml).
type ProvimuxSessionReturnState struct {
	ObservedAt string                      `json:"observed_at"`
	Setting    ProvimuxSessionReturnSet    `json:"setting"`
	InEffect   bool                        `json:"in_effect"`
	Reason     *string                     `json:"reason"`
	TTLS       *int64                      `json:"ttl_s"` // the binding lifetime in use; nil with session affinity off
	Counts     ProvimuxSessionReturnCounts `json:"counts"`
}

// ProvimuxSessionReturnSet is SessionReturnState.setting.
type ProvimuxSessionReturnSet struct {
	ColdAfterS *float64 `json:"cold_after_s"`
}

// ProvimuxSessionReturnCounts is SessionReturnState.counts.
type ProvimuxSessionReturnCounts struct {
	Returned       int64   `json:"returned"`
	LastReturnedAt *string `json:"last_returned_at"`
	Since          string  `json:"since"`
}

// The reasons of SessionReturnState.reason, in the order the contract gives them.
const (
	provimuxReturnHomeMode        = "home-mode"
	provimuxReturnPluginScheduler = "plugin-scheduler"
	provimuxReturnAffinityOff     = "session-affinity-off"
	provimuxReturnNotSet          = "not-set"
	provimuxReturnRebindNever     = "rebind-never"
	provimuxReturnNotBelowTTL     = "not-below-ttl"
)

// provimuxReturnColdAfter is cold-after as committed, and whether it is set.
func provimuxReturnColdAfter() (time.Duration, bool) {
	if value := provimuxReturnCurrent.Load(); value != nil {
		return value.coldAfter, true
	}
	return 0, false
}

// provimuxSeconds is d in seconds, with its fraction: what the contract shows, never what the rule compares.
func provimuxSeconds(d time.Duration) *float64 {
	seconds := d.Seconds()
	return &seconds
}

// provimuxReturnReason is why the rule does not run for m now, or "" when it runs: the first that applies, in the
// contract's order. It reads what Pick's call reads (the value in use, m's mode and m's selector as it runs), so ""
// is exactly when that call judges.
func provimuxReturnReason(m *Manager) string {
	affinity, isAffinity := m.Selector().(*SessionAffinitySelector)
	value := provimuxReturnCurrent.Load()
	switch {
	case m.HomeEnabled():
		return provimuxReturnHomeMode
	case m.hasPluginScheduler():
		return provimuxReturnPluginScheduler
	case !isAffinity:
		return provimuxReturnAffinityOff
	case value == nil:
		return provimuxReturnNotSet
	case affinity.rebindOnNever:
		return provimuxReturnRebindNever
	case value.coldAfter >= affinity.cache.ttl:
		return provimuxReturnNotBelowTTL
	}
	return ""
}

// ProvimuxSessionReturnStateOf is the rule's state for m at now: the setting, whether the rule runs and why not, the
// binding lifetime in use, and the exact count of returns since the start. It reads; it changes nothing.
func ProvimuxSessionReturnStateOf(m *Manager, now time.Time) ProvimuxSessionReturnState {
	state := ProvimuxSessionReturnState{
		ObservedAt: provimuxTime(now),
		Counts:     ProvimuxSessionReturnCounts{Returned: provimuxReturned.Load(), Since: provimuxTime(provimuxReturnSince)},
	}
	if last := provimuxReturnedLast.Load(); last != nil {
		state.Counts.LastReturnedAt = provimuxTimePtr(*last)
	}
	if coldAfter, set := provimuxReturnColdAfter(); set {
		state.Setting.ColdAfterS = provimuxSeconds(coldAfter)
	}
	if affinity, ok := m.Selector().(*SessionAffinitySelector); ok {
		ttl := int64(affinity.cache.ttl / time.Second)
		state.TTLS = &ttl
	}
	if reason := provimuxReturnReason(m); reason != "" {
		state.Reason = &reason
	} else {
		state.InEffect = true
	}
	return state
}

// provimuxSessionRuleOf is the show's rule for m: whether the rule runs now, and the operator's time (null when not
// set).
func provimuxSessionRuleOf(m *Manager) ProvimuxSessionRule {
	rule := ProvimuxSessionRule{InEffect: provimuxReturnReason(m) == ""}
	if coldAfter, set := provimuxReturnColdAfter(); set {
		rule.ColdAfterS = provimuxSeconds(coldAfter)
	}
	return rule
}
