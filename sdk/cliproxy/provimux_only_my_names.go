package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Provimux: models.only-my-names (only-my-names.md section 16). The service puts the rule of a config in force
// where the config is committed, and from the first config of a start, which is not committed. Registration
// never consults the rule: the rule is applied where names are read.

// provimuxNamesRuleOf is the rule of cfg: its alias rows, keep, the switch and Home mode.
func provimuxNamesRuleOf(cfg *config.Config) *onlymynames.Rule {
	return config.ProvimuxNamesRule(cfg)
}

// provimuxSetNamesRule puts the rule of cfg in force for every list and request that follows.
func provimuxSetNamesRule(cfg *config.Config) {
	registry.SetProvimuxNamesRule(provimuxNamesRuleOf(cfg))
}
