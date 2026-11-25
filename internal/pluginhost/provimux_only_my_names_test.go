package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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
