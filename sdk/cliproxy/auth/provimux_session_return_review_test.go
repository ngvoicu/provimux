package auth

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the review of the rule's first build (a sweep), its findings 2, 5 and 11, each as the reviewer's own case
// against the rule as built now. Findings 4, 6, 7, 8, 9 and 12 are the rows of provimux_session_return_test.go
// (HeldRequest; Aliases and NoteTable; Restored; Bounds; LeftAlone; CountRacing, LeftAlone and the WebSocket test of
// sdk/api/handlers/openai). Every account and key is invented; nothing is sent anywhere.

// TestProvimuxSessionReturnFinding2Image is finding 2 as the reviewer made it: two paid Codex accounts of priority 10
// and a free one of priority 20, and a request that may not use a free account (the image route's restriction). The
// conversation is on a paid account, the first route for that request; unused for cold-after, its next request stays
// there. The sweep moved it to the other paid account.
func TestProvimuxSessionReturnFinding2Image(t *testing.T) {
	calls := &provimuxRTCalls{}
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	f.withTTL(provimuxRTTTL)
	f.rule(provimuxRTCold)
	f.providers = []string{"codex"}
	f.m.RegisterExecutor(provimuxSRExecutor{provider: "codex", call: calls.call})
	for _, a := range []struct{ id, plan, priority string }{{"paid-a", "plus", "10"}, {"paid-b", "plus", "10"}, {"free", "free", "20"}} {
		id := "review-image-" + a.id
		if _, err := f.m.Register(WithSkipPersist(t.Context()), &Auth{ID: id, Provider: "codex", Status: StatusActive,
			Attributes: map[string]string{"priority": a.priority, "plan_type": a.plan, "api_key": "synthetic"}}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: provimuxSRModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	header := http.Header{"Session-Id": {"image"}}
	noFree := map[string]any{cliproxyexecutor.DisallowFreeAuthMetadataKey: true}
	if err := f.turn(header, "", "", noFree); err != nil {
		t.Fatal(err)
	}
	first := calls.take()
	if len(first) != 1 || !strings.Contains(first[0], "paid") {
		t.Fatalf("first request called %v, want a paid account", first)
	}
	before := provimuxReturned.Load()
	f.age(provimuxRTCold + time.Hour)
	if err := f.turn(header, "", "", noFree); err != nil {
		t.Fatal(err)
	}
	if next := calls.take(); len(next) != 1 || next[0] != first[0] || provimuxReturned.Load() != before {
		t.Fatalf("next request called %v, counted %d; want %s again and nothing dropped", next, provimuxReturned.Load()-before, first[0])
	}
}

// TestProvimuxSessionReturnFinding2ProviderSet is finding 2's second case: a provider outside the request's set serves
// the name at a higher priority. For this request the conversation is on its first route, and it stays.
func TestProvimuxSessionReturnFinding2ProviderSet(t *testing.T) {
	calls := &provimuxRTCalls{}
	f := newProvimuxSRFixture(t, "fill-first", true, calls.call)
	f.withTTL(provimuxRTTTL)
	f.rule(provimuxRTCold)
	third := "review-third-" + strings.ReplaceAll(t.Name(), "/", "-")
	f.m.RegisterExecutor(provimuxSRExecutor{provider: third, call: calls.call})
	if _, err := f.m.Register(WithSkipPersist(t.Context()), &Auth{ID: third + "-auth", Provider: third, Status: StatusActive,
		Attributes: map[string]string{"priority": "50", "api_key": "synthetic"}}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(third+"-auth", third, []*registry.ModelInfo{{ID: provimuxSRModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(third + "-auth") })
	header := http.Header{"X-Session-Affinity": {"only-two"}}
	if err := f.turn(header, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.take(); len(got) != 1 || got[0] != f.high {
		t.Fatalf("first request called %v, want the high account of the request's providers", got)
	}
	before := provimuxReturned.Load()
	f.age(provimuxRTCold + time.Hour)
	if err := f.turn(header, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.take(); len(got) != 1 || got[0] != f.high || provimuxReturned.Load() != before {
		t.Fatalf("next request called %v, counted %d; want the high account again and nothing dropped", got, provimuxReturned.Load()-before)
	}
}
