package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// Provimux: models.only-my-names, the door (G2) and the routers (G3) of only-my-names.md 16.3 and 18. With the
// rule not applying, each costs one atomic load and allocates nothing.

// provimuxRuleValue is the rule value a request is judged under, carried from the router check (G3) to the door
// (G2) in modelRouteDecision's one Provimux field, and with the decision in the context of a prepared stream
// route. Nil when the decision was not made by applyModelRouter: the door then loads the rule itself.
type provimuxRuleValue = *onlymynames.Rule

// provimuxDoorLoaded runs right after the door loaded its rule value, and provimuxDoorBetween between its
// judgement of the requested name and of a router's target. Tests commit another rule there, to show that every
// judgement of the request uses the value the door loaded first.
var (
	provimuxDoorLoaded  = func() {}
	provimuxDoorBetween = func() {}
)

// ProvimuxDoorHoldAfterLoad sets the hook that runs right after the door loaded its rule value, and returns a
// function that puts the previous one back. For tests (a service-level test holds a request there).
func ProvimuxDoorHoldAfterLoad(hold func()) func() {
	previous := provimuxDoorLoaded
	provimuxDoorLoaded = hold
	return func() { provimuxDoorLoaded = previous }
}

// provimuxDoor refuses a request whose name, or whose router target, is not served. It loads the rule in force
// once and judges every name of the request under that value (provimuxDoorJudge: the rule and the name, nothing
// else). With the rule applying it first replaces "auto" in *modelName by the first available of his names under
// the same value; that resolution reads what is available, the judgement never does. It is the first statement of
// providersForExecution, which every handler execution passes before an account is chosen: the registry path, a
// forced provider and a router's provider decision alike. A refusal is upstream's unknown-model answer (400,
// invalid_request_error, model_not_found, param model) with the setting's message, counted under the name
// without its thinking suffix.
func (h *BaseAPIHandler) provimuxDoor(modelName *string, originalRequestedModel string, decision modelRouteDecision) *interfaces.ErrorMessage {
	rule := decision.provimuxRule // the value the router check judged under
	if rule == nil {
		rule = onlymynames.Current() // no router check ran for this decision: load once
	}
	if !rule.Applies() {
		return nil
	}
	provimuxDoorLoaded()
	requested := strings.TrimSpace(*modelName)
	if requested == "" {
		requested = strings.TrimSpace(originalRequestedModel)
	}
	if requested == "" {
		return nil // names no model
	}
	if parsed := thinking.ParseSuffix(requested); strings.TrimSpace(parsed.ModelName) == onlymynames.Auto {
		// auto is never a name he set: it becomes the first available of his names under this rule. When none is
		// available it stays "auto" and is refused below.
		if first := registry.ProvimuxFirstServedUnder(rule); first != "" {
			resolved := first
			if parsed.HasSuffix {
				resolved = fmt.Sprintf("%s(%s)", first, parsed.RawSuffix)
			}
			*modelName = resolved
			requested = resolved
		}
	}
	if errMsg := provimuxDoorJudge(rule, requested); errMsg != nil {
		return errMsg
	}
	provimuxDoorBetween()
	if target := strings.TrimSpace(decision.Model); target != "" {
		return provimuxDoorJudge(rule, target)
	}
	return nil
}

// provimuxDoorJudge is the door's answer for one name under one rule value: nil when the rule serves it, else
// the refusal (counted). It reads nothing but the rule and the name; a source test keeps it that way.
func provimuxDoorJudge(rule *onlymynames.Rule, name string) *interfaces.ErrorMessage {
	if !rule.Refuses(name) {
		return nil
	}
	onlymynames.Count(rule, name)
	body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
	body, errSet := ojson.Set(body, "error.message", onlymynames.NotServedMessage(name))
	if errSet != nil {
		body = `{"error":{"message":"unknown provider for model","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
	}
	return &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(body)}
}

// provimuxNoRouter loads the rule in force, stores it on the decision for the door, and reports whether no
// plugin router may be asked about modelName: the rule applies and does not serve the name. "auto" is never
// served as a name, so no router is asked about it while the rule applies. The request then goes on to the
// door, which refuses it under the same value. Whatever applyModelRouter returns carries the value: every
// return of it passes after this call, and none builds a new decision.
func provimuxNoRouter(decision *modelRouteDecision, modelName string) bool {
	rule := onlymynames.Current()
	decision.provimuxRule = rule
	name := strings.TrimSpace(modelName)
	return name != "" && rule.Refuses(name)
}
