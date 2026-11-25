package onlymynames

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func rule(enabled bool, aliases []string, keep []string, prefixes ...string) *Rule {
	r := NewRule(enabled, false)
	for _, alias := range aliases {
		r.AddName(alias, SetByAlias)
	}
	r.AddKeep(keep)
	for _, prefix := range prefixes {
		r.AddPrefix(prefix)
	}
	return r
}

// Section 18, by structure: the rule reads no registration. Its package imports nothing but the standard
// library entries below, so it cannot reach the registry (or anything else that holds registrations); a test
// in sdk/cliproxy checks that the door's judgement calls nothing but this package.
func TestRuleImportsNoRegistry(t *testing.T) {
	allowed := map[string]bool{"fmt": true, "sort": true, "strings": true, "sync": true, "sync/atomic": true, "time": true}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, errParse := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if errParse != nil {
			t.Fatal(errParse)
		}
		for _, spec := range parsed.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if !allowed[path] {
				t.Errorf("%s imports %s: the rule must be a function of its value and the name (only-my-names.md 18)", file, path)
			}
		}
	}
	if _, err := os.Stat("rule.go"); err != nil {
		t.Fatal(err)
	}
}

// B: names are compared exactly, trimmed and nothing else.
func TestRuleExactNames(t *testing.T) {
	r := rule(true, []string{" opus "}, []string{" GPT-5.5 ", "mixed-case"})
	for name, want := range map[string]bool{"opus": true, " opus": true, "OPUS": false, "GPT-5.5": true, "gpt-5.5": false, "Mixed-Case": false, "mixed-case": true, "other": false} {
		if got := r.Serves(name); got != want {
			t.Errorf("Serves(%q) = %v", name, got)
		}
	}
	if names := r.Names(); len(names) != 3 || names[0].Name != "opus" || names[1].Name != "GPT-5.5" {
		t.Fatalf("names %+v", names)
	}
}

// C: a name with a thinking suffix is served when its base is.
func TestRuleSuffixes(t *testing.T) {
	r := rule(true, nil, []string{"local-base"}, "crew")
	for name, want := range map[string]bool{"local-base": true, "local-base(high)": true, "local-base(low)": true, "crew/local-base(high)": true, "other(high)": false, "local-base(": false} {
		if got := r.Serves(name); got != want {
			t.Errorf("Serves(%q) = %v", name, got)
		}
	}
}

// 1 (section 18): a copy of one of his names under a prefix the config declares is his; under any other
// prefix (a login's own file) it is not, unless keep lists it in full. A namespace is part of the name.
func TestRulePrefixesFromTheConfig(t *testing.T) {
	r := rule(true, []string{"mine"}, []string{"opus", "devin/x", "solo/opus"}, " crew ", "team-a", "crew")
	if !slices.Equal(r.Prefixes(), []string{"crew", "team-a"}) {
		t.Fatalf("prefixes %v", r.Prefixes())
	}
	for name, want := range map[string]bool{"crew/mine": true, "team-a/opus": true, "solo/mine": false, "solo/opus": true, "crew/devin/x": true,
		"devin/x": true, "x": false, "crew/other": false, "crew/": false, "crew": false, "crewmine": false, "crew/crew/mine": false} {
		if got := r.Serves(name); got != want {
			t.Errorf("Serves(%q) = %v", name, got)
		}
	}
	if name, ok := r.CopyOf("team-a/opus"); !ok || name != "opus" {
		t.Fatalf("CopyOf team-a/opus: %q %v", name, ok)
	}
}

// 1 (section 18): upstream's copy is the exact concatenation prefix + "/" + name. Only the whole name is
// trimmed, once, as a request is; a space after the slash (or before it) makes another name, not a copy.
func TestRuleCopyIsTheExactConcatenation(t *testing.T) {
	r := rule(true, []string{"mine-t"}, []string{"opus"}, "team-a", "data team")
	for name, want := range map[string]bool{"team-a/mine-t": true, " team-a/mine-t ": true, "team-a/mine-t(high)": true,
		"data team/opus": true, "team-a/ mine-t": false, "team-a/mine-t x": false, "team-a /mine-t": false,
		"team-a/ mine-t(high)": false, "team-a/\tmine-t": false, "data team/ opus": false, "data team /opus": false} {
		if got := r.Serves(name); got != want {
			t.Errorf("Serves(%q) = %v", name, got)
		}
	}
	for _, id := range []string{"team-a/ mine-t", "team-a/\u00a0mine-t", "team-a/\nmine-t"} {
		if name, ok := r.CopyOf(id); ok {
			t.Errorf("CopyOf(%q) = %q", id, name)
		}
	}
	if name, ok := r.CopyOf(" team-a/mine-t "); !ok || name != "mine-t" {
		t.Fatalf("CopyOf of the trimmed whole: %q %v", name, ok)
	}
}

// F: keep takes exact names; *, empty and auto are ignored and reported; auto is never served.
func TestRuleKeepIgnoredAndAuto(t *testing.T) {
	r := rule(true, []string{"auto"}, []string{"claude-*", "  ", "auto", "gpt"})
	want := []KeepIgnored{{Value: "claude-*", Reason: "pattern"}, {Value: "  ", Reason: "empty"}, {Value: "auto", Reason: "reserved"}}
	if !slices.Equal(r.KeepIgnored(), want) {
		t.Fatalf("keep_ignored %+v", r.KeepIgnored())
	}
	if r.Serves("auto") || r.Serves("auto(high)") || r.Has("auto") || !r.Serves("gpt") {
		t.Fatal("auto")
	}
}

// Refuses: only while the rule applies; Home and off refuse nothing.
func TestRuleRefuses(t *testing.T) {
	if rule(false, nil, nil).Refuses("x") || NewRule(true, true).Refuses("x") || (*Rule)(nil).Refuses("x") {
		t.Fatal("refused while not applying")
	}
	if !rule(true, nil, nil).Refuses("x") {
		t.Fatal("not refused while applying")
	}
	if msg := NotServedMessage("opus(high)"); msg != "model opus is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models)." {
		t.Fatalf("message %q", msg)
	}
}

// The value put in force is a copy with its generation; the counter counts every refused request under the rule
// in force (whether or not anything registers the name), at most 256 names, and restarts when the switch goes on.
func TestPutAndCount(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	clock := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	t.Cleanup(SetClock(func() time.Time { return clock }))
	given := rule(true, []string{"mine"}, nil)
	stored, wasOn := Put(given)
	if wasOn || stored == given || stored.Generation() != 1 || given.Generation() != 0 || Current() != stored {
		t.Fatalf("put: %v %d", wasOn, stored.Generation())
	}
	Count(stored, "nobody(high)")
	clock = clock.Add(time.Minute)
	Count(stored, "nobody")
	Count(given, "not-in-force")
	refused := RefusedNow()
	if refused == nil || len(refused.Names) != 1 || refused.Names[0].Name != "nobody" || refused.Names[0].Requests != 2 || !refused.Names[0].LastAt.Equal(clock) {
		t.Fatalf("refused %+v", refused)
	}
	for i := range 300 {
		Count(stored, fmt.Sprintf("n-%d", i))
	}
	if refused := RefusedNow(); len(refused.Names) != 256 || !refused.Truncated {
		t.Fatalf("limit: %d %v", len(refused.Names), refused.Truncated)
	}
	Put(rule(true, []string{"mine", "x"}, nil)) // on to on keeps the count
	if refused := RefusedNow(); len(refused.Names) != 256 {
		t.Fatal("a reload with the switch on dropped the count")
	}
	Put(rule(false, nil, nil))
	if RefusedNow() != nil {
		t.Fatal("off keeps a counter")
	}
	clock = clock.Add(time.Hour)
	on, _ := Put(rule(true, nil, nil))
	if refused := RefusedNow(); refused == nil || len(refused.Names) != 0 || !refused.Since.Equal(clock) || on.Generation() != 4 {
		t.Fatalf("on again: %+v gen %d", refused, on.Generation())
	}
}

// Serves allocates nothing.
func TestServesAllocatesNothing(t *testing.T) {
	r := rule(true, []string{"mine"}, []string{"opus"}, "crew", "team-a")
	if allocs := testing.AllocsPerRun(200, func() {
		r.Serves("crew/mine(high)")
		r.Serves("nobody")
		r.Refuses("opus")
	}); allocs != 0 {
		t.Fatalf("%v allocations", allocs)
	}
}
