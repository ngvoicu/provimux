package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
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

func (h *Handler) provimuxNamesAnswer() provimuxNamesResponse {
	byID := map[string]*coreauth.Auth{}
	if manager := h.provimuxAuthManager(); manager != nil {
		for _, auth := range manager.List() {
			if auth != nil && !auth.Disabled {
				byID[auth.ID] = auth
			}
		}
	}
	// One rule for the whole answer: the rule in force, its refusal counter and the registrations, read
	// together (16.6; the second review's finding on overlapping reads).
	rule, registered, refused := registry.ProvimuxNamesView()
	index := registry.NewProvimuxIndex(registered)

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

	// What is shown: the enabled accounts and the plugin executors. Whether an id is served is decided over
	// every registration, with the predicate the lists and the door use.
	visible := func(clientID string) bool {
		return byID[clientID] != nil || provimuxPluginExecutorProvider(clientID) != ""
	}

	// names: the config's, exactly as written (alias rows and keep). A name is the owner's only when the
	// config says so.
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

	// Every registered id is in exactly one of names (as a name or a copy) and others, by the door's own
	// predicate: a request for the id is served or refused.
	type idClients struct {
		clients []string
		from    []string // the PrefixedFrom of its registrations
	}
	ids := map[string]*idClients{}
	var idOrder []string
	for _, entry := range registered {
		if !visible(entry.ClientID) {
			continue
		}
		group, ok := ids[entry.ID]
		if !ok {
			group = &idClients{}
			ids[entry.ID] = group
			idOrder = append(idOrder, entry.ID)
		}
		group.clients = append(group.clients, entry.ClientID)
		if entry.PrefixedFrom != "" {
			group.from = append(group.from, entry.PrefixedFrom)
		}
	}
	// copyTarget is the name a served id is listed under when it is not itself a name of the config: a copy
	// under an account's prefix lists under the name it copies; a spelling with a thinking suffix under the
	// name its base is, or copies.
	copied := map[string]string{} // id -> the config name some registration of it copies
	for _, entry := range registered {
		if from := strings.TrimSpace(entry.PrefixedFrom); from != "" && own[from] != nil && copied[entry.ID] == "" {
			copied[entry.ID] = from
		}
	}
	prefixedFrom := func(id string) string { return copied[id] }
	addCopy := func(target, id string, clients []string) {
		entry := own[target]
		if entry.copies[id] == nil {
			entry.copies[id] = map[string]struct{}{}
		}
		for _, clientID := range clients {
			entry.copies[id][clientID] = struct{}{}
		}
	}
	others := map[string]*provimuxOtherName{}
	for _, id := range idOrder {
		group := ids[id]
		if !rule.Serves(id, index) {
			other := others[id]
			if other == nil {
				other = &provimuxOtherName{Name: id, Accounts: []provimuxNameAccount{}}
				others[id] = other
			}
			for _, clientID := range group.clients {
				other.Accounts = append(other.Accounts, provimuxNameAccountOf(byID, clientID))
			}
			continue
		}
		placed := false
		if entry := own[id]; entry != nil {
			for _, clientID := range group.clients {
				entry.clients[clientID] = struct{}{}
			}
			placed = true
		}
		if target := prefixedFrom(id); target != "" {
			addCopy(target, id, group.clients)
			placed = true
		}
		if placed {
			continue
		}
		base := registry.ProvimuxBase(id)
		target := ""
		switch {
		case own[base] != nil:
			target = base
		case prefixedFrom(base) != "":
			target = prefixedFrom(base)
		}
		if target != "" {
			addCopy(target, id, group.clients)
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
	if setBy == registry.ProvimuxNameSetByAlias {
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
