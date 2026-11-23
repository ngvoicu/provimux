package management

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	codexmodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// One name, claude-opus-5-5, that an install may serve through any mix of two Claude logins, a Devin
// login (an oauth.model-alias fork), a Claude API key and an OpenRouter alias, with whatever
// priorities the install sets. The " (Devin)" suffix in the Codex-format list must follow only
// whether Devin serves the name alone: not the priorities, which routes exist, or the order the
// routes registered in. (The base name itself is the last registered route's, as in the original;
// installs that combine routes set display-name on their alias rows.)
func TestProvimuxDevinSuffixAnyPrioritiesAnyMix(t *testing.T) {
	provimuxTestClock(t)
	const id = "claude-opus-5-5"
	type route struct {
		provider string
		kind     string
		models   []*registry.ModelInfo
	}
	login := func() []*registry.ModelInfo {
		return []*registry.ModelInfo{{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: "Claude Opus 5.5", ContextLength: 1_000_000, MaxCompletionTokens: 128_000}}
	}
	routes := map[string]route{
		"login1": {"claude", coreauth.AuthKindOAuth, login()},
		"login2": {"claude", coreauth.AuthKindOAuth, login()},
		"devin": {"devin", coreauth.AuthKindOAuth, []*registry.ModelInfo{
			{ID: "devin/" + id, Object: "model", OwnedBy: "anthropic", Type: "devin", DisplayName: "Claude Opus 5.5", ContextLength: 1_000_000, MaxCompletionTokens: 128_000},
			{ID: id, Object: "model", OwnedBy: "cognition", Type: "devin", DisplayName: "Claude Opus 5.5", OAuthAlias: id, OAuthAliasSource: "devin/" + id, MetadataModelID: "devin/" + id, ContextLength: 1_000_000, MaxCompletionTokens: 128_000},
		}},
		"key":        {"claude", coreauth.AuthKindAPIKey, []*registry.ModelInfo{{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude", DisplayName: id, MetadataModelID: id, UserDefined: true, ContextLength: 1_000_000, MaxCompletionTokens: 128_000}}},
		"openrouter": {"openai-compatible-openrouter", coreauth.AuthKindAPIKey, []*registry.ModelInfo{{ID: id, Object: "model", OwnedBy: "anthropic", Type: "openai-compatibility", DisplayName: "Claude Opus 5.5", MetadataModelID: "anthropic/claude-opus-5.5", UserDefined: true}}},
	}
	mixes := map[string][]string{
		"all five":             {"login1", "login2", "devin", "key", "openrouter"},
		"logins only":          {"login1", "login2"},
		"keys only":            {"key", "openrouter"},
		"one login":            {"login1"},
		"one api key":          {"key"},
		"devin only":           {"devin"},
		"openrouter only":      {"openrouter"},
		"devin and openrouter": {"devin", "openrouter"},
		"api key and devin":    {"key", "devin"},
		"no openrouter":        {"login1", "devin", "key"},
	}
	priorities := map[string]map[string]int{
		"logins first":     {"login1": 30, "login2": 30, "devin": 20, "key": 10, "openrouter": 0},
		"openrouter first": {"openrouter": 30, "key": 20, "devin": 10, "login1": 0, "login2": 0},
		"devin first":      {"devin": 30, "openrouter": 20, "login1": 10, "key": 5, "login2": -1},
		"all equal":        {"login1": 0, "login2": 0, "devin": 0, "key": 0, "openrouter": 0},
	}
	mixNames := make([]string, 0, len(mixes))
	for name := range mixes {
		mixNames = append(mixNames, name)
	}
	sort.Strings(mixNames)

	for _, mixName := range mixNames {
		mix := mixes[mixName]
		devinOnly, withDevin := true, false
		for _, label := range mix {
			devinOnly = devinOnly && label == "devin"
			withDevin = withDevin || label == "devin"
		}
		for _, priorityName := range []string{"logins first", "openrouter first", "devin first", "all equal"} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s, %s, reverse %v", mixName, priorityName, reverse), func(t *testing.T) {
					order := append([]string(nil), mix...)
					// Register in the install's priority order (highest first), or the reverse.
					sort.SliceStable(order, func(i, j int) bool {
						if reverse {
							return priorities[priorityName][order[i]] < priorities[priorityName][order[j]]
						}
						return priorities[priorityName][order[i]] > priorities[priorityName][order[j]]
					})
					credentials := make([]provimuxTestCredential, 0, len(order))
					for _, name := range order {
						r := routes[name]
						auth := &coreauth.Auth{
							ID:       "pmx-combined-" + name,
							Provider: r.provider,
							Label:    name,
							Status:   coreauth.StatusActive,
							Attributes: map[string]string{
								coreauth.AttributeAuthKind: r.kind,
								"priority":                 strconv.Itoa(priorities[priorityName][name]),
							},
						}
						if r.kind == coreauth.AuthKindAPIKey {
							auth.Attributes[coreauth.AttributeAPIKey] = "fake-key"
						}
						credentials = append(credentials, provimuxTestCredential{auth, r.models})
					}
					manager := provimuxTestManager(t, credentials...)

					body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
					if m := provimuxFindModel(t, body, id); len(m.Routes) != len(mix) {
						t.Fatalf("%d routes, want %d", len(m.Routes), len(mix))
					}

					reg := registry.GetGlobalRegistry()
					var available []map[string]any
					for _, model := range reg.GetAvailableModels("openai") {
						if model["id"] == id || model["id"] == "devin/"+id {
							available = append(available, model)
						}
					}
					resp := codexmodels.BuildResponseForClient(available, reg.GetModelProviders, false, "0.153.4")
					list, _ := resp["models"].([]map[string]any)
					names := map[string]string{}
					for _, entry := range list {
						slug, _ := entry["slug"].(string)
						name, _ := entry["display_name"].(string)
						names[slug] = name
					}
					if hasSuffix := strings.HasSuffix(names[id], " (Devin)"); hasSuffix != devinOnly {
						t.Fatalf("Codex list name %q: suffix %v, want %v (Devin serves it alone: %v)", names[id], hasSuffix, devinOnly, devinOnly)
					}
					if withDevin && names["devin/"+id] != "Claude Opus 5.5 (Devin)" {
						t.Fatalf("Codex list devin/%s name %q, want %q", id, names["devin/"+id], "Claude Opus 5.5 (Devin)")
					}
				})
			}
		}
	}
}
