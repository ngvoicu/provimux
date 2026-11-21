package pluginhost

// A provider plugin is an executor the conductor runs with a credential of its own; it reaches its upstream through the
// host's HTTP bridge. Everything it returns came out of plugin code, so its answers name nobody, also when it returns
// its own upstream's body faithfully (DESIGN.md 12.1 rows 7-10, 12.4 test 3). The plugin stands at the production
// adapter (executorAdapter) and the stock Messages route; its nested model call goes through
// BaseAPIHandler.ExecuteModel with the context the adapter gave it.
//
// Modes:
//
//	own-exchange          bridge 200, the plugin returns that body
//	bridge-501-local      bridge 501, the plugin returns bytes of its own (the first review's "1c")
//	bridge-501-nested     bridge 501, then one nested call to the login; the plugin returns the nested body
//	bridge-501-nested+h   the same, and it returns the nested answer's headers with the body
//	nested-only           no bridge exchange; one nested call; the plugin returns the nested body
//	bridge-200-sidecall   bridge 200, one nested call whose answer it discards; it returns the bridge body
//	nested-after-open     streamed only: the plugin opens its stream first, then bridge 501, then one nested call,
//	                      and sends the nested body as its first chunk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	a "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const providerBytesModel = "provider-bytes-model"

func providerBytesBody(by string) string {
	return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"served-by-%s"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, providerBytesModel, by)
}

func providerBytesEvents(by string) []string {
	return []string{
		"event: message_start\ndata: " + fmt.Sprintf(`{"type":"message_start","message":{"id":"served-by-%s","model":%q,"type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`, by, providerBytesModel) + "\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
}

// providerBytesLogin is a built-in executor for the login: its response comes from an upstream exchange. With fail,
// the exchange fails.
type providerBytesLogin struct {
	mu       *sync.Mutex
	attempts *[]string
	fail     bool
}

func (e *providerBytesLogin) Identifier() string { return "devin" }
func (e *providerBytesLogin) note(ctx context.Context, cr *a.Auth) error {
	e.mu.Lock()
	*e.attempts = append(*e.attempts, cr.ID)
	e.mu.Unlock()
	ex.MarkUpstreamAttempt(ctx)
	if e.fail {
		return &a.Error{HTTPStatus: http.StatusServiceUnavailable, Message: "fixture: the built-in upstream failed"}
	}
	return nil
}
func (e *providerBytesLogin) Execute(ctx context.Context, cr *a.Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
	if err := e.note(ctx, cr); err != nil {
		return ex.Response{}, err
	}
	return ex.Response{Payload: []byte(providerBytesBody(cr.ID))}, nil
}
func (e *providerBytesLogin) ExecuteStream(ctx context.Context, cr *a.Auth, _ ex.Request, _ ex.Options) (*ex.StreamResult, error) {
	if err := e.note(ctx, cr); err != nil {
		return nil, err
	}
	ch := make(chan ex.StreamChunk, 2)
	for _, event := range providerBytesEvents(cr.ID) {
		ch <- ex.StreamChunk{Payload: []byte(event)}
	}
	close(ch)
	return &ex.StreamResult{Chunks: ch}, nil
}
func (e *providerBytesLogin) CountTokens(context.Context, *a.Auth, ex.Request, ex.Options) (ex.Response, error) {
	return ex.Response{}, nil
}
func (e *providerBytesLogin) Refresh(_ context.Context, cr *a.Auth) (*a.Auth, error) { return cr, nil }
func (e *providerBytesLogin) HttpRequest(context.Context, *a.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type providerBytesRun struct {
	t        *testing.T
	base     *handlers.BaseAPIHandler
	engine   *gin.Engine
	mu       *sync.Mutex
	attempts *[]string
	upstream string
}

// providerBytesSetup registers the provider plugin's key (plugin-key, served by plugin through the production adapter,
// its upstream answering with upstream) and the built-in login, with the priorities given.
func providerBytesSetup(t *testing.T, plugin *fakeExecutor, upstream http.HandlerFunc, login *providerBytesLogin, keyPriority, loginPriority string) *providerBytesRun {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	run := &providerBytesRun{t: t, mu: login.mu, attempts: login.attempts}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run.mu.Lock()
		*run.attempts = append(*run.attempts, "plugin-key:bridge")
		run.mu.Unlock()
		upstream(w, r)
	}))
	t.Cleanup(up.Close)
	run.upstream = up.URL

	m := a.NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 3)
	run.base = handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m)
	m.RegisterExecutor(newCurrentExecutorAdapterForTest(New(), "provider-bytes", plugin, []tr.Format{tr.FormatClaude}, []tr.Format{tr.FormatClaude}))
	m.RegisterExecutor(login)

	pluginKey := &a.Auth{ID: "plugin-key", Provider: "plugin-provider", Status: a.StatusActive, Attributes: map[string]string{"api_key": "made-up", "base_url": up.URL, "priority": keyPriority}}
	loginAuth := &a.Auth{ID: "login", Provider: "devin", Status: a.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": loginPriority}}
	reg := registry.GetGlobalRegistry()
	for _, cr := range []*a.Auth{pluginKey, loginAuth} {
		if _, err := m.Register(context.Background(), cr); err != nil {
			t.Fatal(err)
		}
		reg.RegisterClient(cr.ID, cr.Provider, []*registry.ModelInfo{{ID: providerBytesModel, OwnedBy: "anthropic"}})
		id := cr.ID
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	run.engine = gin.New()
	run.engine.Use(logging.CPATraceIDMiddleware())
	claudeHandler := claude.NewClaudeCodeAPIHandler(run.base)
	run.engine.POST("/v1/messages", claudeHandler.ClaudeMessages)
	run.engine.POST("/v1/messages/count_tokens", claudeHandler.ClaudeCountTokens)
	return run
}

func (r *providerBytesRun) serve(path string, stream bool) (int, string, logging.AuthStatement, string) {
	rr := httptest.NewRecorder()
	r.engine.ServeHTTP(rr, httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"model":%q,"max_tokens":8,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, providerBytesModel, stream))))
	h := rr.Result().Header
	got := logging.AuthStatement{Kind: h.Get(logging.CPAAuthKindHeader), Provider: h.Get(logging.CPAAuthProviderHeader), Host: h.Get(logging.CPAAuthHostHeader)}
	r.mu.Lock()
	calls := fmt.Sprint(*r.attempts)
	r.mu.Unlock()
	return rr.Code, rr.Body.String(), got, calls
}

func servedBy(body string, who ...string) string {
	by := "?"
	for _, w := range who {
		if strings.Contains(body, "served-by-"+w) {
			by = w
		}
	}
	return by
}

// A provider plugin's answer names nobody, in every mode, plain, SSE and count; the mark is the attempt's own, so a
// built-in credential that answers after a provider plugin's failed attempt is named, and a provider plugin that
// answers after a built-in credential's failed attempt is not. (DESIGN.md 12.4, test 3.)
func TestProviderPluginNamesNobody(t *testing.T) {
	nobody := logging.AuthStatement{}
	for _, mode := range []string{"own-exchange", "bridge-501-local", "bridge-501-nested", "bridge-501-nested+h", "nested-only", "bridge-200-sidecall", "nested-after-open"} {
		for _, stream := range []bool{false, true} {
			if mode == "nested-after-open" && !stream {
				continue
			}
			t.Run(fmt.Sprintf("%s stream=%t", mode, stream), func(t *testing.T) {
				var run *providerBytesRun
				nested := func(ctx context.Context, payload []byte) ([]byte, http.Header) {
					req := handlers.ModelExecutionRequest{EntryProtocol: "claude", Model: providerBytesModel, AuthID: "login", Body: payload, Stream: stream, SkipRouterPluginID: "provider-bytes", SkipInterceptorPluginID: "provider-bytes"}
					if !stream {
						res, e := run.base.ExecuteModel(ctx, req)
						if e != nil {
							panic(e.Error)
						}
						return res.Body, res.Headers
					}
					res, e := run.base.ExecuteModelStream(ctx, req)
					if e != nil {
						panic(e.Error)
					}
					var b strings.Builder
					for ch := range res.Chunks {
						if ch.Err != nil {
							panic(ch.Err)
						}
						b.Write(ch.Payload)
					}
					return []byte(b.String()), res.Headers
				}
				// do is the plugin: what it does with its own upstream and with the host's models, and what it returns.
				do := func(ctx context.Context, req pluginapi.ExecutorRequest) ([]byte, http.Header, error) {
					own := providerBytesBody("plugin-key")
					made := providerBytesBody("nobody-made-by-the-plugin")
					if stream {
						own = strings.Join(providerBytesEvents("plugin-key"), "")
						made = strings.Join(providerBytesEvents("nobody-made-by-the-plugin"), "")
					}
					if mode != "nested-only" {
						res, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: "POST", URL: run.upstream + "/v1/messages", Body: req.Payload})
						if err != nil {
							return nil, nil, err
						}
						if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNotImplemented {
							return nil, nil, fmt.Errorf("unexpected %d", res.StatusCode)
						}
					}
					switch mode {
					case "own-exchange":
						return []byte(own), nil, nil
					case "bridge-501-local":
						return []byte(made), nil, nil
					case "bridge-200-sidecall":
						nested(ctx, req.Payload)
						return []byte(own), nil, nil
					case "bridge-501-nested+h":
						b, h := nested(ctx, req.Payload)
						return b, h, nil
					}
					b, _ := nested(ctx, req.Payload)
					return b, nil, nil
				}
				plugin := &fakeExecutor{identifier: "plugin-provider",
					execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						b, h, err := do(ctx, req)
						return pluginapi.ExecutorResponse{Payload: b, Headers: h}, err
					},
					executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						if mode == "nested-after-open" {
							ch := make(chan pluginapi.ExecutorStreamChunk, 1)
							go func() {
								defer close(ch)
								b, _, err := do(ctx, req)
								ch <- pluginapi.ExecutorStreamChunk{Payload: b, Err: err}
							}()
							return pluginapi.ExecutorStreamResponse{Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Chunks: ch}, nil
						}
						b, h, err := do(ctx, req)
						if err != nil {
							return pluginapi.ExecutorStreamResponse{}, err
						}
						ch := make(chan pluginapi.ExecutorStreamChunk, 1)
						ch <- pluginapi.ExecutorStreamChunk{Payload: b}
						close(ch)
						return pluginapi.ExecutorStreamResponse{Headers: h, Chunks: ch}, nil
					},
				}
				upstream := func(w http.ResponseWriter, _ *http.Request) {
					if mode == "own-exchange" || mode == "bridge-200-sidecall" {
						fmt.Fprint(w, providerBytesBody("plugin-key"))
						return
					}
					http.Error(w, "fixture upstream unsupported", http.StatusNotImplemented)
				}
				mu := &sync.Mutex{}
				attempts := []string{}
				run = providerBytesSetup(t, plugin, upstream, &providerBytesLogin{mu: mu, attempts: &attempts}, "10", "0")
				code, body, got, calls := run.serve("/v1/messages", stream)
				by := servedBy(body, "plugin-key", "login", "nobody-made-by-the-plugin")
				wantBy := map[string]string{
					"own-exchange":        "plugin-key",
					"bridge-501-local":    "nobody-made-by-the-plugin",
					"bridge-501-nested":   "login",
					"bridge-501-nested+h": "login",
					"nested-only":         "login",
					"bridge-200-sidecall": "plugin-key",
					"nested-after-open":   "login",
				}[mode]
				if code != http.StatusOK || by != wantBy {
					t.Fatalf("fixture: status %d, body served by %s, want %s (attempts %s): %q", code, by, wantBy, calls, body)
				}
				if got != nobody {
					t.Fatalf("body served by %s: headers name %+v, want nobody (attempts %s)", by, got, calls)
				}
			})
		}
	}

	// The count route: the plugin counts through its own upstream, or estimates the count itself after its upstream
	// answered 501 (the first review's "1c").
	for _, mode := range []string{"own-exchange", "bridge-501-local"} {
		t.Run("count "+mode, func(t *testing.T) {
			var run *providerBytesRun
			plugin := &fakeExecutor{identifier: "plugin-provider",
				countTokens: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					res, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: "POST", URL: run.upstream + "/v1/messages/count_tokens", Body: req.Payload})
					if err != nil {
						return pluginapi.ExecutorResponse{}, err
					}
					if res.StatusCode == http.StatusOK {
						return pluginapi.ExecutorResponse{Payload: res.Body}, nil
					}
					return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":3,"note":"served-by-nobody-made-by-the-plugin"}`)}, nil
				},
			}
			upstream := func(w http.ResponseWriter, _ *http.Request) {
				if mode == "own-exchange" {
					fmt.Fprint(w, `{"input_tokens":5,"note":"served-by-plugin-key"}`)
					return
				}
				http.Error(w, "fixture upstream unsupported", http.StatusNotImplemented)
			}
			mu := &sync.Mutex{}
			attempts := []string{}
			run = providerBytesSetup(t, plugin, upstream, &providerBytesLogin{mu: mu, attempts: &attempts}, "10", "0")
			code, body, got, calls := run.serve("/v1/messages/count_tokens", false)
			wantBy := map[string]string{"own-exchange": "plugin-key", "bridge-501-local": "nobody-made-by-the-plugin"}[mode]
			if by := servedBy(body, "plugin-key", "nobody-made-by-the-plugin"); code != http.StatusOK || by != wantBy || calls != "[plugin-key:bridge]" {
				t.Fatalf("fixture: status %d, count by %s, want %s (attempts %s): %q", code, by, wantBy, calls, body)
			}
			if got != nobody {
				t.Fatalf("count: headers name %+v, want nobody", got)
			}
		})
	}

	// The mark belongs to one attempt. The first credential tried fails and the conductor goes on to the other, inside
	// one call.
	for _, mode := range []string{"plugin-fails-builtin-answers", "builtin-fails-plugin-answers"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stream=%t", mode, stream), func(t *testing.T) {
				pluginFails := mode == "plugin-fails-builtin-answers"
				var run *providerBytesRun
				do := func(ctx context.Context, req pluginapi.ExecutorRequest) ([]byte, error) {
					res, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: "POST", URL: run.upstream + "/v1/messages", Body: req.Payload})
					if err != nil {
						return nil, err
					}
					if res.StatusCode != http.StatusOK {
						return nil, &a.Error{HTTPStatus: http.StatusServiceUnavailable, Message: "fixture: the plugin's upstream failed"}
					}
					if stream {
						return []byte(strings.Join(providerBytesEvents("plugin-key"), "")), nil
					}
					return []byte(providerBytesBody("plugin-key")), nil
				}
				plugin := &fakeExecutor{identifier: "plugin-provider",
					execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						b, err := do(ctx, req)
						return pluginapi.ExecutorResponse{Payload: b}, err
					},
					executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						b, err := do(ctx, req)
						if err != nil {
							return pluginapi.ExecutorStreamResponse{}, err
						}
						ch := make(chan pluginapi.ExecutorStreamChunk, 1)
						ch <- pluginapi.ExecutorStreamChunk{Payload: b}
						close(ch)
						return pluginapi.ExecutorStreamResponse{Chunks: ch}, nil
					},
				}
				upstream := func(w http.ResponseWriter, _ *http.Request) {
					if pluginFails {
						http.Error(w, "fixture upstream unsupported", http.StatusNotImplemented)
						return
					}
					fmt.Fprint(w, providerBytesBody("plugin-key"))
				}
				mu := &sync.Mutex{}
				attempts := []string{}
				keyPriority, loginPriority := "10", "0"
				if !pluginFails {
					keyPriority, loginPriority = "0", "10"
				}
				run = providerBytesSetup(t, plugin, upstream, &providerBytesLogin{mu: mu, attempts: &attempts, fail: !pluginFails}, keyPriority, loginPriority)
				code, body, got, calls := run.serve("/v1/messages", stream)
				wantCalls, wantBy, want := "[plugin-key:bridge login]", "login", logging.AuthStatement{Kind: "oauth", Provider: "devin"}
				if !pluginFails {
					wantCalls, wantBy, want = "[login plugin-key:bridge]", "plugin-key", nobody
				}
				if by := servedBy(body, "plugin-key", "login"); code != http.StatusOK || calls != wantCalls || by != wantBy {
					t.Fatalf("fixture: status %d, attempts %s, body by %s; want %s by %s: %q", code, calls, by, wantCalls, wantBy, body)
				}
				if got != want {
					t.Fatalf("headers name %+v, want %+v", got, want)
				}
			})
		}
	}
}
