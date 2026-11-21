package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pluginBytesWebsocketExecutor answers a turn with a Responses stream whose response.created carries the credential.
// With lead, a response.in_progress chunk comes first.
type pluginBytesWebsocketExecutor struct {
	websocketCreatedExecutor
	provider string
	lead     bool
}

func (e pluginBytesWebsocketExecutor) Identifier() string { return e.provider }

func (e pluginBytesWebsocketExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	events := []string{"response.created", "response.completed"}
	if e.lead {
		events = append([]string{"response.in_progress"}, events...)
	}
	chunks := make(chan coreexecutor.StreamChunk, len(events))
	for _, eventType := range events {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"response\":{\"id\":%q,\"output\":[]}}\n\n", eventType, eventType, "resp-"+auth.ID))}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

type pluginBytesWebsocketInner struct{}

// pluginBytesWebsocketHost is plugin code on a websocket turn. side-call: a stream-chunk interceptor makes a nested
// call and keeps the outer stream. replace: it puts the nested answer in place of the outer one. delegate: a routed
// executor forwards the nested answer. drop-first: the interceptor drops the outer stream's first chunk (a
// response.in_progress before response.created). With
// withHeaders the plugin returns the nested answer's headers with its bytes, an X-CPA-ANSWER of its own and forged
// X-CPA-AUTH-* values naming the nested credential.
type pluginBytesWebsocketHost struct {
	h           *handlers.BaseAPIHandler
	mode        string
	withHeaders bool
	mu          sync.Mutex
	watched     int
	nested      []byte
}

func (p *pluginBytesWebsocketHost) inner(ctx context.Context, body []byte) ([]byte, http.Header) {
	ctx = context.WithValue(ctx, pluginBytesWebsocketInner{}, true)
	res, errMsg := p.h.ExecuteModelStream(ctx, handlers.ModelExecutionRequest{EntryProtocol: "openai-response", Model: pluginBytesWebsocketModel, AuthID: "ws-inner", Body: body, Stream: true, SkipRouterPluginID: "ws-plugin", SkipInterceptorPluginID: "ws-plugin"})
	if errMsg != nil {
		panic(errMsg.Error)
	}
	var b bytes.Buffer
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			panic(chunk.Err)
		}
		b.Write(chunk.Payload)
	}
	p.mu.Lock()
	p.nested = b.Bytes()
	p.mu.Unlock()
	if !p.withHeaders {
		return b.Bytes(), nil
	}
	headers := res.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("X-CPA-ANSWER", "made-up-reference")
	headers.Set(logging.CPAAuthKindHeader, "oauth")
	headers.Set(logging.CPAAuthProviderHeader, "devin")
	return b.Bytes(), headers
}

func (p *pluginBytesWebsocketHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (p *pluginBytesWebsocketHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (p *pluginBytesWebsocketHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func (p *pluginBytesWebsocketHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	if ctx.Value(pluginBytesWebsocketInner{}) != nil || p.mode == "delegate" || req.ChunkIndex < 0 {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	switch p.mode {
	case "drop-first":
		return pluginapi.StreamChunkInterceptResponse{DropChunk: req.ChunkIndex == 0}
	case "watch", "none":
		p.mu.Lock()
		p.watched++
		p.mu.Unlock()
		return pluginapi.StreamChunkInterceptResponse{Body: req.Body}
	}
	if req.ChunkIndex == 0 {
		body, headers := p.inner(ctx, req.RequestBody)
		if p.mode == "replace" {
			return pluginapi.StreamChunkInterceptResponse{Body: body, Headers: headers}
		}
	} else if p.mode == "replace" {
		return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
	}
	return pluginapi.StreamChunkInterceptResponse{}
}

func (p *pluginBytesWebsocketHost) HasModelRouters() bool { return p.mode == "delegate" }

// HasStreamInterceptors reports whether the plugin's stream-chunk interceptor is in the turn's path: not for a routed
// executor's turn, and not in mode none, where the plugin has no such capability.
func (p *pluginBytesWebsocketHost) HasStreamInterceptors() bool {
	return p.mode != "delegate" && p.mode != "none"
}

func (p *pluginBytesWebsocketHost) RouteModel(ctx context.Context, _ pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	if p.mode != "delegate" || ctx.Value(pluginBytesWebsocketInner{}) != nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "ws-plugin"}, true
}

func (p *pluginBytesWebsocketHost) ExecutePluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unused")
}

func (p *pluginBytesWebsocketHost) ExecutePluginExecutorStream(ctx context.Context, _ string, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	body, headers := p.inner(ctx, req.Payload)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: body}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks, Headers: headers}, nil
}

func (p *pluginBytesWebsocketHost) CountPluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unused")
}

const pluginBytesWebsocketModel = "plugin-bytes-websocket-model"

// On a websocket turn, response.created names the credential only when the proxy carried the turn's bytes and no
// stream-chunk interceptor was in their path: a turn that passes through one names nobody whatever it does (it watches,
// makes a nested call it keeps to itself, replaces the answer, drops the first chunk; after the third review), and so
// does a routed executor's answer, whatever headers the plugin returns. (DESIGN.md 12.4, test 2.)
func TestWebsocketTurnNamesNobodyForBytesFromPluginCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	outer := logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "outer-gateway.example.test"}
	for _, cell := range []struct {
		mode        string
		withHeaders bool
		created     string // the response.created frame's id
		want        logging.AuthStatement
	}{
		{mode: "none", created: "resp-ws-outer", want: outer},
		{mode: "watch", created: "resp-ws-outer"},
		{mode: "side-call", created: "resp-ws-outer"},
		{mode: "replace", created: "resp-ws-inner"},
		{mode: "replace", withHeaders: true, created: "resp-ws-inner"},
		{mode: "delegate", created: "resp-ws-inner"},
		{mode: "delegate", withHeaders: true, created: "resp-ws-inner"},
		{mode: "drop-first", created: "resp-ws-outer"},
	} {
		name := cell.mode
		if cell.withHeaders {
			name += " with the nested answer's headers and forged ones"
		}
		t.Run(name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			for _, credential := range []*coreauth.Auth{
				{ID: "ws-outer", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "made-up", "base_url": "https://outer-gateway.example.test/v1", "priority": "10"}},
				{ID: "ws-inner", Provider: "devin", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": "0"}},
			} {
				manager.RegisterExecutor(pluginBytesWebsocketExecutor{provider: credential.Provider, lead: cell.mode == "drop-first"})
				if _, err := manager.Register(context.Background(), credential); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: pluginBytesWebsocketModel}})
				id := credential.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			plugin := &pluginBytesWebsocketHost{h: base, mode: cell.mode, withHeaders: cell.withHeaders}
			base.SetPluginHost(plugin)
			base.SetModelRouterHost(plugin)
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.GET("/v1/responses", NewOpenAIResponsesAPIHandler(base).ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()
			conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			_ = response
			if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"hi"}]}`, pluginBytesWebsocketModel))); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
			var created []byte
			for done := false; !done; {
				_, frame, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("read: %v", errRead)
				}
				fields := logging.AuthStatement{Kind: ojson.GetBytesNoCopy(frame, "cpa_auth_kind").String(), Provider: ojson.GetBytesNoCopy(frame, "cpa_auth_provider").String(), Host: ojson.GetBytesNoCopy(frame, "cpa_auth_host").String()}
				switch eventType := ojson.GetBytesNoCopy(frame, "type").String(); eventType {
				case "response.created":
					created = frame
					if !strings.Contains(string(frame), cell.created) || fields != cell.want {
						t.Fatalf("response.created %s carries %+v, want %s with %+v", frame, fields, cell.created, cell.want)
					}
				case "error":
					t.Fatalf("error frame: %s", frame)
				case "response.completed":
					done = true
					fallthrough
				default:
					if fields != (logging.AuthStatement{}) {
						t.Fatalf("%s frame carries %+v: %s", eventType, fields, frame)
					}
				}
			}
			plugin.mu.Lock()
			nested := string(plugin.nested)
			plugin.mu.Unlock()
			plugin.mu.Lock()
			watched := plugin.watched
			plugin.mu.Unlock()
			if (cell.mode == "watch") != (watched > 0) {
				t.Fatalf("fixture: the interceptor watched %d chunks in mode %s", watched, cell.mode)
			}
			if cell.mode == "drop-first" || cell.mode == "watch" || cell.mode == "none" {
				if created == nil || nested != "" {
					t.Fatalf("fixture: created %q, nested %q, want no nested call", created, nested)
				}
				return
			}
			if created == nil || !strings.Contains(nested, "resp-ws-inner") {
				t.Fatalf("fixture: created %q, nested %q", created, nested)
			}
		})
	}
}
