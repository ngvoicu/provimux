package pluginhost

// A plugin response translator or normalizer (Capabilities.ResponseTranslator, ResponseBeforeTranslator,
// ResponseAfterTranslator) runs inside the attempt of a built-in executor and returns the attempt's bytes. The response
// it is handed names nobody, whatever it returns (DESIGN.md 12.1 row E and row 12, 12.4 test 4). The real Claude
// executor answers from a canned transport; the production plugin host is the translator's hook, as the service
// installs it.
//
// Modes:
//
//	none             the plugin host is the hook, no plugin has the capability (the owner's setup)
//	observe          the plugin returns the body as given
//	edit             it changes the body
//	replace-nested   it makes a host model call for the login and returns that answer
//
// Plain and SSE, on the stock Messages route and on the stock chat-completions route. A Messages stream from a Claude
// upstream is not translated at all (the executor forwards it), so no plugin is handed it and it stays named. A plugin
// ResponseTranslator is handed only a pair without a native transform: here Claude to Claude, the Messages route plain.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	a "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const translatorBytesModel = "claude-opus-5-5"

// translatorBytesUpstream is the Claude key's upstream: a canned transport, so the real executor's exchange happens
// without a network.
type translatorBytesUpstream struct {
	mu    *sync.Mutex
	hosts *[]string
}

func (x translatorBytesUpstream) RoundTripperFor(*a.Auth) http.RoundTripper { return x }
func (x translatorBytesUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	x.mu.Lock()
	*x.hosts = append(*x.hosts, req.URL.Hostname())
	x.mu.Unlock()
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	var q struct{ Stream bool }
	if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
		return nil, err
	}
	typ, body := "application/json", `{"id":"served-by-upstream","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"served-by-upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`
	if q.Stream {
		typ = "text/event-stream"
		body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"served-by-upstream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"served-by-upstream\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {typ}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

// The response of a built-in executor's attempt that is handed to a plugin response translator or normalizer names
// nobody, whatever the plugin returns; one no plugin is handed keeps the credential's name. (DESIGN.md 12.4, test 4.)
func TestResponseHandedToAPluginTranslatorNamesNobody(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	claudeKey := logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "upstream.example.test"}
	type capability struct {
		name string
		// handed reports whether the plugin is handed the response on a route.
		handed func(route string, stream bool) bool
	}
	normalizerHanded := func(route string, stream bool) bool { return !(route == "messages" && stream) }
	capabilities := []capability{
		{name: "ResponseAfterTranslator", handed: normalizerHanded},
		{name: "ResponseBeforeTranslator", handed: normalizerHanded},
		{name: "ResponseTranslator", handed: func(route string, stream bool) bool { return route == "messages" && !stream }},
	}
	type cell struct {
		capability capability
		mode       string
	}
	cells := []cell{{capability: capability{name: "no plugin", handed: func(string, bool) bool { return false }}, mode: "none"}}
	for _, c := range capabilities {
		for _, mode := range []string{"observe", "edit", "replace-nested"} {
			cells = append(cells, cell{capability: c, mode: mode})
		}
	}
	for _, c := range cells {
		for _, route := range []string{"messages", "chat"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s %s %s stream=%t", c.capability.name, c.mode, route, stream), func(t *testing.T) {
					mu := &sync.Mutex{}
					attempts := []string{}
					hosts := []string{}
					m := a.NewManager(nil, nil, nil)
					m.SetRetryConfig(0, 0, 3)
					m.SetRoundTripperProvider(translatorBytesUpstream{mu: mu, hosts: &hosts})
					m.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
					m.RegisterExecutor(&providerBytesLogin{mu: mu, attempts: &attempts})
					base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m)
					for _, cr := range []*a.Auth{
						{ID: "claude-key", Provider: "claude", Status: a.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fictional-key", "base_url": "https://upstream.example.test/v1", "priority": "10"}},
						{ID: "login", Provider: "devin", Status: a.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": "0"}},
					} {
						if _, err := m.Register(context.Background(), cr); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(cr.ID, cr.Provider, []*registry.ModelInfo{{ID: translatorBytesModel, OwnedBy: "anthropic", Created: 1700000000}})
						id := cr.ID
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					}
					var calls atomic.Int32
					transform := func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
						calls.Add(1)
						if !bytes.Contains(req.Body, []byte("served-by-upstream")) {
							return pluginapi.PayloadResponse{Body: req.Body}, nil
						}
						switch c.mode {
						case "edit":
							return pluginapi.PayloadResponse{Body: bytes.ReplaceAll(req.Body, []byte("served-by-upstream"), []byte("edited-by-plugin"))}, nil
						case "replace-nested":
							r, e := base.ExecuteModel(ctx, handlers.ModelExecutionRequest{EntryProtocol: "claude", Model: translatorBytesModel, AuthID: "login", Body: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"fixture"}],"max_tokens":8}`, translatorBytesModel))})
							if e != nil {
								return pluginapi.PayloadResponse{}, e.Error
							}
							if !bytes.Contains(r.Body, []byte("served-by-login")) {
								return pluginapi.PayloadResponse{}, fmt.Errorf("fixture: nested body %s", r.Body)
							}
							return pluginapi.PayloadResponse{Body: bytes.ReplaceAll(req.Body, []byte("served-by-upstream"), []byte("served-by-login"))}, nil
						}
						return pluginapi.PayloadResponse{Body: req.Body}, nil
					}
					var caps pluginapi.Capabilities
					switch c.capability.name {
					case "ResponseAfterTranslator":
						caps.ResponseAfterTranslator = responseNormalizerFunc(transform)
					case "ResponseBeforeTranslator":
						caps.ResponseBeforeTranslator = responseNormalizerFunc(transform)
					case "ResponseTranslator":
						caps.ResponseTranslator = responseTranslatorFunc(transform)
					}
					host := newHostWithRecords()
					if c.mode != "none" {
						host = newHostWithRecords(capabilityRecord{id: "translator-bytes", priority: 10, plugin: pluginapi.Plugin{Capabilities: caps}})
					}
					base.SetPluginHost(host)
					sdktranslator.SetPluginHooks(host)
					t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
					engine := gin.New()
					engine.Use(logging.CPATraceIDMiddleware())
					engine.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
					engine.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(base).ChatCompletions)
					path := map[string]string{"messages": "/v1/messages", "chat": "/v1/chat/completions"}[route]
					rr := httptest.NewRecorder()
					engine.ServeHTTP(rr, httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":"x"}],"stream":%t}`, translatorBytesModel, stream))))
					hdr := rr.Result().Header
					got := logging.AuthStatement{Kind: hdr.Get(logging.CPAAuthKindHeader), Provider: hdr.Get(logging.CPAAuthProviderHeader), Host: hdr.Get(logging.CPAAuthHostHeader)}
					body := rr.Body.String()
					handed := calls.Load() > 0
					marker := "served-by-upstream"
					if handed {
						marker = map[string]string{"observe": "served-by-upstream", "edit": "edited-by-plugin", "replace-nested": "served-by-login"}[c.mode]
					}
					mu.Lock()
					upstreamHosts, nestedAttempts := fmt.Sprint(hosts), fmt.Sprint(attempts)
					mu.Unlock()
					if rr.Code != http.StatusOK || !strings.Contains(body, marker) || handed != c.capability.handed(route, stream) || upstreamHosts != "[upstream.example.test]" {
						t.Fatalf("fixture: status %d, handed %v (want %v), upstream %s, nested %s: %s", rr.Code, handed, c.capability.handed(route, stream), upstreamHosts, nestedAttempts, body)
					}
					want := logging.AuthStatement{}
					if !handed {
						want = claudeKey
					}
					if got != want {
						t.Fatalf("body %s: headers name %+v, want %+v", marker, got, want)
					}
				})
			}
		}
	}
}
