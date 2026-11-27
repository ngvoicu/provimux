package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

func TestFinalReaderTLS(t *testing.T) {
	provimuxSentOn(t, true)
	type step struct{ method, host, path, authority, media, body, actual string }
	first := step{"POST", "openrouter.ai", "/v1/chat/completions", "", "application/json", `{"model":"fixture/first"}`, ""}
	discovery := step{"POST", "cloudcode-pa.googleapis.com", "/v1internal:loadCodeAssist", "", "application/json", `{"metadata":{}}`, ""}
	copyStep := func(v step, change func(*step)) step { change(&v); return v }
	for _, tc := range []struct {
		name, kind string
		steps      []step
		want       string
	}{
		{"current-body", "openai-compatibility", []step{copyStep(first, func(s *step) { s.actual = `{"model":"fixture/current"}` })}, "fixture/current"},
		{"other-executors-catalog", "openai-compatibility", []step{first, {"GET", "api.githubcopilot.com", "/models", "", "", "", ""}}, ""},
		{"own-catalog", "copilot", []step{first, {"GET", "api.githubcopilot.com", "/models", "", "", "", ""}}, "fixture/first"},
		{"exact-discovery", "antigravity", []step{first, discovery}, "fixture/first"},
		{"foreign-discovery", "openai-compatibility", []step{first, discovery}, ""},
		{"prefixed-discovery", "antigravity", []step{first, copyStep(discovery, func(s *step) { s.path = "/other" + s.path })}, ""},
		{"wrong-discovery-media", "antigravity", []step{first, copyStep(discovery, func(s *step) { s.media = "application/x-www-form-urlencoded"; s.body = "content=fixture" })}, ""},
		{"wrong-token-authority", "vertex", []step{first, {"POST", "oauth2.googleapis.com", "/token", "relay.fixture.invalid", "application/x-www-form-urlencoded", "grant_type=fixture", ""}}, ""},
		{"duplicate-success", "openai-compatibility", []step{first, first}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan string, len(tc.steps))
			vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				got <- string(raw)
				w.WriteHeader(200)
			}))
			defer vendor.Close()
			tr := vendor.Client().Transport.(*http.Transport).Clone()
			defer tr.CloseIdleConnections()
			tr.TLSClientConfig.ServerName = "example.com"
			tr.DialContext = func(c context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(c, "tcp", vendor.Listener.Addr().String())
			}
			// Ported (the amendment of 2026-11-27 (2), part 3): a call whose caller asked for the statement.
			ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(tr))))
			client := httptransport.Decorate(ctx, &http.Client{Transport: tr})
			for _, s := range tc.steps {
				req, _ := http.NewRequestWithContext(ctx, s.method, "https://"+s.host+s.path, strings.NewReader(s.body))
				if s.actual != "" {
					req.Body = io.NopCloser(bytes.NewBufferString(s.actual))
					req.ContentLength = int64(len(s.actual))
				}
				if s.media != "" {
					req.Header.Set("Content-Type", s.media)
				}
				if s.authority != "" {
					req.Host = s.authority
				}
				res, e := client.Do(req)
				if e != nil {
					t.Fatal(e)
				}
				res.Body.Close()
				sent := s.body
				if s.actual != "" {
					sent = s.actual
				}
				if received := <-got; received != sent {
					t.Fatal("wrong fixture body", fmt.Sprint(received))
				}
			}
			s, ok := ProvimuxSent(ctx, tc.kind)
			if (ok != (tc.want != "")) || s.Model != tc.want {
				t.Errorf("model=%q ok=%v want=%q", s.Model, ok, tc.want)
			}
		})
	}
}
