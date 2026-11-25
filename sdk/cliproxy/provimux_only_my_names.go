package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: models.only-my-names (only-my-names.md). The service's two calls: the rule is set where a
// config is committed (and from the first config of a start, which is not committed), and an account's
// models pass the rule where registerResolvedModelsForAuthFenced hands them to the registry.

// provimuxNamesRuleOf is the rule of cfg: its alias rows, keep, the switch and Home mode.
func provimuxNamesRuleOf(cfg *config.Config) *registry.ProvimuxNamesRule {
	return config.ProvimuxNamesRule(cfg)
}

// provimuxSetNamesRule installs the rule of cfg for every registration that follows.
func provimuxSetNamesRule(cfg *config.Config) {
	registry.SetProvimuxNamesRule(provimuxNamesRuleOf(cfg))
}

// provimuxOnlyMyNames returns the models of a that are served, after aliases, namespaces and the
// account's prefix were applied, and records what a offered.
func provimuxOnlyMyNames(a *coreauth.Auth, models []*ModelInfo) []*ModelInfo {
	if a == nil {
		return models
	}
	return registry.ProvimuxOnlyMyNames(a.ID, a.Prefix, models)
}
