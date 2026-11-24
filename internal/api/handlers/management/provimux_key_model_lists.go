package management

import (
	"encoding/json"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: the keys array of GET /v8/management/provimux/model-lists (key-model-lists.md 7.1).
// One row per config API key whose model list refreshes itself or could: every
// openai-compatibility key, and every built-in key with rows. A row has the shape of a logins row
// plus an object key. Keys are not in logins: a key's service (a gateway behind a Claude key) is
// not its type's vendor, and readers of logins publish what each login type's vendor lists.

// ProvimuxModelListKey is what the config says about one key and what its list adds to it.
type ProvimuxModelListKey struct {
	Type         string // openai-compatibility, claude, codex, xai, meta, gemini, interactions, vertex
	Name         string // an openai-compatibility entry's name, else the type
	Fixed        bool
	NotAsked     string   // "", fixed, live_lists_off, no_list_endpoint
	Configured   int      // rows in the config
	Unlisted     int      // rows whose model the held list does not name; -1 without a list in use
	UnlistedRows []string // those rows' names as written; nil without a list in use
	Learned      []string // ids the key offers that no row names, in the list's order
}

// ProvimuxKeyModelListSource reads a key's model-list state. sdk/cliproxy provides it.
type ProvimuxKeyModelListSource interface {
	// KeyModelList reports one credential's state with the config in use; false when it gets no keys row.
	KeyModelList(cfg *config.Config, auth *coreauth.Auth, liveListsOn, withModels, withLive bool) (ProvimuxModelListLogin, ProvimuxModelListKey, bool)
}

var provimuxKeyModelListSource ProvimuxKeyModelListSource

// SetProvimuxKeyModelListSource registers the source; nil removes it.
func SetProvimuxKeyModelListSource(source ProvimuxKeyModelListSource) {
	provimuxModelListSourceMu.Lock()
	defer provimuxModelListSourceMu.Unlock()
	provimuxKeyModelListSource = source
}

func currentProvimuxKeyModelListSource() ProvimuxKeyModelListSource {
	provimuxModelListSourceMu.RLock()
	defer provimuxModelListSourceMu.RUnlock()
	return provimuxKeyModelListSource
}

type provimuxKeyModelListRow struct {
	provimuxModelListRow
	Models *[]provimuxKeyModelListModel `json:"models"` // replaces the logins row's models
	Key    provimuxKeyModelListKey      `json:"key"`
}

type provimuxKeyModelListModel struct {
	provimuxModelListModel
	// Listed: whether the service's held list names the model; null without a list in use, and for
	// a model no row and no list gave (a plugin's).
	Listed *bool `json:"listed"`
}

type provimuxKeyModelListKey struct {
	Type       string   `json:"type"`
	Name       string   `json:"name"`
	Fixed      bool     `json:"fixed"`
	NotAsked   *string  `json:"not_asked"`
	Configured int      `json:"configured_models"`
	Unlisted   *int     `json:"unlisted_models"`
	Learned    []string `json:"learned"`
	UnlistedOf []string `json:"unlisted"`
}

// provimuxKeyModelListRows builds the keys array; wantIndex and withLive as for logins.
func (h *Handler) provimuxKeyModelListRows(cfg *config.Config, liveListsOn bool, wantIndex string, withLive bool) []provimuxKeyModelListRow {
	rows := []provimuxKeyModelListRow{}
	source := currentProvimuxKeyModelListSource()
	manager := h.provimuxAuthManager()
	if source == nil || manager == nil || cfg == nil {
		return rows
	}
	modelRegistry := registry.GetGlobalRegistry()
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		index := lockedAuthIndex(auth)
		withModels := wantIndex != "" && index == wantIndex
		if wantIndex != "" && !withModels {
			continue
		}
		state, key, ok := source.KeyModelList(cfg, auth, liveListsOn, withModels, withLive)
		if !ok {
			continue
		}
		row := provimuxKeyModelListRow{provimuxModelListRow: provimuxModelListRowOf(auth, index, state, len(modelRegistry.GetModelsForClient(auth.ID)))}
		row.Provider = key.Name
		row.Key = provimuxKeyModelListKey{Type: key.Type, Name: key.Name, Fixed: key.Fixed, Configured: key.Configured,
			Learned: provimuxStrings(key.Learned)}
		if key.NotAsked != "" {
			row.Key.NotAsked = provimuxString(key.NotAsked)
		}
		if key.Unlisted >= 0 {
			unlisted := key.Unlisted
			row.Key.Unlisted = &unlisted
			row.Key.UnlistedOf = provimuxStrings(key.UnlistedRows)
		}
		if withModels {
			models := provimuxKeyModelListModelsOf(state.Models)
			row.Models = &models
		}
		if withLive {
			row.LiveList = json.RawMessage("null")
			if state.Live != nil {
				row.LiveList = provimuxVendorModelsOf(state.Live)
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.Key.Type != right.Key.Type {
			return left.Key.Type < right.Key.Type
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return provimuxValue(left.AuthIndex) < provimuxValue(right.AuthIndex)
	})
	return rows
}

func provimuxKeyModelListModelsOf(models []ProvimuxModelListModel) []provimuxKeyModelListModel {
	listed := make(map[string]*bool, len(models))
	for _, model := range models {
		listed[model.ID] = model.Listed
	}
	base := provimuxModelListModelsOf(models)
	out := make([]provimuxKeyModelListModel, 0, len(base))
	for _, model := range base {
		out = append(out, provimuxKeyModelListModel{provimuxModelListModel: model, Listed: listed[model.ID]})
	}
	return out
}
