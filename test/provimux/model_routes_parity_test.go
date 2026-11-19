// Package provimux holds cross-module tests of Provimux's own endpoints.
package provimux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type parityRoute struct {
	Provider string  `json:"provider"`
	Kind     *string `json:"kind"`
	Priority int     `json:"priority"`
	Host     *string `json:"host"`
}

func (r parityRoute) key() string {
	kind, host := "", ""
	if r.Kind != nil {
		kind = *r.Kind
	}
	if r.Host != nil {
		host = *r.Host
	}
	return r.Provider + "|" + kind + "|" + host + "|" + strconv.Itoa(r.Priority)
}

// GET /v1/models?include=accounts and GET /v8/management/provimux/model-routes list the same routes
// in the same order. This catches the copied host rule or the order drifting from the patch.
func TestProvimuxModelRoutesMatchModelAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	shared, keyOnly, compat := "parity-shared-model", "parity-key-model", "parity-lab/compat-model"
	auths := []*coreauth.Auth{
		{ID: "parity-key", Provider: "claude", Attributes: map[string]string{coreauth.AttributeAPIKey: "k1", "priority": "-25", "base_url": "https://API.example.com:8443/v1"}},
		{ID: "parity-login", Provider: "claude", Label: "ada@example.com", Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth", "priority": "-15"}},
		{ID: "parity-codex", Provider: "codex", Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth", "priority": "-15"}},
		{ID: "parity-off", Provider: "claude", Disabled: true, Attributes: map[string]string{coreauth.AttributeAuthKind: "oauth", "priority": "-10"}},
		{ID: "parity-compat", Provider: "openai-compatible-mine", Attributes: map[string]string{
			coreauth.AttributeAPIKey: "k2", "priority": "3",
			"base_url": "https://user:pass@OpenRouter.ai:443/api/v1?token=t",
		}},
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		models := []*registry.ModelInfo{{ID: shared, OwnedBy: "anthropic"}}
		switch auth.ID {
		case "parity-key":
			models = append(models, &registry.ModelInfo{ID: keyOnly, OwnedBy: "anthropic"})
		case "parity-compat":
			models = append(models, &registry.ModelInfo{ID: compat, OwnedBy: "openai"})
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	accounts := map[string][]parityRoute{}
	{
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models?include=accounts", nil)
		openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(nil, manager)).OpenAIModels(ctx)
		var body struct {
			Data []struct {
				ID       string        `json:"id"`
				Accounts []parityRoute `json:"accounts"`
			} `json:"data"`
		}
		if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode /v1/models: %v (%s)", errDecode, recorder.Body.String())
		}
		for _, entry := range body.Data {
			accounts[entry.ID] = entry.Accounts
		}
	}

	routes := map[string][]parityRoute{}
	{
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-routes", nil)
		management.NewHandlerWithoutConfigFilePath(&config.Config{}, manager).ProvimuxModelRoutes(ctx)
		var body struct {
			Models []struct {
				ID     string        `json:"id"`
				Routes []parityRoute `json:"routes"`
			} `json:"models"`
		}
		if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode model-routes: %v (%s)", errDecode, recorder.Body.String())
		}
		for _, model := range body.Models {
			routes[model.ID] = model.Routes
		}
	}

	for _, model := range []string{shared, keyOnly, compat} {
		want, got := accounts[model], routes[model]
		if len(want) == 0 || len(want) != len(got) {
			t.Fatalf("%s: include=accounts has %d routes, model-routes %d", model, len(want), len(got))
		}
		for i := range want {
			if want[i].key() != got[i].key() {
				t.Fatalf("%s route %d: include=accounts %+v, model-routes %+v", model, i, want[i], got[i])
			}
		}
	}
	if host := routes[compat][0].Host; host == nil || *host != "openrouter.ai" {
		t.Fatalf("compat host = %v, want openrouter.ai", host)
	}
}
