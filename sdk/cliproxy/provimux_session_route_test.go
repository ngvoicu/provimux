package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: finding 11 of the review of session-route G1, through the service: the book follows the configuration's
// hot reload both ways. With routing.session-affinity off nothing is subscribed to results; a reload that turns it on
// subscribes the book, one that turns it off again unsubscribes it. Nothing is sent anywhere.
func TestProvimuxSessionRouteFollowsHotReload(t *testing.T) {
	t.Cleanup(sessionroute.EnableInTests())
	h := newNamesHarness(t, "routing:\n  session-affinity: false\n")
	m := h.service.coreManager
	if n := coreauth.ProvimuxResultObservers(m); n != 0 {
		t.Fatalf("affinity off at the start: %d observers", n)
	}
	reload := func(affinity bool) {
		t.Helper()
		cfg := h.service.currentConfig().CloneForRuntime()
		cfg.Routing.SessionAffinity = affinity
		h.service.applyWatcherConfigUpdate(cfg)
	}
	reload(true)
	if mode := coreauth.ProvimuxSessionAffinityMode(m); mode != "on" {
		t.Fatalf("after the reload affinity is %q", mode)
	}
	if n := coreauth.ProvimuxResultObservers(m); n != 1 {
		t.Fatalf("affinity on by reload: %d observers", n)
	}
	reload(false)
	if n := coreauth.ProvimuxResultObservers(m); n != 0 {
		t.Fatalf("affinity off again by reload: %d observers", n)
	}
}
