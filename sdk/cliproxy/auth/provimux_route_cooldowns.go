package auth

import (
	"strings"
	"time"
)

// ProvimuxRouteCooldowns returns, for each of routeModels (model IDs as clients send them), the
// unexpired cooldown that blocks it on auth, if any. A credential-wide cooldown applies to every
// route model; a model cooldown applies to the route model whose selection key (prefix cut, OAuth
// alias resolved) is the cooldown's key. When both apply, the later RetryAt wins.
//
// It reads only: no state of the manager or of auth changes. In particular it does not use
// ResolveExecutionModel, which advances the round-robin offset of a pooled OpenAI-compatible alias.
func (m *Manager) ProvimuxRouteCooldowns(auth *Auth, routeModels []string, now time.Time) map[string]CooldownView {
	out := make(map[string]CooldownView)
	if auth == nil || len(routeModels) == 0 {
		return out
	}
	views := CooldownSnapshotForAuth(auth, now)
	if len(views) == 0 {
		return out
	}
	for _, routeModel := range routeModels {
		routeModel = strings.TrimSpace(routeModel)
		if routeModel == "" {
			continue
		}
		key := provimuxRouteSelectionLookupKey(m, auth, routeModel)
		var best CooldownView
		found := false
		for _, view := range views {
			switch view.Scope {
			case "credential":
			case "model":
				if key == "" || canonicalModelLookupKey(view.ModelKey) != key {
					continue
				}
			default:
				continue
			}
			if !found || view.RetryAt.After(best.RetryAt) {
				best = view
				found = true
			}
		}
		if found {
			out[routeModel] = best
		}
	}
	return out
}

// provimuxRouteSelectionLookupKey is the lower-case key the scheduler keeps routeModel's state under.
func provimuxRouteSelectionLookupKey(m *Manager, auth *Auth, routeModel string) string {
	if m == nil {
		return canonicalModelLookupKey(rewriteModelForAuth(routeModel, auth))
	}
	return canonicalModelLookupKey(m.selectionModelKeyForAuth(auth, routeModel))
}
