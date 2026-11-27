package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// provimuxFollowSessionRoute puts cfg in force for session-route on m (session-route.md), where the service commits a
// config and from the first config of a start: the book of part one (with session affinity off or in Home mode it
// observes nothing and keeps nothing, and it starts again when a later config turns affinity on), and the rule's
// setting (design section 15.6), as one value the selector loads at each request.
func provimuxFollowSessionRoute(m *coreauth.Manager, cfg *config.Config) {
	sessionroute.Follow(m, cfg)
	coreauth.ProvimuxSetSessionReturn(m, provimuxSessionReturnSettingOf(cfg))
}

// provimuxSessionReturnSettingOf is the rule's setting in cfg, with upstream's routing settings as they apply (the
// same normalisation that makes the selector).
func provimuxSessionReturnSettingOf(cfg *config.Config) coreauth.ProvimuxSessionReturnSetting {
	if cfg == nil {
		return coreauth.ProvimuxSessionReturnSetting{}
	}
	routing := normalizedRoutingRuntimeState(cfg)
	coldAfter, set := cfg.ProvimuxSessionReturnColdAfter()
	return coreauth.ProvimuxSessionReturnSetting{
		ColdAfter:    coldAfter,
		ColdAfterSet: set,
		Affinity:     routing.sessionAffinity,
		Home:         cfg.Home.Enabled,
		TTL:          routing.sessionAffinityTTL,
		Strategy:     routing.strategy,
		Subagents:    routing.sessionAffinitySubagents,
		RebindOn:     routing.sessionAffinityRebindOn,
	}
}
