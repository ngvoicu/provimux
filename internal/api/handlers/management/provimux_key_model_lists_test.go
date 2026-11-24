package management

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The keys array of GET /provimux/model-lists (key-model-lists.md 7.1), with a stand-in source.

type provimuxFakeKeySource struct {
	rows map[string]struct {
		state ProvimuxModelListLogin
		key   ProvimuxModelListKey
	}
	cfg *config.Config
}

func (s *provimuxFakeKeySource) KeyModelList(cfg *config.Config, auth *coreauth.Auth, _, withModels, withLive bool) (ProvimuxModelListLogin, ProvimuxModelListKey, bool) {
	s.cfg = cfg
	row, ok := s.rows[auth.ID]
	if !ok {
		return ProvimuxModelListLogin{}, ProvimuxModelListKey{}, false
	}
	if !withModels {
		row.state.Models = nil
	}
	if !withLive {
		row.state.Live = nil
	}
	return row.state, row.key, true
}

func provimuxUseKeySource(t *testing.T, source ProvimuxKeyModelListSource) {
	t.Helper()
	previous := currentProvimuxKeyModelListSource()
	SetProvimuxKeyModelListSource(source)
	t.Cleanup(func() { SetProvimuxKeyModelListSource(previous) })
}

func provimuxKeyFixture(t *testing.T) (*Handler, *provimuxFakeKeySource) {
	t.Helper()
	h, _ := provimuxModelListFixture(t)
	for _, auth := range []*coreauth.Auth{
		{ID: "or-key", Provider: "openai-compatible-openrouter", Label: "openrouter",
			Attributes: map[string]string{"api_key": "sk-or-secret", "source": "config:openrouter[x]", "compat_name": "openrouter"}},
		{ID: "claude-fixed", Provider: "claude", Label: "Claude API Key", Attributes: map[string]string{"api_key": "sk-ant-secret", "source": "config:claude[y]"}},
	} {
		if _, err := h.authManager.Register(t.Context(), auth); err != nil {
			t.Fatal(err)
		}
	}
	listedTrue, listedFalse := true, false
	source := &provimuxFakeKeySource{rows: map[string]struct {
		state ProvimuxModelListLogin
		key   ProvimuxModelListKey
	}{
		"or-key": {
			state: ProvimuxModelListLogin{Source: "live", Supported: true, Held: true, FetchedAt: provimuxFetchedAt, Listed: 3, Learned: 1,
				Models: []ProvimuxModelListModel{
					{ID: "or-opus", Name: "or-opus", Source: "other", Listed: &listedTrue, Sources: map[string]string{}},
					{ID: "old", Name: "old", Source: "other", Listed: &listedFalse, Sources: map[string]string{}},
					{ID: "openai/gpt-5.5", Source: "live", Learned: true, Listed: &listedTrue, Context: 400000, Sources: map[string]string{}},
				},
				Live: []ProvimuxVendorModel{{ID: "openai/gpt-5.5"}, {ID: "anthropic/claude-opus-5.5"}, {ID: "x-ai/grok-5"}}},
			key: ProvimuxModelListKey{Type: "openai-compatibility", Name: "openrouter", Configured: 2, Unlisted: 1,
				UnlistedRows: []string{"acme/retired-model"}, Learned: []string{"openai/gpt-5.5"}},
		},
		"claude-fixed": {
			state: ProvimuxModelListLogin{Source: "config"},
			key:   ProvimuxModelListKey{Type: "claude", Name: "claude", Fixed: true, NotAsked: "fixed", Configured: 2, Unlisted: -1},
		},
	}}
	provimuxUseKeySource(t, source)
	return h, source
}

func TestProvimuxKeyRows(t *testing.T) {
	h, source := provimuxKeyFixture(t)
	body, raw := provimuxGetModelLists(t, h, "", http.StatusOK)
	provimuxValidate(t, "ModelListsResponse", raw)
	if source.cfg == nil || !source.cfg.Models.LiveListsOn() {
		t.Fatal("the source reads the config in use")
	}
	keys := body["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("keys = %s", raw)
	}
	claude, openrouter := keys[0].(map[string]any), keys[1].(map[string]any)
	claudeKey := claude["key"].(map[string]any)
	if claude["provider"] != "claude" || claude["source"] != "config" || claudeKey["not_asked"] != "fixed" || claudeKey["fixed"] != true ||
		claudeKey["unlisted_models"] != nil || claudeKey["unlisted"] != nil || len(claudeKey["learned"].([]any)) != 0 {
		t.Fatalf("claude = %v", claude)
	}
	key := openrouter["key"].(map[string]any)
	if openrouter["provider"] != "openrouter" || openrouter["source"] != "live" || openrouter["learned_models"].(float64) != 1 ||
		key["type"] != "openai-compatibility" || key["not_asked"] != nil || key["unlisted_models"].(float64) != 1 ||
		key["learned"].([]any)[0] != "openai/gpt-5.5" || key["unlisted"].([]any)[0] != "acme/retired-model" || openrouter["models"] != nil {
		t.Fatalf("openrouter = %v", openrouter)
	}
	// Keys are never in logins.
	for _, login := range body["logins"].([]any) {
		if id := login.(map[string]any)["provider"]; id == "openrouter" {
			t.Fatal("a key in logins")
		}
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatal("no key in the answer")
	}
}

func TestProvimuxKeyRowWithModelsAndLive(t *testing.T) {
	h, _ := provimuxKeyFixture(t)
	index := ""
	for _, auth := range h.authManager.List() {
		if auth.ID == "or-key" {
			index = lockedAuthIndex(auth)
		}
	}
	body, raw := provimuxGetModelLists(t, h, "?view=live&auth_index="+index, http.StatusOK)
	provimuxValidate(t, "ModelListsResponse", raw)
	keys := body["keys"].([]any)
	if len(keys) != 1 || len(body["logins"].([]any)) != 0 {
		t.Fatalf("only the named key: %s", raw)
	}
	row := keys[0].(map[string]any)
	models := row["models"].([]any)
	if len(models) != 3 {
		t.Fatalf("models = %v", models)
	}
	byID := map[string]map[string]any{}
	for _, model := range models {
		byID[model.(map[string]any)["id"].(string)] = model.(map[string]any)
	}
	if byID["old"]["listed"] != false || byID["or-opus"]["listed"] != true || byID["openai/gpt-5.5"]["learned"] != true ||
		byID["openai/gpt-5.5"]["source"] != "live" {
		t.Fatalf("models = %v", byID)
	}
	if list := row["live_list"].([]any); len(list) != 3 {
		t.Fatalf("live_list = %v", row["live_list"])
	}
}

func TestProvimuxKeyRowsWithoutSource(t *testing.T) {
	h, _ := provimuxModelListFixture(t)
	provimuxUseKeySource(t, nil)
	body, raw := provimuxGetModelLists(t, h, "", http.StatusOK)
	if keys, ok := body["keys"].([]any); !ok || len(keys) != 0 {
		t.Fatalf("keys must be [] not null: %s", raw)
	}
}
