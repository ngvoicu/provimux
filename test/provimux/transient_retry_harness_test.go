package provimux

// The harness of the transient-retry tests: stand-in vendors on loopback, each scripted per call, five
// synthetic accounts that serve one model by priority as in the owner's report, upstream's real
// executors, manager and Claude handler. Nothing leaves loopback; no credential is real.

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

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// fault is one scripted answer of a stand-in vendor.
type fault struct {
	status  int               // HTTP status; 0 with a zero kind: a normal answer
	body    string            // the error body
	headers map[string]string // extra response headers
	kind    string            // "reset": the connection is reset before any byte; "eof": closed before any byte;
	// "midstream": a 200 stream that sends its first event and then an error event;
	// "errevent": a 200 stream whose first event is an error event (body)
}

// standIn is a stand-in vendor: it answers its scripted faults in order, then normal answers.
type standIn struct {
	name   string
	openai bool // OpenAI chat-completions shape (the OpenRouter stand-in); else Anthropic messages
	srv    *httptest.Server

	mu       sync.Mutex
	script   []fault
	calls    int
	gate     chan struct{} // when set, each call waits until it is closed before answering
	received []string      // each request as received: method, path and body
}

func newStandIn(t *testing.T, name string, openai bool, script ...fault) *standIn {
	t.Helper()
	s := &standIn{name: name, openai: openai, script: script}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *standIn) setScript(script ...fault) {
	s.mu.Lock()
	s.script = script
	s.mu.Unlock()
}

func (s *standIn) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *standIn) setGate(gate chan struct{}) {
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
}

func (s *standIn) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *standIn) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	s.received = append(s.received, r.Method+" "+r.URL.String()+"\n"+string(body))
	var f fault
	if len(s.script) > 0 {
		f = s.script[0]
		s.script = s.script[1:]
	}
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	stream := strings.Contains(string(body), `"stream":true`)
	switch f.kind {
	case "reset", "eof":
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			panic("stand-in: no hijacker")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			panic(err)
		}
		if tcp, okTCP := conn.(*net.TCPConn); okTCP && f.kind == "reset" {
			_ = tcp.SetLinger(0) // close with RST: "connection reset by peer"
		}
		_ = conn.Close()
		return
	case "midstream", "errevent":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f.kind == "midstream" {
			_, _ = io.WriteString(w, s.claudeStreamHead())
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial from "+s.name+"\"}}\n\n")
		}
		_, _ = io.WriteString(w, "event: error\ndata: "+f.body+"\n\n")
		return
	}
	if f.status != 0 {
		for k, v := range f.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
		return
	}
	text := "answer from " + s.name
	if s.openai {
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"`+testModel+`","choices":[{"index":0,"delta":{"role":"assistant","content":"`+text+`"},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"`+testModel+`","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","created":1,"model":"`+testModel+`","choices":[{"index":0,"message":{"role":"assistant","content":"`+text+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, s.claudeStreamHead())
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\""+text+"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+testModel+`","content":[{"type":"text","text":"`+text+`"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (s *standIn) claudeStreamHead() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + testModel + "\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
}

// openRouterKey is the provider key upstream gives an openai-compatibility entry named openrouter.
var openRouterKey = util.OpenAICompatibleProviderKey("openrouter")

// testModel is a model name no vendor ships, served by every account of the pool.
const testModel = "claude-fixture-5-2"

// The bodies of the owner's report, and of the other faults the tests use.
const (
	bodyOverloaded    = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	bodyWeeklyQuota   = `{"type":"error","error":{"type":"rate_limit_error","message":"weekly usage quota exhausted"}}`
	bodyCreditTooLow  = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`
	bodyAPIError      = `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`
	bodyBadRequest    = `{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`
	bodyUnauthorized  = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	bodyForbidden     = `{"type":"error","error":{"type":"permission_error","message":"Your API key does not have permission to use the specified resource."}}`
	bodyRateLimit     = `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`
	bodyNotFound      = `{"type":"error","error":{"type":"not_found_error","message":"model: ` + testModel + `"}}`
	bodyPayment       = `{"type":"error","error":{"type":"billing_error","message":"payment required"}}`
	bodyGatewayPlain  = `<html><body>502 Bad Gateway</body></html>`
	bodyUnavailable   = `{"type":"error","error":{"type":"api_error","message":"Service Unavailable"}}`
	bodyGatewayTimout = `<html><body>504 Gateway Time-out</body></html>`
)

// pool is the owner's five accounts for one model, by priority, in front of their stand-ins.
type pool struct {
	t        *testing.T
	manager  *coreauth.Manager
	router   *gin.Engine
	vendors  map[string]*standIn
	order    []string
	affinity *coreauth.SessionAffinitySelector
	model    string
}

type poolOptions struct {
	affinity bool
	rebindOn string
	rule     *bool  // routing.retry.same-account-retry; nil: not set (off in a test binary)
	trace    bool   // the server's answered-by middleware in front of the handler
	model    string // the model the pool serves and the client asks for; "" is testModel
}

var (
	ruleOn  = func() *bool { v := true; return &v }()
	ruleOff = func() *bool { v := false; return &v }()
)

// newPool builds the pool: the Claude login (priority 50), the Devin login stand-in (40; served here by a
// Claude-shaped stand-in that answers as Devin did in the report), two Claude API keys (30, 20) and an
// OpenRouter key (10, OpenAI shape). Retry settings are config.example.yaml's: request-retry 3,
// max-retry-interval 30 s.
func newPool(t *testing.T, opts poolOptions) *pool {
	t.Helper()
	gin.SetMode(gin.TestMode)
	isolateHome(t)
	loopbackVendors(t)
	cfg := &config.Config{}
	cfg.RequestRetry = 3
	cfg.MaxRetryInterval = 30
	cfg.SameAccountRetry = opts.rule
	var selector coreauth.Selector = &coreauth.RoundRobinSelector{}
	var affinity *coreauth.SessionAffinitySelector
	if opts.affinity {
		affinity = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback: &coreauth.RoundRobinSelector{},
			TTL:      time.Hour,
			RebindOn: opts.rebindOn,
		})
		t.Cleanup(affinity.Stop)
		selector = affinity
	}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(openRouterKey, cfg))

	p := &pool{t: t, manager: manager, vendors: map[string]*standIn{}, affinity: affinity, model: opts.model}
	if p.model == "" {
		p.model = testModel
	}
	type account struct {
		id, provider string
		priority     int
		oauth        bool
		openai       bool
	}
	accounts := []account{
		{"login", "claude", 50, true, false},
		{"devin", "claude", 40, false, false},
		{"key1", "claude", 30, false, false},
		{"key2", "claude", 20, false, false},
		{"openrouter", openRouterKey, 10, false, true},
	}
	suffix := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	for _, a := range accounts {
		vendor := newStandIn(t, a.id, a.openai)
		p.vendors[a.id] = vendor
		p.order = append(p.order, a.id)
		auth := &coreauth.Auth{
			ID:       a.id + "-" + suffix,
			Provider: a.provider,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url": vendor.srv.URL,
				"priority": fmt.Sprint(a.priority),
			},
		}
		switch {
		case a.oauth:
			auth.Metadata = map[string]any{
				"access_token":      "synthetic-oauth-token",
				"expired":           time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"type":              "claude",
				"email":             "login@fixture.example.test",
				"account_uuid":      "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
				"organization_uuid": "0f0e0d0c-0b0a-4909-8807-060504030201",
			}
		case a.openai:
			auth.Attributes["api_key"] = "synthetic-openrouter-key"
			auth.Attributes["base_url"] = vendor.srv.URL + "/v1"
			auth.Attributes["compat_name"] = "openrouter"
			auth.Attributes["provider_key"] = openRouterKey
			auth.Label = "openrouter"
		default:
			auth.Attributes["api_key"] = "synthetic-key-" + a.id
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s): %v", a.id, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: p.model}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	p.router = gin.New()
	if opts.trace {
		p.router.Use(logging.CPATraceIDMiddleware())
	}
	p.router.POST("/v1/messages", claudehandlers.NewClaudeCodeAPIHandler(base).ClaudeMessages)
	p.manager = manager
	return p
}

func (p *pool) authID(name string) string {
	return name + "-" + strings.NewReplacer("/", "-", " ", "-").Replace(p.t.Name())
}

// send sends one Claude messages request of the conversation session (stream or not) and returns the
// status and the body the client got.
func (p *pool) send(session string, stream bool) (int, string) {
	p.t.Helper()
	rec := p.sendRecorded(session, stream)
	return rec.Code, rec.Body.String()
}

// sendRecorded is send, returning the whole recorded answer (status, headers, body).
func (p *pool) sendRecorded(session string, stream bool) *httptest.ResponseRecorder {
	body := `{"model":"` + p.model + `","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`
	if stream {
		body = `{"model":"` + p.model + `","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Session-ID", session)
	}
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	return rec
}

// calls returns each stand-in's call count, in priority order.
func (p *pool) calls() map[string]int {
	out := map[string]int{}
	for _, name := range p.order {
		out[name] = p.vendors[name].callCount()
	}
	return out
}

// answeredBy names the stand-in whose answer the body carries ("" for none).
func answeredBy(body string) string {
	for _, name := range []string{"login", "devin", "key1", "key2", "openrouter"} {
		if strings.Contains(body, "answer from "+name) {
			return name
		}
	}
	return ""
}

// cooldown returns how long the account is blocked for the model from now (0: not blocked), read from
// the manager's state as upstream records it.
func (p *pool) cooldown(name string) time.Duration {
	p.t.Helper()
	auth, ok := p.manager.GetByID(p.authID(name))
	if !ok {
		p.t.Fatalf("no auth %s", name)
	}
	now := time.Now()
	var until time.Time
	consider := func(at time.Time) {
		if at.After(until) {
			until = at
		}
	}
	if auth.Unavailable || auth.Quota.Exceeded {
		consider(auth.NextRetryAfter)
		consider(auth.Quota.NextRecoverAt)
	}
	for _, state := range auth.ModelStates {
		if state == nil || !state.Unavailable {
			continue
		}
		consider(state.NextRetryAfter)
		consider(state.Quota.NextRecoverAt)
	}
	if !until.After(now) {
		return 0
	}
	return until.Sub(now)
}
