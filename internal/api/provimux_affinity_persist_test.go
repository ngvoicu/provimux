package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func provimuxRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-password")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, req)
	return recorder
}

// routing.session-affinity-persist is read and written through the config API like its neighbours,
// and GET /v8/management/provimux/session-affinity-persist says whether it is in effect and why not.
func TestProvimuxAffinityPersistManagement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nrouting:\n  session-affinity: true\nmanagement:\n  secret-key: test-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Dir = filepath.Dir(path)
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	const setting = "/v8/management/config/routing/session-affinity-persist"
	if got := provimuxRequest(s, http.MethodPut, setting, `false`); got.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", got.Code, got.Body.String())
	}
	if got := provimuxRequest(s, http.MethodGet, setting, ""); got.Code != http.StatusOK || strings.TrimSpace(got.Body.String()) != "false" {
		t.Fatalf("GET: %d %s", got.Code, got.Body.String())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "session-affinity-persist: false") {
		t.Fatalf("config file:\n%s", data)
	}

	const state = "/v8/management/provimux/session-affinity-persist"
	management.SetProvimuxAffinityPersistSource(nil)
	if got := provimuxRequest(s, http.MethodGet, state, ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("without a source: %d", got.Code)
	}
	management.SetProvimuxAffinityPersistSource(func() any {
		return map[string]any{"setting": true, "set": false, "in_effect": true}
	})
	t.Cleanup(func() { management.SetProvimuxAffinityPersistSource(nil) })
	if got := provimuxRequest(s, http.MethodGet, state, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"in_effect":true`) || !strings.Contains(got.Body.String(), `"set":false`) {
		t.Fatalf("state: %d %s", got.Code, got.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, state, nil)
	req.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("without the management key: %d", recorder.Code)
	}
}
