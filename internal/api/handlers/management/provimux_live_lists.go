package management

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/live-lists. Per Claude, Codex and Meta login, whether
// its models come from the list its provider returned (models.live-lists) or from the catalog, when
// that list was fetched, the last failed fetch and the next retry. Read-only; it starts no fetch.
// Contract: .github/provimux/design/pui13/openapi.yaml.

// ProvimuxLiveListState is one login's live model list as the fetcher holds it.
type ProvimuxLiveListState struct {
	Covered      bool      // the login's provider has live lists and it is not an API key
	HasList      bool      // a list of this account is held
	FetchedAt    time.Time // zero without a list
	ListedModels int
	Fetching     bool
	Failure      *ProvimuxLiveListFailure // the current account's last failed fetch; nil for an earlier account's
}

// ProvimuxLiveListFailure describes the latest fetch attempt of the login's account when it failed.
// It holds codes only, never error text.
type ProvimuxLiveListFailure struct {
	At         time.Time
	Category   string // http_status, credentials, invalid_response, network, other
	HTTPStatus int    // the provider's status for http_status, else 0
}

// ProvimuxLiveListSource reads the live-model-list fetcher's state. sdk/cliproxy provides it, which
// this package cannot import.
type ProvimuxLiveListSource interface {
	Providers() []string
	State(auth *coreauth.Auth) ProvimuxLiveListState
}

var (
	provimuxLiveListSourceMu sync.RWMutex
	provimuxLiveListSource   ProvimuxLiveListSource
)

// SetProvimuxLiveListSource registers the source; nil removes it.
func SetProvimuxLiveListSource(source ProvimuxLiveListSource) {
	provimuxLiveListSourceMu.Lock()
	defer provimuxLiveListSourceMu.Unlock()
	provimuxLiveListSource = source
}

func currentProvimuxLiveListSource() ProvimuxLiveListSource {
	provimuxLiveListSourceMu.RLock()
	defer provimuxLiveListSourceMu.RUnlock()
	return provimuxLiveListSource
}

type provimuxLiveListsResponse struct {
	ObservedAt string              `json:"observed_at"`
	Enabled    bool                `json:"enabled"`
	Providers  []string            `json:"providers"`
	Logins     []provimuxLiveLogin `json:"logins"`
}

type provimuxLiveLogin struct {
	AuthIndex        *string                  `json:"auth_index"`
	Provider         string                   `json:"provider"`
	Login            provimuxLogin            `json:"login"`
	Disabled         bool                     `json:"disabled"`
	Source           string                   `json:"source"`
	Fetching         bool                     `json:"fetching"`
	FetchedAt        *string                  `json:"fetched_at"`
	ListedModels     *int                     `json:"listed_models"`
	RegisteredModels int                      `json:"registered_models"`
	LastFailure      *provimuxLiveListFailure `json:"last_failure"`
}

type provimuxLiveListFailure struct {
	At         string `json:"at"`
	Category   string `json:"category"`
	HTTPStatus *int   `json:"http_status"`
}

// ProvimuxLiveLists handles GET /v8/management/provimux/live-lists.
func (h *Handler) ProvimuxLiveLists(c *gin.Context) {
	source := currentProvimuxLiveListSource()
	if source == nil {
		// Not 404: the console must not take this for an absent endpoint.
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "live-list status unavailable"})
		return
	}
	now := provimuxNow().UTC()
	enabled := false
	if cfg := h.configSnapshot(); cfg != nil {
		enabled = cfg.Models.LiveListsOn()
	}
	response := provimuxLiveListsResponse{
		ObservedAt: provimuxTime(now),
		Enabled:    enabled,
		Providers:  append([]string{}, source.Providers()...),
		Logins:     []provimuxLiveLogin{},
	}
	if manager := h.provimuxAuthManager(); manager != nil {
		modelRegistry := registry.GetGlobalRegistry()
		for _, auth := range manager.List() {
			if auth == nil {
				continue
			}
			state := source.State(auth)
			if !state.Covered {
				continue
			}
			response.Logins = append(response.Logins, provimuxLiveLoginOf(auth, state, enabled, len(modelRegistry.GetModelsForClient(auth.ID))))
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

func provimuxLiveLoginOf(auth *coreauth.Auth, state ProvimuxLiveListState, enabled bool, registered int) provimuxLiveLogin {
	row := provimuxLiveLogin{
		AuthIndex:        provimuxString(lockedAuthIndex(auth)),
		Provider:         auth.Provider,
		Login:            provimuxLoginOf(auth),
		Disabled:         auth.Disabled,
		Fetching:         state.Fetching,
		RegisteredModels: registered,
	}
	if state.HasList {
		row.FetchedAt = provimuxString(provimuxTime(state.FetchedAt))
		listed := state.ListedModels
		row.ListedModels = &listed
	}
	if state.Failure != nil {
		failure := &provimuxLiveListFailure{At: provimuxTime(state.Failure.At), Category: state.Failure.Category}
		if state.Failure.HTTPStatus > 0 {
			status := state.Failure.HTTPStatus
			failure.HTTPStatus = &status
		}
		row.LastFailure = failure
	}
	// The fetcher's own test (applyLiveModelList): a held list counts only while the option is on.
	row.Source = "catalog"
	if enabled && state.HasList {
		row.Source = "live"
	}
	return row
}
