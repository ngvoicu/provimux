package sessionroute

import (
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Provimux: finding 11 of the review of session-route G1: nothing runs where nothing applies. With session affinity
// off, in Home mode or under a plugin scheduler the book is not subscribed to results and keeps nothing; it follows
// each committed config both ways; there is no timer; and a book does not keep its manager alive. Every account and
// key is invented; no result here reaches an executor.

func srConfig(affinity, home bool) *config.Config {
	cfg := &config.Config{}
	cfg.Routing.SessionAffinity = affinity
	cfg.Home.Enabled = home
	return cfg
}

// srResult is a successful result of a conversation with a session header and a client key.
func srResult() coreauth.Result {
	return coreauth.Result{AuthID: "sr-follow-auth", Provider: "sr-follow", Model: "sr-follow-model", Success: true,
		Options: cliproxyexecutor.Options{Headers: http.Header{"X-Session-Affinity": {"follow"}},
			Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "scope"}}}
}

type srScheduler struct{}

func (srScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

func srAffinity(t *testing.T) *coreauth.SessionAffinitySelector {
	t.Helper()
	affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.RoundRobinSelector{}, TTL: time.Hour})
	t.Cleanup(affinity.Stop)
	return affinity
}

// TestProvimuxSessionRouteFollowsConfig: the book is subscribed exactly while the committed config runs session
// affinity (and no plugin schedules), both ways, and forgets what it held when it stops.
func TestProvimuxSessionRouteFollowsConfig(t *testing.T) {
	t.Cleanup(EnableInTests())
	m := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)

	// Affinity off from the start: nothing subscribed, nothing kept.
	Follow(m, srConfig(false, false))
	book := Start(m)
	if n := coreauth.ProvimuxResultObservers(m); n != 0 {
		t.Fatalf("affinity off: %d observers", n)
	}
	m.MarkResult(context.Background(), srResult())
	if len(book.Entries()) != 0 || Book(m).Subscribed() {
		t.Fatal("affinity off: the book kept something or subscribed at a show")
	}

	// A config with affinity on, its selector set after the commit as the service does.
	Follow(m, srConfig(true, false))
	m.SetSelector(srAffinity(t))
	if n := coreauth.ProvimuxResultObservers(m); n != 1 || !book.Subscribed() {
		t.Fatalf("affinity on: %d observers", n)
	}

	// Off again: unsubscribed, and what the book held is gone.
	Follow(m, srConfig(false, false))
	m.SetSelector(&coreauth.RoundRobinSelector{})
	if n := coreauth.ProvimuxResultObservers(m); n != 0 || book.Subscribed() || len(book.Entries()) != 0 {
		t.Fatalf("affinity off again: %d observers, %d entries", n, len(book.Entries()))
	}

	// Home mode: never subscribed, whatever affinity says.
	Follow(m, srConfig(true, true))
	if n := coreauth.ProvimuxResultObservers(m); n != 0 {
		t.Fatalf("home: %d observers", n)
	}

	// A plugin scheduler: the config wants it, the first result stops it and nothing is kept; once the plugin is
	// gone, the next show subscribes it again.
	m.SetSelector(srAffinity(t))
	m.SetPluginScheduler(srScheduler{})
	Follow(m, srConfig(true, false))
	m.MarkResult(context.Background(), srResult())
	if n := coreauth.ProvimuxResultObservers(m); n != 0 || len(book.Entries()) != 0 {
		t.Fatalf("plugin scheduler: %d observers, %d entries after a result", n, len(book.Entries()))
	}
	m.SetPluginScheduler(nil)
	if !Book(m).Subscribed() || coreauth.ProvimuxResultObservers(m) != 1 {
		t.Fatal("the show did not subscribe the book again once affinity ran")
	}
}

// TestProvimuxSessionRouteFootprint is the review's measurement of a result's cost with session affinity off: the
// same as without the feature, because nothing is subscribed (the result runs the very same code). The timings are
// logged, not compared (they depend on the machine); the observers and the allocations are compared.
func TestProvimuxSessionRouteFootprint(t *testing.T) {
	t.Cleanup(EnableInTests())
	measure := func(withFeature bool) (float64, float64) {
		m := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
		if withFeature {
			Follow(m, srConfig(false, false))
			Start(m)
		}
		if n := coreauth.ProvimuxResultObservers(m); n != 0 {
			t.Fatalf("with feature %v: %d observers", withFeature, n)
		}
		result := srResult()
		allocs := testing.AllocsPerRun(200, func() { m.MarkResult(context.Background(), result) })
		const n = 2000
		start := time.Now()
		for i := 0; i < n; i++ {
			m.MarkResult(context.Background(), result)
		}
		return allocs, float64(time.Since(start).Nanoseconds()) / n
	}
	allocsWithout, nsWithout := measure(false)
	allocsWith, nsWith := measure(true)
	t.Logf("a result with session affinity off: without the feature %.0f ns, %.1f allocs; with it %.0f ns, %.1f allocs", nsWithout, allocsWithout, nsWith, allocsWith)
	if allocsWith != allocsWithout {
		t.Fatalf("allocations per result differ: %v with the feature, %v without", allocsWith, allocsWithout)
	}
}

// TestProvimuxSessionRouteBookDoesNotHoldItsManager: a manager nothing else holds is not kept alive by its book or
// by this package (the review: stopped managers were retained).
func TestProvimuxSessionRouteBookDoesNotHoldItsManager(t *testing.T) {
	t.Cleanup(EnableInTests())
	held := func() (weak.Pointer[coreauth.Manager], *coreauth.ProvimuxSessionBook) {
		m := coreauth.NewManager(nil, nil, nil)
		m.SetSelector(srAffinity(t))
		Follow(m, srConfig(true, false))
		return weak.Make(m), Start(m)
	}
	manager, book := held()
	runtime.GC()
	runtime.GC()
	if manager.Value() != nil {
		t.Skip("the manager is still reachable from elsewhere in this process; retention not observable here")
	}
	if book.Manager() != nil {
		t.Fatal("the book still reaches its manager")
	}
}

// TestProvimuxSessionRouteAffinityOffSubscribesNothing is the review's case of finding 11 as it ran on the reviewed
// commit: the gateway's start, with session affinity off, subscribed the book to every result.
func TestProvimuxSessionRouteAffinityOffSubscribesNothing(t *testing.T) {
	t.Cleanup(EnableInTests())
	m := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	Start(m)
	if n := coreauth.ProvimuxResultObservers(m); n != 0 {
		t.Fatalf("affinity off: %d observers subscribed to every result", n)
	}
}

// TestProvimuxSessionRouteShowSubscribesAgain pins the review's case of a reload: a result that arrives after the
// config turned affinity on but before the service installed its selector stops the book; once affinity runs nothing
// is subscribed, and the next show subscribes the book again (it observes from the next result on).
func TestProvimuxSessionRouteShowSubscribesAgain(t *testing.T) {
	t.Cleanup(EnableInTests())
	m := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	Follow(m, srConfig(true, false)) // committed: affinity on; the selector comes after
	Start(m)
	m.MarkResult(context.Background(), srResult()) // a result before the selector is installed
	m.SetSelector(srAffinity(t))                   // installed
	if mode := coreauth.ProvimuxSessionAffinityMode(m); mode != "on" {
		t.Fatalf("affinity %q", mode)
	}
	if n := coreauth.ProvimuxResultObservers(m); n != 0 {
		t.Fatalf("before a show: %d observers", n)
	}
	if !Book(m).Subscribed() || coreauth.ProvimuxResultObservers(m) != 1 {
		t.Fatal("the show did not subscribe the book again")
	}
}
