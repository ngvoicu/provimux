package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// provimuxManagementPaths are Provimux's own management endpoints.
var provimuxManagementPaths = []string{
	"/v8/management/provimux/model-routes",
	"/v8/management/provimux/live-lists",
}

// provimuxTestLiveSource stands in for sdk/cliproxy, which registers the real one and which this
// test binary does not link.
type provimuxTestLiveSource struct{}

func (provimuxTestLiveSource) Providers() []string { return []string{"claude"} }

func (provimuxTestLiveSource) State(*coreauth.Auth) management.ProvimuxLiveListState {
	return management.ProvimuxLiveListState{}
}

func provimuxTestServer(t *testing.T, enabled, home bool) *Server {
	t.Helper()
	management.SetProvimuxLiveListSource(provimuxTestLiveSource{})
	t.Cleanup(func() { management.SetProvimuxLiveListSource(nil) })
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home.Enabled = home
	cfg.Plugins.Dir = filepath.Dir(path)
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(enabled)
	s.registerManagementRoutes()
	return s
}

func provimuxServe(s *Server, path string, authorized bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:1234"
	if authorized {
		req.Header.Set("Authorization", "Bearer test-password")
	}
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, req)
	return recorder
}

// The Provimux routes pass the same access control as every other v8 management route.
func TestProvimuxManagementRoutesShareAccessControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                      string
		enabled, home, authorized bool
		want                      int
	}{
		{"authorized", true, false, true, http.StatusOK},
		{"missing key", true, false, false, http.StatusUnauthorized},
		{"disabled", false, false, true, http.StatusNotFound},
		{"home", true, true, true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := provimuxTestServer(t, tc.enabled, tc.home)
			for _, path := range provimuxManagementPaths {
				if recorder := provimuxServe(s, path, tc.authorized); recorder.Code != tc.want {
					t.Fatalf("%s: status=%d want=%d body=%s", path, recorder.Code, tc.want, recorder.Body.String())
				}
			}
		})
	}
}

func TestProvimuxManagementUnknownRouteIsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := provimuxTestServer(t, true, false)
	if recorder := provimuxServe(s, "/v8/management/provimux/nope", true); recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404 body=%s", recorder.Code, recorder.Body.String())
	}
}
