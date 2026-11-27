package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Provimux: the tests of the rule of session-route (.github/provimux/design/session-route.md 15.3 and 15.10). Every
// account, key and session is invented; every executor is a stand-in in this process; nothing is sent anywhere. No
// test sleeps and none depends on the machine's speed or the date: the rule's own function is called with a time the
// test chooses from the entry it made, and through a real Pick the bindings and notes are moved into the past by
// margins of hours that a test run cannot cross (ProvimuxSessionReturnAgeForTest).

const (
	provimuxRTCold = 2 * time.Hour
	provimuxRTTTL  = 4 * time.Hour
)

// calls is what the stand-in executor was called with, in order.
type provimuxRTCalls struct {
	mu  sync.Mutex
	ids []string
}

func (c *provimuxRTCalls) call(_ context.Context, auth *Auth, _ cliproxyexecutor.Request) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, auth.ID)
	return nil
}

func (c *provimuxRTCalls) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.ids
	c.ids = nil
	return out
}

// withTTL gives f a new selector of the same strategy with ttl, as a configuration that changes the binding lifetime
// does (upstream makes a new selector).
func (f *provimuxSRFixture) withTTL(ttl time.Duration) {
	f.t.Helper()
	next := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: f.affinity.fallback, TTL: ttl})
	f.t.Cleanup(next.Stop)
	f.m.SetSelector(next)
	f.affinity = next
}

// rule puts cold-after in force for f's manager, as the service's hook does for a configuration that sets it.
func (f *provimuxSRFixture) rule(coldAfter time.Duration) {
	ProvimuxSetSessionReturn(f.m, coldAfter)
	f.t.Cleanup(func() { ProvimuxSetSessionReturn(nil, 0) })
}

// age moves every binding and note d into the past.
func (f *provimuxSRFixture) age(d time.Duration) { ProvimuxSessionReturnAgeForTest(f.m, d) }

// fellBack makes the conversation fall back: the high account cools, a turn binds it to the low one, the high one
// recovers. It returns upstream's key of the binding.
func (f *provimuxSRFixture) fellBack(header http.Header, body string) string {
	f.t.Helper()
	f.coolHigh(30 * time.Minute)
	if err := f.turn(header, body, provimuxSRScope("sk-client-one"), nil); err != nil {
		f.t.Fatalf("turn: %v", err)
	}
	expireSessionAffinityPriorityModelCooldown(f.t, f.m, f.high, provimuxSRModel)
	entries := f.book.Entries()
	if len(entries) != 1 {
		f.t.Fatalf("book has %d entries, want 1", len(entries))
	}
	key := entries[0].Key
	if authID, ok := f.binding(key); !ok || authID != f.low {
		f.t.Fatalf("after falling back the binding is %q (%v), want %q", authID, ok, f.low)
	}
	return key
}

// notes is the number of keys the rule's table holds.
func (f *provimuxSRFixture) notes() int {
	f.affinity.cache.mu.RLock()
	defer f.affinity.cache.mu.RUnlock()
	return len(f.affinity.provimux.seen)
}

// entryOf is upstream's entry under key now.
func (f *provimuxSRFixture) entryOf(key string) sessionEntry {
	f.affinity.cache.mu.RLock()
	defer f.affinity.cache.mu.RUnlock()
	return f.affinity.cache.entries[key]
}

// noteOf is the rule's note of key: when a request of it last came to selection.
func (f *provimuxSRFixture) noteOf(key string) time.Time {
	f.affinity.cache.mu.RLock()
	defer f.affinity.cache.mu.RUnlock()
	return f.affinity.provimux.seen[key]
}

// auths is the two accounts, as the conductor hands them to Pick.
func (f *provimuxSRFixture) auths() []*Auth {
	return []*Auth{mustAuth(f.t, f.m, f.high), mustAuth(f.t, f.m, f.low)}
}

// TestProvimuxSessionReturnSevenThings is the table of 15.3: each of K1 to K7, and the comparison, with what it must
// know and its "not certain: nothing". The rule's function is called with a time chosen from the conversation's note.
// Each row starts from a conversation bound below a free higher-priority account, whose last message was at sent.
func TestProvimuxSessionReturnSevenThings(t *testing.T) {
	header := http.Header{"X-Session-Affinity": {"pi-seven"}}
	type setup struct {
		f    *provimuxSRFixture
		key  string
		sent time.Time // the conversation's last message, as the rule noted it
	}
	start := func(t *testing.T) setup {
		f := newProvimuxSRFixture(t, "round-robin", false, nil)
		f.withTTL(provimuxRTTTL)
		f.rule(provimuxRTCold)
		key := f.fellBack(header, "")
		return setup{f: f, key: key, sent: f.noteOf(key)}
	}
	cold := func(s setup) time.Time { return s.sent.Add(provimuxRTCold) }
	rows := []struct {
		name    string
		prepare func(t *testing.T, s setup) (named bool, fallbackKey string, available []*Auth, now time.Time)
		removed bool
		noted   bool // the call wrote its note (it passed K1 and K2)
	}{
		{name: "the comparison: unused for exactly cold-after: dropped", removed: true, noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) { return true, "", s.f.auths(), cold(s) }},
		{name: "the comparison: one nanosecond less: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				return true, "", s.f.auths(), cold(s).Add(-time.Nanosecond)
			}},
		{name: "K1: not set: nothing, no note",
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				ProvimuxSetSessionReturn(s.f.m, 0)
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K1: not below the binding lifetime: nothing, the message noted", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				s.f.rule(provimuxRTTTL)
				return true, "", s.f.auths(), s.sent.Add(provimuxRTTTL - time.Nanosecond)
			}},
		{name: "K1: rebind-on never: nothing, the message noted", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				s.f.affinity.rebindOnNever = true
				t.Cleanup(func() { s.f.affinity.rebindOnNever = false })
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K1: Home mode: nothing, the message noted", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				cfg := &internalconfig.Config{}
				cfg.Home.Enabled = true
				s.f.m.SetConfig(cfg)
				t.Cleanup(func() { s.f.m.SetConfig(&internalconfig.Config{}) })
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K1: a plugin scheduler: nothing, the message noted", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				s.f.m.SetPluginScheduler(provimuxSRScheduler{})
				t.Cleanup(func() { s.f.m.SetPluginScheduler(nil) })
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K2: not named, a sub-agent or a fork: nothing, no note",
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) { return false, "", s.f.auths(), cold(s) }},
		{name: "K3: no binding under the key: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				s.f.affinity.cache.mu.Lock()
				s.f.affinity.cache.removeAliasGroupLocked(s.f.affinity.cache.entries[s.key])
				s.f.affinity.cache.mu.Unlock()
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K3: an expired binding: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				return true, "", s.f.auths(), s.f.entryOf(s.key).expiresAt
			}},
		{name: "K4: the second key is another binding's: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				other := strings.Replace(s.key, "pi-seven", "pi-other", 1)
				s.f.affinity.cache.SetAliases(s.f.high, other)
				return true, other, s.f.auths(), cold(s)
			}},
		{name: "K4: the second key is the same binding's: dropped", removed: true, noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				return true, s.key, s.f.auths(), cold(s)
			}},
		{name: "K5: the bound account is not in the request's list: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				return true, "", []*Auth{mustAuth(t, s.f.m, s.f.high)}, cold(s)
			}},
		{name: "K5: nothing in the list is above the bound account (a pinned request's list): nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				return true, "", []*Auth{mustAuth(t, s.f.m, s.f.low)}, cold(s)
			}},
		{name: "K5: equal priorities: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				high := mustAuth(t, s.f.m, s.f.high)
				high.Attributes["priority"] = "5"
				return true, "", []*Auth{high, mustAuth(t, s.f.m, s.f.low)}, cold(s)
			}},
		{name: "K6: no note of this key in this process: nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				s.f.affinity.cache.mu.Lock()
				delete(s.f.affinity.provimux.seen, s.key)
				s.f.affinity.cache.mu.Unlock()
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K6: the binding's expiry plays no part (refreshed just now by a result): dropped", removed: true, noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				cache := s.f.affinity.cache
				cache.mu.Lock()
				entry := cache.entries[s.key]
				cache.replaceAliasGroupsLocked(entry.authID, cold(s).Add(provimuxRTTTL), entry.aliases, entry.binding, entry)
				cache.mu.Unlock()
				return true, "", s.f.auths(), cold(s)
			}},
		{name: "K6: a new observation after the setting changed (off, then on): nothing", noted: true,
			prepare: func(t *testing.T, s setup) (bool, string, []*Auth, time.Time) {
				ProvimuxSetSessionReturn(s.f.m, 0)
				s.f.rule(provimuxRTCold)
				return true, "", s.f.auths(), cold(s)
			}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s := start(t)
			named, fallbackKey, available, now := row.prepare(t, s)
			before := provimuxReturned.Load()
			s.f.affinity.cache.mu.RLock()
			noteBefore := s.f.affinity.provimux.seen[s.key]
			s.f.affinity.cache.mu.RUnlock()
			s.f.affinity.provimuxReturn(named, s.key, fallbackKey, available, now)
			_, bound := s.f.binding(s.key)
			removed := provimuxReturned.Load() - before
			if row.removed && (bound || removed != 1) {
				t.Fatalf("want dropped and counted once; bound %v, counted %d", bound, removed)
			}
			if !row.removed && removed != 0 {
				t.Fatalf("want nothing; counted %d", removed)
			}
			if !row.removed && !strings.HasPrefix(row.name, "K3") && !bound {
				t.Fatal("want nothing; the binding is gone")
			}
			s.f.affinity.cache.mu.RLock()
			noteAfter := s.f.affinity.provimux.seen[s.key]
			s.f.affinity.cache.mu.RUnlock()
			if noted := !noteAfter.Equal(noteBefore) && noteAfter.Equal(now); noted != row.noted {
				t.Fatalf("note written %v, want %v", noted, row.noted)
			}
		})
	}
}

// TestProvimuxSessionReturnNoteTable is K7's table: written under the cache's lock, on the selector, emptied when it
// reaches its size, and after that the first request of a key is not judged (it has no note). Finding 3 of the
// second review: the emptying does not touch the binding's life, so the show still gives the token it gave, and that
// token still releases the binding.
func TestProvimuxSessionReturnNoteTable(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	f.withTTL(provimuxRTTTL)
	f.rule(provimuxRTCold)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"pi-table"}}
	key := f.fellBack(header, "")
	f.age(provimuxRTCold + time.Hour)
	if err := f.turn(header, "", scope, nil); err != nil { // returned: the high account now, with a return mark
		t.Fatal(err)
	}
	shown := f.book.Show(scope, header, nil, provimuxSRModel)
	if authID, _ := f.binding(key); authID != f.high || shown.Binding == nil {
		t.Fatalf("after the return: bound %q, show %s", authID, mustJSON(t, shown))
	}
	f.affinity.cache.mu.Lock()
	for i := 0; len(f.affinity.provimux.seen) < provimuxReturnNoteLimit; i++ {
		f.affinity.provimux.seen["filler-"+strconv.Itoa(i)] = time.Now()
	}
	f.affinity.cache.mu.Unlock()
	if err := f.turn(http.Header{"X-Session-Affinity": {"pi-other"}}, "", scope, nil); err != nil { // empties the table
		t.Fatal(err)
	}
	if n := f.notes(); n != 1 {
		t.Fatalf("notes %d after the table filled; want it emptied and one new note", n)
	}
	again := f.book.Show(scope, header, nil, provimuxSRModel)
	if again.Binding == nil || *again.Binding != *shown.Binding {
		t.Fatalf("the show lost the binding's token after the table emptied: %s", mustJSON(t, again))
	}
	if released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, *shown.Binding); conflict != nil || !released.Released {
		t.Fatalf("the token just shown did not release: %s %v", mustJSON(t, released), conflict != nil)
	}
}

// TestProvimuxSessionReturnPin is rows 1 and 2 of 15.10: through a real Manager and selector, under each strategy, on
// one provider and on two, for each kind of named conversation (sub-agents apart), a conversation bound below and
// unused for cold-after goes to the highest priority at its next request, and one used within it does not; with the
// setting absent the same samples select as upstream does and the rule writes nothing.
func TestProvimuxSessionReturnPin(t *testing.T) {
	for _, set := range []bool{true, false} {
		for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin"} {
			for _, mixed := range []bool{false, true} {
				for _, kind := range provimuxSRKinds {
					if kind.subagent {
						continue
					}
					name := strategy + "/single/" + kind.name
					if mixed {
						name = strategy + "/mixed/" + kind.name
					}
					if !set {
						name = "unset/" + name
					}
					t.Run(name, func(t *testing.T) {
						calls := &provimuxRTCalls{}
						f := newProvimuxSRFixture(t, strategy, mixed, calls.call)
						f.withTTL(provimuxRTTTL)
						if set {
							f.rule(provimuxRTCold)
						}
						key := f.fellBack(kind.header, kind.body)
						calls.take()
						before := provimuxReturned.Load()

						f.age(provimuxRTCold - time.Hour)
						if err := f.turn(kind.header, kind.body, "", nil); err != nil {
							t.Fatal(err)
						}
						if got := calls.take(); len(got) != 1 || got[0] != f.low {
							t.Fatalf("used within cold-after: called %v, want the low account", got)
						}

						f.age(provimuxRTCold + time.Hour)
						if err := f.turn(kind.header, kind.body, "", nil); err != nil {
							t.Fatal(err)
						}
						got := calls.take()
						authID, _ := f.binding(key)
						returned := provimuxReturned.Load() - before
						if set && (len(got) != 1 || got[0] != f.high || authID != f.high || returned != 1) {
							t.Fatalf("unused for cold-after: called %v, bound %q, counted %d; want the high account, once", got, authID, returned)
						}
						if !set && (len(got) != 1 || got[0] != f.low || authID != f.low || returned != 0 || f.notes() != 0) {
							t.Fatalf("setting absent: called %v, bound %q, counted %d, notes %d; want upstream's selection and nothing written", got, authID, returned, f.notes())
						}
					})
				}
			}
		}
	}
}

// TestProvimuxSessionReturnLeftAlone is row 4 of 15.10: a pinned request never drops a binding, nor a sub-agent, a
// fork, a conversation without a session id, or one the prefix matcher binds by its content; and nothing is dropped
// for a conversation the rule has not seen since it was switched on.
func TestProvimuxSessionReturnLeftAlone(t *testing.T) {
	type turnOf func(f *provimuxSRFixture) error
	plain := func(header http.Header, body string, extra map[string]any) turnOf {
		return func(f *provimuxSRFixture) error { return f.turn(header, body, "", extra) }
	}
	content := func(f *provimuxSRFixture, messages string) error {
		payload := []byte(`{"model":"` + provimuxSRModel + `","messages":[` + messages + `]}`)
		opts := cliproxyexecutor.Options{OriginalRequest: payload, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{}}
		_, err := f.m.Execute(context.Background(), f.providers, cliproxyexecutor.Request{Model: provimuxSRModel, Payload: payload}, opts)
		return err
	}
	first := `{"role":"system","content":"You are a synthetic fixture with a long enough system prompt for the prefix matcher."},{"role":"user","content":"first question of a synthetic conversation"}`
	rows := []struct {
		name        string
		first, next turnOf
		ruleAfter   bool
	}{
		{name: "pinned", first: plain(http.Header{"X-Session-Affinity": {"pi-pinned"}}, "", nil),
			next: func(f *provimuxSRFixture) error {
				return f.turn(http.Header{"X-Session-Affinity": {"pi-pinned"}}, "", "", map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: f.low})
			}},
		{name: "sub-agent", first: plain(http.Header{"X-Claude-Code-Session-Id": {"cc-child"}, "X-Claude-Code-Agent-Id": {"agent-1"}}, "", nil),
			next: plain(http.Header{"X-Claude-Code-Session-Id": {"cc-child"}, "X-Claude-Code-Agent-Id": {"agent-1"}}, "", nil)},
		{name: "fork", first: plain(http.Header{"X-Codex-Turn-Metadata": {`{"session_id":"s","thread_id":"t","forked_from_thread_id":"p"}`}}, "", nil),
			next: plain(http.Header{"X-Codex-Turn-Metadata": {`{"session_id":"s","thread_id":"t","forked_from_thread_id":"p"}`}}, "", nil)},
		{name: "no session id (derived identity)", first: plain(nil, "", nil), next: plain(nil, "", nil)},
		{name: "no session id (content)", first: func(f *provimuxSRFixture) error { return content(f, first) },
			next: func(f *provimuxSRFixture) error {
				return content(f, first+`,{"role":"assistant","content":"ok"},{"role":"user","content":"second question"}`)
			}},
		{name: "first request after the rule was switched on", ruleAfter: true,
			first: plain(http.Header{"X-Session-Affinity": {"pi-switched"}}, "", nil), next: plain(http.Header{"X-Session-Affinity": {"pi-switched"}}, "", nil)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			calls := &provimuxRTCalls{}
			f := newProvimuxSRFixture(t, "fill-first", false, calls.call)
			f.withTTL(provimuxRTTTL)
			if !row.ruleAfter {
				f.rule(provimuxRTCold)
			}
			f.coolHigh(30 * time.Minute)
			if err := row.first(f); err != nil {
				t.Fatal(err)
			}
			expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
			if got := calls.take(); len(got) == 0 || got[len(got)-1] != f.low {
				t.Fatalf("first turn called %v, want the low account", got)
			}
			if row.ruleAfter {
				f.rule(provimuxRTCold)
			}
			before := provimuxReturned.Load()
			f.age(provimuxRTCold + time.Hour)
			if err := row.next(f); err != nil {
				t.Fatal(err)
			}
			if got := calls.take(); len(got) != 1 || got[0] != f.low || provimuxReturned.Load() != before {
				t.Fatalf("next turn called %v, counted %d; want the low account and nothing dropped", got, provimuxReturned.Load()-before)
			}
		})
	}
}

// TestProvimuxSessionReturnAliases is row 5 (finding 6): two aliases of one binding used in turn; the last use is the
// group's, whichever alias asks, and a return drops every alias.
func TestProvimuxSessionReturnAliases(t *testing.T) {
	calls := &provimuxRTCalls{}
	f := newProvimuxSRFixture(t, "fill-first", false, calls.call)
	f.withTTL(provimuxRTTTL)
	f.rule(provimuxRTCold)
	pair := `,"prompt_cache_key":"aliasp","conversation":"aliasc"`
	key := f.fellBack(nil, pair)
	aliases := f.aliasesOf(key)
	if len(aliases) != 2 {
		t.Fatalf("group %v, want two aliases", aliases)
	}
	before := provimuxReturned.Load()
	f.age(time.Hour)
	if err := f.turn(nil, `,"conversation":"aliasc"`, "", nil); err != nil { // the other alias, within cold-after
		t.Fatal(err)
	}
	f.age(provimuxRTCold - 30*time.Minute) // the first alias unused for 2h30m, the group for 1h30m
	if err := f.turn(nil, pair, "", nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.take(); got[len(got)-1] != f.low || provimuxReturned.Load() != before {
		t.Fatalf("the group was used within cold-after through the other alias, and was moved: %v", got)
	}
	f.age(provimuxRTCold + time.Hour)
	if err := f.turn(nil, `,"conversation":"aliasc"`, "", nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.take(); got[len(got)-1] != f.high || provimuxReturned.Load()-before != 1 {
		t.Fatalf("unused for cold-after: called %v, counted %d", got, provimuxReturned.Load()-before)
	}
	for _, alias := range aliases {
		if authID, ok := f.binding(alias); ok && authID == f.low {
			t.Fatalf("alias %s still bound to the low account after the return", alias)
		}
	}
	asking := aliases[0]
	if !strings.Contains(asking, "aliasc") {
		asking = aliases[1]
	}
	if authID, ok := f.binding(asking); !ok || authID != f.high {
		t.Fatalf("the asking alias %s is bound %q after the return, want the high account", asking, authID)
	}
}

// TestProvimuxSessionReturnRestored is row 6, second half (finding 7): a binding written before this selector existed
// (a new selector after a change of the routing settings, a restore after a restart) has no note, so it is not
// judged at its first request, whatever its times say; from its second request on it is.
func TestProvimuxSessionReturnRestored(t *testing.T) {
	for _, which := range []string{"new selector, longer lifetime", "new selector, shorter lifetime"} {
		t.Run(which, func(t *testing.T) {
			calls := &provimuxRTCalls{}
			f := newProvimuxSRFixture(t, "round-robin", false, calls.call)
			f.withTTL(provimuxRTTTL)
			f.rule(provimuxRTCold)
			header := http.Header{"X-Session-Affinity": {"pi-restored"}}
			key := f.fellBack(header, "")
			entry := f.entryOf(key)
			ttl := 2 * provimuxRTTTL
			if strings.HasSuffix(which, "shorter lifetime") {
				ttl = provimuxRTTTL - time.Hour
			}
			f.withTTL(ttl) // the binding is carried into the new selector, which has no note of it
			f.rule(provimuxRTCold)
			f.affinity.cache.mu.Lock()
			f.affinity.cache.ensureInitializedLocked()
			f.affinity.cache.replaceAliasGroupsLocked(entry.authID, entry.expiresAt.Add(-(provimuxRTCold + time.Hour)), entry.aliases, entry.binding)
			f.affinity.cache.mu.Unlock()
			calls.take()
			before := provimuxReturned.Load()
			if err := f.turn(header, "", "", nil); err != nil {
				t.Fatal(err)
			}
			if got := calls.take(); len(got) != 1 || got[0] != f.low || provimuxReturned.Load() != before {
				t.Fatalf("first request after the restore: called %v; want it left on the low account", got)
			}
			f.age(provimuxRTCold + time.Minute)
			if err := f.turn(header, "", "", nil); err != nil {
				t.Fatal(err)
			}
			if got := calls.take(); len(got) != 1 || got[0] != f.high || provimuxReturned.Load()-before != 1 {
				t.Fatalf("second request, unused for cold-after: called %v; want the high account", got)
			}
		})
	}
}

// TestProvimuxSessionReturnBounds is row 7 (finding 8): cold-after at 1m0.9s is kept to the nanosecond: dropped
// exactly from that time on, never before.
func TestProvimuxSessionReturnBounds(t *testing.T) {
	cold := time.Minute + 900*time.Millisecond
	for _, early := range []bool{true, false} {
		f := newProvimuxSRFixture(t, "round-robin", false, nil)
		f.withTTL(time.Hour)
		f.rule(cold)
		key := f.fellBack(http.Header{"X-Session-Affinity": {"pi-bounds"}}, "")
		now := f.noteOf(key).Add(cold)
		if early {
			now = now.Add(-time.Nanosecond)
		}
		before := provimuxReturned.Load()
		f.affinity.provimuxReturn(true, key, "", f.auths(), now)
		if dropped := provimuxReturned.Load() - before; (dropped == 1) == early {
			t.Fatalf("at cold-after%s: dropped %d", map[bool]string{true: " less 1ns", false: ""}[early], dropped)
		}
	}
}

// TestProvimuxSessionReturnHeldRequest is row 8 (finding 4): a request held in an executor on the low account for
// longer than cold-after, while a second request of the conversation returns it: the held one's success and its
// failure both leave the new binding.
func TestProvimuxSessionReturnHeldRequest(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			entered := make(chan string, 1)
			gate := make(chan error, 1)
			calls := &provimuxRTCalls{}
			f := newProvimuxSRFixture(t, "round-robin", false, func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request) error {
				if strings.Contains(string(req.Payload), `"hold":true`) {
					entered <- auth.ID
					return <-gate
				}
				return calls.call(ctx, auth, req)
			})
			f.withTTL(provimuxRTTTL)
			f.rule(provimuxRTCold)
			header := http.Header{"X-Session-Affinity": {"pi-held"}}
			key := f.fellBack(header, "")
			pinned := map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: f.low} // the held one does not retry elsewhere
			done := make(chan error, 1)
			go func() { done <- f.turn(header, `,"hold":true`, "", pinned) }()
			if started := <-entered; started != f.low {
				t.Fatalf("held request started on %s", started)
			}
			f.age(provimuxRTCold + time.Hour)
			before := provimuxReturned.Load()
			if err := f.turn(header, "", "", nil); err != nil {
				t.Fatal(err)
			}
			if authID, _ := f.binding(key); authID != f.high || provimuxReturned.Load()-before != 1 {
				t.Fatalf("second request: bound %q, want returned to the high account", authID)
			}
			if outcome == "success" {
				gate <- nil
			} else {
				gate <- &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic quota"}
			}
			<-done
			if authID, ok := f.binding(key); !ok || authID != f.high {
				t.Fatalf("after the held request's %s the binding is %q (%v), want the new one on the high account", outcome, authID, ok)
			}
		})
	}
}

// TestProvimuxSessionReturnCountRacing is row 9: with requests of one conversation racing, and a client's release
// racing them, the binding below is dropped once, and the counter equals the number of drops. Run under -race.
func TestProvimuxSessionReturnCountRacing(t *testing.T) {
	const conversations, requests = 8, 6
	for _, withRelease := range []bool{false, true} {
		t.Run(map[bool]string{false: "requests", true: "requests and a client's release"}[withRelease], func(t *testing.T) {
			f := newProvimuxSRFixture(t, "round-robin", false, nil)
			f.withTTL(provimuxRTTTL)
			f.rule(provimuxRTCold)
			scope := provimuxSRScope("sk-client-one")
			f.coolHigh(30 * time.Minute)
			headers := make([]http.Header, conversations)
			for i := range headers {
				headers[i] = http.Header{"X-Session-Affinity": {"pi-race-" + string(rune('a'+i))}}
				if err := f.turn(headers[i], "", scope, nil); err != nil {
					t.Fatal(err)
				}
			}
			expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
			f.age(provimuxRTCold + time.Hour)
			before := provimuxReturned.Load()
			var wg sync.WaitGroup
			var mu sync.Mutex
			releasedLow := 0
			for _, header := range headers {
				for r := 0; r < requests; r++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if err := f.turn(header, "", scope, nil); err != nil {
							t.Error(err)
						}
					}()
				}
				if withRelease {
					wg.Add(1)
					go func() {
						defer wg.Done()
						released, _ := f.book.Release(scope, header, nil, provimuxSRModel, "")
						if released.Released && released.Was != nil && released.Was.Priority == 5 {
							mu.Lock()
							releasedLow++
							mu.Unlock()
						}
					}()
				}
			}
			wg.Wait()
			byRule := int(provimuxReturned.Load() - before)
			if byRule+releasedLow != conversations {
				t.Fatalf("the rule dropped %d and clients released %d bindings on the low account; want %d in all, each once", byRule, releasedLow, conversations)
			}
			for _, entry := range f.book.Entries() {
				if authID, ok := f.binding(entry.Key); ok && authID == f.low {
					t.Fatalf("%s still bound low", entry.Key)
				}
			}
		})
	}
}

// TestProvimuxSessionReturnEndsTheLife: a return by the rule ends the binding's life for expect (15.11: "to its
// removal by anyone"), and the stretch after it starts at the first route: a token shown before the return does not
// release the binding made after it, even when the request fell back to the same account again.
func TestProvimuxSessionReturnEndsTheLife(t *testing.T) {
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"pi-life"}}
	failHigh := false
	var mu sync.Mutex
	f := newProvimuxSRFixture(t, "round-robin", false, func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request) error {
		mu.Lock()
		defer mu.Unlock()
		if failHigh && strings.HasPrefix(auth.ID, "sr-high-") {
			return &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic quota"}
		}
		return nil
	})
	f.withTTL(provimuxRTTTL)
	f.rule(provimuxRTCold)
	key := f.fellBack(header, "")
	old := f.book.Show(scope, header, nil, provimuxSRModel)
	if old.Binding == nil || old.Bound.Origin != "first" {
		t.Fatalf("show: %s", mustJSON(t, old))
	}
	f.age(provimuxRTCold + time.Hour)
	mu.Lock()
	failHigh = true // the return's request fails on the high account and falls back to the low one again
	mu.Unlock()
	before := provimuxReturned.Load()
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	if authID, _ := f.binding(key); authID != f.low || provimuxReturned.Load()-before != 1 {
		t.Fatalf("bound %q, counted %d; want returned, then fallen back to the low account", authID, provimuxReturned.Load()-before)
	}
	now := f.book.Show(scope, header, nil, provimuxSRModel)
	if now.Binding == nil || *now.Binding == *old.Binding {
		t.Fatalf("the token did not change across the rule's return: %s", mustJSON(t, now))
	}
	if _, conflict := f.book.Release(scope, header, nil, provimuxSRModel, *old.Binding); conflict == nil {
		t.Fatal("the token shown before the return released the binding made after it")
	}
	if released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, *now.Binding); conflict != nil || !released.Released {
		t.Fatalf("the current token: %s %v", mustJSON(t, released), conflict != nil)
	}
}

// TestProvimuxSessionReturnState is row 11: the state names each reason of 15.6, in the contract's order, and the rule
// does nothing where the state says it is not in effect; in effect, it says so. With affinity off there is no selector
// of affinity in use to ask.
func TestProvimuxSessionReturnState(t *testing.T) {
	rows := []struct {
		reason  string
		prepare func(t *testing.T, f *provimuxSRFixture)
	}{
		{reason: "", prepare: func(t *testing.T, f *provimuxSRFixture) {}},
		{reason: provimuxReturnHomeMode, prepare: func(t *testing.T, f *provimuxSRFixture) {
			cfg := &internalconfig.Config{}
			cfg.Home.Enabled = true
			f.m.SetConfig(cfg)
		}},
		{reason: provimuxReturnPluginScheduler, prepare: func(t *testing.T, f *provimuxSRFixture) {
			f.m.SetPluginScheduler(provimuxSRScheduler{})
		}},
		{reason: provimuxReturnAffinityOff, prepare: func(t *testing.T, f *provimuxSRFixture) {
			f.m.SetSelector(&RoundRobinSelector{})
		}},
		{reason: provimuxReturnNotSet, prepare: func(t *testing.T, f *provimuxSRFixture) {
			ProvimuxSetSessionReturn(f.m, 0)
		}},
		{reason: provimuxReturnRebindNever, prepare: func(t *testing.T, f *provimuxSRFixture) {
			never := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: provimuxRTTTL, RebindOn: "never"})
			t.Cleanup(never.Stop)
			f.m.SetSelector(never)
			f.affinity = never
		}},
		{reason: provimuxReturnNotBelowTTL, prepare: func(t *testing.T, f *provimuxSRFixture) {
			f.rule(provimuxRTTTL)
		}},
	}
	for _, row := range rows {
		name := row.reason
		if name == "" {
			name = "in effect"
		}
		t.Run(name, func(t *testing.T) {
			f := newProvimuxSRFixture(t, "round-robin", false, nil)
			f.withTTL(provimuxRTTTL)
			header := http.Header{"X-Session-Affinity": {"pi-state"}}
			f.rule(provimuxRTCold)
			key := f.fellBack(header, "")
			row.prepare(t, f)
			state := ProvimuxSessionReturnStateOf(f.m, time.Now())
			reason := ""
			if state.Reason != nil {
				reason = *state.Reason
			}
			if reason != row.reason || state.InEffect != (row.reason == "") {
				t.Fatalf("state %s, want reason %q", mustJSON(t, state), row.reason)
			}
			if rule := provimuxSessionRuleOf(f.m); rule.InEffect != state.InEffect {
				t.Fatalf("the show's rule %+v disagrees with the state", rule)
			}
			if row.reason == provimuxReturnAffinityOff {
				return // no selector of affinity selects, so none is asked
			}
			before := provimuxReturned.Load()
			f.affinity.provimuxReturn(true, key, "", f.auths(), f.noteOf(key).Add(provimuxRTCold+time.Minute))
			if dropped := provimuxReturned.Load() - before; (dropped == 1) != state.InEffect {
				t.Fatalf("in effect %v, dropped %d", state.InEffect, dropped)
			}
		})
	}
}

// TestProvimuxSessionReturnStateCounts: the state's count is exact and its last time is the drop's; the setting is
// shown in seconds with its fraction.
func TestProvimuxSessionReturnStateCounts(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	f.withTTL(provimuxRTTTL)
	f.rule(time.Minute + 900*time.Millisecond)
	key := f.fellBack(http.Header{"X-Session-Affinity": {"pi-counts"}}, "")
	before := ProvimuxSessionReturnStateOf(f.m, time.Now())
	at := f.noteOf(key).Add(time.Hour)
	f.affinity.provimuxReturn(true, key, "", f.auths(), at)
	after := ProvimuxSessionReturnStateOf(f.m, time.Now())
	if after.Counts.Returned != before.Counts.Returned+1 || after.Counts.LastReturnedAt == nil || *after.Counts.LastReturnedAt != provimuxTime(at) {
		t.Fatalf("counts before %s after %s", mustJSON(t, before.Counts), mustJSON(t, after.Counts))
	}
	if after.Setting.ColdAfterS == nil || *after.Setting.ColdAfterS != 60.9 {
		t.Fatalf("setting %s", mustJSON(t, after.Setting))
	}
	raw, _ := json.Marshal(after)
	if !strings.Contains(string(raw), `"cold_after_s":60.9`) {
		t.Fatalf("state %s", raw)
	}
}

// BenchmarkProvimuxSessionReturnCall is the rule's own call, as Pick makes it, for a conversation bound below and used
// within cold-after: unset (one atomic load), and set (every check, nothing dropped).
func BenchmarkProvimuxSessionReturnCall(b *testing.B) {
	for _, set := range []bool{false, true} {
		b.Run(map[bool]string{false: "unset", true: "set"}[set], func(b *testing.B) {
			m := NewManager(nil, nil, nil)
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: provimuxRTTTL})
			defer affinity.Stop()
			m.SetSelector(affinity)
			if set {
				ProvimuxSetSessionReturn(m, provimuxRTCold)
				defer ProvimuxSetSessionReturn(nil, 0)
			}
			high := &Auth{ID: "bench-high", Provider: "bench", Status: StatusActive, Attributes: map[string]string{"priority": "10"}}
			low := &Auth{ID: "bench-low", Provider: "bench", Status: StatusActive, Attributes: map[string]string{"priority": "5"}}
			key := "bench::affinity:pi-bench::bench-model"
			affinity.cache.SetAliases(low.ID, key)
			available := []*Auth{high, low}
			now := time.Now() // Pick takes it anyway; within cold-after of the binding just made
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				affinity.provimuxReturn(true, key, "", available, now)
			}
		})
	}
}
