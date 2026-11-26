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

// Provimux: the Muse relay (row 33 of the first-byte matrix) names the account of the credential whose upstream
// response it writes with the marker of answered-by.md 5.2: after a failed first key the second's, and nothing on its
// own error. Two keys of one host here, so only the marker tells them apart. Fixtures are invented.
func TestProvimuxMuseRelayNamesTheAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth.SetProvimuxMarkerKey([]byte("provimux-muse-test-marker-key-32"))
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		auth.SetProvimuxMarkerKey(nil)
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer made-up-first" && strings.Contains(r.URL.RawQuery, "fail-first") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"scoped_continue_401"}`)
			return
		}
		_, _ = io.WriteString(w, `{"served_by":"`+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`"}`)
	}))
	t.Cleanup(upstream.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	for _, test := range []struct {
		name     string
		query    string
		base     string
		named    string // the credential named, "" for none
		answered string
	}{
		{name: "the first key answers", base: upstream.URL, named: "meta-first", answered: "made-up-first"},
		{name: "the first login is refused under a continue rule, the second answers", query: "?fail-first", base: upstream.URL, named: "meta-second", answered: "made-up-second"},
		{name: "upstream unreachable", base: closedURL},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{"meta": {{Status: http.StatusUnauthorized, Match: []string{"scoped_continue_401"}, Action: auth.RequestScopedActionContinue}}}}
			manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
			first := &auth.Auth{ID: "meta-first", Provider: "meta", Status: auth.StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth", "api_key": "made-up-first", "base_url": test.base + "/v1", "priority": "10"},
				Metadata:   map[string]any{"type": "meta", "access_token": "made-up-first", "api_key": "made-up-first", "base_url": test.base + "/v1", "email": "ada@fixture.example.test"}}
			second := &auth.Auth{ID: "meta-second", Provider: "meta", Status: auth.StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth", "api_key": "made-up-second", "base_url": test.base + "/v1", "priority": "0"},
				Metadata:   map[string]any{"type": "meta", "access_token": "made-up-second", "api_key": "made-up-second", "base_url": test.base + "/v1", "email": "bo@fixture.example.test"}}
			markers := map[string]string{"meta-first": first.ProvimuxAccountMarker(), "meta-second": second.ProvimuxAccountMarker()}
			if markers["meta-first"] == "" || markers["meta-first"] == markers["meta-second"] {
				t.Fatalf("fixture: markers %v", markers)
			}
			manager.SetConfig(cfg)
			manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
			registerMetaTestCred(t, manager, first, "muse-wonder-1.0")
			registerMetaTestCred(t, manager, second, "muse-wonder-1.0")
			engine := gin.New()
			engine.Use(logging.CPATraceIDMiddleware())
			engine.Any("/muse-code/*path", NewHandler(manager, cfg).Handle)
			recorder := &relayCommitRecorder{ResponseRecorder: httptest.NewRecorder()}
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/muse-code/search"+test.query, strings.NewReader(`{"q":"go"}`)))
			kind, account := recorder.snapshot.Get(logging.CPAAuthKindHeader), recorder.snapshot.Values(logging.ProvimuxAccountHeader)
			if test.named == "" {
				if kind != "" || len(account) != 0 {
					t.Fatalf("the proxy's own error (%d) names kind %q, account %v", recorder.Code, kind, account)
				}
				return
			}
			if kind != "oauth" || len(account) != 1 || account[0] != markers[test.named] || !strings.Contains(string(recorder.nextBytes), test.answered) {
				t.Fatalf("status %d by %q: kind %q, account %v; want %s's %s", recorder.Code, recorder.nextBytes, kind, account, test.named, markers[test.named])
			}
		})
	}
}
