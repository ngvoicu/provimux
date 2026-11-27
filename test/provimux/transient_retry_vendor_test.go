package provimux

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The review of e8d5fcac6, its four points as its own cases: each failed there and passes now.

// Point 1: a Responses request that offers the image tool gets no second try after OpenAI's documented 503 refusal,
// however tool_choice is spelt ("required", or the object), through the public /v1/responses handler.
func TestTransientRetryImageToolAnySpelling(t *testing.T) {
	for _, choice := range []string{`"required"`, `{"type":"image_generation"}`, `"auto"`} {
		for _, rule := range []*bool{ruleOff, ruleOn} {
			t.Run(choice, func(t *testing.T) {
				isolateHome(t)
				loopbackVendors(t)
				installClock(t, nil)
				gin.SetMode(gin.TestMode)
				var calls atomic.Int32
				vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.ReadAll(r.Body)
					calls.Add(1)
					serveExecutorFault(w, "openai-overloaded")
				}))
				defer vendor.Close()
				cfg := &config.Config{SameAccountRetry: rule}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 1)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				auth := apiKeyAuth("codex", nil)(vendor.URL)
				auth.ID += "-image-" + strings.NewReplacer("/", "-").Replace(t.Name())
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				const model = "provimux-test-codex"
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
				defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				router := gin.New()
				router.POST("/v1/responses", openaihandlers.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)).Responses)
				body := `{"model":"` + model + `","input":"Draw a bee","tools":[{"type":"image_generation"}],"tool_choice":` + choice + `}`
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				if got := calls.Load(); got != 1 {
					t.Fatalf("rule %v: the vendor received %d requests, want 1", *rule, got)
				}
			})
		}
	}
}

// vendorHostCall makes one call through a manager with one synthetic account whose base address is base; the call's
// transport seam sends every request to the loopback stand-in, which answers status, header and body to every request.
// It returns the requests the stand-in received. The real host check is in force (no loopbackVendors).
func vendorHostCall(t *testing.T, provider, base string, status int, header map[string]string, body string) int {
	t.Helper()
	isolateHome(t)
	installClock(t, nil)
	var calls atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		calls.Add(1)
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer vendor.Close()
	target, _ := url.Parse(vendor.URL)
	if base == "" {
		base = vendor.URL
	}
	cfg := &config.Config{SameAccountRetry: ruleOn}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 1)
	var executor coreauth.ProviderExecutor = runtimeexecutor.NewClaudeExecutor(cfg)
	source, model, payload := "claude", testModel, `{"model":"`+testModel+`","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`
	if provider == "codex" {
		executor = runtimeexecutor.NewCodexExecutor(cfg)
		source, model, payload = "openai-response", "provimux-test-codex", `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}]}`
	}
	manager.RegisterExecutor(executor)
	auth := apiKeyAuth(provider, nil)(base)
	auth.ID += "-host-" + strings.NewReplacer("/", "-").Replace(t.Name())
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // a guard only
	defer cancel()
	ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		return redirectTransport{next: http.DefaultTransport, target: target} // loopback only, never the named host
	})
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
	_, _ = manager.Execute(ctx, []string{provider}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(source), OriginalRequest: req.Payload})
	return int(calls.Load())
}

// Point 2: the allow-list applies only where its source applies: Anthropic's entry to api.anthropic.com under 529, 503
// and 502; OpenAI's to api.openai.com and chatgpt.com under 503 only. A claude or codex account with another base
// address gets no second try.
func TestTransientRetryOnlyTheVendorsOwnHosts(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		base     string
		status   int
		body     string
		want     int
	}{
		{"claude on api.anthropic.com, 529", "claude", "https://api.anthropic.com", 529, bodyOverloaded, 2},
		{"claude on api.anthropic.com, 503", "claude", "https://api.anthropic.com", 503, bodyOverloaded, 2},
		{"claude on api.anthropic.com, 502", "claude", "https://api.anthropic.com", 502, bodyOverloaded, 2},
		{"claude on api.anthropic.com, 500", "claude", "https://api.anthropic.com", 500, bodyOverloaded, 1},
		{"claude on another https host", "claude", "https://gateway.example.test", 529, bodyOverloaded, 1},
		{"claude on a loopback base", "claude", "", 529, bodyOverloaded, 1},
		{"codex on api.openai.com, 503", "codex", "https://api.openai.com/v1", 503, bodyOpenAIOverloaded, 2},
		{"codex on chatgpt.com, 503", "codex", "https://chatgpt.com/backend-api/codex", 503, bodyOpenAIOverloaded, 2},
		{"codex on api.openai.com, 502", "codex", "https://api.openai.com/v1", 502, bodyOpenAIOverloaded, 1},
		{"codex on another https host", "codex", "https://router.example.test/v1", 503, bodyOpenAIOverloaded, 1},
		{"codex on a loopback base", "codex", "", 503, bodyOpenAIOverloaded, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vendorHostCall(t, c.provider, c.base, c.status, nil, c.body); got != c.want {
				t.Fatalf("the vendor received %d requests, want %d", got, c.want)
			}
		})
	}
}

// Point 3: a refusal that carries x-should-retry: false gets no second try.
func TestTransientRetryShouldRetryFalse(t *testing.T) {
	for _, c := range []struct {
		value string
		want  int
	}{{"false", 1}, {"False", 1}, {"true", 2}, {"", 2}} {
		header := map[string]string{}
		if c.value != "" {
			header["X-Should-Retry"] = c.value
		}
		if got := vendorHostCall(t, "claude", "https://api.anthropic.com", 529, header, bodyOverloaded); got != c.want {
			t.Errorf("x-should-retry %q: the vendor received %d requests, want %d", c.value, got, c.want)
		}
	}
}

// Point 4: the counts are what the gateway did. A second connection refused: the executor was called a second time and
// returned an error, although the vendor never received a second request. A second answer of 200 with an empty body:
// the call returned without an error, and the next account answered.
func TestTransientRetryCountsAreTheGatewaysCalls(t *testing.T) {
	t.Run("second connection refused", func(t *testing.T) {
		isolateHome(t)
		loopbackVendors(t)
		installClock(t, nil)
		var calls atomic.Int32
		var listener net.Listener
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			calls.Add(1)
			_ = listener.Close() // nothing more is accepted: the second connection is refused
			w.Header().Set("Connection", "close")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(529)
			_, _ = io.WriteString(w, bodyOverloaded)
		}))
		listener = server.Listener
		server.Start()
		defer server.Close()
		cfg := &config.Config{SameAccountRetry: ruleOn}
		manager := coreauth.NewManager(nil, nil, nil)
		manager.SetConfig(cfg)
		manager.SetRetryConfig(0, 0, 1)
		manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
		auth := apiKeyAuth("claude", nil)(server.URL)
		auth.ID += "-refused"
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: testModel}})
		defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		before := coreauth.ProvimuxSameAccountRetryStats()
		req := cliproxyexecutor.Request{Model: testModel, Payload: []byte(`{"model":"` + testModel + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)}
		_, _ = manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), OriginalRequest: req.Payload})
		if got := countsDelta(before, coreauth.ProvimuxSameAccountRetryStats()); got != [4]uint64{1, 0, 1, 0} || calls.Load() != 1 {
			t.Fatalf("counts (calls, without error, with error, left) %v, vendor requests %d; want [1 0 1 0] and 1", got, calls.Load())
		}
	})
	t.Run("second answer an empty 200", func(t *testing.T) {
		installClock(t, nil)
		p := newPool(t, poolOptions{rule: ruleOn})
		p.vendors["login"].setScript(fault{status: 529, body: bodyOverloaded}, fault{status: 200, body: ""})
		before := coreauth.ProvimuxSameAccountRetryStats()
		_, body := p.send("", true)
		if got := countsDelta(before, coreauth.ProvimuxSameAccountRetryStats()); got != [4]uint64{1, 1, 0, 0} || answeredBy(body) != "devin" {
			t.Fatalf("counts %v, answered by %q; want [1 1 0 0] (the call returned without an error) and devin", got, answeredBy(body))
		}
	})
}
