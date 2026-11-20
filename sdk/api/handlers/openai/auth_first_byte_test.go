package openai

import (
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
)

// The rows of the first-byte matrix that the proxy's engine cannot reach (internal/api holds the others): the image
// routes over a Responses stream (rows 21, 22: every image model a request may name is taken by an earlier branch
// of ImagesGenerations and ImagesEdits, so they are called directly here) and the websocket turn (row 34).

// firstCommitWriter records, at the first WriteHeader it receives, the header snapshot and status, and then the next
// call: a Write with its bytes, or a Flush.
type firstCommitWriter struct {
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

func (r *firstCommitWriter) commit(status int) {
	if !r.committed {
		r.committed, r.snapshot, r.status = true, r.ResponseRecorder.Header().Clone(), status
	}
}

func (r *firstCommitWriter) WriteHeader(status int) {
	r.mu.Lock()
	r.commit(status)
	r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(status)
}

func (r *firstCommitWriter) Write(data []byte) (int, error) {
	r.mu.Lock()
	r.commit(http.StatusOK)
	if !r.nextKnown {
		r.nextKnown, r.nextBytes = true, append([]byte(nil), data...)
	}
	r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

func (r *firstCommitWriter) WriteString(data string) (int, error) { return r.Write([]byte(data)) }

func (r *firstCommitWriter) Flush() {
	r.mu.Lock()
	r.commit(http.StatusOK)
	if !r.nextKnown {
		r.nextKnown, r.nextFlush = true, true
	}
	r.mu.Unlock()
	r.ResponseRecorder.Flush()
	r.flushOnce.Do(func() { close(r.flushed) })
}

type firstByteImagesExecutor struct {
	behaviour map[string]string
	flushed   <-chan struct{}
}

func (e *firstByteImagesExecutor) Identifier() string { return "codex" }

func (e *firstByteImagesExecutor) wait(ctx context.Context) {
	if e.flushed == nil {
		time.Sleep(100 * time.Millisecond)
		return
	}
	select {
	case <-e.flushed:
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
}

func (e *firstByteImagesExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	switch e.behaviour[auth.ID] {
	case "fail":
		e.wait(ctx)
		return nil, &coreauth.Error{Code: "unavailable", Message: "fixture upstream unavailable", HTTPStatus: http.StatusServiceUnavailable}
	case "hold":
		go func() {
			defer close(chunks)
			e.wait(ctx)
			chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{Code: "unavailable", Message: "fixture stream interrupted", HTTPStatus: http.StatusServiceUnavailable}}
		}()
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if e.flushed != nil {
		e.wait(ctx)
	}
	marker := "served-by-" + auth.ID
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"created_at\":1,\"output\":[{\"type\":\"image_generation_call\",\"result\":%q,\"revised_prompt\":%q}]}}\n\n", marker, marker))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *firstByteImagesExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not used")
}

func (e *firstByteImagesExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *firstByteImagesExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not used")
}

func (e *firstByteImagesExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

var firstByteImagesStatements = map[string]logging.AuthStatement{
	"matrix-first-codex":  {Kind: "apikey", Provider: "codex", Host: "first-codex.example.test"},
	"matrix-second-codex": {Kind: "apikey", Provider: "codex", Host: "second-codex.example.test"},
}

func TestImageRoutesOverAResponsesStreamNameACredentialOnlyAtItsOwnFirstByte(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, row := range []string{"21 collectImagesFromResponses", "22 streamImagesFromResponses"} {
		for _, scenario := range []struct {
			name      string
			behaviour [2]string
			answering string
		}{
			{name: "first answers", behaviour: [2]string{"answer", "answer"}, answering: "matrix-first-codex"},
			{name: "first fails upstream, second answers", behaviour: [2]string{"fail", "answer"}, answering: "matrix-second-codex"},
			{name: "first stream held then fails, second answers", behaviour: [2]string{"hold", "answer"}, answering: "matrix-second-codex"},
			{name: "all fail", behaviour: [2]string{"fail", "fail"}},
		} {
			for _, keepAlive := range []bool{false, true} {
				t.Run(fmt.Sprintf("row %s/%s/keep-alive=%t", row, scenario.name, keepAlive), func(t *testing.T) {
					cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1}}
					if keepAlive {
						cfg.NonStreamKeepAliveInterval = 1
						cfg.Streaming.KeepAliveSeconds = 1
						cfg.Streaming.SendKeepAliveBeforeFT = true
					}
					recorder := &firstCommitWriter{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
					executor := &firstByteImagesExecutor{behaviour: map[string]string{"matrix-first-codex": scenario.behaviour[0], "matrix-second-codex": scenario.behaviour[1]}}
					if keepAlive {
						executor.flushed = recorder.flushed
					}
					manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
					manager.SetRetryConfig(0, 0, 3)
					manager.RegisterExecutor(executor)
					for i, id := range []string{"matrix-first-codex", "matrix-second-codex"} {
						credential := &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "made-up", "base_url": "https://" + firstByteImagesStatements[id].Host + "/v1", "priority": fmt.Sprint(10 - 10*i)}}
						if _, err := manager.Register(context.Background(), credential); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: defaultImagesMainModel}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					}
					h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
					router := gin.New()
					router.Use(logging.CPATraceIDMiddleware())
					request := buildImagesResponsesRequest("a square", nil, []byte(`{"type":"image_generation","action":"generate","model":"gpt-image-2"}`))
					router.POST("/matrix", func(c *gin.Context) {
						if strings.HasPrefix(row, "21") {
							h.collectImagesFromResponses(c, request, "b64_json")
						} else {
							h.streamImagesFromResponses(c, request, "b64_json", "image_generation")
						}
					})
					router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/matrix", nil))

					recorder.mu.Lock()
					defer recorder.mu.Unlock()
					body := recorder.Body.String()
					got := logging.AuthStatement{Kind: recorder.snapshot.Get(logging.CPAAuthKindHeader), Provider: recorder.snapshot.Get(logging.CPAAuthProviderHeader), Host: recorder.snapshot.Get(logging.CPAAuthHostHeader)}
					named := ""
					for id, statement := range firstByteImagesStatements {
						if got == statement {
							named = id
						}
					}
					if got != (logging.AuthStatement{}) {
						if named == "" {
							t.Fatalf("headers %+v: not one credential's whole statement", got)
						}
						if !recorder.nextKnown || recorder.nextFlush || string(recorder.nextBytes) == "\n" || strings.HasPrefix(string(recorder.nextBytes), ": keep-alive") || strings.HasPrefix(string(recorder.nextBytes), "event: keepalive") {
							t.Fatalf("%s committed by %q (flush %v)", named, recorder.nextBytes, recorder.nextFlush)
						}
						if recorder.status != http.StatusOK {
							t.Fatalf("%s committed with status %d", named, recorder.status)
						}
					}
					for id := range firstByteImagesStatements {
						has := strings.Contains(body, "served-by-"+id)
						if named != "" && has != (id == named) {
							t.Fatalf("headers name %s; body %q", named, body)
						}
						if !keepAlive && has && named != id {
							t.Fatalf("default configuration: body from %s, headers %+v", id, got)
						}
					}
					if scenario.answering == "" {
						if named != "" || strings.Contains(body, "served-by-") {
							t.Fatalf("all failed: headers %+v body %q", got, body)
						}
						return
					}
					if !strings.Contains(body, "served-by-"+scenario.answering) {
						t.Fatalf("fixture: body %q lacks %s", body, scenario.answering)
					}
					if !keepAlive && named != scenario.answering {
						t.Fatalf("default configuration: %s answered, headers %+v", scenario.answering, got)
					}
				})
			}
		}
	}
}

type firstByteWebsocketExecutor struct {
	websocketCreatedExecutor
	provider string
	fail     map[string]bool
}

func (e firstByteWebsocketExecutor) Identifier() string { return e.provider }

func (e firstByteWebsocketExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if e.fail[auth.ID] {
		coreexecutor.MarkUpstreamAttempt(ctx)
		return nil, &coreauth.Error{Code: "unavailable", Message: "fixture upstream unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	return e.websocketCreatedExecutor.ExecuteStream(ctx, auth, req, opts)
}

// Row 34: on a websocket turn the statement is put on the upstream's own response.created as it is forwarded, and on
// no other frame: the answered credential's, after a failed first credential the second's, and none when all fail.
func TestWebsocketTurnNamesACredentialOnlyOnItsOwnResponseCreated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "first-byte-websocket-model"
	statements := map[string]logging.AuthStatement{
		"ws-first-key":    {Kind: "apikey", Provider: "claude", Host: "first-gateway.example.test"},
		"ws-second-login": {Kind: "oauth", Provider: "devin"},
	}
	for _, scenario := range []struct {
		name      string
		fail      map[string]bool
		answering string
	}{
		{name: "first answers", answering: "ws-first-key"},
		{name: "first fails upstream, second answers", fail: map[string]bool{"ws-first-key": true}, answering: "ws-second-login"},
		{name: "all fail", fail: map[string]bool{"ws-first-key": true, "ws-second-login": true}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			credentials := []*coreauth.Auth{
				{ID: "ws-first-key", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "made-up", "base_url": "https://first-gateway.example.test/v1", "priority": "10"}},
				{ID: "ws-second-login", Provider: "devin", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "priority": "0"}},
			}
			for _, credential := range credentials {
				manager.RegisterExecutor(firstByteWebsocketExecutor{provider: credential.Provider, fail: scenario.fail})
				if _, err := manager.Register(context.Background(), credential); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
				id := credential.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware())
			router.GET("/v1/responses", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"hi"}]}`, model))); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
			var created []byte
			for done := false; !done; {
				_, frame, errRead := conn.ReadMessage()
				if errRead != nil && scenario.answering == "" {
					break // when every credential failed the turn ends with the connection: no frame named anything
				}
				if errRead != nil {
					t.Fatalf("read: %v", errRead)
				}
				eventType := ojson.GetBytesNoCopy(frame, "type").String()
				fields := logging.AuthStatement{Kind: ojson.GetBytesNoCopy(frame, "cpa_auth_kind").String(), Provider: ojson.GetBytesNoCopy(frame, "cpa_auth_provider").String(), Host: ojson.GetBytesNoCopy(frame, "cpa_auth_host").String()}
				switch eventType {
				case "response.created":
					created = frame
					want := statements[scenario.answering]
					if !strings.Contains(string(frame), "resp-"+scenario.answering) || fields != want {
						t.Fatalf("response.created %s carries %+v, want %s's %+v", frame, fields, scenario.answering, want)
					}
				case "response.completed", "error":
					done = true
					fallthrough
				default:
					if fields != (logging.AuthStatement{}) {
						t.Fatalf("%s frame carries %+v: %s", eventType, fields, frame)
					}
				}
			}
			if (created != nil) != (scenario.answering != "") {
				t.Fatalf("response.created %q for %s", created, scenario.name)
			}
		})
	}
}
