package pluginhost

import (
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ProvimuxPluginCredential reports whether a plugin made or changed auth (.github/provimux/design/answered-by.md 4.3,
// step 3). Every credential the host makes from a plugin's data (a plugin's own login, an update a plugin returns
// with its models) carries the plugin host's storage (pluginAuthDataToCoreAuth), and a credential's copies and
// refreshes keep it; an update a plugin returned also carries coreauth.ProvimuxPluginCredentialMark, which the
// credential's file keeps after a restart. A plugin's virtual credential is one too. The rule is the account marker's
// (coreauth.ProvimuxPluginMade), so the two agree. The gateway does not vouch for such a credential: a plugin can set
// its provider and kind to anything.
func ProvimuxPluginCredential(auth *coreauth.Auth) bool {
	return auth != nil && (coreauth.IsPluginVirtualAuth(auth) || coreauth.ProvimuxPluginMade(auth))
}

// ProvimuxMarkPluginCredential marks auth, an update a plugin returned for a credential, as a plugin's: the service
// calls it where it commits the update (service_executors.go), before the update is stored. It writes the one
// metadata key coreauth.ProvimuxPluginCredentialMark and nothing else.
func ProvimuxMarkPluginCredential(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[coreauth.ProvimuxPluginCredentialMark] = true
}
