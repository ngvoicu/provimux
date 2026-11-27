package provimux

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// sessionRouteStart starts session-route's book in this test binary for the test, as a gateway does at its start.
// (The golden files of session_route_today_test.go are written with a stub of this function that does nothing, on
// the commit before session-route.)
func sessionRouteStart(t *testing.T) {
	t.Helper()
	t.Cleanup(sessionroute.EnableInTests())
}

// sessionRouteObserved requires that the book observed the fixture's turns, so that the comparison is made with
// session-route at work: the conversation that fell back is noted on the account that answers it, used by both keys.
// (The stub of the commit before session-route does nothing.)
func sessionRouteObserved(t *testing.T, manager *coreauth.Manager) {
	t.Helper()
	for _, entry := range sessionroute.Book(manager).Entries() {
		if entry.Session == "affinity:fixture-pi-0001" && entry.Current == "session-route-fixture-low" && entry.Previous == "session-route-fixture-high" && len(entry.Scopes) == 2 {
			return
		}
	}
	t.Fatalf("the book did not observe the fixture's turns: %+v", sessionroute.Book(manager).Entries())
}
