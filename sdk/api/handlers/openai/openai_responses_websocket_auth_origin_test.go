package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// On response.created the provider and the host are sent only as plain printable ASCII, both or neither; the kind is
// sent either way.
func TestResponsesWebsocketAuthProviderSkipsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name         string
		provider     string
		host         string
		wantProvider string
		wantHost     string
	}{
		{name: "provider not valid UTF-8", provider: "openai-compatible-\xff", host: "api.example.test"},
		{name: "host not valid UTF-8", provider: "openai-compatible-lab", host: "api.example\xfe.test"},
		{name: "control character in the provider", provider: "openai-compatible-study\x7fteam\x01", host: "api.example.test"},
		{name: "internationalized host", provider: "claude", host: "bücher.example"},
		{name: "host without provider", host: "api.example.test"},
		{name: "login without a host", provider: "devin", wantProvider: "devin"},
		{name: "valid values", provider: "openai-compatible-lab", host: "api.example.test", wantProvider: "openai-compatible-lab", wantHost: "api.example.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := setResponsesWebsocketAuthStatement([]byte(`{"type":"response.created","response":{"id":"resp-1"}}`),
				func() logging.AuthStatement {
					return logging.AuthStatement{Kind: "apikey", Provider: test.provider, Host: test.host}
				})
			var event map[string]any
			if errDecode := json.Unmarshal(payload, &event); errDecode != nil {
				t.Fatalf("response.created is not valid JSON: %v (%q)", errDecode, payload)
			}
			if event["cpa_auth_kind"] != "apikey" {
				t.Fatalf("cpa_auth_kind = %v, want apikey: %s", event["cpa_auth_kind"], payload)
			}
			for field, want := range map[string]string{"cpa_auth_provider": test.wantProvider, "cpa_auth_host": test.wantHost} {
				got, present := event[field]
				if present != (want != "") || (present && got != want) {
					t.Fatalf("%s = %q (present %v), want %q: %s", field, got, present, want, payload)
				}
			}
		})
	}
}

// responsesWebsocketCreatedFor runs one turn through a real socket served by the credential and returns its
// response.created event.
func responsesWebsocketCreatedFor(t *testing.T, model string, executor coreauth.ProviderExecutor, credential *coreauth.Auth) []byte {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register %s: %v", credential.ID, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	router := gin.New()
	router.GET("/v1/responses/ws", NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)).ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1"}]}`, model)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	var created []byte
	for {
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read: %v", errRead)
		}
		switch ojson.GetBytesNoCopy(payload, "type").String() {
		case "response.created":
			created = payload
		case "response.completed":
			return created
		case "error":
			t.Fatalf("turn failed: %s", payload)
		}
	}
}

// Through a real socket: a key whose base URL has an internationalized host is sent as its kind alone, as in the
// response headers, so one credential is not two services depending on the transport.
func TestResponsesWebsocketSendsOnlyTheKindForAnInternationalizedHost(t *testing.T) {
	credential := &coreauth.Auth{
		ID:         "auth-origin-idn-key",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAPIKey: "made-up-key", "base_url": "https://bücher.example/v1"},
	}
	created := responsesWebsocketCreatedFor(t, "auth-origin-idn-model", websocketCreatedProviderExecutor{provider: "claude"}, credential)
	if got := ojson.GetBytesNoCopy(created, "cpa_auth_kind").String(); got != "apikey" {
		t.Fatalf("cpa_auth_kind = %q, want apikey: %s", got, created)
	}
	for _, field := range []string{"cpa_auth_provider", "cpa_auth_host"} {
		if value := ojson.GetBytesNoCopy(created, field); value.Exists() {
			t.Fatalf("%s = %q, want absent: %s", field, value.String(), created)
		}
	}
}

// The response headers and the response.created fields send one statement the same way, whatever its values.
func TestAuthStatementIsTheSameOnEveryTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, statement := range []logging.AuthStatement{
		{Kind: "apikey", Provider: "openai-compatible-lab", Host: "api.example.test"},
		{Kind: "oauth", Provider: "devin"},
		{Kind: "apikey", Provider: "claude", Host: "bücher.example"},
		{Kind: "apikey", Provider: "openai-compatible-büro", Host: "api.example.test"},
		{Kind: "apikey", Provider: "openai-compatible-study\x7fteam", Host: "api.example.test"},
		{Kind: "apikey", Provider: "openai-compatible-\xff"},
		{Kind: "oauth"},
		{},
	} {
		engine := gin.New()
		engine.Use(logging.CPATraceIDMiddleware())
		engine.GET("/", func(c *gin.Context) {
			logging.SetGinCPAAuthStatement(c, statement)
			c.Status(http.StatusOK)
		})
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		headers := logging.AuthStatement{
			Kind:     recorder.Header().Get(logging.CPAAuthKindHeader),
			Provider: recorder.Header().Get(logging.CPAAuthProviderHeader),
			Host:     recorder.Header().Get(logging.CPAAuthHostHeader),
		}
		created := setResponsesWebsocketAuthStatement([]byte(`{"type":"response.created"}`), func() logging.AuthStatement { return statement })
		var event map[string]string
		if errDecode := json.Unmarshal(created, &event); errDecode != nil {
			t.Fatalf("decode %q: %v", created, errDecode)
		}
		fields := logging.AuthStatement{Kind: event["cpa_auth_kind"], Provider: event["cpa_auth_provider"], Host: event["cpa_auth_host"]}
		if headers != fields || headers != statement.OnTheWire() {
			t.Fatalf("statement %+q: headers %+q, websocket fields %+q, want both %+q", statement, headers, fields, statement.OnTheWire())
		}
	}
}

// gatewayCreatedExecutor is an upstream that is itself a gateway: its response.created names its own credential.
type gatewayCreatedExecutor struct {
	websocketCreatedExecutor
	provider string
}

func (e gatewayCreatedExecutor) Identifier() string { return e.provider }

func (e gatewayCreatedExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	// Like a real executor's transport: the response comes from an upstream exchange.
	coreexecutor.MarkUpstreamAttempt(ctx)
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":"resp-%s"},"cpa_auth_kind":"apikey","cpa_auth_provider":"inner-gateway-provider","cpa_auth_host":"inner.gateway.example.test"}`, auth.ID))}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%s","output":[]}}`, auth.ID))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

// The three fields on response.created are this proxy's own: what an upstream gateway put there is removed, and the
// host is absent when this proxy's credential has none.
func TestResponsesWebsocketAuthOriginIsThisProxysOwn(t *testing.T) {
	for _, test := range []struct {
		name       string
		credential *coreauth.Auth
		want       map[string]string
	}{
		{
			name:       "login behind a gateway base URL",
			credential: &coreauth.Auth{ID: "auth-origin-gateway-login", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth", "base_url": "https://gateway.example.test/v1"}},
			want:       map[string]string{"cpa_auth_kind": "oauth", "cpa_auth_provider": "codex"},
		},
		{
			name:       "key with its own host",
			credential: &coreauth.Auth{ID: "auth-origin-gateway-key", Provider: "openai-compatible-lab", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributeAPIKey: "made-up-key", "base_url": "https://api.example.test/v1"}},
			want:       map[string]string{"cpa_auth_kind": "apikey", "cpa_auth_provider": "openai-compatible-lab", "cpa_auth_host": "api.example.test"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			created := responsesWebsocketCreatedFor(t, "auth-origin-gateway-model-"+test.credential.ID, gatewayCreatedExecutor{provider: test.credential.Provider}, test.credential)
			for _, field := range []string{"cpa_auth_kind", "cpa_auth_provider", "cpa_auth_host"} {
				value := ojson.GetBytesNoCopy(created, field)
				want, named := test.want[field]
				if value.Exists() != named || value.String() != want {
					t.Fatalf("%s = %q (present %v), want %q (present %v): %s", field, value.String(), value.Exists(), want, named, created)
				}
			}
			if strings.Count(string(created), "cpa_auth_") != len(test.want) {
				t.Fatalf("response.created carries other credential fields: %s", created)
			}
		})
	}
}

// Without any value of this proxy's own, none of the upstream's fields survive.
func TestRemoveResponsesWebsocketAuthOriginDropsAllThree(t *testing.T) {
	payload := removeResponsesWebsocketAuthOrigin([]byte(`{"type":"response.created","response":{"id":"r"},"cpa_auth_kind":"apikey","cpa_auth_provider":"inner","cpa_auth_host":"inner.example.test"}`))
	if strings.Contains(string(payload), "cpa_auth_") || ojson.GetBytesNoCopy(payload, "response.id").String() != "r" {
		t.Fatalf("payload = %s", payload)
	}
}
