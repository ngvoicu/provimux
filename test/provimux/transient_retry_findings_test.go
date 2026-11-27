package provimux

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The review's findings as its own cases, on the real executors in front of loopback stand-ins, with the rule on. Each
// failed on the reviewed head (534d89452) and passes now. The wire of each case is the review's.

// singleAccountCall makes one client call through a manager with one synthetic account of the executor, against a
// stand-in that answers its first request with first and every later one with "400 invalid input", and returns the
// requests the stand-in received.
func singleAccountCall(t *testing.T, provider, source, model, body string, build func(*config.Config) coreauth.ProviderExecutor, first func(w http.ResponseWriter), stream bool) int {
	t.Helper()
	isolateHome(t)
	loopbackVendors(t)
	installClock(t, nil)
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			first(w)
			return
		}
		http.Error(w, "invalid input", http.StatusBadRequest)
	}))
	defer server.Close()
	cfg := &config.Config{SameAccountRetry: ruleOn}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(build(cfg))
	auth := &coreauth.Auth{ID: "fixture-" + strings.NewReplacer("/", "-").Replace(t.Name()), Provider: provider, Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(source), OriginalRequest: req.Payload, Stream: stream}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // a guard only
	defer cancel()
	if stream {
		if result, err := manager.ExecuteStream(ctx, []string{provider}, req, opts); err == nil && result != nil {
			for range result.Chunks {
			}
		}
	} else {
		_, _ = manager.Execute(ctx, []string{provider}, req, opts)
	}
	mu.Lock()
	defer mu.Unlock()
	return calls
}

func resetAfterRead(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

func partial200(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "2000")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"accepted","choices":[`))
	w.(http.Flusher).Flush()
}

var (
	buildGemini       = func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewGeminiExecutor(cfg) }
	buildInteractions = func(cfg *config.Config) coreauth.ProviderExecutor {
		return runtimeexecutor.NewGeminiInteractionsExecutor(cfg)
	}
	buildVertex = func(cfg *config.Config) coreauth.ProviderExecutor {
		return runtimeexecutor.NewGeminiVertexExecutor(cfg)
	}
	buildCodex  = func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewCodexExecutor(cfg) }
	buildMeta   = func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewMetaExecutor(cfg) }
	buildKimi   = func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewKimiExecutor(cfg) }
	buildXAI    = func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewXAIExecutor(cfg) }
	buildClaude = func(cfg *config.Config) coreauth.ProviderExecutor { return runtimeexecutor.NewClaudeExecutor(cfg) }
	buildCompat = func(cfg *config.Config) coreauth.ProviderExecutor {
		return runtimeexecutor.NewOpenAICompatExecutor("fixture", cfg)
	}
)

const jobTools = `"store":true,"background":true,"tools":[{"type":"image_generation"},{"type":"mcp","server_label":"fixture","server_url":"https://fixture.invalid","require_approval":"never"}]`

// Finding 1: a reset after the vendor read the whole request is never followed by a second request: a translated
// Gemini interaction, Gemini image and speech generations, a Vertex image, Responses with background work and hosted
// tools (Codex, Meta, Kimi, xAI), a stored chat completion; and on the Claude and Codex executors, which have an
// allow-list entry.
func TestTransientRetryFinding1ResetAfterRead(t *testing.T) {
	cases := []struct {
		name, provider, source, model, body string
		build                               func(*config.Config) coreauth.ProviderExecutor
	}{
		{"translated interaction", "gemini-interactions", "openai", "fixture", `{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`, buildInteractions},
		{"gemini image", "gemini", "gemini", "fixture", `{"contents":[{"parts":[{"text":"draw a bee"}],"role":"user"}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`, buildGemini},
		{"gemini speech", "gemini", "gemini", "fixture", `{"contents":[{"parts":[{"text":"read this"}],"role":"user"}],"generationConfig":{"responseModalities":["AUDIO"],"speechConfig":{"voiceConfig":{"prebuiltVoiceConfig":{"voiceName":"Kore"}}}}}`, buildGemini},
		{"vertex image", "vertex", "gemini", "fixture", `{"contents":[{"parts":[{"text":"draw a bee"}],"role":"user"}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`, buildVertex},
		{"codex responses with jobs", "codex", "openai-response", "provimux-test-codex", `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}],` + jobTools + `}`, buildCodex},
		{"meta responses with jobs", "meta", "openai-response", "fixture", `{"input":"hello",` + jobTools + `}`, buildMeta},
		{"kimi responses with jobs", "kimi", "openai-response", "fixture", `{"model":"fixture","input":[{"role":"user","content":"hello"}],` + jobTools + `}`, buildKimi},
		{"xai responses with jobs", "xai", "openai-response", "provimux-test-xai", `{"model":"provimux-test-xai","input":[{"role":"user","content":"hello"}],` + jobTools + `}`, buildXAI},
		{"stored chat completion", "fixture", "openai", "fixture", `{"messages":[{"role":"user","content":"hello"}],"store":true}`, buildCompat},
		{"claude message", "claude", "claude", testModel, `{"model":"` + testModel + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, buildClaude},
	}
	for _, c := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(c.name+"/"+streamName(stream), func(t *testing.T) {
				if got := singleAccountCall(t, c.provider, c.source, c.model, c.body, c.build, resetAfterRead, stream); got != 1 {
					t.Fatalf("the stand-in received %d requests, want 1", got)
				}
			})
		}
	}
}

// Finding 2: a 200 whose body is cut short is an answer that began: never followed by a second request (the
// OpenAI-compatible executor of the review, the Claude and Codex executors).
func TestTransientRetryFinding2Partial200(t *testing.T) {
	for _, c := range []struct {
		name, provider, source, model, body string
		build                               func(*config.Config) coreauth.ProviderExecutor
	}{
		{"openai-compatible", "fixture", "openai", "fixture", `{"messages":[{"role":"user","content":"hello"}]}`, buildCompat},
		{"claude", "claude", "claude", testModel, `{"model":"` + testModel + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, buildClaude},
		{"codex", "codex", "openai-response", "provimux-test-codex", `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}]}`, buildCodex},
	} {
		if got := singleAccountCall(t, c.provider, c.source, c.model, c.body, c.build, partial200, false); got != 1 {
			t.Errorf("%s: the stand-in received %d requests, want 1", c.name, got)
		}
	}
}

// Finding 3: a Codex WebSocket turn whose socket is lost after the vendor read the turn is never sent again, on the
// SDK's non-streaming Execute path and on ExecuteStream; a refused upgrade is not a turn either.
func TestTransientRetryFinding3WebSocketTurn(t *testing.T) {
	for _, mode := range []string{"loss-nonstream", "loss-stream", "refused-upgrade-nonstream"} {
		t.Run(mode, func(t *testing.T) {
			isolateHome(t)
			loopbackVendors(t)
			installClock(t, nil)
			var mu sync.Mutex
			upgrades, frames := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				upgrades++
				n := upgrades
				mu.Unlock()
				if mode == "refused-upgrade-nonstream" && n == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, bodyOpenAIOverloaded)
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
				mu.Lock()
				frames++
				mu.Unlock()
				if strings.HasPrefix(mode, "loss") {
					return // the turn was read; the socket is lost
				}
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_f","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
			}))
			defer server.Close()
			cfg := &config.Config{SameAccountRetry: ruleOn}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			auth := &coreauth.Auth{ID: "fixture-ws-" + mode, Provider: "codex", Status: coreauth.StatusActive,
				Attributes: map[string]string{"api_key": "synthetic", "base_url": server.URL, "websockets": "true"}}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "provimux-test-codex"}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			req := cliproxyexecutor.Request{Model: "provimux-test-codex", Payload: []byte(`{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: req.Payload}
			ctx, cancel := context.WithTimeout(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), 20*time.Second)
			defer cancel()
			if mode == "loss-stream" {
				if result, err := manager.ExecuteStream(ctx, []string{"codex"}, req, opts); err == nil && result != nil {
					for range result.Chunks {
					}
				}
			} else {
				_, _ = manager.Execute(ctx, []string{"codex"}, req, opts)
			}
			mu.Lock()
			defer mu.Unlock()
			if frames > 1 {
				t.Fatalf("the vendor received %d response.create frames, want at most 1", frames)
			}
			if mode == "refused-upgrade-nonstream" && upgrades > 2 {
				t.Fatalf("%d upgrade requests", upgrades)
			}
		})
	}
}

// Finding 4: a spend limit is never sent again, whether it comes as a response.failed in a 200 stream (Codex, Meta)
// or with a code under an error status; the review's limit bodies on the Claude login are not followed by a second
// login request; and the 529 overloaded_error with "Insufficient server capacity", which went to the next account, now
// gets its second try.
func TestTransientRetryFinding4LimitsAndOverload(t *testing.T) {
	spendIn200 := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.failed\ndata: "+`{"type":"response.failed","response":{"id":"resp_x","status":"failed","error":{"type":"organization_spend_limit_exceeded","code":"organization_spend_limit_exceeded","message":"You reached the monthly spend limit."}}}`+"\n\n")
	}
	for _, c := range []struct {
		name, provider, model, body string
		build                       func(*config.Config) coreauth.ProviderExecutor
	}{
		{"codex", "codex", "provimux-test-codex", `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}],` + jobTools + `}`, buildCodex},
		{"meta", "meta", "provimux-test-codex", `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}],` + jobTools + `}`, buildMeta},
	} {
		if got := singleAccountCall(t, c.provider, "openai-response", c.model, c.body, c.build, spendIn200, false); got != 1 {
			t.Errorf("%s spend limit in a 200: %d requests, want 1", c.name, got)
		}
	}
	codexSpend := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"type":"service_unavailable_error","code":"organization_spend_limit_exceeded","message":"You reached the monthly spend limit."}}`)
	}
	if got := singleAccountCall(t, "codex", "openai-response", "provimux-test-codex", `{"model":"provimux-test-codex","input":[{"role":"user","content":"hello"}]}`, buildCodex, codexSpend, false); got != 1 {
		t.Errorf("codex spend code under 503: %d requests, want 1", got)
	}
	for _, c := range []struct {
		status  int
		body    string
		refusal bool
	}{
		{503, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Monthly token allowance exhausted"}}`, false},
		{503, `{"error":{"code":"concurrency_limit_exceeded","message":"Maximum concurrent requests reached"}}`, false},
		{429, `{"error":{"message":"Your concurrency limit is exceeded while the service is overloaded"}}`, false},
		{400, `{"error":{"message":"unknown model overloaded-model"}}`, false},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Insufficient server capacity. Try again shortly."}}`, true},
	} {
		installClock(t, nil)
		p := newPool(t, poolOptions{rule: ruleOn})
		p.vendors["login"].setScript(fault{status: c.status, body: c.body})
		_, body := p.send("", false)
		login := p.vendors["login"].callCount()
		if c.refusal && (login != 2 || answeredBy(body) != "login") {
			t.Errorf("%d %s: login %d requests, answered by %q; want its second try answering", c.status, c.body, login, answeredBy(body))
		}
		if !c.refusal && login != 1 {
			t.Errorf("%d %s: login %d requests, want 1", c.status, c.body, login)
		}
	}
}

// Finding 5: one second try per client call, whatever upstream's rounds. Five accounts that reset every request after
// reading it, request-retry 3: 20 vendor requests off and 20 on (the review saw 40). Five accounts that refuse every
// request as overloaded under 502 (a retry-round status) with the transient cooldown off, so that upstream runs its four
// rounds: off N, on N+1, and the client waited once.
func TestTransientRetryFinding5OncePerClientCall(t *testing.T) {
	total := func(calls map[string]int) int {
		n := 0
		for _, c := range calls {
			n += c
		}
		return n
	}
	for _, stream := range []bool{false, true} {
		results := map[string][2]int{}
		for _, scenario := range []string{"reset", "overloaded"} {
			var counts [2]int
			for i, rule := range []*bool{ruleOff, ruleOn} {
				clock := installClock(t, nil)
				if scenario == "overloaded" {
					coreauth.SetTransientErrorCooldownSeconds(-1)
				}
				p := newPool(t, poolOptions{rule: rule})
				for _, vendor := range p.vendors {
					f := fault{kind: "reset"}
					if scenario == "overloaded" {
						f = fault{status: 502, body: bodyOverloaded}
					}
					vendor.setScript(repeat(f, 40)...)
				}
				p.send("", stream)
				coreauth.SetTransientErrorCooldownSeconds(0)
				counts[i] = total(p.calls())
				waits := len(clock.recorded())
				if rule == ruleOff && waits != 0 || rule == ruleOn && scenario == "overloaded" && waits != 1 || scenario == "reset" && waits != 0 {
					t.Errorf("%s %s rule %v: %d waits", streamName(stream), scenario, *rule, waits)
				}
			}
			results[scenario] = counts
		}
		t.Logf("%s: vendor requests (off, on): resets %v, overloaded %v", streamName(stream), results["reset"], results["overloaded"])
		if r := results["reset"]; r[0] != r[1] || r[0] < 5 {
			t.Errorf("%s resets: off %d, on %d; want equal", streamName(stream), r[0], r[1])
		}
		if o := results["overloaded"]; o[1] != o[0]+1 || o[0] < 10 {
			t.Errorf("%s overloaded: off %d, on %d; want on = off + 1 over upstream's rounds", streamName(stream), o[0], o[1])
		}
	}
}

// Finding 7: the second try is stated by what it does itself. (a) The vendor refuses the first try as overloaded and
// the executor answers the second locally, with no upstream exchange: the answer names nobody. (b) The first try's
// bytes are marked as plugin bytes, the vendor refuses it, and the second try is a genuine vendor answer: it names the
// account.
type attributionExecutor struct {
	coreauth.ProviderExecutor
	mu          sync.Mutex
	n           int
	pluginFirst bool
}

func (e *attributionExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.n++
	n := e.n
	e.mu.Unlock()
	if n == 1 && e.pluginFirst {
		cliproxyexecutor.MarkPluginBytes(ctx)
	}
	if n == 1 || e.pluginFirst {
		return e.ProviderExecutor.Execute(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"type":"message","content":[{"type":"text","text":"synthetic local answer"}]}`)}, nil
}

func TestTransientRetryFinding7Attribution(t *testing.T) {
	answeredBySwitch(t, true)
	for _, pluginFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("pluginFirst=%v", pluginFirst), func(t *testing.T) {
			installClock(t, nil)
			p := newPool(t, poolOptions{rule: ruleOn, trace: true})
			claude, _ := p.manager.Executor("claude")
			p.manager.RegisterExecutor(&attributionExecutor{ProviderExecutor: claude, pluginFirst: pluginFirst})
			p.vendors["login"].setScript(fault{status: 529, body: bodyOverloaded})
			rec := p.sendRecorded("", false)
			auth, _ := p.manager.GetByID(p.authID("login"))
			got := rec.Header().Get(logging.ProvimuxAccountHeader)
			switch {
			case !pluginFirst && (got != "" || rec.Header().Get("X-Cpa-Auth-Kind") != "" || !strings.Contains(rec.Body.String(), "synthetic local answer")):
				t.Fatalf("a local second answer: X-Provimux-Account %q, X-CPA-AUTH-KIND %q; want none; body %s", got, rec.Header().Get("X-Cpa-Auth-Kind"), rec.Body.String())
			case pluginFirst && (got != auth.ProvimuxAccountMarker() || answeredBy(rec.Body.String()) != "login"):
				t.Fatalf("a genuine second answer after plugin bytes on the first: X-Provimux-Account %q, want %q; body %s", got, auth.ProvimuxAccountMarker(), rec.Body.String())
			}
			if p.vendors["login"].callCount() != map[bool]int{false: 1, true: 2}[pluginFirst] {
				t.Fatalf("login %d requests", p.vendors["login"].callCount())
			}
		})
	}
}

// Usage: a refusal carries no tokens. Savings counts a client call whose second try answered as two requests (the
// refused first try, a failed request with no tokens, and the answered second try) and the second try's tokens only.
type usageCatcher struct {
	mu      sync.Mutex
	authID  string
	records []coreusage.Record
	seen    chan struct{}
}

func (c *usageCatcher) HandleUsage(_ context.Context, record coreusage.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if record.AuthID != c.authID {
		return
	}
	c.records = append(c.records, record)
	select {
	case c.seen <- struct{}{}:
	default:
	}
}

func TestTransientRetryUsageRecords(t *testing.T) {
	startSavings(t)
	installClock(t, nil)
	// A model of its own: Savings is one tally for the process, and other tests' records arrive under testModel.
	const model = "provimux-test-usage-retry"
	p := newPool(t, poolOptions{rule: ruleOn, model: model})
	catcher := &usageCatcher{authID: p.authID("login"), seen: make(chan struct{}, 8)}
	coreusage.RegisterNamedPlugin("provimux-transient-retry-usage", catcher)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin("provimux-transient-retry-usage", &usageCatcher{seen: make(chan struct{}, 1)})
	})
	before := getSavings(t, "period=today&model="+model).Totals
	p.vendors["login"].setScript(fault{status: 502, body: bodyOverloaded})
	if _, body := p.send("", false); answeredBy(body) != "login" {
		t.Fatalf("answered by %q", answeredBy(body))
	}
	for i := 0; i < 2; i++ {
		select {
		case <-catcher.seen:
		case <-time.After(10 * time.Second): // a guard only
			t.Fatalf("%d usage records, want 2", len(catcher.records))
		}
	}
	after := getSavings(t, "period=today&model="+model).Totals
	catcher.mu.Lock()
	defer catcher.mu.Unlock()
	failed, answered := catcher.records[0], catcher.records[1]
	if !failed.Failed || failed.Detail.TotalTokens != 0 || answered.Failed || answered.Detail.TotalTokens != 2 {
		t.Fatalf("records: first failed=%v tokens=%d, second failed=%v tokens=%d", failed.Failed, failed.Detail.TotalTokens, answered.Failed, answered.Detail.TotalTokens)
	}
	if after.Requests-before.Requests != 2 || after.Tokens.Input.Total-before.Tokens.Input.Total != 1 || after.Tokens.Output.Total-before.Tokens.Output.Total != 1 {
		t.Fatalf("Savings: %+v -> %+v; want two requests and the second try's 1 input and 1 output token", before, after)
	}
}
