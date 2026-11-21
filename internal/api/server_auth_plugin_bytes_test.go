package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkhandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type pluginBytesInnerCall struct{}

// pluginBytesExecutor is answeredExecutor with a count its upstream produced, and, for the credential in prelude, a
// stream that fails before any deliverable payload, after one chunk when sendPrelude is set.
type pluginBytesExecutor struct {
	*answeredExecutor
	prelude     string
	sendPrelude bool
}

func (e *pluginBytesExecutor) CountTokens(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if err := e.attempt(ctx, a); err != nil {
		return coreexecutor.Response{}, err
	}
	return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"input_tokens":7,"note":"counted-by-%s"}`, a.ID))}, nil
}

func (e *pluginBytesExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if a.ID != e.prelude {
		return e.answeredExecutor.ExecuteStream(ctx, a, req, opts)
	}
	e.mu.Lock()
	*e.attempts = append(*e.attempts, a.ID)
	e.mu.Unlock()
	coreexecutor.MarkUpstreamAttempt(ctx)
	chunks := make(chan coreexecutor.StreamChunk, 2)
	if e.sendPrelude {
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: ping\ndata: {\"type\":\"ping\",\"by\":\"prelude-of-" + a.ID + "\"}\n\n")}
	}
	chunks <- coreexecutor.StreamChunk{Err: &auth.Error{HTTPStatus: http.StatusServiceUnavailable, Message: "fixture failed before any deliverable payload"}}
	close(chunks)
	return &coreexecutor.StreamResult{Headers: http.Header{"X-Fixture-Attempt": {a.ID}}, Chunks: chunks}, nil
}

// pluginBytesHost stands for plugin code at the handlers' host interfaces: response and stream-chunk interceptors,
// request interceptors and a model router with its executor. Every nested model call it makes is pinned to the inner
// credential. Modes:
//
//	side-call              a response interceptor makes a nested call and keeps the outer body
//	replace                it returns the nested answer's body
//	replace+headers        the same, with the nested answer's headers, an X-CPA-ANSWER of its own and forged
//	                       X-CPA-AUTH-* headers naming the inner credential
//	edit                   it edits the outer body; no nested call
//	edit+side-call         it edits the outer body after a nested call
//	keep+headers           it returns the body as given, with headers of its own (X-CPA-ANSWER, forged X-CPA-AUTH-*)
//	delegate               a routed executor forwards the nested answer's body
//	own                    a routed executor makes a nested call and returns bytes of its own
//	before-auth-side-call  a request interceptor makes a nested call before the credential is selected
//	after-auth-side-call   a request interceptor makes a nested call after it is selected
//	drop-first             (stream) the first chunk is dropped, the rest returned as given
//	edit-later             (stream) the first chunk is returned as given, a later one replaced
//	drop-prelude           (stream) a failed attempt's chunk is dropped, the retry's returned as given
//	edit-retry             (stream) the retry's first chunk is edited
type pluginBytesHost struct {
	h      *sdkhandlers.BaseAPIHandler
	mode   string
	inner  string
	failed string
	mu     sync.Mutex
	nested []byte
	// droppedPrelude counts the failed attempt's chunks the chain dropped (drop-prelude).
	droppedPrelude int
}

func pluginBytesForgedHeaders(extra http.Header) http.Header {
	out := http.Header{}
	for k, v := range extra {
		out[k] = append([]string(nil), v...)
	}
	out.Set("X-CPA-ANSWER", "made-up-reference")
	out.Set("X-Plugin-Note", "kept")
	out.Set(internallogging.CPAAuthKindHeader, "oauth")
	out.Set(internallogging.CPAAuthProviderHeader, "devin")
	out["x-cpa-auth-host"] = []string{"forged.example.test"}
	return out
}

func (p *pluginBytesHost) call(ctx context.Context, body []byte, stream bool) ([]byte, http.Header) {
	ctx = context.WithValue(ctx, pluginBytesInnerCall{}, true)
	req := sdkhandlers.ModelExecutionRequest{EntryProtocol: "claude", Model: answeredTestModel, AuthID: p.inner, Body: body, Stream: stream, SkipRouterPluginID: "plugin-bytes", SkipInterceptorPluginID: "plugin-bytes"}
	var out []byte
	var headers http.Header
	if !stream {
		res, errMsg := p.h.ExecuteModel(ctx, req)
		if errMsg != nil {
			panic(errMsg.Error)
		}
		out, headers = res.Body, res.Headers
	} else {
		res, errMsg := p.h.ExecuteModelStream(ctx, req)
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
		out, headers = b.Bytes(), res.Headers
	}
	p.mu.Lock()
	p.nested = out
	p.mu.Unlock()
	return out, headers
}

func (p *pluginBytesHost) routed() bool { return p.mode == "delegate" || p.mode == "own" }

// HasStreamInterceptors reports whether this plugin's stream-chunk interceptor is in a stream's path: in every mode but
// those whose plugin code is a request interceptor or a routed executor.
func (p *pluginBytesHost) HasStreamInterceptors() bool {
	switch p.mode {
	case "before-auth-side-call", "after-auth-side-call", "delegate", "own":
		return false
	}
	return true
}

func (p *pluginBytesHost) madeNestedCall() bool {
	switch p.mode {
	case "edit", "keep+headers", "drop-first", "edit-later", "drop-prelude", "edit-retry":
		return false
	}
	return true
}

func (p *pluginBytesHost) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if p.mode == "before-auth-side-call" && ctx.Value(pluginBytesInnerCall{}) == nil {
		p.call(ctx, req.Body, false)
	}
	return pluginapi.RequestInterceptResponse{}
}

func (p *pluginBytesHost) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if p.mode == "after-auth-side-call" && ctx.Value(pluginBytesInnerCall{}) == nil {
		p.call(ctx, req.Body, false)
	}
	return pluginapi.RequestInterceptResponse{}
}

func pluginBytesEdit(body []byte) []byte {
	body = bytes.ReplaceAll(body, []byte("served-by-"), []byte("edited-served-by-"))
	return bytes.ReplaceAll(body, []byte("counted-by-"), []byte("edited-counted-by-"))
}

func (p *pluginBytesHost) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	if ctx.Value(pluginBytesInnerCall{}) != nil {
		return pluginapi.ResponseInterceptResponse{}
	}
	switch p.mode {
	case "edit":
		return pluginapi.ResponseInterceptResponse{Body: pluginBytesEdit(req.Body)}
	case "keep+headers":
		return pluginapi.ResponseInterceptResponse{Body: req.Body, Headers: pluginBytesForgedHeaders(nil)}
	case "side-call", "replace", "replace+headers", "edit+side-call":
	default:
		return pluginapi.ResponseInterceptResponse{}
	}
	nestedBody := req.RequestBody
	if strings.Contains(string(req.Body), "counted-by-") { // a count: the nested call is a plain message
		nestedBody = []byte(fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, answeredTestModel))
	}
	body, headers := p.call(ctx, nestedBody, false)
	switch p.mode {
	case "replace":
		return pluginapi.ResponseInterceptResponse{Body: body}
	case "replace+headers":
		return pluginapi.ResponseInterceptResponse{Body: body, Headers: pluginBytesForgedHeaders(headers)}
	case "edit+side-call":
		return pluginapi.ResponseInterceptResponse{Body: pluginBytesEdit(req.Body)}
	}
	return pluginapi.ResponseInterceptResponse{}
}

func (p *pluginBytesHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	if ctx.Value(pluginBytesInnerCall{}) != nil || req.ChunkIndex < 0 {
		return pluginapi.StreamChunkInterceptResponse{}
	}
	first := req.ChunkIndex == 0
	prelude := p.failed != "" && strings.Contains(string(req.Body), "prelude-of-"+p.failed)
	switch p.mode {
	case "edit":
		return pluginapi.StreamChunkInterceptResponse{Body: pluginBytesEdit(req.Body)}
	case "keep+headers":
		if first {
			return pluginapi.StreamChunkInterceptResponse{Body: req.Body, Headers: pluginBytesForgedHeaders(nil)}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: req.Body}
	case "drop-first":
		if first {
			return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: req.Body}
	case "edit-later":
		if first {
			return pluginapi.StreamChunkInterceptResponse{Body: req.Body}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: []byte("event: message_stop\ndata: {\"type\":\"message_stop\",\"by\":\"plugin-later\"}\n\n")}
	case "drop-prelude":
		if prelude {
			p.mu.Lock()
			p.droppedPrelude++
			p.mu.Unlock()
			return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
		}
		return pluginapi.StreamChunkInterceptResponse{Body: req.Body}
	case "edit-retry":
		return pluginapi.StreamChunkInterceptResponse{Body: pluginBytesEdit(req.Body)}
	case "side-call", "replace", "replace+headers", "edit+side-call":
	default:
		return pluginapi.StreamChunkInterceptResponse{}
	}
	if !first {
		switch p.mode {
		case "replace", "replace+headers":
			return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
		case "edit+side-call":
			return pluginapi.StreamChunkInterceptResponse{Body: pluginBytesEdit(req.Body)}
		}
		return pluginapi.StreamChunkInterceptResponse{}
	}
	body, headers := p.call(ctx, req.RequestBody, true)
	switch p.mode {
	case "replace":
		return pluginapi.StreamChunkInterceptResponse{Body: body}
	case "replace+headers":
		return pluginapi.StreamChunkInterceptResponse{Body: body, Headers: pluginBytesForgedHeaders(headers)}
	case "edit+side-call":
		return pluginapi.StreamChunkInterceptResponse{Body: pluginBytesEdit(req.Body)}
	}
	return pluginapi.StreamChunkInterceptResponse{}
}

func (p *pluginBytesHost) HasModelRouters() bool { return p.routed() }

func (p *pluginBytesHost) RouteModel(ctx context.Context, _ pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	if !p.routed() || ctx.Value(pluginBytesInnerCall{}) != nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "plugin-bytes"}, true
}

func (p *pluginBytesHost) result(body []byte, headers http.Header) ([]byte, http.Header) {
	if p.mode == "own" {
		return bytes.ReplaceAll(body, []byte("served-by-"+p.inner), []byte("made-by-the-plugin")), http.Header{"Content-Type": {"application/json"}}
	}
	return body, headers
}

func (p *pluginBytesHost) ExecutePluginExecutor(ctx context.Context, _ string, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	body, headers := p.result(p.call(ctx, req.Payload, false))
	return coreexecutor.Response{Payload: body, Headers: headers}, nil
}

func (p *pluginBytesHost) ExecutePluginExecutorStream(ctx context.Context, _ string, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	body, headers := p.result(p.call(ctx, req.Payload, true))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: body}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks, Headers: headers}, nil
}

func (p *pluginBytesHost) CountPluginExecutor(ctx context.Context, _ string, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	_, headers := p.call(ctx, []byte(fmt.Sprintf(`{"model":%q,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, answeredTestModel)), false)
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":19,"note":"made-by-the-plugin"}`), Headers: headers}, nil
}

type pluginBytesCell struct {
	mode     string
	path     string // "" is /v1/messages
	streams  []bool
	body     string
	want     internallogging.AuthStatement
	retry    int // credentials per conductor call; 0: no failing first attempt
	wantCode int
}

const (
	pluginBytesOuter      = "outer"
	pluginBytesEdited     = "outer, edited"
	pluginBytesNested     = "nested"
	pluginBytesPlugin     = "plugin's"
	pluginBytesFromSecond = "outer from its second chunk"
	pluginBytesThenPlugin = "outer, then the plugin's"
	pluginBytesRetry      = "the retry's"
	pluginBytesRetryEd    = "the retry's, edited"
	pluginBytesCount      = "outer count"
	pluginBytesCountEd    = "outer count, edited"
)

// The proxy names a credential only for bytes its own code carried, unchanged, from that credential's exchange. Where
// the response interceptor chain changed a plain or count body, or the bytes came from a routed executor, the response
// names nobody; a stream that passes through the stream-chunk interceptor chain names nobody whatever the chain does
// (after the third review). Plugin code that only looks at a plain body, makes a nested call it does not return, or
// acts on the request leaves the name alone; headers a plugin returns (an X-CPA-ANSWER of its own, forged X-CPA-AUTH-*
// values) select nothing. (DESIGN.md 12.4, test 1.)
func TestBytesFromPluginCodeNameNobody(t *testing.T) {
	outer, inner := answeredCredentials()
	outerStatement := internallogging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "openrouter.example.test"}
	nobody := internallogging.AuthStatement{}
	both := []bool{false, true}
	sse := []bool{true}
	plain := []bool{false}
	const count = "/v1/messages/count_tokens"
	cells := []pluginBytesCell{
		{mode: "side-call", streams: plain, body: pluginBytesOuter, want: outerStatement},
		// A stream-chunk interceptor is in the stream's path: nobody, whatever it does (after the third review).
		{mode: "side-call", streams: sse, body: pluginBytesOuter, want: nobody},
		{mode: "replace", streams: both, body: pluginBytesNested, want: nobody},
		{mode: "replace+headers", streams: both, body: pluginBytesNested, want: nobody},
		{mode: "edit", streams: both, body: pluginBytesEdited, want: nobody},
		{mode: "edit+side-call", streams: both, body: pluginBytesEdited, want: nobody},
		{mode: "keep+headers", streams: plain, body: pluginBytesOuter, want: outerStatement},
		{mode: "keep+headers", streams: sse, body: pluginBytesOuter, want: nobody},
		{mode: "delegate", streams: both, body: pluginBytesNested, want: nobody},
		{mode: "own", streams: both, body: pluginBytesPlugin, want: nobody},
		{mode: "before-auth-side-call", streams: both, body: pluginBytesOuter, want: outerStatement},
		{mode: "after-auth-side-call", streams: both, body: pluginBytesOuter, want: outerStatement},
		{mode: "drop-first", streams: sse, body: pluginBytesFromSecond, want: nobody},
		// Formerly the stated limit (a stream named by its first deliverable payload); since the third review, nobody.
		{mode: "edit-later", streams: sse, body: pluginBytesThenPlugin, want: nobody},
		{mode: "drop-prelude", streams: sse, body: pluginBytesRetry, want: nobody, retry: 1},
		{mode: "drop-prelude", streams: sse, body: pluginBytesRetry, want: nobody, retry: 3},
		{mode: "edit-retry", streams: sse, body: pluginBytesRetryEd, want: nobody, retry: 1},
		{mode: "edit-retry", streams: sse, body: pluginBytesRetryEd, want: nobody, retry: 3},
		{mode: "side-call", path: count, streams: plain, body: pluginBytesCount, want: outerStatement},
		{mode: "edit", path: count, streams: plain, body: pluginBytesCountEd, want: nobody},
		{mode: "own", path: count, streams: plain, body: pluginBytesPlugin, want: nobody},
	}
	for _, passthrough := range []bool{false, true} {
		for _, c := range cells {
			path := c.path
			if path == "" {
				path = "/v1/messages"
			}
			for _, stream := range c.streams {
				name := fmt.Sprintf("%s %s stream=%t passthrough=%t", path, c.mode, stream, passthrough)
				if c.retry > 0 {
					name += fmt.Sprintf(" credentials-per-call=%d", c.retry)
				}
				t.Run(name, func(t *testing.T) {
					got, body, attempts := runPluginBytesCell(t, c, path, stream, passthrough, outer, inner)
					hasOuter := strings.Contains(body, "served-by-"+outer.ID) || strings.Contains(body, "counted-by-"+outer.ID)
					hasInner := strings.Contains(body, "served-by-"+inner.ID)
					edited := strings.Contains(body, "edited-served-by-") || strings.Contains(body, "edited-counted-by-")
					made := strings.Contains(body, "made-by-the-plugin")
					later := strings.Contains(body, "plugin-later")
					prelude := strings.Contains(body, "prelude-of-")
					var bodyOK bool
					switch c.body {
					case pluginBytesOuter, pluginBytesCount:
						bodyOK = hasOuter && !hasInner && !edited && !made && !later
					case pluginBytesEdited, pluginBytesCountEd:
						bodyOK = hasOuter && !hasInner && edited && !made
					case pluginBytesNested:
						bodyOK = hasInner && !hasOuter && !edited && !made
					case pluginBytesPlugin:
						bodyOK = made && !hasOuter && !hasInner
					case pluginBytesFromSecond:
						bodyOK = !hasOuter && !hasInner && strings.Contains(body, "message_stop")
					case pluginBytesThenPlugin:
						bodyOK = hasOuter && !hasInner && later
					case pluginBytesRetry:
						bodyOK = hasInner && !hasOuter && !edited && !prelude
					case pluginBytesRetryEd:
						bodyOK = hasInner && !hasOuter && edited && !prelude
					}
					if !bodyOK {
						t.Fatalf("fixture: body is not the %s one (attempts %v): %q", c.body, attempts, body)
					}
					if got != c.want {
						t.Fatalf("body %s: headers name %+v, want %+v", c.body, got, c.want)
					}
				})
			}
		}
	}
}

func runPluginBytesCell(t *testing.T, c pluginBytesCell, path string, stream, passthrough bool, outer, inner *auth.Auth) (internallogging.AuthStatement, string, []string) {
	t.Helper()
	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"test-key"}
	cfg.PassthroughHeaders = passthrough
	if c.retry > 0 {
		cfg.Streaming.BootstrapRetries = 1
	}
	server := newTestServerWithConfig(t, cfg)
	if c.retry > 0 {
		server.handlers.AuthManager.SetRetryConfig(0, 0, c.retry)
	}
	attempts := []string{}
	mu := &sync.Mutex{}
	prelude := ""
	if c.retry > 0 {
		prelude = outer.ID
	}
	for _, credential := range []*auth.Auth{outer, inner} {
		server.handlers.AuthManager.RegisterExecutor(&pluginBytesExecutor{answeredExecutor: &answeredExecutor{provider: credential.Provider, attempts: &attempts, mu: mu}, prelude: prelude, sendPrelude: c.mode == "drop-prelude"})
		if _, err := server.handlers.AuthManager.Register(context.Background(), credential.Clone()); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: answeredTestModel, OwnedBy: "anthropic"}})
		id := credential.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	host := &pluginBytesHost{h: server.handlers, mode: c.mode, inner: inner.ID, failed: prelude}
	server.handlers.SetPluginHost(host)
	server.handlers.SetModelRouterHost(host)

	recorder := newFirstCommitRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fmt.Sprintf(`{"model":%q,"max_tokens":8,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, answeredTestModel, stream)))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	body := recorder.Body.String()
	mu.Lock()
	calls := append([]string(nil), attempts...)
	mu.Unlock()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, body %q (attempts %v)", recorder.Code, body, calls)
	}
	host.mu.Lock()
	nested := string(host.nested)
	droppedPrelude := host.droppedPrelude
	host.mu.Unlock()
	if c.mode == "drop-prelude" && droppedPrelude != 1 {
		t.Fatalf("fixture: the chain dropped %d chunks of the failed attempt, want 1 (attempts %v)", droppedPrelude, calls)
	}
	if want := host.madeNestedCall(); want != strings.Contains(nested, "served-by-"+inner.ID) {
		t.Fatalf("fixture: nested call answered by %s: %v, want %v (attempts %v)", inner.ID, !want, want, calls)
	}
	if c.retry > 0 && fmt.Sprint(calls) != fmt.Sprint([]string{outer.ID, inner.ID}) {
		t.Fatalf("fixture: attempts %v, want the outer credential to fail and the inner one to answer", calls)
	}
	got := internallogging.AuthStatement{
		Kind:     recorder.snapshot.Get(internallogging.CPAAuthKindHeader),
		Provider: recorder.snapshot.Get(internallogging.CPAAuthProviderHeader),
		Host:     recorder.snapshot.Get(internallogging.CPAAuthHostHeader),
	}
	if got != (internallogging.AuthStatement{}) && (!recorder.nextKnown || recorder.nextFlush || isKeepAliveWrite(recorder.nextBytes)) {
		t.Fatalf("%+v not committed by the write of the answer (flush %v, bytes %q)", got, recorder.nextFlush, recorder.nextBytes)
	}
	for _, name := range answeredOriginHeaders {
		if values := recorder.snapshot.Values(name); len(values) > 1 {
			t.Fatalf("%s carries %d values: %q", name, len(values), values)
		}
		for key, values := range recorder.snapshot {
			if strings.EqualFold(key, name) && key != http.CanonicalHeaderKey(name) {
				t.Fatalf("the response carries %s under the spelling %q: %q", name, key, values)
			}
		}
	}
	return got, body, calls
}
