package openai

import (
	"context"
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

// Provimux: the account marker on the Responses WebSocket (answered-by.md 5.2). Two keys of one kind and one host:
// cpa_auth_* is the same for both, provimux_account says which answered each turn. The upstream, itself a gateway,
// puts its own provimux_* fields on response.created: they never pass with the switch on, and pass untouched with it
// off. Keys and hosts are invented.

var provimuxWebsocketTestKey = []byte("provimux-ws-test-marker-key-32by")

// provimuxForeignCreated are the statement fields an upstream gateway puts on its own response.created.
const provimuxForeignCreated = `"cpa_auth_kind":"oauth","provimux_account":"a.zzzzzzzzzzzzzzzzzzzz","provimux_model":"foreign-model","provimux_model_basis":"vendor","provimux_model_sent":"foreign-model","provimux_effort":"high","provimux_entry":"foreign/model"`

// provimuxWebsocketExecutor answers a turn as its upstream gateway would, or fails the credentials named in fail.
type provimuxWebsocketExecutor struct {
	websocketCreatedExecutor
	mu   sync.Mutex
	fail map[string]bool
}

func (e *provimuxWebsocketExecutor) Identifier() string { return "claude" }

func (e *provimuxWebsocketExecutor) failing(ids ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fail = map[string]bool{}
	for _, id := range ids {
		e.fail[id] = true
	}
}

func (e *provimuxWebsocketExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	e.mu.Lock()
	fail := e.fail[auth.ID]
	e.mu.Unlock()
	if fail {
		return nil, &coreauth.Error{Code: "unavailable", Message: "fixture upstream unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"resp-%s"},%s}`, auth.ID, provimuxForeignCreated))}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s","output":[]},"provimux_account":"a.zzzzzzzzzzzzzzzzzzzz"}`, auth.ID))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestProvimuxWebsocketTurnsNameTheirAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "provimux-websocket-model"
	coreauth.SetProvimuxMarkerKey(provimuxWebsocketTestKey)
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		coreauth.SetProvimuxMarkerKey(nil)
	})

	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	executor := &provimuxWebsocketExecutor{}
	manager.RegisterExecutor(executor)
	markers := map[string]string{}
	for _, credential := range []*coreauth.Auth{
		{ID: "ws-first-key", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-first", "base_url": "https://shared-gateway.example.test/v1", "priority": "10"}},
		{ID: "ws-second-key", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-second", "base_url": "https://shared-gateway.example.test/v1", "priority": "0"}},
	} {
		markers[credential.ID] = credential.ProvimuxAccountMarker()
		if _, err := manager.Register(context.Background(), credential); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
		id := credential.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	if markers["ws-first-key"] == "" || markers["ws-first-key"] == markers["ws-second-key"] {
		t.Fatalf("fixture: markers %v", markers)
	}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.Use(logging.CPATraceIDMiddleware())
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn, upgrade, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if values := upgrade.Header.Values(logging.ProvimuxAccountHeader); len(values) != 0 {
		t.Fatalf("the upgrade names an account: %v", values)
	}

	statement := logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "shared-gateway.example.test"}
	for turn, test := range []struct {
		name      string
		fail      []string
		answering string
		off       bool
	}{
		{name: "the first key answers", answering: "ws-first-key"},
		{name: "the first fails inside the turn, the second answers", fail: []string{"ws-first-key"}, answering: "ws-second-key"},
		{name: "the next turn stays with the second", answering: "ws-second-key"},
		{name: "the switch off: the upstream's own fields pass untouched", answering: "ws-second-key", off: true},
	} {
		executor.failing(test.fail...)
		logging.SetProvimuxAnsweredBy(!test.off)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"hi"}]}`, model))); errWrite != nil {
			t.Fatal(errWrite)
		}
		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		created := false
		for done := false; !done; {
			_, frame, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("turn %d (%s): %v", turn, test.name, errRead)
			}
			eventType := ojson.GetBytesNoCopy(frame, "type").String()
			switch eventType {
			case "response.created":
				created = true
				if !strings.Contains(string(frame), "resp-"+test.answering) {
					t.Fatalf("turn %d: %s answered, want %s", turn, frame, test.answering)
				}
				if test.off {
					if !strings.Contains(string(frame), `"provimux_account":"a.zzzzzzzzzzzzzzzzzzzz","provimux_model":"foreign-model"`) || strings.Contains(string(frame), markers[test.answering]) {
						t.Fatalf("turn %d, switch off: the upstream's fields are not as it sent them: %s", turn, frame)
					}
					continue
				}
				got := logging.AuthStatement{Kind: ojson.GetBytesNoCopy(frame, "cpa_auth_kind").String(), Provider: ojson.GetBytesNoCopy(frame, "cpa_auth_provider").String(), Host: ojson.GetBytesNoCopy(frame, "cpa_auth_host").String()}
				if got != statement || ojson.GetBytesNoCopy(frame, "provimux_account").String() != markers[test.answering] {
					t.Fatalf("turn %d: response.created %s, want %+v and %s's marker %s", turn, frame, statement, test.answering, markers[test.answering])
				}
				for _, name := range logging.ProvimuxFieldNames()[1:] {
					if ojson.GetBytesNoCopy(frame, name.Field).Exists() {
						t.Fatalf("turn %d: the upstream's %s passes: %s", turn, name.Field, frame)
					}
				}
				if !strings.HasSuffix(string(frame), `"cpa_auth_host":"shared-gateway.example.test","provimux_account":"`+markers[test.answering]+`"}`) {
					t.Fatalf("turn %d: provimux_account is not beside cpa_auth_*: %s", turn, frame)
				}
			case "response.completed":
				done = true
				// Only response.created is this gateway's to name: another event is forwarded as the upstream sent it.
				if !strings.Contains(string(frame), `"provimux_account":"a.zzzzzzzzzzzzzzzzzzzz"`) {
					t.Fatalf("turn %d: response.completed changed: %s", turn, frame)
				}
			case "error":
				t.Fatalf("turn %d: %s", turn, frame)
			}
		}
		if !created {
			t.Fatalf("turn %d: no response.created", turn)
		}
	}
}
