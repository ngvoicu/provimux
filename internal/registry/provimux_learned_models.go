package registry

import (
	"reflect"
	"strings"
	"sync"
)

// Provimux: learned models. A login's vendor list can name a model the catalog lacks (a model
// released after the catalog was built). sdk/cliproxy registers such a model for the logins whose
// list names it, and records it here, per login type, so the lookups requests make find it too:
// the Devin executor reads a model's effort levels through LookupDevinModel, and Copilot routes
// a model only when its section names it. A section's learned models are the union of what the
// live lists of that type's logins name and the catalog lacks; logins without a live list never
// register them (sdk/cliproxy filters them out).
//
// Sections: claude, codex, meta, copilot, antigravity, devin, kimi, xai, gemini.

var provimuxLearned = struct {
	mu        sync.RWMutex
	bySection map[string][]*ModelInfo
}{bySection: make(map[string][]*ModelInfo)}

// ProvimuxLearnedSection maps a lookup channel to its learned section ("" for none).
func ProvimuxLearnedSection(channel string) string {
	switch key := strings.ToLower(strings.TrimSpace(channel)); key {
	case "claude", "codex", "meta", "copilot", "antigravity", "devin", "xai", "gemini":
		return key
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return "kimi"
	case "x-ai", "grok":
		return "xai"
	case "muse":
		return "meta"
	case "gemini-interactions":
		return "gemini"
	}
	return ""
}

// SetProvimuxLearnedModels replaces a section's learned models and reports whether they changed.
// Models are stored as copies, in the order given; nil or empty clears the section.
func SetProvimuxLearnedModels(section string, models []*ModelInfo) bool {
	section = ProvimuxLearnedSection(section)
	if section == "" {
		return false
	}
	clean := make([]*ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(model.ID))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, cloneModelInfo(model))
	}
	provimuxLearned.mu.Lock()
	defer provimuxLearned.mu.Unlock()
	previous := provimuxLearned.bySection[section]
	if len(clean) == 0 {
		delete(provimuxLearned.bySection, section)
		return len(previous) > 0
	}
	provimuxLearned.bySection[section] = clean
	return !reflect.DeepEqual(previous, clean)
}

// ProvimuxLearnedModels returns copies of a section's learned models.
func ProvimuxLearnedModels(section string) []*ModelInfo {
	section = ProvimuxLearnedSection(section)
	provimuxLearned.mu.RLock()
	defer provimuxLearned.mu.RUnlock()
	return cloneModelInfos(provimuxLearned.bySection[section])
}

// ProvimuxIsLearned reports whether id is a learned model of the section (case-insensitive; for
// Devin with or without the "devin/" namespace).
func ProvimuxIsLearned(section, id string) bool {
	return provimuxLookupLearned(id, section) != nil
}

// provimuxLookupLearned is the lookups' last resort: a learned model of the channel's section, or nil.
func provimuxLookupLearned(modelID, channel string) *ModelInfo {
	section := ProvimuxLearnedSection(channel)
	key := strings.ToLower(strings.TrimSpace(modelID))
	if section == "" || key == "" {
		return nil
	}
	if section == "devin" {
		key = strings.TrimPrefix(key, "devin/")
	}
	provimuxLearned.mu.RLock()
	defer provimuxLearned.mu.RUnlock()
	for _, model := range provimuxLearned.bySection[section] {
		id := strings.ToLower(model.ID)
		if section == "devin" {
			id = strings.TrimPrefix(id, "devin/")
		}
		if id == key {
			return cloneModelInfo(model)
		}
	}
	return nil
}

// provimuxLookupLearnedDevin finds a learned Devin model by its id or, for a variant id such as
// gemini-4-neon-high, by its base, as LookupDevinModel does for the catalog.
func provimuxLookupLearnedDevin(clean string) *ModelInfo {
	if model := provimuxLookupLearned(clean, "devin"); model != nil {
		return model
	}
	if base, _ := splitDevinModelID(clean); base != clean && base != "" {
		return provimuxLookupLearned(base, "devin")
	}
	return nil
}

// ProvimuxCatalogModels returns copies of a section's catalog models, without learned ones. For
// codex it is the union of the four plan sections.
func ProvimuxCatalogModels(section string) []*ModelInfo {
	switch section = ProvimuxLearnedSection(section); section {
	case "":
		return nil
	case "copilot":
		if data := getModels(); data != nil {
			return cloneModelInfos(data.Copilot)
		}
		return nil
	case "codex":
		var out []*ModelInfo
		seen := map[string]struct{}{}
		for _, models := range [][]*ModelInfo{GetCodexProModels(), GetCodexPlusModels(), GetCodexTeamModels(), GetCodexFreeModels()} {
			for _, model := range models {
				if model == nil {
					continue
				}
				if _, ok := seen[strings.ToLower(model.ID)]; !ok {
					seen[strings.ToLower(model.ID)] = struct{}{}
					out = append(out, model)
				}
			}
		}
		return out
	}
	return GetStaticModelDefinitionsByChannel(section)
}

// withProvimuxLearned appends a section's learned models to its catalog models.
func withProvimuxLearned(models []*ModelInfo, section string) []*ModelInfo {
	learned := ProvimuxLearnedModels(section)
	if len(learned) == 0 {
		return models
	}
	have := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model != nil {
			have[strings.ToLower(model.ID)] = struct{}{}
		}
	}
	for _, model := range learned {
		if _, ok := have[strings.ToLower(model.ID)]; !ok {
			models = append(models, model)
		}
	}
	return models
}
