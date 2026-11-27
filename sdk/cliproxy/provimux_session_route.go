package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// provimuxFollowSessionRoute puts cfg in force for session-route's book of m (session-route.md, G1), where the service
// commits a config and from the first config of a start: with session affinity off or in Home mode the book observes
// nothing and keeps nothing, and it starts again when a later config turns affinity on.
func provimuxFollowSessionRoute(m *coreauth.Manager, cfg *config.Config) {
	sessionroute.Follow(m, cfg)
}
