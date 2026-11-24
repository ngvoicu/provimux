package cliproxy

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the keys array of GET /v8/management/provimux/model-lists (key-model-lists.md 7.1). One
// row per config key whose list this design covers: every openai-compatibility key, and every
// built-in key with rows. It reads the live-list store and the registry and copies values only;
// it starts no fetch.

// keyModelListLearned are the ids a key offers that no row names: its held list's learned models,
// minus the entry's excluded-models, in the list's order.
func keyModelListLearned(cfg *config.Config, auth *coreauth.Auth, info keyModelListInfo, list liveModelList) []string {
	excluded := keyModelListExcluded(cfg, auth, info)
	var out []string
	for _, model := range keyLearnedModels(info.rows, list) {
		if len(applyExcludedModels([]*ModelInfo{{ID: model.ID}}, excluded)) == 1 {
			out = append(out, model.ID)
		}
	}
	return out
}

// keyModelListExcluded is a key entry's excluded-models, as registration reads them.
func keyModelListExcluded(cfg *config.Config, auth *coreauth.Auth, info keyModelListInfo) []string {
	switch info.keyType {
	case "openai-compatibility":
		if info.compat != nil {
			return info.compat.ExcludedModels
		}
	case "claude":
		if entry := resolveConfigClaudeKeyForConfig(cfg, auth); entry != nil {
			return entry.ExcludedModels
		}
	case "codex":
		if entry := resolveConfigCodexKeyForConfig(cfg, auth); entry != nil {
			return entry.ExcludedModels
		}
	case "xai":
		if entry := resolveConfigXAIKeyForConfig(cfg, auth); entry != nil {
			return entry.ExcludedModels
		}
	case "meta":
		if entry := resolveConfigMetaKeyForConfig(cfg, auth); entry != nil {
			return entry.ExcludedModels
		}
	case "gemini":
		if entry := resolveConfigGeminiKeyForConfig(cfg, auth); entry != nil {
			return entry.ExcludedModels
		}
	}
	return nil
}

// KeyModelList reports one key's state; false when the credential gets no keys row.
func (provimuxLiveListSource) KeyModelList(cfg *config.Config, auth *coreauth.Auth, liveListsOn, withModels, withLive bool) (management.ProvimuxModelListLogin, management.ProvimuxModelListKey, bool) {
	info, ok := resolveKeyModelList(cfg, auth)
	if !ok {
		return management.ProvimuxModelListLogin{}, management.ProvimuxModelListKey{}, false
	}
	key := management.ProvimuxModelListKey{Type: info.keyType, Name: keyModelListName(auth, info), Fixed: info.fixed,
		NotAsked: info.notAsked, Configured: len(info.rows), Unlisted: -1}
	state := management.ProvimuxModelListLogin{Source: "config", Supported: info.notAsked == ""}

	// The store, read as for a login: only an observation of this key's identity and list kind counts.
	identity := liveModelListIdentity(auth)
	var (
		list    liveModelList
		hasList bool
	)
	liveModelLists.mu.Lock()
	provimuxLiveListNotes.mu.Lock()
	provimuxPruneLiveListNotesLocked()
	if held, okList := liveModelLists.lists[auth.ID]; okList && info.kind != "" && held.identity == identity && held.provider == info.kind {
		list, hasList = held, true
	}
	_, state.Fetching = liveModelLists.inFlight[auth.ID]
	if note, okNote := provimuxLiveListNotes.notes[auth.ID]; okNote && info.kind != "" && note.identity == identity {
		state.Failure = &management.ProvimuxLiveListFailure{At: note.retry.Add(-liveModelListRetry), Category: note.category, HTTPStatus: note.status}
	}
	provimuxLiveListNotes.mu.Unlock()
	liveModelLists.mu.Unlock()

	learned := map[string]struct{}{}
	key.Learned = []string{}
	if hasList {
		state.FetchedAt, state.Listed, state.FromCache = list.fetchedAt, len(list.ids), list.fromCache
		state.Held = liveModelListHeld(list, liveModelListNow())
		state.Expired = !state.Held
	}
	inUse := hasList && state.Held && liveListsOn
	if inUse {
		state.Source = "live"
		if ids := keyModelListLearned(cfg, auth, info, list); ids != nil {
			key.Learned = ids
		}
		key.UnlistedRows = keyUnlistedRows(info.rows, list)
		if key.UnlistedRows == nil {
			key.UnlistedRows = []string{}
		}
		key.Unlisted = len(key.UnlistedRows)
		for _, id := range key.Learned {
			learned[strings.ToLower(id)] = struct{}{}
		}
	}
	state.Learned = len(key.Learned)
	if withLive && hasList {
		state.Live = []management.ProvimuxVendorModel{}
		for _, model := range list.extra {
			if model != nil {
				state.Live = append(state.Live, provimuxVendorModelOf(model.ID, model))
			}
		}
	}
	if withModels {
		state.Models = keyModelListModels(auth, info, list, inUse, learned)
	}
	return state, key, true
}

// keyModelListName names the key's service in its row: an openai-compatibility entry's name, else
// the key type.
func keyModelListName(auth *coreauth.Auth, info keyModelListInfo) string {
	if info.compat != nil {
		return strings.ToLower(strings.TrimSpace(info.compat.Name))
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// keyModelListModels is one row per registered model of the key: a learned model has source live
// and learned true; a row has source other and listed, whether the held list names its model (null
// without a list in use, and for a plugin's model).
func keyModelListModels(auth *coreauth.Auth, info keyModelListInfo, list liveModelList, inUse bool, learned map[string]struct{}) []management.ProvimuxModelListModel {
	rowModel := map[string]string{} // client name -> the model the row names, lower case
	for _, row := range info.rows {
		name := strings.TrimSpace(row.GetName())
		client := strings.TrimSpace(row.GetAlias())
		if client == "" {
			client = name
		}
		rowModel[strings.ToLower(client)] = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(name).ModelName))
	}
	prefix := strings.ToLower(strings.TrimSpace(auth.Prefix))
	out := []management.ProvimuxModelListModel{}
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if model == nil {
			continue
		}
		id := strings.ToLower(model.ID)
		if prefix != "" {
			id = strings.TrimPrefix(id, prefix+"/")
		}
		row := management.ProvimuxModelListModel{ID: model.ID, Name: model.DisplayName, Source: "other",
			Context: provimuxFirstPositive(model.ContextLength, model.InputTokenLimit),
			Output:  provimuxFirstPositive(model.MaxCompletionTokens, model.OutputTokenLimit),
			Inputs:  model.SupportedInputModalities, Outputs: model.SupportedOutputModalities, Sources: map[string]string{}}
		if model.Thinking != nil {
			row.Levels = model.Thinking.Levels
		}
		if _, ok := learned[id]; ok {
			row.Source, row.Learned = "live", true
			listed := true
			row.Listed = &listed
		} else if named, ok := rowModel[id]; ok && inUse {
			_, listed := list.ids[named]
			row.Listed = &listed
		}
		out = append(out, row)
	}
	return out
}

func init() { management.SetProvimuxKeyModelListSource(provimuxLiveListSource{}) }
