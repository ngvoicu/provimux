package savings

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

// E (only-my-names round 2): requests are also counted under the name the client sent, apart from
// the rows, and given with include=requested_models. The rows and every existing field are unchanged;
// a day written before keeps only its rows.
func TestRequestedModels(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)

	// A day written before this change: rows only.
	old := newDayRows("Europe/Bucharest", 7200)
	old.mergePending(map[pendingKey]*counters{{k: "oauth", p: "claude", m: "claude-opus-5-5"}: {Req: 5, In: 10}}, nil)
	old.asked = nil
	data, err := old.encode("2026-11-19")
	if err != nil || bytes.Contains(data, []byte("requested")) {
		t.Fatalf("an old day: %s %v", data, err)
	}
	if _, err := store.KVSet(context.Background(), dayKey("2026-11-19"), data, kvstore.KVSetOptions{}); err != nil {
		t.Fatal(err)
	}

	alias := claudeRecord("claude-opus-5-5", "key-a", 3, 0, 0, 1)
	alias.Alias = "Mine(high)"
	prefixed := claudeRecord("claude-opus-5-5", "key-a", 1, 0, 0, 1)
	prefixed.Alias = "team-a/mine"
	plain := claudeRecord("claude-opus-5-5", "key-a", 1, 0, 0, 1) // no alias: not counted by name
	env.record(alias)
	env.record(alias)
	env.record(prefixed)
	env.record(plain)

	check := func(when string) {
		t.Helper()
		without := env.query(t, "period=7d&group=model")
		if without.RequestedModels != nil {
			t.Fatalf("%s: requested_models without include", when)
		}
		raw, _ := json.Marshal(without)
		if bytes.Contains(raw, []byte("requested_models")) {
			t.Fatalf("%s: the default answer changed: %s", when, raw)
		}
		if rows := without.Breakdowns[0].Rows; len(rows) != 1 || *rows[0].Model != "claude-opus-5-5" || rows[0].Requests != 9 {
			t.Fatalf("%s: rows by model %+v", when, rows)
		}
		with := env.query(t, "period=7d&group=model&include=requested_models")
		got := with.RequestedModels
		if got == nil || got.From == nil || *got.From != today || len(got.Models) != 2 ||
			got.Models[0] != (RequestedModel{Model: "mine", Requests: 2, Tokens: 8}) ||
			got.Models[1] != (RequestedModel{Model: "team-a/mine", Requests: 1, Tokens: 2}) {
			t.Fatalf("%s: requested_models %+v", when, got)
		}
		a, _ := json.Marshal(with.Totals)
		b, _ := json.Marshal(without.Totals)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: totals moved with include", when)
		}
	}
	check("in memory")
	env.round()
	check("from the store")
	if _, err := ParseRequest(map[string][]string{"include": {"nothing"}}); err == nil {
		t.Fatal("an unknown include value is accepted")
	}
}
