package provimux

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: the answers of the answered-by fixture (answered-by.md 8.4). Like answered_by_fixture_test.go, this file
// uses only what the commit before answered-by has. An upstream that is itself a gateway sends its own X-Provimux-*
// headers (put in the header map before the answer, as a foreign writer would) and provimux_* fields on its
// response.created. Every key and host is invented.

const answeredAnswerModel = "provimux-answered-model"

// answeredForeignHeaders are what the upstream gateway's answer put in the header map.
var answeredForeignHeaders = [][2]string{
	{"X-Provimux-Account", "a.zzzzzzzzzzzzzzzzzzzz"}, {"X-Provimux-Model", "foreign-model"}, {"X-Provimux-Entry", "foreign/model"},
}

// answeredForeignFields are the upstream gateway's own fields on its response.created.
const answeredForeignFields = `"cpa_auth_kind":"oauth","provimux_account":"a.zzzzzzzzzzzzzzzzzzzz","provimux_model":"foreign-model","provimux_entry":"foreign/model"`

// answeredByExecutor is the credentials' executor: it answers every format, from an upstream exchange.
type answeredByExecutor struct{ coreauth.ProviderExecutor }

func (answeredByExecutor) Identifier() string { return "answered-fixture" }

func (answeredByExecutor) Execute(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return coreexecutor.Response{Payload: []byte(`{"id":"chatcmpl-fixture","object":"chat.completion","created":1700000000,"model":"` + answeredAnswerModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}],"candidates":[{"content":{"role":"model","parts":[{"text":"fixture answer"}]},"finishReason":"STOP"}]}`)}, nil
}

func (answeredByExecutor) CountTokens(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	return coreexecutor.Response{Payload: []byte(`{"totalTokens":7}`)}, nil
}

func (answeredByExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	var frames []string
	switch {
	case string(opts.SourceFormat) == "openai-response" || string(opts.ResponseFormat) == "openai-response":
		frames = []string{
			`data: {"type":"response.created","response":{"id":"resp-fixture","output":[]},` + answeredForeignFields + `}` + "\n\n",
			`data: {"type":"response.completed","response":{"id":"resp-fixture","status":"completed","output":[]}}` + "\n\n",
		}
	default:
		frames = []string{`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1700000000,"model":"` + answeredAnswerModel + `","choices":[{"index":0,"delta":{"content":"fixture answer"},"finish_reason":"stop"}]}`}
	}
	chunks := make(chan coreexecutor.StreamChunk, len(frames))
	for _, frame := range frames {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(frame)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

// answeredByAnswerKey is the fixture's credential: a key with a base URL.
func answeredByAnswerKey() *coreauth.Auth {
	return &coreauth.Auth{ID: "answered-fixture-key", Provider: "answered-fixture", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "sk-fixture-answer", "base_url": "https://answers.example.test/v1"}}
}

// answeredByAnswers serves the fixture's requests and returns each answer written out: status, every header in name
// order, and the body; and the frames of one Responses WebSocket turn.
func answeredByAnswers(t *testing.T) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(answeredByExecutor{})
	key := answeredByAnswerKey()
	if _, errRegister := manager.Register(context.Background(), key); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(key.ID, key.Provider, []*registry.ModelInfo{{ID: answeredAnswerModel, OwnedBy: "provimux-test", Created: 1700000000}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(key.ID) })

	handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	router := gin.New()
	router.Use(logging.CPATraceIDMiddleware())
	router.Use(func(c *gin.Context) {
		for _, header := range answeredForeignHeaders {
			c.Header(header[0], header[1])
		}
		c.Next()
	})
	router.POST("/v1/chat/completions", openai.NewOpenAIAPIHandler(handler).ChatCompletions)
	router.POST("/v1beta/models/*action", gemini.NewGeminiAPIHandler(handler).GeminiHandler)
	router.GET("/v1/responses", openai.NewOpenAIResponsesAPIHandler(handler).ResponsesWebsocket)

	var out strings.Builder
	for _, request := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"` + answeredAnswerModel + `","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"` + answeredAnswerModel + `","messages":[{"role":"user","content":"hi"}],"stream":true}`},
		{"/v1beta/models/" + answeredAnswerModel + ":countTokens", `{"contents":[{"parts":[{"text":"hi"}]}]}`},
		{"/v1/chat/completions", `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`},
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(request.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		fmt.Fprintf(&out, "POST %s %s\n%d\n", request.path, request.body, recorder.Code)
		names := make([]string, 0, len(recorder.Header()))
		for name := range recorder.Header() {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&out, "%s: %q\n", name, recorder.Header()[name])
		}
		fmt.Fprintf(&out, "%s\n\n", recorder.Body.String())
	}

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"`+answeredAnswerModel+`","input":[{"type":"message","role":"user","content":"hi"}]}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	out.WriteString("WEBSOCKET\n")
	for {
		_, frame, errRead := conn.ReadMessage()
		if errRead != nil {
			if errRead == io.EOF {
				break
			}
			t.Fatalf("websocket: %v", errRead)
		}
		out.Write(frame)
		out.WriteByte('\n')
		if eventType := ojson.GetBytes(frame, "type").String(); eventType == "response.completed" || eventType == "error" {
			break
		}
	}
	return []byte(out.String())
}
