package registry

import (
	"sort"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
)

// devinOutputLimitPlaceholder is the max_completion_tokens upstream's
// fetch_devin_models writes for every Devin model, whatever Devin says.
const devinOutputLimitPlaceholder = 64000

// devinOutputLimitReally64000 lists the catalog models whose Devin model config
// itself says 64000 (read with go run ./cmd/fetch_devin_models). None as of
// 2026-11-26: every variant Devin serves reports its own limit.
var devinOutputLimitReally64000 = map[string]bool{}

// TestEmbeddedDevinCatalogHasNoOutputLimitPlaceholder fails, naming the models,
// when the embedded Devin catalog holds a model with upstream's placeholder
// output limit (or none) that our regenerated data does not cover: typically a
// model upstream added after the list was last regenerated. Fix: regenerate
// internal/registry/models/devin_models.json with go run ./cmd/fetch_devin_models
// (needs a Devin login) in the patch that reads Devin's output limits.
func TestEmbeddedDevinCatalogHasNoOutputLimitPlaceholder(t *testing.T) {
	var catalog struct {
		Devin []struct {
			ID                  string `json:"id"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
		} `json:"devin"`
	}
	if err := json.Unmarshal(embeddedDevinModelsJSON, &catalog); err != nil {
		t.Fatalf("parse embedded devin_models.json: %v", err)
	}
	if len(catalog.Devin) == 0 {
		t.Fatal("embedded devin_models.json has no Devin models")
	}
	var placeholder []string
	for _, m := range catalog.Devin {
		if devinOutputLimitReally64000[m.ID] {
			continue
		}
		if m.MaxCompletionTokens == devinOutputLimitPlaceholder || m.MaxCompletionTokens <= 0 {
			placeholder = append(placeholder, m.ID)
		}
	}
	sort.Strings(placeholder)
	if len(placeholder) > 0 {
		t.Fatalf("%d Devin model(s) carry upstream's placeholder output limit %d (or none), not Devin's own: %s; "+
			"regenerate internal/registry/models/devin_models.json with go run ./cmd/fetch_devin_models",
			len(placeholder), devinOutputLimitPlaceholder, strings.Join(placeholder, ", "))
	}
}
