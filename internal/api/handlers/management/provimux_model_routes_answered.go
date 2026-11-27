package management

import (
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: `answered` on a route of GET /v8/management/provimux/model-routes (.github/provimux/design/answered-by.md
// 4.4; pui13/openapi.yaml, RouteAnswered): the same items as /v1/models?include=accounts, with the time, behind the
// management key: every client key's record together (answered-by.md 4.4 as amended on 2026-11-27).

// provimuxRouteAnswered is one item: the entry those answers named (null: none) and when the last of them was stated.
type provimuxRouteAnswered struct {
	Entry *string `json:"entry"`
	At    string  `json:"at"`
}

// provimuxAnsweredTimes is the route's `answered`: what the answers of the credential authID (whose marker is marker)
// for model said, to every client key together; empty when nothing is recorded, nil (left out) without a marker.
func provimuxAnsweredTimes(marker *string, authID, model string) *[]provimuxRouteAnswered {
	if marker == nil || *marker == "" {
		return nil
	}
	items := coreauth.ProvimuxAnsweredAll(authID, *marker, model)
	out := make([]provimuxRouteAnswered, 0, len(items))
	for _, item := range items {
		answered := provimuxRouteAnswered{At: item.At.UTC().Format("2006-01-02T15:04:05.000Z07:00")}
		if item.Entry != "" {
			entry := item.Entry
			answered.Entry = &entry
		}
		out = append(out, answered)
	}
	return &out
}

// provimuxAnsweredEntries is the set of entries the routes' `answered` items name: the selection of figures.entries
// on the management paths (prices-and-cache.md 6.3), from the same routes model-routes gives, so under the same
// "serve only my names" rule.
func provimuxAnsweredEntries(models []provimuxModel) map[string]bool {
	entries := map[string]bool{}
	for _, model := range models {
		for _, route := range model.Routes {
			if route.Answered == nil {
				continue
			}
			for _, item := range *route.Answered {
				if item.Entry != nil {
					entries[*item.Entry] = true
				}
			}
		}
	}
	return entries
}
