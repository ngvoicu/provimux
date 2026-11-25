package cliproxy

import (
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// --- the status source behind GET /provimux/model-lists ----------------------------------------

func TestProvimuxModelListSource(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "kimi")
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){"/coding/v1/models": provimuxBody(provimuxFixture(t, "kimi-models.json"))})
	executor := &liveListExecutor{provider: "kimi"}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "kimi-status-test", Provider: "kimi",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/coding"}, Metadata: map[string]any{"email": "office@providers.dev"}})
	svc.registerModelsForAuth(t.Context(), auth)
	source := provimuxLiveListSource{}
	before, ok := source.ModelListLogin(auth, true, false, true)
	if !ok || before.Source != "built-in" || !before.Supported || before.Held || before.Live != nil {
		t.Fatalf("before = %+v", before)
	}
	provimuxRefresh(svc, executor, false)
	state, _ := source.ModelListLogin(auth, true, true, true)
	if state.Source != "live" || !state.Held || state.Listed != 3 || state.Learned != 1 || len(state.Live) != 3 || len(state.Models) != 3 {
		t.Fatalf("state = %+v", state)
	}
	for _, model := range state.Models {
		switch model.ID {
		case "provimux-test-kimi-learned":
			if !model.Learned || model.Source != "live" || model.Sources["context"] != "live" || model.Sources["output"] != "" {
				t.Fatalf("neon = %+v", model)
			}
		case "kimi-k2.8":
			// a model the catalog has: every value, and so every source, is the catalog's
			for field, from := range model.Sources {
				if from != "built-in" {
					t.Fatalf("k2.8 %s from %s: %+v", field, from, model)
				}
			}
			if model.Learned || len(model.Sources) == 0 {
				t.Fatalf("k2.8 = %+v", model)
			}
		}
	}
	if off, _ := source.ModelListLogin(auth, false, false, false); off.Source == "live" {
		t.Fatal("with the option off the live list decides nothing")
	}
	key := &coreauth.Auth{ID: "claude-key-status", Provider: "claude", Attributes: map[string]string{"api_key": "sk"}}
	if _, row := source.ModelListLogin(key, true, false, false); row {
		t.Fatal("API keys other than Gemini keys get no row")
	}
	vertex := &coreauth.Auth{ID: "vertex-status", Provider: "vertex", Attributes: map[string]string{"auth_kind": "oauth"}}
	if state, row := source.ModelListLogin(vertex, true, false, false); !row || state.Supported || state.Source != "built-in" {
		t.Fatalf("vertex = %+v %v", state, row)
	}
	antigravity := &coreauth.Auth{ID: "antigravity-status", Provider: "antigravity", Attributes: map[string]string{"auth_kind": "oauth"}}
	if state, _ := source.ModelListLogin(antigravity, true, false, false); state.Source != "none" || !state.Supported {
		t.Fatalf("an Antigravity account that has not answered offers nothing: %+v", state)
	}
}
