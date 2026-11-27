package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"runtime"
	"sort"
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

// TestProvimuxSessionReturnFinding5 is finding 5: the reviewer changed the world between the sweep's judgement and its
// removal (the higher account cooled, the bound one was raised above it, the switch went off, the conversation was
// used), and each time the sweep removed. The rule judges and removes in one call under the cache's lock, on the
// values of the request being selected, so there is no between: each change, made before the request, is in what the
// request's own selection sees, and nothing is dropped.
func TestProvimuxSessionReturnFinding5(t *testing.T) {
	for _, change := range []string{"higher account cools", "bound account raised", "setting removed", "conversation used"} {
		t.Run(change, func(t *testing.T) {
			calls := &provimuxRTCalls{}
			f := newProvimuxSRFixture(t, "round-robin", false, calls.call)
			f.withTTL(provimuxRTTTL)
			f.rule(provimuxRTCold)
			header := http.Header{"X-Session-Affinity": {"pi-finding-5"}}
			f.fellBack(header, "")
			if change == "conversation used" {
				f.age(time.Hour)
			} else {
				f.age(provimuxRTCold + time.Hour)
			}
			switch change {
			case "higher account cools":
				f.coolHigh(time.Hour)
			case "bound account raised":
				low := mustAuth(t, f.m, f.low)
				low.Attributes["priority"] = "20"
				if _, err := f.m.Update(WithSkipPersist(t.Context()), low); err != nil {
					t.Fatal(err)
				}
			case "setting removed":
				t.Cleanup(SetProvimuxSessionReturnForTest(f.m, ProvimuxSessionReturnSetting{Affinity: true, TTL: provimuxRTTTL}))
			case "conversation used":
				if err := f.turn(header, "", "", nil); err != nil {
					t.Fatal(err)
				}
				f.age(provimuxRTCold - 30*time.Minute) // unused for 2h30m since the first turn, 1h30m since this one
			}
			calls.take()
			before := provimuxReturned.Load()
			if err := f.turn(header, "", "", nil); err != nil {
				t.Fatal(err)
			}
			if got := calls.take(); len(got) != 1 || got[0] != f.low || provimuxReturned.Load() != before {
				t.Fatalf("after %s: called %v, counted %d; want the low account and nothing dropped", change, got, provimuxReturned.Load()-before)
			}
		})
	}
}

// TestProvimuxSessionReturnFinding11 is the rule's part of finding 11: the first build subscribed an observer and a
// ticker per manager in every mode and kept them for stopped managers. The rule has no observer, no goroutine and no
// table outside the selector: putting it in force adds no result hook and starts nothing, and the package-level state
// of its file is the setting and the count, nothing else.
func TestProvimuxSessionReturnFinding11(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	f.withTTL(provimuxRTTTL)
	observers := ProvimuxResultObservers(f.m)
	runtime.GC()
	goroutines := runtime.NumGoroutine()
	f.rule(provimuxRTCold)
	header := http.Header{"X-Session-Affinity": {"pi-finding-11"}}
	f.fellBack(header, "")
	f.age(provimuxRTCold + time.Hour)
	if err := f.turn(header, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if ProvimuxResultObservers(f.m) != observers {
		t.Fatalf("result observers %d, were %d", ProvimuxResultObservers(f.m), observers)
	}
	if now := runtime.NumGoroutine(); now > goroutines {
		t.Fatalf("goroutines %d, were %d", now, goroutines)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "provimux_session_return.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var globals []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				globals = append(globals, name.Name)
			}
		}
	}
	sort.Strings(globals)
	if want := "provimuxReturnCurrent,provimuxReturnSince,provimuxReturned,provimuxReturnedLast"; strings.Join(globals, ",") != want {
		t.Fatalf("package-level state of the rule: %v, want only the setting and the count (%s)", globals, want)
	}
}
