package openai

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: finding 1 of the second review of the rule, the reviewer's own case through the real Responses handler. A
// streamed answer of 90 seconds; the handler stops execution after delivering response.completed, before the stream's
// end, so upstream never refreshes the binding from the result. The rule reads the conversation's last message routed to an account:
// with cold-after 3m the follow-up stays on its account; with 1m it goes back, as intended (the message was sent 90
// seconds before). Every account and key is invented; the executor is a stand-in in this process.

type srReturnSSEExecutor struct {
	srSocketExecutor
	m *coreauth.Manager
}

func (e *srReturnSSEExecutor) Identifier() string { return "antigravity" }

func (e *srReturnSSEExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	stop := usage.SupportStreamDelivery(ctx)
	if strings.Contains(string(req.Payload), "long-turn") {
		coreauth.ProvimuxSessionReturnAgeForTest(e.m, 90*time.Second) // the answer takes 90 seconds
	}
	for _, event := range []string{"response.created", "response.completed"} {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"response\":{\"id\":%q,\"output\":[]}}\n\n", event, event, "resp-"+a.ID))}
	}
	go func() { <-ctx.Done(); close(chunks); stop() }()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestProvimuxSessionReturnLongAnswerSSE(t *testing.T) {
	const model = "provimux-sr-return-sse"
	for _, row := range []struct {
		cold     time.Duration
		returned bool
	}{{cold: 3 * time.Minute}, {cold: time.Minute, returned: true}} {
		t.Run(row.cold.String(), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.FillFirstSelector{}, TTL: time.Hour})
			t.Cleanup(selector.Stop)
			m := coreauth.NewManager(nil, selector, nil)
			m.RegisterExecutor(&srReturnSSEExecutor{m: m})
			coreauth.ProvimuxSetSessionReturn(m, row.cold)
			t.Cleanup(func() { coreauth.ProvimuxSetSessionReturn(nil, 0) })
			for i, id := range []string{"sr-sse-low", "sr-sse-high"} {
				if _, err := m.Register(coreauth.WithSkipPersist(t.Context()), &coreauth.Auth{Provider: "antigravity", ID: id, Status: coreauth.StatusActive,
					Attributes: map[string]string{"priority": fmt.Sprint(5 + 5*i), "api_key": "synthetic"}}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model, Object: "model"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m))
			router := gin.New()
			router.POST("/v1/responses", h.Responses)
			call := func(message string) string {
				t.Helper()
				recorder := httptest.NewRecorder()
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second) // only a hung test meets it
				defer cancel()
				req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"stream":true,"model":%q,"input":%q}`, model, message))).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Session-Affinity", "sse-conversation")
				router.ServeHTTP(recorder, req)
				if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "response.completed") {
					t.Fatalf("answer: %d %s", recorder.Code, recorder.Body.String())
				}
				return recorder.Body.String()
			}
			hour := time.Hour
			m.MarkResult(t.Context(), coreauth.Result{AuthID: "sr-sse-high", Provider: "antigravity", Model: model, Error: &coreauth.Error{HTTPStatus: 429, Message: "synthetic"}, RetryAfter: &hour})
			if out := call("long-turn"); !strings.Contains(out, "resp-sr-sse-low") {
				t.Fatalf("the long answer came from %s", out)
			}
			m.MarkResult(t.Context(), coreauth.Result{AuthID: "sr-sse-high", Provider: "antigravity", Model: model, Success: true}) // high is free again
			before := coreauth.ProvimuxSessionReturnStateOf(m, time.Now()).Counts.Returned
			out := call("follow-up")
			returned := coreauth.ProvimuxSessionReturnStateOf(m, time.Now()).Counts.Returned - before
			if row.returned && (!strings.Contains(out, "resp-sr-sse-high") || returned != 1) || !row.returned && (!strings.Contains(out, "resp-sr-sse-low") || returned != 0) {
				t.Fatalf("cold-after %s: follow-up %s, counted %d; want returned %v", row.cold, out, returned, row.returned)
			}
		})
	}
}
