package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const pmxModel = "provimux-test-model"

func pmxAuth(id string) *Auth {
	return &Auth{ID: id, Provider: "claude", Status: StatusActive}
}

func pmxSelector(ttl time.Duration, rebind string) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: ttl, RebindOn: rebind})
}

// pmxExplicit is a request with an explicit session id (and optionally a parent) from one client key.
func pmxExplicit(scope, session, parent string) cliproxyexecutor.Options {
	headers := http.Header{"X-Session-Id": []string{session}}
	if parent != "" {
		headers.Set("X-Parent-Session-Id", parent)
	}
	return cliproxyexecutor.Options{Headers: headers, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}
}

// pmxConversation is a Responses request without any id: its turns, in order. scope is the client
// key's caller scope ("" for a gateway without client keys).
func pmxConversation(scope string, turns ...string) cliproxyexecutor.Options {
	var input []string
	for i, turn := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		input = append(input, fmt.Sprintf(`{"type":"message","role":%q,"content":%q}`, role, turn))
	}
	body := `{"model":"` + pmxModel + `","instructions":"provimux-test helper","input":[` + strings.Join(input, ",") + `]}`
	metadata := map[string]any{}
	if scope != "" {
		metadata[cliproxyexecutor.CallerScopeMetadataKey] = scope
	}
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: []byte(body), Metadata: metadata}
}

// pmxPick picks as a request does: the session identity is derived first (Enrich), then Pick.
func pmxPick(t *testing.T, selector *SessionAffinitySelector, opts cliproxyexecutor.Options, auths ...*Auth) (string, error) {
	t.Helper()
	opts.Metadata = cloneTestMetadata(opts.Metadata)
	_, opts = cliproxysession.Enrich(cliproxyexecutor.Request{Model: pmxModel, Payload: opts.OriginalRequest, Format: opts.SourceFormat}, opts)
	picked, err := selector.Pick(context.Background(), "mixed", pmxModel, opts, auths)
	if err != nil {
		return "", err
	}
	return picked.ID, nil
}

func cloneTestMetadata(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func pmxTokens(id string, _ time.Time) (string, bool) { return "account-of-" + id, true }

func pmxAccounts(ids ...string) map[string]string {
	out := map[string]string{}
	for _, id := range ids {
		out["account-of-"+id] = id
	}
	return out
}

// pmxRestart copies warm's bindings and puts them in a fresh selector, as a stop and a start do.
func pmxRestart(t *testing.T, warm *SessionAffinitySelector, fresh *SessionAffinitySelector, after time.Duration, ids ...string) ProvimuxAffinityRestored {
	t.Helper()
	now := time.Now()
	state, err := ProvimuxAffinityCapture(warm, pmxTokens, now)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ProvimuxAffinityRestore(fresh, state, pmxAccounts(ids...), now.Add(after))
	if err != nil {
		t.Fatal(err)
	}
	return restored
}

// pmxNext makes the next new binding of selector go away from account: probes (new conversations
// of their own) until one lands on it.
func pmxNext(t *testing.T, selector *SessionAffinitySelector, account string, auths ...*Auth) {
	t.Helper()
	for i := 0; i < 3; i++ {
		got, _ := pmxPick(t, selector, pmxExplicit("probe", fmt.Sprintf("provimux-test-probe-%d-%d", time.Now().UnixNano(), i), ""), auths...)
		if got == account {
			return
		}
	}
	t.Fatalf("no probe landed on %s", account)
}

// What comes back after a restart, and what does not. Each case: the conversation's next turn on
// the restored selector against the same turn on the selector that never stopped, with the next new
// binding forced onto the other account so that coming back cannot happen by turn.
func TestProvimuxAffinityRestoreKeepsConversations(t *testing.T) {
	a, b := pmxAuth("provimux-test-a"), pmxAuth("provimux-test-b")
	type conversation struct {
		name     string
		turns    []cliproxyexecutor.Options // turns before the restart
		next     cliproxyexecutor.Options   // the turn after it
		restored bool                       // true: back on its account; false: bound afresh
	}
	q := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("question %d", i), fmt.Sprintf("answer %d", i))
		}
		return out[:2*n-1]
	}
	with := func(scope string, turns ...string) cliproxyexecutor.Options { return pmxConversation(scope, turns...) }
	cases := []conversation{
		{"with an id", []cliproxyexecutor.Options{pmxExplicit("s1", "provimux-test-session", "")}, pmxExplicit("s1", "provimux-test-session", ""), true},
		{"with an id, compacted (same id, new history)", []cliproxyexecutor.Options{pmxExplicit("s1", "provimux-test-compacted", "")}, pmxExplicit("s1", "provimux-test-compacted", ""), true},
		{"with an id, forked (new id, parent named)", []cliproxyexecutor.Options{pmxExplicit("s1", "provimux-test-parent", "")}, pmxExplicit("s1", "provimux-test-fork", "provimux-test-parent"), true},
		{"no id, no client keys", []cliproxyexecutor.Options{with("", q(1)...), with("", q(2)...)}, with("", q(3)...), true},
		{"no id, no client keys, forked (same opening)", []cliproxyexecutor.Options{with("", q(1)...), with("", q(2)...)}, with("", append(q(1), "answer 1", "a different question 2")...), true},
		{"no id, no client keys, compacted (new opening)", []cliproxyexecutor.Options{with("", q(1)...), with("", q(2)...)}, with("", "summary of the conversation so far", "ok", "question 3"), false},
		{"no id, with client keys", []cliproxyexecutor.Options{with("s1", q(1)...), with("s1", q(2)...)}, with("s1", q(3)...), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warm := pmxSelector(time.Hour, "")
			defer warm.Stop()
			// A primer takes a first: the conversation lands on b.
			pmxPick(t, warm, pmxExplicit("s1", "provimux-test-primer", ""), a, b)
			var bound string
			for i, turn := range tc.turns {
				got, err := pmxPick(t, warm, turn, a, b)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					bound = got
				} else if got != bound {
					t.Fatalf("premise: turn %d on %s, bound to %s", i+1, got, bound)
				}
			}
			if bound != b.ID {
				t.Fatalf("premise: conversation on %s", bound)
			}
			fresh := pmxSelector(time.Hour, "")
			defer fresh.Stop()
			pmxRestart(t, warm, fresh, time.Second, a.ID, b.ID)
			pmxNext(t, warm, bound, a, b)
			pmxNext(t, fresh, bound, a, b)
			uninterrupted, _ := pmxPick(t, warm, tc.next, a, b)
			afterRestart, _ := pmxPick(t, fresh, tc.next, a, b)
			if tc.restored {
				if afterRestart != bound || uninterrupted != bound {
					t.Fatalf("after the restart on %s, uninterrupted on %s; was on %s", afterRestart, uninterrupted, bound)
				}
				return
			}
			// Bound afresh: the next account in turn, as a new conversation is; never a binding of
			// the snapshot.
			if afterRestart == bound {
				t.Fatalf("after the restart on %s: expected a new binding on the next account", afterRestart)
			}
		})
	}
}

// What upstream decided before the stop stands after it. A 429 that moved a session stays moved;
// with rebind-on never a session stays on its unavailable account; a child asking before its
// parent inherits as it would have in memory; two client keys sending one id share, as in memory.
func TestProvimuxAffinityRestoreKeepsUpstreamDecisions(t *testing.T) {
	a, b := pmxAuth("provimux-test-a"), pmxAuth("provimux-test-b")

	t.Run("429 moved the session", func(t *testing.T) {
		warm := pmxSelector(time.Hour, "")
		defer warm.Stop()
		opts := pmxExplicit("s1", "provimux-test-moved", "")
		first, _ := pmxPick(t, warm, opts, a, b)
		warm.OnResult(Result{AuthID: first, Provider: "claude", Model: pmxModel, Success: false, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}, Options: pmxExplicit("s1", "provimux-test-moved", "")})
		other := a
		if first == a.ID {
			other = b
		}
		if got, _ := pmxPick(t, warm, opts, other); got != other.ID {
			t.Fatalf("premise: moved to %s", got)
		}
		fresh := pmxSelector(time.Hour, "")
		defer fresh.Stop()
		pmxRestart(t, warm, fresh, time.Second, a.ID, b.ID)
		for i := 0; i < 3; i++ {
			if got, _ := pmxPick(t, fresh, opts, a, b); got != other.ID {
				t.Fatalf("after the restart the moved session went back to %s", got)
			}
		}
	})

	t.Run("rebind-on never", func(t *testing.T) {
		warm := pmxSelector(time.Hour, SessionAffinityRebindOnNever)
		defer warm.Stop()
		opts := pmxExplicit("s1", "provimux-test-never", "")
		bound, _ := pmxPick(t, warm, opts, a, b)
		fresh := pmxSelector(time.Hour, SessionAffinityRebindOnNever)
		defer fresh.Stop()
		pmxRestart(t, warm, fresh, time.Second, a.ID, b.ID)
		disabledA, disabledB := pmxAuth(a.ID), pmxAuth(b.ID)
		if bound == a.ID {
			disabledA.Disabled = true
		} else {
			disabledB.Disabled = true
		}
		_, errWarm := pmxPick(t, warm, opts, disabledA, disabledB)
		_, errFresh := pmxPick(t, fresh, opts, disabledA, disabledB)
		if errWarm == nil || errFresh == nil {
			t.Fatalf("never: warm %v, restored %v; both must refuse to move", errWarm, errFresh)
		}
	})

	t.Run("child before parent", func(t *testing.T) {
		warm := pmxSelector(time.Hour, "")
		defer warm.Stop()
		pmxPick(t, warm, pmxExplicit("s1", "provimux-test-primer", ""), a, b)
		parent, _ := pmxPick(t, warm, pmxExplicit("s1", "provimux-test-parent", ""), a, b)
		fresh := pmxSelector(time.Hour, "")
		defer fresh.Stop()
		pmxRestart(t, warm, fresh, time.Second, a.ID, b.ID)
		child := pmxExplicit("s1", "provimux-test-child", "provimux-test-parent")
		warmChild, _ := pmxPick(t, warm, child, a, b)
		freshChild, _ := pmxPick(t, fresh, child, a, b)
		warmParent, _ := pmxPick(t, warm, pmxExplicit("s1", "provimux-test-parent", ""), a, b)
		freshParent, _ := pmxPick(t, fresh, pmxExplicit("s1", "provimux-test-parent", ""), a, b)
		if warmChild != freshChild || warmParent != freshParent || freshParent != parent {
			t.Fatalf("child/parent: in memory %s/%s, after restart %s/%s, parent was on %s", warmChild, warmParent, freshChild, freshParent, parent)
		}
	})

	t.Run("two client keys, one session id: as upstream's memory has it", func(t *testing.T) {
		warm := pmxSelector(time.Hour, "")
		defer warm.Stop()
		pmxPick(t, warm, pmxExplicit("s1", "provimux-test-primer", ""), a, b)
		pmxPick(t, warm, pmxExplicit("s1", "provimux-test-shared", ""), a, b)
		fresh := pmxSelector(time.Hour, "")
		defer fresh.Stop()
		pmxRestart(t, warm, fresh, time.Second, a.ID, b.ID)
		warmOther, _ := pmxPick(t, warm, pmxExplicit("s2", "provimux-test-shared", ""), a, b)
		freshOther, _ := pmxPick(t, fresh, pmxExplicit("s2", "provimux-test-shared", ""), a, b)
		if warmOther != freshOther {
			t.Fatalf("client 2 on %s in memory, %s after restart", warmOther, freshOther)
		}
	})
}

// Finding 4 (eviction order): the restored cache evicts in the order the live one would. At the
// production capacity (65,536 keys), a session touched by a late result sits behind newer ones;
// after the next insertion the same sessions survive in both.
func TestProvimuxAffinityRestoreKeepsEvictionOrder(t *testing.T) {
	a, b := pmxAuth("provimux-test-a"), pmxAuth("provimux-test-b")
	warm := pmxSelector(time.Hour, "")
	defer warm.Stop()
	capacity := warm.cache.maxEntries
	if capacity != defaultMaxSessionEntries {
		t.Fatalf("premise: capacity %d", capacity)
	}
	session := func(i int) cliproxyexecutor.Options {
		return pmxExplicit("s1", fmt.Sprintf("provimux-test-cap-%d", i), "")
	}
	for i := 0; i < capacity; i++ {
		if _, err := pmxPick(t, warm, session(i), a, b); err != nil {
			t.Fatal(err)
		}
	}
	// A late success for session 0 (the oldest) moves it behind the others.
	touched, _ := warm.LookupAffinity("mixed", pmxModel, "provimux-test-cap-0")
	opts := session(0)
	opts.Metadata = cloneTestMetadata(opts.Metadata)
	warm.OnResult(Result{AuthID: touched, Provider: "mixed", Model: pmxModel, Success: true, Options: opts})
	fresh := pmxSelector(time.Hour, "")
	defer fresh.Stop()
	if restored := pmxRestart(t, warm, fresh, time.Second, a.ID, b.ID); restored.Sessions != capacity {
		t.Fatalf("restored %d of %d", restored.Sessions, capacity)
	}
	pmxPick(t, warm, session(capacity), a, b)
	pmxPick(t, fresh, session(capacity), a, b)
	for _, i := range []int{0, 1, 2, capacity - 1, capacity} {
		key := fmt.Sprintf("provimux-test-cap-%d", i)
		warmID, warmState := warm.LookupAffinity("mixed", pmxModel, key)
		freshID, freshState := fresh.LookupAffinity("mixed", pmxModel, key)
		if warmID != freshID || warmState != freshState {
			t.Fatalf("%s: in memory %s (%s), after restart %s (%s)", key, warmID, warmState, freshID, freshState)
		}
	}
	if _, state := fresh.LookupAffinity("mixed", pmxModel, "provimux-test-cap-0"); state != "bound" {
		t.Fatal("the touched session was evicted after the restart")
	}
	if _, state := fresh.LookupAffinity("mixed", pmxModel, "provimux-test-cap-1"); state != "unbound" {
		t.Fatal("the oldest untouched session was not the one evicted")
	}
}

// Finding 5: no entry outlives its in-memory lifetime, whatever the clock or the TTL does; the
// reviewer's case (20 minutes left at the snapshot) keeps 20 minutes, not a new hour.
func TestProvimuxAffinityRestoreLifetimes(t *testing.T) {
	a, b := pmxAuth("provimux-test-a"), pmxAuth("provimux-test-b")
	expiries := func(selector *SessionAffinitySelector) map[string]time.Time {
		selector.cache.mu.RLock()
		defer selector.cache.mu.RUnlock()
		out := map[string]time.Time{}
		for key, entry := range selector.cache.entries {
			out[key] = entry.expiresAt
		}
		return out
	}
	bound := func(ttl time.Duration) (*SessionAffinitySelector, *ProvimuxAffinityState, time.Time) {
		warm := pmxSelector(ttl, "")
		t.Cleanup(warm.Stop)
		pmxPick(t, warm, pmxExplicit("s1", "provimux-test-primer", ""), a, b)
		pmxPick(t, warm, pmxExplicit("s1", "provimux-test-session", ""), a, b)
		pmxPick(t, warm, pmxConversation("", "no id question"), a, b)
		now := time.Now()
		state, err := ProvimuxAffinityCapture(warm, pmxTokens, now)
		if err != nil {
			t.Fatal(err)
		}
		return warm, state, now
	}

	t.Run("20 minutes left stay 20 minutes", func(t *testing.T) {
		warm := pmxSelector(time.Hour, "")
		defer warm.Stop()
		pmxPick(t, warm, pmxExplicit("s1", "provimux-test-session", ""), a, b)
		// Last used 40 minutes ago: 20 minutes left.
		warm.cache.mu.Lock()
		var groups []sessionEntry
		for _, group := range warm.cache.groups {
			groups = append(groups, group)
		}
		for _, group := range groups {
			warm.cache.replaceAliasGroupsLocked(group.authID, time.Now().Add(20*time.Minute), group.aliases, group.binding, group)
		}
		warm.cache.mu.Unlock()
		before := expiries(warm)
		now := time.Now()
		state, _ := ProvimuxAffinityCapture(warm, pmxTokens, now)
		fresh := pmxSelector(time.Hour, "")
		defer fresh.Stop()
		if restored, err := ProvimuxAffinityRestore(fresh, state, pmxAccounts(a.ID, b.ID), now.Add(time.Second)); err != nil || restored.Sessions != 1 {
			t.Fatalf("restored %+v (%v)", restored, err)
		}
		after := expiries(fresh)
		if len(after) != len(before) {
			t.Fatalf("%d keys after, %d before", len(after), len(before))
		}
		for key, expires := range before {
			if !after[key].Equal(expires) {
				t.Fatalf("%s: expiry %s after the restart, %s before", key, after[key], expires)
			}
			// At minute 21 both have forgotten it.
			if !expires.Before(now.Add(21 * time.Minute)) {
				t.Fatalf("alive at minute 21: %s", expires)
			}
		}
	})

	t.Run("ttl of one second", func(t *testing.T) {
		_, state, now := bound(time.Second)
		fresh := pmxSelector(time.Second, "")
		defer fresh.Stop()
		if restored, err := ProvimuxAffinityRestore(fresh, state, pmxAccounts(a.ID, b.ID), now.Add(1100*time.Millisecond)); err == nil || restored.Sessions != 0 {
			t.Fatalf("restored %+v (%v) after the ttl", restored, err)
		}
	})

	t.Run("clock set back", func(t *testing.T) {
		_, state, now := bound(time.Hour)
		fresh := pmxSelector(time.Hour, "")
		defer fresh.Stop()
		if restored, err := ProvimuxAffinityRestore(fresh, state, pmxAccounts(a.ID, b.ID), now.Add(-time.Minute)); err == nil || restored.Sessions != 0 {
			t.Fatalf("a snapshot from the future restored %+v (%v)", restored, err)
		}
	})

	t.Run("shortened ttl", func(t *testing.T) {
		_, state, now := bound(time.Hour)
		short := pmxSelector(5*time.Minute, "")
		defer short.Stop()
		if restored, _ := ProvimuxAffinityRestore(short, state, pmxAccounts(a.ID, b.ID), now.Add(10*time.Minute)); restored.Sessions != 0 {
			t.Fatalf("a 5m ttl restored %+v last used 10m ago", restored)
		}
		half := pmxSelector(30*time.Minute, "")
		defer half.Stop()
		restored, err := ProvimuxAffinityRestore(half, state, pmxAccounts(a.ID, b.ID), now.Add(10*time.Minute))
		if err != nil || restored.Sessions != 3 {
			t.Fatalf("a 30m ttl restored %+v (%v)", restored, err)
		}
		for key, expires := range expiries(half) {
			if expires.After(now.Add(30*time.Minute + time.Second)) {
				t.Fatalf("%s lives until %s, past its last use plus the new ttl", key, expires)
			}
		}
	})

	t.Run("restored twice (a second crash): the same expiries", func(t *testing.T) {
		warm, state, now := bound(time.Hour)
		first := pmxSelector(time.Hour, "")
		defer first.Stop()
		second := pmxSelector(time.Hour, "")
		defer second.Stop()
		ProvimuxAffinityRestore(first, state, pmxAccounts(a.ID, b.ID), now.Add(time.Second))
		ProvimuxAffinityRestore(second, state, pmxAccounts(a.ID, b.ID), now.Add(4*time.Minute))
		in, one, two := expiries(warm), expiries(first), expiries(second)
		for key, expires := range in {
			if !one[key].Equal(expires) || !two[key].Equal(expires) {
				t.Fatalf("%s: %s in memory, %s and %s after two restores", key, expires, one[key], two[key])
			}
		}
	})
}

// Finding 10: a snapshot whose pin this build does not reproduce: where a kind of conversation is
// named differently its entries are left out; where a rule differs nothing is restored.
func TestProvimuxAffinityPin(t *testing.T) {
	a, b := pmxAuth("provimux-test-a"), pmxAuth("provimux-test-b")
	pin, err := ProvimuxAffinityCurrentPin()
	if err != nil {
		t.Fatal(err)
	}
	if differs, errCheck := ProvimuxAffinityCheckPin(pin); errCheck != nil || len(differs) != 0 {
		t.Fatalf("this build does not reproduce its own pin: %v %v", differs, errCheck)
	}
	// Every way the session cache names a conversation is in the samples: each kind of key the
	// selector writes appears in the results.
	all := strings.Join(pin.Results, "\n")
	for _, kind := range []string{"::claude:", ":agent:", "::codex:", "::agy:", "::header:", "::affinity:", "::slot:", "::task:", "::conv:", "::thread:", "::clientreq:", "::pck:", "::session:", "::geminicache:", "::execution:", "#", "::derived:ctx:", "::derived:pin-derived", "::msg:", "::pin-other-model", "left=0.50", "left=-0.01", "capacity 2"} {
		if !strings.Contains(all, kind) {
			t.Errorf("no sample produces %q", kind)
		}
	}
	warm := pmxSelector(time.Hour, "")
	defer warm.Stop()
	pmxPick(t, warm, pmxExplicit("s1", "provimux-test-session", ""), a, b)
	claudeCode := pmxExplicit("s1", "", "")
	claudeCode.Headers = http.Header{"X-Claude-Code-Session-Id": []string{"provimux-test-cc"}}
	pmxPick(t, warm, claudeCode, a, b)
	now := time.Now()
	sample := func(name string) int {
		for i, s := range pin.Samples {
			if s.Name == name {
				return i
			}
		}
		t.Fatalf("no sample %q", name)
		return -1
	}
	edit := func(name string) func(*ProvimuxAffinityState) {
		return func(s *ProvimuxAffinityState) {
			s.Pin.Results = append([]string(nil), s.Pin.Results...)
			s.Pin.Results[sample(name)] += "differs\n"
		}
	}
	for _, tc := range []struct {
		name     string
		change   func(*ProvimuxAffinityState)
		restored int    // -1: the snapshot is ignored
		kinds    string // the kinds left out
	}{
		{"a Claude Code session is named differently", edit("claude code header"), 1, "claude"},
		{"a generic session header is named differently", edit("session header"), 1, "header"},
		{"a rule differs: recency", edit("recency: reuse and a result reorder the cache"), -1, ""},
		{"a rule differs: expiry", edit("expiry: an entry past its lifetime is not used"), -1, ""},
		{"a rule differs: the model key", edit("model key"), -1, ""},
		{"an entry holds other fields", func(s *ProvimuxAffinityState) { s.Pin.Layout += ";extra" }, -1, ""},
		{"a step this build does not know", func(s *ProvimuxAffinityState) {
			s.Pin.Samples = append(append([]ProvimuxAffinityPinSample(nil), s.Pin.Samples...), ProvimuxAffinityPinSample{Name: "future", Steps: []ProvimuxAffinityPinStep{{Op: "rewind"}}})
			s.Pin.Results = append(append([]string(nil), s.Pin.Results...), "rewound\n")
		}, -1, ""},
		{"no pin", func(s *ProvimuxAffinityState) { s.Pin = ProvimuxAffinityPin{} }, -1, ""},
		{"another format", func(s *ProvimuxAffinityState) { s.Format = 1 }, -1, ""},
	} {
		state, _ := ProvimuxAffinityCapture(warm, pmxTokens, now)
		tc.change(state)
		fresh := pmxSelector(time.Hour, "")
		restored, errRestore := ProvimuxAffinityRestore(fresh, state, pmxAccounts(a.ID, b.ID), now)
		fresh.Stop()
		if tc.restored < 0 {
			if errRestore == nil || restored.Sessions != 0 {
				t.Errorf("%s: restored %+v (%v)", tc.name, restored, errRestore)
			}
			continue
		}
		if errRestore != nil || restored.Sessions != tc.restored || restored.OtherBuild != 2-tc.restored || strings.Join(restored.OtherBuildKinds, ",") != tc.kinds {
			t.Errorf("%s: restored %+v (%v)", tc.name, restored, errRestore)
		}
	}
}

// An entry comes back only on the account with the same token; another login now at the same
// path has another identity, so another token.
func TestProvimuxAffinityRestoreOnlyOnTheSameAccount(t *testing.T) {
	a, b := pmxAuth("provimux-test-a"), pmxAuth("provimux-test-b")
	warm := pmxSelector(time.Hour, "")
	defer warm.Stop()
	pmxPick(t, warm, pmxExplicit("s1", "provimux-test-primer", ""), a, b)
	pmxPick(t, warm, pmxExplicit("s1", "provimux-test-session", ""), a, b) // on b
	now := time.Now()
	state, _ := ProvimuxAffinityCapture(warm, pmxTokens, now)
	fresh := pmxSelector(time.Hour, "")
	defer fresh.Stop()
	// b's path now holds another account: its token is not b's.
	accounts := map[string]string{"account-of-" + a.ID: a.ID, "account-of-someone-else": b.ID}
	restored, _ := ProvimuxAffinityRestore(fresh, state, accounts, now)
	if restored.Sessions != 1 || restored.UnknownAccounts != 1 {
		t.Fatalf("restored %+v, want only a's binding", restored)
	}
	if id, state := fresh.LookupAffinity("mixed", pmxModel, "provimux-test-session"); state == "bound" {
		t.Fatalf("the replaced login got the old account's binding (%s)", id)
	}
	// The capture leaves out what the account function refuses (a login replaced after its use).
	refused, _ := ProvimuxAffinityCapture(warm, func(id string, _ time.Time) (string, bool) { return "account-of-" + id, id != b.ID }, now)
	if len(refused.Sessions) != 1 {
		t.Fatalf("captured %d sessions, want a's only", len(refused.Sessions))
	}
}

func pmxJWT(claims string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(claims)) + ".sig"
}

func TestProvimuxAccountIdentity(t *testing.T) {
	devin := func(id, userID, email string) *Auth {
		return &Auth{ID: id, Provider: "devin", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"user_id": userID, "email": email, "user_email": email}}
	}
	cases := []struct {
		name string
		auth *Auth
		ok   bool
	}{
		{"devin without user_id", devin("same-path.json", "", "shared@example.invalid"), false},
		{"devin with user_id", devin("same-path.json", "user-1", "shared@example.invalid"), true},
		{"claude without organization", &Auth{ID: "c.json", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_uuid": "acc", "email": "x@example.invalid"}}, false},
		{"claude", &Auth{ID: "c.json", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_uuid": "acc", "organization_uuid": "org"}}, true},
		{"codex", &Auth{ID: "x.json", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "ws", "id_token": pmxJWT(`{"https://api.openai.com/auth":{"chatgpt_user_id":"user-9"}}`)}}, true},
		{"codex without account", &Auth{ID: "x.json", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "x@example.invalid"}}, false},
		{"meta login (no stable id)", &Auth{ID: "m.json", Provider: "meta", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "x@example.invalid"}}, false},
		{"api key", &Auth{ID: "openai-compatibility:x:1", Provider: "openai-compatibility", Attributes: map[string]string{"auth_kind": "apikey", "api_key": "k", "base_url": "https://Host.example/v1"}}, true},
	}
	for _, tc := range cases {
		identity, ok := ProvimuxAccountIdentity(tc.auth)
		if ok != tc.ok {
			t.Errorf("%s: identity %q ok=%v, want ok=%v", tc.name, identity, ok, tc.ok)
		}
		if ok && (strings.Contains(identity, tc.auth.ID) || strings.Contains(identity, "@")) {
			t.Errorf("%s: identity %q holds the file name or an e-mail", tc.name, identity)
		}
	}
	one, _ := ProvimuxAccountIdentity(devin("same-path.json", "user-1", "shared@example.invalid"))
	two, _ := ProvimuxAccountIdentity(devin("same-path.json", "user-2", "shared@example.invalid"))
	if one == two {
		t.Fatal("two Devin users with one e-mail at one path share an identity")
	}
	keyA, _ := ProvimuxAccountIdentity(&Auth{Provider: "openai-compatibility", Attributes: map[string]string{"auth_kind": "apikey", "api_key": "k", "base_url": "https://one.example/v1"}})
	keyB, _ := ProvimuxAccountIdentity(&Auth{Provider: "openai-compatibility", Attributes: map[string]string{"auth_kind": "apikey", "api_key": "k", "base_url": "https://two.example/v1"}})
	if keyA == keyB {
		t.Fatal("one key on two hosts shares an identity")
	}
}
