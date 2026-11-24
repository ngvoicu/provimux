package cliproxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: every API key's model list keeps itself up to date (design: key-model-lists.md).
//
// A key that is asked offers every row of its models in config.yaml exactly as written, plus every
// model its service lists that no row names (under the service's own id, built by the key type's
// own builder as a bare row), minus its excluded-models. The service's list is read by the
// live-list fetcher of live_model_lists.go, with the request the console's "Get the list from …"
// button sends, and held in memory and in the disk cache like a login's; nothing is ever written
// to config.yaml. fixed-models: true on a key's entry turns it off for that key, and
// models.live-lists: false (or home mode) for every key.
//
// A key's list never feeds the catalog's learned models: keys have list kinds of their own, which
// never equal a login type. A Gemini key on Google's own host keeps the login kind "gemini".

// List kinds of config API keys.
const (
	keyKindCompat = "openai-compatibility"
	keyKindClaude = "claude-key"
	keyKindCodex  = "codex-key"
	keyKindXAI    = "xai-key"
	keyKindMeta   = "meta-key"
	keyKindGemini = "gemini-key"
)

// Reasons a key is not asked (key.not_asked of the status endpoint).
const (
	keyNotAskedFixed      = "fixed"
	keyNotAskedLiveOff    = "live_lists_off"
	keyNotAskedNoEndpoint = "no_list_endpoint"
)

// isKeyModelListKind reports whether a list kind belongs to a config key (not a login type).
func isKeyModelListKind(kind string) bool {
	switch kind {
	case keyKindCompat, keyKindClaude, keyKindCodex, keyKindXAI, keyKindMeta, keyKindGemini:
		return true
	}
	return false
}

// keyModelListInfo is what the config says about one key: its type, its rows, whether it is asked
// and with which list kind.
type keyModelListInfo struct {
	keyType  string // openai-compatibility, claude, codex, xai, meta, gemini, interactions, vertex
	kind     string // the list kind; "" when the key is not asked
	notAsked string // "", fixed, live_lists_off, no_list_endpoint
	fixed    bool
	rows     []modelEntry
	compat   *config.OpenAICompatibility // the entry of an openai-compatibility key
}

// keyRowsOf returns a key entry's rows as model entries.
func keyRowsOf[T modelEntry](rows []T) []modelEntry {
	out := make([]modelEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

// resolveKeyModelList finds the config entry of a key whose list this design covers: every
// openai-compatibility key, and every built-in key that has rows. ok is false for logins, for a
// built-in key without rows (it keeps the catalog, or a Gemini key on Google's host its live list)
// and for a key no config entry matches.
func resolveKeyModelList(cfg *config.Config, auth *coreauth.Auth) (keyModelListInfo, bool) {
	if cfg == nil || auth == nil {
		return keyModelListInfo{}, false
	}
	var info keyModelListInfo
	if _, compatName, compat := openAICompatInfoFromAuth(auth); compat {
		entry := keyCompatEntry(cfg, auth, compatName)
		if entry == nil {
			return keyModelListInfo{}, false
		}
		info = keyModelListInfo{keyType: "openai-compatibility", kind: keyKindCompat, fixed: entry.FixedModels,
			rows: keyRowsOf(entry.Models), compat: entry}
	} else {
		if auth.AuthKind() != coreauth.AuthKindAPIKey {
			return keyModelListInfo{}, false
		}
		switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
		case "claude":
			if entry := resolveConfigClaudeKeyForConfig(cfg, auth); entry != nil {
				info = keyModelListInfo{keyType: "claude", kind: keyKindClaude, fixed: entry.FixedModels, rows: keyRowsOf(entry.Models)}
			}
		case "codex":
			if entry := resolveConfigCodexKeyForConfig(cfg, auth); entry != nil {
				info = keyModelListInfo{keyType: "codex", kind: keyKindCodex, fixed: entry.FixedModels, rows: keyRowsOf(entry.Models)}
			}
		case "xai":
			if entry := resolveConfigXAIKeyForConfig(cfg, auth); entry != nil {
				info = keyModelListInfo{keyType: "xai", kind: keyKindXAI, fixed: entry.FixedModels, rows: keyRowsOf(entry.Models)}
			}
		case "meta":
			if entry := resolveConfigMetaKeyForConfig(cfg, auth); entry != nil {
				info = keyModelListInfo{keyType: "meta", kind: keyKindMeta, fixed: entry.FixedModels, rows: keyRowsOf(entry.Models)}
			}
		case constant.Gemini:
			if entry := resolveConfigGeminiKeyForConfig(cfg, auth); entry != nil {
				kind := keyKindGemini
				if liveModelListGeminiHost(auth) {
					kind = "gemini" // the login kind: Google's list, already fetched for this key (model-lists.md 13)
				}
				info = keyModelListInfo{keyType: "gemini", kind: kind, fixed: entry.FixedModels, rows: keyRowsOf(entry.Models)}
			}
		case constant.GeminiInteractions:
			if entry := resolveConfigInteractionsKeyForConfig(cfg, auth); entry != nil {
				info = keyModelListInfo{keyType: "interactions", notAsked: keyNotAskedNoEndpoint, fixed: entry.FixedModels,
					rows: keyRowsOf(entry.Models)}
			}
		case "vertex":
			if entry := resolveConfigVertexCompatKeyForConfig(cfg, auth); entry != nil {
				info = keyModelListInfo{keyType: "vertex", notAsked: keyNotAskedNoEndpoint, rows: keyRowsOf(entry.Models)}
			}
		}
		if info.keyType == "" || len(info.rows) == 0 {
			return keyModelListInfo{}, false
		}
	}
	switch {
	case info.notAsked != "":
	case info.fixed:
		info.notAsked = keyNotAskedFixed
	case !cfg.Models.LiveListsOn() || cfg.Home.Enabled:
		info.notAsked = keyNotAskedLiveOff
	case info.kind == "gemini" && !liveModelListAsked["gemini"]:
		info.notAsked = keyNotAskedNoEndpoint
	}
	if info.notAsked != "" {
		info.kind = ""
	}
	return info, true
}

// keyCompatEntry finds an openai-compatibility key's entry the way its registration does: by the
// credential's config index, else by name. A disabled entry registers nothing and counts as none.
func keyCompatEntry(cfg *config.Config, auth *coreauth.Auth, compatName string) *config.OpenAICompatibility {
	if entry := configEntryForAuthIndex(auth, cfg.OpenAICompatibility); entry != nil {
		if entry.Disabled {
			return nil
		}
		return entry
	}
	if auth.Attributes != nil {
		if name := strings.TrimSpace(auth.Attributes["compat_name"]); name != "" {
			compatName = name
		}
	}
	for i := range cfg.OpenAICompatibility {
		entry := &cfg.OpenAICompatibility[i]
		if !entry.Disabled && strings.EqualFold(strings.TrimSpace(entry.Name), compatName) {
			return entry
		}
	}
	return nil
}

// keyModelListKind is the list kind of a config API key, or "" with the reason it is not asked
// ("fixed", "live_lists_off", "no_list_endpoint"). Kinds: "openai-compatibility", "claude-key",
// "codex-key", "xai-key", "meta-key", "gemini-key". A Gemini key on Google's host keeps the
// login kind "gemini". Kinds never equal a login type: only login types feed learned models.
// A login, or a built-in key without rows, gives "", "".
func keyModelListKind(cfg *config.Config, auth *coreauth.Auth) (kind, notAsked string) {
	info, ok := resolveKeyModelList(cfg, auth)
	if !ok {
		return "", ""
	}
	return info.kind, info.notAsked
}

// liveModelListKindFor is the list the fetcher keeps for an auth: a login's type, a key's list kind,
// or "" when nothing is fetched for it. A Gemini key on Google's host without rows keeps its login
// kind, unless its entry says fixed-models: true.
func liveModelListKindFor(cfg *config.Config, auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if info, ok := resolveKeyModelList(cfg, auth); ok {
		return info.kind
	}
	provider := liveModelListProvider(auth)
	if provider == "gemini" && keyGeminiFixed(cfg, auth) {
		return ""
	}
	return provider
}

// keyGeminiFixed reports whether a Gemini key's config entry says fixed-models: true.
func keyGeminiFixed(cfg *config.Config, auth *coreauth.Auth) bool {
	if auth == nil || auth.AuthKind() != coreauth.AuthKindAPIKey {
		return false
	}
	entry := resolveConfigGeminiKeyForConfig(cfg, auth)
	return entry != nil && entry.FixedModels
}

// liveModelListKind is liveModelListKindFor with the service's current config.
func (s *Service) liveModelListKind(auth *coreauth.Auth) string {
	return liveModelListKindFor(s.currentConfig(), auth)
}

// --- The request -------------------------------------------------------------------------------

// keyModelListURL is the list URL of section 4 for the key, "" when its type has none.
func keyModelListURL(kind string, auth *coreauth.Auth) string {
	switch kind {
	case keyKindCompat:
		base := liveModelListBaseURL(auth, "")
		if base == "" {
			return ""
		}
		if strings.HasSuffix(strings.ToLower(base), "/models") {
			return base
		}
		return base + "/models"
	case keyKindClaude:
		base := liveModelListBaseURL(auth, "https://api.anthropic.com")
		if strings.HasSuffix(strings.ToLower(base), "/v1") {
			base = base[:len(base)-len("/v1")]
		}
		return base + "/v1/models?" + url.Values{"limit": {"1000"}}.Encode()
	case keyKindCodex, keyKindXAI, keyKindMeta:
		fallback := ""
		if kind == keyKindMeta {
			fallback = "https://api.meta.ai/v1"
		}
		base := liveModelListBaseURL(auth, fallback)
		if base == "" {
			return ""
		}
		if strings.HasSuffix(strings.ToLower(base), "/v1") {
			return base + "/models"
		}
		return base + "/v1/models"
	case keyKindGemini:
		base := liveModelListBaseURL(auth, "")
		if base == "" {
			return ""
		}
		if strings.HasSuffix(strings.ToLower(base), "/v1beta") {
			base = base[:len(base)-len("/v1beta")]
		}
		return base + "/v1beta/models"
	}
	return ""
}

// errKeyModelListNoURL is a key whose type has a list endpoint but whose entry gives no host.
var errKeyModelListNoURL = errors.New("model list: the key has no base URL")

// fetchKeyModelList asks the key's service and returns its list (ids, display names). get sends a
// request with the key, the entry's headers and its proxy, as the executor does for a model
// request; bare sends it through the same proxy without them, and is used once by an
// openai-compatibility key whose service refuses the key on /models (401 or 403).
func fetchKeyModelList(kind string, auth *coreauth.Auth, get, bare liveModelListGet) (liveModelList, error) {
	rawURL := keyModelListURL(kind, auth)
	if rawURL == "" {
		return liveModelList{}, errKeyModelListNoURL
	}
	now := liveModelListNow()
	switch kind {
	case keyKindCompat:
		body, err := get(rawURL, nil)
		if err != nil && bare != nil {
			if category, status := provimuxLiveListFailureOf(err); category == provimuxLiveFailureHTTP &&
				(status == http.StatusUnauthorized || status == http.StatusForbidden) {
				body, err = bare(rawURL, nil)
			}
		}
		if err != nil {
			return liveModelList{}, err
		}
		return parseOpenAIModelList(body, now)
	case keyKindClaude:
		header := http.Header{}
		header.Set("Anthropic-Version", "2023-06-01")
		body, err := get(rawURL, header)
		if err != nil {
			return liveModelList{}, err
		}
		return parseClaudeLiveModelList(body, now)
	case keyKindCodex, keyKindXAI, keyKindMeta:
		body, err := get(rawURL, nil)
		if err != nil {
			return liveModelList{}, err
		}
		return parseOpenAIModelList(body, now)
	case keyKindGemini:
		var pages [][]byte
		token := ""
		for page := 0; ; page++ {
			if page == geminiLiveModelListMaxPages {
				return liveModelList{}, fmt.Errorf("gemini model list: %w (more than %d pages)", errLiveModelListIncomplete, geminiLiveModelListMaxPages)
			}
			query := url.Values{"pageSize": {geminiLiveModelListPageSize}}
			if token != "" {
				query.Set("pageToken", token)
			}
			body, err := get(rawURL+"?"+query.Encode(), nil)
			if err != nil {
				return liveModelList{}, err
			}
			if !ojson.ValidBytes(body) {
				return liveModelList{}, fmt.Errorf("gemini model list: invalid JSON")
			}
			pages = append(pages, body)
			token = strings.TrimSpace(ojson.GetBytes(body, "nextPageToken").String())
			if token == "" {
				break
			}
		}
		return parseKeyGeminiModelList(pages, now)
	}
	return liveModelList{}, fmt.Errorf("unsupported key list kind %q", kind)
}

// parseOpenAIModelList reads an OpenAI-shaped /models answer as the console's button does
// (provimux-ui src/utils/models.ts): the array data, else models, else the answer itself; an
// entry is a string, or an object whose id is id, else name, model, value; display_name or
// displayName is kept when it differs from the id. An answer that is no list, or names no model,
// is a failed fetch.
func parseOpenAIModelList(body []byte, now time.Time) (liveModelList, error) {
	if !ojson.ValidBytes(body) {
		return liveModelList{}, fmt.Errorf("openai model list: invalid JSON")
	}
	root := ojson.ParseBytes(body)
	var items ojson.Result
	switch {
	case root.IsArray():
		items = root
	case root.Get("data").IsArray():
		items = root.Get("data")
	case root.Get("models").IsArray():
		items = root.Get("models")
	default:
		return liveModelList{}, fmt.Errorf("openai model list: %w", errLiveModelListMalformed)
	}
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for _, item := range items.Array() {
		var id, displayName string
		switch {
		case item.Type == ojson.String:
			id = item.String()
		case item.IsObject():
			for _, field := range []string{"id", "name", "model", "value"} {
				if value := item.Get(field); value.Type == ojson.String && strings.TrimSpace(value.String()) != "" {
					id = value.String()
					break
				}
			}
			for _, field := range []string{"display_name", "displayName"} {
				if value := item.Get(field); value.Type == ojson.String && strings.TrimSpace(value.String()) != "" {
					displayName = strings.TrimSpace(value.String())
					break
				}
			}
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, dup := list.ids[key]; dup {
			continue
		}
		list.ids[key] = struct{}{}
		model := &ModelInfo{ID: id, Object: "model"}
		if displayName != id {
			model.DisplayName = displayName
		}
		list.extra = append(list.extra, model)
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("openai-compatible service")
	}
	return list, nil
}

// parseKeyGeminiModelList reads every page of a Gemini-style host of the owner's: as Google's list,
// except that a model which states no supportedGenerationMethods is kept (a gateway may not say);
// one that states them without generateContent is not.
func parseKeyGeminiModelList(pages [][]byte, now time.Time) (liveModelList, error) {
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: now}
	for i, page := range pages {
		if !ojson.ValidBytes(page) {
			return liveModelList{}, fmt.Errorf("gemini model list: invalid JSON")
		}
		root := ojson.ParseBytes(page)
		if models := root.Get("models"); models.Exists() && !models.IsArray() {
			return liveModelList{}, fmt.Errorf("gemini model list: %w", errLiveModelListMalformed)
		}
		if i == len(pages)-1 && strings.TrimSpace(root.Get("nextPageToken").String()) != "" {
			return liveModelList{}, fmt.Errorf("gemini model list: %w", errLiveModelListIncomplete)
		}
		for _, item := range root.Get("models").Array() {
			id := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item.Get("name").String()), "models/"))
			if id == "" {
				continue
			}
			if methods := item.Get("supportedGenerationMethods"); len(methods.Array()) > 0 && !geminiLiveGenerates(item) {
				continue
			}
			key := strings.ToLower(id)
			if _, dup := list.ids[key]; dup {
				continue
			}
			list.ids[key] = struct{}{}
			model := &ModelInfo{ID: id, Object: "model"}
			if name := strings.TrimSpace(item.Get("displayName").String()); name != id {
				model.DisplayName = name
			}
			list.extra = append(list.extra, model)
		}
	}
	if len(list.ids) == 0 {
		return liveModelList{}, errEmptyLiveModelList("gemini")
	}
	return list, nil
}

// --- The merge ---------------------------------------------------------------------------------

type keyLearnedModel struct{ ID, DisplayName string }

// keyRowNames are the lower-case names a key's rows take: the models they name (without a thinking
// suffix) and the names clients use for them (alias, else name).
func keyRowNames[T modelEntry](rows []T) (models, clientNames map[string]struct{}) {
	models, clientNames = map[string]struct{}{}, map[string]struct{}{}
	for _, row := range rows {
		name := strings.TrimSpace(row.GetName())
		if base := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(name).ModelName)); base != "" {
			models[base] = struct{}{}
		}
		client := strings.TrimSpace(row.GetAlias())
		if client == "" {
			client = name
		}
		if client != "" {
			clientNames[strings.ToLower(client)] = struct{}{}
		}
	}
	return models, clientNames
}

// keyLearnedModels returns the listed ids no row names (3.1, 3.2), in the list's order: not an id
// a row names (its name without a thinking suffix, compared without case), not a row's client name
// (alias, else name), not an id that reads as a thinking suffix, not empty, each once.
func keyLearnedModels[T modelEntry](rows []T, list liveModelList) []keyLearnedModel {
	named, taken := keyRowNames(rows)
	seen := map[string]struct{}{}
	var out []keyLearnedModel
	for _, model := range list.extra {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		key := strings.ToLower(id)
		if id == "" || thinking.ParseSuffix(id).HasSuffix {
			continue
		}
		if _, listed := list.ids[key]; !listed {
			continue
		}
		_, dup := seen[key]
		_, rowModel := named[key]
		_, rowName := taken[key]
		if dup || rowModel || rowName {
			continue
		}
		seen[key] = struct{}{}
		learned := keyLearnedModel{ID: id}
		if name := strings.TrimSpace(model.DisplayName); name != "" && name != id {
			learned.DisplayName = name
		}
		out = append(out, learned)
	}
	return out
}

// keyUnlistedRows returns the rows whose model the list does not name, by the row's name as written.
func keyUnlistedRows[T modelEntry](rows []T, list liveModelList) []string {
	var out []string
	for _, row := range rows {
		name := strings.TrimSpace(row.GetName())
		if _, listed := list.ids[strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(name).ModelName))]; !listed && name != "" {
			out = append(out, name)
		}
	}
	return out
}

// buildKeyLearnedModels builds one bare row per learned id with the key type's own builder, as the
// row the console's button would save: {name: <id>}, with display-name when the service states one.
func buildKeyLearnedModels(info keyModelListInfo, learned []keyLearnedModel) []*ModelInfo {
	if len(learned) == 0 {
		return nil
	}
	switch info.keyType {
	case "openai-compatibility":
		if info.compat == nil {
			return nil
		}
		entry := config.OpenAICompatibility{Name: info.compat.Name, BaseURL: info.compat.BaseURL}
		for _, model := range learned {
			entry.Models = append(entry.Models, config.OpenAICompatibilityModel{Name: model.ID, DisplayName: model.DisplayName})
		}
		return buildOpenAICompatibilityConfigModels(&entry)
	case "claude":
		rows := make([]config.ClaudeModel, 0, len(learned))
		for _, model := range learned {
			rows = append(rows, config.ClaudeModel{Name: model.ID, DisplayName: model.DisplayName})
		}
		return buildConfigModels(rows, "anthropic", "claude", "claude")
	case "codex", "xai", "meta":
		rows := make([]config.CodexModel, 0, len(learned))
		for _, model := range learned {
			rows = append(rows, config.CodexModel{Name: model.ID, DisplayName: model.DisplayName})
		}
		switch info.keyType {
		case "codex":
			return buildConfigModels(rows, "openai", "openai", "codex")
		case "xai":
			return buildConfigModels(rows, "xai", "xai", "xai")
		}
		return buildConfigModels(rows, "meta", "meta", "meta")
	case "gemini":
		rows := make([]config.GeminiModel, 0, len(learned))
		for _, model := range learned {
			rows = append(rows, config.GeminiModel{Name: model.ID, DisplayName: model.DisplayName})
		}
		return buildConfigModels(rows, "google", "gemini", "gemini")
	}
	return nil
}

// keyHeldModelList returns the key's held list, reading the disk cache once if none is in memory,
// and asks for a fetch when there is none.
func (s *Service) keyHeldModelList(auth *coreauth.Auth, kind string) (liveModelList, bool) {
	s.openLiveModelListCache()
	identity := liveModelListIdentity(auth)
	list, ok := liveModelLists.get(auth.ID, identity)
	if !ok && liveModelLists.loadCached(auth.ID, identity, kind) {
		if list, ok = liveModelLists.get(auth.ID, identity); ok {
			if kind == "gemini" {
				provimuxUpdateLearnedModels(kind)
				log.Info(provimuxLiveLogLine(kind, auth, list))
			} else {
				log.Info(keyModelListLogLine(s.currentConfig(), auth, list))
			}
		}
	}
	if !ok {
		liveModelLists.request(false)
	}
	return list, ok
}

// applyKeyModelList returns models plus the key's learned models, built by its type's builder.
// It adds nothing for a login, a key that is not asked, or a key with no held list (and asks for
// a fetch). For an openai-compatibility key it then applies the entry's excluded-models, whether
// or not anything was added (6.1); the built-in key types get theirs from the line that follows
// the hook. It never changes cfg or the slice it is given.
func (s *Service) applyKeyModelList(cfg *config.Config, auth *coreauth.Auth, models []*ModelInfo) []*ModelInfo {
	info, ok := resolveKeyModelList(cfg, auth)
	if !ok {
		return models
	}
	out := models
	if info.kind != "" {
		if list, held := s.keyHeldModelList(auth, info.kind); held {
			if built := buildKeyLearnedModels(info, keyLearnedModels(info.rows, list)); len(built) > 0 {
				out = make([]*ModelInfo, 0, len(models)+len(built))
				out = append(out, models...)
				out = append(out, built...)
			}
		}
	}
	if info.compat != nil && len(info.compat.ExcludedModels) > 0 {
		out = applyExcludedModels(out, info.compat.ExcludedModels)
	}
	return out
}

// --- Log lines ---------------------------------------------------------------------------------

// keyModelListLogName names a key in the log: its entry's name for an openai-compatibility key,
// else its type, and its auth index, which carries no key.
func keyModelListLogName(auth *coreauth.Auth) string {
	name := strings.ToLower(strings.TrimSpace(auth.Provider))
	if _, compatName, compat := openAICompatInfoFromAuth(auth); compat && compatName != "" {
		name = compatName
	}
	return name + " key " + provimuxLiveLogName(auth)
}

// keyModelListLogLine is the line of a key whose list changed:
// "model list for openrouter key c41d9a07…: live, 342 listed, 341 added to 2 configured
// (1 configured not listed: acme/retired-model)".
func keyModelListLogLine(cfg *config.Config, auth *coreauth.Auth, list liveModelList) string {
	info, _ := resolveKeyModelList(cfg, auth)
	learned := keyLearnedModels(info.rows, list)
	line := fmt.Sprintf("model list for %s: live, %d listed, %d added to %d configured", keyModelListLogName(auth),
		len(list.ids), len(learned), len(info.rows))
	if unlisted := keyUnlistedRows(info.rows, list); len(unlisted) > 0 {
		if len(unlisted) > 5 {
			unlisted = append(unlisted[:5:5], "…")
		}
		line += fmt.Sprintf(" (%d configured not listed: %s)", len(keyUnlistedRows(info.rows, list)), strings.Join(unlisted, ", "))
	}
	if list.fromCache {
		line += ", read from the disk cache (list of " + list.fetchedAt.UTC().Format(time.DateOnly) + ")"
	}
	return line
}

// keyModelListKeptText says what a key offers after a failed fetch.
func keyModelListKeptText(authID, identity string) string {
	if list, ok := liveModelLists.get(authID, identity); ok {
		return "keeping the list of " + list.fetchedAt.UTC().Format(time.DateOnly) + " added to the configured models"
	}
	return "keeping the configured models"
}

// --- The fetcher's helpers ---------------------------------------------------------------------

// keyModelListExecutorKey is the executor a key's list request goes through: its provider's, and
// for an openai-compatibility key its entry's.
func keyModelListExecutorKey(auth *coreauth.Auth) string {
	if providerKey, _, compat := openAICompatInfoFromAuth(auth); compat && providerKey != "" {
		return providerKey
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// keyListIDs returns the auth IDs whose held list is a key's.
func (st *liveModelListStore) keyListIDs() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var ids []string
	for id, list := range st.lists {
		if isKeyModelListKind(list.provider) {
			ids = append(ids, id)
		}
	}
	return ids
}

// refreshKeyRegistrations registers the given keys again, so a key whose list was dropped offers its
// configured models.
func (s *Service) refreshKeyRegistrations(ids []string) {
	if s == nil || s.coreManager == nil {
		return
	}
	for _, id := range ids {
		if current, ok := s.coreManager.GetByID(id); ok && current != nil && !current.Disabled {
			s.refreshModelRegistrationForAuth(current)
		}
	}
}
