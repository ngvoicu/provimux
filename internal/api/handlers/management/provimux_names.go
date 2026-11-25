package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/names, the state of models.only-my-names (only-my-names.md
// sections 6 and 16.6; contract: .github/provimux/design/pui13/openapi.yaml, NamesResponse). Computed when
// asked, from the rule in force and from what the accounts register at that moment: the same rule value and
// the same registry every list and every request are checked against, with the same predicate. The same
// shape with the switch on or off: off, others is what would stop being served. Read-only, in memory; no key,
// token or error text. The response only grows.

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

// provimuxNamesApplied says which rule is in force: the config's ETag (the config API's) and its generation.
// applying is always false: the rule is in force for the next list and request from the commit on.
type provimuxNamesApplied struct {
	Generation uint64  `json:"generation"`
	ConfigETag *string `json:"config_etag"`
	Applying   bool    `json:"applying"`
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

// provimuxNamesBeforeAccounts runs between the reading of the registrations and of the accounts. A test
// registers a login and commits a rule there.
var provimuxNamesBeforeAccounts = func() {}

func (h *Handler) provimuxNamesAnswer() provimuxNamesResponse {
	// One rule for the whole answer: the rule in force, its refusal counter and the registrations, read together
	// (one read lock of the registry, under which a rule is put in force). The accounts are read after: an account
	// is in the auth manager before its models are in the registry, so every registration read is matched to its
	// account, and a count is right at the first answer that carries a rule's ETag.
	rule, registered, refused := registry.ProvimuxNamesView()
	provimuxNamesBeforeAccounts()
	byID := map[string]*coreauth.Auth{}
	if manager := h.provimuxAuthManager(); manager != nil {
		for _, auth := range manager.List() {
			if auth != nil && !auth.Disabled {
				byID[auth.ID] = auth
			}
		}
	}

	home := rule.Home()
	response := provimuxNamesResponse{
		ObservedAt:  provimuxTime(provimuxNow()),
		OnlyMyNames: provimuxOnlyMyNamesState{Enabled: rule.Enabled(), Applies: !home},
		Applied:     provimuxNamesApplied{Generation: rule.Generation()},
		Names:       []provimuxOwnName{},
		KeepIgnored: []provimuxKeepIgnored{},
		Others:      []provimuxOtherName{},
	}
	if source := rule.Source(); source != "" {
		response.Applied.ConfigETag = provimuxString(configV8ETag(source))
	}
	if home {
		response.OnlyMyNames.NotApplied = provimuxString("home")
	}

	// What is shown: the enabled accounts and the plugin executors.
	visible := func(clientID string) bool {
		return byID[clientID] != nil || provimuxPluginExecutorProvider(clientID) != ""
	}

	// names: the config's, exactly as written (alias rows and keep).
	type ownName struct {
		name    string
		setBy   []string
		clients map[string]struct{}
		copies  map[string]map[string]struct{}
	}
	own := map[string]*ownName{}
	var order []string
	for _, name := range rule.Names() {
		own[name.Name] = &ownName{name: name.Name, setBy: name.SetBy, clients: map[string]struct{}{}, copies: map[string]map[string]struct{}{}}
		order = append(order, name.Name)
	}

	// Every registered id is judged with the door's predicate under the same rule value: served (in names, as a
	// name, a copy, or both) or not (in others).
	ids := map[string][]string{}
	var idOrder []string
	for _, entry := range registered {
		if !visible(entry.ClientID) {
			continue
		}
		if _, ok := ids[entry.ID]; !ok {
			idOrder = append(idOrder, entry.ID)
		}
		ids[entry.ID] = append(ids[entry.ID], entry.ClientID)
	}
	addCopy := func(target, id string, clients []string) {
		entry := own[target]
		if entry == nil {
			return
		}
		if entry.copies[id] == nil {
			entry.copies[id] = map[string]struct{}{}
		}
		for _, clientID := range clients {
			entry.copies[id][clientID] = struct{}{}
		}
	}
	// copyTarget is the name a served id is listed under besides its own row: the name it copies under a prefix
	// the config declares, else the name its base (without a thinking suffix) is or copies.
	copyTarget := func(id string) string {
		if name, ok := rule.CopyOf(id); ok {
			return name
		}
		if base := onlymynames.Base(id); base != id {
			if rule.Has(base) {
				return base
			}
			if name, ok := rule.CopyOf(base); ok {
				return name
			}
		}
		return ""
	}
	others := map[string]*provimuxOtherName{}
	for _, id := range idOrder {
		clients := ids[id]
		if !rule.Serves(id) {
			other := others[id]
			if other == nil {
				other = &provimuxOtherName{Name: id, Accounts: []provimuxNameAccount{}}
				others[id] = other
			}
			for _, clientID := range clients {
				other.Accounts = append(other.Accounts, provimuxNameAccountOf(byID, clientID))
			}
			continue
		}
		if entry := own[id]; entry != nil {
			for _, clientID := range clients {
				entry.clients[clientID] = struct{}{}
			}
		}
		if target := copyTarget(id); target != "" {
			addCopy(target, id, clients)
		}
	}

	sort.Slice(order, func(i, j int) bool { return provimuxNameLess(order[i], order[j]) })
	for _, key := range order {
		entry := own[key]
		setBy := entry.setBy
		sort.SliceStable(setBy, func(i, j int) bool { return provimuxSetByRank(setBy[i]) < provimuxSetByRank(setBy[j]) })
		name := provimuxOwnName{Name: entry.name, SetBy: setBy, Accounts: len(entry.clients), Copies: []provimuxServedCopy{}}
		for id, clients := range entry.copies {
			name.Copies = append(name.Copies, provimuxServedCopy{ID: id, Accounts: len(clients)})
		}
		sort.Slice(name.Copies, func(i, j int) bool { return provimuxNameLess(name.Copies[i].ID, name.Copies[j].ID) })
		response.Names = append(response.Names, name)
	}

	for _, ignored := range rule.KeepIgnored() {
		response.KeepIgnored = append(response.KeepIgnored, provimuxKeepIgnored{Value: ignored.Value, Reason: ignored.Reason})
	}

	for _, other := range others {
		accounts := other.Accounts
		sort.SliceStable(accounts, func(i, j int) bool {
			return provimuxRouteLess(provimuxRoute{AuthIndex: accounts[i].AuthIndex, Provider: accounts[i].Provider, Kind: accounts[i].Kind, Priority: accounts[i].Priority, Login: accounts[i].Login},
				provimuxRoute{AuthIndex: accounts[j].AuthIndex, Provider: accounts[j].Provider, Kind: accounts[j].Kind, Priority: accounts[j].Priority, Login: accounts[j].Login})
		})
		response.Others = append(response.Others, *other)
	}
	sort.SliceStable(response.Others, func(i, j int) bool { return provimuxNameLess(response.Others[i].Name, response.Others[j].Name) })

	if refused != nil {
		body := &provimuxRefusedNamesBody{Since: provimuxTime(refused.Since), Names: []provimuxRefusedName{}, Truncated: refused.Truncated}
		for _, name := range refused.Names {
			body.Names = append(body.Names, provimuxRefusedName{Name: name.Name, Requests: name.Requests, LastAt: provimuxTime(name.LastAt)})
		}
		response.Refused = body
	}
	return response
}

// provimuxNameAccountOf names a registry client as a route of GET /provimux/model-routes names its account; a
// plugin executor's own models have no account, only the plugin's provider.
func provimuxNameAccountOf(byID map[string]*coreauth.Auth, clientID string) provimuxNameAccount {
	if auth := byID[clientID]; auth != nil {
		return provimuxNameAccount{
			AuthIndex: provimuxString(lockedAuthIndex(auth)),
			Provider:  auth.Provider,
			Kind:      provimuxString(auth.AuthKind()),
			Priority:  auth.Priority(),
			Host:      provimuxString(provimuxBaseURLHost(auth)),
			Login:     provimuxLoginOf(auth),
		}
	}
	return provimuxNameAccount{Provider: provimuxPluginExecutorProvider(clientID)}
}

// provimuxNameLess sorts names without case, then by their bytes, as model-routes sorts ids.
func provimuxNameLess(left, right string) bool {
	if l, r := strings.ToLower(left), strings.ToLower(right); l != r {
		return l < r
	}
	return left < right
}

func provimuxSetByRank(setBy string) int {
	if setBy == onlymynames.SetByAlias {
		return 0
	}
	return 1
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
