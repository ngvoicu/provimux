package handlers

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// provimuxNotServedBody is upstream's unknown-model body, with the message of models.only-my-names when
// an account offers baseModel but the setting holds it back (only-my-names.md section 5): same status,
// type, code and param; the refusal is counted. Otherwise the body is returned unchanged.
func provimuxNotServedBody(body, baseModel string) string {
	message := registry.ProvimuxNotServedMessage(baseModel)
	if message == "" {
		return body
	}
	if updated, errSet := ojson.Set(body, "error.message", message); errSet == nil {
		return updated
	}
	return body
}
