package pluginhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// E of only-my-names.md section 17 (the third review's finding 2): a plugin executor registers a public id and
// a different provider-native Name, and only the public id is kept. The Gemini format names models by Name, so
// it leaves this one out: its list does not show the native name and its detail answers 404 for it (and, as
// that format has no other name for the model, for the kept id); the OpenAI list shows the kept id, and the
// door refuses the native name.
func TestProvimuxOnlyMyNamesGeminiNativeName(t *testing.T) {
	const id = "local-public-name"
	const native = "vendor-native-name"
	const provider = "names-native-provider"
	const plugin = "names-native-plugin"
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	rule := onlymynames.NewRule(true, false)
	rule.AddKeep([]string{id})
	registry.SetProvimuxNamesRule(rule)
	host := newRouteModelHostWithRecords(capabilityRecord{id: plugin, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		ModelRegistrar: modelRegistrarFunc(func(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
			return pluginapi.ModelRegistrationResponse{Provider: provider, Models: []pluginapi.ModelInfo{{ID: id, Name: "models/" + native}}}, nil
		}),
		Executor: &fakeExecutor{identifier: provider},
	}}})
	reg := registry.GetGlobalRegistry()
	manager := coreauth.NewManager(nil, nil, nil)
	host.RegisterModels(t.Context(), reg)
	host.RegisterExecutors(manager, reg)
	t.Cleanup(func() { reg.UnregisterClient(pluginExecutorModelClientID(plugin, provider)) })
	if reg.GetModelCount(id) != 1 {
		t.Fatal("premise: the plugin registers its public id")
	}
	api := gemini.NewGeminiAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v1beta/models", api.GeminiModels)
	router.GET("/v1beta/models/*action", api.GeminiGetHandler)
	get := func(path string) (int, string) {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder.Code, recorder.Body.String()
	}
	_, list := get("/v1beta/models")
	nativeCode, _ := get("/v1beta/models/" + native)
	keptCode, _ := get("/v1beta/models/" + id)
	if strings.Contains(list, native) || nativeCode != http.StatusNotFound || keptCode != http.StatusNotFound || !onlymynames.Current().Refuses(native) {
		t.Fatalf("Gemini list %s, native detail %d, kept detail %d", list, nativeCode, keptCode)
	}
	listed := false
	for _, model := range reg.GetAvailableModels("openai") {
		listed = listed || model["id"] == id
	}
	if !listed {
		t.Fatal("the OpenAI list does not show the kept id")
	}
}
