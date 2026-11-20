package openai

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// websocketCreatedProviderExecutor is websocketCreatedExecutor for another provider.
type websocketCreatedProviderExecutor struct {
	websocketCreatedExecutor
	provider string
}

func (e websocketCreatedProviderExecutor) Identifier() string { return e.provider }

// Each turn's response.created names, beside cpa_auth_kind, the provider of the credential serving the turn and, for an
// API key with a base URL, its host: the same values /v1/models?include=accounts lists for that credential.
func TestResponsesWebsocketNamesTheAuthProviderAndHostOnResponseCreated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	model := "auth-provider-websocket-model"
	codexLogin := &coreauth.Auth{
		ID:         "auth-provider-websocket-codex-login",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Label:      "someone@example.test",
		Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth"},
	}
	devinLogin := &coreauth.Auth{
		ID:         "auth-provider-websocket-devin-login",
		Provider:   "devin",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth"},
	}
	compatKey := &coreauth.Auth{
		ID:       "auth-provider-websocket-key",
		Provider: "openai-compatible-lab",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey: "secret-key",
			"base_url":               "https://user:secret-pass@API.Example.test:8443/acct-secret-id/v1",
		},
	}
	codexKey := &coreauth.Auth{
		ID:         "auth-provider-websocket-codex-key",
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAPIKey: "secret-key"},
	}
	credentials := []*coreauth.Auth{codexLogin, devinLogin, compatKey, codexKey}
	// Each connection gets the next credential.
	selector := &orderedWebsocketSelector{order: []string{codexLogin.ID, devinLogin.ID, compatKey.ID, codexKey.ID}}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(websocketCreatedExecutor{})
	manager.RegisterExecutor(websocketCreatedProviderExecutor{provider: "devin"})
	manager.RegisterExecutor(websocketCreatedProviderExecutor{provider: "openai-compatible-lab"})
	for _, auth := range credentials {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"

	// What the model accounts list gives for this model, as kind/provider/host triples.
	listed := map[string]bool{}
	for _, account := range NewOpenAIAPIHandler(base).modelAccounts()[model] {
		listed[account.Kind+"/"+account.Provider+"/"+account.Host] = true
	}

	turn := func(conn *websocket.Conn) (created, completed []byte) {
		t.Helper()
		request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-1"}]}`, model)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
			t.Fatalf("write: %v", errWrite)
		}
		for completed == nil {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read: %v", errRead)
			}
			switch ojson.GetBytesNoCopy(payload, "type").String() {
			case "response.created":
				created = payload
			case "response.completed":
				completed = payload
			}
		}
		return created, completed
	}

	loginProviders := map[string]bool{}
	for _, want := range []struct {
		auth     *coreauth.Auth
		kind     string
		provider string
		host     string
	}{
		{auth: codexLogin, kind: "oauth", provider: "codex"},
		{auth: devinLogin, kind: "oauth", provider: "devin"},
		{auth: compatKey, kind: "apikey", provider: "openai-compatible-lab", host: "api.example.test"},
		{auth: codexKey, kind: "apikey", provider: "codex"},
	} {
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial: %v", errDial)
		}
		created, completed := turn(conn)
		if got := ojson.GetBytesNoCopy(created, "response.id").String(); got != "resp-"+want.auth.ID {
			t.Fatalf("served by %q, want %s", got, want.auth.ID)
		}
		kind := ojson.GetBytesNoCopy(created, "cpa_auth_kind").String()
		provider := ojson.GetBytesNoCopy(created, "cpa_auth_provider").String()
		host := ojson.GetBytesNoCopy(created, "cpa_auth_host")
		if kind != want.kind || provider != want.provider {
			t.Fatalf("response.created cpa_auth_kind, cpa_auth_provider = %q, %q, want %q, %q: %s", kind, provider, want.kind, want.provider, created)
		}
		if host.Exists() != (want.host != "") || host.String() != want.host {
			t.Fatalf("response.created cpa_auth_host = %q (present %v), want %q: %s", host.String(), host.Exists(), want.host, created)
		}
		if !listed[kind+"/"+provider+"/"+host.String()] {
			t.Fatalf("response.created names %s/%s/%s, not among the accounts of %s: %v", kind, provider, host.String(), model, listed)
		}
		if kind == "oauth" {
			loginProviders[provider] = true
		}
		// Only response.created carries them, and nothing that identifies the credential.
		for _, field := range []string{"cpa_auth_provider", "cpa_auth_host"} {
			if ojson.GetBytesNoCopy(completed, field).Exists() {
				t.Fatalf("response.completed carries %s: %s", field, completed)
			}
		}
		withoutID, _ := ojson.DeleteBytes(created, "response.id")
		for _, secret := range []string{"secret-key", "secret-pass", "acct-secret-id", "8443", "someone@example.test", want.auth.ID} {
			if strings.Contains(string(withoutID), secret) {
				t.Fatalf("response.created exposes %q: %s", secret, created)
			}
		}
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close: %v", errClose)
		}
	}
	// Two logins under one model name: same kind, different providers.
	if len(loginProviders) != 2 {
		t.Fatalf("login providers named = %v, want two different ones", loginProviders)
	}
}
