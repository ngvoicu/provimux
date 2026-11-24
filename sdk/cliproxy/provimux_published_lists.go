package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"reflect"
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
// The layers of a model's fields. A model the catalog has: providers.dev's value on top of the catalog's
// for every field its listing states (the registry's catalog overlay, provimuxCatalogOverlayOf), with the
// vendor's list keeping what 13.1 gave it: a field the catalog itself leaves empty. A model the catalog
// lacks (a learned model): the vendor's live list first; then providers.dev's listing, for every field the
// vendor's list does not state; then the parser's placeholder (a name equal to the id, text output),
// never an invented limit. Which models a login offers is not decided here: its live list, else the
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

// ---- the composition of a learned model's fields

// provimuxLiveStates reports whether the vendor's list stated a field of a learned model, as against
// leaving it empty or to the parser's placeholder: a name equal to the id, and "text" alone as the
// output, which every parser but xAI's writes without the vendor saying it. The thinking flags
// (can be off, dynamic budget) are stated by Claude's list only, and a true flag by any list.
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
	case "reasoning_levels":
		return live.Thinking != nil && len(live.Thinking.Levels) > 0
	case "reasoning_budget":
		return live.Thinking != nil && (live.Thinking.Min > 0 || live.Thinking.Max > 0)
	case "web_search":
		return live.NativeCapabilities != nil && live.NativeCapabilities.WebSearch != nil
	}
	return false
}

// provimuxComposeLearned returns a learned model with providers.dev's values in every field the
// vendor's list does not state. Without a listing row it is the vendor's model, unchanged.
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
	if !provimuxLiveStates(section, live, "context") && provimuxPositive(row.ContextWindow) {
		out.ContextLength = *row.ContextWindow
		if out.Type == "gemini" {
			out.InputTokenLimit = *row.ContextWindow
		}
	}
	if !provimuxLiveStates(section, live, "output") && provimuxPositive(row.MaxOutput) {
		out.MaxCompletionTokens = *row.MaxOutput
		if out.Type == "gemini" || out.OutputTokenLimit > 0 || section == "copilot" {
			out.OutputTokenLimit = *row.MaxOutput
		}
	}
	if !provimuxLiveStates(section, live, "input_modalities") && len(row.InputModalities) > 0 {
		out.SupportedInputModalities = append([]string(nil), row.InputModalities...)
	}
	if !provimuxLiveStates(section, live, "output_modalities") && len(row.OutputModalities) > 0 {
		out.SupportedOutputModalities = append([]string(nil), row.OutputModalities...)
	}
	provimuxComposeThinking(section, live, out, row.Reasoning)
	if !provimuxLiveStates(section, live, "web_search") && row.WebSearch != nil {
		if out.NativeCapabilities == nil {
			out.NativeCapabilities = &registry.NativeCapabilities{}
		}
		webSearch := *row.WebSearch
		out.NativeCapabilities.WebSearch = &webSearch
	}
	return out
}

// provimuxComposeThinking fills the thinking block field by field: levels, budgets and the two flags.
// A listing that says the model does not reason adds nothing.
func provimuxComposeThinking(section string, live, out *ModelInfo, reasoning *modellists.Reasoning) {
	if reasoning == nil || (reasoning.Supported != nil && !*reasoning.Supported) {
		return
	}
	levels := provimuxKnownLevels(reasoning.Levels)
	hasBudget := provimuxPositive(reasoning.BudgetMax)
	if out.Thinking == nil {
		if len(levels) == 0 && !hasBudget {
			return
		}
		out.Thinking = &registry.ThinkingSupport{}
	}
	if !provimuxLiveStates(section, live, "reasoning_levels") && len(levels) > 0 {
		out.Thinking.Levels = levels
	}
	if !provimuxLiveStates(section, live, "reasoning_budget") && hasBudget {
		out.Thinking.Max = *reasoning.BudgetMax
		if provimuxPositive(reasoning.BudgetMin) {
			out.Thinking.Min = *reasoning.BudgetMin
		}
	}
	flagsStated := section == "claude" && live.Thinking != nil
	if !flagsStated && !out.Thinking.ZeroAllowed && reasoning.CanDisable != nil {
		out.Thinking.ZeroAllowed = *reasoning.CanDisable
	}
	if !flagsStated && !out.Thinking.DynamicAllowed && reasoning.Dynamic != nil {
		out.Thinking.DynamicAllowed = *reasoning.Dynamic
	}
	if section == "claude" && out.Thinking.DisableType == "" && reasoning.DisableWith != nil {
		out.Thinking.DisableType = strings.TrimSpace(*reasoning.DisableWith)
	}
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
	row := provimuxPublished.lookup(section, live.ID)
	composed := provimuxComposeLearned(section, live)
	for _, field := range []string{"display_name", "context", "output", "input_modalities", "output_modalities", "reasoning_levels"} {
		switch {
		case provimuxLiveStates(section, live, field):
			out[field] = "live"
		case row != nil && provimuxFieldDiffers(field, live, composed):
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

// ---- providers.dev on top of the catalog (models the catalog has)

// provimuxCatalogOverlayOf is the registry's catalog overlay: a catalog model with providers.dev's value
// in every field its listing states, or nil when the listing has no row for it (or nothing differs).
// The vendor's list still fills, as built, only the fields the catalog itself leaves empty
// (fillLiveModelOver), and there it wins over providers.dev.
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
	if provimuxRowStates(row, "display_name") {
		out.DisplayName = strings.TrimSpace(*row.Name)
	}
	if provimuxRowStates(row, "context") {
		out.ContextLength = *row.ContextWindow
		if out.InputTokenLimit > 0 || out.Type == "gemini" {
			out.InputTokenLimit = *row.ContextWindow
		}
	}
	if provimuxRowStates(row, "output") {
		out.MaxCompletionTokens = *row.MaxOutput
		if out.OutputTokenLimit > 0 || out.Type == "gemini" {
			out.OutputTokenLimit = *row.MaxOutput
		}
	}
	if provimuxRowStates(row, "input_modalities") {
		out.SupportedInputModalities = append([]string(nil), row.InputModalities...)
	}
	if provimuxRowStates(row, "output_modalities") {
		out.SupportedOutputModalities = append([]string(nil), row.OutputModalities...)
	}
	if reasoning := row.Reasoning; reasoning != nil {
		if reasoning.Supported != nil && !*reasoning.Supported {
			out.Thinking = nil // providers.dev states the model does not reason
		} else {
			levels := provimuxKnownLevels(reasoning.Levels)
			if out.Thinking == nil && (len(levels) > 0 || provimuxPositive(reasoning.BudgetMax)) {
				out.Thinking = &registry.ThinkingSupport{}
			}
			if out.Thinking != nil {
				if len(levels) > 0 {
					out.Thinking.Levels = levels
				}
				if provimuxPositive(reasoning.BudgetMax) {
					out.Thinking.Max = *reasoning.BudgetMax
				}
				if provimuxPositive(reasoning.BudgetMin) {
					out.Thinking.Min = *reasoning.BudgetMin
				}
				if reasoning.CanDisable != nil {
					out.Thinking.ZeroAllowed = *reasoning.CanDisable
				}
				if reasoning.Dynamic != nil {
					out.Thinking.DynamicAllowed = *reasoning.Dynamic
				}
				if section == "claude" && reasoning.DisableWith != nil {
					out.Thinking.DisableType = strings.TrimSpace(*reasoning.DisableWith)
				}
			}
		}
	}
	if row.WebSearch != nil {
		if out.NativeCapabilities == nil {
			out.NativeCapabilities = &registry.NativeCapabilities{}
		}
		webSearch := *row.WebSearch
		out.NativeCapabilities.WebSearch = &webSearch
	}
	if reflect.DeepEqual(out, raw) {
		return nil
	}
	return out
}

// provimuxRowStates reports whether a listing row states a field of the status endpoint.
func provimuxRowStates(row *modellists.ListingModel, field string) bool {
	if row == nil {
		return false
	}
	switch field {
	case "display_name":
		return row.Name != nil && strings.TrimSpace(*row.Name) != ""
	case "context":
		return provimuxPositive(row.ContextWindow)
	case "output":
		return provimuxPositive(row.MaxOutput)
	case "input_modalities":
		return len(row.InputModalities) > 0
	case "output_modalities":
		return len(row.OutputModalities) > 0
	case "reasoning_levels":
		return row.Reasoning != nil && ((row.Reasoning.Supported != nil && !*row.Reasoning.Supported) || len(provimuxKnownLevels(row.Reasoning.Levels)) > 0)
	}
	return false
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

func init() { registry.SetProvimuxCatalogOverlay(provimuxCatalogOverlayOf) }
