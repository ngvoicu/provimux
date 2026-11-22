package cliproxy

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Provimux: learned models (model-lists.md 5.1) and the log lines of 5.5.
//
// A section's learned models are the models a held live list of any login of that type names
// and the catalog lacks. They are registered only for the logins whose list names them (by
// filterByLiveModelList, the Copilot catalog and the Antigravity hints); here they are recorded in
// the registry so its lookups find them as well.

var (
	errLiveModelListMalformed  = errors.New("malformed answer")
	errLiveModelListIncomplete = errors.New("incomplete answer")
	errLiveModelListTooLarge   = errors.New("answer larger than 8 MB")
)

// provimuxLearnedMu serialises recomputations, so two of them never publish out of order.
var provimuxLearnedMu sync.Mutex

// provimuxCatalogIDs is the set of lower-case ids of a section's catalog models.
func provimuxCatalogIDs(section string) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, model := range registry.ProvimuxCatalogModels(section) {
		if model != nil {
			ids[strings.ToLower(model.ID)] = struct{}{}
		}
	}
	return ids
}

// provimuxLiveLearned returns the models of a list that the catalog lacks, in list order.
func provimuxLiveLearned(provider string, list liveModelList, catalog map[string]struct{}) []*ModelInfo {
	builtins := liveModelListBuiltins(provider)
	var learned []*ModelInfo
	for _, model := range list.extra {
		if model == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(model.ID))
		_, listed := list.ids[key]
		_, known := catalog[key]
		_, builtin := builtins[key]
		if listed && !known && !builtin {
			learned = append(learned, model)
		}
	}
	return learned
}

// provimuxUpdateLearnedModels recomputes the learned models of the given sections from the held
// live lists (and, for copilot and antigravity, from their own stores).
func provimuxUpdateLearnedModels(sections ...string) {
	provimuxLearnedMu.Lock()
	defer provimuxLearnedMu.Unlock()
	done := map[string]bool{}
	for _, section := range sections {
		if section == "" || done[section] {
			continue
		}
		done[section] = true
		var learned []*ModelInfo
		switch section {
		case "copilot":
			learned = provimuxCopilotLearned()
		case "antigravity":
			learned = provimuxAntigravityLearned()
		default:
			learned = provimuxStoreLearned(section)
		}
		registry.SetProvimuxLearnedModels(section, learned)
	}
}

// provimuxStoreLearned is the union, by auth ID order, of the learned models of the held lists of a
// login type.
func provimuxStoreLearned(provider string) []*ModelInfo {
	catalog := provimuxCatalogIDs(provider)
	now := liveModelListNow()
	liveModelLists.mu.Lock()
	ids := make([]string, 0, len(liveModelLists.lists))
	for id, list := range liveModelLists.lists {
		if list.provider == provider && liveModelListHeld(list, now) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	lists := make([]liveModelList, 0, len(ids))
	for _, id := range ids {
		lists = append(lists, liveModelLists.lists[id])
	}
	liveModelLists.mu.Unlock()
	var learned []*ModelInfo
	seen := map[string]struct{}{}
	for _, list := range lists {
		for _, model := range provimuxLiveLearned(provider, list, catalog) {
			key := strings.ToLower(model.ID)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			learned = append(learned, model)
		}
	}
	return learned
}

// provimuxLearningOn reports whether logins may offer learned models: models.live-lists is on
// (unset counts as on) and the process is not in home mode.
func provimuxLearningOn(cfg *config.Config) bool {
	if cfg == nil {
		return registry.CatalogSources{}.LiveListsOn()
	}
	return cfg.Models.LiveListsOn() && !cfg.Home.Enabled
}

// provimuxLiveLogName names a login in the log by its auth index, which the console shows too and
// which carries no account name.
func provimuxLiveLogName(auth *coreauth.Auth) string {
	if auth == nil {
		return "?"
	}
	if index := strings.TrimSpace(auth.Clone().EnsureIndex()); index != "" {
		return index
	}
	return "?"
}

// provimuxLiveLogLine is the line of a login whose list changed:
// "model list for devin login a8d1…: live, 53 models, 1 not in the catalog yet (gemini-4-neon)".
func provimuxLiveLogLine(provider string, auth *coreauth.Auth, list liveModelList) string {
	learned := provimuxLiveLearned(provider, list, provimuxCatalogIDs(provider))
	line := fmt.Sprintf("model list for %s login %s: live, %d models", provider, provimuxLiveLogName(auth), len(list.ids))
	if len(learned) > 0 {
		names := make([]string, 0, 5)
		for i, model := range learned {
			if i == 5 {
				names = append(names, "…")
				break
			}
			names = append(names, provimuxPublicModelID(provider, model.ID))
		}
		line += fmt.Sprintf(", %d not in the catalog yet (%s)", len(learned), strings.Join(names, ", "))
	}
	if list.fromCache {
		line += ", read from the disk cache (list of " + list.fetchedAt.UTC().Format(time.DateOnly) + ")"
	}
	return line
}

func provimuxLiveFailureText(category string, status int) string {
	if category == provimuxLiveFailureHTTP && status > 0 {
		return fmt.Sprintf("%s %d", category, status)
	}
	return category
}

// provimuxLiveKeptText says what the login offers after a failed fetch.
func provimuxLiveKeptText(authID, identity string) string {
	if list, ok := liveModelLists.get(authID, identity); ok {
		return "keeping the list of " + list.fetchedAt.UTC().Format(time.DateOnly)
	}
	return "the catalog stays in use"
}

// provimuxPublicModelID is the id under which a login of the provider registers a model of its catalog
// or live list. Since 8.1.1 the Devin catalog, Devin's live lists and its learned models hold bare ids
// (swe-2), and the registration adds the devin/ namespace (applyModelNamespace in service_models.go).
func provimuxPublicModelID(provider, id string) string {
	if provider == "devin" && id != "" && !strings.HasPrefix(strings.ToLower(id), "devin/") {
		return "devin/" + id
	}
	return id
}

// provimuxListKeyOfRegistered is the key a registered model is matched by against its login's catalog,
// live list and Provimux's own models: lower case, and for Devin without the devin/ namespace. A Devin
// id without that namespace (a configured alias, a plugin's model) is no native id: "" matches nothing.
func provimuxListKeyOfRegistered(provider, id string) string {
	key := strings.ToLower(strings.TrimSpace(id))
	if provider == "devin" {
		native, ok := strings.CutPrefix(key, "devin/")
		if !ok {
			return ""
		}
		key = native
	}
	return key
}
