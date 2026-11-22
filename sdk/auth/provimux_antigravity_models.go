package auth

import (
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
)

// Provimux: what Antigravity's fetchAvailableModels says about each model. Upstream's runtime
// keeps only the ids of the answer (ParseAntigravityModelCapabilityHints); its own tool
// (cmd/fetch_antigravity_models) also reads displayName, maxTokens and maxOutputTokens. Provimux
// keeps those three per model id, so a model the catalog lacks is offered with what the vendor
// said about it. Descriptions are per model, not per account: the newest answer wins.

// AntigravityModelDescription is what one answer said about one model.
type AntigravityModelDescription struct {
	DisplayName     string
	MaxTokens       int
	MaxOutputTokens int
}

var provimuxAntigravityDescriptions = struct {
	mu   sync.RWMutex
	byID map[string]AntigravityModelDescription
}{byID: make(map[string]AntigravityModelDescription)}

// provimuxObserveAntigravityModels records the descriptions of a successful answer.
func provimuxObserveAntigravityModels(body []byte) {
	models := ojson.GetBytes(body, "models")
	if !models.IsObject() {
		return
	}
	descriptions := make(map[string]AntigravityModelDescription)
	for id, item := range models.Map() {
		normalized := NormalizeAntigravityModelID(id)
		if normalized == "" {
			continue
		}
		descriptions[normalized] = AntigravityModelDescription{
			DisplayName:     strings.TrimSpace(item.Get("displayName").String()),
			MaxTokens:       int(item.Get("maxTokens").Int()),
			MaxOutputTokens: int(item.Get("maxOutputTokens").Int()),
		}
		if len(descriptions) >= antigravityMaxCatalogModels {
			break
		}
	}
	provimuxAntigravityDescriptions.mu.Lock()
	for id, description := range descriptions {
		provimuxAntigravityDescriptions.byID[id] = description
	}
	provimuxAntigravityDescriptions.mu.Unlock()
}

// ProvimuxAntigravityModelDescription returns what the newest answer said about a model.
func ProvimuxAntigravityModelDescription(modelID string) (AntigravityModelDescription, bool) {
	provimuxAntigravityDescriptions.mu.RLock()
	defer provimuxAntigravityDescriptions.mu.RUnlock()
	description, ok := provimuxAntigravityDescriptions.byID[NormalizeAntigravityModelID(modelID)]
	return description, ok
}
