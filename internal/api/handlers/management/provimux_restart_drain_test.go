package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// GET /v8/management/provimux/restart-drain: the examples of the contract (design/restart-drain-state.md), each
// from a configuration file as a user writes it. A text value is refused by the gateway: it keeps the configuration
// it runs with (here: the drain unset), and the state says what the file writes and why it is not used.
func TestProvimuxRestartDrainState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := config.SetProvimuxRestartDrainDefault(config.ProvimuxRestartDrainDefaultSeconds) // a real gateway's
	t.Cleanup(func() { config.SetProvimuxRestartDrainDefault(previous) })
	intp := func(v int) *int { return &v }
	strp := func(v string) *string { return &v }
	yes, no := func() *bool { v := true; return &v }(), func() *bool { v := false; return &v }()
	const head = "config-version: 8\nserver:\n  port: 8317\n"
	for _, tc := range []struct {
		name    string
		running string // the configuration the gateway runs with
		file    string // the file now ("" = the same)
		want    ProvimuxRestartDrainState
	}{
		{"unset", head, "", ProvimuxRestartDrainState{Seconds: 25, WaitsAtMost: 25, Default: 25, Max: 30, StopLimit: 30}},
		{"0", head + "  restart-drain-seconds: 0\n", "", ProvimuxRestartDrainState{Seconds: 0, WaitsAtMost: 0, Set: true, Written: strp("0"), WrittenAccepted: yes, WrittenSeconds: intp(0), Default: 25, Max: 30, StopLimit: 30}},
		{"10", head + "  restart-drain-seconds: 10\n", "", ProvimuxRestartDrainState{Seconds: 10, WaitsAtMost: 10, Set: true, Written: strp("10"), WrittenAccepted: yes, WrittenSeconds: intp(10), Default: 25, Max: 30, StopLimit: 30}},
		{"45", head + "  restart-drain-seconds: 45\n", "", ProvimuxRestartDrainState{Seconds: 30, WaitsAtMost: 30, Set: true, Written: strp("45"), WrittenAccepted: yes, WrittenSeconds: intp(30), WrittenClamped: true, Default: 25, Max: 30, StopLimit: 30}},
		{"a text value", head, head + "  restart-drain-seconds: ten\n", ProvimuxRestartDrainState{Seconds: 25, WaitsAtMost: 25, Set: true, Written: strp("ten"), WrittenAccepted: no,
			WrittenError: `server.restart-drain-seconds must be a whole number of seconds from 0 to 30, got "ten"`, Default: 25, Max: 30, StopLimit: 30}},
		{"\"10\" in quotes, 10 still running", head + "  restart-drain-seconds: 10\n", head + "  restart-drain-seconds: \"10\"\n", ProvimuxRestartDrainState{Seconds: 10, WaitsAtMost: 10, Set: true, Written: strp(`"10"`), WrittenAccepted: no,
			WrittenError: `server.restart-drain-seconds must be a whole number of seconds from 0 to 30, got "10"`, Default: 25, Max: 30, StopLimit: 30}},
		{"30.0, 10 still running", head + "  restart-drain-seconds: 10\n", head + "  restart-drain-seconds: 30.0\n", ProvimuxRestartDrainState{Seconds: 10, WaitsAtMost: 10, Set: true, Written: strp("30.0"), WrittenAccepted: no,
			WrittenError: `server.restart-drain-seconds must be a whole number of seconds from 0 to 30, got "30.0"`, Default: 25, Max: 30, StopLimit: 30}},
		{"shutdown-timeout 28, the drain unset", head + "  shutdown-timeout: 28\n", "", ProvimuxRestartDrainState{Seconds: 28, WaitsAtMost: 28, Default: 28, ShutdownTimeout: 28, Max: 30, StopLimit: 30}},
		{"shutdown-timeout 60, the drain unset", head + "  shutdown-timeout: 60\n", "", ProvimuxRestartDrainState{Seconds: 60, WaitsAtMost: 30, Default: 60, ShutdownTimeout: 60, Max: 30, StopLimit: 30}},
		{"null is unset", head + "  restart-drain-seconds: null\n", "", ProvimuxRestartDrainState{Seconds: 25, WaitsAtMost: 25, Default: 25, Max: 30, StopLimit: 30}},
		{"the layout before v8", "port: 8317\nrestart-drain-seconds: 9\n", "", ProvimuxRestartDrainState{Seconds: 9, WaitsAtMost: 9, Set: true, Written: strp("9"), WrittenAccepted: yes, WrittenSeconds: intp(9), Default: 25, Max: 30, StopLimit: 30}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.running), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.file != "" {
				if errWrite := os.WriteFile(path, []byte(tc.file), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			h := NewHandler(cfg, path, nil)
			router := gin.New()
			router.GET("/v8/management/provimux/restart-drain", h.ProvimuxRestartDrain)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v8/management/provimux/restart-drain", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			var got ProvimuxRestartDrainState
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &got); errDecode != nil {
				t.Fatal(errDecode)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("state\n got %s\nwant %+v", recorder.Body.String(), tc.want)
			}
			if got.Seconds != cfg.ProvimuxStopDrainSeconds() {
				t.Fatalf("seconds %d, the stop is given %d", got.Seconds, cfg.ProvimuxStopDrainSeconds())
			}
			t.Logf("%s", recorder.Body.String())
		})
	}
}
