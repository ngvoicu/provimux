package provimux

import (
	"sort"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: who answered (answered-by.md 8.4), on the lists and the wire. Fixtures are invented.

var answeredMarkerPattern = regexp.MustCompile(`^[as]\.[a-z2-7]{20}$`)

type answeredListRow struct {
	AuthIndex *string `json:"auth_index"`
	Provider  string  `json:"provider"`
	Kind      *string `json:"kind"`
	Host      *string `json:"host"`
	Priority  int     `json:"priority"`
	Account   *string `json:"account"`
}

func (r answeredListRow) account() string {
	if r.Account == nil {
		return ""
	}
	return *r.Account
}

func decodeAnsweredModels(t *testing.T, body []byte) map[string][]answeredListRow {
	t.Helper()
	var decoded struct {
		Data []struct {
			ID       string            `json:"id"`
			Accounts []answeredListRow `json:"accounts"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(body, &decoded); errDecode != nil {
		t.Fatalf("decode /v1/models: %v (%s)", errDecode, body)
	}
	out := map[string][]answeredListRow{}
	for _, model := range decoded.Data {
		out[model.ID] = model.Accounts
	}
	return out
}

func decodeAnsweredRoutes(t *testing.T, body []byte) map[string][]answeredListRow {
	t.Helper()
	var decoded struct {
		Models []struct {
			ID     string            `json:"id"`
			Routes []answeredListRow `json:"routes"`
		} `json:"models"`
	}
	if errDecode := json.Unmarshal(body, &decoded); errDecode != nil {
		t.Fatalf("decode model-routes: %v (%s)", errDecode, body)
	}
	out := map[string][]answeredListRow{}
	for _, model := range decoded.Models {
		out[model.ID] = model.Routes
	}
	return out
}

func sortedAccounts(rows []answeredListRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.account())
	}
	sort.Strings(out)
	return out
}

// Both lists give each account its marker, and one account the same marker in both: every route of model-routes
// carries its credential's marker, and per model the markers of /v1/models?include=accounts are the routes'. Finding
// 8's two keys, alike in provider, kind and host, have two markers; the login held in two files has one marker on two
// rows; the switch off leaves account out of every row (TestProvimuxAnsweredByOffIsToday holds the bytes).
func TestProvimuxAnsweredByLists(t *testing.T) {
	manager, auths := registerAnsweredByLists(t)
	answeredBySwitch(t, true)
	byIndex := map[string]*coreauth.Auth{}
	for _, auth := range auths {
		byIndex[auth.EnsureIndex()] = auth
	}

	accounts := decodeAnsweredModels(t, answeredByModelsRaw(t, manager, "?include=accounts"))
	routes := decodeAnsweredRoutes(t, answeredByRoutes(t, manager))
	for _, model := range []string{answeredSharedModel, answeredKeyModel} {
		if len(routes[model]) == 0 || len(routes[model]) != len(accounts[model]) {
			t.Fatalf("%s: %d routes, %d accounts", model, len(routes[model]), len(accounts[model]))
		}
		for _, route := range routes[model] {
			if route.AuthIndex == nil || byIndex[*route.AuthIndex] == nil {
				t.Fatalf("%s: a route of no fixture credential: %+v", model, route)
			}
			want := byIndex[*route.AuthIndex].ProvimuxAccountMarker()
			if !answeredMarkerPattern.MatchString(route.account()) || route.account() != want {
				t.Fatalf("%s: route %s carries %q, want its credential's %q", model, *route.AuthIndex, route.account(), want)
			}
		}
		if got, want := sortedAccounts(accounts[model]), sortedAccounts(routes[model]); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: include=accounts gives %v, model-routes %v", model, got, want)
		}
	}

	markers := map[string]string{}
	for _, auth := range auths {
		markers[auth.ID] = auth.ProvimuxAccountMarker()
	}
	if markers["claude:apikey:dear"] == markers["claude:apikey:cheap"] {
		t.Fatal("finding 8's two keys share a marker")
	}
	if markers["claude-ada.json"] != markers["claude-ada-again.json"] || !strings.HasPrefix(markers["claude-ada.json"], "a.") {
		t.Fatalf("one login in two files: %s and %s, want one a. marker", markers["claude-ada.json"], markers["claude-ada-again.json"])
	}
	count := 0
	for _, row := range accounts[answeredSharedModel] {
		if row.account() == markers["claude-ada.json"] {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("the login held twice is on %d rows of the shared model, want 2", count)
	}

	if body := answeredByModels(t, manager, ""); strings.Contains(string(body), `"account"`) {
		t.Fatalf("/v1/models without include carries an account: %s", body)
	}
	answeredBySwitch(t, false)
	for name, body := range map[string][]byte{"include=accounts": answeredByModels(t, manager, "?include=accounts"), "model-routes": answeredByRoutes(t, manager)} {
		if strings.Contains(string(body), `"account"`) {
			t.Fatalf("%s with the switch off carries an account: %s", name, body)
		}
	}
}
