package musecode

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// relayCommitRecorder records, at the first WriteHeader it receives, the header snapshot and status, and the bytes
// of the Write that follows.
type relayCommitRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	committed bool
	snapshot  http.Header
	status    int
	nextBytes []byte
	nextKnown bool
}

func (r *relayCommitRecorder) WriteHeader(status int) {
	r.mu.Lock()
	if !r.committed {
		r.committed, r.snapshot, r.status = true, r.ResponseRecorder.Header().Clone(), status
	}
	r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(status)
}

func (r *relayCommitRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	if !r.committed {
		r.committed, r.snapshot, r.status = true, r.ResponseRecorder.Header().Clone(), http.StatusOK
	}
	if !r.nextKnown {
		r.nextKnown, r.nextBytes = true, append([]byte(nil), data...)
	}
	r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

// Row 33 of the first-byte matrix: the relay names the credential whose upstream response it writes, on the commit
// of that response (after a failed first credential, the second's), and nothing on its own error.
func TestRelayNamesACredentialOnlyAtItsOwnFirstByte(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := func(status int, marker string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"served_by":"`+marker+`"}`)
		}))
		t.Cleanup(s.Close)
		return s
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	for _, test := range []struct {
		name   string
		first  func() string
		second func() string
		status int
		named  string // host named, "" for none
		marker string
	}{
		{name: "first answers", first: func() string { return upstream(http.StatusOK, "first").URL }, status: http.StatusOK, named: "127.0.0.1", marker: "first"},
		{name: "first answers an upstream error", first: func() string { return upstream(http.StatusBadRequest, "first").URL }, status: http.StatusBadRequest, named: "127.0.0.1", marker: "first"},
		{name: "first login refused under a continue rule, second answers", first: func() string { return upstream(http.StatusUnauthorized, "scoped_continue_401").URL }, second: func() string {
			return strings.Replace(upstream(http.StatusOK, "second").URL, "127.0.0.1", "localhost", 1)
		}, status: http.StatusOK, named: "localhost", marker: "second"},
		{name: "upstream unreachable", first: func() string { return closedURL }, status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
			first := &auth.Auth{ID: "meta-first", Provider: "meta", Status: auth.StatusActive,
				Attributes: map[string]string{"api_key": "made-up-first", "base_url": test.first() + "/v1", "priority": "10"}}
			if test.second != nil {
				// A login the relay leaves for the next credential, as the request-scoped rule says.
				cfg.OAuthRequestScopedErrors = map[string][]config.RequestScopedErrorRule{"meta": {{Status: http.StatusUnauthorized, Match: []string{"scoped_continue_401"}, Action: auth.RequestScopedActionContinue}}}
				first.Attributes["auth_kind"] = "oauth"
				first.Metadata = map[string]any{"type": "meta", "access_token": "made-up-first", "api_key": "made-up-first", "base_url": first.Attributes["base_url"]}
			}
			manager.SetConfig(cfg)
			manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
			registerMetaTestCred(t, manager, first, "muse-wonder-1.0")
			if test.second != nil {
				registerMetaTestCred(t, manager, &auth.Auth{ID: "meta-second", Provider: "meta", Status: auth.StatusActive,
					Attributes: map[string]string{"api_key": "made-up-second", "base_url": test.second() + "/v1", "priority": "0"}}, "muse-wonder-1.0")
			}
			engine := gin.New()
			engine.Use(logging.CPATraceIDMiddleware())
			engine.Any("/muse-code/*path", NewHandler(manager, cfg).Handle)
			recorder := &relayCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"go"}`)))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			got := logging.AuthStatement{Kind: recorder.snapshot.Get(logging.CPAAuthKindHeader), Provider: recorder.snapshot.Get(logging.CPAAuthProviderHeader), Host: recorder.snapshot.Get(logging.CPAAuthHostHeader)}
			if test.named == "" {
				if got != (logging.AuthStatement{}) {
					t.Fatalf("a proxy error names %+v", got)
				}
				return
			}
			if want := (logging.AuthStatement{Kind: "apikey", Provider: "meta", Host: test.named}); got != want {
				t.Fatalf("headers %+v, want %+v", got, want)
			}
			if recorder.status != test.status || !recorder.nextKnown || !strings.Contains(string(recorder.nextBytes), `"served_by":"`+test.marker+`"`) {
				t.Fatalf("named commit: status %d by %q, want the %s upstream's response", recorder.status, recorder.nextBytes, test.marker)
			}
		})
	}
}
