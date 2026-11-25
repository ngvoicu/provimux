package registry

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Tests of models.only-my-names at the registry (only-my-names.md 16.3 and 16.7): the rule value, the list
// gate (G1), the predicate the door and the routers use (ProvimuxServes, ProvimuxRefuse), the refusal counter
// and the status view. The service-level cases are in sdk/cliproxy, the status endpoint's in
// internal/api/handlers/management.

func namesReset(t *testing.T) {
	t.Helper()
	ResetProvimuxNames()
	t.Cleanup(ResetProvimuxNames)
}

// namesRegister registers models as an account does: the rule is never consulted.
func namesRegister(t *testing.T, clientID string, models []*ModelInfo) {
	t.Helper()
	GetGlobalRegistry().RegisterClient(clientID, "test", models)
	t.Cleanup(func() { GetGlobalRegistry().UnregisterClient(clientID) })
}

func namesRule(enabled bool, aliases []string, keep ...string) *ProvimuxNamesRule {
	rule := NewProvimuxNamesRule(enabled, false)
	for _, alias := range aliases {
		rule.AddName(alias, ProvimuxNameSetByAlias)
	}
	rule.AddKeep(keep)
	return rule
}

func namesModels(ids ...string) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, &ModelInfo{ID: id, Object: "model", OwnedBy: "test"})
	}
	return out
}

// namesListed is what both list loops show, restricted to ids with the given prefix (the global registry
// is shared by the package's tests).
func namesListed(t *testing.T, prefix string) []string {
	t.Helper()
	var fromMaps, fromInfos []string
	for _, model := range GetGlobalRegistry().GetAvailableModels("openai") {
		if id, _ := model["id"].(string); strings.HasPrefix(id, prefix) {
			fromMaps = append(fromMaps, id)
		}
	}
	for _, info := range GetGlobalRegistry().GetAvailableModelInfos() {
		if strings.HasPrefix(info.ID, prefix) {
			fromInfos = append(fromInfos, info.ID)
		}
	}
	sort.Strings(fromMaps)
	sort.Strings(fromInfos)
	if !slices.Equal(fromMaps, fromInfos) {
		t.Fatalf("the two list loops disagree: %v and %v", fromMaps, fromInfos)
	}
	return fromMaps
}

// R1 and K5 at the registry: with the switch off (and without any rule) everything is listed and served, and
// what is registered never depends on the rule.
func TestProvimuxNamesOffListsAndServesAll(t *testing.T) {
	namesReset(t)
	namesRegister(t, "r1-acct", namesModels("r1-opus", "r1-gpt"))
	if got := namesListed(t, "r1-"); !slices.Equal(got, []string{"r1-gpt", "r1-opus"}) {
		t.Fatalf("no rule: %v", got)
	}
	SetProvimuxNamesRule(namesRule(false, []string{"r1-opus"}))
	if got := namesListed(t, "r1-"); !slices.Equal(got, []string{"r1-gpt", "r1-opus"}) {
		t.Fatalf("off: %v", got)
	}
	if !ProvimuxServes("r1-gpt") || ProvimuxApplies() {
		t.Fatal("off: a name is refused")
	}
	if refused, _ := ProvimuxRefuse("r1-gpt"); refused {
		t.Fatal("off: refused")
	}
	// The preview: the same predicate, whatever the switch says.
	rule, registered := ProvimuxNamesView()
	index := NewProvimuxIndex(registered)
	if rule.His("r1-gpt", index) || !rule.His("r1-opus", index) {
		t.Fatal("the preview's predicate")
	}
	if ProvimuxRefusedNow() != nil {
		t.Fatal("off: a refusal counter")
	}
}

// R2, R4: names are compared trimmed and without case; the list gate leaves out the rest.
func TestProvimuxNamesCaseAndSpaces(t *testing.T) {
	namesReset(t)
	namesRegister(t, "r4-acct", namesModels("r4-opus", "R4-GPT", "r4-other"))
	SetProvimuxNamesRule(namesRule(true, []string{" R4-Opus "}, " r4-gpt "))
	if got := namesListed(t, "r4-"); !slices.Equal(got, []string{"r4-opus"}) {
		t.Fatalf("listed %v", got)
	}
	if got := namesListed(t, "R4-"); !slices.Equal(got, []string{"R4-GPT"}) {
		t.Fatalf("listed %v", got)
	}
	if !ProvimuxServes("R4-OPUS") || !ProvimuxServes("r4-opus") || ProvimuxServes("r4-other") {
		t.Fatal("served by case")
	}
	rule, _ := ProvimuxNamesView()
	if got := rule.Names(); len(got) != 2 || got[0].Name != "R4-Opus" || got[1].Name != "r4-gpt" {
		t.Fatalf("names keep their first spelling, trimmed: %+v", got)
	}
}

// R3 and R5 (16.3, cost 1): an alias (OAuthAlias, from an oauth-model-alias row or a login's own file) is the
// owner's by name: every account that registers the spelling serves it; a name only a non-alias registers is
// not.
func TestProvimuxNamesLoginAliasIsByName(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, nil))
	namesRegister(t, "r5-login", []*ModelInfo{{ID: "r5-work-fast", OAuthAlias: "r5-work-fast"}, {ID: "team/r5-work-fast", OAuthAlias: "r5-work-fast", PrefixedFrom: "r5-work-fast"}, {ID: "r5-source"}})
	namesRegister(t, "r5-key", namesModels("r5-work-fast", "r5-mine"))
	if got := namesListed(t, "r5-"); !slices.Equal(got, []string{"r5-work-fast"}) {
		t.Fatalf("listed %v", got)
	}
	if got := namesListed(t, "team/r5-"); !slices.Equal(got, []string{"team/r5-work-fast"}) {
		t.Fatalf("listed %v", got)
	}
	if count := GetGlobalRegistry().GetModelCount("r5-work-fast"); count != 2 {
		t.Fatalf("both accounts still register r5-work-fast: %d", count)
	}
	if !ProvimuxServes("r5-work-fast") || ProvimuxServes("r5-source") || ProvimuxServes("r5-mine") {
		t.Fatal("served")
	}
	// The login goes: the key's own r5-work-fast is no longer anyone's alias.
	GetGlobalRegistry().UnregisterClient("r5-login")
	if ProvimuxServes("r5-work-fast") || len(namesListed(t, "r5-")) != 0 {
		t.Fatal("an alias of a removed login still serves the name")
	}
}

// R7: a prefix belongs to the account (PrefixedFrom on the copy); a namespace belongs to the name.
func TestProvimuxNamesPrefixAndNamespace(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"r7-opus"}, "r7-x"))
	namesRegister(t, "r7-team", []*ModelInfo{
		{ID: "r7-opus"}, {ID: "team-a/r7-opus", PrefixedFrom: "r7-opus"}, {ID: "team-a/r7-other", PrefixedFrom: "r7-other"},
		{ID: "devin/r7-x"}, {ID: "team-b/r7-opus"},
	})
	listed := append(namesListed(t, "r7-"), append(namesListed(t, "team-"), namesListed(t, "devin/")...)...)
	if !slices.Equal(listed, []string{"r7-opus", "team-a/r7-opus"}) {
		t.Fatalf("listed %v", listed)
	}
	SetProvimuxNamesRule(namesRule(true, []string{"r7-opus"}, "devin/r7-x"))
	if !ProvimuxServes("devin/r7-x") || ProvimuxServes("r7-x") {
		t.Fatal("keep devin/r7-x")
	}
}

// R8: keep takes exact names; an entry with * or an empty one is ignored and reported.
func TestProvimuxNamesKeepIgnored(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"r8-mine"}, "r8-claude-*", "  ", "r8-gpt"))
	namesRegister(t, "r8-acct", namesModels("r8-claude-opus", "r8-claude-*", "r8-gpt", "r8-mine"))
	if got := namesListed(t, "r8-"); !slices.Equal(got, []string{"r8-gpt", "r8-mine"}) {
		t.Fatalf("listed %v", got)
	}
	rule, _ := ProvimuxNamesView()
	want := []ProvimuxKeepIgnored{{Value: "r8-claude-*", Reason: "pattern"}, {Value: "  ", Reason: "empty"}}
	if !slices.Equal(rule.KeepIgnored(), want) {
		t.Fatalf("keep_ignored %+v", rule.KeepIgnored())
	}
}

// R10 and O4: zero names with the switch on serves nothing and warns at every rule set (every reload); an
// ignored keep entry is logged then too. With the switch off a reload logs nothing about the setting.
func TestProvimuxNamesWarnings(t *testing.T) {
	namesReset(t)
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)
	count := func(text string) int {
		n := 0
		for _, entry := range hook.AllEntries() {
			if strings.Contains(entry.Message, text) {
				n++
			}
		}
		return n
	}
	namesRegister(t, "r10-acct", namesModels("r10-a", "r10-b"))
	SetProvimuxNamesRule(namesRule(true, nil))
	if got := namesListed(t, "r10-"); len(got) != 0 {
		t.Fatalf("zero names listed %v", got)
	}
	SetProvimuxNamesRule(namesRule(true, nil, "a*"))
	if n := count("no name is set"); n != 2 {
		t.Fatalf("zero-names warnings = %d, want one per rule set", n)
	}
	if n := count("is ignored"); n != 1 {
		t.Fatalf("ignored keep warnings = %d", n)
	}
	hook.Reset()
	SetProvimuxNamesRule(namesRule(false, nil, "a*", ""))
	SetProvimuxNamesRule(nil)
	home := NewProvimuxNamesRule(true, true)
	SetProvimuxNamesRule(home)
	for _, entry := range hook.AllEntries() {
		if entry.Level <= log.InfoLevel && strings.Contains(entry.Message, "only-my-names") {
			t.Fatalf("off or Home logged: %q", entry.Message)
		}
	}
}

// Section 5 and S4: the message, counts per name, the last time, at most 256 names, nothing counted for a
// name nobody registers, and counting starts again when the switch goes on.
func TestProvimuxNamesRefusedCounter(t *testing.T) {
	namesReset(t)
	clock := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	t.Cleanup(SetProvimuxNamesClock(func() time.Time { return clock }))
	SetProvimuxNamesRule(namesRule(true, []string{"mine"}))
	held := make([]string, 0, 300)
	for i := range 300 {
		held = append(held, fmt.Sprintf("s4-held-%03d", i))
	}
	namesRegister(t, "s4-acct", namesModels(append(held, "mine", "claude-opus-5-5")...))

	if refused, _ := ProvimuxRefuse("mine"); refused {
		t.Fatal("a served name was refused")
	}
	if refused, offered := ProvimuxRefuse("nobody-offers-this"); !refused || offered {
		t.Fatalf("a name nobody registers: refused %v offered %v", refused, offered)
	}
	want := "model claude-opus-5-5 is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models)."
	if msg := ProvimuxNotServedMessage("claude-opus-5-5"); msg != want {
		t.Fatalf("message %q", msg)
	}
	if refused, offered := ProvimuxRefuse("claude-opus-5-5"); !refused || !offered {
		t.Fatal("refused and offered")
	}
	clock = clock.Add(time.Minute)
	ProvimuxRefuse("claude-opus-5-5")
	refused := ProvimuxRefusedNow()
	if refused == nil || !refused.Since.Equal(clock.Add(-time.Minute)) {
		t.Fatalf("refused %+v", refused)
	}
	if got := refused.Names; len(got) != 1 || got[0].Name != "claude-opus-5-5" || got[0].Requests != 2 || !got[0].LastAt.Equal(clock) {
		t.Fatalf("refused names %+v", got)
	}
	for _, name := range held {
		clock = clock.Add(time.Second)
		ProvimuxRefuse(name)
	}
	refused = ProvimuxRefusedNow()
	if len(refused.Names) != 256 || !refused.Truncated {
		t.Fatalf("names %d, truncated %v", len(refused.Names), refused.Truncated)
	}
	if refused.Names[0].Name != "s4-held-254" {
		t.Fatalf("most recent first: %s", refused.Names[0].Name)
	}
	if r, o := ProvimuxRefuse("s4-held-299"); !r || !o {
		t.Fatal("past 256 names a refusal is still a refusal")
	}

	// Off: no counting, nothing kept. On again: a new count from now.
	SetProvimuxNamesRule(namesRule(false, []string{"mine"}))
	if ProvimuxRefusedNow() != nil {
		t.Fatal("off: a counter")
	}
	clock = clock.Add(time.Hour)
	SetProvimuxNamesRule(namesRule(true, []string{"mine"}))
	refused = ProvimuxRefusedNow()
	if refused == nil || len(refused.Names) != 0 || refused.Truncated || !refused.Since.Equal(clock) {
		t.Fatalf("on again: %+v", refused)
	}
	// A reload that keeps the switch on keeps the count.
	ProvimuxRefuse("s4-held-001")
	SetProvimuxNamesRule(namesRule(true, []string{"mine", "other"}))
	if refused := ProvimuxRefusedNow(); len(refused.Names) != 1 {
		t.Fatalf("a reload with the switch on: %+v", refused)
	}
}

// Home mode: the rule does not apply.
func TestProvimuxNamesHomeDoesNotApply(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(NewProvimuxNamesRule(true, true))
	namesRegister(t, "home-acct", namesModels("home-a"))
	if got := namesListed(t, "home-"); len(got) != 1 || !ProvimuxServes("home-a") || ProvimuxApplies() {
		t.Fatalf("home: %v", got)
	}
	rule, _ := ProvimuxNamesView()
	if !rule.Enabled() || !rule.Home() || rule.Applies() || ProvimuxRefusedNow() != nil {
		t.Fatalf("home rule %+v", rule)
	}
}

// K6 at the registry: SetProvimuxNamesRule alone, with nothing registering, moves the next list and the next
// request; a list cached before is gone. Generations count the rules put in force.
func TestProvimuxNamesNoLag(t *testing.T) {
	namesReset(t)
	namesRegister(t, "k6-acct", namesModels("k6-mine", "k6-held"))
	off := namesRule(false, []string{"k6-mine"})
	off.SetSource("hash-off")
	SetProvimuxNamesRule(off)
	if got := namesListed(t, "k6-"); len(got) != 2 { // the list is cached now
		t.Fatalf("off: %v", got)
	}
	epoch := GetGlobalRegistry().ClientRegistrationEpoch("k6-acct")
	on := namesRule(true, []string{"k6-mine"})
	on.SetSource("hash-on")
	SetProvimuxNamesRule(on)
	if got := namesListed(t, "k6-"); !slices.Equal(got, []string{"k6-mine"}) {
		t.Fatalf("on, with no registration: %v", got)
	}
	if refused, offered := ProvimuxRefuse("k6-held"); !refused || !offered {
		t.Fatal("on: the next request")
	}
	rule, _ := ProvimuxNamesView()
	if rule.Source() != "hash-on" || rule.Generation() != 2 {
		t.Fatalf("in force: %q generation %d", rule.Source(), rule.Generation())
	}
	if on.Generation() != 0 {
		t.Fatal("the value given was changed: the stored rule is a copy")
	}
	SetProvimuxNamesRule(off)
	if got := namesListed(t, "k6-"); len(got) != 2 {
		t.Fatalf("off again: %v", got)
	}
	if GetGlobalRegistry().ClientRegistrationEpoch("k6-acct") != epoch {
		t.Fatal("a rule change registered the account again")
	}
}

// O3: with the switch off the gate reads nothing but the pointer: a registry whose lock is held does not
// block ProvimuxServes, ProvimuxRefuse or ProvimuxApplies. With the switch on, a literal name of the config
// does not need the lock either.
func TestProvimuxNamesOffTakesNoLock(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(false, []string{"o3-mine"}))
	reg := GetGlobalRegistry()
	reg.mutex.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ProvimuxApplies()
		ProvimuxServes("o3-anything")
		ProvimuxRefuse("o3-anything")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		reg.mutex.Unlock()
		t.Fatal("off: the gate waited for the registry's lock")
	}
	reg.mutex.Unlock()
	SetProvimuxNamesRule(namesRule(true, []string{"o3-mine"}))
	reg.mutex.Lock()
	done = make(chan struct{})
	go func() {
		defer close(done)
		ProvimuxServes("O3-MINE")
		ProvimuxRefuse("o3-mine")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		reg.mutex.Unlock()
		t.Fatal("on: a literal name waited for the registry's lock")
	}
	reg.mutex.Unlock()
}

// K6, the reviewer's 250 cycles: accounts added and removed leave no state in the layer; its state is the
// pointer and at most 256 counted names, before and after.
func TestProvimuxNamesChurnLeavesNothing(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"churn-mine"}))
	for i := range 250 {
		id := fmt.Sprintf("churn-%d", i)
		GetGlobalRegistry().RegisterClient(id, "test", namesModels("churn-mine", "churn-held-"+id))
		ProvimuxRefuse("churn-held-" + id)
		GetGlobalRegistry().UnregisterClient(id)
		if refused, offered := ProvimuxRefuse("churn-held-" + id); !refused || offered {
			t.Fatalf("%s: after removal refused %v, offered %v", id, refused, offered)
		}
	}
	if refused := ProvimuxRefusedNow(); len(refused.Names) > 256 {
		t.Fatalf("counted %d names", len(refused.Names))
	}
	if got := namesListed(t, "churn-"); len(got) != 0 {
		t.Fatalf("listed after churn: %v", got)
	}
}

// Writers, removals, rule changes, lists, status views and refusals at once (run with -race).
func TestProvimuxNamesConcurrent(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"conc-mine"}))
	var wg sync.WaitGroup
	for w := range 6 {
		wg.Go(func() {
			for i := range 200 {
				id := fmt.Sprintf("conc-%d-%d", w, i%5)
				GetGlobalRegistry().RegisterClient(id, "test", append(namesModels("conc-mine", "conc-held-"+id), &ModelInfo{ID: "conc-alias", OAuthAlias: "conc-alias"}))
				if i%3 == 0 {
					GetGlobalRegistry().UnregisterClient(id)
				}
			}
		})
	}
	for i := range 3 {
		wg.Go(func() {
			for j := range 200 {
				switch (i + j) % 4 {
				case 0:
					SetProvimuxNamesRule(namesRule(j%2 == 0, []string{"conc-mine"}))
				case 1:
					GetGlobalRegistry().GetAvailableModels("openai")
				case 2:
					rule, registered := ProvimuxNamesView()
					index := NewProvimuxIndex(registered)
					rule.His("conc-alias", index)
				default:
					ProvimuxRefuse("conc-held-conc-0-1")
					ProvimuxServes("conc-alias")
					ProvimuxRefusedNow()
				}
			}
		})
	}
	wg.Wait()
	for w := range 6 {
		for i := range 5 {
			GetGlobalRegistry().UnregisterClient(fmt.Sprintf("conc-%d-%d", w, i))
		}
	}
}

// The bound of 16.7, with the switch on: an uncached list over 20 accounts of 500 names not his, under 1 ms.
func TestProvimuxNamesListBoundSwitchOn(t *testing.T) {
	if testing.Short() || namesRaceDetector {
		t.Skip("timing")
	}
	namesReset(t)
	namesBenchAccounts(t, 20, 500)
	SetProvimuxNamesRule(namesRule(true, []string{"bench-0-0"}))
	reg := GetGlobalRegistry()
	best := time.Hour
	for range 20 {
		reg.mutex.Lock()
		reg.invalidateAvailableModelsCacheLocked()
		reg.mutex.Unlock()
		start := time.Now()
		models := reg.GetAvailableModels("openai")
		if took := time.Since(start); took < best {
			best = took
		}
		if len(models) != 1 {
			t.Fatalf("listed %d", len(models))
		}
	}
	t.Logf("uncached list, switch on, 20 accounts x 500 names not his: %v (best of 20)", best)
	if best > time.Millisecond {
		t.Fatalf("uncached list took %v, bound 1 ms", best)
	}
}

// With the switch on: the list of O2, and an uncached GetAvailableModels.
func BenchmarkProvimuxNamesGetAvailableModelInfosOn(b *testing.B) {
	ResetProvimuxNames()
	b.Cleanup(ResetProvimuxNames)
	namesBenchAccounts(b, 20, 500)
	SetProvimuxNamesRule(namesRule(true, []string{"bench-0-0"}))
	reg := GetGlobalRegistry()
	b.ReportAllocs()
	for b.Loop() {
		reg.GetAvailableModelInfos()
	}
}

func BenchmarkProvimuxNamesUncachedListOn(b *testing.B) {
	ResetProvimuxNames()
	b.Cleanup(ResetProvimuxNames)
	namesBenchAccounts(b, 20, 500)
	SetProvimuxNamesRule(namesRule(true, []string{"bench-0-0"}))
	reg := GetGlobalRegistry()
	b.ReportAllocs()
	for b.Loop() {
		reg.mutex.Lock()
		reg.invalidateAvailableModelsCacheLocked()
		reg.mutex.Unlock()
		reg.GetAvailableModels("openai")
	}
}
