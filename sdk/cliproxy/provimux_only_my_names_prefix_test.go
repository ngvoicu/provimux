package cliproxy

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// 1 of section 18: the rule knows the prefixed copies from the config alone. Over generated v8 configs (read by
// the config loader, synthesized and registered as a start does), with entries of every key and provider family
// that takes a prefix, alias rows and plain rows, force-model-prefix on and off, prefixes written with outer and
// inner slashes and spaces, and keep: for every account, the copies of his names that upstream registers under
// the account's prefix are, as a set, exactly the copies the rule computes for the names the account offers; and
// every id upstream registers is served exactly when the oracle says it is.
//
// The oracle does not ask the rule. His names are computed here from the generated rows and keep (an alias that
// differs from its row's name, a keep entry that is not empty, "auto" or a pattern, each trimmed); the declared
// prefixes are the prefixes upstream gave the accounts it synthesized; upstream's copies are what it registers
// for the prefixed config under force-model-prefix: true (then it registers its copies and nothing else), and the
// test checks that with force off it registers the plain config's ids and those copies. A registered id is served
// when it is one of his names, or is p + "/" + n for a declared prefix p and one of his names n.
func TestProvimuxOnlyMyNamesRuleCopiesEqualUpstreamCopies(t *testing.T) {
	namesReset(t)
	families := []string{"gemini", "interactions", "claude", "codex", "xai", "meta", "vertex", "openai-compatibility"}
	// Prefix inputs as written: plain, surrounded by spaces, outer slashes (normalized away), an inner slash
	// (upstream drops the prefix), an inner space (kept), and none.
	prefixInputs := []string{"", "", "crew", "  crew  ", "/crew/", "team-a", "org/crew", "data team", "/ data team /"}
	aliasPool := []string{"mine", "opus", "crew", "gpt-5.5", "Mixed", "devin/x", "fast", "auto", " spaced "}
	// Raw model names (rows with no alias): some look like a copy ("crew/mine", "team-a/ mine" with a space after
	// the slash, "data team/opus").
	rawPool := []string{"up-a", "up-b", "mine", "crew/mine", "team-a/ mine", "data team/opus", "fast", "team-a/mine"}
	keepPool := []string{"mine", "opus", "up-a", "fast", "crew/mine", "team-a/mine", "data team/opus", "team-a/ mine", "claude-*", "auto", "  ", "solo"}
	random := rand.New(rand.NewPCG(18, 554))

	type row struct{ name, alias string }
	type entry struct {
		family, prefix string
		rows           []row
	}
	render := func(entries []entry, withPrefixes, force bool, keep []string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "config-version: 8\nrouting:\n  force-model-prefix: %t\nmodels:\n  only-my-names:\n    enabled: true\n    keep:\n", force)
		for _, k := range keep {
			fmt.Fprintf(&b, "      - %q\n", k)
		}
		if len(keep) == 0 {
			b.WriteString("      []\n")
		}
		b.WriteString("api-keys:\n")
		for _, family := range families {
			first := true
			for i, e := range entries {
				if e.family != family {
					continue
				}
				if first {
					fmt.Fprintf(&b, "  %s:\n", family)
					first = false
				}
				prefix := ""
				if withPrefixes {
					prefix = e.prefix
				}
				fmt.Fprintf(&b, "    - name: entry-%d\n      prefix: %q\n      base-url: http://127.0.0.1:1/v1\n      keys: [{api-key: sk-%s-%d}]\n      models:\n", i, prefix, family, i)
				for _, r := range e.rows {
					if r.alias == "" {
						fmt.Fprintf(&b, "        - {name: %q}\n", r.name)
					} else {
						fmt.Fprintf(&b, "        - {name: %q, alias: %q}\n", r.name, r.alias)
					}
				}
			}
		}
		return b.String()
	}
	load := func(source string) *config.Config {
		file := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadConfig(file)
		if err != nil {
			t.Fatalf("%v\n%s", err, source)
		}
		return cfg
	}
	registered := func(cfg *config.Config) ([]string, [][]string) {
		_, auths := namesService(t, cfg)
		prefixes, ids := make([]string, len(auths)), make([][]string, len(auths))
		for i, auth := range auths {
			prefixes[i] = strings.TrimSpace(auth.Prefix)
			ids[i] = provimuxRegistered(auth)
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
		return prefixes, ids
	}

	type coverage struct{ accounts, copies int }
	covered := map[string]*coverage{}
	prefixSeen := map[string]int{}
	checked, served := 0, 0
	for round := range 320 {
		force := round%2 == 1
		var entries []entry
		for i := range 1 + random.IntN(4) {
			family := families[random.IntN(len(families))]
			if i == 0 {
				family = families[(round/2)%len(families)] // every family, under both settings, in turn
			}
			e := entry{family: family, prefix: prefixInputs[random.IntN(len(prefixInputs))]}
			seen := map[string]bool{}
			for range 1 + random.IntN(4) {
				r := row{name: rawPool[random.IntN(len(rawPool))]}
				if random.IntN(2) == 0 {
					r = row{name: "up-" + fmt.Sprint(random.IntN(6)), alias: aliasPool[random.IntN(len(aliasPool))]}
				}
				if seen[r.name] {
					continue
				}
				seen[r.name] = true
				e.rows = append(e.rows, r)
			}
			entries = append(entries, e)
		}
		var keep []string
		for range random.IntN(4) {
			keep = append(keep, keepPool[random.IntN(len(keepPool))])
		}

		// His names, computed from what was generated.
		his := map[string]bool{}
		for _, e := range entries {
			for _, r := range e.rows {
				name, alias := strings.TrimSpace(r.name), strings.TrimSpace(r.alias)
				if name != "" && alias != "" && alias != name && alias != "auto" {
					his[alias] = true
				}
			}
		}
		for _, k := range keep {
			if k = strings.TrimSpace(k); k != "" && k != "auto" && !strings.Contains(k, "*") {
				his[k] = true
			}
		}

		plainCfg, prefixedCfg, forcedCfg := load(render(entries, false, force, keep)), load(render(entries, true, force, keep)), load(render(entries, true, true, keep))
		if prefixedCfg.ForceModelPrefix != force || !forcedCfg.ForceModelPrefix {
			t.Fatalf("round %d: force-model-prefix lost", round)
		}
		_, without := registered(plainCfg)
		_, forced := registered(forcedCfg)
		prefixes, with := registered(prefixedCfg)
		if len(without) != len(with) || len(forced) != len(with) || len(with) != len(entries) {
			t.Fatalf("round %d: %d entries, %d, %d and %d accounts", round, len(entries), len(without), len(forced), len(with))
		}
		rule := provimuxNamesRuleOf(prefixedCfg)

		// The declared prefixes, as upstream gave them to its accounts.
		declared := map[string]bool{}
		for _, p := range prefixes {
			if p != "" {
				declared[p] = true
			}
		}
		if got, want := rule.Prefixes(), sortedKeys(declared); !slices.Equal(got, want) {
			t.Fatalf("round %d: rule prefixes %q, upstream's accounts %q", round, got, want)
		}
		ruleNames := map[string]bool{}
		for _, own := range rule.Names() {
			ruleNames[own.Name] = true
		}
		if got, want := sortedKeys(ruleNames), sortedKeys(his); !slices.Equal(got, want) {
			t.Fatalf("round %d: rule names %q, the config's %q", round, got, want)
		}
		oracleServes := func(id string) bool {
			if his[id] {
				return true
			}
			for p := range declared {
				if strings.HasPrefix(id, p+"/") && his[strings.TrimPrefix(id, p+"/")] {
					return true
				}
			}
			return false
		}

		for _, registration := range []struct {
			force bool
			ids   [][]string
		}{{force, with}, {true, forced}} {
			for i, e := range entries {
				prefix := prefixes[i]
				for _, id := range registration.ids[i] {
					checked++
					want := oracleServes(id)
					if rule.Serves(id) != want || rule.Serves(id+"(high)") != want {
						t.Fatalf("round %d: %s registers %q: served %v, oracle %v (his %q, declared %q)", round, e.family, id, rule.Serves(id), want, sortedKeys(his), sortedKeys(declared))
					}
					if want {
						served++
					}
				}
				if prefix == "" {
					if !slices.Equal(registration.ids[i], without[i]) {
						t.Fatalf("round %d: an account with no prefix registers %v with prefixes, %v without", round, registration.ids[i], without[i])
					}
					continue
				}
				key := fmt.Sprintf("%s force=%t", e.family, registration.force)
				if covered[key] == nil {
					covered[key] = &coverage{}
				}
				prefixSeen[e.prefix]++
				covered[key].accounts++
				// Every id the prefixed registration adds is a copy: prefix + "/" + an id of the plain registration.
				for _, id := range registration.ids[i] {
					if slices.Contains(without[i], id) {
						continue
					}
					if base, ok := strings.CutPrefix(id, prefix+"/"); !ok || !slices.Contains(without[i], base) {
						t.Fatalf("round %d: %q of the %q account copies nothing of %v", round, id, prefix, without[i])
					}
				}
				// Upstream's copies of his names, as registered: prefix + "/" + b for an id b of the plain registration
				// that is his, when the account registers that id.
				upstream := map[string]bool{}
				for _, base := range without[i] {
					if id := prefix + "/" + base; his[base] && slices.Contains(registration.ids[i], id) {
						upstream[id] = true
					}
				}
				// The rule's copies: the account's prefix, which must be one the rule declares, before each of the
				// rule's names the account offers.
				if !slices.Contains(rule.Prefixes(), prefix) {
					t.Fatalf("round %d: the account's prefix %q is not the rule's %q", round, prefix, rule.Prefixes())
				}
				computed := map[string]bool{}
				for _, own := range rule.Names() {
					if slices.Contains(without[i], own.Name) {
						computed[prefix+"/"+own.Name] = true
					}
				}
				// Equal as sets.
				if got, want := sortedKeys(computed), sortedKeys(upstream); !slices.Equal(got, want) {
					t.Fatalf("round %d: %s %q force=%t: the rule's copies %q, upstream's %q (registered %q, plain %q)", round, e.family, prefix, registration.force, got, want, registration.ids[i], without[i])
				}
				covered[key].copies += len(upstream)
			}
		}
	}
	for _, family := range families {
		for _, force := range []bool{false, true} {
			c := covered[fmt.Sprintf("%s force=%t", family, force)]
			if c == nil || c.accounts == 0 || c.copies == 0 {
				t.Errorf("%s force=%t: no prefixed copy of his names checked (%+v)", family, force, c)
			}
		}
	}
	for _, input := range []string{"crew", "  crew  ", "/crew/", "org/crew", "data team", "/ data team /"} {
		if input != "org/crew" && prefixSeen[input] == 0 {
			t.Errorf("no account with the prefix %q", input)
		}
	}
	copies := 0
	for _, c := range covered {
		copies += c.copies
	}
	t.Logf("320 configs, %d registered ids judged (%d served), %d upstream copies of his names, equal to the rule's, %d family/force pairs", checked, served, copies, len(covered))
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
