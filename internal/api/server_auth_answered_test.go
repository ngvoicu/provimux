package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const answeredTestModel = "answered-test-model"

var answeredOriginHeaders = []string{internallogging.CPAAuthKindHeader, internallogging.CPAAuthProviderHeader, internallogging.CPAAuthHostHeader}

type answeredUnavailableError struct{}

func (answeredUnavailableError) Error() string   { return "fixture temporarily unavailable" }
func (answeredUnavailableError) StatusCode() int { return http.StatusServiceUnavailable }

// answeredExecutor answers for one provider. An auth in upstreamFail fails with an upstream 503, after waiting for
// wait when it is set; an auth in localFail fails before contacting any upstream.
type answeredExecutor struct {
	provider     string
	upstreamFail map[string]bool
	localFail    map[string]bool
	wait         <-chan struct{}
	attempts     *[]string
	mu           *sync.Mutex
}

func (e *answeredExecutor) Identifier() string { return e.provider }

func (e *answeredExecutor) attempt(ctx context.Context, a *auth.Auth) error {
	e.mu.Lock()
	*e.attempts = append(*e.attempts, a.ID)
	e.mu.Unlock()
	if e.localFail[a.ID] {
		return errors.New("local fixture preflight stopped")
	}
	if !e.upstreamFail[a.ID] {
		// Like a real executor's transport: the response comes from an upstream exchange.
		coreexecutor.MarkUpstreamAttempt(ctx)
		return nil
	}
	if e.wait != nil {
		select {
		case <-e.wait:
		case <-time.After(6 * time.Second):
			return errors.New("fixture did not see the keep-alive")
		}
	}
	coreexecutor.MarkUpstreamAttempt(ctx)
	return answeredUnavailableError{}
}

func (e *answeredExecutor) Execute(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if err := e.attempt(ctx, a); err != nil {
		return coreexecutor.Response{}, err
	}
	return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"served-by-%s"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, answeredTestModel, a.ID))}, nil
}

func (e *answeredExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if err := e.attempt(ctx, a); err != nil {
		return nil, err
	}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"served-by-%s\",\"model\":%q,\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", a.ID, answeredTestModel))}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *answeredExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *answeredExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *answeredExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// flushSignalRecorder closes flushed at the first flush: the moment a keep-alive commits the response headers.
type flushSignalRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    sync.Once
}

func (r *flushSignalRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.once.Do(func() { close(r.flushed) })
}

type answeredSetup struct {
	cfg          *proxyconfig.Config
	credentials  []*auth.Auth
	upstreamFail map[string]bool
	localFail    map[string]bool
	waitForFlush bool
}

func runAnsweredRequest(t *testing.T, setup answeredSetup, stream bool) (*flushSignalRecorder, []string) {
	t.Helper()
	cfg := setup.cfg
	if cfg == nil {
		cfg = &proxyconfig.Config{}
	}
	cfg.APIKeys = []string{"test-key"}
	server := newTestServerWithConfig(t, cfg)
	recorder := &flushSignalRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
	attempts := []string{}
	mu := &sync.Mutex{}
	registered := map[string]bool{}
	for _, credential := range setup.credentials {
		if !registered[credential.Provider] {
			executor := &answeredExecutor{provider: credential.Provider, upstreamFail: setup.upstreamFail, localFail: setup.localFail, attempts: &attempts, mu: mu}
			if setup.waitForFlush {
				executor.wait = recorder.flushed
			}
			server.handlers.AuthManager.RegisterExecutor(executor)
			registered[credential.Provider] = true
		}
		if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
			t.Fatalf("register %s: %v", credential.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: answeredTestModel, OwnedBy: "anthropic"}})
		id := credential.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"max_tokens":8,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, answeredTestModel, stream)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)
	mu.Lock()
	defer mu.Unlock()
	return recorder, append([]string(nil), attempts...)
}

func answeredCredentials() (hostedKey, devinLogin *auth.Auth) {
	hostedKey = &auth.Auth{ID: "answered-hosted-key", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "sk-made-up", "base_url": "https://openrouter.example.test/api/v1", "priority": "10"}}
	devinLogin = &auth.Auth{ID: "answered-devin-login", Provider: "devin", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: "oauth", "priority": "0"}}
	return hostedKey, devinLogin
}

func assertNoAuthOrigin(t *testing.T, headers http.Header) {
	t.Helper()
	for _, name := range answeredOriginHeaders {
		if values, present := headers[http.CanonicalHeaderKey(name)]; present {
			t.Fatalf("%s = %q, want absent: no credential had answered when the headers were committed", name, values)
		}
	}
}

// With keep-alives before the first answer, the headers are committed while the first credential is still pending;
// it then fails and another answers. The committed headers cannot name the one that answered, so they name none.
func TestKeepAliveBeforeAnAnswerCommitsNoCredentialHeaders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			hostedKey, devinLogin := answeredCredentials()
			cfg := &proxyconfig.Config{}
			cfg.NonStreamKeepAliveInterval = 1
			cfg.Streaming.KeepAliveSeconds = 1
			cfg.Streaming.SendKeepAliveBeforeFT = true
			recorder, attempts := runAnsweredRequest(t, answeredSetup{
				cfg:          cfg,
				credentials:  []*auth.Auth{hostedKey, devinLogin},
				upstreamFail: map[string]bool{hostedKey.ID: true},
				waitForFlush: true,
			}, stream)
			result := recorder.Result()
			if result.StatusCode != http.StatusOK || len(attempts) != 2 || attempts[1] != devinLogin.ID || !strings.Contains(recorder.Body.String(), "served-by-"+devinLogin.ID) {
				t.Fatalf("status=%d attempts=%v body=%q, want the hosted key to fail and the login to answer", result.StatusCode, attempts, recorder.Body.String())
			}
			assertNoAuthOrigin(t, result.Header)
		})
	}
}

// The hosted key returns an upstream 503; the login then fails locally without contacting an upstream. The client
// receives an error, which no header may attribute to the login that never answered.
func TestErrorAfterALocalFailureNamesNoSelectedCredential(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			hostedKey, devinLogin := answeredCredentials()
			recorder, attempts := runAnsweredRequest(t, answeredSetup{
				credentials:  []*auth.Auth{hostedKey, devinLogin},
				upstreamFail: map[string]bool{hostedKey.ID: true},
				localFail:    map[string]bool{devinLogin.ID: true},
			}, stream)
			result := recorder.Result()
			if result.StatusCode == http.StatusOK || len(attempts) != 2 {
				t.Fatalf("status=%d attempts=%v, want an error after both credentials were tried", result.StatusCode, attempts)
			}
			if got := result.Header.Get(internallogging.CPAAuthProviderHeader); got == devinLogin.Provider {
				t.Fatalf("%s = %q names the login that only failed its local preflight", internallogging.CPAAuthProviderHeader, got)
			}
			assertNoAuthOrigin(t, result.Header)
		})
	}
}

// On the default configuration (no keep-alive) a streamed answer is named as a plain one is.
func TestStreamedAnswerNamesTheCredentialThatAnswered(t *testing.T) {
	hostedKey, devinLogin := answeredCredentials()
	recorder, attempts := runAnsweredRequest(t, answeredSetup{
		credentials:  []*auth.Auth{hostedKey, devinLogin},
		upstreamFail: map[string]bool{hostedKey.ID: true},
	}, true)
	result := recorder.Result()
	if result.StatusCode != http.StatusOK || len(attempts) != 2 || !strings.Contains(recorder.Body.String(), "served-by-"+devinLogin.ID) {
		t.Fatalf("status=%d attempts=%v body=%q", result.StatusCode, attempts, recorder.Body.String())
	}
	for name, want := range map[string]string{internallogging.CPAAuthKindHeader: "oauth", internallogging.CPAAuthProviderHeader: "devin"} {
		if got := result.Header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if _, present := result.Header[http.CanonicalHeaderKey(internallogging.CPAAuthHostHeader)]; present {
		t.Fatalf("%s present for a login", internallogging.CPAAuthHostHeader)
	}
}

// Alpha search names the Codex login whose upstream answered, and no credential on the proxy's own error when the
// upstream could not be reached.
func TestCodexAlphaSearchNamesTheCredentialOnlyOnTheUpstreamAnswer(t *testing.T) {
	for _, test := range []struct {
		name    string
		httpErr error
		status  int
		named   bool
	}{
		{name: "login answered", status: http.StatusOK, named: true},
		{name: "upstream unreachable", httpErr: errors.New("fixture upstream unreachable"), status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			executor := &codexSearchCaptureExecutor{httpErr: test.httpErr}
			server.handlers.AuthManager.RegisterExecutor(executor)
			credential := &auth.Auth{ID: "codex-login", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "made-up-token"}}
			if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
				t.Fatalf("register: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"q"}`))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, test.status, rr.Body.String())
			}
			if !test.named {
				assertNoAuthOrigin(t, rr.Header())
				return
			}
			for name, want := range map[string]string{internallogging.CPAAuthKindHeader: "oauth", internallogging.CPAAuthProviderHeader: "codex"} {
				if got := rr.Header().Get(name); got != want {
					t.Fatalf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// countingTransport answers every upstream count request in memory and counts them.
type countingTransport struct{ calls *atomic.Int32 }

func (rt countingTransport) RoundTripperFor(*auth.Auth) http.RoundTripper { return rt }

func (rt countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":8}`)), Request: req}, nil
}

// A token count the proxy computes itself names no credential; one an upstream answered names it. Production
// executors: Claude counts upstream for its own API, locally for a key with another base URL; Devin estimates.
func TestTokenCountNamesACredentialOnlyWhenAnUpstreamCounted(t *testing.T) {
	for _, test := range []struct {
		name       string
		credential *auth.Auth
		executor   func(cfg *proxyconfig.Config) auth.ProviderExecutor
		upstream   int32
		want       map[string]string
	}{
		{
			name:       "claude key counted upstream",
			credential: &auth.Auth{ID: "count-native", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "sk-made-up"}},
			executor:   func(cfg *proxyconfig.Config) auth.ProviderExecutor { return runtimeexecutor.NewClaudeExecutor(cfg) },
			upstream:   1,
			want:       map[string]string{internallogging.CPAAuthKindHeader: "apikey", internallogging.CPAAuthProviderHeader: "claude"},
		},
		{
			name:       "claude key on another base URL, counted locally",
			credential: &auth.Auth{ID: "count-hosted", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "sk-made-up", "base_url": "https://openrouter.example.test/api/v1"}},
			executor:   func(cfg *proxyconfig.Config) auth.ProviderExecutor { return runtimeexecutor.NewClaudeExecutor(cfg) },
		},
		{
			name:       "devin login, estimated locally",
			credential: &auth.Auth{ID: "count-devin", Provider: "devin", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: "oauth"}, Metadata: map[string]any{"access_token": "made-up-token"}},
			executor:   func(cfg *proxyconfig.Config) auth.ProviderExecutor { return runtimeexecutor.NewDevinExecutor(cfg) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &proxyconfig.Config{}
			cfg.APIKeys = []string{"test-key"}
			server := newTestServerWithConfig(t, cfg)
			var calls atomic.Int32
			server.handlers.AuthManager.SetRoundTripperProvider(countingTransport{calls: &calls})
			server.handlers.AuthManager.RegisterExecutor(test.executor(cfg))
			if _, errRegister := server.handlers.AuthManager.Register(context.Background(), test.credential); errRegister != nil {
				t.Fatalf("register: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(test.credential.ID, test.credential.Provider, []*registry.ModelInfo{{ID: answeredTestModel, OwnedBy: "anthropic"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(test.credential.ID) })

			req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, answeredTestModel)))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK || calls.Load() != test.upstream {
				t.Fatalf("status=%d upstream calls=%d (want %d) body=%s", rr.Code, calls.Load(), test.upstream, rr.Body.String())
			}
			if test.want == nil {
				assertNoAuthOrigin(t, rr.Header())
				return
			}
			for name, want := range test.want {
				if got := rr.Header().Get(name); got != want {
					t.Fatalf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}
