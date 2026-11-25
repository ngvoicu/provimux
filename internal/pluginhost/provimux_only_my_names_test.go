package pluginhost

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// R9 (only-my-names.md): a plugin executor's own models pass the rule by exact name, with no prefix.
func TestProvimuxOnlyMyNamesPluginExecutorModels(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	register := func(rule *registry.ProvimuxNamesRule, modelID string) *fakeModelClient {
		registry.SetProvimuxNamesRule(rule)
		modelRegistry := newFakeModelRegistry()
		host := newHostWithRecords(capabilityRecord{
			id: "alpha",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: staticModelRegistrar("plugin-provider", modelID),
				Executor:       &fakeExecutor{identifier: "plugin-provider"},
			}},
		})
		host.RegisterModels(context.Background(), modelRegistry)
		host.RegisterExecutors(newFakeExecutorManager(), modelRegistry)
		return modelRegistry.clients[pluginExecutorModelClientID("alpha", "plugin-provider")]
	}

	off := registry.NewProvimuxNamesRule(false, false)
	if client := register(off, "plugin-model"); client == nil || len(client.models) != 1 {
		t.Fatalf("off: %+v", client)
	}
	on := registry.NewProvimuxNamesRule(true, false)
	if client := register(on, "plugin-model"); client == nil || len(client.models) != 0 {
		t.Fatalf("on, not named: %+v", client)
	}
	snapshot := registry.ProvimuxNamesState(nil)
	if len(snapshot.Others) != 1 || snapshot.Others[0].Name != "plugin-model" {
		t.Fatalf("the plugin's model is not recorded: %+v", snapshot.Others)
	}
	named := registry.NewProvimuxNamesRule(true, false)
	named.AddKeep([]string{"plugin-model"})
	if client := register(named, "plugin-model"); client == nil || len(client.models) != 1 || client.models[0].ID != "plugin-model" {
		t.Fatalf("on, kept: %+v", client)
	}
}

// F (the reviewer's production host): with zero owner names, a model router that sends a plugin's own
// held-back model to its executor no longer runs it: the decision is dropped, the request takes the
// registry's path and is refused with the setting's message. Kept, the same route runs.
func TestProvimuxOnlyMyNamesRouterToAPluginExecutor(t *testing.T) {
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	const model = "names-plugin-model"
	for _, keep := range []bool{false, true} {
		rule := registry.NewProvimuxNamesRule(true, false)
		if keep {
			rule.AddKeep([]string{model})
		}
		registry.SetProvimuxNamesRule(rule)
		calls := 0
		exec := &fakeExecutor{identifier: "names-provider", execute: func(_ context.Context, r pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			calls++
			return pluginapi.ExecutorResponse{Payload: []byte(`{"served":true}`)}, nil
		}}
		host := newRouteModelHostWithRecords(capabilityRecord{id: "names-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("names-provider", model),
			Executor:       exec,
			ModelRouter: modelRouterFunc(func(_ context.Context, r pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
				return pluginapi.ModelRouteResponse{Handled: strings.HasPrefix(r.RequestedModel, model), TargetKind: pluginapi.ModelRouteTargetSelf}, nil
			}),
		}}})
		reg := registry.GetGlobalRegistry()
		manager := coreauth.NewManager(nil, nil, nil)
		host.RegisterModels(t.Context(), reg)
		host.RegisterExecutors(manager, reg)
		key := pluginExecutorModelClientID("names-plugin", "names-provider")
		h := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
		h.SetModelRouterHost(host)
		for _, requested := range []string{model, model + "(high)"} {
			_, _, errMsg := h.ExecuteWithAuthManager(t.Context(), "openai", requested, []byte(`{"model":"`+requested+`"}`), "")
			if keep {
				if errMsg != nil || calls == 0 {
					t.Fatalf("kept %s: %v, calls %d", requested, errMsg, calls)
				}
				continue
			}
			if errMsg == nil || calls != 0 || !strings.Contains(errMsg.Error.Error(), "is not served") {
				t.Fatalf("held %s: err %v, plugin calls %d", requested, errMsg, calls)
			}
		}
		reg.UnregisterClient(key)
	}
}
