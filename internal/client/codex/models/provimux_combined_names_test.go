package models

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func provimuxRegister(t *testing.T, clientID, provider string, models ...*registry.ModelInfo) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })
}

// provimuxCodexNames builds the Codex-format list for ids from the global registry, as the handler does.
func provimuxCodexNames(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var available []map[string]any
	for _, model := range reg.GetAvailableModels("openai") {
		if id, _ := model["id"].(string); want[id] {
			available = append(available, model)
		}
	}
	resp := BuildResponseForClient(available, reg.GetModelProviders, false, "0.153.4")
	models, _ := resp["models"].([]map[string]any)
	names := map[string]string{}
	for _, m := range models {
		names[stringModelValue(m, "slug")] = stringModelValue(m, "display_name")
	}
	return names
}

// claude-opus-5-5 served by two Claude logins, a Claude API key, an OpenRouter alias and a Devin
// alias (oauth.model-alias fork, registered last): not Devin's alone, so no " (Devin)".
// devin/claude-opus-5-5, which only Devin serves, keeps it.
func TestCodexClientModelsSharedNameHasNoDevinSuffix(t *testing.T) {
	const id = "claude-opus-5-5"
	login := &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: "Claude Opus 5.5"}
	provimuxRegister(t, "t-claude-api-key", "claude", &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: id, MetadataModelID: id, UserDefined: true})
	provimuxRegister(t, "t-claude-login-1", "claude", login)
	provimuxRegister(t, "t-claude-login-2", "claude", login)
	provimuxRegister(t, "t-openrouter", "openai-compatible-openrouter", &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "openai-compatibility", DisplayName: "Claude Opus 5.5", MetadataModelID: "anthropic/claude-opus-5.5", UserDefined: true})
	provimuxRegister(t, "t-devin", "devin",
		&registry.ModelInfo{ID: "devin/" + id, Object: "model", OwnedBy: "anthropic", Type: "devin", DisplayName: "Claude Opus 5.5"},
		&registry.ModelInfo{ID: id, Object: "model", OwnedBy: "cognition", Type: "devin", DisplayName: "Claude Opus 5.5", OAuthAlias: id, OAuthAliasSource: "devin/" + id, MetadataModelID: "devin/" + id},
	)
	names := provimuxCodexNames(t, id, "devin/"+id)
	if got := names[id]; got != "Claude Opus 5.5" {
		t.Fatalf("%s display_name = %q, want %q", id, got, "Claude Opus 5.5")
	}
	if got := names["devin/"+id]; got != "Claude Opus 5.5 (Devin)" {
		t.Fatalf("devin/%s display_name = %q, want %q", id, got, "Claude Opus 5.5 (Devin)")
	}

	// Without a providers function the global registry answers.
	var available []map[string]any
	for _, model := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
		if model["id"] == id {
			available = append(available, model)
		}
	}
	models, _ := BuildResponseForClient(available, nil, false, "0.153.4")["models"].([]map[string]any)
	if len(models) != 1 || strings.HasSuffix(stringModelValue(models[0], "display_name"), "(Devin)") {
		t.Fatalf("without providers: %v", models)
	}
}

// Review finding 5, case 1: a compatibility alias that also serves devin/claude-opus-5-5. Devin no
// longer serves it alone, so the devin/ prefix alone does not add the suffix. Fixed.
func TestCodexClientModelsDevinPrefixSharedWithAlias(t *testing.T) {
	const id = "devin/claude-opus-5-6-shared"
	provimuxRegister(t, "t-devin-prefixed", "devin", &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "devin", DisplayName: "Claude Opus 5.6"})
	provimuxRegister(t, "t-compat-on-devin-id", "openai-compatible-team", &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "openai-compatibility", DisplayName: "Claude Opus 5.6", MetadataModelID: "anthropic/claude-opus-5.6", UserDefined: true})
	if got := provimuxCodexNames(t, id)[id]; got != "Claude Opus 5.6" {
		t.Fatalf("%s display_name = %q, want no (Devin) while an alias shares it", id, got)
	}
	registry.GetGlobalRegistry().UnregisterClient("t-compat-on-devin-id")
	if got := provimuxCodexNames(t, id)[id]; got != "Claude Opus 5.6 (Devin)" {
		t.Fatalf("%s display_name = %q after the alias left, want the (Devin) suffix back", id, got)
	}
}

// Review finding 5, case 2, KNOWN, not fixed (the same on 003d1f27): a bare OAuth fork whose Devin
// source an exclusion removes. Only Claude serves the id now, but the registry keeps the published
// info of the Devin route that registered last (type devin), and the original's check reads that type
// for ids Devin does not serve (its own test expects the suffix for a devin-typed model on other
// providers). Flip this test when that is fixed.
func TestKnownCodexClientModelsStaleDevinTypeAfterExclusion(t *testing.T) {
	const id = "claude-opus-5-6-fork"
	provimuxRegister(t, "t-fork-claude", "claude", &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: "Claude Opus 5.6"})
	provimuxRegister(t, "t-fork-devin", "devin",
		&registry.ModelInfo{ID: "devin/" + id, Object: "model", OwnedBy: "anthropic", Type: "devin", DisplayName: "Claude Opus 5.6"},
		&registry.ModelInfo{ID: id, Object: "model", OwnedBy: "cognition", Type: "devin", DisplayName: "Claude Opus 5.6", OAuthAlias: id, OAuthAliasSource: "devin/" + id},
	)
	if got := provimuxCodexNames(t, id)[id]; got != "Claude Opus 5.6" {
		t.Fatalf("shared: %q, want no suffix", got)
	}
	// The exclusion: Devin registers again without the bare fork.
	registry.GetGlobalRegistry().RegisterClient("t-fork-devin", "devin", []*registry.ModelInfo{{ID: "devin/" + id, Object: "model", OwnedBy: "anthropic", Type: "devin", DisplayName: "Claude Opus 5.6"}})
	if providers := registry.GetGlobalRegistry().GetModelProviders(id); len(providers) != 1 || providers[0] != "claude" {
		t.Fatalf("providers after the exclusion = %v, want only claude", providers)
	}
	if got := provimuxCodexNames(t, id)[id]; got != "Claude Opus 5.6 (Devin)" {
		t.Fatalf("known behaviour changed: %q; if fixed, expect %q and update this test", got, "Claude Opus 5.6")
	}
}

// Review finding 5, case 3, KNOWN, not fixed (the same on 003d1f27): a Home catalog lists an id that
// both Claude and Devin serve. Home passes no providers lookup for names and marks the entry type
// devin when any of its providers is Devin, so the suffix is added. Flip this test when that is fixed.
func TestKnownCodexClientModelsHomeEntryWithDevinAndClaude(t *testing.T) {
	const id = "claude-opus-5-6-home" // not in the global registry: Home's models are its own
	homeProviders := func(model string) []string {
		if model == id {
			return []string{"claude", "devin"}
		}
		return nil
	}
	models := []map[string]any{{"id": id, "object": "model", "type": "devin", "display_name": "Claude Opus 5.6"}}
	for _, resp := range []map[string]any{
		BuildResponseForClientWithSearchToolRoutes(models, nil, homeProviders, nil, nil, nil, false, "0.153.4"),
		BuildResponseForClientWithToolCapabilities(models, nil, nil, nil, nil, false, "0.153.4"),
	} {
		list, _ := resp["models"].([]map[string]any)
		if len(list) != 1 || stringModelValue(list[0], "display_name") != "Claude Opus 5.6 (Devin)" {
			t.Fatalf("known behaviour changed: %v; if fixed, expect %q and update this test", list, "Claude Opus 5.6")
		}
	}
}
