package models

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// provimuxServedBeyondDevin reports whether Devin serves id together with other providers. Such an
// id (an oauth.model-alias fork shared with Claude logins, API keys or OpenRouter, or a devin/ id an
// alias also serves) is not Devin's alone, so its name gets no " (Devin)" suffix, whichever route
// registered it last (the published type and owner are that route's). Ids Devin does not serve keep
// the original's checks. Without a providers function the global registry answers.
func provimuxServedBeyondDevin(id string, providersForModel ProvidersForModelFunc) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if providersForModel == nil {
		providersForModel = registry.GetGlobalRegistry().GetModelProviders
	}
	devin, others := false, false
	for _, provider := range providersForModel(id) {
		switch provider = strings.TrimSpace(provider); {
		case strings.EqualFold(provider, "devin"):
			devin = true
		case provider != "":
			others = true
		}
	}
	return devin && others
}
