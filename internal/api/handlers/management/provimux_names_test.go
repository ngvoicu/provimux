package management

import (
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

// S1-S4 and S6 (only-my-names.md section 9): GET /v8/management/provimux/names. S5 is in sdk/cliproxy,
// beside the model-lists source it reads.

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

// namesFixture registers accounts with the manager and hands each one's models to the registry
// through the rule, as the service does.
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
			registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, registry.ProvimuxOnlyMyNames(registered.ID, registered.Prefix, account.models))
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
		registry.ProvimuxNotServedMessage("gpt-5.5")
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
	registry.ProvimuxNotServedMessage("gpt-5.5")
	clock = clock.Add(90 * time.Second)
	registry.ProvimuxNotServedMessage("gpt-5.5")
	registry.ProvimuxNotServedMessage("nobody-offers-this")
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

// C (the reviewer's case): a login's own alias work-fast and a key that offers the same spelling. The
// name is served (on the login), so it is in names with the accounts that serve it, and not in others,
// with the switch on and off.
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
		want := 1 // on: the login only (the key's own work-fast is held back there)
		if !enabled {
			want = 2
		}
		if accountsNow != want {
			t.Fatalf("enabled %v: work-fast accounts %d, want %d: %s", enabled, accountsNow, want, raw)
		}
	}
}

// D: a name served under an account's prefix is listed as a copy of the name.
func TestProvimuxNamesPrefixedCopies(t *testing.T) {
	key := &coreauth.Auth{ID: "names-teamed", Provider: "claude", Prefix: "team-a", Attributes: map[string]string{"api_key": "sk-team"}}
	h := namesFixture(t, namesCfg(true), namesAccount{key, namesModels("opus", "team-a/opus", "team-a/other", "other")})
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

// B and C for plugins: a kept plugin model counts its plugin as an account; an unloaded plugin's
// models leave the answer and the refusal message at once.
func TestProvimuxNamesPluginModels(t *testing.T) {
	h := namesFixture(t, namesCfg(true, "plugin-owned"))
	reg := registry.GetGlobalRegistry()
	id := "plugin:unit:demo:executor"
	reg.RegisterClient(id, "demo", registry.ProvimuxOnlyMyNames(id, "", namesModels("plugin-owned", "plugin-unlisted")))
	t.Cleanup(func() { reg.UnregisterClient(id) })
	answer, raw := namesGet(t, h)
	var owned int
	for _, name := range answer.Names {
		if name.Name == "plugin-owned" {
			owned = name.Accounts
		}
	}
	if owned != 1 || len(answer.Others) != 1 || answer.Others[0].Name != "plugin-unlisted" || answer.Others[0].Accounts[0].AuthIndex != nil {
		t.Fatalf("active plugin: %s", raw)
	}
	reg.UnregisterClient(id)
	if msg := registry.ProvimuxNotServedMessage("plugin-unlisted"); msg != "" {
		t.Fatalf("an unloaded plugin still holds a name: %q", msg)
	}
	if answer, raw := namesGet(t, h); len(answer.Others) != 0 {
		t.Fatalf("unloaded plugin: %s", raw)
	}
}
