package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: finding 12 of the review of the rule's first build, its real-socket case, against the rule as built now
// (design section 15). A turn on an open Codex WebSocket is pinned to the connection's login, so the rule never moves
// it (K5: its list is that login alone), and it is a use of the conversation like any other. A new connection's first
// turn is not pinned: it returns only if the conversation has been unused for cold-after, its pinned turns included.
// Every account and key is invented; the executor is a stand-in in this process; frames go over loopback only.
func TestProvimuxSessionReturnWebsocket(t *testing.T) {
	const model = "provimux-sr-return-ws-model"
	const cold, ttl = 2 * time.Hour, 4 * time.Hour
	gin.SetMode(gin.TestMode)
	selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.FillFirstSelector{}, TTL: ttl})
	t.Cleanup(selector.Stop)
	m := coreauth.NewManager(nil, selector, nil)
	m.RegisterExecutor(&srSocketExecutor{})
	coreauth.ProvimuxSetSessionReturn(m, cold)
	t.Cleanup(func() { coreauth.ProvimuxSetSessionReturn(nil, 0) })
	high, low := "sr-return-ws-high", "sr-return-ws-low"
	for _, a := range []struct{ id, priority string }{{high, "10"}, {low, "5"}} {
		if _, err := m.Register(coreauth.WithSkipPersist(t.Context()), &coreauth.Auth{ID: a.id, Provider: "codex", Status: coreauth.StatusActive,
			Attributes: map[string]string{"api_key": "synthetic", "websockets": "true", "priority": a.priority}}); err != nil {
			t.Fatal(err)
		}
		id := a.id
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model, Object: "model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	recorded := make(chan struct{}, 8)
	t.Cleanup(m.AddHook(srRecorded(func(res coreauth.Result) {
		if len(res.Options.OriginalRequest) > 0 {
			recorded <- struct{}{}
		}
	})))
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m))
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("userApiKey", "sk-sr-return-ws"); c.Next() })
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	header := http.Header{"Session-Id": {"ws-return"}}
	dial := func() *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", header)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	turn := func(conn *websocket.Conn) string {
		t.Helper()
		request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"role":"user","type":"message","content":"hi"}]}`, model)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second)) // only a hung test meets it: every frame is from this process
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			switch ojson.GetBytesNoCopy(frame, "type").String() {
			case "error":
				t.Fatal(string(frame))
			case "response.completed":
				select {
				case <-recorded:
				case <-time.After(30 * time.Second): // only a hung test waits this long
					t.Fatal("the turn's result was never recorded")
				}
				return ojson.GetBytesNoCopy(frame, "response.id").String()
			}
		}
	}
	returned := func() int64 { return coreauth.ProvimuxSessionReturnStateOf(m, time.Now()).Counts.Returned }

	hour := time.Hour
	m.MarkResult(context.Background(), coreauth.Result{AuthID: high, Provider: "codex", Model: model, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic"}, RetryAfter: &hour})
	socket := dial()
	if got := turn(socket); got != "resp-"+low {
		t.Fatalf("first turn answered by %s", got)
	}
	m.MarkResult(context.Background(), coreauth.Result{AuthID: high, Provider: "codex", Model: model, Success: true}) // high is free again
	before := returned()

	coreauth.ProvimuxSessionReturnAgeForTest(m, cold+time.Hour)
	if got := turn(socket); got != "resp-"+low || returned() != before {
		t.Fatalf("a pinned turn on the open connection, unused for cold-after: answered by %s, counted %d; want the low login, nothing dropped", got, returned()-before)
	}
	_ = socket.Close()
	if got := turn(dial()); got != "resp-"+low || returned() != before {
		t.Fatalf("a new connection right after the pinned turn: answered by %s; want the low login (used within cold-after)", got)
	}
	coreauth.ProvimuxSessionReturnAgeForTest(m, cold+time.Hour)
	if got := turn(dial()); got != "resp-"+high || returned()-before != 1 {
		t.Fatalf("a new connection, unused for cold-after: answered by %s, counted %d; want the high login, once", got, returned()-before)
	}
}
