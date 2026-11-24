package cliproxy

import (
	"slices"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the state behind GET /v8/management/provimux/model-lists (model-lists.md 5.5). It reads
// the live-list store, the Copilot and Antigravity records and the registry, and copies values
// only. Field sources are "built-in" (the catalog gave the value), "live" (the vendor's list did: a
// learned model, or a field the catalog leaves empty) or "published" (providers.dev did, for a field of a
// learned model the vendor's list does not state).

// provimuxModelListLoginTypes are the providers whose credentials get a row: the nine with a live
// list, and Vertex and AI Studio, which take the catalog.
var provimuxModelListLoginTypes = []string{"antigravity", "aistudio", "claude", "codex", "copilot", "devin", "gemini", "kimi", "meta", "vertex", "xai"}

func (provimuxLiveListSource) LoginTypes() []string {
	return append([]string(nil), provimuxModelListLoginTypes...)
}

// provimuxModelListType is the row's login type, or "" for a credential without a row: API keys,
// except Gemini keys on Google's own host, keep the catalog and are not listed.
func provimuxModelListType(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if provider := liveModelListType(auth); provider != "" && (liveModelListAsked[provider] || auth.AuthKind() != coreauth.AuthKindAPIKey) {
		return provider
	}
	if auth.AuthKind() == coreauth.AuthKindAPIKey {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if slices.Contains(provimuxModelListLoginTypes, provider) && provider != "gemini" {
		return provider
	}
	return ""
}

func provimuxCatalogOf(section string) []*ModelInfo {
	if section == "vertex" || section == "aistudio" {
		return registry.GetStaticModelDefinitionsByChannel(section)
	}
	return registry.ProvimuxCatalogModels(section)
}

func (source provimuxLiveListSource) ModelListLogin(auth *coreauth.Auth, liveListsOn, withModels, withLive bool) (management.ProvimuxModelListLogin, bool) {
	section := provimuxModelListType(auth)
	if section == "" {
		return management.ProvimuxModelListLogin{}, false
	}
	catalog := provimuxCatalogOf(section)
	catalogByID := make(map[string]*ModelInfo, len(catalog))
	for _, model := range catalog {
		if model != nil {
			catalogByID[strings.ToLower(model.ID)] = model
		}
	}
	state := management.ProvimuxModelListLogin{Source: "none"}
	if len(catalog) > 0 {
		state.Source = "built-in"
	}
	var (
		live      map[string]*ModelInfo // the vendor's descriptions, by lower-case id
		liveIDs   map[string]struct{}   // the ids the vendor listed
		decides   bool                  // the live list decides the login's models now
		allLive   bool                  // every registered model comes from the account (Copilot)
		vendorOut []management.ProvimuxVendorModel
	)
	switch section {
	case "copilot":
		state.Supported = true
		catalogNow := auth.ActiveCopilotCatalog(time.Now())
		switch {
		case catalogNow == nil:
			state.Source = "none"
		case catalogNow == copilot.PresetCatalog:
			state.Source = "built-in"
		case catalogNow == copilot.UnavailableCatalog:
			state.Source = "none"
		default:
			state.Source, state.Held, decides, allLive = "live", true, true, true
		}
		if entry, ok := provimuxCopilotListOf(auth.ID); ok {
			state.FetchedAt, state.Listed = entry.fetchedAt, entry.listed
			live = map[string]*ModelInfo{}
			liveIDs = map[string]struct{}{}
			for _, model := range entry.models {
				live[strings.ToLower(model.ID)] = model
				liveIDs[strings.ToLower(model.ID)] = struct{}{}
				vendorOut = append(vendorOut, provimuxVendorModelOf(model.ID, model))
			}
		}
	case "antigravity":
		state.Supported = true
		state.Source = "none" // upstream registers no Antigravity model before the account answers
		if entry, ok := provimuxAntigravityListOf(auth.ID); ok {
			state.Source, state.Held, decides = "live", true, true
			state.FetchedAt, state.Listed = entry.fetchedAt, entry.listed
			live = map[string]*ModelInfo{}
			liveIDs = map[string]struct{}{}
			for _, id := range entry.ids {
				model := &ModelInfo{ID: id}
				if description, okDescription := sdkauth.ProvimuxAntigravityModelDescription(id); okDescription {
					model.DisplayName, model.ContextLength, model.MaxCompletionTokens = description.DisplayName, description.MaxTokens, description.MaxOutputTokens
				}
				live[id], liveIDs[id] = model, struct{}{}
				vendorOut = append(vendorOut, provimuxVendorModelOf(id, model))
			}
		}
	case "vertex", "aistudio":
	default:
		if liveModelListProvider(auth) == "" {
			break // a type whose fetcher is switched off (Kimi, xAI): the catalog, as before
		}
		state.Supported = true
		held := source.State(auth)
		state.Fetching = held.Fetching
		state.Failure = held.Failure
		identity := liveModelListIdentity(auth)
		liveModelLists.mu.Lock()
		list, ok := liveModelLists.lists[auth.ID]
		liveModelLists.mu.Unlock()
		if ok && list.identity == identity {
			state.FetchedAt, state.Listed, state.FromCache = list.fetchedAt, len(list.ids), list.fromCache
			state.Held = liveModelListHeld(list, liveModelListNow())
			state.Expired = !state.Held
			live = map[string]*ModelInfo{}
			liveIDs = list.ids
			for _, model := range list.extra {
				if model != nil {
					live[strings.ToLower(model.ID)] = model
				}
			}
			for id := range list.ids {
				vendor := provimuxVendorModelOf(id, live[id])
				vendor.ID = provimuxPublicModelID(section, vendor.ID)
				vendorOut = append(vendorOut, vendor)
			}
		}
		if state.Held && liveListsOn {
			state.Source, decides = "live", true
		}
	}
	if withLive && live != nil {
		state.Live = vendorOut
		if state.Live == nil {
			state.Live = []management.ProvimuxVendorModel{}
		}
	}
	builtins := liveModelListBuiltins(section)
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if model == nil {
			continue
		}
		// A configured alias is classified first: it is never one of the vendor's or the catalog's ids,
		// whatever its name ("other" in the contract). On 8.1.1 a Devin alias's bare name can equal a
		// native id without its devin/ namespace.
		key := ""
		if strings.TrimSpace(model.OAuthAlias) == "" {
			key = provimuxListKeyOfRegistered(section, model.ID)
		}
		_, listed := liveIDs[key]
		_, known := catalogByID[key]
		learned := decides && listed && !known
		if learned {
			state.Learned++
		}
		if !withModels {
			continue
		}
		row := management.ProvimuxModelListModel{ID: model.ID, Name: model.DisplayName, Learned: learned,
			Context: provimuxFirstPositive(model.ContextLength, model.InputTokenLimit),
			Output:  provimuxFirstPositive(model.MaxCompletionTokens, model.OutputTokenLimit),
			Inputs:  model.SupportedInputModalities, Outputs: model.SupportedOutputModalities, Sources: map[string]string{}}
		if model.Thinking != nil {
			row.Levels = model.Thinking.Levels
		}
		_, builtin := builtins[key]
		switch {
		case builtin:
			row.Source = "provimux"
		case decides && (listed || allLive):
			row.Source = "live"
		case known && !decides:
			row.Source = "built-in"
		default:
			row.Source = "other"
		}
		if row.Source == "live" || row.Source == "built-in" {
			vendor := live[key]
			if !decides {
				vendor = nil
			}
			if learned && !allLive {
				// A model the catalog lacks: the vendor's list, then providers.dev (step P3).
				provimuxLearnedFieldSources(row.Sources, section, vendor)
			} else {
				// The catalog as it states the model, and as providers.dev filled its gaps.
				catalog, filled := catalogByID[key], catalogByID[key]
				if catalog != nil {
					if raw := registry.ProvimuxRawCatalogModel(section, catalog.ID); raw != nil {
						catalog = raw
					}
				}
				provimuxFieldSources(row.Sources, vendor, catalog, allLive, filled)
			}
		}
		state.Models = append(state.Models, row)
	}
	if withModels && state.Models == nil {
		state.Models = []management.ProvimuxModelListModel{}
	}
	return state, true
}

func provimuxFirstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// provimuxFieldSources names, per field, where the registered value came from, by the rule of
// fillLiveModel: for a model the catalog has, the catalog's value ("built-in"), and the vendor's
// ("live") only where the catalog leaves the field empty, never for the reasoning levels (the
// catalog's thinking block is kept whole); for a learned model, the vendor's. With allLive every
// stated value is the account's (Copilot resolves each model from the account's answer).
func provimuxFieldSources(out map[string]string, vendor, catalog *ModelInfo, allLive bool, filled *ModelInfo) {
	states := func(model *ModelInfo, field string) bool {
		if model == nil {
			return false
		}
		switch field {
		case "display_name":
			return model.DisplayName != ""
		case "context":
			return provimuxFirstPositive(model.ContextLength, model.InputTokenLimit) > 0
		case "output":
			return provimuxFirstPositive(model.MaxCompletionTokens, model.OutputTokenLimit) > 0
		case "input_modalities":
			return len(model.SupportedInputModalities) > 0
		case "output_modalities":
			return len(model.SupportedOutputModalities) > 0
		case "reasoning_levels":
			return model.Thinking != nil && len(model.Thinking.Levels) > 0
		}
		return false
	}
	for _, field := range []string{"display_name", "context", "output", "input_modalities", "output_modalities", "reasoning_levels"} {
		switch {
		case allLive:
			if states(vendor, field) || states(catalog, field) {
				out[field] = "live"
			}
		case states(catalog, field):
			out[field] = "built-in"
		case catalog != nil && field != "reasoning_levels" && states(vendor, field):
			out[field] = "live" // the vendor fills what the catalog leaves empty, before providers.dev
		case catalog != nil && field != "reasoning_levels" && states(filled, field):
			out[field] = "published" // providers.dev fills a gap both leave
		case catalog != nil && field == "reasoning_levels":
			// the catalog's thinking block is kept whole: no levels from the vendor
		case states(vendor, field):
			out[field] = "live"
		}
	}
}

func provimuxVendorModelOf(id string, model *ModelInfo) management.ProvimuxVendorModel {
	out := management.ProvimuxVendorModel{ID: id}
	if model == nil {
		return out
	}
	out.ID = model.ID
	out.Name = model.DisplayName
	out.Context = provimuxFirstPositive(model.ContextLength, model.InputTokenLimit)
	out.Output = provimuxFirstPositive(model.MaxCompletionTokens, model.OutputTokenLimit)
	out.Inputs = model.SupportedInputModalities
	out.Outputs = model.SupportedOutputModalities
	if model.Thinking != nil {
		out.Levels = model.Thinking.Levels
	}
	return out
}

func init() { management.SetProvimuxModelListSource(provimuxLiveListSource{}) }
