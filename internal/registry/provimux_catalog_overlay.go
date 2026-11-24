package registry

import (
	"reflect"
	"strings"
	"sync"
)

// Provimux: providers.dev fills the catalog's gaps, and never overrides a value the catalog states
// (model-lists.md 13.5, step P3). Per field: the vendor's live list where the catalog leaves the field empty,
// then the catalog, then providers.dev, only for a field both leave empty, and only the name, window, output
// limit and modalities: providers.dev never supplies the thinking block or web search, for any model.
//
// sdk/cliproxy registers an overlay: for a catalog model of a login type, the model with providers.dev's
// value in those fields the catalog leaves empty. Every read of the catalog (getModels, the Devin store)
// returns the filled catalog, so the registered models, the lookups executors make (LookupModelInfo, LookupDevinModel, Copilot's resolver) and the status endpoint see the
// same values. Upstream's updater keeps reading and replacing the raw catalog (provimuxRawModels), so a
// filled gap is never stored in it. Without an overlay, or with nothing to fill, the raw catalog itself is
// returned.

// ProvimuxCatalogOverlay returns the overlaid model of a section, or nil to keep raw unchanged. It must
// not modify raw.
type ProvimuxCatalogOverlay func(section string, raw *ModelInfo) *ModelInfo

var provimuxOverlay = struct {
	mu         sync.Mutex
	fn         ProvimuxCatalogOverlay
	generation uint64
	modelsRaw  *staticModelsJSON
	modelsGen  uint64
	models     *staticModelsJSON
	devinRaw   []*ModelInfo
	devinGen   uint64
	devin      []*ModelInfo
}{}

// SetProvimuxCatalogOverlay registers the overlay (nil removes it) and drops every overlaid copy.
func SetProvimuxCatalogOverlay(fn ProvimuxCatalogOverlay) {
	provimuxOverlay.mu.Lock()
	defer provimuxOverlay.mu.Unlock()
	provimuxOverlay.fn = fn
	provimuxOverlay.generation++
}

// ProvimuxCatalogOverlayChanged drops the overlaid copies: the overlay's answers changed.
func ProvimuxCatalogOverlayChanged() {
	provimuxOverlay.mu.Lock()
	defer provimuxOverlay.mu.Unlock()
	provimuxOverlay.generation++
}

// provimuxRawModels is the catalog as upstream's updater stored it.
func provimuxRawModels() *staticModelsJSON {
	modelsCatalogStore.mu.RLock()
	defer modelsCatalogStore.mu.RUnlock()
	return modelsCatalogStore.data
}

// provimuxOverlaidModels returns the catalog with the overlay applied, computed once per catalog and
// overlay generation.
func provimuxOverlaidModels(raw *staticModelsJSON) *staticModelsJSON {
	if raw == nil {
		return nil
	}
	provimuxOverlay.mu.Lock()
	defer provimuxOverlay.mu.Unlock()
	if provimuxOverlay.fn == nil {
		return raw
	}
	if provimuxOverlay.modelsRaw == raw && provimuxOverlay.modelsGen == provimuxOverlay.generation {
		return provimuxOverlay.models
	}
	out := *raw
	changed := false
	for _, part := range provimuxSectionsOf(&out) {
		if overlaid, ok := provimuxOverlaySlice(provimuxOverlay.fn, part.section, *part.models); ok {
			*part.models = overlaid
			changed = true
		}
	}
	result := raw
	if changed {
		result = &out
	}
	provimuxOverlay.modelsRaw, provimuxOverlay.modelsGen, provimuxOverlay.models = raw, provimuxOverlay.generation, result
	return result
}

// provimuxOverlaidDevin returns the Devin store's models with the overlay applied.
func provimuxOverlaidDevin(raw []*ModelInfo) []*ModelInfo {
	if len(raw) == 0 {
		return raw
	}
	provimuxOverlay.mu.Lock()
	defer provimuxOverlay.mu.Unlock()
	if provimuxOverlay.fn == nil {
		return raw
	}
	if len(provimuxOverlay.devinRaw) == len(raw) && &provimuxOverlay.devinRaw[0] == &raw[0] && provimuxOverlay.devinGen == provimuxOverlay.generation {
		return provimuxOverlay.devin
	}
	result := raw
	if overlaid, ok := provimuxOverlaySlice(provimuxOverlay.fn, "devin", raw); ok {
		result = overlaid
	}
	provimuxOverlay.devinRaw, provimuxOverlay.devinGen, provimuxOverlay.devin = raw, provimuxOverlay.generation, result
	return result
}

// provimuxOverlaySlice applies fn to every model of a section; ok is false when nothing changed.
func provimuxOverlaySlice(fn ProvimuxCatalogOverlay, section string, models []*ModelInfo) ([]*ModelInfo, bool) {
	var out []*ModelInfo
	for i, model := range models {
		if model == nil {
			continue
		}
		overlaid := fn(section, model)
		if overlaid == nil || overlaid == model {
			continue
		}
		if out == nil {
			out = append([]*ModelInfo(nil), models...)
		}
		out[i] = overlaid
	}
	return out, out != nil
}

type provimuxSectionPart struct {
	section string
	models  *[]*ModelInfo
}

// provimuxSectionsOf names the login type of each section of models.json. gemini-cli is read by nothing.
func provimuxSectionsOf(data *staticModelsJSON) []provimuxSectionPart {
	return []provimuxSectionPart{
		{"copilot", &data.Copilot}, {"claude", &data.Claude}, {"gemini", &data.Gemini}, {"vertex", &data.Vertex},
		{"aistudio", &data.AIStudio}, {"codex", &data.CodexFree}, {"codex", &data.CodexTeam}, {"codex", &data.CodexPlus},
		{"codex", &data.CodexPro}, {"kimi", &data.Kimi}, {"antigravity", &data.Antigravity}, {"xai", &data.XAI},
		{"devin", &data.Devin}, {"meta", &data.Meta},
	}
}

// ProvimuxRawCatalogModel returns a copy of a section's catalog model as the catalog states it, without
// providers.dev's values, or nil. Devin ids are matched with or without devin/.
func ProvimuxRawCatalogModel(section, id string) *ModelInfo {
	section = strings.ToLower(strings.TrimSpace(section))
	key := strings.ToLower(strings.TrimSpace(id))
	var candidates []*ModelInfo
	if section == "devin" {
		key = strings.TrimPrefix(key, "devin/")
		devinCatalogStore.mu.RLock()
		candidates = devinCatalogStore.models
		devinCatalogStore.mu.RUnlock()
		if len(candidates) == 0 {
			if raw := provimuxRawModels(); raw != nil {
				candidates = raw.Devin
			}
		}
	} else if raw := provimuxRawModels(); raw != nil {
		for _, part := range provimuxSectionsOf(raw) {
			if part.section == section {
				candidates = append(candidates, *part.models...)
			}
		}
	}
	for _, model := range candidates {
		if model == nil {
			continue
		}
		modelKey := strings.ToLower(strings.TrimSpace(model.ID))
		if section == "devin" {
			modelKey = strings.TrimPrefix(modelKey, "devin/")
		}
		if modelKey == key {
			return cloneModelInfo(model)
		}
	}
	return nil
}

// ProvimuxRawOfOverlaid returns the catalog's own entry behind model when model is a catalog entry the
// overlay changed (equal to the overlaid entry of its id), else nil: model is then what the catalog
// states, or not a catalog entry at all (a config model, a plugin's).
func ProvimuxRawOfOverlaid(section string, model *ModelInfo) *ModelInfo {
	if model == nil {
		return nil
	}
	section = strings.ToLower(strings.TrimSpace(section))
	key := strings.ToLower(strings.TrimSpace(model.ID))
	var overlaid []*ModelInfo
	if section == "devin" {
		key = strings.TrimPrefix(key, "devin/")
		overlaid = devinModelDefinitions()
	} else if data := getModels(); data != nil {
		for _, part := range provimuxSectionsOf(data) {
			if part.section == section {
				overlaid = append(overlaid, *part.models...)
			}
		}
	}
	for _, entry := range overlaid {
		if entry == nil {
			continue
		}
		entryKey := strings.ToLower(strings.TrimSpace(entry.ID))
		if section == "devin" {
			entryKey = strings.TrimPrefix(entryKey, "devin/")
		}
		if entryKey != key {
			continue
		}
		raw := ProvimuxRawCatalogModel(section, entry.ID)
		if raw == nil || reflect.DeepEqual(raw, entry) || !reflect.DeepEqual(model, entry) {
			return nil
		}
		return raw
	}
	return nil
}
