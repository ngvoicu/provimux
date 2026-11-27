package provimux

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the fixture of session-route's before-and-after check (session-route.md 12 row 1; G1). Like the
// answered-by fixture, this file uses only what the commit before session-route has (the design's head), so that it
// builds there: the golden files under testdata/session-route are what that commit writes (go test ./test/provimux
// -run TestProvimuxSessionRouteLeavesOtherPathsAsToday -provimux-answered-golden, with session_route_switch_test.go
// replaced by a stub whose function does nothing). With the book started, this build must write the same bytes on
// every path a caller of the old build could use, and send each turn to the same account. Every key, login and host
// here is invented; the executor is a stand-in in this process.

const sessionRouteFixtureModel = "provimux-session-route-fixture"

// sessionRouteFixtureExecutor answers every format from one exchange, notes which account each call went to, and
// fails on the high account while fail is set.
type sessionRouteFixtureExecutor struct {
	coreauth.ProviderExecutor
	mu    *sync.Mutex
	calls *[]string
	fail  *bool
}

func (sessionRouteFixtureExecutor) Identifier() string { return "session-route-fixture" }

func (e sessionRouteFixtureExecutor) note(auth *coreauth.Auth) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	*e.calls = append(*e.calls, auth.ID)
	if *e.fail && strings.HasSuffix(auth.ID, "-high") {
		return &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
	}
	return nil
}

const sessionRouteFixtureAnswer = `{"id":"chatcmpl-fixture","object":"chat.completion","created":1700000000,"model":"` + sessionRouteFixtureModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func (e sessionRouteFixtureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	if err := e.note(auth); err != nil {
		return coreexecutor.Response{}, err
	}
	return coreexecutor.Response{Payload: []byte(sessionRouteFixtureAnswer)}, nil
}

func (e sessionRouteFixtureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	if err := e.note(auth); err != nil {
		return nil, err
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"` + sessionRouteFixtureModel + `","choices":[{"index":0,"delta":{"content":"fixture answer"},"finish_reason":"stop"}]}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e sessionRouteFixtureExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	if err := e.note(auth); err != nil {
		return coreexecutor.Response{}, err
	}
	return coreexecutor.Response{Payload: []byte(`{"total_tokens":3}`)}, nil
}

func (sessionRouteFixtureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

// sessionRouteFixtureAnswers serves the fixture's requests through a full server with session affinity on and two
// client keys, and returns each answer written out (status, every header in name order, the body) and, for each
// request, the accounts the executor was called with.
func sessionRouteFixtureAnswers(t *testing.T) ([]byte, *coreauth.Manager) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
		fail  bool
	)
	manager := coreauth.NewManager(nil, nil, nil)
	affinity := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.RoundRobinSelector{}, TTL: time.Hour})
	t.Cleanup(affinity.Stop)
	manager.SetSelector(affinity)
	manager.RegisterExecutor(sessionRouteFixtureExecutor{mu: &mu, calls: &calls, fail: &fail})
	for _, auth := range []*coreauth.Auth{
		{ID: "session-route-fixture-high", Provider: "session-route-fixture", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "10"}},
		{ID: "session-route-fixture-low", Provider: "session-route-fixture", Status: coreauth.StatusActive,
			Attributes: map[string]string{"priority": "5", "api_key": "sk-fixture-low", "base_url": "https://low.fixture.example.test/v1"}},
	} {
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: sessionRouteFixtureModel, Object: "model", OwnedBy: "provimux-test", Created: 1700000000}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	tmp := t.TempDir()
	cfg := &config.Config{}
	cfg.APIKeys = []string{"sk-fixture-one", "sk-fixture-two"}
	cfg.AuthDir = filepath.Join(tmp, "auth")
	cfg.Routing.SessionAffinity = true
	server := api.NewServer(cfg, manager, sdkaccess.NewManager(), filepath.Join(tmp, "config.yaml"))
	handler := server.Handler()

	chat := func(extra string) string {
		return `{"model":"` + sessionRouteFixtureModel + `","messages":[{"role":"user","content":"hi"}]` + extra + `}`
	}
	type request struct {
		method, path, key, body string
		header                  http.Header
		failHigh                bool
	}
	requests := []request{
		{method: http.MethodGet, path: "/healthz"},
		{method: http.MethodGet, path: "/v1/models", key: "sk-fixture-one"},
		{method: http.MethodGet, path: "/v1/models?include=accounts", key: "sk-fixture-one"},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-one", body: chat(""), header: http.Header{"X-Session-Affinity": {"fixture-pi-0001"}}},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-one", body: chat(""), header: http.Header{"X-Session-Affinity": {"fixture-pi-0001"}}, failHigh: true},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-one", body: chat(""), header: http.Header{"X-Session-Affinity": {"fixture-pi-0001"}}},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-two", body: chat(`,"stream":true`), header: http.Header{"X-Session-Affinity": {"fixture-pi-0001"}}},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-two", body: chat(`,"prompt_cache_key":"project-a"`)},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-one", body: chat(""), header: http.Header{"Session-Id": {"fixture-codex-0002"}}},
		{method: http.MethodPost, path: "/v1/responses", key: "sk-fixture-one", body: `{"model":"` + sessionRouteFixtureModel + `","input":"hi"}`, header: http.Header{"Session-Id": {"fixture-codex-0002"}}},
		{method: http.MethodPost, path: "/v1/messages", key: "sk-fixture-one", body: `{"model":"` + sessionRouteFixtureModel + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, header: http.Header{"X-Claude-Code-Session-Id": {"fixture-claude-0003"}}},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-fixture-one", body: `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`},
		{method: http.MethodPost, path: "/v1/chat/completions", body: chat("")},
		{method: http.MethodPost, path: "/v1/chat/completions", key: "sk-unknown", body: chat("")},
		{method: http.MethodPost, path: "/v1/provimux/unknown", key: "sk-fixture-one", body: `{}`},
		{method: http.MethodGet, path: "/v1/models", key: "sk-fixture-two"},
	}
	var out strings.Builder
	for _, r := range requests {
		mu.Lock()
		calls = nil
		fail = r.failHigh
		mu.Unlock()
		req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
		if r.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if r.key != "" {
			req.Header.Set("Authorization", "Bearer "+r.key)
		}
		for name, values := range r.header {
			req.Header[name] = values
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		fmt.Fprintf(&out, "%s %s key=%q headers=%v fail-high=%v\n%d\n", r.method, r.path, r.key, r.header, r.failHigh, recorder.Code)
		names := make([]string, 0, len(recorder.Header()))
		for name := range recorder.Header() {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			values := recorder.Header()[name]
			if name == "X-Cpa-Trace-Id" {
				// A trace id holds the time it was made and random bits: written as a constant.
				values = []string{"(trace id)"}
			}
			fmt.Fprintf(&out, "%s: %q\n", name, values)
		}
		body := recorder.Body.String()
		if data := ojson.Get(body, "data"); strings.HasPrefix(r.path, "/v1/models") && data.IsArray() {
			// The registry lists models in its map order: the envelope, then each model's bytes as sent, sorted.
			models := make([]string, 0, len(data.Array()))
			for _, model := range data.Array() {
				if strings.Contains(model.Raw, sessionRouteFixtureModel) {
					models = append(models, model.Raw)
				}
			}
			sort.Strings(models)
			body = "(models of this fixture) " + strings.Join(models, "\n")
		}
		mu.Lock()
		fmt.Fprintf(&out, "%s\ncalls: %v\n\n", body, calls)
		mu.Unlock()
	}
	return []byte(out.String()), manager
}

// checkSessionRouteGolden compares got with the golden file name under testdata/session-route, or writes it under
// -provimux-answered-golden.
func checkSessionRouteGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "session-route", name)
	if *provimuxAnsweredGolden {
		if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		if errWrite := os.WriteFile(path, got, 0o644); errWrite != nil {
			t.Fatal(errWrite)
		}
		return
	}
	want, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("golden %s: %v", name, errRead)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from what the commit before session-route writes:\n got %s\nwant %s", name, got, want)
	}
}
