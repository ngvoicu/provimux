package auth

import (
	"sync/atomic"
	"time"
	"weak"
)

// Provimux: session-route, the rule (.github/provimux/design/session-route.md 15). At a conversation's next request,
// inside upstream's selection (SessionAffinitySelector.Pick, one line before the binding is looked up), the binding is
// dropped when the operator has set cold-after, the client named the conversation, it is bound to an account below
// the highest-priority account this request may use, and no message of it was routed to an account for cold-after.
// That is read from the rule's own note: the time a request of the conversation last came to selection. A message
// refused before selection reaches no provider and leaves no note. A provider's prompt cache is refreshed when a
// request reads it, not while the answer is written, so the time since the last routed message is what tells whether
// the cache can still be warm. Whatever is not certain leaves the binding alone. No book,
// no result hook, no usage record, no timer, nothing from a file.
//
// The names of upstream's this file uses: the selector's cache, rebindOnNever and its own field provimux; the cache's
// mu, entries, ttl, ensureInitializedLocked and removeAliasGroupLocked; an entry's authID, expiresAt and aliases;
// sameSessionEntryGroup; authPriority; the manager's Selector, HomeEnabled and hasPluginScheduler.

// provimuxReturnNoteLimit is the number of keys the table of notes holds; at that size it is emptied (15.4). The marks
// of returns (for part one's expect) are kept apart and never emptied while their binding stands.
const provimuxReturnNoteLimit = 16384

// provimuxSessionReturn is the selector's own part of the rule, guarded by the selector's cache's mu; the zero value is
// ready. seen is K7: when a request of each key last came to selection. returned is the number of the rule's last
// return of a binding each key was in.
type provimuxSessionReturn struct {
	generation uint64 // the setting's generation the notes were written under
	seen       map[string]time.Time
	returned   map[string]uint64
	returns    uint64 // the last return number given by this selector
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

// provimuxReturnValue is the value in use: the setting, the manager it was committed for, whether the configuration
// lets the rule run at all, and its generation, which changes whenever cold-after or that answer changes. One atomic
// value, loaded once at the top of each call.
type provimuxReturnValue struct {
	setting    ProvimuxSessionReturnSetting
	manager    weak.Pointer[Manager]
	possible   bool
	generation uint64
}

var (
	provimuxReturnCurrent    atomic.Pointer[provimuxReturnValue]
	provimuxReturnGeneration atomic.Uint64
	// provimuxReturned counts the bindings the rule dropped since startup: raised in the step that drops one.
	provimuxReturned     atomic.Int64
	provimuxReturnedLast atomic.Pointer[time.Time]
	provimuxReturnSince  = time.Now()
)

// ProvimuxSetSessionReturn puts setting in force for m, where the service commits a configuration and for the first
// configuration of a start. A nil m puts the rule out of force. When the value in use changes (set, changed, removed,
// set again), a new observation starts: every selector empties its notes at its next call, so the first message of
// each conversation after that is not judged.
func ProvimuxSetSessionReturn(m *Manager, setting ProvimuxSessionReturnSetting) {
	if m == nil {
		provimuxReturnCurrent.Store(nil)
		return
	}
	possible := setting.ColdAfterSet && setting.ColdAfter > 0 && setting.Affinity && !setting.Home &&
		normalizeSessionAffinityRebindOn(setting.RebindOn) != SessionAffinityRebindOnNever &&
		(setting.TTL <= 0 || setting.ColdAfter < setting.TTL)
	value := &provimuxReturnValue{setting: setting, manager: weak.Make(m), possible: possible}
	if previous := provimuxReturnCurrent.Load(); previous != nil && previous.manager.Value() == m &&
		previous.possible == possible && previous.setting.ColdAfter == setting.ColdAfter {
		value.generation = previous.generation
	} else {
		value.generation = provimuxReturnGeneration.Add(1)
	}
	provimuxReturnCurrent.Store(value)
}

// SetProvimuxSessionReturnForTest puts a setting in force for m and returns the function that puts the previous value
// back. For tests only.
func SetProvimuxSessionReturnForTest(m *Manager, setting ProvimuxSessionReturnSetting) (restore func()) {
	previous := provimuxReturnCurrent.Load()
	ProvimuxSetSessionReturn(m, setting)
	return func() { provimuxReturnCurrent.Store(previous) }
}

// ProvimuxSessionReturnAgeForTest moves every note of the rule on m's selector d into the past, as if d had passed
// since each message. For tests only.
func ProvimuxSessionReturnAgeForTest(m *Manager, d time.Duration) {
	s, ok := m.Selector().(*SessionAffinitySelector)
	if !ok || s == nil || s.cache == nil {
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
	return !s.rebindOnNever && coldAfter > 0 && coldAfter < s.cache.ttl && !m.HomeEnabled() && !m.hasPluginScheduler()
}

// provimuxReturn is the rule (15.3), called by Pick right before it looks the binding up. named is K2 (the client
// named the conversation and it is neither a sub-agent nor a fork); cacheKey and fallbackKey are Pick's keys;
// available is the list Pick chooses from for this request; now is the time Pick took. It returns nothing: it either
// dropped the binding or did not, and Pick's own lines decide everything after it. Not in effect, it is one atomic
// load and a return. Every request that passes K1 and K2 is noted, judged or not, so a note is always the
// conversation's last message routed to an account.
func (s *SessionAffinitySelector) provimuxReturn(named bool, cacheKey, fallbackKey string, available []*Auth, now time.Time) {
	value := provimuxReturnCurrent.Load()
	if value == nil || !value.possible || !named || s == nil || cacheKey == "" { // K1 (as configured), K2
		return
	}
	coldAfter := value.setting.ColdAfter
	runs := provimuxReturnRuns(value.manager.Value(), s, coldAfter) // K1 (as it runs)
	cache := s.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.ensureInitializedLocked()
	if s.provimux.generation != value.generation || s.provimux.seen == nil || len(s.provimux.seen) >= provimuxReturnNoteLimit {
		s.provimux.generation, s.provimux.seen = value.generation, make(map[string]time.Time) // a new observation
	}
	if runs {
		s.provimuxJudgeLocked(cache, cacheKey, fallbackKey, available, now, coldAfter)
	}
	s.provimux.seen[cacheKey] = now // K7
}

// provimuxJudgeLocked is K3 to K6 and the comparison, under the cache's write lock; it reports whether it dropped
// the binding.
func (s *SessionAffinitySelector) provimuxJudgeLocked(cache *SessionCache, cacheKey, fallbackKey string, available []*Auth, now time.Time, coldAfter time.Duration) bool {
	entry, ok := cache.entries[cacheKey] // K3: a live binding
	if !ok || entry.authID == "" || !now.Before(entry.expiresAt) {
		return false
	}
	if fallbackKey != "" && fallbackKey != cacheKey { // K4: the second key is in the same binding
		if other, okOther := cache.entries[fallbackKey]; okOther && other.authID != "" && now.Before(other.expiresAt) && !sameSessionEntryGroup(other, entry) {
			return false
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
		return false
	}
	var last time.Time // K6: the conversation's last routed message, through any key of its binding
	for _, key := range append([]string{cacheKey}, entry.aliases...) {
		if seen := s.provimux.seen[key]; seen.After(last) {
			last = seen
		}
	}
	if last.IsZero() || now.Sub(last) < coldAfter {
		return false
	}
	cache.removeAliasGroupLocked(entry)
	s.provimux.returns++
	s.provimux.markReturnedLocked(cache, entry.aliases)
	provimuxReturned.Add(1)
	at := now
	provimuxReturnedLast.Store(&at)
	return true
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
