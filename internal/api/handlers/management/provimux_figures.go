package management

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/figures (prices-and-cache.md 6.3; steps F2 and F3). The figures
// Provimux holds from providers.dev, how it got them, and its last failure. Read-only: it starts no fetch
// and predicts nothing. Contract: .github/provimux/design/prices-and-cache/openapi.yaml, FiguresStatus.
//
// figures.entries holds every held entry with ?all=true. Without it, the entries an `answered` item of a route of
// GET /v8/management/provimux/model-routes names (provimuxFiguresSelected): the routes are built as model-routes
// builds them, so under the same "serve only my names" rule. The figures object is modellists.BuildFiguresView,
// the builder /v1/models?include=figures uses.

// ProvimuxFiguresState is the held figures as sdk/cliproxy holds them.
type ProvimuxFiguresState struct {
	State     string // held or none; the handler says off when the switch is off
	Document  *modellists.FiguresDocument
	FromCache bool
	Failure   *ProvimuxLiveListFailure
}

var (
	provimuxFiguresSourceMu sync.RWMutex
	provimuxFiguresSource   func() ProvimuxFiguresState
)

// SetProvimuxFiguresSource registers the reader of the held figures; nil removes it.
func SetProvimuxFiguresSource(source func() ProvimuxFiguresState) {
	provimuxFiguresSourceMu.Lock()
	defer provimuxFiguresSourceMu.Unlock()
	provimuxFiguresSource = source
}

type provimuxFiguresResponse struct {
	ObservedAt  string                   `json:"observed_at"`
	Options     provimuxFiguresOptions   `json:"options"`
	Figures     modellists.FiguresView   `json:"figures"`
	HeldEntries int                      `json:"held_entries"`
	FromCache   bool                     `json:"from_cache"`
	LastFailure *provimuxLiveListFailure `json:"last_failure"`
}

type provimuxFiguresOptions struct {
	PublishedLists bool `json:"published_lists"`
}

// ProvimuxFigures handles GET /v8/management/provimux/figures.
func (h *Handler) ProvimuxFigures(c *gin.Context) {
	on := registry.CatalogSources{}.PublishedListsOn()
	if cfg := h.configSnapshot(); cfg != nil {
		on = cfg.Models.PublishedListsOn() && !cfg.Home.Enabled
	}
	all, _ := strconv.ParseBool(strings.TrimSpace(c.Query("all")))
	now := provimuxNow()
	selected := modellists.SelectAll
	if !all {
		selected = nil
		if on { // off gives no figure: the routes are not built for nothing
			selected = provimuxFiguresSelected(h.provimuxAuthManager(), now)
		}
	}
	c.JSON(http.StatusOK, provimuxFiguresOf(on, selected, now))
}

// provimuxFiguresOf is the answer, with the entries selected gives (prices-and-cache.md 6.3).
func provimuxFiguresOf(on bool, selected func(string) bool, now time.Time) provimuxFiguresResponse {
	response := provimuxFiguresResponse{
		ObservedAt: provimuxTime(now.UTC()),
		Options:    provimuxFiguresOptions{PublishedLists: on},
		Figures:    modellists.BuildFiguresView(false, nil, nil),
	}
	if !on {
		// Off: no figure is given, whatever this process still holds from before the switch.
		return response
	}
	provimuxFiguresSourceMu.RLock()
	source := provimuxFiguresSource
	provimuxFiguresSourceMu.RUnlock()
	state := ProvimuxFiguresState{State: modellists.FiguresStateNone}
	if source != nil {
		state = source()
	}
	if state.Failure != nil {
		failure := &provimuxLiveListFailure{At: provimuxTime(state.Failure.At), Category: state.Failure.Category}
		if state.Failure.HTTPStatus > 0 {
			status := state.Failure.HTTPStatus
			failure.HTTPStatus = &status
		}
		response.LastFailure = failure
	}
	doc := state.Document
	if state.State != modellists.FiguresStateHeld {
		doc = nil
	}
	response.Figures = modellists.BuildFiguresView(true, doc, selected)
	if doc != nil {
		response.FromCache = state.FromCache
		response.HeldEntries = len(doc.Entries)
	}
	return response
}

// provimuxFiguresSelected is the selection without ?all=true: the entries an `answered` item of a route names, the
// routes built as GET /v8/management/provimux/model-routes builds them (every model, the same rule of "serve only
// my names", provimuxGated). Without a manager there is no route, so nothing is selected.
func provimuxFiguresSelected(manager *coreauth.Manager, now time.Time) func(string) bool {
	if manager == nil {
		return nil
	}
	return modellists.SelectEntries(provimuxAnsweredEntries(provimuxBuildModelRoutes(manager, nil, now.UTC())))
}
