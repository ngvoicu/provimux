package registry

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Tests of models.only-my-names (only-my-names.md section 9): the rule, its record, the refusal
// message and its counter. The service-level cases are in sdk/cliproxy.

func namesReset(t *testing.T) {
	t.Helper()
	ResetProvimuxNames()
	t.Cleanup(ResetProvimuxNames)
}

func namesRule(enabled bool, aliases []string, keep ...string) *ProvimuxNamesRule {
	rule := NewProvimuxNamesRule(enabled, false)
	for _, alias := range aliases {
		rule.AddName(alias, ProvimuxNameSetByAlias)
	}
	rule.AddKeep(keep)
	return rule
}

func namesIDs(models []*ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	slices.Sort(ids)
	return ids
}

func namesModels(ids ...string) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, &ModelInfo{ID: id})
	}
	return out
}

func namesOthers(snapshot ProvimuxNamesSnapshot) map[string][]string {
	out := map[string][]string{}
	for _, other := range snapshot.Others {
		out[other.Name] = other.ClientIDs
	}
	return out
}

// R1: the switch off registers everything, and the record says what would be held back.
func TestProvimuxNamesOffServesAllAndRecords(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(false, []string{"opus"}))
	models := namesModels("opus", "gpt-5.5")
	got := ProvimuxOnlyMyNames("acct", "", models)
	if &got[0] != &models[0] || len(got) != 2 {
		t.Fatalf("off: the input must come back as given, got %v", namesIDs(got))
	}
	snapshot := ProvimuxNamesState([]string{"acct"})
	if snapshot.Enabled || snapshot.Refused != nil {
		t.Fatalf("off: enabled %v, refused %v", snapshot.Enabled, snapshot.Refused)
	}
	if others := namesOthers(snapshot); len(others) != 1 || !slices.Equal(others["gpt-5.5"], []string{"acct"}) {
		t.Fatalf("would be held back: %v", others)
	}
	if msg := ProvimuxNotServedMessage("gpt-5.5"); msg != "" {
		t.Fatalf("off: no message, got %q", msg)
	}
	// Without any rule (upstream's tests, before a config commits) the input comes back too.
	ResetProvimuxNames()
	if got := ProvimuxOnlyMyNames("acct", "", models); len(got) != 2 {
		t.Fatalf("no rule: %v", namesIDs(got))
	}
}

// R4: names are compared trimmed and without case.
func TestProvimuxNamesCaseAndSpaces(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{" Opus "}, " gpt-5.5 "))
	got := ProvimuxOnlyMyNames("acct", "", namesModels("opus", "GPT-5.5", "other"))
	if ids := namesIDs(got); !slices.Equal(ids, []string{"GPT-5.5", "opus"}) {
		t.Fatalf("served %v", ids)
	}
	snapshot := ProvimuxNamesState([]string{"acct"})
	if len(snapshot.Names) != 2 || snapshot.Names[0].Name != "Opus" || snapshot.Names[1].Name != "gpt-5.5" {
		t.Fatalf("names keep their first spelling, trimmed: %+v", snapshot.Names)
	}
}

// R3 and R5 at the rule: a login alias (OAuthAlias) is served on its account whatever its name.
func TestProvimuxNamesLoginAliasServed(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, nil))
	models := []*ModelInfo{{ID: "work-fast", OAuthAlias: "work-fast"}, {ID: "team/work-fast", OAuthAlias: "work-fast"}, {ID: "work-fast-source"}}
	if ids := namesIDs(ProvimuxOnlyMyNames("login", "team", models)); !slices.Equal(ids, []string{"team/work-fast", "work-fast"}) {
		t.Fatalf("served %v", ids)
	}
	// The same name on another account, not an alias there, is held back.
	if got := ProvimuxOnlyMyNames("other", "", namesModels("work-fast")); len(got) != 0 {
		t.Fatalf("another account's work-fast: %v", namesIDs(got))
	}
}

// R7: a prefix belongs to the account; a namespace belongs to the name.
func TestProvimuxNamesPrefixAndNamespace(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"opus"}, "x"))
	got := ProvimuxOnlyMyNames("team", "Team-A", namesModels("opus", "team-a/opus", "team-a/other", "devin/x", "team-b/opus"))
	if ids := namesIDs(got); !slices.Equal(ids, []string{"opus", "team-a/opus"}) {
		t.Fatalf("served %v", ids)
	}
	SetProvimuxNamesRule(namesRule(true, []string{"opus"}, "devin/x"))
	if ids := namesIDs(ProvimuxOnlyMyNames("devin", "", namesModels("devin/x", "x"))); !slices.Equal(ids, []string{"devin/x"}) {
		t.Fatalf("keep devin/x: %v", ids)
	}
}

// R8: keep takes exact names; an entry with * or an empty one is ignored and reported.
func TestProvimuxNamesKeepIgnored(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, []string{"mine"}, "claude-*", "  ", "gpt-5.5"))
	got := ProvimuxOnlyMyNames("acct", "", namesModels("claude-opus-5-5", "claude-*", "gpt-5.5", "mine"))
	if ids := namesIDs(got); !slices.Equal(ids, []string{"gpt-5.5", "mine"}) {
		t.Fatalf("served %v", ids)
	}
	snapshot := ProvimuxNamesState([]string{"acct"})
	want := []ProvimuxKeepIgnored{{Value: "claude-*", Reason: "pattern"}, {Value: "  ", Reason: "empty"}}
	if !slices.Equal(snapshot.KeepIgnored, want) {
		t.Fatalf("keep_ignored %+v", snapshot.KeepIgnored)
	}
}

// R10: zero names with the switch on serves nothing and warns at every rule set (every reload).
func TestProvimuxNamesZeroNamesWarns(t *testing.T) {
	namesReset(t)
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)
	warnings := func() int {
		n := 0
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "no name is set") {
				n++
			}
		}
		return n
	}
	SetProvimuxNamesRule(namesRule(true, nil))
	if got := ProvimuxOnlyMyNames("acct", "", namesModels("a", "b")); len(got) != 0 {
		t.Fatalf("zero names served %v", namesIDs(got))
	}
	SetProvimuxNamesRule(namesRule(true, nil, "a*"))
	if n := warnings(); n != 2 {
		t.Fatalf("warnings = %d, want one per rule set", n)
	}
	SetProvimuxNamesRule(namesRule(false, nil))
	SetProvimuxNamesRule(namesRule(true, []string{"a"}))
	if n := warnings(); n != 2 {
		t.Fatalf("warnings = %d after off and on with a name", n)
	}
}

// The refusal message and S4: counts per name, the last time, at most 256 names, and counting
// starts again when the switch goes on.
func TestProvimuxNamesRefusedCounter(t *testing.T) {
	namesReset(t)
	clock := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	t.Cleanup(SetProvimuxNamesClock(func() time.Time { return clock }))
	SetProvimuxNamesRule(namesRule(true, []string{"mine"}))
	held := make([]string, 0, 300)
	for i := range 300 {
		held = append(held, fmt.Sprintf("held-%03d", i))
	}
	ProvimuxOnlyMyNames("acct", "", namesModels(append(held, "mine", "Claude-Opus-5-5")...))

	if msg := ProvimuxNotServedMessage("mine"); msg != "" {
		t.Fatalf("a served name: %q", msg)
	}
	if msg := ProvimuxNotServedMessage("nobody-offers-this"); msg != "" {
		t.Fatalf("a name no account offers keeps upstream's message, got %q", msg)
	}
	want := "model claude-opus-5-5 is not served: this Provimux serves only the names its owner set (models.only-my-names). Keep it on the console's Models page, or ask for a served name (GET /v1/models)."
	if msg := ProvimuxNotServedMessage("claude-opus-5-5"); msg != want {
		t.Fatalf("message %q", msg)
	}
	clock = clock.Add(time.Minute)
	ProvimuxNotServedMessage("claude-opus-5-5")
	snapshot := ProvimuxNamesState([]string{"acct"})
	if snapshot.Refused == nil || !snapshot.Refused.Since.Equal(clock.Add(-time.Minute)) {
		t.Fatalf("refused %+v", snapshot.Refused)
	}
	if got := snapshot.Refused.Names; len(got) != 1 || got[0].Name != "claude-opus-5-5" || got[0].Requests != 2 || !got[0].LastAt.Equal(clock) {
		t.Fatalf("refused names %+v", got)
	}
	for _, name := range held {
		clock = clock.Add(time.Second)
		ProvimuxNotServedMessage(name)
	}
	snapshot = ProvimuxNamesState([]string{"acct"})
	if len(snapshot.Refused.Names) != 256 || !snapshot.Refused.Truncated {
		t.Fatalf("names %d, truncated %v", len(snapshot.Refused.Names), snapshot.Refused.Truncated)
	}
	if snapshot.Refused.Names[0].Name != "held-254" {
		t.Fatalf("most recent first: %s", snapshot.Refused.Names[0].Name)
	}
	if msg := ProvimuxNotServedMessage("held-299"); msg == "" {
		t.Fatal("past 256 names the refusal still says why")
	}

	// Off: no counting, refused is null. On again: a new count from now.
	SetProvimuxNamesRule(namesRule(false, []string{"mine"}))
	if snapshot := ProvimuxNamesState([]string{"acct"}); snapshot.Refused != nil {
		t.Fatalf("off: refused %+v", snapshot.Refused)
	}
	clock = clock.Add(time.Hour)
	SetProvimuxNamesRule(namesRule(true, []string{"mine"}))
	snapshot = ProvimuxNamesState([]string{"acct"})
	if snapshot.Refused == nil || len(snapshot.Refused.Names) != 0 || snapshot.Refused.Truncated || !snapshot.Refused.Since.Equal(clock) {
		t.Fatalf("on again: %+v", snapshot.Refused)
	}
	// A reload that keeps the switch on keeps the count.
	ProvimuxNotServedMessage("held-001")
	SetProvimuxNamesRule(namesRule(true, []string{"mine", "other"}))
	if snapshot := ProvimuxNamesState([]string{"acct"}); len(snapshot.Refused.Names) != 1 {
		t.Fatalf("a reload with the switch on: %+v", snapshot.Refused)
	}
}

// Home mode: the rule does not apply.
func TestProvimuxNamesHomeDoesNotApply(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(NewProvimuxNamesRule(true, true))
	if got := ProvimuxOnlyMyNames("acct", "", namesModels("a")); len(got) != 1 {
		t.Fatalf("home: %v", namesIDs(got))
	}
	if msg := ProvimuxNotServedMessage("a"); msg != "" {
		t.Fatalf("home: %q", msg)
	}
	if snapshot := ProvimuxNamesState(nil); !snapshot.Enabled || !snapshot.Home || snapshot.Refused != nil {
		t.Fatalf("home snapshot %+v", snapshot)
	}
}

// The record of an account that is no longer given is dropped; a plugin executor's stays.
func TestProvimuxNamesRecordsOfOtherClientsDropped(t *testing.T) {
	namesReset(t)
	SetProvimuxNamesRule(namesRule(true, nil))
	ProvimuxOnlyMyNames("gone", "", namesModels("a"))
	ProvimuxOnlyMyNames("plugin:p:prov:executor", "", namesModels("b"))
	ProvimuxNamesState([]string{"kept"})
	if msg := ProvimuxNotServedMessage("a"); msg != "" {
		t.Fatalf("a dropped record still holds a name: %q", msg)
	}
	if msg := ProvimuxNotServedMessage("b"); msg == "" {
		t.Fatal("a plugin executor's record was dropped")
	}
}
