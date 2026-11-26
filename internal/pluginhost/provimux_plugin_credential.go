package pluginhost

import (
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ProvimuxPluginCredentialMark is the metadata key that marks a credential a plugin changed with the models it
// returned (ProvimuxMarkPluginCredential). It is stored with the credential, so it outlives the process: a login file
// a plugin rewrote is read at the next start by the native loader, whose credential has no plugin storage.
const ProvimuxPluginCredentialMark = "provimux_plugin_credential"

// ProvimuxPluginCredential reports whether a plugin made or changed auth (.github/provimux/design/answered-by.md 4.3,
// step 3). Every credential the host makes from a plugin's data (a plugin's own login, an update a plugin returns
// with its models) carries the plugin host's storage (pluginAuthDataToCoreAuth), and a credential's copies and
// refreshes keep it; an update a plugin returned also carries the mark, which the credential's file keeps after a
// restart. A plugin's virtual credential is one too. The gateway does not vouch for such a credential: a plugin can
// set its provider and kind to anything.
func ProvimuxPluginCredential(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if coreauth.IsPluginVirtualAuth(auth) {
		return true
	}
	if marked, _ := auth.Metadata[ProvimuxPluginCredentialMark].(bool); marked {
		return true
	}
	_, plugin := auth.Storage.(*pluginTokenStorage)
	return plugin
}

// ProvimuxMarkPluginCredential marks auth, an update a plugin returned for a credential, as a plugin's: the service
// calls it where it commits the update (service_executors.go), before the update is stored.
func ProvimuxMarkPluginCredential(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata[ProvimuxPluginCredentialMark] = true
}
