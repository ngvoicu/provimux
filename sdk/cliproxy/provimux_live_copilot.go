package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: Copilot's live list. Upstream's own refresher asks every Copilot account for its
// models every 2 hours (copilot_catalogs.go) and keeps them as the account's catalog, but routes
// only the ids the catalog's copilot section names, so a model only the account lists is dropped.
// This fetch reads the same answer, records what the account listed and, for a model the section
// lacks that the account offers in its picker, a learned preset: the registry then names it, and
// upstream's resolver fills its fields from the account's own answer as for any other model. What
// an empty or failed answer does stays upstream's: its tests pin that an empty answer is an
// authoritative empty catalog.

type provimuxCopilotList struct {
	fetchedAt time.Time
	listed    int
	models    []*ModelInfo // every listed model, as the account described it
	learned   []*ModelInfo // offered models the catalog lacked when the list arrived
}

var provimuxCopilotLists = struct {
	mu     sync.Mutex
	byAuth map[string]provimuxCopilotList
}{byAuth: make(map[string]provimuxCopilotList)}

// provimuxFetchCopilotCatalog replaces copilot.FetchCatalog in upstream's refresher.
func provimuxFetchCopilotCatalog(ctx context.Context, client *http.Client, base, token, authID string) (*copilot.Catalog, error) {
	raw, err := copilot.FetchModels(ctx, client, base, token)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Models []copilot.Model `json:"data"`
	}
	if errDecode := json.Unmarshal(raw, &payload); errDecode != nil {
		return nil, fmt.Errorf("copilot: invalid model directory: %w", errDecode)
	}
	now := time.Now()
	provimuxNoteCopilotList(authID, payload.Models, now)
	return copilot.NewCatalog(payload.Models, now), nil
}

// provimuxNoteCopilotList records an account's answer and recomputes the learned presets.
func provimuxNoteCopilotList(authID string, models []copilot.Model, now time.Time) {
	catalog := provimuxCatalogIDs("copilot")
	entry := provimuxCopilotList{fetchedAt: now}
	seen := map[string]struct{}{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		entry.listed++
		entry.models = append(entry.models, provimuxCopilotPreset(model))
		if _, known := catalog[strings.ToLower(id)]; known || !provimuxCopilotOffered(model) {
			continue
		}
		entry.learned = append(entry.learned, provimuxCopilotPreset(model))
	}
	provimuxCopilotLists.mu.Lock()
	provimuxCopilotLists.byAuth[authID] = entry
	provimuxCopilotLists.mu.Unlock()
	provimuxUpdateLearnedModels("copilot")
}

// provimuxCopilotOffered mirrors the conditions under which upstream's resolver routes a model:
// not "auto", enabled by policy, shown in the picker, a chat model with a known endpoint.
func provimuxCopilotOffered(model copilot.Model) bool {
	if model.ID == "auto" || (model.Policy.State != "" && model.Policy.State != "enabled") ||
		(model.Picker != nil && !*model.Picker) || (model.Capabilities.Type != "" && model.Capabilities.Type != "chat") {
		return false
	}
	for _, endpoint := range model.Endpoints {
		if endpoint == copilot.Chat || endpoint == copilot.Messages || endpoint == copilot.Responses {
			return true
		}
	}
	return false
}

// provimuxCopilotPreset is the section entry of a learned Copilot model: the id and what the
// account stated. The resolver re-derives the fields from the account's answer per request.
func provimuxCopilotPreset(model copilot.Model) *ModelInfo {
	limits := model.Capabilities.Limits
	preset := &ModelInfo{ID: model.ID, Object: "model", Type: copilot.Provider, OwnedBy: copilot.Provider,
		DisplayName: strings.TrimSpace(model.Name), ContextLength: limits.Context, InputTokenLimit: limits.Input,
		OutputTokenLimit: limits.Output, MaxCompletionTokens: limits.Output,
		SupportedInputModalities: []string{"TEXT"}, SupportedOutputModalities: []string{"TEXT"}}
	if preset.DisplayName == "" {
		preset.DisplayName = model.ID
	}
	if vision := model.Capabilities.Supports.Vision; vision != nil && *vision {
		preset.SupportedInputModalities = append(preset.SupportedInputModalities, "IMAGE")
	}
	return preset
}

func provimuxCopilotLearned() []*ModelInfo {
	provimuxCopilotLists.mu.Lock()
	defer provimuxCopilotLists.mu.Unlock()
	ids := make([]string, 0, len(provimuxCopilotLists.byAuth))
	for id := range provimuxCopilotLists.byAuth {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var learned []*ModelInfo
	for _, id := range ids {
		for _, model := range provimuxCopilotLists.byAuth[id].learned {
			learned = append(learned, provimuxComposeLearned("copilot", model))
		}
	}
	return learned
}

// provimuxCopilotRoutes is the routes a Copilot login registers. A login whose account has not
// answered yet (the preset catalog) offers the catalog's models only, never learned ones, and so
// does every login while models.live-lists is off.
func provimuxCopilotRoutes(cfg *config.Config, catalog *copilot.Catalog) []copilot.Route {
	routes := catalog.Routes()
	if catalog != copilot.PresetCatalog && provimuxLearningOn(cfg) {
		return routes
	}
	kept := routes[:0]
	for _, route := range routes {
		if route.Info == nil || !registry.ProvimuxIsLearned("copilot", route.Info.ID) {
			kept = append(kept, route)
		}
	}
	return kept
}

// provimuxCopilotListOf returns what was recorded of an account's last answer.
func provimuxCopilotListOf(authID string) (provimuxCopilotList, bool) {
	provimuxCopilotLists.mu.Lock()
	defer provimuxCopilotLists.mu.Unlock()
	entry, ok := provimuxCopilotLists.byAuth[authID]
	return entry, ok
}

// provimuxForgetForeignLists drops Copilot and Antigravity records of logins that are gone.
func provimuxForgetForeignLists(present func(authID string) bool) {
	changed := map[string]bool{}
	provimuxCopilotLists.mu.Lock()
	for id := range provimuxCopilotLists.byAuth {
		if !present(id) {
			delete(provimuxCopilotLists.byAuth, id)
			changed["copilot"] = true
		}
	}
	provimuxCopilotLists.mu.Unlock()
	provimuxAntigravityLists.mu.Lock()
	for id := range provimuxAntigravityLists.byAuth {
		if !present(id) {
			delete(provimuxAntigravityLists.byAuth, id)
			changed["antigravity"] = true
		}
	}
	provimuxAntigravityLists.mu.Unlock()
	for section := range changed {
		provimuxUpdateLearnedModels(section)
	}
}
