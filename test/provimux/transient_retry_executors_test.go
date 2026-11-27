package provimux

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Every built-in executor in front of a stand-in vendor that answers every request with one fault: the number of
// requests the stand-in receives for one client call, with the rule on and off. They are equal for every fault but
// the executor's own documented overload refusal where the allow-list has one (Anthropic's overloaded_error for
// claude, OpenAI's server_is_overloaded for codex), which gets exactly one second try.

// executorFixture is one built-in executor with a synthetic account against the stand-in's URL.
type executorFixture struct {
	name     string
	provider string
	source   string
	model    string
	body     string
	overload string // the fault name of its documented overload refusal ("" without an allow-list entry)
	build    func(cfg *config.Config) coreauth.ProviderExecutor
	auth     func(url string) *coreauth.Auth
	// redirect: the executor sends only to its vendor's own host (Copilot), so the call's transport seam sends those
	// requests to the stand-in instead; nothing is dialled but loopback.
	redirect bool
}

// redirectTransport sends every request to the stand-in at target, whatever its URL.
type redirectTransport struct {
	next   http.RoundTripper
	target *url.URL
}

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host, clone.Host = r.target.Scheme, r.target.Host, ""
	return r.next.RoundTrip(clone)
}

func apiKeyAuth(provider string, attrs map[string]string) func(string) *coreauth.Auth {
	return func(url string) *coreauth.Auth {
		a := &coreauth.Auth{ID: "fixture-" + provider, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "synthetic", "base_url": url}}
		for k, v := range attrs {
			a.Attributes[k] = v
		}
		return a
	}
}

var builtinExecutors = []executorFixture{
	{name: "claude", provider: "claude", source: "claude", model: testModel, overload: "anthropic-overloaded",
		body:  `{"model":"` + testModel + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewClaudeExecutor(cfg) }, auth: apiKeyAuth("claude", nil)},
	{name: "codex", provider: "codex", source: "openai-response", model: "provimux-test-codex", overload: "openai-overloaded",
		body:  `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewCodexExecutor(cfg) }, auth: apiKeyAuth("codex", nil)},
	{name: "gemini", provider: "gemini", source: "gemini", model: "fixture",
		body:  `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewGeminiExecutor(cfg) }, auth: apiKeyAuth("gemini", nil)},
	{name: "gemini-interactions", provider: "gemini-interactions", source: "openai", model: "fixture",
		body: `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor {
			return runtimeexecutor.NewGeminiInteractionsExecutor(cfg)
		}, auth: apiKeyAuth("gemini-interactions", nil)},
	{name: "vertex", provider: "vertex", source: "gemini", model: "fixture",
		body: `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor {
			return runtimeexecutor.NewGeminiVertexExecutor(cfg)
		}, auth: apiKeyAuth("vertex", nil)},
	{name: "kimi", provider: "kimi", source: "openai", model: "fixture",
		body:  `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewKimiExecutor(cfg) }, auth: apiKeyAuth("kimi", nil)},
	{name: "xai", provider: "xai", source: "openai-response", model: "provimux-test-xai",
		body:  `{"model":"provimux-test-xai","input":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewXAIExecutor(cfg) }, auth: apiKeyAuth("xai", nil)},
	{name: "meta", provider: "meta", source: "openai-response", model: "fixture",
		body:  `{"model":"fixture","input":"hello"}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewMetaExecutor(cfg) }, auth: apiKeyAuth("meta", nil)},
	{name: "openai-compatible", provider: "openai-compatible-fixture", source: "openai", model: "fixture",
		body: `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor {
			return runtimeexecutor.NewOpenAICompatExecutor("openai-compatible-fixture", cfg)
		},
		auth: apiKeyAuth("openai-compatible-fixture", map[string]string{"compat_name": "fixture", "provider_key": "openai-compatible-fixture"})},
	{name: "devin", provider: "devin", source: "openai", model: "fixture",
		body:  `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewDevinExecutor(cfg) }, auth: apiKeyAuth("devin", nil)},
	{name: "copilot", provider: copilot.Provider, source: "openai", model: copilotChatModel(), redirect: true,
		body:  `{"model":"` + copilotChatModel() + `","messages":[{"role":"user","content":"hello"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewCopilotExecutor(cfg) },
		auth: func(url string) *coreauth.Auth {
			return &coreauth.Auth{ID: "fixture-copilot", Provider: copilot.Provider, Status: coreauth.StatusActive,
				Metadata:       map[string]any{"access_token": "synthetic", "base_url": "https://api.individual.githubcopilot.com"},
				CopilotCatalog: copilot.NewCatalog([]copilot.Model{{ID: copilotChatModel(), Endpoints: []string{copilot.Chat}}}, time.Now())}
		}},
	{name: "antigravity", provider: "antigravity", source: "gemini", model: "fixture",
		body:  `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}`,
		build: func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewAntigravityExecutor(cfg) },
		auth: func(url string) *coreauth.Auth {
			return &coreauth.Auth{ID: "fixture-antigravity", Provider: "antigravity", Status: coreauth.StatusActive,
				Metadata:   map[string]any{"access_token": "synthetic", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339), "project_id": "fixture"},
				Attributes: map[string]string{"base_url": url}}
		}},
}

// copilotChatModel is a model upstream's Copilot compatibility list sends to /chat/completions (Copilot serves only
// listed models), read from that list so that no model name is written here; "" when it lists none.
func copilotChatModel() string {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "auth", "copilot", "compat.json"))
	if err != nil {
		return ""
	}
	var entries map[string]struct {
		Endpoint string `json:"endpoint"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return ""
	}
	var names []string
	for name, entry := range entries {
		if entry.Endpoint == copilot.Chat {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// The faults every executor meets, each answered to every request of the call.
var executorFaults = []string{"anthropic-overloaded", "openai-overloaded", "502", "503", "504", "429-overloaded",
	"spend-limit", "spend-limit-in-200", "reset-after-read", "partial-200"}

func serveExecutorFault(w http.ResponseWriter, fault string) {
	switch fault {
	case "anthropic-overloaded":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = io.WriteString(w, bodyOverloaded)
	case "openai-overloaded":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = io.WriteString(w, bodyOpenAIOverloaded)
	case "502", "503", "504":
		code := map[string]int{"502": 502, "503": 503, "504": 504}[fault]
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, "<html><body>"+http.StatusText(code)+"</body></html>")
	case "429-overloaded":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, bodyOverloaded)
	case "spend-limit":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `{"error":{"type":"service_unavailable_error","code":"organization_spend_limit_exceeded","message":"You reached the monthly spend limit."}}`)
	case "spend-limit-in-200":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.failed\ndata: "+`{"type":"response.failed","response":{"id":"resp_x","status":"failed","error":{"type":"organization_spend_limit_exceeded","code":"organization_spend_limit_exceeded","message":"You reached the monthly spend limit."}}}`+"\n\n")
	case "reset-after-read":
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	case "partial-200":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "2000")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":"accepted","choices":[`)
		w.(http.Flusher).Flush()
	default:
		panic("unknown fault " + fault)
	}
}

const bodyOpenAIOverloaded = `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"The requested model is temporarily overloaded.","param":null}}`

// executorCalls makes one client call (Execute, or ExecuteStream drained) against the executor whose stand-in
// answers every request with fault, and returns the requests the stand-in received and the waits the rule asked for.
func executorCalls(t *testing.T, fx executorFixture, fault string, rule *bool, stream bool) (int, int) {
	t.Helper()
	isolateHome(t)
	clock := installClock(t, nil)
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body) // the whole request is read before the fault
		mu.Lock()
		calls++
		mu.Unlock()
		serveExecutorFault(w, fault)
	}))
	defer server.Close()
	cfg := &config.Config{SameAccountRetry: rule}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(fx.build(cfg))
	auth := fx.auth(server.URL)
	auth.ID = auth.ID + "-" + strings.NewReplacer("/", "-").Replace(t.Name())
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	model := &registry.ModelInfo{ID: fx.model}
	if fx.provider == copilot.Provider {
		model.SourceModelID = copilot.ModelPrefix + fx.model // Copilot serves published routes only
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, fx.provider, []*registry.ModelInfo{model})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	req := cliproxyexecutor.Request{Model: fx.model, Payload: []byte(fx.body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(fx.source), OriginalRequest: req.Payload, Stream: stream}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // a guard only: every case ends on its own
	defer cancel()
	if fx.redirect {
		target, _ := url.Parse(server.URL)
		ctx = httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper { return redirectTransport{next: next, target: target} })
	}
	if stream {
		result, err := manager.ExecuteStream(ctx, []string{fx.provider}, req, opts)
		if err == nil && result != nil {
			for range result.Chunks {
			}
		}
	} else {
		_, errExec := manager.Execute(ctx, []string{fx.provider}, req, opts)
		if errExec != nil {
			t.Logf("the call's error: %v", errExec)
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("the call did not end: %v", ctx.Err())
	}
	mu.Lock()
	defer mu.Unlock()
	return calls, len(clock.recorded())
}

func TestTransientRetryEveryExecutor(t *testing.T) {
	var table strings.Builder
	for _, fx := range builtinExecutors {
		for _, stream := range []bool{false, true} {
			for _, fault := range executorFaults {
				name := fx.name + "/" + streamName(stream) + "/" + fault
				t.Run(name, func(t *testing.T) {
					off, offWaits := executorCalls(t, fx, fault, ruleOff, stream)
					on, onWaits := executorCalls(t, fx, fault, ruleOn, stream)
					fmt.Fprintf(&table, "%-20s %-9s %-20s off=%d on=%d\n", fx.name, streamName(stream), fault, off, on)
					if offWaits != 0 {
						t.Errorf("the rule off waited %d times", offWaits)
					}
					if off == 0 {
						t.Skipf("%s never reached the stand-in offline", fx.name)
					}
					if fault == fx.overload {
						if on != off+1 || onWaits != 1 {
							t.Errorf("documented overload: on %d requests (%d waits), off %d; want exactly one second try", on, onWaits, off)
						}
						return
					}
					if on != off || onWaits != 0 {
						t.Errorf("on %d requests (%d waits), off %d; want the same", on, onWaits, off)
					}
				})
			}
		}
	}
	t.Logf("requests the stand-in received per client call:\n%s", table.String())
}
