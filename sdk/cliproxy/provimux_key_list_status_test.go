package cliproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The keys array as the real source fills it (key-model-lists.md 7.1, section 10's status table).

type keyStatusBody struct {
	Logins []map[string]any `json:"logins"`
	Keys   []struct {
		AuthIndex  string `json:"auth_index"`
		Provider   string `json:"provider"`
		Source     string `json:"source"`
		Registered int    `json:"registered_models"`
		Learned    int    `json:"learned_models"`
		Live       struct {
			Supported   bool `json:"supported"`
			Held        bool `json:"held"`
			Listed      *int `json:"listed_models"`
			LastFailure *struct {
				Category   string `json:"category"`
				HTTPStatus *int   `json:"http_status"`
			} `json:"last_failure"`
		} `json:"live"`
		Models *[]struct {
			ID      string `json:"id"`
			Source  string `json:"source"`
			Learned bool   `json:"learned"`
			Listed  *bool  `json:"listed"`
		} `json:"models"`
		LiveList json.RawMessage `json:"live_list"`
		Key      struct {
			Type       string   `json:"type"`
			Fixed      bool     `json:"fixed"`
			NotAsked   *string  `json:"not_asked"`
			Configured int      `json:"configured_models"`
			Unlisted   *int     `json:"unlisted_models"`
			LearnedIDs []string `json:"learned"`
			UnlistedOf []string `json:"unlisted"`
		} `json:"key"`
	} `json:"keys"`
}

func keyStatus(t *testing.T, svc *Service, query string) (keyStatusBody, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-lists"+query, nil)
	management.NewHandlerWithoutConfigFilePath(svc.cfg, svc.coreManager).ProvimuxModelLists(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var body keyStatusBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body, recorder.Body.Bytes()
}

func TestProvimuxKeyStatus(t *testing.T) {
	provimuxLiveReset(t)
	openrouter := newKeyStandIn(t, keyServe(keyFixture(t, "openrouter-models.json")))
	broken := newKeyStandIn(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	gateway := newKeyStandIn(t, keyServe(keyFixture(t, "claude-models.json")))
	cfg := provimuxOn()
	cfg.OpenAICompatibility = []config.OpenAICompatibility{
		{Name: "openrouter", BaseURL: openrouter.URL + "/api/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-or-secret"}},
			ExcludedModels: []string{"x-ai/*"},
			Models: []config.OpenAICompatibilityModel{{Name: "anthropic/claude-opus-5.5", Alias: "or-opus"},
				{Name: "acme/retired-model", Alias: "old"}}},
		{Name: "acme-gateway", BaseURL: broken.URL + "/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-acme-secret"}},
			Models: []config.OpenAICompatibilityModel{{Name: "a"}}},
	}
	cfg.ClaudeKey = []config.ClaudeKey{
		{APIKey: "sk-gw-secret", BaseURL: gateway.URL, Models: []config.ClaudeModel{{Name: "claude-opus-5-5", Alias: "opus"}}},
		{APIKey: "sk-fixed-secret", FixedModels: true, Models: []config.ClaudeModel{{Name: "claude-opus-5-5"}}},
		{APIKey: "sk-catalog-secret"}, // no rows: the catalog, no keys row
	}
	cfg.VertexCompatAPIKey = []config.VertexCompatKey{{APIKey: "vx-secret", BaseURL: "https://vertex.invalid",
		Models: []config.VertexCompatModel{{Name: "gemini-4-pro"}}}}
	svc, auths := keyService(t, cfg)
	for _, auth := range auths {
		svc.registerModelsForAuth(t.Context(), auth)
	}
	keyRefresh(svc, false)

	body, raw := keyStatus(t, svc, "")
	if out := os.Getenv("PMX_KEY_STATUS_OUT"); out != "" {
		_ = os.WriteFile(out, raw, 0o600) // the report's example
	}
	if len(body.Keys) != 5 {
		t.Fatalf("keys = %s", raw)
	}
	byProvider := map[string]int{}
	for i, row := range body.Keys {
		byProvider[row.Provider+"/"+row.Key.Type+"/"+boolText(row.Key.Fixed)] = i
	}
	or := body.Keys[byProvider["openrouter/openai-compatibility/false"]]
	if or.Source != "live" || !or.Live.Held || *or.Live.Listed != 3 || or.Learned != 1 || !slices.Equal(or.Key.LearnedIDs, []string{"openai/gpt-5.5"}) ||
		*or.Key.Unlisted != 1 || !slices.Equal(or.Key.UnlistedOf, []string{"acme/retired-model"}) || or.Key.Configured != 2 || or.Key.NotAsked != nil {
		t.Fatalf("openrouter = %+v", or)
	}
	failed := body.Keys[byProvider["acme-gateway/openai-compatibility/false"]]
	if failed.Source != "config" || failed.Live.Held || failed.Live.LastFailure == nil || failed.Live.LastFailure.Category != "http_status" ||
		*failed.Live.LastFailure.HTTPStatus != 404 || failed.Key.Unlisted != nil || len(failed.Key.LearnedIDs) != 0 {
		t.Fatalf("failed = %+v", failed)
	}
	gw := body.Keys[byProvider["claude/claude/false"]]
	if gw.Source != "live" || !slices.Equal(gw.Key.LearnedIDs, []string{"claude-gateway-only-1"}) || *gw.Key.Unlisted != 0 {
		t.Fatalf("claude gateway = %+v", gw)
	}
	fixed := body.Keys[byProvider["claude/claude/true"]]
	if fixed.Source != "config" || fixed.Live.Supported || fixed.Key.NotAsked == nil || *fixed.Key.NotAsked != "fixed" {
		t.Fatalf("fixed = %+v", fixed)
	}
	vertex := body.Keys[byProvider["vertex/vertex/false"]]
	if vertex.Key.NotAsked == nil || *vertex.Key.NotAsked != "no_list_endpoint" || vertex.Live.Supported {
		t.Fatalf("vertex = %+v", vertex)
	}
	if containsSecret(raw) {
		t.Fatal("a key in the answer")
	}
	// A Claude key behind a gateway is not a login, with or without view=live.
	for _, query := range []string{"", "?view=live"} {
		answer, _ := keyStatus(t, svc, query)
		for _, login := range answer.Logins {
			if login["provider"] == "claude" {
				t.Fatalf("%q: a Claude key in logins: %v", query, login)
			}
		}
	}

	// One key with its models: learned and rows, with listed.
	one, _ := keyStatus(t, svc, "?view=live&auth_index="+or.AuthIndex)
	if len(one.Keys) != 1 || one.Keys[0].Models == nil {
		t.Fatalf("one = %+v", one)
	}
	models := map[string]int{}
	for i, model := range *one.Keys[0].Models {
		models[model.ID] = i
	}
	list := *one.Keys[0].Models
	if len(list) != 3 || !list[models["openai/gpt-5.5"]].Learned || list[models["openai/gpt-5.5"]].Source != "live" ||
		*list[models["old"]].Listed || !*list[models["or-opus"]].Listed || list[models["or-opus"]].Source != "other" {
		t.Fatalf("models = %+v", list)
	}
	var live []map[string]any
	if err := json.Unmarshal(one.Keys[0].LiveList, &live); err != nil || len(live) != 3 {
		t.Fatalf("live_list = %s", one.Keys[0].LiveList)
	}
	// Every key auth is found by its index.
	for _, auth := range auths {
		if _, _, ok := (provimuxLiveListSource{}).KeyModelList(cfg, auth, true, false, false); ok && auth.Attributes["api_key"] == "sk-catalog-secret" {
			t.Fatal("a built-in key without rows has no keys row")
		}
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func containsSecret(raw []byte) bool {
	for _, secret := range []string{"sk-or-secret", "sk-acme-secret", "sk-gw-secret", "sk-fixed-secret", "vx-secret"} {
		if strings.Contains(string(raw), secret) {
			return true
		}
	}
	return false
}

// A Gemini key on Google's host with fixed-models: true and no rows is never asked: its logins row says
// so (live.supported false, the catalog), as does the older live-lists endpoint.
func TestProvimuxKeyStatusFixedGeminiOnGoogle(t *testing.T) {
	provimuxLiveReset(t)
	cfg := provimuxOn()
	cfg.GeminiKey = []config.GeminiKey{{APIKey: "AIza-fixed", FixedModels: true}, {APIKey: "AIza-asked"}}
	svc, auths := keyService(t, cfg)
	for _, auth := range auths {
		fixed := auth.Attributes["api_key"] == "AIza-fixed"
		state, ok := provimuxLiveListSource{}.ModelListLogin(auth, true, false, false)
		if !ok || state.Supported == fixed || (fixed && state.Source != "built-in") {
			t.Fatalf("%s: logins row = %+v (ok %v)", auth.Attributes["api_key"], state, ok)
		}
		if covered := (provimuxLiveListSource{}).State(auth).Covered; covered == fixed {
			t.Fatalf("%s: live-lists covered = %v", auth.Attributes["api_key"], covered)
		}
		if _, _, keyRow := (provimuxLiveListSource{}).KeyModelList(cfg, auth, true, false, false); keyRow {
			t.Fatal("a Gemini key without rows has no keys row")
		}
		if got := liveModelListKindFor(cfg, auth); (got == "") != fixed {
			t.Fatalf("%s: kind %q", auth.Attributes["api_key"], got)
		}
	}
	_ = svc
}
