package provimux

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// answeredByTestKey is an invented key of the account markers.
var answeredByTestKey = []byte("provimux-answered-test-key-32by!")

// answeredBySwitch puts models.answered-by in force for the test, with the markers' key set either way, and
// restores both after it. (The golden files of answered_by_today_test.go are written with a stub of this function
// that does nothing, on the commit before answered-by.)
func answeredBySwitch(t *testing.T, on bool) {
	t.Helper()
	coreauth.SetProvimuxMarkerKey(answeredByTestKey)
	was := logging.SetProvimuxAnsweredBy(on)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		coreauth.SetProvimuxMarkerKey(nil)
	})
}
