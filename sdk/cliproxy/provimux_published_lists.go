package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// Provimux: providers.dev's model lists (model-lists.md step P3, sections 5.1 to 5.4).
//
// providers.dev fills gaps only, never overrides. A model the catalog has: the vendor's list where the
// catalog leaves a field empty (13.1), then the catalog, then providers.dev for a field both leave empty
// (the registry's catalog overlay, provimuxCatalogOverlayOf; never the thinking block or web search). A
// model the catalog lacks (a learned model): the vendor's live list first; then providers.dev's listing,
// for every field the vendor's list does not state, within the guards of provimuxComposeLearned; then the
// parser's placeholder (a name equal to the id, text output), never an invented limit. Which models a login offers is not decided here: its live list, else the
// catalog.
//
// Start never waits for providers.dev: the last accepted document is read from disk with the live
// lists, and the fetch runs in the background, at start, every 6 hours and on
// POST /v8/management/models/refresh. A 304 changes nothing. After a failure: 10 minutes, then
// 1 hour, then the normal 6 hours; the held document stays.

const (
	provimuxPublishedInterval = 6 * time.Hour
	provimuxPublishedScan     = 10 * time.Minute
)

// provimuxPublishedBackoff is the wait after the 1st, 2nd, ... consecutive failure; past the end, the
// normal interval.
var provimuxPublishedBackoff = []time.Duration{10 * time.Minute, time.Hour}

// provimuxPublishedBase is providers.dev's listings resource; tests point it at a test server.
var provimuxPublishedBase = modellists.PublishedURL

var provimuxPublishedFetchTimeout = 30 * time.Second

type provimuxPublishedStore struct {
	on atomic.Bool // models.published-lists, outside home mode; set from the service's config

	mu         sync.Mutex
	doc        *modellists.Document
	fromCache  bool
	cacheRead  bool
	failure    *management.ProvimuxLiveListFailure
	failures   int       // consecutive
	next       time.Time // the next fetch is due
	index      map[string]map[string]*modellists.ListingModel
	wake       chan struct{}
	running    atomic.Bool
	fetching   sync.Mutex // one fetch at a time
	lastLogged string     // dataset version of the last accepted-document line
}

var provimuxPublished = &provimuxPublishedStore{wake: make(chan struct{}, 1)}

// provimuxPublishedListsOn is the option for this process: on, outside home mode.
func provimuxPublishedListsOn(cfg *config.Config) bool {
	if cfg == nil {
		return registry.CatalogSources{}.PublishedListsOn()
	}
	return cfg.Models.PublishedListsOn() && !cfg.Home.Enabled
}

// setDoc installs a document and its index. Callers hold mu.
func (st *provimuxPublishedStore) setDocLocked(doc *modellists.Document, fromCache bool) {
	st.doc, st.fromCache = doc, fromCache
	st.index = map[string]map[string]*modellists.ListingModel{}
	if doc == nil {
		return
	}
	for provider, listing := range doc.Listings {
		byKey := map[string]*modellists.ListingModel{}
		for i := range listing.Models {
			model := &listing.Models[i]
			for _, id := range append([]string{model.ID}, model.Aliases...) {
				key := provimuxPublishedKey(provider, id)
				if _, dup := byKey[key]; key != "" && !dup {
					byKey[key] = model
				}
			}
		}
		st.index[provider] = byKey
	}
}

// provimuxPublishedKey is the key a listing row and a learned model are matched by: lower case, and for
// Devin without the devin/ namespace.
func provimuxPublishedKey(provider, id string) string {
	key := strings.ToLower(strings.TrimSpace(id))
	if provider == "devin" {
		key = strings.TrimPrefix(key, "devin/")
	}
	return key
}

// loadCache reads the disk copy once, before any request. A damaged or foreign file is ignored with
// one warning (by the store).
func (st *provimuxPublishedStore) loadCache(cache *modellists.Store) {
	if cache == nil {
		return
	}
	st.mu.Lock()
	if st.cacheRead || st.doc != nil {
		st.cacheRead = true
		st.mu.Unlock()
		return
	}
	st.cacheRead = true
	st.mu.Unlock()
	doc, err := cache.ReadPublished()
	if err != nil || doc == nil {
		return
	}
	st.mu.Lock()
	if st.doc != nil { // a fetch finished meanwhile
		st.mu.Unlock()
		return
	}
	st.setDocLocked(doc, true)
	st.mu.Unlock()
	registry.ProvimuxCatalogOverlayChanged() // never with mu held: the overlay takes it
	log.Infof("model lists from providers.dev: dataset %s of %s, %d listings, read from the disk cache",
		provimuxShortVersion(doc.DatasetVersion), doc.GeneratedAt.UTC().Format(time.DateOnly), len(doc.Listings))
}

// lookup returns providers.dev's row for a learned model of a login type, or nil (option off, no
// document, no listing, no row).
func (st *provimuxPublishedStore) lookup(section, id string) *modellists.ListingModel {
	if !st.on.Load() {
		return nil
	}
	provider := modellists.ListingProvider(section)
	if provider == "" {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if model := st.index[provider][provimuxPublishedKey(provider, id)]; model != nil {
		copied := *model
		return &copied
	}
	return nil
}

func (st *provimuxPublishedStore) request() {
	st.mu.Lock()
	st.next = time.Time{}
	st.mu.Unlock()
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// due reports whether a fetch should run now.
func (st *provimuxPublishedStore) due(now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !now.Before(st.next)
}

// runPublishedListRefresh is the background fetcher. It never blocks the start.
func (s *Service) runPublishedListRefresh(ctx context.Context) {
	if !provimuxPublished.running.CompareAndSwap(false, true) {
		return
	}
	defer provimuxPublished.running.Store(false)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-provimuxPublished.wake:
		case <-timer.C:
			timer.Reset(provimuxPublishedScan)
		}
		s.cfgMu.RLock()
		cfg := s.cfg
		s.cfgMu.RUnlock()
		on := provimuxPublishedListsOn(cfg)
		if provimuxSetPublishedOn(on) {
			// Turned on or off: the learned models' fields change with it.
			s.provimuxPublishedChanged(provimuxAllSections())
		}
		if !on || !provimuxPublished.due(liveModelListNow()) {
			continue
		}
		s.refreshPublishedLists(ctx, cfg)
	}
}

// refreshPublishedLists sends the one request and applies its answer.
func (s *Service) refreshPublishedLists(ctx context.Context, cfg *config.Config) {
	st := provimuxPublished
	st.fetching.Lock()
	defer st.fetching.Unlock()
	st.mu.Lock()
	held := st.doc
	st.mu.Unlock()
	etag := ""
	if held != nil {
		etag = held.ETag
	}
	client := &http.Client{}
	if cfg != nil {
		util.SetProxy(&cfg.SDKConfig, client)
	}
	fetchCtx, cancel := context.WithTimeout(ctx, provimuxPublishedFetchTimeout)
	defer cancel()
	now := liveModelListNow()
	result, err := modellists.Fetch(fetchCtx, client, provimuxPublishedBase, etag, "provimux/"+buildinfo.DisplayVersion(), now)
	if ctx.Err() != nil {
		return
	}
	var merged *modellists.Document
	var changed []string
	if err == nil && !result.NotModified {
		merged, changed, err = modellists.Accept(held, result.Document)
	}
	if err != nil {
		s.provimuxPublishedFailed(err, held, now)
		return
	}
	st.mu.Lock()
	st.failure, st.failures = nil, 0
	st.next = now.Add(provimuxPublishedInterval)
	if result.NotModified {
		// 304: the held document is current. Nothing else changes.
		st.fromCache = false
		st.mu.Unlock()
		return
	}
	st.setDocLocked(merged, false)
	logged := st.lastLogged == merged.DatasetVersion
	st.lastLogged = merged.DatasetVersion
	st.mu.Unlock()
	registry.ProvimuxCatalogOverlayChanged()
	if cache := liveModelLists.cache.Load(); cache != nil {
		_ = cache.WritePublished(merged)
	}
	if !logged {
		log.Infof("model lists from providers.dev: dataset %s of %s, %d listings", provimuxShortVersion(merged.DatasetVersion),
			merged.GeneratedAt.UTC().Format(time.DateOnly), len(merged.Listings))
	}
	for _, provider := range result.Bad {
		if _, kept := merged.Listings[provider]; kept {
			log.Warnf("model lists from providers.dev: listing %s missing or not valid; keeping the held one", provider)
		} else {
			log.Debugf("model lists from providers.dev: no listing %s yet", provider)
		}
	}
	var sections []string
	for _, provider := range changed {
		sections = append(sections, modellists.LoginTypesOf(provider)...)
	}
	s.provimuxPublishedChanged(sections)
}

func (s *Service) provimuxPublishedFailed(err error, held *modellists.Document, now time.Time) {
	st := provimuxPublished
	category, status := modellists.FailureNetwork, 0
	var fetchErr *modellists.FetchError
	switch {
	case errors.As(err, &fetchErr):
		category, status = fetchErr.Category, fetchErr.Status
	case errors.Is(err, modellists.ErrOlder):
		category = modellists.FailureResponse
	}
	st.mu.Lock()
	st.failure = &management.ProvimuxLiveListFailure{At: now, Category: category, HTTPStatus: status}
	wait := provimuxPublishedInterval
	if st.failures < len(provimuxPublishedBackoff) {
		wait = provimuxPublishedBackoff[st.failures]
	}
	st.failures++
	st.next = now.Add(wait)
	st.mu.Unlock()
	kept := "no providers.dev list is held; the vendors' lists and the catalog stay in use"
	if held != nil {
		kept = "keeping dataset " + provimuxShortVersion(held.DatasetVersion) + " of " + held.GeneratedAt.UTC().Format(time.DateOnly)
	}
	text := provimuxLiveFailureText(category, status)
	if errors.Is(err, modellists.ErrOlder) {
		text += ", older than the held document"
	}
	log.Warnf("model lists from providers.dev: fetch failed (%s); %s", text, kept)
}

// provimuxPublishedChanged recomputes the learned models of the sections and registers the models of
// their logins again, so a learned model's fields follow the new listing.
func (s *Service) provimuxPublishedChanged(sections []string) {
	if len(sections) == 0 {
		return
	}
	provimuxUpdateLearnedModels(sections...)
	if s == nil || s.coreManager == nil {
		return
	}
	want := map[string]bool{}
	for _, section := range sections {
		want[section] = true
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.Disabled {
			continue
		}
		section := provimuxModelListType(auth)
		if section == "" {
			// An API key: it registers the same catalog sections, so their overlay reaches it too.
			section = registry.ProvimuxLearnedSection(auth.Provider)
			if provider := strings.ToLower(strings.TrimSpace(auth.Provider)); provider == "vertex" || provider == "aistudio" {
				section = provider
			}
		}
		if want[section] {
			s.refreshModelRegistrationForAuth(auth)
		}
	}
}

// provimuxAllSections are the login types whose learned models providers.dev can fill.
func provimuxAllSections() []string {
	return append(append([]string(nil), liveModelListTypes...), "copilot", "antigravity", "vertex", "aistudio")
}

func provimuxShortVersion(version string) string {
	if len(version) > 8 {
		return version[:8] + "…"
	}
	if version == "" {
		return "?"
	}
	return version
}

// provimuxPublishedStateOf reports the held document for the status endpoint.
func provimuxPublishedStateOf() management.ProvimuxPublishedState {
	st := provimuxPublished
	st.mu.Lock()
	defer st.mu.Unlock()
	state := management.ProvimuxPublishedState{State: "none", FromCache: st.fromCache}
	if st.failure != nil {
		failure := *st.failure
		state.Failure = &failure
	}
	if st.doc != nil {
		state.State = "held"
		state.DatasetVersion = st.doc.DatasetVersion
		state.GeneratedAt = st.doc.GeneratedAt
		state.FetchedAt = st.doc.FetchedAt
	}
	return state
}

func init() { management.SetProvimuxPublishedStateSource(provimuxPublishedStateOf) }

// ---- the composition: providers.dev fills gaps only (model-lists.md 13.5)

// provimuxRowWindow is a listing row's context window: max_input where it is stated and smaller than
// context_window (or the only one stated), else context_window; 0 when the row states neither.
func provimuxRowWindow(row *modellists.ListingModel) int {
	window := 0
	if provimuxPositive(row.ContextWindow) {
		window = *row.ContextWindow
	}
	if provimuxPositive(row.MaxInput) && (window == 0 || *row.MaxInput < window) {
		window = *row.MaxInput
	}
	return window
}

// provimuxRowOutput is a listing row's output limit, or 0 when it states none or states one that is not
// below the window (window: the model's, else the row's own): such a figure is the window, not a limit.
func provimuxRowOutput(row *modellists.ListingModel, window int) int {
	if !provimuxPositive(row.MaxOutput) {
		return 0
	}
	if window <= 0 {
		window = provimuxRowWindow(row)
	}
	if window > 0 && *row.MaxOutput >= window {
		return 0
	}
	return *row.MaxOutput
}

// provimuxLiveStates reports whether the vendor's list stated a field of a learned model, as against
// leaving it empty or to the parser's placeholder: a name equal to the id, and "text" alone as the
// output, which every parser but xAI's writes without the vendor saying it.
func provimuxLiveStates(section string, live *ModelInfo, field string) bool {
	if live == nil {
		return false
	}
	switch field {
	case "display_name":
		return live.DisplayName != "" && !strings.EqualFold(strings.TrimSpace(live.DisplayName), strings.TrimSpace(live.ID))
	case "context":
		return provimuxFirstPositive(live.ContextLength, live.InputTokenLimit) > 0
	case "output":
		return provimuxFirstPositive(live.MaxCompletionTokens, live.OutputTokenLimit) > 0
	case "input_modalities":
		return len(live.SupportedInputModalities) > 0
	case "output_modalities":
		if len(live.SupportedOutputModalities) == 0 {
			return false
		}
		return section == "xai" || len(live.SupportedOutputModalities) > 1 || !strings.EqualFold(live.SupportedOutputModalities[0], "text")
	case "web_search":
		return live.NativeCapabilities != nil && live.NativeCapabilities.WebSearch != nil
	}
	return false
}

// provimuxComposeLearned returns a learned model (one the catalog lacks) with providers.dev's values in
// the fields the vendor's list does not state. Guards: the thinking block is taken whole from one source
// (the vendor's when it has one, else providers.dev's levels, budgets and dynamic flag; never
// "supported: false" and never "can_disable"); a max_output not below the window is refused; a stated
// max_input smaller than the context window is the window. Without a listing row it is the vendor's
// model, unchanged.
func provimuxComposeLearned(section string, live *ModelInfo) *ModelInfo {
	if live == nil {
		return nil
	}
	out := cloneLiveModel(live)
	if live.NativeCapabilities != nil {
		capabilities := *live.NativeCapabilities
		out.NativeCapabilities = &capabilities
	}
	row := provimuxPublished.lookup(section, live.ID)
	if row == nil {
		return out
	}
	if !provimuxLiveStates(section, live, "display_name") && row.Name != nil && strings.TrimSpace(*row.Name) != "" {
		out.DisplayName = strings.TrimSpace(*row.Name)
	}
	if window := provimuxRowWindow(row); !provimuxLiveStates(section, live, "context") && window > 0 {
		out.ContextLength = window
		if out.Type == "gemini" {
			out.InputTokenLimit = window
		}
	}
	if output := provimuxRowOutput(row, provimuxFirstPositive(out.ContextLength, out.InputTokenLimit)); !provimuxLiveStates(section, live, "output") && output > 0 {
		out.MaxCompletionTokens = output
		if out.Type == "gemini" || out.OutputTokenLimit > 0 || section == "copilot" {
			out.OutputTokenLimit = output
		}
	}
	if !provimuxLiveStates(section, live, "input_modalities") && len(row.InputModalities) > 0 {
		out.SupportedInputModalities = append([]string(nil), row.InputModalities...)
	}
	if !provimuxLiveStates(section, live, "output_modalities") && len(row.OutputModalities) > 0 {
		out.SupportedOutputModalities = append([]string(nil), row.OutputModalities...)
	}
	if live.Thinking == nil {
		out.Thinking = provimuxRowThinking(row.Reasoning)
	}
	if !provimuxLiveStates(section, live, "web_search") && row.WebSearch != nil {
		if out.NativeCapabilities == nil {
			out.NativeCapabilities = &registry.NativeCapabilities{}
		}
		webSearch := *row.WebSearch
		out.NativeCapabilities.WebSearch = &webSearch
	}
	return out
}

// provimuxRowThinking is a listing's thinking block, whole: its known levels, its budgets and its dynamic
// flag. Never "can_disable" (thinking that can be turned off) and never "supported: false": those are
// not taken from providers.dev. nil when the row states no level and no budget.
func provimuxRowThinking(reasoning *modellists.Reasoning) *registry.ThinkingSupport {
	if reasoning == nil {
		return nil
	}
	levels := provimuxKnownLevels(reasoning.Levels)
	if len(levels) == 0 && !provimuxPositive(reasoning.BudgetMax) {
		return nil
	}
	thinking := &registry.ThinkingSupport{Levels: levels}
	if provimuxPositive(reasoning.BudgetMax) {
		thinking.Max = *reasoning.BudgetMax
		if provimuxPositive(reasoning.BudgetMin) && *reasoning.BudgetMin <= thinking.Max {
			thinking.Min = *reasoning.BudgetMin
		}
	}
	if reasoning.Dynamic != nil {
		thinking.DynamicAllowed = *reasoning.Dynamic
	}
	return thinking
}

// provimuxKnownLevels keeps the effort levels Provimux knows, in its order.
func provimuxKnownLevels(levels []string) []string {
	return liveModelListLevels(func(level string) bool {
		for _, value := range levels {
			if strings.EqualFold(strings.TrimSpace(value), level) {
				return true
			}
		}
		return false
	})
}

func provimuxPositive(value *int) bool { return value != nil && *value > 0 }

// provimuxLearnedFieldSources names, for a learned model, the source of each field of the status
// endpoint: "live" where the vendor's list stated it, "published" where providers.dev filled it, "live"
// for the parser's placeholder that stayed, nothing where no source states it.
func provimuxLearnedFieldSources(out map[string]string, section string, live *ModelInfo) {
	if live == nil {
		return
	}
	composed := provimuxComposeLearned(section, live)
	for _, field := range []string{"display_name", "context", "output", "input_modalities", "output_modalities", "reasoning_levels"} {
		switch {
		case field != "reasoning_levels" && provimuxLiveStates(section, live, field):
			out[field] = "live"
		case field == "reasoning_levels" && live.Thinking != nil && len(live.Thinking.Levels) > 0:
			out[field] = "live"
		case provimuxFieldDiffers(field, live, composed):
			out[field] = "published"
		case provimuxModelHas(live, field):
			out[field] = "live"
		}
	}
}

func provimuxModelHas(model *ModelInfo, field string) bool {
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

func provimuxFieldDiffers(field string, a, b *ModelInfo) bool {
	switch field {
	case "display_name":
		return a.DisplayName != b.DisplayName
	case "context":
		return provimuxFirstPositive(a.ContextLength, a.InputTokenLimit) != provimuxFirstPositive(b.ContextLength, b.InputTokenLimit)
	case "output":
		return provimuxFirstPositive(a.MaxCompletionTokens, a.OutputTokenLimit) != provimuxFirstPositive(b.MaxCompletionTokens, b.OutputTokenLimit)
	case "input_modalities":
		return strings.Join(a.SupportedInputModalities, ",") != strings.Join(b.SupportedInputModalities, ",")
	case "output_modalities":
		return strings.Join(a.SupportedOutputModalities, ",") != strings.Join(b.SupportedOutputModalities, ",")
	case "reasoning_levels":
		var left, right []string
		if a.Thinking != nil {
			left = a.Thinking.Levels
		}
		if b.Thinking != nil {
			right = b.Thinking.Levels
		}
		return strings.Join(left, ",") != strings.Join(right, ",")
	}
	return false
}

// provimuxCatalogOverlayOf is the registry's catalog overlay for a model the catalog has: providers.dev
// fills only the fields the catalog leaves empty (name, window, output limit, input and output
// modalities), never one it states. The thinking block and web search stay the catalog's, whole: a
// catalog model without a thinking block is one whose thinking the router does not handle. The vendor's
// list, where a login has one, still fills an empty field first and wins there (fillLiveModelOver), so
// the order per field is the vendor's list, the catalog, providers.dev. nil when nothing is filled.
func provimuxCatalogOverlayOf(section string, raw *ModelInfo) *ModelInfo {
	if raw == nil {
		return nil
	}
	row := provimuxPublished.lookup(section, raw.ID)
	if row == nil {
		return nil
	}
	out := cloneLiveModel(raw)
	if raw.NativeCapabilities != nil {
		capabilities := *raw.NativeCapabilities
		out.NativeCapabilities = &capabilities
	}
	changed := false
	if raw.DisplayName == "" && row.Name != nil && strings.TrimSpace(*row.Name) != "" {
		out.DisplayName, changed = strings.TrimSpace(*row.Name), true
	}
	if window := provimuxRowWindow(row); raw.ContextLength == 0 && raw.InputTokenLimit == 0 && window > 0 {
		out.ContextLength, changed = window, true
		if out.Type == "gemini" {
			out.InputTokenLimit = window
		}
	}
	if raw.MaxCompletionTokens == 0 && raw.OutputTokenLimit == 0 {
		if output := provimuxRowOutput(row, provimuxFirstPositive(out.ContextLength, out.InputTokenLimit)); output > 0 {
			out.MaxCompletionTokens, changed = output, true
			if out.Type == "gemini" {
				out.OutputTokenLimit = output
			}
		}
	}
	if len(raw.SupportedInputModalities) == 0 && len(row.InputModalities) > 0 {
		out.SupportedInputModalities, changed = append([]string(nil), row.InputModalities...), true
	}
	if len(raw.SupportedOutputModalities) == 0 && len(row.OutputModalities) > 0 {
		out.SupportedOutputModalities, changed = append([]string(nil), row.OutputModalities...), true
	}
	if !changed {
		return nil
	}
	return out
}

// provimuxSetPublishedOn sets the option for this process and, when it changed, drops the registry's
// overlaid catalog. Never called with provimuxPublished.mu held (the overlay takes it).
func provimuxSetPublishedOn(on bool) bool {
	if provimuxPublished.on.Swap(on) == on {
		return false
	}
	registry.ProvimuxCatalogOverlayChanged()
	return true
}

// provimuxPrepareModelLists runs before a login's models are read from the catalog: it opens the disk
// cache (and with it providers.dev's last accepted document) and sets the option for this process, so
// the first registration of a start already sees the document on disk.
func (s *Service) provimuxPrepareModelLists(cfg *config.Config) {
	s.openLiveModelListCache()
	provimuxSetPublishedOn(provimuxPublishedListsOn(cfg))
}

func init() { registry.SetProvimuxCatalogOverlay(provimuxCatalogOverlayOf) }
