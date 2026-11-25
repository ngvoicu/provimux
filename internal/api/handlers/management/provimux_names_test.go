package management

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// S1-S4, S6 and K7 (only-my-names.md sections 9 and 16.7): GET /v8/management/provimux/names. S5 is in
// sdk/cliproxy, beside the model-lists source it reads.

type namesAnswer struct {
	OnlyMyNames struct {
		Enabled    bool    `json:"enabled"`
		Applies    bool    `json:"applies"`
		NotApplied *string `json:"not_applied"`
	} `json:"only_my_names"`
	Names []struct {
		Name     string   `json:"name"`
		SetBy    []string `json:"set_by"`
		Accounts int      `json:"accounts"`
	} `json:"names"`
	KeepIgnored []struct{ Value, Reason string } `json:"keep_ignored"`
	Others      []struct {
		Name     string `json:"name"`
		Accounts []struct {
			AuthIndex *string `json:"auth_index"`
			Provider  string  `json:"provider"`
			Kind      *string `json:"kind"`
			Priority  int     `json:"priority"`
		} `json:"accounts"`
	} `json:"others"`
	Refused *struct {
		Since string `json:"since"`
		Names []struct {
			Name     string `json:"name"`
			Requests int    `json:"requests"`
			LastAt   string `json:"last_at"`
		} `json:"names"`
		Truncated bool `json:"truncated"`
	} `json:"refused"`
}

func namesGet(t *testing.T, h *Handler) (namesAnswer, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/names", nil)
	h.ProvimuxNames(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var answer namesAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer, recorder.Body.Bytes()
}

// namesFixture registers accounts with the manager and their models with the registry, as the service
// does (registration never consults the rule), and puts cfg's rule in force.
type namesAccount struct {
	auth   *coreauth.Auth
	models []*registry.ModelInfo
}

func namesFixture(t *testing.T, cfg *config.Config, accounts ...namesAccount) *Handler {
	t.Helper()
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
	registry.SetProvimuxNamesRule(config.ProvimuxNamesRule(cfg))
	manager := coreauth.NewManager(nil, nil, nil)
	for _, account := range accounts {
		registered, err := manager.Register(t.Context(), account.auth)
		if err != nil {
			t.Fatal(err)
		}
		if !registered.Disabled {
			registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, account.models)
		}
		id := registered.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return &Handler{cfg: cfg, authManager: manager}
}

func namesModels(ids ...string) []*registry.ModelInfo {
	out := make([]*registry.ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, &registry.ModelInfo{ID: id})
	}
	return out
}

func namesCfg(enabled bool, keep ...string) *config.Config {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "sk-names", Models: []config.ClaudeModel{{Name: "claude-up", Alias: "opus"}}}}}
	cfg.Models.OnlyMyNames = &registry.OnlyMyNames{Enabled: enabled, Keep: keep}
	return cfg
}

func namesStandardAccounts() []namesAccount {
	login := &coreauth.Auth{ID: "names-login.json", Provider: "codex", FileName: "names-login.json", Label: "office@example.com",
		Attributes: map[string]string{"auth_kind": "oauth", "path": "/auths/names-login.json", "priority": "5",
			"model_aliases": `[{"name":"gpt-5.5-codex","alias":"work-fast"}]`},
		Metadata: map[string]any{"email": "office@example.com"}}
	key := &coreauth.Auth{ID: "names-key", Provider: "claude", Label: "claude-team", Attributes: map[string]string{"api_key": "sk-names", "base_url": "https://API.example.com/v1"}}
	off := &coreauth.Auth{ID: "names-off", Provider: "claude", Disabled: true, Attributes: map[string]string{"api_key": "sk-off"}}
	return []namesAccount{
		{login, []*registry.ModelInfo{{ID: "work-fast", OAuthAlias: "work-fast"}, {ID: "gpt-5.5"}, {ID: "opus"}}},
		{key, namesModels("opus", "claude-haiku-5", "gpt-5.5")},
		{off, namesModels("only-on-a-disabled-account")},
	}
}

// S1: the answer validates against the contract with the switch on and off, refusals included, and
// the example in pui13/examples follows it too.
func TestProvimuxNamesFollowsTheContract(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		h := namesFixture(t, namesCfg(enabled, "claude-*", "keep-me"), namesStandardAccounts()...)
		registry.ProvimuxRefuse("gpt-5.5")
		answer, raw := namesGet(t, h)
		provimuxValidate(t, "NamesResponse", raw)
		if answer.OnlyMyNames.Enabled != enabled || (answer.Refused != nil) != enabled {
			t.Fatalf("enabled %v: %s", enabled, raw)
		}
		for _, key := range []string{`"observed_at"`, `"only_my_names"`, `"names"`, `"keep_ignored"`, `"others"`, `"refused"`, `"not_applied":null`} {
			if !strings.Contains(string(raw), key) {
				t.Fatalf("enabled %v: %s missing: %s", enabled, key, raw)
			}
		}
		if strings.Contains(string(raw), "sk-names") {
			t.Fatal("a key in the answer")
		}
	}
	data, err := os.ReadFile(filepath.Join(provimuxDesignDir(t), "examples", "names.json"))
	if err != nil {
		t.Fatal(err)
	}
	provimuxValidate(t, "NamesResponse", data)
}

// S2: names equals the console's rule on the shared fixture config, plus keep and a login's own
// aliases, each with who set it.
func TestProvimuxNamesOnTheSharedFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(provimuxDesignDir(t), "examples", "names-fixture.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	h := namesFixture(t, cfg)
	answer, raw := namesGet(t, h)
	got := map[string][]string{}
	var order []string
	for _, name := range answer.Names {
		got[name.Name] = name.SetBy
		order = append(order, name.Name)
	}
	want := map[string][]string{
		"claude-opus-5-5": {"keep"}, "devin/claude-opus-4.6": {"keep"}, "Fast": {"alias"}, "gpt-5.5": {"alias", "keep"},
		"my-opus": {"alias"}, "team-sonnet": {"alias"}, "vertex-flash": {"alias"}, "work-fast": {"alias"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names:\n got %v\nwant %v\n%s", got, want, raw)
	}
	if !slices.Equal(order, []string{"claude-opus-5-5", "devin/claude-opus-4.6", "Fast", "gpt-5.5", "my-opus", "team-sonnet", "vertex-flash", "work-fast"}) {
		t.Fatalf("order %v", order)
	}
	if len(answer.KeepIgnored) != 2 || answer.KeepIgnored[0].Value != "claude-*" || answer.KeepIgnored[0].Reason != "pattern" || answer.KeepIgnored[1].Reason != "empty" {
		t.Fatalf("keep_ignored %+v", answer.KeepIgnored)
	}
	provimuxValidate(t, "NamesResponse", raw)

	// A login's own alias joins with set_by login-file, on a name the config also sets or not.
	login := &coreauth.Auth{ID: "fixture-login", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth",
		"model_aliases": `[{"name":"claude-opus-5-5","alias":"my-opus"},{"name":"claude-sonnet-5","alias":"own-sonnet"}]`}}
	h = namesFixture(t, cfg, namesAccount{login, []*registry.ModelInfo{{ID: "my-opus", OAuthAlias: "my-opus"}, {ID: "own-sonnet", OAuthAlias: "own-sonnet"}}})
	answer, _ = namesGet(t, h)
	for _, name := range answer.Names {
		switch name.Name {
		case "my-opus":
			if !slices.Equal(name.SetBy, []string{"alias", "login-file"}) || name.Accounts != 1 {
				t.Fatalf("my-opus %+v", name)
			}
		case "own-sonnet":
			if !slices.Equal(name.SetBy, []string{"login-file"}) || name.Accounts != 1 {
				t.Fatalf("own-sonnet %+v", name)
			}
		case "team-sonnet":
			if name.Accounts != 0 {
				t.Fatalf("a name nothing answers: %+v", name)
			}
		}
	}
}

// S3: others plus the served names is every name an enabled account offers; a disabled account
// contributes nothing. Others carries the accounts as model-routes names them, in its order.
func TestProvimuxNamesOthersAndServedCoverEveryOfferedName(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		h := namesFixture(t, namesCfg(enabled), namesStandardAccounts()...)
		answer, raw := namesGet(t, h)
		offered := map[string]bool{}
		for _, account := range namesStandardAccounts() {
			if account.auth.Disabled {
				continue
			}
			for _, model := range account.models {
				offered[model.ID] = true
			}
		}
		covered := map[string]bool{}
		for _, other := range answer.Others {
			covered[other.Name] = true
		}
		for _, name := range answer.Names {
			if name.Accounts > 0 {
				covered[name.Name] = true
			}
		}
		if !reflect.DeepEqual(covered, offered) {
			t.Fatalf("enabled %v: covered %v, offered %v\n%s", enabled, covered, offered, raw)
		}
		if strings.Contains(string(raw), "only-on-a-disabled-account") {
			t.Fatal("a disabled account contributes a name")
		}
		var gpt []string
		for _, other := range answer.Others {
			if other.Name == "gpt-5.5" {
				for _, account := range other.Accounts {
					gpt = append(gpt, account.Provider+"/"+*account.Kind)
				}
			}
		}
		// Priority 5 (the login) first, then the key (priority 0).
		if !slices.Equal(gpt, []string{"codex/oauth", "claude/apikey"}) {
			t.Fatalf("gpt-5.5 accounts %v", gpt)
		}
		var names []string
		for _, name := range answer.Names {
			names = append(names, name.Name+":"+strings.Join(name.SetBy, ","))
		}
		if !slices.Equal(names, []string{"opus:alias", "work-fast:login-file"}) {
			t.Fatalf("names %v", names)
		}
		if answer.Names[0].Accounts != 2 {
			t.Fatalf("opus is registered on both enabled accounts, whatever the switch: %+v", answer.Names[0])
		}
	}
}

// S4 at the endpoint: refused counts per name, keeps the last time, says since when, and is null
// while the switch is off.
func TestProvimuxNamesRefused(t *testing.T) {
	clock := time.Date(2026, 11, 25, 10, 4, 0, 0, time.UTC)
	t.Cleanup(registry.SetProvimuxNamesClock(func() time.Time { return clock }))
	h := namesFixture(t, namesCfg(true), namesStandardAccounts()...)
	registry.ProvimuxRefuse("gpt-5.5")
	clock = clock.Add(90 * time.Second)
	registry.ProvimuxRefuse("gpt-5.5")
	registry.ProvimuxRefuse("nobody-offers-this")
	answer, raw := namesGet(t, h)
	if answer.Refused == nil || answer.Refused.Since != "2026-11-25T10:04:00Z" || len(answer.Refused.Names) != 1 ||
		answer.Refused.Names[0].Name != "gpt-5.5" || answer.Refused.Names[0].Requests != 2 || answer.Refused.Names[0].LastAt != "2026-11-25T10:05:30Z" || answer.Refused.Truncated {
		t.Fatalf("refused: %s", raw)
	}
	h = namesFixture(t, namesCfg(false), namesStandardAccounts()...)
	if answer, raw := namesGet(t, h); answer.Refused != nil {
		t.Fatalf("off: %s", raw)
	}
}

// S6: Home mode: applies false, not_applied "home", refused null.
func TestProvimuxNamesHome(t *testing.T) {
	cfg := namesCfg(true)
	cfg.Home.Enabled = true
	h := namesFixture(t, cfg, namesStandardAccounts()...)
	answer, raw := namesGet(t, h)
	if answer.OnlyMyNames.Applies || answer.OnlyMyNames.NotApplied == nil || *answer.OnlyMyNames.NotApplied != "home" || !answer.OnlyMyNames.Enabled || answer.Refused != nil {
		t.Fatalf("home: %s", raw)
	}
	provimuxValidate(t, "NamesResponse", raw)
}

// The endpoint sits behind the management key like the other Provimux endpoints.
func TestProvimuxNamesNeedsManagementKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := namesFixture(t, namesCfg(true))
	h.cfg.RemoteManagement.SecretKey = ""
	router := gin.New()
	group := router.Group("/v8/management", h.Middleware())
	group.GET("/provimux/names", h.ProvimuxNames)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v8/management/provimux/names", nil))
	if recorder.Code == http.StatusOK {
		t.Fatalf("answered without a management key: %d", recorder.Code)
	}
}

// C and cost 1 of 16.1: a login's own alias work-fast and a key that registers the same spelling. The alias
// is the owner's by name, so the key serves it too: in names with both accounts, never in others, with the
// switch on and off.
func TestProvimuxNamesAServedNameIsNotAnOther(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		accounts := namesStandardAccounts()
		accounts[1].models = append(accounts[1].models, &registry.ModelInfo{ID: "work-fast"})
		h := namesFixture(t, namesCfg(enabled), accounts...)
		answer, raw := namesGet(t, h)
		for _, other := range answer.Others {
			if other.Name == "work-fast" {
				t.Fatalf("enabled %v: work-fast in others: %s", enabled, raw)
			}
		}
		var accountsNow int
		for _, name := range answer.Names {
			if name.Name == "work-fast" {
				accountsNow = name.Accounts
			}
		}
		if accountsNow != 2 {
			t.Fatalf("enabled %v: work-fast accounts %d, want 2: %s", enabled, accountsNow, raw)
		}
		if !registry.ProvimuxServes("work-fast") {
			t.Fatalf("enabled %v: the gate refuses work-fast", enabled)
		}
	}
}

// D: a name served under an account's prefix is listed as a copy of the name.
func TestProvimuxNamesPrefixedCopies(t *testing.T) {
	key := &coreauth.Auth{ID: "names-teamed", Provider: "claude", Prefix: "team-a", Attributes: map[string]string{"api_key": "sk-team"}}
	h := namesFixture(t, namesCfg(true), namesAccount{key, []*registry.ModelInfo{{ID: "opus"}, {ID: "team-a/opus", PrefixedFrom: "opus"},
		{ID: "team-a/other", PrefixedFrom: "other"}, {ID: "other"}}})
	answer, raw := namesGet(t, h)
	if len(answer.Names) != 1 || answer.Names[0].Name != "opus" || answer.Names[0].Accounts != 1 {
		t.Fatalf("names: %s", raw)
	}
	var body struct {
		Names []struct {
			Copies []struct {
				ID       string `json:"id"`
				Accounts int    `json:"accounts"`
			} `json:"copies"`
		} `json:"names"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if copies := body.Names[0].Copies; len(copies) != 1 || copies[0].ID != "team-a/opus" || copies[0].Accounts != 1 {
		t.Fatalf("copies: %s", raw)
	}
	var others []string
	for _, other := range answer.Others {
		others = append(others, other.Name)
	}
	if !slices.Equal(others, []string{"other", "team-a/other"}) {
		t.Fatalf("others %v", others)
	}
	provimuxValidate(t, "NamesResponse", raw)
}

// Plugins: a kept plugin model counts its plugin as an account; an unloaded plugin's models leave the answer
// and the refusal message at once (nothing is remembered).
func TestProvimuxNamesPluginModels(t *testing.T) {
	h := namesFixture(t, namesCfg(true, "plugin-owned"))
	reg := registry.GetGlobalRegistry()
	id := "plugin:unit:demo:executor"
	reg.RegisterClient(id, "demo", namesModels("plugin-owned", "plugin-unlisted"))
	t.Cleanup(func() { reg.UnregisterClient(id) })
	answer, raw := namesGet(t, h)
	var owned int
	for _, name := range answer.Names {
		if name.Name == "plugin-owned" {
			owned = name.Accounts
		}
	}
	if owned != 1 || len(answer.Others) != 1 || answer.Others[0].Name != "plugin-unlisted" || answer.Others[0].Accounts[0].AuthIndex != nil || answer.Others[0].Accounts[0].Provider != "demo" {
		t.Fatalf("active plugin: %s", raw)
	}
	if refused, offered := registry.ProvimuxRefuse("plugin-unlisted"); !refused || !offered {
		t.Fatal("the plugin's unlisted model is served")
	}
	reg.UnregisterClient(id)
	if _, offered := registry.ProvimuxRefuse("plugin-unlisted"); offered {
		t.Fatal("an unloaded plugin still offers a name")
	}
	if answer, raw := namesGet(t, h); len(answer.Others) != 0 {
		t.Fatalf("unloaded plugin: %s", raw)
	}
}

// Finding 13 of the second review: a prefixed copy that is itself on keep stays among its base name's copies
// and has its own row.
func TestProvimuxNamesKeptCopyStaysACopy(t *testing.T) {
	key := &coreauth.Auth{ID: "names-teamed-13", Provider: "claude", Prefix: "team-a", Attributes: map[string]string{"api_key": "sk-team"}}
	h := namesFixture(t, namesCfg(true, "team-a/opus"), namesAccount{key, []*registry.ModelInfo{{ID: "opus"}, {ID: "team-a/opus", PrefixedFrom: "opus"}}})
	_, raw := namesGet(t, h)
	var body struct {
		Names []struct {
			Name   string   `json:"name"`
			SetBy  []string `json:"set_by"`
			Copies []struct {
				ID string `json:"id"`
			} `json:"copies"`
		} `json:"names"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Names) != 2 || body.Names[0].Name != "opus" || len(body.Names[0].Copies) != 1 || body.Names[0].Copies[0].ID != "team-a/opus" ||
		body.Names[1].Name != "team-a/opus" || !slices.Equal(body.Names[1].SetBy, []string{"keep"}) {
		t.Fatalf("names: %s", raw)
	}
}

// applied (16.6): the rule in force, exact: its config's ETag and generation, applying always false.
func TestProvimuxNamesAppliedIsTheRuleInForce(t *testing.T) {
	cfg := namesCfg(true)
	cfg.SourceHash = "0123abcd"
	h := namesFixture(t, cfg, namesStandardAccounts()...)
	var body struct {
		Applied struct {
			Generation uint64  `json:"generation"`
			ConfigETag *string `json:"config_etag"`
			Applying   bool    `json:"applying"`
		} `json:"applied"`
	}
	_, raw := namesGet(t, h)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Applied.Applying || body.Applied.ConfigETag == nil || *body.Applied.ConfigETag != configV8ETag("0123abcd") || body.Applied.Generation != 1 {
		t.Fatalf("applied: %s", raw)
	}
	next := namesCfg(false)
	next.SourceHash = "4567"
	registry.SetProvimuxNamesRule(config.ProvimuxNamesRule(next))
	_, raw = namesGet(t, h)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Applied.Applying || *body.Applied.ConfigETag != configV8ETag("4567") || body.Applied.Generation != 2 {
		t.Fatalf("applied after the next rule: %s", raw)
	}
}

// K7 (only-my-names.md 16.7): a property test over generated registries and rules. Every registered id is in
// exactly one of names (as a name or a copy) and others; the ids under names are exactly the ids the two list
// loops show and the door lets through (registry.ProvimuxRefuse is the door's predicate); the ids under
// others are exactly the ones skipped and refused. Ids are lower-case: names and others group spellings that
// differ only in case, which a property over single ids cannot see.
func TestProvimuxNamesStatusAndGateAgree(t *testing.T) {
	random := rand.New(rand.NewPCG(16, 7))
	pool := []string{"opus", "gpt-5.5", "fast", "sonnet", "devin/x", "learned-a", "learned-b", "team-a/opus", "work-fast"}
	prefixes := []string{"", "", "team-a", "team-b"}
	for round := range 300 {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			cfg := &config.Config{}
			var rows []config.ClaudeModel
			for _, name := range pool {
				if random.IntN(4) == 0 {
					rows = append(rows, config.ClaudeModel{Name: "up-" + name, Alias: name})
				}
			}
			cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-k7", Models: rows}}
			var keep []string
			for _, name := range pool {
				if random.IntN(6) == 0 {
					keep = append(keep, name)
				}
			}
			cfg.Models.OnlyMyNames = &registry.OnlyMyNames{Enabled: true, Keep: keep}
			var accounts []namesAccount
			for a := range 1 + random.IntN(4) {
				prefix := prefixes[random.IntN(len(prefixes))]
				auth := &coreauth.Auth{ID: fmt.Sprintf("k7-%d-%d", round, a), Provider: "claude", Prefix: prefix, Attributes: map[string]string{"api_key": "sk"}}
				var models []*registry.ModelInfo
				seen := map[string]bool{}
				for _, name := range pool {
					if random.IntN(3) != 0 {
						continue
					}
					alias := ""
					if random.IntN(5) == 0 {
						alias = name
					}
					if !seen[name] {
						seen[name] = true
						models = append(models, &registry.ModelInfo{ID: name, OAuthAlias: alias})
					}
					if prefix != "" && !seen[prefix+"/"+name] {
						seen[prefix+"/"+name] = true
						models = append(models, &registry.ModelInfo{ID: prefix + "/" + name, OAuthAlias: alias, PrefixedFrom: name})
					}
				}
				accounts = append(accounts, namesAccount{auth, models})
			}
			h := namesFixture(t, cfg, accounts...)
			_, raw := namesGet(t, h)
			var body struct {
				Names []struct {
					Name   string `json:"name"`
					Copies []struct {
						ID string `json:"id"`
					} `json:"copies"`
					Accounts int `json:"accounts"`
				} `json:"names"`
				Others []struct {
					Name string `json:"name"`
				} `json:"others"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			inNames, inOthers := map[string]bool{}, map[string]bool{}
			for _, name := range body.Names {
				if name.Accounts > 0 {
					inNames[name.Name] = true
				}
				for _, copied := range name.Copies {
					inNames[copied.ID] = true
				}
			}
			for _, other := range body.Others {
				inOthers[other.Name] = true
			}
			listed := map[string]bool{}
			for _, model := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
				if id, _ := model["id"].(string); id != "" {
					listed[id] = true
				}
			}
			infos := map[string]bool{}
			for _, info := range registry.GetGlobalRegistry().GetAvailableModelInfos() {
				infos[info.ID] = true
			}
			registered := map[string]bool{}
			for _, account := range accounts {
				for _, model := range account.models {
					registered[model.ID] = true
				}
			}
			for id := range registered {
				refused, offered := registry.ProvimuxRefuse(id)
				switch {
				case inNames[id] == inOthers[id]:
					t.Fatalf("%s: in names %v, in others %v\n%s", id, inNames[id], inOthers[id], raw)
				case inNames[id] != listed[id] || inNames[id] != infos[id]:
					t.Fatalf("%s: in names %v, listed %v / %v\n%s", id, inNames[id], listed[id], infos[id], raw)
				case inOthers[id] != refused || (refused && !offered):
					t.Fatalf("%s: in others %v, refused %v (offered %v)\n%s", id, inOthers[id], refused, offered, raw)
				}
			}
			for id := range listed {
				if !registered[id] && strings.HasPrefix(id, "k7") {
					t.Fatalf("listed %s, which nobody registers", id)
				}
			}
		})
	}
}
