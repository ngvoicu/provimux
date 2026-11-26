package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Service-level tests of models.only-my-names (only-my-names.md sections 9 and 16.7: R2-R11, K5, K6, S5):
// real configs, the real registration path, the in-process registry. Registration never consults the rule;
// what is served is read through the gate. No request leaves the process.

func namesReset(t *testing.T) {
	t.Helper()
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
}

func namesOn(keep ...string) *registry.OnlyMyNames {
	return &registry.OnlyMyNames{Enabled: true, Keep: keep}
}

// namesService registers auths with a manager the way keyService does, puts the rule of cfg in force as a
// commit does, and registers every auth's models.
func namesService(t *testing.T, cfg *config.Config, extra ...*coreauth.Auth) (*Service, []*coreauth.Auth) {
	t.Helper()
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	provimuxSetNamesRule(cfg)
	var registered []*coreauth.Auth
	for _, auth := range append(keySynthesize(t, cfg), extra...) {
		got, err := svc.coreManager.Register(t.Context(), auth)
		if err != nil {
			t.Fatal(err)
		}
		registered = append(registered, got)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(got.ID) })
	}
	for _, auth := range registered {
		svc.registerModelsForAuth(t.Context(), auth)
	}
	return svc, registered
}

// namesCommit puts the rule of cfg in force as a config commit does, and nothing else: no account registers
// again (16.3: no account has to).
func namesCommit(svc *Service, cfg *config.Config) {
	svc.cfgMu.Lock()
	svc.cfg = cfg
	svc.cfgMu.Unlock()
	provimuxSetNamesRule(cfg)
}

// namesReconfigure commits cfg and registers every auth again, as a reload whose config changes what the
// accounts register (an alias row, an exclusion) does.
func namesReconfigure(svc *Service, cfg *config.Config, auths []*coreauth.Auth) {
	namesCommit(svc, cfg)
	for _, auth := range auths {
		svc.registerModelsForAuth(context.Background(), auth)
	}
}

func namesLogin(id, provider string, attributes map[string]string) *coreauth.Auth {
	attrs := map[string]string{"auth_kind": "oauth"}
	for k, v := range attributes {
		attrs[k] = v
	}
	return &coreauth.Auth{ID: id, Provider: provider, Attributes: attrs, Metadata: map[string]any{"email": id + "@example.com"}}
}

func namesClaudeCatalog(t *testing.T) (string, string) {
	t.Helper()
	models := registry.GetClaudeModels()
	if len(models) < 2 {
		t.Fatalf("the Claude catalog has %d models", len(models))
	}
	return models[0].ID, models[1].ID
}

// namesServed is what auth registers that the gate serves: listed by both list loops and let through the
// door. It fails the test if the lists and the door disagree under one rule value. The lists, the door and
// the registrations are read one after another, so a reload that commits between those reads (the test's
// PUT returns before the watcher commits) would compare two rules: then it reads them all again, until
// one rule value and one set of registrations cover the whole reading.
func namesServed(t *testing.T, auth *coreauth.Auth) []string {
	t.Helper()
	for attempt := 0; ; attempt++ {
		rule, registered := onlymynames.Current(), provimuxRegistered(auth)
		listed, infos, doors := namesRead(registered)
		if onlymynames.Current() != rule || !slices.Equal(provimuxRegistered(auth), registered) {
			if attempt == 1000 {
				t.Fatal("the rule or the registrations changed during 1000 readings in a row")
			}
			continue // a reload committed during the reading: read again
		}
		var served []string
		for _, id := range registered {
			if listed[id] != doors[id] || infos[id] != doors[id] {
				t.Fatalf("%s: listed %v / %v, the door %v", id, listed[id], infos[id], doors[id])
			}
			if doors[id] {
				served = append(served, id)
			}
		}
		return served
	}
}

// namesRead reads both lists, then the door for each id.
func namesRead(ids []string) (listed, infos, doors map[string]bool) {
	listed = map[string]bool{}
	for _, model := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
		if id, _ := model["id"].(string); id != "" {
			listed[id] = true
		}
	}
	infos = map[string]bool{}
	for _, info := range registry.GetGlobalRegistry().GetAvailableModelInfos() {
		infos[info.ID] = true
	}
	doors = map[string]bool{}
	for _, id := range ids {
		doors[id] = namesServes(id)
	}
	return listed, infos, doors
}

// namesOthers is the status's others (ids that are not his), with the clients that register each, read
// through the same view and predicate as GET /provimux/names.
func namesOthers(ids ...string) map[string][]string {
	rule, registered, _ := registry.ProvimuxNamesView()
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	out := map[string][]string{}
	for _, entry := range registered {
		if wanted[entry.ClientID] && !rule.Serves(entry.ID) {
			out[entry.ID] = append(out[entry.ID], entry.ClientID)
		}
	}
	return out
}

func namesClaudeKey(prefix string, models ...config.ClaudeModel) config.ClaudeKey {
	return config.ClaudeKey{APIKey: "sk-names-" + prefix, BaseURL: "http://127.0.0.1:1", Prefix: prefix, Models: models}
}

// R1, R2 and K5: a key row with an alias serves the alias; a row whose alias equals its name, or that has
// none, gives no name. What every account registers is deep-equal without the setting, with it off, on, and
// off again; only what is listed and served moves.
func TestProvimuxOnlyMyNamesKeyRows(t *testing.T) {
	namesReset(t)
	rows := []config.ClaudeModel{{Name: "claude-up", Alias: "mine"}, {Name: "same", Alias: "same"}, {Name: "plain"}}

	without := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	svc, auths := namesService(t, without)
	key := auths[0]
	stock := namesRegisteredStable(key.ID)
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine", "plain", "same"}) {
		t.Fatalf("no setting: %v", got)
	}

	off := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	off.Models.OnlyMyNames = &registry.OnlyMyNames{Enabled: false, Keep: []string{"plain"}}
	namesReconfigure(svc, off, auths)
	if got := namesRegisteredStable(key.ID); !reflect.DeepEqual(got, stock) {
		t.Fatalf("off differs from no setting:\n off %+v\nnone %+v", got, stock)
	}
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine", "plain", "same"}) {
		t.Fatalf("off: %v", got)
	}
	if others := namesOthers(key.ID); len(others) != 1 || others["same"] == nil {
		t.Fatalf("would not be served: %v", others)
	}

	on := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	on.Models.OnlyMyNames = namesOn()
	namesReconfigure(svc, on, auths)
	if got := namesRegisteredStable(key.ID); !reflect.DeepEqual(got, stock) {
		t.Fatalf("K5: on changed what is registered:\n  on %+v\nnone %+v", got, stock)
	}
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("on: %v", got)
	}
	if others := namesOthers(key.ID); len(others) != 2 || others["same"] == nil || others["plain"] == nil {
		t.Fatalf("not served: %v", others)
	}
	namesReconfigure(svc, off, auths)
	if got := namesRegisteredStable(key.ID); !reflect.DeepEqual(got, stock) {
		t.Fatalf("off again differs: %+v", got)
	}
	if got := namesServed(t, key); len(got) != 3 {
		t.Fatalf("off again: %v", got)
	}
}

// R3: an oauth-model-alias row without fork serves the alias; with fork the model's own name is not served
// until keep names it.
func TestProvimuxOnlyMyNamesOAuthAliasAndFork(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-claude-login", "claude", nil)
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: c1, Alias: "my-opus"}}}}
	cfg.Models.OnlyMyNames = namesOn()
	svc, auths := namesService(t, cfg, login)
	if got := namesServed(t, login); !slices.Equal(got, []string{"my-opus"}) {
		t.Fatalf("no fork: %v", got)
	}
	fork := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: c1, Alias: "my-opus", Fork: true}}}}
	fork.Models.OnlyMyNames = namesOn()
	namesReconfigure(svc, fork, auths)
	if got := namesServed(t, login); !slices.Equal(got, []string{"my-opus"}) {
		t.Fatalf("fork: %v", got)
	}
	if others := namesOthers(login.ID); others[c1] == nil {
		t.Fatalf("fork: %s is not in others: %v", c1, others)
	}
	fork.Models.OnlyMyNames = namesOn(c1)
	namesCommit(svc, fork)
	want := []string{c1, "my-opus"}
	slices.Sort(want)
	if got := namesServed(t, login); !slices.Equal(got, want) {
		t.Fatalf("fork and keep: %v", got)
	}
}

// R5, as amended by section 17 (A): an alias in a login's own file is not a name the config sets. It is not
// served, on the login or on another account that registers the spelling, until keep lists it; then every
// account that registers it serves it.
func TestProvimuxOnlyMyNamesLoginFileAlias(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-login-file", "claude", map[string]string{"model_aliases": fmt.Sprintf(`[{"name":%q,"alias":"work-fast"}]`, c1)})
	build := func(keep ...string) *config.Config {
		cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "work-fast"}, config.ClaudeModel{Name: "key-only"})}}
		cfg.Models.OnlyMyNames = namesOn(keep...)
		return cfg
	}
	svc, auths := namesService(t, build(), login)
	if got := provimuxRegistered(login); !slices.Contains(got, "work-fast") {
		t.Fatalf("premise: the login registers its alias: %v", got)
	}
	key := auths[0]
	if got, other := namesServed(t, login), namesServed(t, key); len(got) != 0 || len(other) != 0 {
		t.Fatalf("a login-file alias alone serves: login %v, key %v", got, other)
	}
	if others := namesOthers(login.ID, key.ID); len(others["work-fast"]) != 2 {
		t.Fatalf("others %v", others)
	}
	namesCommit(svc, build("work-fast"))
	if got, other := namesServed(t, login), namesServed(t, key); !slices.Equal(got, []string{"work-fast"}) || !slices.Equal(other, []string{"work-fast"}) {
		t.Fatalf("kept: login %v, key %v", got, other)
	}
}

// R6: by name, not by route: an alias gpt-5.5 on a login leaves a key's own gpt-5.5 served; a pool keeps
// every member, in the same order as with the switch off.
func TestProvimuxOnlyMyNamesByNameAndPools(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-by-name", "claude", nil)
	build := func(only *registry.OnlyMyNames) *config.Config {
		cfg := &config.Config{
			OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: c1, Alias: "gpt-5.5"}}},
			ClaudeKey: []config.ClaudeKey{
				namesClaudeKey("", config.ClaudeModel{Name: "gpt-5.5"}, config.ClaudeModel{Name: "up-a", Alias: "names-pool"}),
				{APIKey: "sk-names-pool-b", BaseURL: "http://127.0.0.1:2", Models: []config.ClaudeModel{{Name: "up-b", Alias: "names-pool"}}},
			},
		}
		cfg.Models.OnlyMyNames = only
		return cfg
	}
	svc, auths := namesService(t, build(nil), login)
	poolOff := registry.GetGlobalRegistry().GetModelProviders("names-pool")
	countOff := registry.GetGlobalRegistry().GetModelCount("names-pool")
	namesCommit(svc, build(namesOn()))
	if !namesServes("gpt-5.5") || registry.GetGlobalRegistry().GetModelCount("gpt-5.5") != 2 {
		t.Fatal("gpt-5.5 is served by both accounts")
	}
	if got := registry.GetGlobalRegistry().GetModelProviders("names-pool"); !slices.Equal(got, poolOff) || registry.GetGlobalRegistry().GetModelCount("names-pool") != countOff || countOff != 2 {
		t.Fatalf("pool on %v, off %v (%d)", got, poolOff, countOff)
	}
	for _, auth := range auths {
		if served := namesServed(t, auth); len(served) == 0 {
			t.Fatalf("%s serves nothing", auth.ID)
		}
	}
}

// R7: a prefix belongs to the account (team-a/opus is served when opus is his, with force-model-prefix too:
// the prefix is declared in the config); a namespace belongs to the name (devin/x is served only when keep has
// devin/x).
func TestProvimuxOnlyMyNamesPrefixesAndNamespaces(t *testing.T) {
	namesReset(t)
	build := func(force bool) *config.Config {
		cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("team-a", config.ClaudeModel{Name: "up", Alias: "opus"}, config.ClaudeModel{Name: "other"})}}
		cfg.ForceModelPrefix = force
		cfg.Models.OnlyMyNames = namesOn()
		return cfg
	}
	svc, auths := namesService(t, build(false))
	if got := namesServed(t, auths[0]); !slices.Equal(got, []string{"opus", "team-a/opus"}) {
		t.Fatalf("prefix: %v", got)
	}
	namesReconfigure(svc, build(true), auths)
	if got := namesServed(t, auths[0]); !slices.Equal(got, []string{"team-a/opus"}) {
		t.Fatalf("force-model-prefix: %v (registered %v)", got, provimuxRegistered(auths[0]))
	}

	devinModels := registry.GetDevinModels()
	if len(devinModels) == 0 {
		t.Fatal("no Devin catalog")
	}
	bare := devinModels[0].ID
	devin := namesLogin("names-devin", "devin", nil)
	cfg := &config.Config{}
	cfg.Models.OnlyMyNames = namesOn(bare)
	devinSvc, _ := namesService(t, cfg, devin)
	if got := namesServed(t, devin); len(got) != 0 {
		t.Fatalf("keep %s serves %v", bare, got)
	}
	if others := namesOthers(devin.ID); others["devin/"+bare] == nil {
		t.Fatalf("devin/%s not in others: %v", bare, others)
	}
	kept := &config.Config{}
	kept.Models.OnlyMyNames = namesOn("devin/" + bare)
	namesCommit(devinSvc, kept)
	if got := namesServed(t, devin); !slices.Equal(got, []string{"devin/" + bare}) {
		t.Fatalf("keep devin/%s: %v", bare, got)
	}
}

// R9: the first list of a start already obeys the rule. The first config of a start is not committed
// (commitConfigUpdate runs on reloads only), so Build puts its rule in force; what is registered is
// everything the account offers (K5).
func TestProvimuxOnlyMyNamesFirstListOfAStart(t *testing.T) {
	namesReset(t)
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "up", Alias: "mine"}, config.ClaudeModel{Name: "held-at-start"})}}
	cfg.AuthDir = t.TempDir()
	cfg.Models.OnlyMyNames = namesOn()
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if err != nil {
		t.Fatal(err)
	}
	if !onlymynames.Current().Applies() {
		t.Fatal("Build did not put the rule in force")
	}
	// What Run does first for config keys, before any reload.
	service.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(t.Context()), service.currentConfig())
	auths := service.coreManager.List()
	if len(auths) != 1 {
		t.Fatalf("auths %d", len(auths))
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auths[0].ID) })
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"held-at-start", "mine"}) {
		t.Fatalf("registered at start: %v", got)
	}
	if got := namesServed(t, auths[0]); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("served at start: %v", got)
	}
}

// R11: a key's learned model and a login's learned model are not served unless named, and served once
// kept; an excluded model is not even registered, so it is not in others.
func TestProvimuxOnlyMyNamesLearnedModels(t *testing.T) {
	namesReset(t)
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	c1, _ := namesClaudeCatalog(t)
	build := func(keep ...string) *config.Config {
		cfg := keyCompatConfig("http://127.0.0.1:1/v1", config.OpenAICompatibilityModel{Name: "row", Alias: "mine"})
		cfg.OpenAICompatibility[0].ExcludedModels = []string{"secret-*"}
		cfg.Models.OnlyMyNames = namesOn(keep...)
		cfg.SanitizeOpenAICompatibility()
		return cfg
	}
	login := namesLogin("names-learned-login", "claude", nil)
	login.Metadata["account_id"] = "acct-names"
	cfg := build()
	svc, auths := keyService(t, cfg)
	if _, err := svc.coreManager.Register(t.Context(), login); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(login.ID) })
	key := auths[0]
	learned := keyList("learned-a", "secret-1")
	liveModelLists.put(key.ID, liveModelList{identity: liveModelListIdentity(key), ids: learned.ids, extra: learned.extra, fetchedAt: liveModelListNow()})
	loginList := keyList(c1, "learned-login")
	liveModelLists.put(login.ID, liveModelList{identity: liveModelListIdentity(login), provider: liveModelListProvider(login), ids: loginList.ids, extra: loginList.extra, fetchedAt: liveModelListNow()})
	all := append([]*coreauth.Auth{login}, auths...)
	namesReconfigure(svc, cfg, all)
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("key: %v", got)
	}
	if got := namesServed(t, login); len(got) != 0 {
		t.Fatalf("login: %v", got)
	}
	others := namesOthers(key.ID, login.ID)
	if others["learned-a"] == nil || others["learned-login"] == nil {
		t.Fatalf("learned models not in others: %v", others)
	}
	if others["secret-1"] != nil {
		t.Fatalf("an excluded model is in others: %v", others)
	}
	namesCommit(svc, build("learned-a", "LEARNED-LOGIN"))
	if got := namesServed(t, login); len(got) != 0 {
		t.Fatalf("keep LEARNED-LOGIN serves %v: names are compared exactly", got)
	}
	namesCommit(svc, build("learned-a", "learned-login"))
	if got := namesServed(t, key); !slices.Equal(got, []string{"learned-a", "mine"}) {
		t.Fatalf("key, kept: %v", got)
	}
	if got := namesServed(t, login); !slices.Equal(got, []string{"learned-login"}) {
		t.Fatalf("login, kept: %v", got)
	}
}

// The rule a config gives: every key family, openai-compatibility (disabled entries included),
// oauth-model-alias, keep; a row whose alias equals its name, or without one, gives no name.
func TestProvimuxOnlyMyNamesRuleOfAConfig(t *testing.T) {
	namesReset(t)
	cfg := &config.Config{
		GeminiKey:          []config.GeminiKey{{APIKey: "g", Models: []config.GeminiModel{{Name: "gemini-up", Alias: "g-alias"}}}},
		InteractionsKey:    []config.GeminiKey{{APIKey: "i", Models: []config.GeminiModel{{Name: "i-up", Alias: "i-alias"}}}},
		ClaudeKey:          []config.ClaudeKey{{APIKey: "c", Models: []config.ClaudeModel{{Name: "c-up", Alias: "c-alias"}, {Name: "same", Alias: "same"}, {Name: "none"}}}},
		CodexKey:           []config.CodexKey{{APIKey: "x", Models: []config.CodexModel{{Name: "x-up", Alias: "codex-alias"}}}},
		XAIKey:             []config.XAIKey{{APIKey: "x", Models: []config.XAIModel{{Name: "x-up", Alias: "xai-alias"}}}},
		MetaKey:            []config.MetaKey{{APIKey: "m", Models: []config.MetaModel{{Name: "m-up", Alias: "meta-alias"}}}},
		VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "v", Models: []config.VertexCompatModel{{Name: "v-up", Alias: "vertex-alias"}}}},
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "off", Disabled: true,
			Models: []config.OpenAICompatibilityModel{{Name: "o-up", Alias: "compat-alias"}}}},
		OAuthModelAlias: map[string][]config.OAuthModelAlias{"codex": {{Name: "gpt", Alias: "login-alias"}, {Name: "keep-me", Alias: "c-alias"}}},
	}
	cfg.Models.OnlyMyNames = namesOn("keep-me", "C-ALIAS", "pat*", "", "auto")
	provimuxSetNamesRule(cfg)
	rule, _, _ := registry.ProvimuxNamesView()
	got := map[string][]string{}
	for _, name := range rule.Names() {
		got[name.Name] = name.SetBy
	}
	want := map[string][]string{
		"g-alias": {"alias"}, "i-alias": {"alias"}, "c-alias": {"alias"}, "C-ALIAS": {"keep"}, "codex-alias": {"alias"},
		"xai-alias": {"alias"}, "meta-alias": {"alias"}, "vertex-alias": {"alias"}, "compat-alias": {"alias"},
		"login-alias": {"alias"}, "keep-me": {"keep"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names:\n got %v\nwant %v", got, want)
	}
	if ignored := rule.KeepIgnored(); len(ignored) != 3 || ignored[0].Reason != "pattern" || ignored[1].Reason != "empty" || ignored[2].Reason != "reserved" {
		t.Fatalf("keep_ignored %+v", ignored)
	}
	if !rule.Enabled() || rule.Home() {
		t.Fatalf("enabled %v home %v", rule.Enabled(), rule.Home())
	}
	home := &config.Config{}
	home.Home.Enabled = true
	home.Models.OnlyMyNames = namesOn()
	provimuxSetNamesRule(home)
	if rule, _, _ := registry.ProvimuxNamesView(); !rule.Home() || onlymynames.Current().Applies() {
		t.Fatal("home mode is not recorded")
	}
}

// S5, as amended (16.6): GET /provimux/model-lists with the switch on is the same as with it off: what an
// account registers does not move, so neither registered_models nor any other field does.
func TestProvimuxOnlyMyNamesModelListsDoNotMove(t *testing.T) {
	namesReset(t)
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-model-lists", "claude", nil)
	login.FileName = "names-model-lists.json"
	login.Attributes["path"] = "/auths/names-model-lists.json"
	login.Metadata["account_id"] = "acct-model-lists"
	build := func(only *registry.OnlyMyNames) *config.Config {
		cfg := provimuxOn()
		cfg.Models.OnlyMyNames = only
		return cfg
	}
	svc, auths := namesService(t, build(nil), login)
	list := keyList(c1, "learned-login")
	liveModelLists.put(login.ID, liveModelList{identity: liveModelListIdentity(login), provider: liveModelListProvider(login), ids: list.ids, extra: list.extra, fetchedAt: liveModelListNow()})
	handler := management.NewHandlerWithoutConfigFilePath(build(nil), svc.coreManager)
	get := func(query string) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-lists"+query, nil)
		handler.ProvimuxModelLists(ctx)
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("model-lists %d %s", recorder.Code, recorder.Body.String())
		}
		delete(body, "observed_at")
		return body
	}
	namesReconfigure(svc, build(nil), auths)
	query := "?view=live&auth_index=" + auths[0].EnsureIndex()
	off := get(query)
	namesCommit(svc, build(namesOn(c1)))
	on := get(query)
	if !reflect.DeepEqual(off, on) {
		t.Fatalf("model-lists moved with the switch:\n off %v\n  on %v", off, on)
	}
	if got := namesServed(t, auths[0]); !slices.Equal(got, []string{c1}) {
		t.Fatalf("served %v", got)
	}
}

// K6 at the service: no lag, and the second review's schedules as regressions. The rule is in force from
// its commit, whatever registration does before or after: a registration computed under the old rule and
// published after the new one, a publication the fence rejects, an account removed in between.
func TestProvimuxOnlyMyNamesLateAndRejectedRegistrations(t *testing.T) {
	namesReset(t)
	rows := []config.ClaudeModel{{Name: "up", Alias: "mine"}, {Name: "plain"}}
	off := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	svc, auths := namesService(t, off)
	key := auths[0]
	late := newModelRegistrationFence(key, off) // computed under the old rule
	on := &config.Config{ClaudeKey: off.ClaudeKey}
	on.Models.OnlyMyNames = namesOn()
	namesCommit(svc, on) // the commit alone, registration paused throughout
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("after the commit, nothing registered again: %v", got)
	}
	// The late publication of a registration computed under the old rule: a new name appears, and is not
	// served, because registration never decided what is served.
	if !svc.registerResolvedModelsForAuthFenced(key, late, "claude", []*ModelInfo{{ID: "mine"}, {ID: "plain"}, {ID: "late-model"}}) {
		t.Fatal("premise: the late publication is accepted")
	}
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("after the late publication: %v", got)
	}
	if others := namesOthers(key.ID); others["late-model"] == nil || others["plain"] == nil {
		t.Fatalf("others after the late publication: %v", others)
	}
	// A publication the fence rejects changes nothing the gate reads.
	stale := newModelRegistrationFence(key, on)
	svc.registerModelsForAuth(t.Context(), key)
	if svc.registerResolvedModelsForAuthFenced(key, stale, "claude", []*ModelInfo{{ID: "stale-only"}}) {
		t.Fatal("premise: the fence accepted a stale publication")
	}
	if got := namesServed(t, key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("after the rejected publication: %v", got)
	}
	if registry.GetGlobalRegistry().GetModelCount("stale-only") != 0 || !namesRefuse("stale-only") {
		t.Fatal("a rejected publication registered its name")
	}
	// Removal: the name is no longer offered at once (no record), and back off nothing is held.
	svc.applyCoreAuthRemoval(context.Background(), key.ID)
	if registry.GetGlobalRegistry().GetModelCount("plain") != 0 {
		t.Fatal("a removed account still offers plain")
	}
	namesCommit(svc, off)
	if !namesServes("plain") {
		t.Fatal("off: plain")
	}
}

// K6: 250 accounts added, published late against a rejected fence, and removed through the service leave the
// layer with nothing but the rule and at most 256 counted names.
func TestProvimuxOnlyMyNamesChurn(t *testing.T) {
	namesReset(t)
	cfg := &config.Config{}
	cfg.Models.OnlyMyNames = namesOn("churn-mine")
	svc, _ := namesService(t, cfg)
	for i := range 250 {
		auth, err := svc.coreManager.Register(t.Context(), &coreauth.Auth{ID: fmt.Sprintf("names-churn-%d", i), Provider: "claude", Attributes: map[string]string{"api_key": "sk"}})
		if err != nil {
			t.Fatal(err)
		}
		fence := newModelRegistrationFence(auth, cfg)
		if !svc.registerResolvedModelsForAuthFenced(auth, fence, "claude", []*ModelInfo{{ID: "churn-mine"}, {ID: "churn-held"}}) {
			t.Fatal("premise: the first publication is accepted")
		}
		if svc.registerResolvedModelsForAuthFenced(auth, fence, "claude", []*ModelInfo{{ID: "churn-obsolete"}}) { // the fence is spent
			t.Fatal("premise: the obsolete publication is rejected")
		}
		namesRefuse("churn-held")
		svc.applyCoreAuthRemoval(context.Background(), auth.ID)
	}
	if refused := onlymynames.RefusedNow(); refused == nil || len(refused.Names) != 1 || refused.Names[0].Requests != 250 {
		t.Fatalf("refused %+v", refused)
	}
	for _, name := range []string{"churn-held", "churn-obsolete", "churn-mine"} {
		if registry.GetGlobalRegistry().GetModelCount(name) != 0 {
			t.Fatalf("%s is still offered after every account went", name)
		}
	}
	_, registered, _ := registry.ProvimuxNamesView()
	for _, entry := range registered {
		if strings.HasPrefix(entry.ClientID, "names-churn-") {
			t.Fatalf("left behind: %+v", entry)
		}
	}
}

// K6 and B (section 17), the reviews' case-only alias edit: the alias mine becomes Mine. Names are compared
// exactly, so from the commit mine is no longer his and is refused, and Mine is served as soon as the account
// registers it; the status never shows an account for a spelling the door refuses.
func TestProvimuxOnlyMyNamesCaseOnlyAliasEdit(t *testing.T) {
	namesReset(t)
	build := func(alias string) *config.Config {
		cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "up", Alias: alias}, config.ClaudeModel{Name: "plain"})}}
		cfg.Models.OnlyMyNames = namesOn()
		return cfg
	}
	svc, auths := namesService(t, build("mine"))
	namesCommit(svc, build("Mine"))
	if got := namesServed(t, auths[0]); len(got) != 0 {
		t.Fatalf("after the commit: %v", got)
	}
	if !namesRefuse("mine") {
		t.Fatal("mine after the commit")
	}
	namesReconfigure(svc, build("Mine"), auths)
	if got := namesServed(t, auths[0]); !slices.Equal(got, []string{"Mine"}) {
		t.Fatalf("after the registration: %v", got)
	}
}

// Finding 6 of the second review: a stale Antigravity-style replacement that the registry rejects leaves the
// status describing the registry, and a registered name in exactly one of names and others (the login-file
// alias work-fast, not a name of the config, under others only).
func TestProvimuxOnlyMyNamesRejectedReplacementKeepsTheStatusExact(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-replace", "claude", map[string]string{"model_aliases": fmt.Sprintf(`[{"name":%q,"alias":"work-fast"}]`, c1)})
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "work-fast"})}}
	cfg.Models.OnlyMyNames = namesOn()
	svc, _ := namesService(t, cfg, login)
	reg := registry.GetGlobalRegistry()
	_, epoch := reg.GetModelsAndEpochForClient(login.ID)
	svc.registerModelsForAuth(t.Context(), login) // moves the epoch: the replacement below is stale
	if _, ok := reg.ReplaceClientModels(login.ID, "claude", epoch, nil); ok {
		t.Fatal("premise: the stale replacement is accepted")
	}
	handler := management.NewHandlerWithoutConfigFilePath(cfg, svc.coreManager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/names", nil)
	handler.ProvimuxNames(ctx)
	var body struct {
		Names []struct {
			Name     string `json:"name"`
			Accounts int    `json:"accounts"`
		} `json:"names"`
		Others []struct {
			Name string `json:"name"`
		} `json:"others"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	inOthers := false
	for _, other := range body.Others {
		inOthers = inOthers || other.Name == "work-fast"
	}
	for _, name := range body.Names {
		if name.Name == "work-fast" {
			t.Fatalf("work-fast in names: %s", recorder.Body.String())
		}
	}
	if !inOthers || namesServes("work-fast") {
		t.Fatalf("work-fast: %s", recorder.Body.String())
	}
}

// A rule change concurrent with registrations, lists and requests (run with -race): every list and every
// door decision is one of the two rules', never a mix within one list.
func TestProvimuxOnlyMyNamesRuleChangesUnderLoad(t *testing.T) {
	namesReset(t)
	rows := []config.ClaudeModel{{Name: "up", Alias: "load-mine"}, {Name: "load-plain"}}
	off := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	svc, auths := namesService(t, off)
	on := &config.Config{ClaudeKey: off.ClaudeKey}
	on.Models.OnlyMyNames = namesOn()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				namesCommit(svc, on)
			} else {
				namesCommit(svc, off)
			}
		}
	})
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			svc.registerModelsForAuth(context.Background(), auths[0])
		}
	})
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		var ids []string
		for _, model := range registry.GetGlobalRegistry().GetAvailableModels("openai") {
			if id, _ := model["id"].(string); strings.HasPrefix(id, "load-") {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if !slices.Equal(ids, []string{"load-mine"}) && !slices.Equal(ids, []string{"load-mine", "load-plain"}) && len(ids) != 0 {
			t.Fatalf("a list mixes rules: %v", ids)
		}
		namesRefuse("load-plain")
	}
	close(stop)
	wg.Wait()
}

// namesServes is the door's predicate under the rule in force: true when it does not apply.
func namesServes(name string) bool { return !onlymynames.Current().Refuses(name) }

// namesRefuse judges a request for name as the door does, under the rule in force, and counts a refusal.
func namesRefuse(name string) bool {
	rule := onlymynames.Current()
	if !rule.Refuses(name) {
		return false
	}
	onlymynames.Count(rule, name)
	return true
}

// namesRegisteredStable is what clientID registers, without Created: a configured model takes it from the
// clock at each registration, so two registrations a second apart differ there alone (the third review's
// finding 10).
func namesRegisteredStable(clientID string) []*ModelInfo {
	models := registry.GetGlobalRegistry().GetModelsForClient(clientID)
	for _, model := range models {
		model.Created = 0
	}
	return models
}
