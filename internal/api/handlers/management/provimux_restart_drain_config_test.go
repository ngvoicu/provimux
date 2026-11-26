package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Provimux: the console reads and writes server.restart-drain-seconds through the generic config path, with no
// endpoint of Provimux's own. An unset key reads 404 and a set key reads as the file has it. A whole number out
// of range is saved as written (the read reports it: 31) and used as its bound (30, or 0); with it in the file,
// saving another key works. Anything that is not a whole number is refused with 422 and the file stays as it was.
func TestProvimuxRestartDrainThroughGenericConfigPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\n  shutdown-timeout: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	reloads := make(chan *config.Config, 16)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	const key = "/v8/management/config/server/restart-drain-seconds"
	request := func(method, url, body string, status int, wantBody string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status || !strings.Contains(recorder.Body.String(), wantBody) {
			t.Fatalf("%s %s %s: %d %s, want %d with %q", method, url, body, recorder.Code, recorder.Body.String(), status, wantBody)
		}
	}
	// waitBudget waits for a reload that hands the runtime the budget want (reloads run asynchronously).
	waitBudget := func(want int) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case cfg := <-reloads:
				if cfg.ProvimuxStopDrainSeconds() == want {
					return
				}
			case <-deadline:
				t.Fatalf("no reload with budget %d", want)
			}
		}
	}
	fileHas := func(want string) {
		t.Helper()
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), want) {
			t.Fatalf("file lacks %q:\n%s", want, data)
		}
	}

	request(http.MethodGet, key, "", http.StatusNotFound, `{"error":"not_found"}`)
	request(http.MethodPut, key, "10", http.StatusOK, `"status":"ok"`)
	fileHas("restart-drain-seconds: 10")
	waitBudget(10)
	request(http.MethodGet, key, "", http.StatusOK, "10")
	request(http.MethodPut, key, "0", http.StatusOK, `"status":"ok"`)
	fileHas("restart-drain-seconds: 0")
	request(http.MethodGet, key, "", http.StatusOK, "0")
	// A whole number out of range: saved as written, read back as written, used as its bound.
	for _, tc := range []struct {
		value string
		used  int
	}{{"31", 30}, {"-1", 0}} {
		request(http.MethodPut, key, tc.value, http.StatusOK, `"status":"ok"`)
		fileHas("restart-drain-seconds: " + tc.value)
		waitBudget(tc.used)
		request(http.MethodGet, key, "", http.StatusOK, tc.value)
	}
	request(http.MethodPut, key, "31", http.StatusOK, `"status":"ok"`)
	waitBudget(30)
	// With 31 in the file, a save of another key works and keeps 31.
	request(http.MethodPut, "/v8/management/config/server/shutdown-timeout", "4", http.StatusOK, `"status":"ok"`)
	fileHas("shutdown-timeout: 4")
	fileHas("restart-drain-seconds: 31")
	waitBudget(30)
	request(http.MethodPatch, "/v8/management/config/server", `{"restart-drain-seconds":45}`, http.StatusOK, `"status":"ok"`)
	request(http.MethodGet, key, "", http.StatusOK, "45")
	request(http.MethodPut, key, "0", http.StatusOK, `"status":"ok"`)
	waitBudget(0)
	// Not a whole number: refused as the read path refuses it (422).
	for _, bad := range []string{"2.75", `"10"`, "true"} {
		request(http.MethodPut, key, bad, http.StatusUnprocessableEntity,
			`{"error":"invalid_config","message":"parse config payload: server.restart-drain-seconds must be a whole number of seconds from 0 to 30`)
		fileHas("restart-drain-seconds: 0")
	}
	request(http.MethodPatch, "/v8/management/config/server", `{"restart-drain-seconds":30}`, http.StatusOK, `"status":"ok"`)
	request(http.MethodGet, key, "", http.StatusOK, "30")
	request(http.MethodDelete, key, "", http.StatusOK, `"status":"ok"`)
	request(http.MethodGet, key, "", http.StatusNotFound, `{"error":"not_found"}`)
	request(http.MethodDelete, key, "", http.StatusNotFound, `{"error":"not_found"}`)
	fileHas("shutdown-timeout: 4")
}
