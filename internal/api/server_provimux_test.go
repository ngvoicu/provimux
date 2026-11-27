package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// provimuxManagementPaths are Provimux's own management endpoints.
var provimuxManagementPaths = []string{
	"/v8/management/provimux/model-routes",
	"/v8/management/provimux/live-lists",
	"/v8/management/provimux/model-lists",
	"/v8/management/provimux/savings",
	"/v8/management/provimux/restart-drain",
	"/v8/management/provimux/figures",
	"/v8/management/provimux/session-return",
}

// provimuxTestLiveSource stands in for sdk/cliproxy, which registers the real one and which this
// test binary does not link.
type provimuxTestLiveSource struct{}

func (provimuxTestLiveSource) Providers() []string { return []string{"claude"} }

func (provimuxTestLiveSource) State(*coreauth.Auth) management.ProvimuxLiveListState {
	return management.ProvimuxLiveListState{}
}

type provimuxTestModelListSource struct{}

func (provimuxTestModelListSource) LoginTypes() []string { return []string{"claude"} }

func (provimuxTestModelListSource) ModelListLogin(*coreauth.Auth, bool, bool, bool) (management.ProvimuxModelListLogin, bool) {
	return management.ProvimuxModelListLogin{}, false
}

func provimuxTestServer(t *testing.T, enabled, home bool) *Server {
	t.Helper()
	management.SetProvimuxLiveListSource(provimuxTestLiveSource{})
	management.SetProvimuxModelListSource(provimuxTestModelListSource{})
	t.Cleanup(func() {
		management.SetProvimuxLiveListSource(nil)
		management.SetProvimuxModelListSource(nil)
	})
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
			for _, path := range provimuxManagementPaths {
				// A server of its own for each path: a few requests without a key ban the caller's address.
				s := provimuxTestServer(t, tc.enabled, tc.home)
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

type provimuxSavingsWaiter chan struct{}

func (w provimuxSavingsWaiter) HandleUsage(context.Context, coreusage.Record) {
	select {
	case w <- struct{}{}:
	default:
	}
}

// provimuxSavingsRoutesRuns numbers the runs of the test below in one process: Savings' recorder is the process's
// own, so each run counts a model of its own and never the record of a run before it.
var provimuxSavingsRoutesRuns atomic.Int64

// Savings records from the moment the server registered its management routes.
func TestProvimuxSavingsRecordingStartsWithTheRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registry.SetLocalModelCatalogs(true)
	t.Cleanup(func() { registry.SetLocalModelCatalogs(false) })
	s := provimuxTestServer(t, true, false)
	seen := make(provimuxSavingsWaiter, 4)
	coreusage.RegisterNamedPlugin("provimux-savings-routes-test", seen) // after Savings'
	model := fmt.Sprintf("routes-test-model-%d", provimuxSavingsRoutesRuns.Add(1))
	coreusage.PublishRecord(context.Background(), coreusage.Record{Provider: "claude", Model: model, AuthType: "oauth",
		Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1}})
	<-seen
	recorder := provimuxServe(s, "/v8/management/provimux/savings?period=today&model="+model, true)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"requests":1,`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
