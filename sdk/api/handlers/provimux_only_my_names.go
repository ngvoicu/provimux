package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// Provimux: models.only-my-names, the door (G2) and the routers (G3) of only-my-names.md 16.3. With the rule
// not applying, each costs one atomic load and allocates nothing.

// provimuxDoor refuses a request whose name, or whose router target, is not the owner's. With the rule
// applying it replaces "auto" in *modelName by the first available of his names. It is the first statement
// of providersForExecution, which every handler execution passes before an account is chosen: the registry
// path, a forced provider and a router's provider decision alike. A refusal is upstream's unknown-model
// answer (400, invalid_request_error, model_not_found, param model), with the setting's message when an
// account registers the name, counted under the name without its thinking suffix.
func (h *BaseAPIHandler) provimuxDoor(modelName *string, originalRequestedModel string, decision modelRouteDecision) *interfaces.ErrorMessage {
	if !registry.ProvimuxApplies() {
		return nil
	}
	requested := strings.TrimSpace(*modelName)
	if requested == "" {
		requested = strings.TrimSpace(originalRequestedModel)
	}
	if requested == "" {
		return nil // names no model
	}
	if parsed := thinking.ParseSuffix(requested); strings.TrimSpace(parsed.ModelName) == "auto" {
		// auto is not a name he set: it becomes the first available of his names (the gated list). When
		// none is available it stays "auto" and is refused below as upstream refuses an auto it cannot
		// resolve.
		if first, errFirst := registry.GetGlobalRegistry().GetFirstAvailableModel(""); errFirst == nil && first != "" {
			resolved := first
			if parsed.HasSuffix {
				resolved = fmt.Sprintf("%s(%s)", first, parsed.RawSuffix)
			}
			*modelName = resolved
			requested = resolved
		}
	}
	if errMsg := provimuxDoorCheck(requested); errMsg != nil {
		return errMsg
	}
	if target := strings.TrimSpace(decision.Model); target != "" {
		return provimuxDoorCheck(target)
	}
	return nil
}

// provimuxDoorCheck refuses name unless it is the owner's: the name without its thinking suffix, or, when that
// is not his, the name as sent (an exact route registered with its suffix).
func provimuxDoorCheck(name string) *interfaces.ErrorMessage {
	base := strings.TrimSpace(thinking.ParseSuffix(name).ModelName)
	if base == "" {
		base = name
	}
	refused, offered := registry.ProvimuxRefuseSent(base, name)
	if !refused {
		return nil
	}
	message := "unknown provider for model " + name
	if offered {
		message = registry.ProvimuxNotServedMessage(base)
	}
	body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
	body, errSet := ojson.Set(body, "error.message", message)
	if errSet != nil {
		body = `{"error":{"message":"unknown provider for model","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
	}
	return &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(body)}
}

// provimuxNoRouter reports whether no plugin router may be asked about modelName: the rule applies and the
// name (without its thinking suffix, or as sent) is not the owner's. "auto" is not his, so no router is
// asked about it while the rule applies. The request then goes on to the door, which refuses it.
func provimuxNoRouter(modelName string) bool {
	if !registry.ProvimuxApplies() {
		return false
	}
	name := strings.TrimSpace(modelName)
	if name == "" {
		return false
	}
	base := strings.TrimSpace(thinking.ParseSuffix(name).ModelName)
	if base == "" {
		base = name
	}
	if registry.ProvimuxServes(base) {
		return false
	}
	return base == name || !registry.ProvimuxServes(name)
}
