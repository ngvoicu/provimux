package openai

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Provimux: GET /v1/models?include=figures, contract 1 (.github/provimux/design/prices-and-cache/openapi.yaml;
// prices-and-cache.md 5; step F3). Each model gains `accounts` as with include=accounts, and the answer gains the
// `figures` object: the held figures of the entries the listed rows' `answered` items name. Without the value
// the list is what it was, byte for byte.

// includeFiguresParam is the /v1/models query value that adds the figures object (and the accounts).
const includeFiguresParam = "figures"

// wantsFigures reports whether the request asked for the figures (`?include=figures`), in the way wantsAccounts
// reads include: comma-separated, repeatable, case-insensitive.
func wantsFigures(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	for _, value := range c.QueryArray("include") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), includeFiguresParam) {
				return true
			}
		}
	}
	return false
}

// provimuxFiguresAccounts is the accounts the list writes: those include=accounts already gave, else, for
// include=figures alone, the same rows with `answered` filled for the caller, as include=accounts fills them. The
// figures are selected from those rows' `answered`, so they are the caller's own.
func (h *OpenAIAPIHandler) provimuxFiguresAccounts(c *gin.Context, figures bool, accounts map[string][]modelAccount) map[string][]modelAccount {
	if !figures || accounts != nil {
		return accounts
	}
	accounts = h.modelAccounts()
	provimuxFillAnswered(c, accounts)
	return accounts
}

// provimuxListFigures is the figures object for the list as it is written: the entries the `answered` items of the
// rows in data name, and only those. The rows are read from data, not from every account the gateway has, so a
// name the list does not show (one "serve only my names" does not serve) brings no entry, even where answers were
// recorded under it before the rule. An entry no answer named is never given, and nothing comes from a row's kind,
// provider or host.
func provimuxListFigures(data []map[string]any) modellists.FiguresView {
	listed := make(map[string][]modelAccount, len(data))
	for _, model := range data {
		id, _ := model["id"].(string)
		if rows, ok := model["accounts"].([]modelAccount); ok {
			listed[id] = append(listed[id], rows...)
		}
	}
	on := registry.ProvimuxPublishedListsInForce()
	var held *modellists.FiguresDocument
	if on {
		held = modellists.HeldFigures()
	}
	return modellists.BuildFiguresView(on, held, modellists.SelectEntries(answeredEntries(listed)))
}
