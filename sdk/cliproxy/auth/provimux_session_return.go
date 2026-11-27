package auth

import (
	"sync/atomic"
	"time"
	"weak"
)

// Provimux: session-route, the rule (.github/provimux/design/session-route.md 15). At a conversation's next request,
// inside upstream's selection (SessionAffinitySelector.Pick, one line before the binding is honoured), the binding is
// dropped when the operator has set cold-after, the client named the conversation, it is bound to an account below
// the highest-priority account this request may use, and it has not been used for cold-after. Upstream then chooses
// for the request as for a new conversation. Seven things are read in one call, under the cache's lock (15.3); the
// first that is not certain ends the call with nothing done. No book, no result hook, no usage record, no figures, no
// timer, nothing from a file.
//
// The names of upstream's this file uses: the selector's cache, rebindOnNever and its own field provimux; the cache's
// mu, entries, ttl, ensureInitializedLocked and removeAliasGroupLocked; an entry's authID, expiresAt and aliases;
// sameSessionEntryGroup; authPriority; the manager's Selector, HomeEnabled and hasPluginScheduler.

// provimuxReturnNoteLimit is the number of keys the table of notes holds; at that size it is emptied (15.4).
const provimuxReturnNoteLimit = 16384

// provimuxSessionReturn is the selector's own part of the rule (K7): when this selector saw a request of each key,
// and the number of the rule's last return of each key's binding. Its life is the selector's, so a note never
// outlives the cache it speaks of. Guarded by the selector's cache's mu; the zero value is ready.
type provimuxSessionReturn struct {
	notes   map[string]provimuxReturnNote
	returns uint64 // the last return number given by this selector
}

// provimuxReturnNote is what the table holds for one key.
type provimuxReturnNote struct {
	seen     time.Time // the time Pick took at the last request of this key that reached the rule; zero: none
	returned uint64    // the number of the rule's last return of a binding this key was in; 0: none
}

// ProvimuxSessionReturnSetting is the configuration the rule depends on, as the service's hook read it.
type ProvimuxSessionReturnSetting struct {
	ColdAfter    time.Duration // routing.session-affinity-return.cold-after; 0 when not set
	ColdAfterSet bool
	Affinity     bool          // routing.session-affinity
	Home         bool          // home.enabled
	TTL          time.Duration // routing.session-affinity-ttl as it applies
	Strategy     string        // routing.strategy as it applies
	Subagents    bool          // routing.session-affinity-subagents as it applies
	RebindOn     string        // routing.session-affinity-rebind-on as it applies
}

// provimuxReturnValue is the value in use: the setting, the manager it was committed for, and whether the
// configuration lets the rule run at all. One atomic value, loaded once at the top of each call.
type provimuxReturnValue struct {
	setting  ProvimuxSessionReturnSetting
	manager  weak.Pointer[Manager]
	possible bool
}

var (
	provimuxReturnCurrent atomic.Pointer[provimuxReturnValue]
	// provimuxReturned counts the bindings the rule dropped since the start: raised in the step that drops one.
	provimuxReturned     atomic.Int64
	provimuxReturnedLast atomic.Pointer[time.Time]
	provimuxReturnSince  = time.Now()
)

// ProvimuxSetSessionReturn puts setting in force for m, where the service commits a configuration and for the first
// configuration of a start. A nil m puts the rule out of force.
func ProvimuxSetSessionReturn(m *Manager, setting ProvimuxSessionReturnSetting) {
	if m == nil {
		provimuxReturnCurrent.Store(nil)
		return
	}
	possible := setting.ColdAfterSet && setting.ColdAfter > 0 && setting.Affinity && !setting.Home &&
		normalizeSessionAffinityRebindOn(setting.RebindOn) != SessionAffinityRebindOnNever &&
		(setting.TTL <= 0 || setting.ColdAfter < setting.TTL)
	provimuxReturnCurrent.Store(&provimuxReturnValue{setting: setting, manager: weak.Make(m), possible: possible})
}

// SetProvimuxSessionReturnForTest puts a setting in force for m and returns the function that puts the previous value
// back. For tests only.
func SetProvimuxSessionReturnForTest(m *Manager, setting ProvimuxSessionReturnSetting) (restore func()) {
	previous := provimuxReturnCurrent.Load()
	ProvimuxSetSessionReturn(m, setting)
	return func() { provimuxReturnCurrent.Store(previous) }
}

// ProvimuxSessionReturnAgeForTest moves every binding of m's session cache, and every note of the rule, d into the past,
// as if d had passed in this process: expiries and notes keep their monotonic readings. Upstream writes its expiries
// with time.Now(), so this is how a test sets the rule's clock without sleeping. For tests only.
func ProvimuxSessionReturnAgeForTest(m *Manager, d time.Duration) {
	s, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || s == nil || s.cache == nil {
		return
	}
	cache := s.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ensureInitializedLocked()
	groups := make([]sessionEntry, 0, len(cache.groups))
	for _, group := range cache.groups {
		groups = append(groups, group)
	}
	for _, group := range groups {
		cache.replaceAliasGroupsLocked(group.authID, group.expiresAt.Add(-d), group.aliases, group.binding, group)
	}
	for key, note := range s.provimux.notes {
		if !note.seen.IsZero() {
			note.seen = note.seen.Add(-d)
		}
		s.provimux.notes[key] = note
	}
}

// ProvimuxSessionReturnLastUseForTest is the latest last use among m's bindings (an expiry minus the cache's TTL), or
// the zero time. A test that ages bindings after a streamed answer waits until this is not before the answer's result:
// upstream refreshes a binding right after the result hooks run, so a hook alone does not order the two. For tests
// only.
func ProvimuxSessionReturnLastUseForTest(m *Manager) time.Time {
	s, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || s == nil || s.cache == nil {
		return time.Time{}
	}
	s.cache.mu.RLock()
	defer s.cache.mu.RUnlock()
	var latest time.Time
	for _, group := range s.cache.groups {
		if lastUse := group.expiresAt.Add(-s.cache.ttl); lastUse.After(latest) {
			latest = lastUse
		}
	}
	return latest
}

// provimuxMonotonic reports whether t carries this process's monotonic clock reading: a time differs from itself with
// the reading stripped (Round(0)) exactly when it has one (design 15.13, J2). An expiry written from a file has none.
func provimuxMonotonic(t time.Time) bool {
	return t != t.Round(0)
}

// provimuxReturnRuns reports whether the rule runs for s, the selector of m, with coldAfter: m selects with s (not
// Home, no plugin scheduler), s does not refuse to move a bound conversation (rebind-on never), and cold-after is
// shorter than s's binding lifetime.
func provimuxReturnRuns(m *Manager, s *SessionAffinitySelector, coldAfter time.Duration) bool {
	if m == nil || s == nil || s.cache == nil || s.rebindOnNever || coldAfter <= 0 || coldAfter >= s.cache.ttl {
		return false
	}
	if m.HomeEnabled() || m.hasPluginScheduler() {
		return false
	}
	current, ok := m.Selector().(*SessionAffinitySelector)
	return ok && current == s
}

// provimuxReturn is the rule (15.3), called by Pick right before it looks the binding up. named is K2 (the client
// named the conversation and it is neither a sub-agent nor a fork); cacheKey and fallbackKey are Pick's keys;
// available is the list Pick chooses from for this request; now is the time Pick took. It returns nothing: it either
// dropped the binding or did not, and Pick's own lines decide everything after it. Not in effect, it is one atomic
// load and a return.
func (s *SessionAffinitySelector) provimuxReturn(named bool, cacheKey, fallbackKey string, available []*Auth, now time.Time) {
	value := provimuxReturnCurrent.Load()
	if value == nil || !value.possible || !named || s == nil || cacheKey == "" { // K1 (as configured), K2
		return
	}
	coldAfter := value.setting.ColdAfter
	if !provimuxReturnRuns(value.manager.Value(), s, coldAfter) { // K1 (as it runs)
		return
	}
	cache := s.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ensureInitializedLocked()
	s.provimuxJudgeLocked(cache, cacheKey, fallbackKey, available, now, coldAfter)
	s.provimux.seenLocked(cacheKey, now) // K7: written at the end of every call that passed K1 and K2
}

// provimuxJudgeLocked is K3 to K6 and the comparison, under the cache's write lock; it reports whether it dropped
// the binding.
func (s *SessionAffinitySelector) provimuxJudgeLocked(cache *SessionCache, cacheKey, fallbackKey string, available []*Auth, now time.Time, coldAfter time.Duration) bool {
	entry, ok := cache.entries[cacheKey] // K3
	if !ok || entry.authID == "" || !now.Before(entry.expiresAt) {
		return false
	}
	if fallbackKey != "" && fallbackKey != cacheKey { // K4
		if other, okOther := cache.entries[fallbackKey]; okOther && other.authID != "" && now.Before(other.expiresAt) && !sameSessionEntryGroup(other, entry) {
			return false
		}
	}
	boundFound, boundPriority, highest, seenAny := false, 0, 0, false // K5
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
		return false
	}
	note := s.provimux.notes[cacheKey] // K6, with K7's note
	if note.seen.IsZero() || !provimuxMonotonic(note.seen) || !provimuxMonotonic(entry.expiresAt) || !provimuxMonotonic(now) {
		return false
	}
	lastUse := entry.expiresAt.Add(-cache.ttl)
	if lastUse.Before(note.seen) {
		return false
	}
	if now.Sub(lastUse) < coldAfter {
		return false
	}
	cache.removeAliasGroupLocked(entry)
	s.provimux.returns++
	for _, alias := range entry.aliases {
		s.provimux.returnedLocked(alias, s.provimux.returns)
	}
	provimuxReturned.Add(1)
	at := now
	provimuxReturnedLast.Store(&at)
	return true
}

// seenLocked notes that a request of key reached the rule at now.
func (r *provimuxSessionReturn) seenLocked(key string, now time.Time) {
	note := r.makeRoomLocked(key)
	note.seen = now
	r.notes[key] = note
}

// returnedLocked notes the number of the rule's return of a binding key was in.
func (r *provimuxSessionReturn) returnedLocked(key string, number uint64) {
	note := r.makeRoomLocked(key)
	note.returned = number
	r.notes[key] = note
}

// makeRoomLocked returns key's note, emptying the table first when it is full and key is not in it.
func (r *provimuxSessionReturn) makeRoomLocked(key string) provimuxReturnNote {
	if r.notes == nil {
		r.notes = make(map[string]provimuxReturnNote)
	}
	note, ok := r.notes[key]
	if !ok && len(r.notes) >= provimuxReturnNoteLimit {
		r.notes = make(map[string]provimuxReturnNote)
	}
	return note
}

// returnedOfLocked is the number of the rule's last return of a binding key was in, or 0; under the cache's lock.
func (r *provimuxSessionReturn) returnedOfLocked(key string) uint64 {
	return r.notes[key].returned
}

// ProvimuxSessionReturnState is the answer of GET /v8/management/provimux/session-return (SessionReturnState in
// session-route/openapi.yaml).
type ProvimuxSessionReturnState struct {
	ObservedAt string                        `json:"observed_at"`
	Setting    ProvimuxSessionReturnSet      `json:"setting"`
	InEffect   bool                          `json:"in_effect"`
	Reason     *string                       `json:"reason"`
	Affinity   ProvimuxSessionReturnAffinity `json:"affinity"`
	Counts     ProvimuxSessionReturnCounts   `json:"counts"`
}

// ProvimuxSessionReturnSet is SessionReturnState.setting.
type ProvimuxSessionReturnSet struct {
	ColdAfterS *float64 `json:"cold_after_s"`
}

// ProvimuxSessionReturnAffinity is SessionReturnState.affinity: upstream's settings the rule depends on, as they run.
type ProvimuxSessionReturnAffinity struct {
	Enabled   bool   `json:"enabled"`
	TTLS      int64  `json:"ttl_s"`
	Strategy  string `json:"strategy"`
	Subagents bool   `json:"subagents"`
	RebindOn  string `json:"rebind_on"`
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

// provimuxReturnColdAfterOf is cold-after as committed for m, and whether it is set.
func provimuxReturnColdAfterOf(m *Manager) (time.Duration, bool) {
	value := provimuxReturnCurrent.Load()
	if m == nil || value == nil || value.manager.Value() != m || !value.setting.ColdAfterSet || value.setting.ColdAfter <= 0 {
		return 0, false
	}
	return value.setting.ColdAfter, true
}

// provimuxSeconds is d in seconds, with its fraction: what the contract shows, never what the rule compares.
func provimuxSeconds(d time.Duration) *float64 {
	seconds := d.Seconds()
	return &seconds
}

// provimuxReturnReason is why the rule does not run for m now, or "" when it runs: the first that applies, in the
// contract's order. Each part holds when it holds as configured (the value Pick loads first) or as it runs (the
// manager's mode and selector, which Pick checks next), so "" is exactly when Pick's rule acts, a reload in between
// included.
func provimuxReturnReason(m *Manager) string {
	if m == nil {
		return provimuxReturnNotSet
	}
	value := provimuxReturnCurrent.Load()
	if value != nil && value.manager.Value() != m {
		value = nil
	}
	var setting ProvimuxSessionReturnSetting
	if value != nil {
		setting = value.setting
	}
	if m.HomeEnabled() || setting.Home {
		return provimuxReturnHomeMode
	}
	if m.hasPluginScheduler() {
		return provimuxReturnPluginScheduler
	}
	affinity, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || affinity == nil || affinity.cache == nil || (value != nil && !setting.Affinity) {
		return provimuxReturnAffinityOff
	}
	if value == nil || !setting.ColdAfterSet || setting.ColdAfter <= 0 {
		return provimuxReturnNotSet
	}
	if affinity.rebindOnNever || normalizeSessionAffinityRebindOn(setting.RebindOn) == SessionAffinityRebindOnNever {
		return provimuxReturnRebindNever
	}
	if setting.ColdAfter >= affinity.cache.ttl || (setting.TTL > 0 && setting.ColdAfter >= setting.TTL) {
		return provimuxReturnNotBelowTTL
	}
	if !value.possible || !provimuxReturnRuns(m, affinity, setting.ColdAfter) {
		return provimuxReturnNotSet // not reached: every condition of the two is named above
	}
	return ""
}

// provimuxStrategyOf names the strategy of a fallback selector as the configuration names it.
func provimuxStrategyOf(selector Selector) string {
	switch selector.(type) {
	case *FillFirstSelector:
		return "fill-first"
	case *WeightedRoundRobinSelector:
		return "weighted-round-robin"
	default:
		return "round-robin"
	}
}

// ProvimuxSessionReturnStateOf is the rule's state for m at now: the setting, the value in use and why, upstream's
// settings as they run, and the exact count of returns since the start. It reads; it changes nothing.
func ProvimuxSessionReturnStateOf(m *Manager, now time.Time) ProvimuxSessionReturnState {
	state := ProvimuxSessionReturnState{
		ObservedAt: provimuxTime(now),
		Affinity:   ProvimuxSessionReturnAffinity{TTLS: int64(time.Hour / time.Second), Strategy: "round-robin", Subagents: true, RebindOn: SessionAffinityRebindOnAny},
		Counts:     ProvimuxSessionReturnCounts{Returned: provimuxReturned.Load(), Since: provimuxTime(provimuxReturnSince)},
	}
	if last := provimuxReturnedLast.Load(); last != nil {
		state.Counts.LastReturnedAt = provimuxTimePtr(*last)
	}
	if coldAfter, set := provimuxReturnColdAfterOf(m); set {
		state.Setting.ColdAfterS = provimuxSeconds(coldAfter)
	}
	if value := provimuxReturnCurrent.Load(); value != nil && m != nil && value.manager.Value() == m {
		if value.setting.TTL > 0 {
			state.Affinity.TTLS = int64(value.setting.TTL / time.Second)
		}
		if value.setting.Strategy != "" {
			state.Affinity.Strategy = value.setting.Strategy
		}
		state.Affinity.Subagents = value.setting.Subagents
		state.Affinity.RebindOn = normalizeSessionAffinityRebindOn(value.setting.RebindOn)
	}
	if m != nil {
		switch selector := m.Selector().(type) {
		case *SessionAffinitySelector:
			if selector != nil && selector.cache != nil {
				state.Affinity.Enabled = true
				state.Affinity.TTLS = int64(selector.cache.ttl / time.Second)
				state.Affinity.Strategy = provimuxStrategyOf(selector.fallback)
				state.Affinity.Subagents = selector.subagentAffinity
				state.Affinity.RebindOn = SessionAffinityRebindOnAny
				if selector.rebindOnQuotaOnly {
					state.Affinity.RebindOn = SessionAffinityRebindOnQuota
				}
				if selector.rebindOnNever {
					state.Affinity.RebindOn = SessionAffinityRebindOnNever
				}
			}
		case nil:
		default:
			state.Affinity.Strategy = provimuxStrategyOf(selector)
		}
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
	if coldAfter, set := provimuxReturnColdAfterOf(m); set {
		rule.ColdAfterS = provimuxSeconds(coldAfter)
	}
	return rule
}
