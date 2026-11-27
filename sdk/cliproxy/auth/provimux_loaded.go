package auth

import "github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"

// Provimux: the loader's mark (.github/provimux/design/answered-by.md, the lead's amendment of 2026-11-27 (2), rule
// A.3). A credential names an entry on its answers only where the gateway's own loader made it: parsed from a login file
// in the auth folder (natively, not by a plugin's parser), from a record of one of the four token stores, or from a key
// of the configuration. The mark is an unexported field, set only through internal/provimux/origin.MarkLoaded, which no
// program outside this module can import; Clone keeps it, so it travels with the credential through the manager, a
// refresh and an update made from a clone. A credential made any other way (an SDK registration held in memory, a
// plugin's conversion) has no mark, whatever its file name, source or other attributes say.

func init() {
	origin.InstallLoadedMark(func(credential any) {
		if auth, ok := credential.(*Auth); ok && auth != nil {
			auth.provimuxLoaded = true
		}
	})
}

// ProvimuxLoaded reports whether the gateway's own loader made this credential. It can be read; nothing outside
// internal/provimux/origin can set it.
func (a *Auth) ProvimuxLoaded() bool { return a != nil && a.provimuxLoaded }
