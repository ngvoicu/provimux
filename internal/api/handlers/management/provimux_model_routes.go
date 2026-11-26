package management

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/model-routes. Per model, every enabled login or key
// that registered it, in priority order, named the way GET /credentials names them, with the
// cooldown the auth manager has recorded. It reports recorded state only; it does not predict which
// route the router would pick. Read-only, in memory. Contract: .github/provimux/design/pui13/openapi.yaml. The response only
// grows: fields are added, never removed or redefined.

// provimuxNow is the clock of the Provimux status endpoints; tests replace it.
var provimuxNow = time.Now

type provimuxModelRoutesResponse struct {
	ObservedAt string          `json:"observed_at"`
	Models     []provimuxModel `json:"models"`
}

type provimuxModel struct {
	ID          string          `json:"id"`
	DisplayName *string         `json:"display_name"`
	OwnedBy     *string         `json:"owned_by"`
	Limits      provimuxLimits  `json:"limits"`
	Routes      []provimuxRoute `json:"routes"`
}

type provimuxLimits struct {
	Context *int `json:"context"`
	Output  *int `json:"output"`
}

type provimuxLogin struct {
	Name  *string `json:"name"`
	Email *string `json:"email"`
	Label *string `json:"label"`
}

type provimuxReasoning struct {
	Levels []string                 `json:"levels"`
	Budget *provimuxReasoningBudget `json:"budget"`
}

type provimuxReasoningBudget struct {
	Min            int  `json:"min"`
	Max            int  `json:"max"`
	ZeroAllowed    bool `json:"zero_allowed"`
	DynamicAllowed bool `json:"dynamic_allowed"`
}

// provimuxRecordedCooldown is an unexpired cooldown as the auth manager recorded it on the
// credential: for the whole credential or for one model, until when, and why (a code).
type provimuxRecordedCooldown struct {
	Scope        string  `json:"scope"`
	Model        *string `json:"model"`
	Reason       string  `json:"reason"`
	Until        string  `json:"until"`
	HTTPStatus   *int    `json:"http_status"`
	BackoffLevel *int    `json:"backoff_level"`
}

type provimuxRoute struct {
	AuthIndex        *string                   `json:"auth_index"`
	Provider         string                    `json:"provider"`
	Kind             *string                   `json:"kind"`
	Priority         int                       `json:"priority"`
	Host             *string                   `json:"host"`
	Account          *string                   `json:"account,omitempty"` // the marker (answered-by.md 2); none with models.answered-by off
	Login            provimuxLogin             `json:"login"`
	Limits           provimuxLimits            `json:"limits"`
	Reasoning        *provimuxReasoning        `json:"reasoning"`
	RecordedCooldown *provimuxRecordedCooldown `json:"recorded_cooldown"`
	Answered         *[]provimuxRouteAnswered  `json:"answered,omitempty"` // what its answers said (answered-by.md 4.4); with account
}

// ProvimuxModelRoutes handles GET /v8/management/provimux/model-routes.
func (h *Handler) ProvimuxModelRoutes(c *gin.Context) {
	now := provimuxNow().UTC()
	response := provimuxModelRoutesResponse{
		ObservedAt: provimuxTime(now),
		Models:     []provimuxModel{},
	}
	var only map[string]struct{}
	if values, ok := c.Request.URL.Query()["model"]; ok {
		only = make(map[string]struct{}, len(values))
		for _, value := range values {
			if value != "" {
				only[value] = struct{}{}
			}
		}
		if len(only) == 0 {
			// The console's presence probe: no state is read.
			c.JSON(http.StatusOK, response)
			return
		}
	}
	if manager := h.provimuxAuthManager(); manager != nil {
		response.Models = provimuxBuildModelRoutes(manager, only, now)
	}
	c.JSON(http.StatusOK, response)
}

// provimuxAuthManager reads the auth manager under h.mu, as SetAuthManager writes it.
func (h *Handler) provimuxAuthManager() *coreauth.Manager {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authManager
}

func provimuxBuildModelRoutes(manager *coreauth.Manager, only map[string]struct{}, now time.Time) []provimuxModel {
	modelRegistry := registry.GetGlobalRegistry()
	routesByModel := make(map[string][]provimuxRoute)
	// models.only-my-names (only-my-names.md 16.4): while the rule applies, only the names it serves. The rule
	// is loaded once, and the whole answer is decided under it; with it not applying this is one atomic load.
	rule := onlymynames.Current()
	var served map[string]bool
	if rule.Applies() {
		served = make(map[string]bool)
	}
	for _, auth := range manager.List() {
		if auth == nil || auth.Disabled {
			continue
		}
		models := modelRegistry.GetModelsForClient(auth.ID)
		routeModels := make([]string, 0, len(models))
		for _, model := range models {
			if model == nil || model.ID == "" {
				continue
			}
			if only != nil {
				if _, wanted := only[model.ID]; !wanted {
					continue
				}
			}
			if provimuxGated(rule, served, model.ID) {
				continue
			}
			routeModels = append(routeModels, model.ID)
		}
		if len(routeModels) == 0 {
			continue
		}
		cooldowns := manager.ProvimuxRouteCooldowns(auth, routeModels, now)
		base := provimuxRoute{
			AuthIndex: provimuxString(lockedAuthIndex(auth)),
			Provider:  auth.Provider,
			Kind:      provimuxString(auth.AuthKind()),
			Priority:  auth.Priority(),
			Host:      provimuxString(provimuxBaseURLHost(auth)),
			Account:   provimuxString(auth.ProvimuxAccountMarker()),
			Login:     provimuxLoginOf(auth),
		}
		for _, model := range models {
			if model == nil || model.ID == "" {
				continue
			}
			if only != nil {
				if _, wanted := only[model.ID]; !wanted {
					continue
				}
			}
			if provimuxGated(rule, served, model.ID) {
				continue
			}
			route := base
			route.Limits = provimuxLimitsOf(model, false)
			route.Reasoning = provimuxReasoningOf(model.Thinking)
			if view, ok := cooldowns[model.ID]; ok {
				route.RecordedCooldown = provimuxRecordedCooldownOf(view)
			}
			route.Answered = provimuxAnsweredTimes(route.Account, model.ID) // Provimux: answered-by.md 4.4
			routesByModel[model.ID] = append(routesByModel[model.ID], route)
		}
	}

	ids := make([]string, 0, len(routesByModel))
	for id := range routesByModel {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := strings.ToLower(ids[i]), strings.ToLower(ids[j])
		if left != right {
			return left < right
		}
		return ids[i] < ids[j]
	})
	out := make([]provimuxModel, 0, len(ids))
	for _, id := range ids {
		routes := routesByModel[id]
		sort.SliceStable(routes, func(i, j int) bool { return provimuxRouteLess(routes[i], routes[j]) })
		entry := provimuxModel{ID: id, Routes: routes}
		if info := modelRegistry.GetModelInfo(id, ""); info != nil {
			entry.DisplayName = provimuxString(info.DisplayName)
			entry.OwnedBy = provimuxString(info.OwnedBy)
			entry.Limits = provimuxLimitsOf(info, true)
		}
		out = append(out, entry)
	}
	return out
}

// provimuxRouteLess orders routes as /v1/models?include=accounts does (priority descending, then
// provider, then kind), then by login label and auth_index so two calls agree.
func provimuxRouteLess(left, right provimuxRoute) bool {
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if left.Provider != right.Provider {
		return left.Provider < right.Provider
	}
	if leftKind, rightKind := provimuxValue(left.Kind), provimuxValue(right.Kind); leftKind != rightKind {
		return leftKind < rightKind
	}
	if leftLabel, rightLabel := provimuxValue(left.Login.Label), provimuxValue(right.Login.Label); leftLabel != rightLabel {
		return leftLabel < rightLabel
	}
	return provimuxValue(left.AuthIndex) < provimuxValue(right.AuthIndex)
}

// provimuxLoginOf names a credential only where GET /credentials shows it under a file name: a
// listed, file-backed login gets its file name, email and label. A configured API key gets its
// label, which is the config's (the entry's name or "<provider>-apikey"). Everything else, runtime
// credentials (whose ID, label and email can be a channel ID) and logins the credentials list hides,
// gets no name: auth_index identifies it. Never AccountInfo, which is the key of an API key.
func provimuxLoginOf(auth *coreauth.Auth) provimuxLogin {
	switch {
	case auth == nil || isRuntimeOnlyAuth(auth):
		return provimuxLogin{}
	case auth.AuthSourceKind() == coreauth.AuthSourceConfig:
		return provimuxLogin{Label: provimuxString(auth.Label)}
	case strings.TrimSpace(authAttribute(auth, "path")) == "" || !isAuthFileListable(auth):
		return provimuxLogin{}
	}
	return provimuxLogin{
		Name:  provimuxString(strings.TrimSpace(auth.FileName)),
		Email: provimuxString(authEmail(auth)),
		Label: provimuxString(auth.Label),
	}
}

// provimuxLimitsOf reduces the fields providers declare limits in, as the registry does
// (model_registry_limits.go): the smallest positive window and output. published honours the
// registry's "routes disagree" marks.
func provimuxLimitsOf(info *registry.ModelInfo, published bool) provimuxLimits {
	if info == nil {
		return provimuxLimits{}
	}
	window := provimuxMinPositive(info.ContextLength, info.MaxContextLength, info.InputTokenLimit)
	output := provimuxMinPositive(info.MaxCompletionTokens, info.OutputTokenLimit)
	if published && info.ContextLengthUnknown {
		window = 0
	}
	if published && info.MaxCompletionTokensUnknown {
		output = 0
	}
	return provimuxLimits{Context: provimuxPositive(window), Output: provimuxPositive(output)}
}

func provimuxReasoningOf(support *registry.ThinkingSupport) *provimuxReasoning {
	if support == nil {
		return nil
	}
	reasoning := &provimuxReasoning{Levels: append([]string{}, support.Levels...)}
	if support.Min != 0 || support.Max != 0 || support.ZeroAllowed || support.DynamicAllowed {
		reasoning.Budget = &provimuxReasoningBudget{
			Min: support.Min, Max: support.Max, ZeroAllowed: support.ZeroAllowed, DynamicAllowed: support.DynamicAllowed,
		}
	}
	return reasoning
}

// provimuxRecordedCooldownOf carries a recorded cooldown's codes and times; never a provider's text.
func provimuxRecordedCooldownOf(view coreauth.CooldownView) *provimuxRecordedCooldown {
	cooldown := &provimuxRecordedCooldown{
		Scope:  view.Scope,
		Model:  provimuxString(view.ModelKey),
		Reason: view.Reason,
		Until:  provimuxTime(view.RetryAt),
	}
	if view.HTTPStatus > 0 {
		status := view.HTTPStatus
		cooldown.HTTPStatus = &status
	}
	if view.BackoffLevel != nil {
		level := *view.BackoffLevel
		cooldown.BackoffLevel = &level
	}
	return cooldown
}

// provimuxBaseURLHost returns the lower-cased host of an API key's base URL, or "" when there is
// none. A copy of baseURLHost in sdk/api/handlers/openai/openai_models_accounts.go, which this
// package cannot import; test/provimux compares both endpoints.
func provimuxBaseURLHost(auth *coreauth.Auth) string {
	if auth.Attributes == nil || auth.AuthKind() != "apikey" {
		return ""
	}
	parsed, errParse := url.Parse(strings.TrimSpace(auth.Attributes["base_url"]))
	if errParse != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func provimuxMinPositive(values ...int) int {
	least := 0
	for _, value := range values {
		if value > 0 && (least == 0 || value < least) {
			least = value
		}
	}
	return least
}

func provimuxPositive(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

// provimuxString returns nil for an empty (or blank) string.
func provimuxString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func provimuxValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// provimuxTime formats t as RFC 3339 in UTC, with the fraction of a second it has: a cooldown with
// less than a second left, or a fetch that ends within the second of a click, must not read as over
// or as earlier.
func provimuxTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// provimuxGated reports whether rule leaves id out; served (nil while the rule does not apply) remembers each
// id's answer for one response.
func provimuxGated(rule *onlymynames.Rule, served map[string]bool, id string) bool {
	if served == nil {
		return false
	}
	ok, known := served[id]
	if !known {
		ok = rule.Serves(id)
		served[id] = ok
	}
	return !ok
}
