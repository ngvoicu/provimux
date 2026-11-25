package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/names, the state of models.only-my-names
// (only-my-names.md section 6; contract: .github/provimux/design/pui13/openapi.yaml, NamesResponse).
// The same shape with the switch on or off: off, others is what would stop being served. It reports
// recorded state and predicts nothing. Read-only, in memory; no key, token or error text. The response
// only grows.

type provimuxNamesResponse struct {
	ObservedAt  string                    `json:"observed_at"`
	OnlyMyNames provimuxOnlyMyNamesState  `json:"only_my_names"`
	Applied     provimuxNamesApplied      `json:"applied"`
	Names       []provimuxOwnName         `json:"names"`
	KeepIgnored []provimuxKeepIgnored     `json:"keep_ignored"`
	Others      []provimuxOtherName       `json:"others"`
	Refused     *provimuxRefusedNamesBody `json:"refused"`
}

type provimuxOnlyMyNamesState struct {
	Enabled    bool    `json:"enabled"`
	Applies    bool    `json:"applies"`
	NotApplied *string `json:"not_applied"`
}

type provimuxOwnName struct {
	Name     string               `json:"name"`
	SetBy    []string             `json:"set_by"`
	Accounts int                  `json:"accounts"`
	Copies   []provimuxServedCopy `json:"copies"`
}

// provimuxServedCopy is a name served under an account's prefix ("team-a/opus" for "opus").
type provimuxServedCopy struct {
	ID       string `json:"id"`
	Accounts int    `json:"accounts"`
}

// provimuxNamesApplied tells the console whether the answer already reflects a write it made: the
// config's ETag (the config API's) and generation of the rule the registry matches, and applying while
// a newer rule or a registration is still on its way to the registry.
type provimuxNamesApplied struct {
	Generation uint64  `json:"generation"`
	ConfigETag *string `json:"config_etag"`
	Applying   bool    `json:"applying"`
}

func provimuxNamesAppliedOf(applied registry.ProvimuxApplied) provimuxNamesApplied {
	out := provimuxNamesApplied{Generation: applied.Generation, Applying: applied.Applying}
	if applied.Source != "" {
		out.ConfigETag = provimuxString(configV8ETag(applied.Source))
	}
	return out
}

type provimuxKeepIgnored struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

type provimuxOtherName struct {
	Name     string                `json:"name"`
	Accounts []provimuxNameAccount `json:"accounts"`
}

// provimuxNameAccount names an account as a route of GET /provimux/model-routes does.
type provimuxNameAccount struct {
	AuthIndex *string       `json:"auth_index"`
	Provider  string        `json:"provider"`
	Kind      *string       `json:"kind"`
	Priority  int           `json:"priority"`
	Host      *string       `json:"host"`
	Login     provimuxLogin `json:"login"`
}

type provimuxRefusedNamesBody struct {
	Since     string                `json:"since"`
	Names     []provimuxRefusedName `json:"names"`
	Truncated bool                  `json:"truncated"`
}

type provimuxRefusedName struct {
	Name     string `json:"name"`
	Requests int    `json:"requests"`
	LastAt   string `json:"last_at"`
}

// ProvimuxNames handles GET /v8/management/provimux/names.
func (h *Handler) ProvimuxNames(c *gin.Context) {
	c.JSON(http.StatusOK, h.provimuxNamesAnswer())
}

func (h *Handler) provimuxNamesAnswer() provimuxNamesResponse {
	var auths []*coreauth.Auth
	manager := h.provimuxAuthManager()
	if manager != nil {
		for _, auth := range manager.List() {
			if auth != nil && !auth.Disabled {
				auths = append(auths, auth)
			}
		}
	}
	byID := make(map[string]*coreauth.Auth, len(auths))
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		byID[auth.ID] = auth
		ids = append(ids, auth.ID)
	}
	snapshot := registry.ProvimuxNamesState(ids)

	// The rule as applied: what the registry serves now, not the config a write just saved.
	enabled, home := snapshot.Enabled, snapshot.Home
	if manager != nil && manager.HomeEnabled() {
		home = true
	}
	response := provimuxNamesResponse{
		ObservedAt:  provimuxTime(provimuxNow()),
		OnlyMyNames: provimuxOnlyMyNamesState{Enabled: enabled, Applies: !home},
		Applied:     provimuxNamesAppliedOf(snapshot.Applied),
		Names:       []provimuxOwnName{},
		KeepIgnored: []provimuxKeepIgnored{},
		Others:      []provimuxOtherName{},
	}
	if home {
		response.OnlyMyNames.NotApplied = provimuxString("home")
	}

	// names: the config's (alias rows, keep), then the aliases in the logins' own files.
	type ownName struct {
		name  string
		setBy []string
	}
	own := map[string]*ownName{}
	var order []string
	add := func(name, setBy string) {
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if key == "" {
			return
		}
		entry, ok := own[key]
		if !ok {
			entry = &ownName{name: name}
			own[key] = entry
			order = append(order, key)
		}
		for _, have := range entry.setBy {
			if have == setBy {
				return
			}
		}
		entry.setBy = append(entry.setBy, setBy)
	}
	for _, name := range snapshot.Names {
		for _, setBy := range name.SetBy {
			add(name.Name, setBy)
		}
	}
	for _, auth := range auths {
		for _, row := range coreauth.OAuthModelAliasesFromAttributes(auth.Attributes) {
			if alias := strings.TrimSpace(row.Alias); alias != "" && alias != strings.TrimSpace(row.Name) {
				add(alias, registry.ProvimuxNameSetByLoginFile)
			}
		}
	}
	// accounts: the enabled accounts (and plugin executors) that register the name now; copies: the
	// same under an account's prefix.
	registered := map[string]int{}
	copies := map[string]map[string]int{}
	modelRegistry := registry.GetGlobalRegistry()
	clients := append(append([]string(nil), ids...), snapshot.Plugins...)
	for _, id := range clients {
		prefix := ""
		if auth := byID[id]; auth != nil {
			prefix = strings.ToLower(strings.TrimSpace(auth.Prefix))
		}
		seen := map[string]struct{}{}
		for _, model := range modelRegistry.GetModelsForClient(id) {
			if model == nil {
				continue
			}
			modelID := strings.TrimSpace(model.ID)
			key := strings.ToLower(modelID)
			if _, dup := seen[key]; dup || key == "" {
				continue
			}
			seen[key] = struct{}{}
			if _, own := own[key]; own {
				registered[key]++
				continue
			}
			if prefix != "" && strings.HasPrefix(key, prefix+"/") {
				if base := key[len(prefix)+1:]; own[base] != nil {
					if copies[base] == nil {
						copies[base] = map[string]int{}
					}
					copies[base][modelID]++
				}
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return provimuxNameLess(own[order[i]].name, own[order[j]].name) })
	for _, key := range order {
		entry := own[key]
		setBy := entry.setBy
		sort.Slice(setBy, func(i, j int) bool { return provimuxSetByRank(setBy[i]) < provimuxSetByRank(setBy[j]) })
		name := provimuxOwnName{Name: entry.name, SetBy: setBy, Accounts: registered[key], Copies: []provimuxServedCopy{}}
		for id, accounts := range copies[key] {
			name.Copies = append(name.Copies, provimuxServedCopy{ID: id, Accounts: accounts})
		}
		sort.Slice(name.Copies, func(i, j int) bool { return provimuxNameLess(name.Copies[i].ID, name.Copies[j].ID) })
		response.Names = append(response.Names, name)
	}

	for _, ignored := range snapshot.KeepIgnored {
		response.KeepIgnored = append(response.KeepIgnored, provimuxKeepIgnored{Value: ignored.Value, Reason: ignored.Reason})
	}

	for _, other := range snapshot.Others {
		accounts := make([]provimuxNameAccount, 0, len(other.ClientIDs))
		for _, id := range other.ClientIDs {
			if auth := byID[id]; auth != nil {
				accounts = append(accounts, provimuxNameAccount{
					AuthIndex: provimuxString(lockedAuthIndex(auth)),
					Provider:  auth.Provider,
					Kind:      provimuxString(auth.AuthKind()),
					Priority:  auth.Priority(),
					Host:      provimuxString(provimuxBaseURLHost(auth)),
					Login:     provimuxLoginOf(auth),
				})
			} else if provider := provimuxPluginExecutorProvider(id); provider != "" {
				// A plugin executor's own models: no account, the plugin's provider.
				accounts = append(accounts, provimuxNameAccount{Provider: provider})
			}
		}
		if len(accounts) == 0 {
			continue
		}
		sort.SliceStable(accounts, func(i, j int) bool {
			return provimuxRouteLess(provimuxRoute{AuthIndex: accounts[i].AuthIndex, Provider: accounts[i].Provider, Kind: accounts[i].Kind, Priority: accounts[i].Priority, Login: accounts[i].Login},
				provimuxRoute{AuthIndex: accounts[j].AuthIndex, Provider: accounts[j].Provider, Kind: accounts[j].Kind, Priority: accounts[j].Priority, Login: accounts[j].Login})
		})
		response.Others = append(response.Others, provimuxOtherName{Name: other.Name, Accounts: accounts})
	}
	sort.SliceStable(response.Others, func(i, j int) bool { return provimuxNameLess(response.Others[i].Name, response.Others[j].Name) })

	if snapshot.Refused != nil && !home && enabled {
		refused := &provimuxRefusedNamesBody{Since: provimuxTime(snapshot.Refused.Since), Names: []provimuxRefusedName{}, Truncated: snapshot.Refused.Truncated}
		for _, name := range snapshot.Refused.Names {
			refused.Names = append(refused.Names, provimuxRefusedName{Name: name.Name, Requests: name.Requests, LastAt: provimuxTime(name.LastAt)})
		}
		response.Refused = refused
	}
	return response
}

// provimuxNameLess sorts names without case, then by their bytes, as model-routes sorts ids.
func provimuxNameLess(left, right string) bool {
	if l, r := strings.ToLower(left), strings.ToLower(right); l != r {
		return l < r
	}
	return left < right
}

func provimuxSetByRank(setBy string) int {
	switch setBy {
	case registry.ProvimuxNameSetByAlias:
		return 0
	case registry.ProvimuxNameSetByKeep:
		return 1
	default:
		return 2
	}
}

// provimuxPluginExecutorProvider is the provider of a plugin executor's model client
// ("plugin:<plugin>:<provider>:executor"), or "".
func provimuxPluginExecutorProvider(clientID string) string {
	if !strings.HasPrefix(clientID, "plugin:") || !strings.HasSuffix(clientID, ":executor") {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(clientID, "plugin:"), ":executor"), ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}
