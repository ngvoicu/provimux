package models

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCodexClientModelsResponseClearsUnknownLimits(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	for _, id := range []string{"unknown-limit-catalog", "unknown-limit-custom"} {
		id := id
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	known := &registry.ModelInfo{ID: "mixed-route-model", ContextLength: 128_000, MaxCompletionTokens: 32_000}
	unknown := &registry.ModelInfo{ID: "mixed-route-model"}
	reg.RegisterClient("unknown-limit-catalog", "claude", []*registry.ModelInfo{known})
	reg.RegisterClient("unknown-limit-custom", "openai-compatibility", []*registry.ModelInfo{unknown})

	resp := BuildResponse([]map[string]any{{"id": "mixed-route-model"}}, nil, false)
	models, _ := resp["models"].([]map[string]any)
	if len(models) != 1 {
		t.Fatalf("models = %v", models)
	}
	entry := models[0]
	if value, present := entry["context_window"]; !present || value != nil {
		t.Fatalf("context_window = %v (present %v), want null instead of the template window", value, present)
	}
	if value, present := entry["max_context_window"]; !present || value != nil {
		t.Fatalf("max_context_window = %v (present %v), want null", value, present)
	}
	if _, present := entry["max_tokens"]; present {
		t.Fatalf("max_tokens = %v, want none", entry["max_tokens"])
	}
}
