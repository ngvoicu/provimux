package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// provimuxFollowSessionRoute puts cfg in force for session-route on m (session-route.md), where the service commits a
// config and from the first config of a start: the book of part one (with session affinity off or in Home mode it
// observes nothing and keeps nothing, and it starts again when a later config turns affinity on), and the rule's
// cold-after (design section 15.4), which the selector loads at each request.
func provimuxFollowSessionRoute(m *coreauth.Manager, cfg *config.Config) {
	sessionroute.Follow(m, cfg)
	coldAfter, _ := cfg.ProvimuxSessionReturnColdAfter()
	coreauth.ProvimuxSetSessionReturn(m, coldAfter)
}
