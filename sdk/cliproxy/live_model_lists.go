package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// Live model lists (models.live-lists, on unless set to false). Claude, Codex, Meta, Devin, Kimi,
// xAI and Gemini (API keys) publish the models each account can use, but the built-in catalog is
// edited by hand and lags them: it can keep retired models and miss new ones for days. CPA fetches
// every such login's list in the background (at startup, every ModelsRefreshInterval, on
// POST /models/refresh, and when a login is added or replaced) and registers exactly the models the
// list names, plus the models CPA adds itself (image, video, speech). A model the catalog has
// keeps the catalog's values (the vendor fills only fields the catalog leaves empty; nothing is
// invented); a model the catalog lacks is added from the vendor's description ("learned",
// model-lists.md 13.1) and recorded in the registry so lookups find it too.
//
// A fetch replaces the held list only when it is complete and names at least one model; anything
// else keeps the held list and schedules a retry. Lists are kept on disk (modellists.Store), so a
// restart without network gives the lists of the last run. A list that could not be refreshed for
// 7 days counts as not held: the login takes the catalog until a fetch succeeds. Until a list
// arrives the catalog is used unchanged, so a provider outage never empties a login. API keys keep
// the catalog, except a Gemini key on Google's own host.

const (
	liveModelListScanInterval = 10 * time.Minute
	liveModelListRetry        = 10 * time.Minute
	liveModelListMaxBody      = 8 << 20
	// liveModelListMaxAge is how long a list that cannot be refreshed stays in use (model-lists.md 5.3).
	liveModelListMaxAge      = 7 * 24 * time.Hour
	codexLiveModelListOrigin = "codex-tui"
	// Codex filters /models by client_version: each model has a minimal_client_version, and an
	// older version silently gets a shorter list. CPA isn't the client, so it asks with a version
	// past every minimum to see everything the account has.
	codexLiveModelListClientVersion = "99.0.0"
)

// liveModelListTypes are the login types this fetcher can ask, in the order the status endpoint
// lists them. Copilot and Antigravity are asked by upstream's own refreshers.
var liveModelListTypes = []string{"claude", "codex", "meta", "devin", "kimi", "xai", "gemini"}

// liveModelListAsked is the internal switch per login type. Kimi and xAI are off: their code is
// kept, but no request is sent, nothing is cached and their logins get the catalog as before,
// until the owner's machine has recorded one real answer from each (model-lists.md 13.4). Not a
// config option; tests flip it.
var liveModelListAsked = map[string]bool{"claude": true, "codex": true, "meta": true, "devin": true, "gemini": true,
	"kimi": false, "xai": false}

// liveModelListProviders returns the login types that are asked now.
func liveModelListProviders() []string {
	var out []string
	for _, provider := range liveModelListTypes {
		if liveModelListAsked[provider] {
			out = append(out, provider)
		}
	}
	return out
}

// liveModelListFetchTimeout bounds one list fetch, request and answer, as upstream's own catalog
// tools do. On timeout the held list stays.
var liveModelListFetchTimeout = 30 * time.Second

// liveModelList is one login's list as its provider returned it.
type liveModelList struct {
	identity  string              // the account the list belongs to (liveModelListIdentity)
	provider  string              // liveModelListProvider of the login
	ids       map[string]struct{} // lower-case ids
	extra     []*ModelInfo        // provider descriptions, one per model the provider describes
	fetchedAt time.Time
	fromCache bool // read from the disk cache at start, not fetched by this process yet
}

type liveModelListStore struct {
	mu           sync.Mutex
	lists        map[string]liveModelList // by auth ID
	nextRetry    map[string]time.Time     // by auth ID, after a failed fetch
	inFlight     map[string]struct{}      // by auth ID
	cacheTried   map[string]string        // by auth ID: the identity whose disk file was read
	expiredNoted map[string]time.Time     // by auth ID: fetchedAt of a list announced as expired
	wake         chan struct{}
	force        atomic.Bool
	fetches      sync.WaitGroup
	cache        atomic.Pointer[modellists.Store]
}

var liveModelLists = &liveModelListStore{
	lists:        make(map[string]liveModelList),
	nextRetry:    make(map[string]time.Time),
	inFlight:     make(map[string]struct{}),
	cacheTried:   make(map[string]string),
	expiredNoted: make(map[string]time.Time),
	wake:         make(chan struct{}, 1),
}

// liveModelListIdentity names the account behind a login. An auth file can be overwritten with
// another account under the same ID, so lists are kept per identity, not per ID alone. Logins
// without a stable account field fall back to a hash of the credential, which only costs an
// extra fetch after a token refresh.
func liveModelListIdentity(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	parts := []string{strings.ToLower(strings.TrimSpace(auth.Provider)), liveModelListBaseURL(auth, "")}
	stable := false
	for _, key := range []string{"account_id", "email", "user_id"} {
		value, _ := auth.Metadata[key].(string)
		if value = strings.TrimSpace(value); value == "" {
			value = strings.TrimSpace(auth.Attributes[key])
		}
		if value != "" {
			parts = append(parts, key+"="+value)
			stable = true
		}
	}
	if !stable {
		token, _ := auth.Metadata["access_token"].(string)
		if token = strings.TrimSpace(token); token == "" {
			token = strings.TrimSpace(auth.Attributes["api_key"])
		}
		sum := sha256.Sum256([]byte(token))
		parts = append(parts, "credential="+hex.EncodeToString(sum[:8]))
	}
	return strings.Join(parts, "|")
}

var liveModelListNow = time.Now

// liveModelListHeld reports whether a list is still in use: not older than liveModelListMaxAge.
func liveModelListHeld(list liveModelList, now time.Time) bool {
	return now.Sub(list.fetchedAt) < liveModelListMaxAge
}

// get returns the login's list if it belongs to the same account and is not older than 7 days.
func (st *liveModelListStore) get(authID, identity string) (liveModelList, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	list, ok := st.lists[authID]
	if !ok || list.identity != identity || !liveModelListHeld(list, liveModelListNow()) {
		return liveModelList{}, false
	}
	return list, true
}

// put stores a successful fetch and reports whether the ids or the provider descriptions changed.
func (st *liveModelListStore) put(authID string, list liveModelList) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	previous, existed := st.lists[authID]
	st.lists[authID] = list
	delete(st.nextRetry, authID)
	delete(st.expiredNoted, authID)
	now := liveModelListNow()
	return !existed || !sameLiveModelList(previous, list) || (!liveModelListHeld(previous, now) && liveModelListHeld(list, now))
}

func (st *liveModelListStore) fail(authID string, now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextRetry[authID] = now.Add(liveModelListRetry)
}

// due reports whether a login's list should be fetched now: never fetched, fetched for another
// account, read from the disk cache only, or older than ModelsRefreshInterval, unless a fetch is
// running or a failure is waiting for its retry.
func (st *liveModelListStore) due(authID, identity string, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, running := st.inFlight[authID]; running {
		return false
	}
	list, ok := st.lists[authID]
	if ok && list.identity != identity {
		return true
	}
	if retry, failed := st.nextRetry[authID]; failed && now.Before(retry) {
		return false
	}
	return !ok || list.fromCache || now.Sub(list.fetchedAt) >= registry.ModelsRefreshInterval
}

// start marks a fetch as running; it reports false if one already is.
func (st *liveModelListStore) start(authID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, running := st.inFlight[authID]; running {
		return false
	}
	st.inFlight[authID] = struct{}{}
	st.fetches.Add(1)
	return true
}

func (st *liveModelListStore) finish(authID string) {
	st.mu.Lock()
	delete(st.inFlight, authID)
	st.mu.Unlock()
	st.fetches.Done()
}

// forget drops lists of logins that no longer exist.
func (st *liveModelListStore) forget(keep map[string]struct{}) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for id := range st.lists {
		if _, ok := keep[id]; !ok {
			delete(st.lists, id)
		}
	}
	for id := range st.nextRetry {
		if _, ok := keep[id]; !ok {
			delete(st.nextRetry, id)
		}
	}
	for id := range st.cacheTried {
		if _, ok := keep[id]; !ok {
			delete(st.cacheTried, id)
		}
	}
	for id := range st.expiredNoted {
		if _, ok := keep[id]; !ok {
			delete(st.expiredNoted, id)
		}
	}
}

// request wakes the refresh loop; force fetches every login instead of only the due ones.
func (st *liveModelListStore) request(force bool) {
	if force {
		st.force.Store(true)
	}
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// loadCached reads the login's list from the disk cache once per account, if the store holds
// none of this account. It reports whether a list was loaded.
// liveModelListCachedID is an id as a cache file holds it, in today's form: lower case, and for Devin
// without the "devin/" namespace, which the catalog dropped in 8.1.1 (it is added when the models are
// registered); files written by 8.1.0-pmx.2 have it.
func liveModelListCachedID(provider, id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if provider == "devin" {
		id = strings.TrimPrefix(id, "devin/")
	}
	return id
}

func (st *liveModelListStore) loadCached(authID, identity, provider string) bool {
	cache := st.cache.Load()
	if cache == nil || authID == "" {
		return false
	}
	st.mu.Lock()
	if st.cacheTried[authID] == identity {
		st.mu.Unlock()
		return false
	}
	st.cacheTried[authID] = identity
	if list, ok := st.lists[authID]; ok && list.identity == identity {
		st.mu.Unlock()
		return false
	}
	st.mu.Unlock()
	file, err := cache.ReadLive(provider, identity)
	if err != nil {
		return false
	}
	list := liveModelList{identity: identity, provider: provider, ids: make(map[string]struct{}, len(file.IDs)),
		extra: file.Models, fetchedAt: file.FetchedAt, fromCache: true}
	for _, id := range file.IDs {
		if id = liveModelListCachedID(provider, id); id != "" {
			list.ids[id] = struct{}{}
		}
	}
	for _, model := range list.extra {
		if model != nil && provider == "devin" {
			model.ID = strings.TrimPrefix(strings.TrimSpace(model.ID), "devin/")
		}
	}
	if len(list.ids) == 0 {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if current, ok := st.lists[authID]; ok && current.identity == identity {
		return false // a fetch finished meanwhile
	}
	st.lists[authID] = list
	return true
}

// writeCached stores a fetched list on disk. A failure is logged by the cache, once.
func (st *liveModelListStore) writeCached(list liveModelList) {
	cache := st.cache.Load()
	if cache == nil {
		return
	}
	ids := make([]string, 0, len(list.ids))
	for id := range list.ids {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	_ = cache.WriteLive(list.provider, list.identity, list.fetchedAt, ids, list.extra)
}

// newlyExpired returns the logins whose list just passed liveModelListMaxAge, once per list.
func (st *liveModelListStore) newlyExpired(now time.Time) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var ids []string
	for id, list := range st.lists {
		if liveModelListHeld(list, now) {
			continue
		}
		if noted, ok := st.expiredNoted[id]; ok && noted.Equal(list.fetchedAt) {
			continue
		}
		st.expiredNoted[id] = list.fetchedAt
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// sameLiveModelList compares everything a list publishes: its account, ids and every field of the
// models it may add.
func sameLiveModelList(a, b liveModelList) bool {
	return a.identity == b.identity && reflect.DeepEqual(a.ids, b.ids) && reflect.DeepEqual(a.extra, b.extra)
}

// liveModelListProvider returns the provider whose live list applies to auth, or "" (also for a
// type whose switch is off).
func liveModelListProvider(auth *coreauth.Auth) string {
	provider := liveModelListType(auth)
	if !liveModelListAsked[provider] {
		return ""
	}
	return provider
}

// liveModelListType is the login type that has a live-list fetcher, whether or not it is asked.
func liveModelListType(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if auth.AuthKind() == coreauth.AuthKindAPIKey {
		// Keys keep the catalog, except a Gemini key on Google's own host: the key is the only
		// credential, and a key with its own base URL is never asked.
		if provider == "gemini" && liveModelListGeminiHost(auth) {
			return provider
		}
		return ""
	}
	switch provider {
	case "claude", "codex", "meta", "devin", "xai":
		return provider
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return "kimi"
	}
	return ""
}

// applyLiveModelList registers exactly the models of a login's live list. Without the option, for
// other logins, or before the first list (fetched or cached) it returns models unchanged (and asks
// for a fetch).
func applyLiveModelList(cfg *config.Config, auth *coreauth.Auth, models []*ModelInfo) []*ModelInfo {
	provimuxSetPublishedOn(provimuxPublishedListsOn(cfg))
	provider := liveModelListProvider(auth)
	if cfg == nil || !cfg.Models.LiveListsOn() || cfg.Home.Enabled || provider == "" || keyGeminiFixed(cfg, auth) {
		return models
	}
	identity := liveModelListIdentity(auth)
	list, ok := liveModelLists.get(auth.ID, identity)
	if !ok && liveModelLists.loadCached(auth.ID, identity, provider) {
		list, ok = liveModelLists.get(auth.ID, identity)
		if ok {
			provimuxUpdateLearnedModels(provider)
			log.Info(provimuxLiveLogLine(provider, auth, list))
		}
	}
	if !ok {
		liveModelLists.request(false)
		return models
	}
	return filterByLiveModelList(provider, models, list)
}

// applyLiveModelList is the service's entry: it opens the disk cache beside the config file
// before the first list is read.
func (s *Service) applyLiveModelList(cfg *config.Config, auth *coreauth.Auth, models []*ModelInfo) []*ModelInfo {
	s.openLiveModelListCache()
	return applyLiveModelList(cfg, auth, models)
}

// openLiveModelListCache sets the disk cache's folder from the service's config path, once, and reads
// providers.dev's last accepted document from it: both local, before any request.
func (s *Service) openLiveModelListCache() {
	if s == nil || liveModelLists.cache.Load() != nil {
		return
	}
	liveModelLists.cache.CompareAndSwap(nil, modellists.NewStore(modellists.Dir(s.configPath)))
	provimuxPublished.loadCache(liveModelLists.cache.Load())
}

// filterByLiveModelList keeps the catalog models the list names, with the catalog's values (only
// empty fields filled from the vendor, fillLiveModel), and appends the listed models the catalog lacks.
func filterByLiveModelList(provider string, models []*ModelInfo, list liveModelList) []*ModelInfo {
	keep := liveModelListBuiltins(provider)
	described := make(map[string]*ModelInfo, len(list.extra))
	for _, model := range list.extra {
		if model != nil {
			described[liveModelListKey(provider, model.ID)] = model
		}
	}
	out := make([]*ModelInfo, 0, len(models)+len(list.extra))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := liveModelListKey(provider, model.ID)
		_, listed := list.ids[id]
		_, builtin := keep[id]
		if !listed && !builtin {
			continue
		}
		if live := described[id]; live != nil && listed && !builtin {
			// Judged against the catalog as it states the model, without the gaps providers.dev filled.
			model = fillLiveModelOver(registry.ProvimuxRawOfOverlaid(provider, model), model, live)
		}
		out = append(out, model)
		seen[id] = struct{}{}
	}
	for _, model := range list.extra {
		if model == nil {
			continue
		}
		id := liveModelListKey(provider, model.ID)
		if _, ok := seen[id]; ok {
			continue
		}
		if _, listed := list.ids[id]; !listed {
			continue
		}
		out = append(out, provimuxComposeLearned(provider, model))
		seen[id] = struct{}{}
	}
	return out
}

// liveModelListKey is the id a list and the catalog are matched by: lower case.
func liveModelListKey(_ string, id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func cloneLiveModel(model *ModelInfo) *ModelInfo {
	clone := *model
	if model.Thinking != nil {
		thinking := *model.Thinking
		thinking.Levels = append([]string(nil), model.Thinking.Levels...)
		clone.Thinking = &thinking
	}
	clone.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	clone.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	clone.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	clone.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	return &clone
}

// fillLiveModel returns the catalog model with the fields the catalog leaves empty filled from the
// vendor's description (model-lists.md 13.1). For a model the catalog has, the vendor's list decides
// only whether the login offers it: every value the catalog states stays the catalog's, as on
// 8.1.0-pmx.1. Empty means a zero number (the window and the output limit each as one field: either
// of their two numbers counts), an empty string or an empty list. The thinking block is the
// catalog's whole: a catalog model without one is a model whose thinking the router does not
// handle, and its levels, budgets and flags are read together.
func fillLiveModel(catalog, live *ModelInfo) *ModelInfo {
	return fillLiveModelOver(catalog, catalog, live)
}

// fillLiveModelOver is fillLiveModel for a catalog model whose gaps providers.dev filled (model): the
// vendor's description fills the fields the catalog itself (raw) leaves empty, and wins there over
// providers.dev; every other field stays model's. A nil raw is model.
func fillLiveModelOver(raw, model, live *ModelInfo) *ModelInfo {
	if raw == nil {
		raw = model
	}
	catalog := raw
	fillName := catalog.DisplayName == "" && live.DisplayName != ""
	fillDescription := catalog.Description == "" && live.Description != ""
	fillCreated := catalog.Created == 0 && live.Created != 0
	fillContext := catalog.ContextLength == 0 && catalog.InputTokenLimit == 0 && (live.ContextLength > 0 || live.InputTokenLimit > 0)
	fillOutput := catalog.MaxCompletionTokens == 0 && catalog.OutputTokenLimit == 0 &&
		(live.MaxCompletionTokens > 0 || live.OutputTokenLimit > 0)
	fillInputs := len(catalog.SupportedInputModalities) == 0 && len(live.SupportedInputModalities) > 0
	fillOutputs := len(catalog.SupportedOutputModalities) == 0 && len(live.SupportedOutputModalities) > 0
	fillMethods := len(catalog.SupportedGenerationMethods) == 0 && len(live.SupportedGenerationMethods) > 0
	if !fillName && !fillDescription && !fillCreated && !fillContext && !fillOutput && !fillInputs && !fillOutputs && !fillMethods {
		return model // nothing to fill: the catalog's own entry (with providers.dev's values), as registered without a list
	}
	merged := cloneLiveModel(model)
	if model.NativeCapabilities != nil {
		capabilities := *model.NativeCapabilities
		merged.NativeCapabilities = &capabilities
	}
	if fillName {
		merged.DisplayName = live.DisplayName
	}
	if fillDescription {
		merged.Description = live.Description
	}
	if fillCreated {
		merged.Created = live.Created
	}
	if fillContext {
		merged.ContextLength, merged.InputTokenLimit = live.ContextLength, live.InputTokenLimit
	}
	if fillOutput {
		merged.MaxCompletionTokens, merged.OutputTokenLimit = live.MaxCompletionTokens, live.OutputTokenLimit
	}
	if fillInputs {
		merged.SupportedInputModalities = append([]string(nil), live.SupportedInputModalities...)
	}
	if fillOutputs {
		merged.SupportedOutputModalities = append([]string(nil), live.SupportedOutputModalities...)
	}
	if fillMethods {
		merged.SupportedGenerationMethods = append([]string(nil), live.SupportedGenerationMethods...)
	}
	return merged
}

// liveModelListBuiltins are models CPA adds itself that the provider's list never includes.
func liveModelListBuiltins(provider string) map[string]struct{} {
	keep := map[string]struct{}{}
	var builtins []*ModelInfo
	switch provider {
	case "codex":
		builtins = registry.WithCodexBuiltins(nil)
	case "xai":
		builtins = registry.WithXAIBuiltins(nil)
	case "devin":
		builtins = registry.WithDevinBuiltins(nil)
	}
	for _, model := range builtins {
		if model != nil {
			keep[strings.ToLower(model.ID)] = struct{}{}
		}
	}
	return keep
}

func (s *Service) liveModelListsEnabled() bool {
	if s == nil {
		return false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg != nil && s.cfg.Models.LiveListsOn() && !s.cfg.Home.Enabled
}

// runLiveModelListRefresh fetches due lists every scan interval and when woken, and registers
// the models of logins whose list changed.
func (s *Service) runLiveModelListRefresh(ctx context.Context) {
	s.openLiveModelListCache()
	// providers.dev's round runs beside this loop and ends with it: this returns only once that round has
	// returned too, so nothing of it writes to the model lists' folder afterwards.
	published := make(chan struct{})
	go func() {
		defer close(published)
		s.runPublishedListRefresh(ctx)
	}()
	defer func() { <-published }()
	registry.SetLiveModelListRefreshHook(func() {
		liveModelLists.request(true)
		provimuxPublished.request()
	})
	defer registry.SetLiveModelListRefreshHook(nil)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-liveModelLists.wake:
		case <-timer.C:
			timer.Reset(liveModelListScanInterval)
		}
		s.refreshLiveModelLists(ctx, liveModelLists.force.Swap(false))
	}
}

// refreshLiveModelLists starts a fetch for every due login (every login when forced). Fetches
// run in parallel, each with its own 30-second deadline (liveModelListFetchTimeout), so a slow
// provider only delays its own logins; a login whose fetch is still running is skipped. A list
// that just passed 7 days without a refresh is announced once and its login takes the catalog.
func (s *Service) refreshLiveModelLists(ctx context.Context, force bool) {
	if s == nil || s.coreManager == nil {
		return
	}
	if !s.liveModelListsEnabled() {
		// Turned off: drop the lists so turning it on again starts from fresh fetches.
		keys := liveModelLists.keyListIDs()
		liveModelLists.forget(nil)
		provimuxUpdateLearnedModels(liveModelListTypes...)
		s.refreshKeyRegistrations(keys) // keys offer their configured models again
		return
	}
	s.openLiveModelListCache()
	cfg := s.currentConfig()
	present := make(map[string]struct{})
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || liveModelListKindFor(cfg, auth) == "" {
			continue
		}
		present[auth.ID] = struct{}{}
		identity := liveModelListIdentity(auth)
		if auth.Disabled || (!force && !liveModelLists.due(auth.ID, identity, liveModelListNow())) {
			continue
		}
		if !liveModelLists.start(auth.ID) {
			continue
		}
		snapshot := auth.Clone()
		go func() {
			defer liveModelLists.finish(snapshot.ID)
			s.fetchAndRegisterLiveModelList(ctx, snapshot, identity)
		}()
	}
	liveModelLists.forget(present)
	provimuxForgetForeignLists(func(id string) bool {
		_, ok := s.coreManager.GetByID(id)
		return ok
	})
	for _, id := range liveModelLists.newlyExpired(liveModelListNow()) {
		current, ok := s.coreManager.GetByID(id)
		if !ok || current == nil {
			continue
		}
		if isKeyModelListKind(liveModelListKindFor(cfg, current)) {
			log.Warnf("model list for %s: list older than 7 days and not refreshed; offering the configured models until a fetch succeeds",
				keyModelListLogName(current))
			if !current.Disabled {
				s.refreshModelRegistrationForAuth(current)
			}
			continue
		}
		log.Warnf("model list for %s login %s: live list older than 7 days and not refreshed; using the catalog until a fetch succeeds",
			liveModelListProvider(current), provimuxLiveLogName(current))
		provimuxUpdateLearnedModels(liveModelListProvider(current))
		if !current.Disabled {
			s.refreshModelRegistrationForAuth(current)
		}
	}
	provimuxUpdateLearnedModels(liveModelListTypes...)
}

func (s *Service) fetchAndRegisterLiveModelList(ctx context.Context, snapshot *coreauth.Auth, identity string) {
	list, err := s.fetchLiveModelList(ctx, snapshot)
	if ctx.Err() != nil {
		return
	}
	// The login may have been replaced by another account while the request ran.
	current, ok := s.coreManager.GetByID(snapshot.ID)
	if !ok || current == nil || liveModelListIdentity(current) != identity {
		return
	}
	provider := s.liveModelListKind(snapshot)
	if provider == "" || provider != s.liveModelListKind(current) {
		return // the key became fixed, or live lists were turned off, while the request ran
	}
	key := isKeyModelListKind(provider)
	if err != nil {
		liveModelLists.fail(snapshot.ID, liveModelListNow())
		noteLiveModelListFailure(snapshot.ID, identity, err)
		category, status := provimuxLiveListFailureOf(err)
		if key {
			log.Warnf("model list for %s: fetch failed (%s); %s", keyModelListLogName(current),
				provimuxLiveFailureText(category, status), keyModelListKeptText(snapshot.ID, identity))
			return
		}
		log.Warnf("model list for %s login %s: fetch failed (%s); %s", provider, provimuxLiveLogName(current),
			provimuxLiveFailureText(category, status), provimuxLiveKeptText(snapshot.ID, identity))
		return
	}
	list.identity = identity
	list.provider = provider
	changed := liveModelLists.put(snapshot.ID, list)
	liveModelLists.writeCached(list)
	if changed {
		if !key {
			provimuxUpdateLearnedModels(provider) // a key's list never teaches the catalog (key-model-lists.md 5)
		}
		if !current.Disabled {
			s.refreshModelRegistrationForAuth(current)
		}
		if key {
			log.Info(keyModelListLogLine(s.currentConfig(), current, list))
		} else {
			log.Info(provimuxLiveLogLine(provider, current, list))
		}
	}
}

func (s *Service) fetchLiveModelList(ctx context.Context, auth *coreauth.Auth) (liveModelList, error) {
	// One deadline for the whole fetch (credentials, every request and page, every answer): a
	// vendor that never answers fails the fetch, the held list stays and the retry is scheduled.
	ctx, cancel := context.WithTimeout(ctx, liveModelListFetchTimeout)
	defer cancel()
	list, err := s.fetchLiveModelListWithin(ctx, auth)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// Whatever step was running (minting Meta's key, a request, reading an answer), the vendor
		// did not answer in time: a network failure, as model-lists.md 13.2 says.
		return liveModelList{}, fmt.Errorf("%w: %w", errLiveModelListTimeout, err)
	}
	return list, err
}

// errLiveModelListTimeout marks a fetch that ran past liveModelListFetchTimeout.
var errLiveModelListTimeout = errors.New("model list: no answer within the deadline")

func (s *Service) fetchLiveModelListWithin(ctx context.Context, auth *coreauth.Auth) (liveModelList, error) {
	provider := s.liveModelListKind(auth)
	executorKey := provider
	if isKeyModelListKind(provider) {
		// A key's requests go through its own provider's executor (an openai-compatibility key's is
		// its entry's), which sets the key, the entry's headers and the proxy.
		executorKey = keyModelListExecutorKey(auth)
	}
	executor, ok := s.coreManager.Executor(executorKey)
	if !ok || executor == nil {
		return liveModelList{}, fmt.Errorf("no %s executor", executorKey)
	}
	// Prepare the credential the way requests do (e.g. Meta mints an API key and learns its base URL
	// from a DCA token), so the result is stored by the manager and the URL below is the prepared one.
	if preparer, okPreparer := executor.(coreauth.RequestAuthPreparer); okPreparer {
		prepared, errPrepare := s.coreManager.PrepareRequestAuth(ctx, preparer, auth)
		if errPrepare != nil {
			return liveModelList{}, fmt.Errorf("prepare credentials: %w", errPrepare)
		}
		if prepared != nil {
			auth = prepared
		}
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	send := func(rawURL string, header http.Header, bare bool) ([]byte, error) {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if errReq != nil {
			return nil, errReq
		}
		for key, values := range header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		var resp *http.Response
		var errDo error
		if bare {
			// Without the key and the entry's headers, through the key's proxy (key-model-lists.md 4).
			resp, errDo = helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
		} else {
			resp, errDo = executor.HttpRequest(ctx, auth, req)
		}
		if errDo != nil {
			return nil, errDo
		}
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Debugf("live model list: close %s response body: %v", req.URL.Path, errClose)
			}
		}()
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, liveModelListMaxBody+1))
		if errRead != nil {
			return nil, errRead
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, req.URL.Path)
		}
		if len(body) > liveModelListMaxBody {
			return nil, fmt.Errorf("%s model list: %w", provider, errLiveModelListTooLarge)
		}
		return body, nil
	}
	get := func(rawURL string, header http.Header) ([]byte, error) { return send(rawURL, header, false) }
	if isKeyModelListKind(provider) {
		bare := func(rawURL string, header http.Header) ([]byte, error) { return send(rawURL, header, true) }
		return fetchKeyModelList(provider, auth, get, bare)
	}

	switch provider {
	case "claude":
		base := liveModelListBaseURL(auth, "https://api.anthropic.com")
		header := http.Header{}
		if probe, errProbe := http.NewRequest(http.MethodGet, base, nil); errProbe == nil {
			helps.ApplyClaudeDefaultDeviceProfileHeaders(probe, cfg)
			header = probe.Header.Clone()
		}
		header.Set("Anthropic-Version", "2023-06-01")
		header.Set("Anthropic-Beta", "oauth-2025-04-20")
		// One page of the maximum size holds every Claude model; a list that still has more is
		// treated as a failed fetch, so a partial list is never published.
		body, err := get(base+"/v1/models?"+url.Values{"limit": {"1000"}}.Encode(), header)
		if err != nil {
			return liveModelList{}, err
		}
		return parseClaudeLiveModelList(body, liveModelListNow())
	case "codex":
		// The account list belongs to the outer ChatGPT login: mark the request as a Codex client
		// one so a use_siwc login still uses its outer token, and name the workspace.
		ctx = coreauth.WithCodexClientRequest(ctx, true)
		base := liveModelListBaseURL(auth, "https://chatgpt.com/backend-api/codex")
		header := http.Header{}
		header.Set("Originator", codexLiveModelListOrigin)
		if cfg != nil && strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent) != "" {
			header.Set("User-Agent", strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent))
		}
		if accountID, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(accountID) != "" {
			header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
		}
		body, err := get(base+"/models?client_version="+codexLiveModelListClientVersion, header)
		if err != nil {
			return liveModelList{}, err
		}
		return parseCodexLiveModelList(body, liveModelListNow())
	case "meta":
		body, err := get(liveModelListBaseURL(auth, metaauth.DefaultAPIBaseURL)+"/models", nil)
		if err != nil {
			return liveModelList{}, err
		}
		return parseMetaLiveModelList(body, liveModelListNow())
	case "devin":
		return fetchDevinLiveModelList(ctx, cfg, auth)
	case "kimi":
		return fetchKimiLiveModelList(auth, get)
	case "xai":
		return fetchXAILiveModelList(auth, get)
	case "gemini":
		return fetchGeminiLiveModelList(auth, get)
	}
	return liveModelList{}, fmt.Errorf("unsupported provider %q", provider)
}

func liveModelListBaseURL(auth *coreauth.Auth, fallback string) string {
	if auth != nil {
		if base := strings.TrimSpace(auth.Attributes["base_url"]); base != "" {
			return strings.TrimRight(base, "/")
		}
		if base, _ := auth.Metadata["base_url"].(string); strings.TrimSpace(base) != "" {
			return strings.TrimRight(strings.TrimSpace(base), "/")
		}
	}
	return fallback
}

// liveModelListLevels keeps the efforts CPA knows, in its order.
func liveModelListLevels(supported func(level string) bool) []string {
	var levels []string
	for _, level := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if supported(level) {
			levels = append(levels, level)
		}
	}
	return levels
}

func errEmptyLiveModelList(provider string) error {
	return fmt.Errorf("%s returned no models", provider)
}

// parseClaudeLiveModelList reads GET /v1/models: ids plus each model's window, output limit,
// efforts and input modalities. A page that says it has more is incomplete and rejected.
func parseClaudeLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("claude model list: invalid JSON")
	}
	root := ojson.ParseBytes(body)
	if root.Get("has_more").Bool() {
		return liveModelList{}, fmt.Errorf("claude model list: incomplete (has_more with limit 1000)")
	}
	for _, item := range root.Get("data").Array() {
		id := strings.TrimSpace(item.Get("id").String())
		if id == "" {
			continue
		}
		list.ids[strings.ToLower(id)] = struct{}{}
		caps := item.Get("capabilities")
		model := &ModelInfo{ID: id, Object: "model", OwnedBy: "anthropic", Type: "claude",
			DisplayName: item.Get("display_name").String(), ContextLength: int(item.Get("max_input_tokens").Int()),
			MaxCompletionTokens: int(item.Get("max_tokens").Int()), SupportedInputModalities: []string{"text"},
			SupportedOutputModalities: []string{"text"}}
		if created, err := time.Parse(time.RFC3339, item.Get("created_at").String()); err == nil {
			model.Created = created.Unix()
		}
		if caps.Get("image_input.supported").Bool() {
			model.SupportedInputModalities = append(model.SupportedInputModalities, "image")
		}
		levels := liveModelListLevels(func(level string) bool {
			return caps.Get("effort." + level + ".supported").Bool()
		})
		if len(levels) > 0 {
			model.Thinking = &registry.ThinkingSupport{Levels: levels,
				ZeroAllowed:    caps.Get("thinking.types.disabled.supported").Bool(),
				DynamicAllowed: caps.Get("thinking.types.adaptive.supported").Bool()}
		}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("claude")
	}
	return list, nil
}

// parseCodexLiveModelList reads GET /backend-api/codex/models: every slug counts as available,
// and the visible ones (Codex hides fallbacks such as gpt-reserve) may be added.
func parseCodexLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("codex model list: invalid JSON")
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range ojson.ParseBytes(body).Get("models").Array() {
		id := strings.TrimSpace(item.Get("slug").String())
		if id == "" {
			continue
		}
		list.ids[strings.ToLower(id)] = struct{}{}
		if strings.EqualFold(item.Get("visibility").String(), "hide") {
			continue
		}
		window := int(item.Get("max_context_window").Int())
		if window <= 0 {
			window = int(item.Get("context_window").Int())
		}
		model := &ModelInfo{ID: id, Object: "model", OwnedBy: "openai", Type: "openai",
			DisplayName: item.Get("display_name").String(), Description: item.Get("description").String(),
			ContextLength: window, SupportedOutputModalities: []string{"text"}}
		for _, modality := range item.Get("input_modalities").Array() {
			if value := strings.TrimSpace(modality.String()); value != "" {
				model.SupportedInputModalities = append(model.SupportedInputModalities, value)
			}
		}
		if len(model.SupportedInputModalities) == 0 {
			model.SupportedInputModalities = []string{"text"}
		}
		efforts := item.Get("supported_reasoning_levels").Array()
		levels := liveModelListLevels(func(level string) bool {
			for _, effort := range efforts {
				if strings.EqualFold(effort.Get("effort").String(), level) {
					return true
				}
			}
			return false
		})
		if len(levels) > 0 {
			model.Thinking = &registry.ThinkingSupport{Levels: levels}
		}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("codex")
	}
	return list, nil
}

// parseMetaLiveModelList reads GET /v1/models, which has ids only: Meta logins are filtered, and
// nothing is added.
func parseMetaLiveModelList(body []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("meta model list: invalid JSON")
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range ojson.ParseBytes(body).Get("data").Array() {
		if id := strings.TrimSpace(item.Get("id").String()); id != "" {
			list.ids[strings.ToLower(id)] = struct{}{}
		}
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("meta")
	}
	return list, nil
}
