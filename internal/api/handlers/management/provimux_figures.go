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
)

// Provimux-only: GET /v8/management/provimux/figures (prices-and-cache.md 6.3; step F2). The figures
// Provimux holds from providers.dev, how it got them, and its last failure. Read-only: it starts no fetch
// and predicts nothing. Contract: .github/provimux/design/prices-and-cache/openapi.yaml, FiguresStatus.
//
// figures.entries holds every held entry with ?all=true. Without it, the design's selection is the entries
// an answered item of a route of GET /v8/management/provimux/model-routes names; that record does not exist
// yet (W6b builds it), so the selection is empty until it does (provimuxFiguresSelected).

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
	Figures     provimuxFigures          `json:"figures"`
	HeldEntries int                      `json:"held_entries"`
	FromCache   bool                     `json:"from_cache"`
	LastFailure *provimuxLiveListFailure `json:"last_failure"`
}

type provimuxFiguresOptions struct {
	PublishedLists bool `json:"published_lists"`
}

type provimuxFigures struct {
	Contract       int                           `json:"contract"`
	Source         string                        `json:"source"`
	State          string                        `json:"state"`
	DatasetVersion *string                       `json:"dataset_version"`
	GeneratedAt    *string                       `json:"generated_at"`
	FetchedAt      *string                       `json:"fetched_at"`
	Entries        map[string]modellists.Figures `json:"entries"`
}

// ProvimuxFigures handles GET /v8/management/provimux/figures.
func (h *Handler) ProvimuxFigures(c *gin.Context) {
	on := registry.CatalogSources{}.PublishedListsOn()
	if cfg := h.configSnapshot(); cfg != nil {
		on = cfg.Models.PublishedListsOn() && !cfg.Home.Enabled
	}
	all, _ := strconv.ParseBool(strings.TrimSpace(c.Query("all")))
	c.JSON(http.StatusOK, provimuxFiguresOf(on, all, provimuxNow()))
}

func provimuxFiguresOf(on, all bool, now time.Time) provimuxFiguresResponse {
	response := provimuxFiguresResponse{
		ObservedAt: provimuxTime(now.UTC()),
		Options:    provimuxFiguresOptions{PublishedLists: on},
		Figures: provimuxFigures{Contract: modellists.FiguresContract, Source: modellists.FiguresSource, State: "off",
			Entries: map[string]modellists.Figures{}},
	}
	if !on {
		// Off: no figure is given, whatever this process still holds from before the switch.
		return response
	}
	provimuxFiguresSourceMu.RLock()
	source := provimuxFiguresSource
	provimuxFiguresSourceMu.RUnlock()
	state := ProvimuxFiguresState{State: "none"}
	if source != nil {
		state = source()
	}
	response.Figures.State = "none"
	response.FromCache = state.FromCache
	if state.Failure != nil {
		failure := &provimuxLiveListFailure{At: provimuxTime(state.Failure.At), Category: state.Failure.Category}
		if state.Failure.HTTPStatus > 0 {
			status := state.Failure.HTTPStatus
			failure.HTTPStatus = &status
		}
		response.LastFailure = failure
	}
	doc := state.Document
	if state.State != "held" || doc == nil {
		response.FromCache = false
		return response
	}
	response.Figures.State = "held"
	response.Figures.DatasetVersion = provimuxString(doc.DatasetVersion)
	response.Figures.GeneratedAt = provimuxString(provimuxTime(doc.GeneratedAt))
	response.Figures.FetchedAt = provimuxString(provimuxTime(doc.FetchedAt))
	response.HeldEntries = len(doc.Entries)
	for ref, entry := range doc.Entries {
		if all || provimuxFiguresSelected(ref) {
			response.Figures.Entries[ref] = entry
		}
	}
	return response
}

// provimuxFiguresSelected reports whether an entry is one an answered item of a route of model-routes names
// (prices-and-cache.md 6.3). No route carries answered yet: W6b adds it to provimuxRoute, and this is where
// the two join, under the same "serve only my names" rule model-routes applies (provimuxGated). Until then
// no entry is selected, and ?all=true gives every held entry.
func provimuxFiguresSelected(string) bool { return false }
