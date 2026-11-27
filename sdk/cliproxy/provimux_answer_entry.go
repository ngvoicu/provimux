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

// provimuxVouches reports whether no plugin made or changed auth: a plugin's credential never names an entry.
func provimuxVouches(auth *coreauth.Auth) bool {
	return !pluginhost.ProvimuxPluginCredential(auth)
}

// provimuxEntryOfAnswer returns the entry of an answer of auth whose attempt sent sent, or "": "" for a credential a
// plugin made or changed, for a URL that is not https on its default port, where the map has no row, and for an xAI
// credential anything marks as not the paid API. The executor and the request were settled before.
func provimuxEntryOfAnswer(auth *coreauth.Auth, sent cliproxyexecutor.ProvimuxSentRequest) string {
	if auth == nil || sent.Model == "" || !provimuxVouches(auth) {
		return ""
	}
	// The map has a row for what was sent.
	if sent.Scheme != "https" || sent.Port != "" {
		return ""
	}
	provider := modellists.EntryProvider(auth.Provider, auth.AuthKind(), sent.Host, sent.Signer)
	if provider == "xai" && !provimuxPaidXAI(auth) {
		return ""
	}
	// That row's provider and the model that was sent.
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
