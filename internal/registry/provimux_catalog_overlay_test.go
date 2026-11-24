package registry

import (
	"testing"
)

// The overlay changes what the catalog's readers see, never the catalog upstream's updater stores: a
// republished catalog keeps its own values and reports no change caused by the overlay.
func TestProvimuxCatalogOverlayNeverReachesTheStoredCatalog(t *testing.T) {
	original := provimuxRawModels()
	t.Cleanup(func() {
		SetProvimuxCatalogOverlay(nil)
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
	if len(original.Claude) == 0 {
		t.Fatal("no claude section")
	}
	target := original.Claude[0]
	SetProvimuxCatalogOverlay(func(section string, raw *ModelInfo) *ModelInfo {
		if section != "claude" || raw.ID != target.ID {
			return nil
		}
		out := cloneModelInfo(raw)
		out.MaxCompletionTokens = raw.MaxCompletionTokens + 1
		return out
	})
	if got := findStaticModel(GetClaudeModels(), target.ID); got == nil || got.MaxCompletionTokens != target.MaxCompletionTokens+1 {
		t.Fatalf("readers see the overlay: %+v", got)
	}
	if got := LookupStaticModelInfoByChannel(target.ID, "claude"); got == nil || got.MaxCompletionTokens != target.MaxCompletionTokens+1 {
		t.Fatalf("lookups see the overlay: %+v", got)
	}
	if raw := ProvimuxRawCatalogModel("claude", target.ID); raw == nil || raw.MaxCompletionTokens != target.MaxCompletionTokens {
		t.Fatalf("raw = %+v", raw)
	}
	changed, err := publishCatalogBytes(embeddedModelsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if stored := findStaticModel(provimuxRawModels().Claude, target.ID); stored == nil || stored.MaxCompletionTokens != target.MaxCompletionTokens {
		t.Fatalf("the overlay reached the stored catalog: %+v", stored)
	}
	for _, provider := range changed {
		if provider == "claude" {
			t.Fatalf("the overlay was reported as a catalog change: %v", changed)
		}
	}
	SetProvimuxCatalogOverlay(nil)
	if got := findStaticModel(GetClaudeModels(), target.ID); got.MaxCompletionTokens != target.MaxCompletionTokens {
		t.Fatalf("without the overlay: %+v", got)
	}
}
