package registry

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Tests of models.only-my-names at the registry (only-my-names.md 16 to 18): putting a rule in force, the list
// gate (G1), the resolution of auto under a rule, and the status's reading. The rule itself is tested in
// internal/provimux/onlymynames; the service-level cases are in sdk/cliproxy.

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

func namesRule(enabled bool, aliases []string, keep ...string) *onlymynames.Rule {
	rule := onlymynames.NewRule(enabled, false)
	for _, alias := range aliases {
		rule.AddName(alias, onlymynames.SetByAlias)
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

func namesGemini(t *testing.T, contains string) []string {
	t.Helper()
	var out []string
	for _, model := range GetGlobalRegistry().GetAvailableModels("gemini") {
		if name, _ := model["name"].(string); strings.Contains(name, contains) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// R1 and K5: with the switch off (and without any rule) everything is listed.
func TestProvimuxNamesOffListsAll(t *testing.T) {
	namesReset(t)
	namesRegister(t, "r1-acct", namesModels("r1-opus", "r1-gpt"))
	if got := namesListed(t, "r1-"); !slices.Equal(got, []string{"r1-gpt", "r1-opus"}) {
		t.Fatalf("no rule: %v", got)
	}
	SetProvimuxNamesRule(namesRule(false, []string{"r1-opus"}))
	if got := namesListed(t, "r1-"); !slices.Equal(got, []string{"r1-gpt", "r1-opus"}) {
		t.Fatalf("off: %v", got)
	}
}

// The list gate is the rule's predicate: exact names, suffixed spellings through their base, copies under a
// prefix the config declares; the registry's alias flag decides nothing.
func TestProvimuxNamesListGate(t *testing.T) {
	namesReset(t)
	rule := namesRule(true, []string{"g-mine"}, "g-local-base")
	rule.AddPrefix("g-crew")
	SetProvimuxNamesRule(rule)
	namesRegister(t, "g-acct", []*ModelInfo{
		{ID: "g-mine"}, {ID: "G-MINE"}, {ID: "g-local-base(high)"}, {ID: "g-crew/g-mine"}, {ID: "g-solo/g-mine"},
		{ID: "g-flagged", OAuthAlias: "g-flagged"}, {ID: "g-other"},
	})
	listed := append(namesListed(t, "g-"), namesListed(t, "G-")...)
	if !slices.Equal(listed, []string{"g-crew/g-mine", "g-local-base(high)", "g-mine"}) {
		t.Fatalf("listed %v", listed)
	}
}

// E and the fourth review's case (section 18): the Gemini format names a model by Name, falling back to the
// id. A plugin model whose provider-native Name differs from its kept id, and a key alias spelled
// "models/x" whose shown name "x" is not served, are left out of the Gemini list (and so of its detail, which
// reads that list), so list, detail and generation agree; the other formats list the kept id.
func TestProvimuxNamesGeminiNames(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"models/e-gemini-2.5-pro"}, "e-public-name", "e-kept"))
	namesRegister(t, "plugin:e:demo:executor", []*ModelInfo{{ID: "e-public-name", Name: "e-vendor-native-name", Object: "model"},
		{ID: "e-kept", Name: "models/e-kept", Object: "model"}})
	namesRegister(t, "e-key", []*ModelInfo{{ID: "models/e-gemini-2.5-pro", Object: "model"}})
	if got := namesGemini(t, "e-"); !slices.Equal(got, []string{"models/e-kept"}) {
		t.Fatalf("gemini %v", got)
	}
	if got := namesListed(t, "e-"); !slices.Equal(got, []string{"e-kept", "e-public-name"}) {
		t.Fatalf("openai %v", got)
	}
	// Kept as the name the Gemini format shows, the alias is listed there and generated under it.
	SetProvimuxNamesRule(namesRule(true, []string{"models/e-gemini-2.5-pro"}, "e-gemini-2.5-pro"))
	if got := namesGemini(t, "e-gemini"); !slices.Equal(got, []string{"models/e-gemini-2.5-pro"}) {
		t.Fatalf("gemini, kept as shown: %v", got)
	}
	SetProvimuxNamesRule(namesRule(false, nil))
	if got := namesGemini(t, "e-"); len(got) != 3 {
		t.Fatalf("off: gemini %v", got)
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
	SetProvimuxNamesRule(namesRule(true, nil, "a*", "auto"))
	if n := count("no name is set"); n != 2 {
		t.Fatalf("zero-names warnings = %d, want one per rule set", n)
	}
	if n := count("is ignored"); n != 2 {
		t.Fatalf("ignored keep warnings = %d", n)
	}
	hook.Reset()
	SetProvimuxNamesRule(namesRule(false, nil, "a*", ""))
	SetProvimuxNamesRule(nil)
	SetProvimuxNamesRule(onlymynames.NewRule(true, true))
	for _, entry := range hook.AllEntries() {
		if entry.Level <= log.InfoLevel && strings.Contains(entry.Message, "only-my-names") {
			t.Fatalf("off or Home logged: %q", entry.Message)
		}
	}
}

// Home mode: the rule does not apply.
func TestProvimuxNamesHomeDoesNotApply(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(onlymynames.NewRule(true, true))
	namesRegister(t, "home-acct", namesModels("home-a"))
	if got := namesListed(t, "home-"); len(got) != 1 {
		t.Fatalf("home: %v", got)
	}
	rule, _, refused := ProvimuxNamesView()
	if !rule.Enabled() || !rule.Home() || rule.Applies() || refused != nil {
		t.Fatalf("home rule %+v", rule)
	}
}

// K6: SetProvimuxNamesRule alone, with nothing registering, moves the next list; a list cached before is gone.
// An off-to-off commit leaves the list cache warm.
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
	rule, _, _ := ProvimuxNamesView()
	if rule.Source() != "hash-on" || rule.Generation() != 2 {
		t.Fatalf("in force: %q generation %d", rule.Source(), rule.Generation())
	}
	SetProvimuxNamesRule(off)
	if got := namesListed(t, "k6-"); len(got) != 2 {
		t.Fatalf("off again: %v", got)
	}
	generation := GetGlobalRegistry().GetGeneration()
	SetProvimuxNamesRule(namesRule(false, []string{"k6-other"}))
	if GetGlobalRegistry().GetGeneration() != generation {
		t.Fatal("an off-to-off commit cleared the list cache")
	}
	if GetGlobalRegistry().ClientRegistrationEpoch("k6-acct") != epoch {
		t.Fatal("a rule change registered the account again")
	}
}

// auto under a rule: the newest available model the rule serves, judged with that rule alone.
func TestProvimuxNamesAutoUnderARule(t *testing.T) {
	namesReset(t)
	namesRegister(t, "auto-acct", []*ModelInfo{{ID: "auto-old", Created: 1}, {ID: "auto-new", Created: 2}, {ID: "auto-newest-not-his", Created: 3}})
	rule := namesRule(true, nil, "auto-old", "auto-new")
	SetProvimuxNamesRule(namesRule(true, nil)) // the rule in force serves nothing; the given value decides
	if got := ProvimuxFirstServedUnder(rule); got != "auto-new" {
		t.Fatalf("auto under the given rule: %q", got)
	}
	if got := ProvimuxFirstServedUnder(namesRule(true, nil, "nobody")); got != "" {
		t.Fatalf("nothing served: %q", got)
	}
}

// Rule changes concurrent with registrations, lists and status readings (run with -race): one list is built
// under one rule.
func TestProvimuxNamesConcurrent(t *testing.T) {
	namesReset(t)
	on := namesRule(true, []string{"conc-mine"})
	off := namesRule(false, []string{"conc-mine"})
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := range 200 {
				id := fmt.Sprintf("conc-%d-%d", w, i%5)
				GetGlobalRegistry().RegisterClient(id, "test", namesModels("conc-mine", "conc-held"))
				if i%3 == 0 {
					GetGlobalRegistry().UnregisterClient(id)
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 400 {
			if i%2 == 0 {
				SetProvimuxNamesRule(on)
			} else {
				SetProvimuxNamesRule(off)
			}
		}
	})
	for range 2 {
		wg.Go(func() {
			for range 400 {
				var ids []string
				for _, model := range GetGlobalRegistry().GetAvailableModels("openai") {
					if id, _ := model["id"].(string); strings.HasPrefix(id, "conc-") {
						ids = append(ids, id)
					}
				}
				sort.Strings(ids)
				if !slices.Equal(ids, []string{"conc-mine"}) && !slices.Equal(ids, []string{"conc-held", "conc-mine"}) && len(ids) != 0 {
					t.Errorf("a list mixes rules: %v", ids)
					return
				}
				ProvimuxNamesView()
			}
		})
	}
	wg.Wait()
	for w := range 4 {
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
