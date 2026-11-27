package cliproxy

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the entry of an answer (.github/provimux/design/answered-by.md 4.3, steps 3 to 5; prices-and-cache.md
// 6.1). The conductor has the attempt's sent request (step 2, cliproxyexecutor.ProvimuxSent); this gives its entry for
// the credential that answered, or none. It is the one function that gives an entry: nothing predicts one.

func init() {
	coreauth.SetProvimuxAnswerEntry(provimuxBuiltinKind, provimuxOwnExecutor, provimuxVouches, provimuxEntryOfAnswer)
}

// provimuxVouches reports whether the gateway's own loader made auth (for its id) and no plugin made or changed it
// (rule A.3 of the lead's amendment of 2026-11-27 (2)).
func provimuxVouches(auth *coreauth.Auth) bool {
	return auth.ProvimuxLoaded() && !pluginhost.ProvimuxPluginCredential(auth)
}

// provimuxEntryOfAnswer returns the entry of an answer of auth whose attempt sent sent, or "": "" for a credential the
// gateway's own loader did not make (rule A.3 of the lead's amendment of 2026-11-27 (2)), for one a plugin made or
// changed, for a URL that is not https on its default port, where the map has no row, and for an xAI credential
// anything marks as not the paid API. The executor (A.1) and the request (A.2, A.4 to A.6) were settled before.
func provimuxEntryOfAnswer(auth *coreauth.Auth, sent cliproxyexecutor.ProvimuxSentRequest) string {
	if auth == nil || sent.Model == "" {
		return ""
	}
	// Step 3: the gateway's own loader made the credential (a login parsed from the auth folder or a store record, a
	// key of the configuration: a mark set where it is parsed, never an attribute), and no plugin made or changed it.
	if !auth.ProvimuxLoaded() || pluginhost.ProvimuxPluginCredential(auth) {
		return ""
	}
	// Step 4: the map has a row for what was sent.
	if sent.Scheme != "https" || sent.Port != "" {
		return ""
	}
	provider := modellists.EntryProvider(auth.Provider, auth.AuthKind(), sent.Host, sent.Signer)
	if provider == "xai" && !provimuxPaidXAI(auth) {
		return ""
	}
	// Step 5: that row's provider and the model that was sent.
	return modellists.EntryRef(provider, modellists.EntryModel(provider, sent.Model))
}

// provimuxPaidXAI reports whether nothing on an xAI credential marks it as not the paid API: no using_api other than
// true in its attributes or its metadata (answered-by.md, decision 15). api.x.ai also takes requests from a credential
// that is not the paid API, and the request does not show which it is.
func provimuxPaidXAI(auth *coreauth.Auth) bool {
	if value, ok := auth.Attributes["using_api"]; ok && !strings.EqualFold(strings.TrimSpace(value), "true") {
		return false
	}
	if value, ok := auth.Metadata["using_api"]; ok {
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			return strings.EqualFold(strings.TrimSpace(typed), "true")
		default:
			return false
		}
	}
	return true
}
