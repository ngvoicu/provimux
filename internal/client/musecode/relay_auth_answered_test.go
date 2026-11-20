package musecode

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The relay names the credential whose upstream produced the response it writes, its own error included, and no
// credential on an error the proxy writes itself because no upstream answered.
func TestRelayNamesTheCredentialWhoseUpstreamAnswered(t *testing.T) {
	answering := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
	}
	unreachable := answering(http.StatusOK)
	unreachableURL := unreachable.URL
	unreachable.Close()

	for _, test := range []struct {
		name    string
		baseURL func(t *testing.T) string
		status  int
		named   bool
	}{
		{name: "upstream answered", baseURL: func(t *testing.T) string {
			s := answering(http.StatusOK)
			t.Cleanup(s.Close)
			return s.URL
		}, status: http.StatusOK, named: true},
		{name: "upstream rate limited", baseURL: func(t *testing.T) string {
			s := answering(http.StatusTooManyRequests)
			t.Cleanup(s.Close)
			return s.URL
		}, status: http.StatusTooManyRequests, named: true},
		{name: "upstream unreachable", baseURL: func(*testing.T) string { return unreachableURL }, status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &config.Config{}
			manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
			registerMetaTestCred(t, manager, &auth.Auth{
				ID:         "meta-answered",
				Provider:   "meta",
				Status:     auth.StatusActive,
				Attributes: map[string]string{"api_key": "made-up-key", "base_url": test.baseURL(t) + "/v1"},
			}, "muse-wonder-1.0")
			engine := gin.New()
			engine.Use(logging.CPATraceIDMiddleware())
			engine.Any("/muse-code/*path", NewHandler(manager, cfg).Handle)

			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"go"}`)))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			want := map[string]string{logging.CPAAuthKindHeader: "", logging.CPAAuthProviderHeader: "", logging.CPAAuthHostHeader: ""}
			if test.named {
				want = map[string]string{logging.CPAAuthKindHeader: "apikey", logging.CPAAuthProviderHeader: "meta", logging.CPAAuthHostHeader: "127.0.0.1"}
			}
			for name, value := range want {
				if got := recorder.Header().Get(name); got != value {
					t.Fatalf("%s = %q, want %q", name, got, value)
				}
			}
		})
	}
}
