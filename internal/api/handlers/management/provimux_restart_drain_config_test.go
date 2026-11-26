package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Provimux: the console reads and writes server.restart-drain-seconds through the generic config path, with no
// endpoint of Provimux's own. An unset key reads 404, a set key reads as its number, and a value other than a
// whole number from 0 to 30 is refused with 422 and leaves the file as it was.
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
	if got := (<-reloads).ProvimuxStopDrainSeconds(); got != 10 {
		t.Fatalf("reloaded budget = %d", got)
	}
	request(http.MethodGet, key, "", http.StatusOK, "10")
	request(http.MethodPut, key, "0", http.StatusOK, `"status":"ok"`)
	fileHas("restart-drain-seconds: 0")
	request(http.MethodGet, key, "", http.StatusOK, "0")
	for _, bad := range []string{"31", "-1", "2.75", `"10"`, "true"} {
		request(http.MethodPut, key, bad, http.StatusUnprocessableEntity,
			`{"error":"invalid_config","message":"parse config payload: server.restart-drain-seconds must be a whole number of seconds from 0 to 30`)
		fileHas("restart-drain-seconds: 0")
	}
	request(http.MethodPatch, "/v8/management/config/server", `{"restart-drain-seconds":30}`, http.StatusOK, `"status":"ok"`)
	request(http.MethodGet, key, "", http.StatusOK, "30")
	request(http.MethodDelete, key, "", http.StatusOK, `"status":"ok"`)
	request(http.MethodGet, key, "", http.StatusNotFound, `{"error":"not_found"}`)
	request(http.MethodDelete, key, "", http.StatusNotFound, `{"error":"not_found"}`)
	fileHas("shutdown-timeout: 0")
}
