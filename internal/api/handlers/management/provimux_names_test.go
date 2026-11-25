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

// S2: names are the config's (alias rows and keep), each with who set it, compared exactly.
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
		"claude-opus-5-5": {"keep"}, "devin/claude-opus-4.6": {"keep"}, "Fast": {"alias"}, "gpt-5.5": {"alias"}, "GPT-5.5": {"keep"},
		"my-opus": {"alias"}, "team-sonnet": {"alias"}, "vertex-flash": {"alias"}, "work-fast": {"alias"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names:\n got %v\nwant %v\n%s", got, want, raw)
	}
	if !slices.Equal(order, []string{"claude-opus-5-5", "devin/claude-opus-4.6", "Fast", "GPT-5.5", "gpt-5.5", "my-opus", "team-sonnet", "vertex-flash", "work-fast"}) {
		t.Fatalf("order %v", order)
	}
	if len(answer.KeepIgnored) != 3 || answer.KeepIgnored[0].Value != "claude-*" || answer.KeepIgnored[0].Reason != "pattern" || answer.KeepIgnored[1].Reason != "empty" ||
		answer.KeepIgnored[2].Value != "auto" || answer.KeepIgnored[2].Reason != "reserved" {
		t.Fatalf("keep_ignored %+v", answer.KeepIgnored)
	}
	provimuxValidate(t, "NamesResponse", raw)

	// A (section 17): an alias in a login's own file is not a name the config sets. my-opus is the config's
	// (an oauth-model-alias row); own-sonnet, only in the login's file, is under others, to be kept in one click.
	login := &coreauth.Auth{ID: "fixture-login", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth",
		"model_aliases": `[{"name":"claude-opus-5-5","alias":"my-opus"},{"name":"claude-sonnet-5","alias":"own-sonnet"}]`}}
	h = namesFixture(t, cfg, namesAccount{login, []*registry.ModelInfo{{ID: "my-opus", OAuthAlias: "my-opus"}, {ID: "own-sonnet", OAuthAlias: "own-sonnet"}}})
	answer, raw = namesGet(t, h)
	for _, name := range answer.Names {
		switch name.Name {
		case "my-opus":
			if !slices.Equal(name.SetBy, []string{"alias"}) || name.Accounts != 1 {
				t.Fatalf("my-opus %+v", name)
			}
		case "own-sonnet":
			t.Fatalf("a login-file alias is a name: %s", raw)
		case "team-sonnet":
			if name.Accounts != 0 {
				t.Fatalf("a name nothing answers: %+v", name)
			}
		}
	}
	if len(answer.Others) != 1 || answer.Others[0].Name != "own-sonnet" {
		t.Fatalf("others: %s", raw)
	}
	if strings.Contains(string(raw), "login-file") {
		t.Fatalf("set_by login-file is no longer given: %s", raw)
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
		if !slices.Equal(names, []string{"opus:alias"}) {
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

// A (section 17, the review's first case of finding 3): a login's own alias work-fast, and a key that
// registers the same spelling. Not a name of the config, so it is under others with both accounts, never also
// in names, and refused; kept, it is a name with both accounts, and served.
func TestProvimuxNamesLoginFileAliasIsAnOther(t *testing.T) {
	for _, keep := range [][]string{nil, {"work-fast"}} {
		accounts := namesStandardAccounts()
		accounts[1].models = append(accounts[1].models, &registry.ModelInfo{ID: "work-fast"})
		h := namesFixture(t, namesCfg(true, keep...), accounts...)
		answer, raw := namesGet(t, h)
		inOthers, inNames := 0, 0
		for _, other := range answer.Others {
			if other.Name == "work-fast" {
				inOthers = len(other.Accounts)
			}
		}
		for _, name := range answer.Names {
			if name.Name == "work-fast" {
				inNames = name.Accounts
			}
		}
		kept := keep != nil
		if kept && (inNames != 2 || inOthers != 0 || !registry.ProvimuxServes("work-fast")) ||
			!kept && (inNames != 0 || inOthers != 2 || registry.ProvimuxServes("work-fast")) {
			t.Fatalf("keep %v: in names %d, in others %d, served %v: %s", keep, inNames, inOthers, registry.ProvimuxServes("work-fast"), raw)
		}
	}
}

// D (section 17): one rule for the whole answer. A commit that lands while an answer is being built (the clock
// is read after the view) does not leak into it: ETag, enabled, applies and refused are all the first rule's.
func TestProvimuxNamesOneRulePerAnswer(t *testing.T) {
	cfgA := namesCfg(true)
	cfgA.SourceHash = "aaaa"
	h := namesFixture(t, cfgA, namesStandardAccounts()...)
	registry.ProvimuxRefuse("gpt-5.5")
	cfgB := namesCfg(false)
	cfgB.SourceHash = "bbbb"
	previous := provimuxNow
	t.Cleanup(func() { provimuxNow = previous })
	committed := false
	provimuxNow = func() time.Time {
		if !committed {
			committed = true
			registry.SetProvimuxNamesRule(config.ProvimuxNamesRule(cfgB))
		}
		return previous()
	}
	answer, raw := namesGet(t, h)
	if !committed || !answer.OnlyMyNames.Enabled || answer.Refused == nil || len(answer.Refused.Names) != 1 || !strings.Contains(string(raw), `aaaa`) {
		t.Fatalf("an answer mixes two rules: %s", raw)
	}
	answer, raw = namesGet(t, h)
	if answer.OnlyMyNames.Enabled || answer.Refused != nil || !strings.Contains(string(raw), `bbbb`) {
		t.Fatalf("the next answer: %s", raw)
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

// K7 (only-my-names.md 16.7, extended by section 17): a property test over generated registries and rules,
// with spellings that differ only in case, spellings with a thinking suffix, prefixed copies and login-file
// aliases. Every registered id is in exactly one of names (a name with accounts, or a copy) and others; the ids
// under names are exactly the ids the two list loops show and the door lets through (registry.ProvimuxRefuse
// is the door's predicate); the ids under others are exactly the ones skipped and refused.
func TestProvimuxNamesStatusAndGateAgree(t *testing.T) {
	random := rand.New(rand.NewPCG(16, 7))
	pool := []string{"opus", "Opus", "OPUS", "opus(high)", "Opus(high)", "gpt-5.5", "GPT-5.5", "fast", "fast(low)", "sonnet",
		"devin/x", "learned-a", "team-a/opus", "work-fast", "Mixed-Case", "mixed-case", "auto"}
	prefixes := []string{"", "", "team-a", "team-b"}
	for round := range 400 {
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
					if random.IntN(3) != 0 || name == "auto" && random.IntN(2) == 0 {
						continue
					}
					alias := ""
					if random.IntN(5) == 0 {
						alias = name // a login-file or global alias: the flag decides nothing
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
						ID       string `json:"id"`
						Accounts int    `json:"accounts"`
					} `json:"copies"`
					Accounts int `json:"accounts"`
				} `json:"names"`
				Others []struct {
					Name     string            `json:"name"`
					Accounts []json.RawMessage `json:"accounts"`
				} `json:"others"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			registered := map[string]int{}
			for _, account := range accounts {
				for _, model := range account.models {
					registered[model.ID]++
				}
			}
			inNames, inOthers := map[string]int{}, map[string]int{}
			// A copy that is itself a name of the config is both a copy and a row (the second review's finding
			// 13): the same accounts, shown twice.
			for _, name := range body.Names {
				inNames[name.Name] = max(inNames[name.Name], name.Accounts)
				for _, copied := range name.Copies {
					inNames[copied.ID] = max(inNames[copied.ID], copied.Accounts)
				}
			}
			for _, other := range body.Others {
				inOthers[other.Name] += len(other.Accounts)
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
			for id, count := range registered {
				refused, offered := registry.ProvimuxRefuse(id)
				named, other := inNames[id] > 0, inOthers[id] > 0
				switch {
				case named == other:
					t.Fatalf("%s: in names %v, in others %v\n%s", id, named, other, raw)
				case inNames[id]+inOthers[id] != count:
					t.Fatalf("%s: %d accounts shown, %d register it\n%s", id, inNames[id]+inOthers[id], count, raw)
				case named != listed[id] || named != infos[id]:
					t.Fatalf("%s: in names %v, listed %v / %v\n%s", id, named, listed[id], infos[id], raw)
				case other != refused || (refused && !offered):
					t.Fatalf("%s: in others %v, refused %v (offered %v)\n%s", id, other, refused, offered, raw)
				}
			}
		})
	}
}
