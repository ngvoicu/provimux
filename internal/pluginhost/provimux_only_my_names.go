package pluginhost

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// provimuxRouteHeld reports whether a model router's decision to run the request on a plugin executor
// must be dropped because models.only-my-names holds the requested name back (only-my-names.md, a
// plugin's models are held back unless named). The request then takes the registry's path and is
// refused there with the setting's message. A router that rewrites the request to a provider or to
// another model is not covered: the rewritten target decides.
func provimuxRouteHeld(req pluginapi.ModelRouteRequest) bool {
	name := strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(req.RequestedModel)).ModelName)
	return registry.ProvimuxRouteHeld(name)
}
