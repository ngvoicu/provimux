package cliproxy

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// 1 of section 18: the rule knows the prefixed copies from the config alone. Over generated configs (key and
// provider entries with and without a prefix, alias rows and plain rows, force-model-prefix on and off, keep),
// in the settled state: for every account the config declares, the copies of his names that upstream registers
// under the account's prefix are exactly the copies the rule computes, and every copy upstream registers is
// served exactly when what it copies is. Upstream's copies are found by registering the same config twice, with
// and without its prefixes: the ids only the prefixed registration has.
func TestProvimuxOnlyMyNamesRuleCopiesEqualUpstreamCopies(t *testing.T) {
	namesReset(t)
	random := rand.New(rand.NewPCG(18, 1))
	pool := []string{"mine", "opus", "crew", "gpt-5.5", "Mixed", "devin/x", "fast", "solo"}
	prefixes := []string{"", "", "crew", " crew ", "team-a", "solo", "/team-b/"}
	pick := func() string { return pool[random.IntN(len(pool))] }
	build := func(withPrefixes bool, seeds []int64, force bool, keep []string) *config.Config {
		r := rand.New(rand.NewPCG(uint64(seeds[0]), uint64(seeds[1])))
		cfg := &config.Config{}
		cfg.ForceModelPrefix = force
		prefix := func() string {
			p := prefixes[r.IntN(len(prefixes))]
			if !withPrefixes {
				return ""
			}
			return p
		}
		rows := func() [][2]string {
			var out [][2]string
			seen := map[string]bool{}
			for range 1 + r.IntN(4) {
				name := "up-" + pool[r.IntN(len(pool))]
				alias := ""
				if r.IntN(2) == 0 {
					alias = pool[r.IntN(len(pool))]
				}
				if seen[name] {
					continue
				}
				seen[name] = true
				out = append(out, [2]string{name, alias})
			}
			return out
		}
		for i := range 1 + r.IntN(2) {
			key := config.ClaudeKey{APIKey: fmt.Sprintf("sk-claude-%d", i), BaseURL: "http://127.0.0.1:1", Prefix: prefix()}
			for _, row := range rows() {
				key.Models = append(key.Models, config.ClaudeModel{Name: row[0], Alias: row[1]})
			}
			cfg.ClaudeKey = append(cfg.ClaudeKey, key)
		}
		for i := range r.IntN(2) {
			key := config.CodexKey{APIKey: fmt.Sprintf("sk-codex-%d", i), BaseURL: "http://127.0.0.1:1", Prefix: prefix()}
			for _, row := range rows() {
				key.Models = append(key.Models, config.CodexModel{Name: row[0], Alias: row[1]})
			}
			cfg.CodexKey = append(cfg.CodexKey, key)
		}
		for i := range r.IntN(2) {
			compat := config.OpenAICompatibility{Name: fmt.Sprintf("compat-%d", i), BaseURL: "http://127.0.0.1:1/v1", Prefix: prefix(),
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: fmt.Sprintf("sk-compat-%d", i)}}}
			for _, row := range rows() {
				compat.Models = append(compat.Models, config.OpenAICompatibilityModel{Name: row[0], Alias: row[1]})
			}
			cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, compat)
		}
		cfg.SanitizeClaudeKeys()
		cfg.SanitizeCodexKeys()
		cfg.SanitizeOpenAICompatibility()
		cfg.Models.OnlyMyNames = namesOn(keep...)
		return cfg
	}
	registered := func(cfg *config.Config) [][]string {
		_, auths := namesService(t, cfg)
		out := make([][]string, len(auths))
		for i, auth := range auths {
			out[i] = provimuxRegistered(auth)
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
		return out
	}
	checked := 0
	for round := range 300 {
		seeds := []int64{int64(round), 7}
		force := random.IntN(3) == 0
		var keep []string
		for range random.IntN(3) {
			keep = append(keep, pick())
		}
		plain, prefixed := build(false, seeds, force, keep), build(true, seeds, force, keep)
		without, with := registered(plain), registered(prefixed)
		provimuxSetNamesRule(prefixed)
		rule := provimuxNamesRuleOf(prefixed)
		auths := keySynthesize(t, prefixed)
		if len(auths) != len(with) || len(without) != len(with) {
			t.Fatalf("round %d: %d auths, %d and %d registrations", round, len(auths), len(without), len(with))
		}
		for i, auth := range auths {
			prefix := strings.TrimSpace(auth.Prefix)
			if prefix == "" {
				continue
			}
			if !slices.Contains(rule.Prefixes(), prefix) {
				t.Fatalf("round %d: the account's prefix %q is not the rule's %v", round, prefix, rule.Prefixes())
			}
			upstreamCopies, ruleCopies := []string{}, []string{}
			for _, id := range with[i] {
				if slices.Contains(without[i], id) {
					continue
				}
				// An id only the prefixed registration has: upstream's copy of an id of the plain registration.
				copied := ""
				for _, base := range without[i] {
					if id == prefix+"/"+base {
						copied = base
					}
				}
				if copied == "" {
					t.Fatalf("round %d: %q of %s copies nothing of %v", round, id, auth.ID, without[i])
				}
				checked++
				if rule.Serves(id) != rule.Serves(copied) {
					t.Fatalf("round %d: copy %q served %v, %q served %v (rule names %v, prefixes %v)", round, id, rule.Serves(id), copied, rule.Serves(copied), rule.Names(), rule.Prefixes())
				}
				if name, ok := rule.CopyOf(id); ok && name == copied {
					upstreamCopies = append(upstreamCopies, id)
				}
			}
			for _, base := range without[i] {
				if rule.Has(base) {
					ruleCopies = append(ruleCopies, prefix+"/"+base)
				}
			}
			slices.Sort(upstreamCopies)
			slices.Sort(ruleCopies)
			// A rule copy upstream did not register is one upstream left as the literal id it already had.
			for _, id := range ruleCopies {
				if !slices.Contains(upstreamCopies, id) && !slices.Contains(with[i], id) {
					t.Fatalf("round %d: the rule's copy %q is not registered by %s: %v", round, id, auth.ID, with[i])
				}
			}
			for _, id := range upstreamCopies {
				if !slices.Contains(ruleCopies, id) {
					t.Fatalf("round %d: upstream's copy %q is not the rule's %v", round, id, ruleCopies)
				}
			}
		}
	}
	if checked < 200 {
		t.Fatalf("only %d copies checked", checked)
	}
	t.Logf("%d prefixed copies checked over 300 configs", checked)
}
