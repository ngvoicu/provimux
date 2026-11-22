package cliproxy

import (
	"sort"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: Antigravity's live list. Upstream's runtime asks every Antigravity account for its
// models (fetchAvailableModels) and filters the catalog by the ids it returns, so a model only the
// account lists is dropped. Here the catalog is filtered the same way and every listed model the
// catalog lacks is added from what the answer said about it (sdk/auth keeps displayName,
// maxTokens and maxOutputTokens per id). The answer states no thinking levels and no modalities:
// those stay unknown for such a model. Ids upstream's own catalog tool skips as internal or
// experimental are not added.

type provimuxAntigravityList struct {
	fetchedAt time.Time
	listed    int
	ids       []string // the account's model ids, as the answer named them
	learned   []*ModelInfo
}

var provimuxAntigravityLists = struct {
	mu     sync.Mutex
	byAuth map[string]provimuxAntigravityList
}{byAuth: make(map[string]provimuxAntigravityList)}

// provimuxAntigravitySkipped are the ids cmd/fetch_antigravity_models never puts in the catalog.
var provimuxAntigravitySkipped = map[string]struct{}{
	"chat_20706": {}, "chat_23310": {}, "tab_flash_lite_preview": {}, "tab_jump_flash_lite_preview": {},
	"gemini-2.5-flash-thinking": {}, "gemini-2.5-pro": {},
}

func provimuxAntigravityInternal(id string) bool {
	if _, skipped := provimuxAntigravitySkipped[id]; skipped {
		return true
	}
	return strings.HasPrefix(id, "chat_") || strings.HasPrefix(id, "tab_")
}

// provimuxAntigravityModels replaces filterAntigravityModels in antigravityModelsForHintsWithConfig:
// the catalog filtered by the account's ids, plus the account's models the catalog lacks.
func provimuxAntigravityModels(cfg *config.Config, auth *coreauth.Auth, catalog []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	models := filterAntigravityModels(catalog, hints)
	if hints.ModelIDs == nil {
		return models
	}
	if !provimuxLearningOn(cfg) {
		provimuxNoteAntigravityList(auth, hints, nil)
		return models
	}
	known := make(map[string]struct{}, len(catalog))
	for _, model := range catalog {
		if model != nil {
			known[normalizeAntigravityFetchedModelID(model.ID)] = struct{}{}
		}
	}
	ids := make([]string, 0, len(hints.ModelIDs))
	for id := range hints.ModelIDs {
		if _, ok := known[id]; !ok && !provimuxAntigravityInternal(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	learned := make([]*ModelInfo, 0, len(ids))
	for _, id := range ids {
		model := &ModelInfo{ID: id, Name: id, Object: "model", OwnedBy: "antigravity", Type: "antigravity", DisplayName: id}
		if description, ok := sdkauth.ProvimuxAntigravityModelDescription(id); ok {
			if description.DisplayName != "" {
				model.DisplayName, model.Description = description.DisplayName, description.DisplayName
			}
			model.ContextLength, model.MaxCompletionTokens = description.MaxTokens, description.MaxOutputTokens
		}
		learned = append(learned, model)
	}
	provimuxNoteAntigravityList(auth, hints, learned)
	for _, model := range learned {
		models = append(models, cloneLiveModel(model))
	}
	return models
}

// provimuxNoteAntigravityList records an account's ids and learned models for the status endpoint
// and the registry.
func provimuxNoteAntigravityList(auth *coreauth.Auth, hints antigravityModelCapabilityHints, learned []*ModelInfo) {
	if auth == nil || auth.ID == "" {
		return
	}
	ids := make([]string, 0, len(hints.ModelIDs))
	for id := range hints.ModelIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	provimuxAntigravityLists.mu.Lock()
	provimuxAntigravityLists.byAuth[auth.ID] = provimuxAntigravityList{fetchedAt: hints.updatedAt, listed: len(ids), ids: ids, learned: learned}
	provimuxAntigravityLists.mu.Unlock()
	provimuxUpdateLearnedModels("antigravity")
}

func provimuxAntigravityLearned() []*ModelInfo {
	provimuxAntigravityLists.mu.Lock()
	defer provimuxAntigravityLists.mu.Unlock()
	ids := make([]string, 0, len(provimuxAntigravityLists.byAuth))
	for id := range provimuxAntigravityLists.byAuth {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var learned []*ModelInfo
	seen := map[string]struct{}{}
	for _, id := range ids {
		for _, model := range provimuxAntigravityLists.byAuth[id].learned {
			if _, dup := seen[model.ID]; !dup {
				seen[model.ID] = struct{}{}
				learned = append(learned, model)
			}
		}
	}
	return learned
}

func provimuxAntigravityListOf(authID string) (provimuxAntigravityList, bool) {
	provimuxAntigravityLists.mu.Lock()
	defer provimuxAntigravityLists.mu.Unlock()
	entry, ok := provimuxAntigravityLists.byAuth[authID]
	return entry, ok
}
