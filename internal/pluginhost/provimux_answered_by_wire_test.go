package pluginhost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Provimux: the answered-by statement on the wire (the owner's patch "state the credential an
// upstream answered with"), as a client reads it. Each request starts with a stale statement
// already offered on its context and all three X-CPA-AUTH-* headers already set, as a previous
// answer on the same connection could leave them. Over one HTTP/1.1 connection, Home grants
// alternate between a credential, a local plugin, the legacy fallback to a credential and a local
// plugin again, for OpenAI chat (plain and SSE) and Gemini generate, SSE and count: the headers
// the first write commits must name the credential that answered, or nothing for a plugin's
// answer. A refused key gets none. On one WebSocket, credential, plugin, credential: each
// response.created names the credential that answered that turn, or carries no field at all.
//
// These are Home grants, and in Home mode no answer names an account (answered-by review): the switch is as a Home
// node has it (off, whatever the config says; TestProvimuxAnsweredByIsOffInHomeMode), the markers' key is set all the
// same, and no answer, stale or upstream value included, carries X-Provimux-* or provimux_*.

var (
	wireAnswered = logging.AuthStatement{Kind: "apikey", Provider: "wire-native", Host: "current.example.test"}
	wireStale    = logging.AuthStatement{Kind: "apikey", Provider: "previous", Host: "previous.example.test", Provimux: logging.ProvimuxAnswer{Account: "a.stalestalestalestale"}}
)

// wireNativeExecutor is the credential's executor: it answers every format from the upstream.
type wireNativeExecutor struct{ coreauth.ProviderExecutor }

func (wireNativeExecutor) Identifier() string { return wireAnswered.Provider }

func (wireNativeExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return coreexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"native answer"},"finish_reason":"stop"}],"candidates":[{"content":{"parts":[{"text":"native answer"}]}}]}`)}, nil
}

func (wireNativeExecutor) CountTokens(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return coreexecutor.Response{Payload: []byte(`{"totalTokens":7}`)}, nil
}

func (wireNativeExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	var frames []string
	switch {
	case string(opts.SourceFormat) == "openai-response" || string(opts.ResponseFormat) == "openai-response":
		// A foreign statement in the upstream's own event must be overwritten, never passed on.
		frames = []string{
			`data: {"type":"response.created","response":{"id":"resp-wire","output":[]},"cpa_auth_kind":"oauth","cpa_auth_provider":"foreign","provimux_account":"a.foreignforeignforeig"}` + "\n\n",
			`data: {"type":"response.completed","response":{"id":"resp-wire","status":"completed","output":[]}}` + "\n\n",
		}
	case string(opts.SourceFormat) == "gemini":
		frames = []string{`{"candidates":[{"content":{"parts":[{"text":"native answer"}]},"finishReason":"STOP"}]}`}
	default:
		frames = []string{`{"choices":[{"delta":{"content":"native answer"},"finish_reason":"stop"}]}`}
	}
	chunks := make(chan coreexecutor.StreamChunk, len(frames))
	for _, frame := range frames {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(frame)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func wireStatement(h http.Header) logging.AuthStatement {
	return logging.AuthStatement{Kind: h.Get(logging.CPAAuthKindHeader), Provider: h.Get(logging.CPAAuthProviderHeader), Host: h.Get(logging.CPAAuthHostHeader)}
}

func TestProvimuxAnsweredByOnTheWire(t *testing.T) {
	const model = "agy/gemini-2.5-pro"
	const pluginID = "alpha"
	const pluginVersion = "1.0.0"
	const pluginYAML = "enabled: true\npriority: 100\n"
	const alias = "acp/" + model
	const key = "acp-test-key"
	configDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(pluginYAML)))

	var viaCredential, viaLegacy atomic.Bool
	answeredAuth := coreauth.Auth{ID: "wire-native-key", Provider: wireAnswered.Provider, Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture", "base_url": "https://current.example.test/v1"}}
	coreauth.SetProvimuxMarkerKey([]byte("provimux-wire-test-marker-key-32"))
	wasAnsweredBy := logging.SetProvimuxAnsweredBy(false) // a Home node's
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(wasAnsweredBy)
		coreauth.SetProvimuxMarkerKey(nil)
	})
	answeredMarker := "" // Home mode: nobody is named by a marker
	credentialReply := homeExecutorDispatchJSON(t, answeredAuth, "")
	client := newHomeExecutorTestClient(t, func(command, argument string) []byte {
		if command == "RPOP" { // the credential dispatch, after a credential grant or the legacy fallback
			return credentialReply
		}
		doc := ojson.NewDocString(argument)
		if doc.Get("type").String() != "local-plugin-dispatch" || doc.Get("model").String() != alias {
			t.Errorf("unexpected Home request: %s %s", command, doc.Get("type").String())
			return nil
		}
		if viaLegacy.Load() {
			return []byte(`{"error":{"type":"error","message":"unsupported type"}}`)
		}
		if doc.Get("headers.authorization").String() != "Bearer "+key {
			return []byte(`{"error":{"type":"model_forbidden","message":"no model access"}}`)
		}
		if viaCredential.Load() {
			return []byte(`{"kind":"credential"}`)
		}
		return []byte(fmt.Sprintf(`{"kind":"local-plugin","plugin":%q,"version":%q,"config_digest":%q,"model":%q,"principal":%q,"access_provider":"config-api-key"}`,
			pluginID, pluginVersion, configDigest, model, key))
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(wireNativeExecutor{})
	manager.PublishHomeDispatch(client, executionregistry.New(), 1)

	const candidate = `{"candidates":[{"content":{"role":"model","parts":[{"text":"local answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8}}`
	attr := &pluginapi.UsageAttribution{AuthIndex: "agy-local-slot", AuthType: "oauth", Source: "local"}
	executor := &fakeExecutor{
		identifier: pluginID,
		execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(candidate), Attribution: attr}, nil
		},
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
			chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte(candidate)}
			close(chunks)
			return pluginapi.ExecutorStreamResponse{Chunks: chunks, Attribution: attr}, nil
		},
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"totalTokens":3}`)}, nil
		},
	}
	record := normalizeTestCapabilityRecord(capabilityRecord{id: pluginID, version: pluginVersion, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		Executor: executor, ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
		ExecutorInputFormats: []string{"gemini"}, ExecutorOutputFormats: []string{"gemini"},
		ModelProvider: modelProviderFunc{staticModels: func(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
			return pluginapi.ModelResponse{Provider: pluginID, Models: []pluginapi.ModelInfo{
				{ID: model, Name: "gemini-2.5-pro", Thinking: &pluginapi.ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
			}}, nil
		}},
		ModelRouter: modelRouterFunc(func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
			return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
		}),
	}}})
	record.loaded = &loadedPlugin{id: record.id, path: record.path, version: record.version, configYAML: []byte(pluginYAML)}
	host := newHostWithRecords(record)
	host.loaded[record.id] = record.loaded
	host.runtimeConfig = &config.Config{}
	host.RegisterModels(t.Context(), registry.GetGlobalRegistry())

	handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler.SetModelRouterHost(host)
	router := gin.New()
	router.Use(logging.CPATraceIDMiddleware())
	router.Use(func(c *gin.Context) { // the stale statement and headers every request starts with
		logging.OfferGinCPAAuthStatement(c, wireStale)
		c.Header(logging.CPAAuthKindHeader, wireStale.Kind)
		c.Header(logging.CPAAuthProviderHeader, wireStale.Provider)
		c.Header(logging.CPAAuthHostHeader, wireStale.Host)
		c.Header(logging.ProvimuxAccountHeader, wireStale.Provimux.Account)
		c.Next()
	})
	openAI := openai.NewOpenAIAPIHandler(handler)
	router.POST("/v1/chat/completions", openAI.ChatCompletions)
	router.POST("/v1beta/models/*action", gemini.NewGeminiAPIHandler(handler).GeminiHandler)
	router.GET("/v1/responses", openai.NewOpenAIResponsesAPIHandler(handler).ResponsesWebsocket)

	// Home's only-my-names rule keeps another name: Home decides on acp/ names itself.
	rule := onlymynames.NewRule(true, true)
	rule.AddKeep([]string{"another-name"})
	registry.SetProvimuxNamesRule(rule)
	t.Cleanup(registry.ResetProvimuxNames)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	httpClient := server.Client()
	post := func(path, payload, bearer string, conn *string) (*http.Response, []byte) {
		t.Helper()
		req, errReq := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(payload))
		if errReq != nil {
			t.Fatal(errReq)
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { *conn = info.Conn.LocalAddr().String() }}))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		resp, errDo := httpClient.Do(req)
		if errDo != nil {
			t.Fatal(errDo)
		}
		body, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			t.Fatal(errRead)
		}
		return resp, body
	}

	t.Run("http", func(t *testing.T) {
		var first string
		requests := []struct{ path, payload, contains string }{
			{"/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"test"}]}`, alias+"(high)"), "answer"},
			{"/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"test"}],"stream":true}`, alias+"(high)"), "answer"},
			{"/v1beta/models/" + alias + "(high):generateContent", `{"contents":[{"parts":[{"text":"test"}]}]}`, "answer"},
			{"/v1beta/models/" + alias + "(high):streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"test"}]}]}`, "answer"},
			{"/v1beta/models/" + alias + "(high):countTokens", `{"contents":[{"parts":[{"text":"test"}]}]}`, "totalTokens"},
		}
		for _, via := range []string{"credential", "local", "legacy", "local"} {
			viaCredential.Store(via == "credential")
			viaLegacy.Store(via == "legacy")
			want, wantMarker := wireAnswered, answeredMarker
			if via == "local" {
				want, wantMarker = logging.AuthStatement{}, ""
			}
			for _, r := range requests {
				var conn string
				resp, body := post(r.path, r.payload, key, &conn)
				if first == "" {
					first = conn
				}
				if conn != first {
					t.Fatalf("%s %s: a new connection (%s, first %s); the case needs one connection", via, r.path, conn, first)
				}
				if got := wireStatement(resp.Header); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), r.contains) || got != want {
					t.Errorf("%s %s: %d %+v %s; want 200 naming %+v", via, r.path, resp.StatusCode, got, body, want)
				}
				if got := resp.Header.Values(logging.ProvimuxAccountHeader); len(got) != 0 && (len(got) != 1 || got[0] != wantMarker) || len(got) == 0 && wantMarker != "" {
					t.Errorf("%s %s: X-Provimux-Account %v, want %q", via, r.path, got, wantMarker)
				}
				for _, name := range logging.ProvimuxFieldNames() {
					if values := resp.Header.Values(name.Header); name.Header != logging.ProvimuxAccountHeader && len(values) != 0 {
						t.Errorf("%s %s: %s %v", via, r.path, name.Header, values)
					}
				}
			}
		}
		viaCredential.Store(false)
		viaLegacy.Store(false)
		var conn string
		resp, body := post("/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"test"}]}`, alias), "restricted-key", &conn)
		if got := wireStatement(resp.Header); resp.StatusCode != http.StatusForbidden || got != (logging.AuthStatement{}) || resp.Header.Get(logging.ProvimuxAccountHeader) != "" {
			t.Errorf("refused key: %d %+v %q %s; want 403 naming nothing", resp.StatusCode, got, resp.Header.Get(logging.ProvimuxAccountHeader), body)
		}
	})

	t.Run("websocket", func(t *testing.T) {
		ws, upgrade, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + key}})
		if errDial != nil {
			t.Fatal(errDial)
		}
		t.Cleanup(func() { _ = ws.Close() })
		if got := wireStatement(upgrade.Header); got != (logging.AuthStatement{}) || upgrade.Header.Get(logging.ProvimuxAccountHeader) != "" {
			t.Fatalf("the upgrade names %+v %q", got, upgrade.Header.Get(logging.ProvimuxAccountHeader))
		}
		fields := []string{"cpa_auth_kind", "cpa_auth_provider", "cpa_auth_host", "provimux_account"}
		for turn, native := range []bool{true, false, true} {
			viaCredential.Store(native)
			_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
			if errWrite := ws.WriteJSON(map[string]any{"type": "response.create", "model": alias + "(high)",
				"input": []any{map[string]any{"type": "message", "role": "user", "content": "test"}}}); errWrite != nil {
				t.Fatal(errWrite)
			}
			created := false
			for {
				_, frame, errRead := ws.ReadMessage()
				if errRead != nil {
					t.Fatalf("turn %d: %v", turn, errRead)
				}
				event := ojson.GetBytes(frame, "type").String()
				if event == "error" || event == "response.failed" {
					t.Fatalf("turn %d: %s", turn, frame)
				}
				if event == "response.created" {
					created = true
					got := logging.AuthStatement{Kind: ojson.GetBytes(frame, fields[0]).String(),
						Provider: ojson.GetBytes(frame, fields[1]).String(), Host: ojson.GetBytes(frame, fields[2]).String()}
					want := logging.AuthStatement{}
					if native {
						want = wireAnswered
					}
					if got != want {
						t.Errorf("turn %d (credential %v): response.created names %+v, want %+v", turn, native, got, want)
					}
					if marker := ojson.GetBytes(frame, fields[3]); native && (marker.String() != answeredMarker || answeredMarker == "" && marker.Exists()) {
						t.Errorf("turn %d: response.created names the account %q, want %q", turn, marker.String(), answeredMarker)
					}
				}
				for _, field := range fields {
					if ojson.GetBytes(frame, field).Exists() && (event != "response.created" || !native) {
						t.Errorf("turn %d (credential %v): %s carries %s", turn, native, event, field)
					}
				}
				if event == "response.completed" {
					break
				}
			}
			if !created {
				t.Errorf("turn %d: no response.created", turn)
			}
		}
		viaCredential.Store(false)
	})
}
