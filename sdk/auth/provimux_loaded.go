package auth

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// provimuxMarkLoaded sets the loader's mark on credentials this package's parser has just made from a record of the
// gateway's own (.github/provimux/design/answered-by.md, the lead's amendment of 2026-11-27 (2), rule A.3): only they
// can name an entry on their answers.
func provimuxMarkLoaded(credentials ...*cliproxyauth.Auth) {
	for _, credential := range credentials {
		origin.MarkLoaded(credential)
	}
}
