package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// firstCommitRecorder is the http.ResponseWriter under the engine. At the first WriteHeader it receives (the moment
// the headers leave) it notes the header snapshot and the status, then what the next call is: a Write, with which
// bytes, or a Flush. It closes flushed at the first flush, so a fixture can wait until a keep-alive has committed.
type firstCommitRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	committed bool
	snapshot  http.Header
	status    int
	nextKnown bool
	nextFlush bool
	nextBytes []byte
	flushed   chan struct{}
	flushOnce sync.Once
}

func newFirstCommitRecorder() *firstCommitRecorder {
	return &firstCommitRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
}

func (r *firstCommitRecorder) commit(status int) {
	if !r.committed {
		r.committed = true
		r.snapshot = r.ResponseRecorder.Header().Clone()
		r.status = status
	}
}

func (r *firstCommitRecorder) WriteHeader(status int) {
	r.mu.Lock()
	r.commit(status)
	r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(status)
}

func (r *firstCommitRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	r.commit(http.StatusOK)
	if !r.nextKnown {
		r.nextKnown = true
		r.nextBytes = append([]byte(nil), data...)
	}
	r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

func (r *firstCommitRecorder) WriteString(data string) (int, error) { return r.Write([]byte(data)) }

func (r *firstCommitRecorder) Flush() {
	r.mu.Lock()
	r.commit(http.StatusOK)
	if !r.nextKnown {
		r.nextKnown = true
		r.nextFlush = true
	}
	r.mu.Unlock()
	r.ResponseRecorder.Flush()
	r.flushOnce.Do(func() { close(r.flushed) })
}

// firstByteCredential is a credential of the matrix, the statement it is named by and the marker its upstream puts in
// what it emits.
type firstByteCredential struct {
	auth      *auth.Auth
	statement internallogging.AuthStatement
	marker    string
}

func isKeepAliveWrite(data []byte) bool {
	switch string(data) {
	case "\n", ": keep-alive\n\n":
		return true
	}
	return strings.HasPrefix(string(data), "event: keepalive\n") || strings.HasPrefix(string(data), ": keep-alive")
}

// assertFirstCommit applies the rule to one response. A statement in the committed headers must be whole, one
// credential's, committed by a Write that is not a keep-alive, with the answer's status, on a body that carries that
// credential's marker and no other credential's. On the default configuration, a body that carries a credential's
// marker must name it. It returns the named credential, or nil.
func assertFirstCommit(t *testing.T, r *firstCommitRecorder, credentials []firstByteCredential, answerStatus int, defaultConfig bool) *firstByteCredential {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	body := r.Body.String()
	got := internallogging.AuthStatement{
		Kind:     r.snapshot.Get(internallogging.CPAAuthKindHeader),
		Provider: r.snapshot.Get(internallogging.CPAAuthProviderHeader),
		Host:     r.snapshot.Get(internallogging.CPAAuthHostHeader),
	}
	var named *firstByteCredential
	if got != (internallogging.AuthStatement{}) {
		for i := range credentials {
			if credentials[i].statement == got {
				named = &credentials[i]
			}
		}
		if named == nil {
			t.Fatalf("headers name %+v: not whole, or no credential's statement", got)
		}
		if !r.nextKnown || r.nextFlush {
			t.Fatalf("%+v committed by a flush, not by the write of an answer (next known %v)", got, r.nextKnown)
		}
		if isKeepAliveWrite(r.nextBytes) {
			t.Fatalf("%+v committed by a keep-alive %q", got, r.nextBytes)
		}
		if r.status != answerStatus {
			t.Fatalf("%+v committed with status %d, want the answer's %d", got, r.status, answerStatus)
		}
		for _, other := range credentials {
			has := strings.Contains(body, other.marker)
			if other.auth.ID == named.auth.ID && !has {
				t.Fatalf("headers name %s; body has no %q: %q", named.auth.ID, other.marker, body)
			}
			if other.auth.ID != named.auth.ID && has {
				t.Fatalf("headers name %s; body carries %s's marker %q", named.auth.ID, other.auth.ID, other.marker)
			}
		}
	}
	if defaultConfig {
		for _, credential := range credentials {
			if strings.Contains(body, credential.marker) && got != credential.statement {
				t.Fatalf("default configuration: body from %s, headers %+v, want %+v", credential.auth.ID, got, credential.statement)
			}
		}
	}
	return named
}

// firstByteBehaviour is what a credential's upstream does in a cell.
type firstByteBehaviour string

const (
	behaviourAnswer firstByteBehaviour = "answers"
	behaviourFail   firstByteBehaviour = "fails"
	behaviourHold   firstByteBehaviour = "holds then fails"
)

type firstByteScenario struct {
	name      string
	behaviour [2]firstByteBehaviour
	answering int // index of the credential whose answer the client must get, -1 for none
	streams   bool
}

var firstByteScenarios = []firstByteScenario{
	{name: "first answers", behaviour: [2]firstByteBehaviour{behaviourAnswer, behaviourAnswer}, answering: 0},
	{name: "first fails upstream, second answers", behaviour: [2]firstByteBehaviour{behaviourFail, behaviourAnswer}, answering: 1},
	{name: "first stream held then fails, second answers", behaviour: [2]firstByteBehaviour{behaviourHold, behaviourAnswer}, answering: 1, streams: true},
	{name: "all fail", behaviour: [2]firstByteBehaviour{behaviourFail, behaviourFail}, answering: -1},
}

type firstByteConfiguration struct {
	name      string
	keepAlive bool
}

var firstByteConfigurations = []firstByteConfiguration{{name: "default"}, {name: "keep-alives at one second", keepAlive: true}}

func firstByteConfig(configuration firstByteConfiguration) *proxyconfig.Config {
	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"test-key"}
	cfg.Streaming.BootstrapRetries = 1
	if configuration.keepAlive {
		cfg.NonStreamKeepAliveInterval = 1
		cfg.Streaming.KeepAliveSeconds = 1
		cfg.Streaming.SendKeepAliveBeforeFT = true
	}
	return cfg
}

// waitForKeepAlive lets a keep-alive commit first, when the cell expects one.
func waitForKeepAlive(ctx context.Context, flushed <-chan struct{}) {
	if flushed == nil {
		return
	}
	select {
	case <-flushed:
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
}

// firstByteClaudeTransport is the upstream of the real Claude executor: it tells the credential by the key or token
// the executor sent, and answers as that credential's behaviour says, with its marker in the text (or in the count).
type firstByteClaudeTransport struct {
	byToken   map[string]int
	behaviour [2]firstByteBehaviour
	markers   [2]string
	usage     [2]int
	flushed   <-chan struct{}
}

func (x *firstByteClaudeTransport) RoundTripperFor(*auth.Auth) http.RoundTripper { return x }

func (x *firstByteClaudeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	index, ok := x.byToken[req.Header.Get("x-api-key")]
	if !ok {
		index, ok = x.byToken[strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")]
	}
	if !ok {
		return nil, fmt.Errorf("fixture upstream: unknown credential for %s", req.URL)
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&payload)
		_ = req.Body.Close()
	}
	response := func(status int, contentType string, body io.ReadCloser) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: body, Request: req}
	}
	switch x.behaviour[index] {
	case behaviourFail:
		waitForKeepAlive(req.Context(), x.flushed)
		return response(http.StatusServiceUnavailable, "application/json", io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"fixture upstream unavailable"}}`))), nil
	case behaviourHold:
		reader, writer := io.Pipe()
		go func() {
			if x.flushed != nil {
				waitForKeepAlive(req.Context(), x.flushed)
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			_ = writer.CloseWithError(errors.New("fixture upstream disconnected"))
		}()
		return response(http.StatusOK, "text/event-stream", reader), nil
	}
	waitForKeepAlive(req.Context(), x.flushed)
	marker := x.markers[index]
	if strings.HasSuffix(req.URL.Path, "/count_tokens") {
		return response(http.StatusOK, "application/json", io.NopCloser(strings.NewReader(`{"input_tokens":`+marker+`}`))), nil
	}
	if payload.Stream {
		events := "event: message_start\ndata: " + fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_matrix","type":"message","role":"assistant","model":%q,"content":[],"usage":{"input_tokens":%d,"output_tokens":0}}}`, wireModelForMatrix, x.usage[index]) + "\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + marker + "\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		return response(http.StatusOK, "text/event-stream", io.NopCloser(strings.NewReader(events))), nil
	}
	return response(http.StatusOK, "application/json", io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":"msg_matrix","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":2}}`, wireModelForMatrix, marker, x.usage[index])))), nil
}

const wireModelForMatrix = "claude-opus-5-5"

type firstBytePath struct {
	row       string
	method    string
	path      string
	body      string
	stream    bool
	count     bool
	compact   bool // the answer's text is encrypted: the marker is the credential's usage count
	keepAlive bool // a keep-alive can commit before the answer on this path
	bound     bool // the request belongs to one credential: no other is tried after a failure
}

func firstByteProtocolPaths() []firstBytePath {
	m := wireModelForMatrix
	messages := func(stream bool) string {
		return fmt.Sprintf(`{"model":%q,"max_tokens":8,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, m, stream)
	}
	paths := []firstBytePath{
		{row: "1", method: http.MethodPost, path: "/v1/messages", body: messages(false), keepAlive: true},
		{row: "2", method: http.MethodPost, path: "/v1/messages", body: messages(true), stream: true, keepAlive: true},
		{row: "3", method: http.MethodPost, path: "/v1/messages/count_tokens", body: fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, m), count: true},
		{row: "10", method: http.MethodPost, path: "/v1/responses/compact", body: fmt.Sprintf(`{"model":%q,"input":"hi"}`, m), compact: true, keepAlive: true},
		{row: "10", method: http.MethodPost, path: "/backend-api/codex/responses/compact", body: fmt.Sprintf(`{"model":%q,"input":"hi"}`, m), compact: true, keepAlive: true},
		{row: "13", method: http.MethodPost, path: "/v1beta/models/" + m + ":countTokens", body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, count: true},
	}
	for _, stream := range []bool{false, true} {
		row := func(plain, streamed string) string {
			if stream {
				return streamed
			}
			return plain
		}
		gemini := "/v1beta/models/" + m + ":generateContent"
		if stream {
			gemini = "/v1beta/models/" + m + ":streamGenerateContent?alt=sse"
		}
		paths = append(paths,
			firstBytePath{row: row("4", "5"), method: http.MethodPost, path: "/v1/chat/completions", body: fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, m, stream), stream: stream, keepAlive: true},
			firstBytePath{row: row("6", "7"), method: http.MethodPost, path: "/v1/completions", body: fmt.Sprintf(`{"model":%q,"stream":%t,"prompt":"hi"}`, m, stream), stream: stream, keepAlive: true},
			firstBytePath{row: row("8", "9"), method: http.MethodPost, path: "/v1/responses", body: fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hi"}`, m, stream), stream: stream, keepAlive: true},
			firstBytePath{row: row("8", "9"), method: http.MethodPost, path: "/backend-api/codex/responses", body: fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hi"}`, m, stream), stream: stream, keepAlive: true},
			firstBytePath{row: row("11", "12"), method: http.MethodPost, path: gemini, body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, stream: stream, keepAlive: true},
			firstBytePath{row: row("14", "15"), method: http.MethodPost, path: "/v1beta/interactions", body: fmt.Sprintf(`{"model":%q,"stream":%t,"input":"hi"}`, m, stream), stream: stream, keepAlive: true},
		)
	}
	return paths
}

// The matrix of section 7.1.2 over the paths of section 3 that the proxy's own engine serves: every protocol route of
// the execution layer (rows 1 to 15) with the real Claude executor, the image, video and speech routes (rows 16 to 20,
// 23 to 27) with fixture executors, and alpha search (row 32). Two credentials with different statements; each
// upstream puts its credential's marker in what it emits. Scenarios: the first answers; the first fails upstream and
// the second answers; the first stream is held then fails and the second answers (one bootstrap retry); all fail. On
// the default configuration and with every keep-alive at one second, the fixture then waiting for the first flush.
func TestHeadersNameACredentialOnlyAtItsOwnFirstByte(t *testing.T) {
	cells := 0
	t.Run("protocols", func(t *testing.T) {
		for _, path := range firstByteProtocolPaths() {
			for _, scenario := range firstByteScenarios {
				if scenario.streams && !path.stream {
					continue
				}
				for _, configuration := range firstByteConfigurations {
					cells++
					path, scenario, configuration := path, scenario, configuration
					t.Run(fmt.Sprintf("row %s %s %s stream=%t/%s/%s", path.row, path.method, path.path, path.stream, scenario.name, configuration.name), func(t *testing.T) {
						runProtocolFirstByteCell(t, path, scenario, configuration)
					})
				}
			}
		}
	})
	t.Run("media", func(t *testing.T) {
		for _, path := range firstByteMediaPaths() {
			for _, scenario := range firstByteScenarios {
				if scenario.streams && !path.stream || path.bound && scenario.answering == 1 {
					continue
				}
				for _, configuration := range firstByteConfigurations {
					cells++
					path, scenario, configuration := path, scenario, configuration
					t.Run(fmt.Sprintf("row %s %s %s stream=%t/%s/%s", path.row, path.method, path.path, path.stream, scenario.name, configuration.name), func(t *testing.T) {
						runMediaFirstByteCell(t, path, scenario, configuration)
					})
				}
			}
		}
	})
	t.Run("row 32 alpha search", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			httpErr error
			status  int
		}{
			{name: "upstream answered", status: http.StatusOK},
			{name: "upstream answered an error", status: http.StatusTooManyRequests},
			{name: "upstream unreachable", httpErr: errors.New("fixture upstream unreachable"), status: http.StatusBadGateway},
		} {
			cells++
			t.Run(test.name, func(t *testing.T) {
				server := newTestServer(t)
				upstreamStatus := test.status
				if test.httpErr != nil {
					upstreamStatus = 0
				}
				executor := &codexSearchCaptureExecutor{httpErr: test.httpErr, responseBody: io.NopCloser(strings.NewReader(`{"marker":"served-by-codex-login"}`))}
				if upstreamStatus != 0 {
					executor.statuses = []int{upstreamStatus}
				}
				server.handlers.AuthManager.RegisterExecutor(executor)
				credential := &auth.Auth{ID: "codex-login", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "made-up-token"}}
				if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
					t.Fatalf("register: %v", err)
				}
				req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"q"}`))
				req.Header.Set("Authorization", "Bearer test-key")
				req.Header.Set("Content-Type", "application/json")
				recorder := newFirstCommitRecorder()
				server.engine.ServeHTTP(recorder, req)
				if recorder.Code != test.status {
					t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
				}
				credentials := []firstByteCredential{{auth: credential, statement: internallogging.AuthStatement{Kind: "oauth", Provider: "codex"}, marker: "served-by-codex-login"}}
				named := assertFirstCommit(t, recorder, credentials, test.status, true)
				if (named != nil) != (test.httpErr == nil) {
					t.Fatalf("named %v for %s", named != nil, test.name)
				}
			})
		}
	})
	t.Logf("matrix: %d cells on this engine", cells)
}

func runProtocolFirstByteCell(t *testing.T, path firstBytePath, scenario firstByteScenario, configuration firstByteConfiguration) {
	t.Helper()
	cfg := firstByteConfig(configuration)
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.SetRetryConfig(0, 0, 3)
	recorder := newFirstCommitRecorder()

	first := &auth.Auth{ID: "matrix-first-key", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-key-first", "base_url": "https://first-gateway.example.test", "priority": "10"}}
	firstStatement := internallogging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "first-gateway.example.test"}
	if path.count {
		// A key with a base URL of its own is counted locally by the Claude executor: no upstream answers it.
		delete(first.Attributes, "base_url")
		firstStatement.Host = ""
	}
	second := &auth.Auth{ID: "matrix-second-login", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": "0"}, Metadata: map[string]any{"access_token": "fixture-token-second"}}
	markers := [2]string{"served-by-matrix-first-key", "served-by-matrix-second-login"}
	if path.count {
		markers = [2]string{"4101", "4202"}
	}
	usage := [2]int{4, 4}
	if path.compact {
		usage = [2]int{5101, 5202}
	}
	credentials := []firstByteCredential{
		{auth: first, statement: firstStatement, marker: markers[0]},
		{auth: second, statement: internallogging.AuthStatement{Kind: "oauth", Provider: "claude"}, marker: markers[1]},
	}
	if path.compact {
		credentials[0].marker = `"input_tokens":5101`
		credentials[1].marker = `"input_tokens":5202`
	}
	transport := &firstByteClaudeTransport{byToken: map[string]int{"fixture-key-first": 0, "fixture-token-second": 1}, behaviour: scenario.behaviour, markers: markers, usage: usage}
	if configuration.keepAlive && path.keepAlive {
		transport.flushed = recorder.flushed
	}
	manager.SetRoundTripperProvider(transport)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
	for _, credential := range credentials {
		if _, err := manager.Register(context.Background(), credential.auth); err != nil {
			t.Fatalf("register: %v", err)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.auth.ID, credential.auth.Provider, []*registry.ModelInfo{{ID: wireModelForMatrix, OwnedBy: "anthropic", Created: 1700000000}})
		id := credential.auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	req := httptest.NewRequest(path.method, path.path, strings.NewReader(path.body))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	server.engine.ServeHTTP(recorder, req)
	checkFirstByteCell(t, recorder, credentials, scenario, configuration)
}

// checkFirstByteCell asserts the rule, then that the cell exercised what it says: the client got the expected
// credential's answer.
func checkFirstByteCell(t *testing.T, recorder *firstCommitRecorder, credentials []firstByteCredential, scenario firstByteScenario, configuration firstByteConfiguration) {
	t.Helper()
	named := assertFirstCommit(t, recorder, credentials, http.StatusOK, !configuration.keepAlive)
	body := recorder.Body.String()
	if scenario.answering < 0 {
		if named != nil {
			t.Fatalf("all failed, headers name %s; body %q", named.auth.ID, body)
		}
		for _, credential := range credentials {
			if strings.Contains(body, credential.marker) {
				t.Fatalf("all failed, body carries %s's marker: %q", credential.auth.ID, body)
			}
		}
		return
	}
	want := credentials[scenario.answering]
	if !strings.Contains(body, want.marker) {
		t.Fatalf("fixture: body %q (status %d) lacks %s's marker %q", body, recorder.Code, want.auth.ID, want.marker)
	}
	if !configuration.keepAlive && (named == nil || named.auth.ID != want.auth.ID) {
		t.Fatalf("default configuration: %s answered, headers name %v", want.auth.ID, named)
	}
}

// firstByteMediaExecutor stands for the image, video and speech upstreams. It answers by the entry protocol, with the
// credential's marker in what it emits; it marks the upstream exchange as an executor's transport does.
type firstByteMediaExecutor struct {
	provider   string
	behaviour  map[string]firstByteBehaviour
	flushed    <-chan struct{}
	contentURL string
}

type firstByteMediaUnavailable struct{}

func (firstByteMediaUnavailable) Error() string   { return "fixture upstream unavailable" }
func (firstByteMediaUnavailable) StatusCode() int { return http.StatusServiceUnavailable }

func (e *firstByteMediaExecutor) Identifier() string { return e.provider }

func (e *firstByteMediaExecutor) Execute(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	waitForKeepAlive(ctx, e.flushed)
	if e.behaviour[a.ID] != behaviourAnswer {
		return coreexecutor.Response{}, firstByteMediaUnavailable{}
	}
	marker := "served-by-" + a.ID
	switch opts.SourceFormat.String() {
	case "openai-video":
		return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"request_id":%q,"status":"completed","progress":100,"video":{"url":%q,"duration":4}}`, marker, e.contentURL+"/"+marker+".mp4"))}, nil
	case "openai-speech":
		return coreexecutor.Response{Payload: []byte("audio-" + marker), Headers: http.Header{"Content-Type": {"audio/mpeg"}}}, nil
	default:
		return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"created":1,"data":[{"b64_json":%q,"revised_prompt":%q,"mime_type":"image/png"}]}`, marker, marker))}, nil
	}
}

func (e *firstByteMediaExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	switch e.behaviour[a.ID] {
	case behaviourFail:
		waitForKeepAlive(ctx, e.flushed)
		return nil, firstByteMediaUnavailable{}
	case behaviourHold:
		chunks := make(chan coreexecutor.StreamChunk, 1)
		go func() {
			defer close(chunks)
			if e.flushed != nil {
				waitForKeepAlive(ctx, e.flushed)
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			chunks <- coreexecutor.StreamChunk{Err: &auth.Error{Code: "unavailable", Message: "fixture stream interrupted", HTTPStatus: http.StatusServiceUnavailable}}
		}()
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	waitForKeepAlive(ctx, e.flushed)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":%q}\n\n", "served-by-"+a.ID))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *firstByteMediaExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *firstByteMediaExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not used")
}

func (e *firstByteMediaExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

type firstByteMediaPath struct {
	firstBytePath
	provider  string
	model     string
	modelType string
	download  bool
}

func firstByteMediaPaths() []firstByteMediaPath {
	compat := "matrix-compat-image"
	return []firstByteMediaPath{
		{firstBytePath: firstBytePath{row: "16", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"gpt-image-2","prompt":"a square"}`, keepAlive: true}, provider: "codex", model: "gpt-image-2"},
		{firstBytePath: firstBytePath{row: "17", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"gpt-image-2","prompt":"a square","stream":true}`, stream: true, keepAlive: true}, provider: "codex", model: "gpt-image-2"},
		{firstBytePath: firstBytePath{row: "18", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"` + compat + `","prompt":"a square","stream":true}`, stream: true, keepAlive: true}, provider: "openai-compatibility", model: compat, modelType: registry.OpenAIImageModelType},
		{firstBytePath: firstBytePath{row: "19", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"` + compat + `","prompt":"a square"}`, keepAlive: true}, provider: "openai-compatibility", model: compat, modelType: registry.OpenAIImageModelType},
		{firstBytePath: firstBytePath{row: "19", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"grok-imagine-image","prompt":"a square"}`, keepAlive: true}, provider: "xai", model: "grok-imagine-image"},
		{firstBytePath: firstBytePath{row: "20", method: http.MethodPost, path: "/v1/images/generations", body: `{"model":"grok-imagine-image","prompt":"a square","stream":true}`, keepAlive: true}, provider: "xai", model: "grok-imagine-image"},
		{firstBytePath: firstBytePath{row: "23", method: http.MethodPost, path: "/v1/videos", body: `{"model":"grok-imagine-video","prompt":"a video"}`, keepAlive: true}, provider: "xai", model: "grok-imagine-video"},
		{firstBytePath: firstBytePath{row: "23", method: http.MethodGet, path: "/v1/videos/matrix-request", keepAlive: true, bound: true}, provider: "xai", model: "grok-imagine-video"},
		{firstBytePath: firstBytePath{row: "24", method: http.MethodPost, path: "/openai/v1/videos", body: `{"model":"sora-2","prompt":"a video"}`, keepAlive: true}, provider: "xai", model: "grok-imagine-video"},
		{firstBytePath: firstBytePath{row: "25", method: http.MethodGet, path: "/openai/v1/videos/matrix-video", keepAlive: true, bound: true}, provider: "xai", model: "grok-imagine-video"},
		{firstBytePath: firstBytePath{row: "26", method: http.MethodGet, path: "/openai/v1/videos/matrix-video/content", keepAlive: true, bound: true}, provider: "xai", model: "grok-imagine-video", download: true},
		{firstBytePath: firstBytePath{row: "27", method: http.MethodPost, path: "/v1/audio/speech", body: `{"model":"grok-tts","input":"hello"}`}, provider: "xai", model: "grok-tts"},
		{firstBytePath: firstBytePath{row: "27", method: http.MethodPost, path: "/v1/tts", body: `{"model":"grok-tts","input":"hello"}`}, provider: "xai", model: "grok-tts"},
	}
}

func runMediaFirstByteCell(t *testing.T, path firstByteMediaPath, scenario firstByteScenario, configuration firstByteConfiguration) {
	t.Helper()
	cfg := firstByteConfig(configuration)
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.SetRetryConfig(0, 0, 3)
	recorder := newFirstCommitRecorder()

	// The raw download of a video: the second fetch of the same bound credential's answer.
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(w, "video-bytes-"+strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".mp4"))
	}))
	t.Cleanup(download.Close)

	credentials := []firstByteCredential{}
	behaviour := map[string]firstByteBehaviour{}
	for i, label := range []string{"first", "second"} {
		id := "matrix-" + label + "-" + path.provider
		host := label + "-" + path.provider + ".example.test"
		a := &auth.Auth{ID: id, Provider: path.provider, Status: auth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-" + label, "base_url": "https://" + host + "/v1", "priority": fmt.Sprint(10 - 10*i)}}
		credentials = append(credentials, firstByteCredential{auth: a, statement: internallogging.AuthStatement{Kind: "apikey", Provider: path.provider, Host: host}, marker: "served-by-" + id})
		behaviour[id] = scenario.behaviour[i]
	}
	executor := &firstByteMediaExecutor{provider: path.provider, behaviour: behaviour, contentURL: download.URL}
	if configuration.keepAlive && path.keepAlive {
		executor.flushed = recorder.flushed
	}
	manager.RegisterExecutor(executor)
	for _, credential := range credentials {
		if _, err := manager.Register(context.Background(), credential.auth); err != nil {
			t.Fatalf("register: %v", err)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.auth.ID, credential.auth.Provider, []*registry.ModelInfo{{ID: path.model, Type: path.modelType, OwnedBy: path.provider, Created: 1700000000}})
		manager.RefreshSchedulerEntry(credential.auth.ID)
		id := credential.auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	var body io.Reader
	if path.body != "" {
		body = strings.NewReader(path.body)
	}
	req := httptest.NewRequest(path.method, path.path, body)
	req.Header.Set("Authorization", "Bearer test-key")
	if path.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	server.engine.ServeHTTP(recorder, req)
	if path.download && scenario.answering >= 0 {
		// The body is the download: it carries the marker in its path-derived bytes.
		for i := range credentials {
			credentials[i].marker = "video-bytes-" + credentials[i].marker
		}
	}
	checkFirstByteCell(t, recorder, credentials, scenario, configuration)
}

// firstByteChatExecutor emits chat-completions chunks as they are, for the branches of the legacy completions stream
// that the Claude translation never takes: a first chunk that converts to text, and a first chunk that is a terminal
// error.
type firstByteChatExecutor struct{ first string }

func (e *firstByteChatExecutor) Identifier() string { return "openai-compatibility" }

func (e *firstByteChatExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(strings.ReplaceAll(e.first, "MARKER", "served-by-"+a.ID))}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *firstByteChatExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not used")
}

func (e *firstByteChatExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *firstByteChatExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not used")
}

func (e *firstByteChatExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

// Row 7, the two first-chunk branches of the legacy completions stream (the first byte written at the conversion of
// the first chunk, and at a first chunk that is a terminal error), on the default configuration.
func TestLegacyCompletionsStreamNamesTheCredentialAtEachFirstWrite(t *testing.T) {
	for _, test := range []struct{ name, first string }{
		{name: "first chunk converts to text", first: `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"MARKER"}}]}`},
		{name: "first chunk is a terminal error", first: `{"error":{"message":"MARKER","type":"server_error"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &proxyconfig.Config{}
			cfg.APIKeys = []string{"test-key"}
			server := newTestServerWithConfig(t, cfg)
			credential := &auth.Auth{ID: "matrix-chat-key", Provider: "openai-compatibility", Status: auth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "made-up", "base_url": "https://chat-gateway.example.test/v1"}}
			server.handlers.AuthManager.RegisterExecutor(&firstByteChatExecutor{first: test.first})
			if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "matrix-chat-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
			req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"matrix-chat-model","stream":true,"prompt":"hi"}`))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			recorder := newFirstCommitRecorder()
			server.engine.ServeHTTP(recorder, req)
			credentials := []firstByteCredential{{auth: credential, statement: internallogging.AuthStatement{Kind: "apikey", Provider: "openai-compatibility", Host: "chat-gateway.example.test"}, marker: "served-by-matrix-chat-key"}}
			if named := assertFirstCommit(t, recorder, credentials, http.StatusOK, true); named == nil || !strings.HasPrefix(string(recorder.nextBytes), "data: ") || !strings.Contains(string(recorder.nextBytes), "served-by-matrix-chat-key") {
				t.Fatalf("first write %q named %v; body %q", recorder.nextBytes, named, recorder.Body.String())
			}
		})
	}
}
