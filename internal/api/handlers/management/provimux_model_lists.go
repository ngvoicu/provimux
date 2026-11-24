package management

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/model-lists (model-lists.md 5.5). Per login, which
// source decides its models now (its vendor's live list, the published list, the built-in list or
// none), the state of its live list, and how many of its models the catalog lacks. With
// ?auth_index= one login's models with the source of each field; with ?view=live each login's
// list as its vendor returned it, unmerged (what groundtruth's discovery reads). Read-only: it
// reports recorded state, starts no fetch and predicts nothing. Contract:
// .github/provimux/design/pui13/openapi.yaml. GET /provimux/live-lists stays for released consoles.

// ProvimuxModelListLogin is one login's model-list state as sdk/cliproxy records it.
type ProvimuxModelListLogin struct {
	// Source: "live", "published", "built-in" or "none".
	Source string
	// Supported: the vendor has a list and this login is asked.
	Supported bool
	Held      bool      // a live list of this account is in use
	Expired   bool      // a list of this account is held but older than 7 days, so not in use
	FetchedAt time.Time // the newest list of this account, zero without one
	FromCache bool      // that list was read from the disk cache and not fetched by this process yet
	Fetching  bool
	Listed    int // models the vendor's list named; valid when FetchedAt is set
	Failure   *ProvimuxLiveListFailure
	Learned   int // registered models of this login the catalog lacks
	// Models: one row per registered model, filled when asked for one login.
	Models []ProvimuxModelListModel
	// Live: the vendor's list as it arrived, filled for ?view=live; nil without a list.
	Live []ProvimuxVendorModel
}

// ProvimuxModelListModel is one registered model of a login and where its values came from.
type ProvimuxModelListModel struct {
	ID      string
	Name    string
	Source  string // live, built-in, provimux (added by Provimux itself), other (config, plugin, alias)
	Learned bool
	Context int
	Output  int
	Inputs  []string
	Outputs []string
	Levels  []string
	Sources map[string]string // per field: live, built-in; missing = no source
	Listed  *bool             // a key's model: whether its service's held list names it; nil = unknown
}

// ProvimuxVendorModel is one entry of a vendor's list as it arrived. Zero means not stated.
type ProvimuxVendorModel struct {
	ID      string
	Name    string
	Context int
	Output  int
	Inputs  []string
	Outputs []string
	Levels  []string
}

// ProvimuxModelListSource reads the model-list state. sdk/cliproxy provides it.
type ProvimuxModelListSource interface {
	// LoginTypes are the providers whose credentials get a row.
	LoginTypes() []string
	// ModelListLogin reports one credential's state; false when it gets no row.
	ModelListLogin(auth *coreauth.Auth, liveListsOn, withModels, withLive bool) (ProvimuxModelListLogin, bool)
}

var (
	provimuxModelListSourceMu sync.RWMutex
	provimuxModelListSource   ProvimuxModelListSource
	provimuxPublishedSource   func() ProvimuxPublishedState
)

// ProvimuxPublishedState is providers.dev's document as sdk/cliproxy holds it (step P3).
type ProvimuxPublishedState struct {
	State          string // held or none; the handler says off when the option is off
	DatasetVersion string
	GeneratedAt    time.Time
	FetchedAt      time.Time
	FromCache      bool
	Failure        *ProvimuxLiveListFailure
}

// SetProvimuxPublishedStateSource registers the reader of providers.dev's document; nil removes it.
func SetProvimuxPublishedStateSource(source func() ProvimuxPublishedState) {
	provimuxModelListSourceMu.Lock()
	defer provimuxModelListSourceMu.Unlock()
	provimuxPublishedSource = source
}

// provimuxPublishedOf is the response's published block.
func provimuxPublishedOf(on bool) provimuxModelListPublished {
	provimuxModelListSourceMu.RLock()
	source := provimuxPublishedSource
	provimuxModelListSourceMu.RUnlock()
	if !on || source == nil {
		return provimuxModelListPublished{State: "off"}
	}
	state := source()
	out := provimuxModelListPublished{State: state.State, FromCache: state.FromCache}
	if out.State == "" {
		out.State = "none"
	}
	if state.DatasetVersion != "" {
		out.DatasetVersion = provimuxString(state.DatasetVersion)
	}
	if !state.GeneratedAt.IsZero() {
		out.GeneratedAt = provimuxString(provimuxTime(state.GeneratedAt))
	}
	if !state.FetchedAt.IsZero() {
		out.FetchedAt = provimuxString(provimuxTime(state.FetchedAt))
	}
	if state.Failure != nil {
		failure := &provimuxLiveListFailure{At: provimuxTime(state.Failure.At), Category: state.Failure.Category}
		if state.Failure.HTTPStatus > 0 {
			status := state.Failure.HTTPStatus
			failure.HTTPStatus = &status
		}
		out.LastFailure = failure
	}
	return out
}

// SetProvimuxModelListSource registers the source; nil removes it.
func SetProvimuxModelListSource(source ProvimuxModelListSource) {
	provimuxModelListSourceMu.Lock()
	defer provimuxModelListSourceMu.Unlock()
	provimuxModelListSource = source
}

func currentProvimuxModelListSource() ProvimuxModelListSource {
	provimuxModelListSourceMu.RLock()
	defer provimuxModelListSourceMu.RUnlock()
	return provimuxModelListSource
}

type provimuxModelListsResponse struct {
	ObservedAt string                     `json:"observed_at"`
	Options    provimuxModelListOptions   `json:"options"`
	BuiltIn    provimuxModelListBuiltIn   `json:"built_in"`
	Published  provimuxModelListPublished `json:"published"`
	LoginTypes []string                   `json:"login_types"`
	Logins     []provimuxModelListRow     `json:"logins"`
	Keys       []provimuxKeyModelListRow  `json:"keys"` // config API keys (key-model-lists.md 7.1)
}

type provimuxModelListOptions struct {
	LiveLists      bool `json:"live_lists"`
	PublishedLists bool `json:"published_lists"`
}

type provimuxModelListBuiltIn struct {
	DatasetVersion *string `json:"dataset_version"`
	GeneratedAt    *string `json:"generated_at"`
}

type provimuxModelListPublished struct {
	State          string                   `json:"state"`
	DatasetVersion *string                  `json:"dataset_version"`
	GeneratedAt    *string                  `json:"generated_at"`
	FetchedAt      *string                  `json:"fetched_at"`
	FromCache      bool                     `json:"from_cache"`
	LastFailure    *provimuxLiveListFailure `json:"last_failure"`
}

type provimuxModelListRow struct {
	AuthIndex        *string                   `json:"auth_index"`
	Provider         string                    `json:"provider"`
	Login            provimuxLogin             `json:"login"`
	Disabled         bool                      `json:"disabled"`
	Source           string                    `json:"source"`
	Live             provimuxModelListLive     `json:"live"`
	RegisteredModels int                       `json:"registered_models"`
	LearnedModels    int                       `json:"learned_models"`
	Models           *[]provimuxModelListModel `json:"models"`
	LiveList         any                       `json:"live_list,omitempty"` // only with ?view=live
}

type provimuxModelListLive struct {
	Supported    bool                     `json:"supported"`
	Held         bool                     `json:"held"`
	Expired      bool                     `json:"expired"`
	FetchedAt    *string                  `json:"fetched_at"`
	FromCache    bool                     `json:"from_cache"`
	Fetching     bool                     `json:"fetching"`
	ListedModels *int                     `json:"listed_models"`
	LastFailure  *provimuxLiveListFailure `json:"last_failure"`
}

type provimuxModelListModel struct {
	ID          string                    `json:"id"`
	DisplayName *string                   `json:"display_name"`
	Source      string                    `json:"source"`
	Learned     bool                      `json:"learned"`
	Limits      provimuxLimits            `json:"limits"`
	Modalities  provimuxModelListModality `json:"modalities"`
	Levels      []string                  `json:"reasoning_levels"`
	Sources     provimuxModelListSources  `json:"sources"`
}

type provimuxModelListModality struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type provimuxModelListSources struct {
	DisplayName      *string `json:"display_name"`
	Context          *string `json:"context"`
	Output           *string `json:"output"`
	InputModalities  *string `json:"input_modalities"`
	OutputModalities *string `json:"output_modalities"`
	ReasoningLevels  *string `json:"reasoning_levels"`
}

type provimuxVendorModel struct {
	ID          string   `json:"id"`
	DisplayName *string  `json:"display_name"`
	Context     *int     `json:"context"`
	Output      *int     `json:"output"`
	Input       []string `json:"input_modalities"`
	OutputMods  []string `json:"output_modalities"`
	Levels      []string `json:"reasoning_levels"`
}

// ProvimuxModelLists handles GET /v8/management/provimux/model-lists.
func (h *Handler) ProvimuxModelLists(c *gin.Context) {
	source := currentProvimuxModelListSource()
	if source == nil {
		// Not 404: the console must not take this for an absent endpoint.
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "model-list status unavailable"})
		return
	}
	wantIndex := strings.TrimSpace(c.Query("auth_index"))
	withLive := strings.EqualFold(strings.TrimSpace(c.Query("view")), "live")
	liveListsOn := registry.CatalogSources{}.LiveListsOn()
	publishedOn := registry.CatalogSources{}.PublishedListsOn()
	cfg := h.configSnapshot()
	if cfg != nil {
		liveListsOn = cfg.Models.LiveListsOn()
		publishedOn = cfg.Models.PublishedListsOn() && !cfg.Home.Enabled
	}
	response := provimuxModelListsResponse{
		ObservedAt: provimuxTime(provimuxNow().UTC()),
		Options:    provimuxModelListOptions{LiveLists: liveListsOn, PublishedLists: publishedOn},
		// The built-in lists are the catalog's, which names no providers.dev dataset (P1 dropped).
		Published:  provimuxPublishedOf(publishedOn),
		LoginTypes: append([]string{}, source.LoginTypes()...),
		Logins:     []provimuxModelListRow{},
	}
	response.Keys = h.provimuxKeyModelListRows(cfg, liveListsOn, wantIndex, withLive)
	if manager := h.provimuxAuthManager(); manager != nil {
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
			state, ok := source.ModelListLogin(auth, liveListsOn, withModels, withLive)
			if !ok {
				continue
			}
			row := provimuxModelListRowOf(auth, index, state, len(modelRegistry.GetModelsForClient(auth.ID)))
			if withModels {
				models := provimuxModelListModelsOf(state.Models)
				row.Models = &models
			}
			if withLive {
				// Present on every row of ?view=live: the list, or null without one.
				row.LiveList = json.RawMessage("null")
				if state.Live != nil {
					row.LiveList = provimuxVendorModelsOf(state.Live)
				}
			}
			response.Logins = append(response.Logins, row)
		}
	}
	sort.SliceStable(response.Logins, func(i, j int) bool {
		left, right := response.Logins[i], response.Logins[j]
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if leftName, rightName := strings.ToLower(provimuxValue(left.Login.Name)), strings.ToLower(provimuxValue(right.Login.Name)); leftName != rightName {
			return leftName < rightName
		}
		return provimuxValue(left.AuthIndex) < provimuxValue(right.AuthIndex)
	})
	c.JSON(http.StatusOK, response)
}

func provimuxModelListRowOf(auth *coreauth.Auth, index string, state ProvimuxModelListLogin, registered int) provimuxModelListRow {
	row := provimuxModelListRow{
		AuthIndex:        provimuxString(index),
		Provider:         strings.ToLower(strings.TrimSpace(auth.Provider)),
		Login:            provimuxLoginOf(auth),
		Disabled:         auth.Disabled,
		Source:           state.Source,
		RegisteredModels: registered,
		LearnedModels:    state.Learned,
		Live: provimuxModelListLive{Supported: state.Supported, Held: state.Held, Expired: state.Expired,
			FromCache: state.FromCache, Fetching: state.Fetching},
	}
	if !state.FetchedAt.IsZero() {
		row.Live.FetchedAt = provimuxString(provimuxTime(state.FetchedAt))
		listed := state.Listed
		row.Live.ListedModels = &listed
	}
	if state.Failure != nil {
		failure := &provimuxLiveListFailure{At: provimuxTime(state.Failure.At), Category: state.Failure.Category}
		if state.Failure.HTTPStatus > 0 {
			status := state.Failure.HTTPStatus
			failure.HTTPStatus = &status
		}
		row.Live.LastFailure = failure
	}
	return row
}

func provimuxOptionalInt(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func provimuxStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
}

func provimuxModelListModelsOf(models []ProvimuxModelListModel) []provimuxModelListModel {
	out := make([]provimuxModelListModel, 0, len(models))
	for _, model := range models {
		field := func(name string) *string { return provimuxString(model.Sources[name]) }
		out = append(out, provimuxModelListModel{
			ID: model.ID, DisplayName: provimuxString(model.Name), Source: model.Source, Learned: model.Learned,
			Limits:     provimuxLimits{Context: provimuxOptionalInt(model.Context), Output: provimuxOptionalInt(model.Output)},
			Modalities: provimuxModelListModality{Input: provimuxStrings(model.Inputs), Output: provimuxStrings(model.Outputs)},
			Levels:     provimuxStrings(model.Levels),
			Sources: provimuxModelListSources{DisplayName: field("display_name"), Context: field("context"), Output: field("output"),
				InputModalities: field("input_modalities"), OutputModalities: field("output_modalities"), ReasoningLevels: field("reasoning_levels")},
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func provimuxVendorModelsOf(models []ProvimuxVendorModel) []provimuxVendorModel {
	out := make([]provimuxVendorModel, 0, len(models))
	for _, model := range models {
		out = append(out, provimuxVendorModel{ID: model.ID, DisplayName: provimuxString(model.Name),
			Context: provimuxOptionalInt(model.Context), Output: provimuxOptionalInt(model.Output),
			Input: provimuxStrings(model.Inputs), OutputMods: provimuxStrings(model.Outputs), Levels: provimuxStrings(model.Levels)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
