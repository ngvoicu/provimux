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
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: finding 12 of the review of session-route G1, through a real loopback Responses WebSocket. A conversation
// with a turn on such a connection is answered websocket_may_hold whatever names it; and the words of the contract
// hold, as upstream behaves today: a release holds only if the connection is closed before its next turn; a turn on
// the open connection binds the conversation to the old login again, closing afterwards does not undo that, and
// releasing again, then closing, does. Every account and key is invented; the executor is a stand-in in this process.

type srSocketExecutor struct{ provimuxWebsocketExecutor }

func (*srSocketExecutor) Identifier() string { return "codex" }

func TestProvimuxSessionRouteWebsocket(t *testing.T) {
	const model = "provimux-sr-ws-model"
	for _, naming := range []struct {
		name     string
		header   http.Header
		body     string // fields added to response.create
		identity string // the show's identity
	}{
		{name: "Session-Id", header: http.Header{"Session-Id": {"ws-1"}}},
		{name: "Thread-Id", header: http.Header{"Thread-Id": {"ws-2"}}},
		{name: "X-Claude-Code-Session-Id", header: http.Header{"X-Claude-Code-Session-Id": {"ws-3"}}},
		{name: "X-Http-Session-Id", header: http.Header{"X-Http-Session-Id": {"ws-4"}}},
		{name: "X-Session-ID", header: http.Header{"X-Session-Id": {"ws-5"}}},
		{name: "X-Session-Affinity", header: http.Header{"X-Session-Affinity": {"ws-6"}}},
		{name: "X-Slot-Session-Id", header: http.Header{"X-Slot-Session-Id": {"ws-7"}}},
		{name: "X-Task-ID", header: http.Header{"X-Task-Id": {"ws-8"}}},
		{name: "X-Conversation-Id", header: http.Header{"X-Conversation-Id": {"ws-9"}}},
		{name: "X-Thread-Id", header: http.Header{"X-Thread-Id": {"ws-10"}}},
		{name: "X-Client-Request-Id", header: http.Header{"X-Client-Request-Id": {"ws-11"}}},
		{name: "prompt_cache_key", body: `,"prompt_cache_key":"ws-12"`, identity: `{"prompt_cache_key":"ws-12"}`},
	} {
		t.Run(naming.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.FillFirstSelector{}, TTL: time.Hour})
			t.Cleanup(selector.Stop)
			m := coreauth.NewManager(nil, selector, nil)
			m.RegisterExecutor(&srSocketExecutor{})
			high, low := "sr-ws-high-"+naming.name, "sr-ws-low-"+naming.name
			for _, a := range []struct{ id, priority string }{{high, "10"}, {low, "5"}} {
				if _, err := m.Register(coreauth.WithSkipPersist(t.Context()), &coreauth.Auth{ID: a.id, Provider: "codex", Status: coreauth.StatusActive,
					Attributes: map[string]string{"api_key": "synthetic", "websockets": "true", "priority": a.priority}}); err != nil {
					t.Fatal(err)
				}
				id := a.id
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model, Object: "model"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			book := coreauth.NewProvimuxSessionBook(m)
			book.Subscribe()
			t.Cleanup(book.Unsubscribe)
			// A turn's result is recorded after its last frame has gone to the client: each turn waits for its
			// result. The hooks run in the order they were added, so when this one runs the book has seen it.
			recorded := make(chan struct{}, 8)
			t.Cleanup(m.AddHook(srRecorded(func(res coreauth.Result) {
				if len(res.Options.OriginalRequest) > 0 {
					recorded <- struct{}{}
				}
			})))
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m))
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("userApiKey", "sk-sr-ws"); c.Next() })
			router.GET("/v1/responses", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			scope := session.CallerScope("sk-sr-ws")

			dial := func() *websocket.Conn {
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", naming.header)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				return conn
			}
			turn := func(conn *websocket.Conn) string {
				t.Helper()
				request := fmt.Sprintf(`{"type":"response.create","model":%q%s,"input":[{"role":"user","type":"message","content":"hi"}]}`, model, naming.body)
				if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
					t.Fatal(err)
				}
				// A deadline that only a hung test meets: every frame here comes from this process.
				_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
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
			show := func() coreauth.ProvimuxSessionRoute {
				return book.Show(scope, naming.header, []byte(naming.identity), model)
			}
			release := func() {
				t.Helper()
				if released, _ := book.Release(scope, naming.header, []byte(naming.identity), model, ""); !released.Released || !released.WebsocketMayHold {
					t.Fatalf("release: %+v", released)
				}
			}

			hour := time.Hour
			m.MarkResult(context.Background(), coreauth.Result{AuthID: high, Provider: "codex", Model: model, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic"}, RetryAfter: &hour})
			socket := dial()
			if got := turn(socket); got != "resp-"+low {
				t.Fatalf("first turn answered by %s", got)
			}
			m.MarkResult(context.Background(), coreauth.Result{AuthID: high, Provider: "codex", Model: model, Success: true}) // high is free again
			if s := show(); s.Conversation != "bound" || !s.WebsocketMayHold {
				t.Fatalf("named by %s: %+v", naming.name, s)
			}

			// Released, then a turn on the open connection: bound to the old login again.
			release()
			if got := turn(socket); got != "resp-"+low {
				t.Fatalf("turn on the open connection answered by %s", got)
			}
			if s := show(); s.Conversation != "bound" || s.Bound == nil || s.Bound.Route.Priority != 5 {
				t.Fatalf("after the pinned turn: %+v", s)
			}
			// Closing afterwards does not undo it: the next connection still goes to the old login.
			_ = socket.Close()
			socket = dial()
			if got := turn(socket); got != "resp-"+low {
				t.Fatalf("next connection answered by %s", got)
			}
			// Release again, then close: the next connection goes to the first route.
			release()
			_ = socket.Close()
			if got := turn(dial()); got != "resp-"+high {
				t.Fatalf("after release then close, answered by %s", got)
			}
		})
	}
}

// srRecorded is a result hook that calls its function for every result.
type srRecorded func(coreauth.Result)

func (srRecorded) OnAuthRegistered(context.Context, *coreauth.Auth)  {}
func (srRecorded) OnAuthUpdated(context.Context, *coreauth.Auth)     {}
func (f srRecorded) OnResult(_ context.Context, res coreauth.Result) { f(res) }
