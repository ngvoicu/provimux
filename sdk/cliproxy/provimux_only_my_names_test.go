package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Service-level tests of models.only-my-names (only-my-names.md section 9, R2-R11 and C4): real
// configs, the real registration path, the in-process registry. No request leaves the process.

func namesReset(t *testing.T) {
	t.Helper()
	registry.ResetProvimuxNames()
	t.Cleanup(registry.ResetProvimuxNames)
}

func namesOn(keep ...string) *registry.OnlyMyNames {
	return &registry.OnlyMyNames{Enabled: true, Keep: keep}
}

// namesService registers auths with a manager the way keyService does, sets the rule of cfg as a
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

// namesReconfigure commits cfg's rule and registers every auth again, as a reload that changes the
// rule does (the watcher forces every auth through registration).
func namesReconfigure(svc *Service, cfg *config.Config, auths []*coreauth.Auth) {
	svc.cfgMu.Lock()
	svc.cfg = cfg
	svc.cfgMu.Unlock()
	provimuxSetNamesRule(cfg)
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

func namesOthers(ids ...string) map[string][]string {
	snapshot := registry.ProvimuxNamesState(ids)
	out := map[string][]string{}
	for _, other := range snapshot.Others {
		out[other.Name] = other.ClientIDs
	}
	return out
}

func namesClaudeKey(prefix string, models ...config.ClaudeModel) config.ClaudeKey {
	return config.ClaudeKey{APIKey: "sk-names-" + prefix, BaseURL: "http://127.0.0.1:1", Prefix: prefix, Models: models}
}

// R1 and R2: a key row with an alias serves the alias; a row whose alias equals its name, or that
// has none, gives no name. With the switch off every model is registered, exactly as without the
// setting, and the record says what would be held back.
func TestProvimuxOnlyMyNamesKeyRows(t *testing.T) {
	namesReset(t)
	rows := []config.ClaudeModel{{Name: "claude-up", Alias: "mine"}, {Name: "same", Alias: "same"}, {Name: "plain"}}

	without := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	_, auths := namesService(t, without)
	key := auths[0]
	stock := registry.GetGlobalRegistry().GetModelsForClient(key.ID)

	off := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	off.Models.OnlyMyNames = &registry.OnlyMyNames{Enabled: false, Keep: []string{"plain"}}
	svc, _ := namesService(t, off)
	if got := registry.GetGlobalRegistry().GetModelsForClient(key.ID); !reflect.DeepEqual(got, stock) {
		t.Fatalf("off differs from no setting:\n off %+v\nnone %+v", got, stock)
	}
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"mine", "plain", "same"}) {
		t.Fatalf("off: %v", got)
	}
	if others := namesOthers(key.ID); len(others) != 1 || others["same"] == nil {
		t.Fatalf("would be held back: %v", others)
	}

	on := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", rows...)}}
	on.Models.OnlyMyNames = namesOn()
	namesReconfigure(svc, on, auths)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("on: %v", got)
	}
	if others := namesOthers(key.ID); len(others) != 2 || others["same"] == nil || others["plain"] == nil {
		t.Fatalf("held back: %v", others)
	}
	// Off again: the last state equals the first.
	namesReconfigure(svc, off, auths)
	if got := registry.GetGlobalRegistry().GetModelsForClient(key.ID); !reflect.DeepEqual(got, stock) {
		t.Fatalf("off again differs: %+v", got)
	}
}

// R3: an oauth-model-alias row without fork serves the alias; with fork the model's own name is held
// back until keep names it.
func TestProvimuxOnlyMyNamesOAuthAliasAndFork(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-claude-login", "claude", nil)
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: c1, Alias: "my-opus"}}}}
	cfg.Models.OnlyMyNames = namesOn()
	svc, auths := namesService(t, cfg, login)
	if got := provimuxRegistered(login); !slices.Equal(got, []string{"my-opus"}) {
		t.Fatalf("no fork: %v", got)
	}
	fork := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: c1, Alias: "my-opus", Fork: true}}}}
	fork.Models.OnlyMyNames = namesOn()
	namesReconfigure(svc, fork, auths)
	if got := provimuxRegistered(login); !slices.Equal(got, []string{"my-opus"}) {
		t.Fatalf("fork: %v", got)
	}
	if others := namesOthers(login.ID); others[c1] == nil {
		t.Fatalf("fork: %s is not in others: %v", c1, others)
	}
	fork.Models.OnlyMyNames = namesOn(c1)
	namesReconfigure(svc, fork, auths)
	want := []string{c1, "my-opus"}
	slices.Sort(want)
	if got := provimuxRegistered(login); !slices.Equal(got, want) {
		t.Fatalf("fork and keep: %v", got)
	}
}

// R5: an alias in a login's own file is served on that login and makes no other account's model
// of that name served.
func TestProvimuxOnlyMyNamesLoginFileAlias(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-login-file", "claude", map[string]string{"model_aliases": fmt.Sprintf(`[{"name":%q,"alias":"work-fast"}]`, c1)})
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "work-fast"})}}
	cfg.Models.OnlyMyNames = namesOn()
	_, auths := namesService(t, cfg, login)
	if got := provimuxRegistered(login); !slices.Equal(got, []string{"work-fast"}) {
		t.Fatalf("login: %v", got)
	}
	key := auths[0]
	if got := provimuxRegistered(key); len(got) != 0 {
		t.Fatalf("the key's own work-fast: %v", got)
	}
	if count := registry.GetGlobalRegistry().GetModelCount("work-fast"); count != 1 {
		t.Fatalf("work-fast has %d accounts, want the login only", count)
	}
}

// R6: by name, not by route: an alias gpt-5.5 on a login leaves a key's own gpt-5.5 served; a pool
// keeps every member, in the same order as with the switch off.
func TestProvimuxOnlyMyNamesByNameAndPools(t *testing.T) {
	namesReset(t)
	c1, _ := namesClaudeCatalog(t)
	login := namesLogin("names-by-name", "claude", nil)
	build := func(only *registry.OnlyMyNames) *config.Config {
		cfg := &config.Config{
			OAuthModelAlias: map[string][]config.OAuthModelAlias{"claude": {{Name: c1, Alias: "gpt-5.5"}}},
			ClaudeKey: []config.ClaudeKey{
				namesClaudeKey("", config.ClaudeModel{Name: "gpt-5.5"}, config.ClaudeModel{Name: "up-a", Alias: "pool"}),
				{APIKey: "sk-names-pool-b", BaseURL: "http://127.0.0.1:2", Models: []config.ClaudeModel{{Name: "up-b", Alias: "pool"}}},
			},
		}
		cfg.Models.OnlyMyNames = only
		return cfg
	}
	svc, auths := namesService(t, build(nil), login)
	poolOff := registry.GetGlobalRegistry().GetModelProviders("pool")
	countOff := registry.GetGlobalRegistry().GetModelCount("pool")
	gptOff := registry.GetGlobalRegistry().GetModelCount("gpt-5.5")
	namesReconfigure(svc, build(namesOn()), auths)
	if got := registry.GetGlobalRegistry().GetModelCount("gpt-5.5"); got != gptOff || got != 2 {
		t.Fatalf("gpt-5.5 accounts on %d, off %d, want 2", got, gptOff)
	}
	if got := registry.GetGlobalRegistry().GetModelProviders("pool"); !slices.Equal(got, poolOff) || registry.GetGlobalRegistry().GetModelCount("pool") != countOff || countOff != 2 {
		t.Fatalf("pool on %v (%d), off %v (%d)", got, registry.GetGlobalRegistry().GetModelCount("pool"), poolOff, countOff)
	}
}

// R7: a prefix belongs to the account (team-a/opus is served when opus is his; opus too unless
// force-model-prefix); a namespace belongs to the name (devin/x is served only when keep has devin/x).
func TestProvimuxOnlyMyNamesPrefixesAndNamespaces(t *testing.T) {
	namesReset(t)
	build := func(force bool) *config.Config {
		cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("team-a", config.ClaudeModel{Name: "up", Alias: "opus"}, config.ClaudeModel{Name: "other"})}}
		cfg.ForceModelPrefix = force
		cfg.Models.OnlyMyNames = namesOn()
		return cfg
	}
	svc, auths := namesService(t, build(false))
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"opus", "team-a/opus"}) {
		t.Fatalf("prefix: %v", got)
	}
	namesReconfigure(svc, build(true), auths)
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"team-a/opus"}) {
		t.Fatalf("force-model-prefix: %v", got)
	}

	devinModels := registry.GetDevinModels()
	if len(devinModels) == 0 {
		t.Fatal("no Devin catalog")
	}
	bare := devinModels[0].ID
	devin := namesLogin("names-devin", "devin", nil)
	cfg := &config.Config{}
	cfg.Models.OnlyMyNames = namesOn(bare)
	devinSvc, devinAuths := namesService(t, cfg, devin)
	if got := provimuxRegistered(devin); len(got) != 0 {
		t.Fatalf("keep %s serves %v", bare, got)
	}
	if others := namesOthers(devin.ID); others["devin/"+bare] == nil {
		t.Fatalf("devin/%s not in others: %v", bare, others)
	}
	kept := &config.Config{}
	kept.Models.OnlyMyNames = namesOn("devin/" + bare)
	namesReconfigure(devinSvc, kept, devinAuths)
	if got := provimuxRegistered(devin); !slices.Equal(got, []string{"devin/" + bare}) {
		t.Fatalf("keep devin/%s: %v", bare, got)
	}
}

// R9: the first registration of a start already applies the rule. The first config of a start is
// not committed (commitConfigUpdate runs on reloads only), so Build sets the rule; a hook on the
// registry fails the test if a held-back name is ever registered.
type namesRegistryWatch struct {
	mu   sync.Mutex
	seen []string
	got  chan struct{}
}

func (w *namesRegistryWatch) OnModelsRegistered(_ context.Context, _, _ string, models []*ModelInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, model := range models {
		w.seen = append(w.seen, model.ID)
	}
	select {
	case w.got <- struct{}{}:
	default:
	}
}

func (w *namesRegistryWatch) OnModelsUnregistered(context.Context, string, string) {}

func TestProvimuxOnlyMyNamesFirstRegistrationOfAStart(t *testing.T) {
	namesReset(t)
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "up", Alias: "mine"}, config.ClaudeModel{Name: "held-at-start"})}}
	cfg.AuthDir = t.TempDir()
	cfg.Models.OnlyMyNames = namesOn()
	watch := &namesRegistryWatch{got: make(chan struct{}, 1)}
	registry.GetGlobalRegistry().SetHook(watch)
	t.Cleanup(func() { registry.GetGlobalRegistry().SetHook(nil) })

	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if err != nil {
		t.Fatal(err)
	}
	// What Run does first for config keys, before any reload.
	service.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(t.Context()), service.currentConfig())
	auths := service.coreManager.List()
	if len(auths) != 1 {
		t.Fatalf("auths %d", len(auths))
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auths[0].ID) })
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("registered at start: %v", got)
	}
	select { // the registry calls its hook on a goroutine
	case <-watch.got:
	case <-time.After(5 * time.Second):
		t.Fatal("the registry's hook saw no registration")
	}
	watch.mu.Lock()
	defer watch.mu.Unlock()
	if len(watch.seen) == 0 || slices.Contains(watch.seen, "held-at-start") {
		t.Fatalf("registered at any time: %v", watch.seen)
	}
}

// R11: a key's learned model and a login's learned model are held back like any other name, and
// served once kept; an excluded model never appears in the record.
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
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("key: %v", got)
	}
	if got := provimuxRegistered(login); len(got) != 0 {
		t.Fatalf("login: %v", got)
	}
	others := namesOthers(key.ID, login.ID)
	if others["learned-a"] == nil || others["learned-login"] == nil {
		t.Fatalf("learned models not in others: %v", others)
	}
	if others["secret-1"] != nil {
		t.Fatalf("an excluded model is in the record: %v", others)
	}
	namesReconfigure(svc, build("learned-a", "LEARNED-LOGIN"), all)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"learned-a", "mine"}) {
		t.Fatalf("key, kept: %v", got)
	}
	if got := provimuxRegistered(login); !slices.Equal(got, []string{"learned-login"}) {
		t.Fatalf("login, kept: %v", got)
	}
}

// C4: a registration computed under the old config and published after the new one is rejected by
// the fence; after the completion pass the account's record matches what it has registered.
func TestProvimuxOnlyMyNamesFenceAndCompletion(t *testing.T) {
	namesReset(t)
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{namesClaudeKey("", config.ClaudeModel{Name: "up", Alias: "mine"}, config.ClaudeModel{Name: "plain"})}}
	cfg.Models.OnlyMyNames = namesOn()
	svc, auths := namesService(t, cfg)
	key := auths[0]
	stale := newModelRegistrationFence(key, cfg)
	// A newer registration of the same account gets there first.
	kept := &config.Config{ClaudeKey: cfg.ClaudeKey}
	kept.Models.OnlyMyNames = namesOn("plain")
	namesReconfigure(svc, kept, auths)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"mine", "plain"}) {
		t.Fatalf("newer: %v", got)
	}
	if svc.registerResolvedModelsForAuthFenced(key, stale, "claude", []*ModelInfo{{ID: "stale-only"}}) {
		t.Fatal("the fence accepted a stale publication")
	}
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"mine", "plain"}) {
		t.Fatalf("after the rejected publication: %v", got)
	}
	// The record is one step behind until the completion pass.
	svc.registerModelsForAuth(t.Context(), key)
	snapshot := registry.ProvimuxNamesState([]string{key.ID})
	if len(snapshot.Others) != 0 {
		t.Fatalf("record after the completion pass: %+v", snapshot.Others)
	}
	if msg := registry.ProvimuxNotServedMessage("stale-only"); msg != "" {
		t.Fatalf("a stale record still holds a name: %q", msg)
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
	cfg.Models.OnlyMyNames = namesOn("keep-me", "C-ALIAS", "pat*", "")
	provimuxSetNamesRule(cfg)
	snapshot := registry.ProvimuxNamesState(nil)
	got := map[string][]string{}
	for _, name := range snapshot.Names {
		got[name.Name] = name.SetBy
	}
	want := map[string][]string{
		"g-alias": {"alias"}, "i-alias": {"alias"}, "c-alias": {"alias", "keep"}, "codex-alias": {"alias"},
		"xai-alias": {"alias"}, "meta-alias": {"alias"}, "vertex-alias": {"alias"}, "compat-alias": {"alias"},
		"login-alias": {"alias"}, "keep-me": {"keep"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names:\n got %v\nwant %v", got, want)
	}
	if len(snapshot.KeepIgnored) != 2 || snapshot.KeepIgnored[0].Reason != "pattern" || snapshot.KeepIgnored[1].Reason != "empty" {
		t.Fatalf("keep_ignored %+v", snapshot.KeepIgnored)
	}
	if !snapshot.Enabled || snapshot.Home {
		t.Fatalf("enabled %v home %v", snapshot.Enabled, snapshot.Home)
	}
	home := &config.Config{}
	home.Home.Enabled = true
	home.Models.OnlyMyNames = namesOn()
	provimuxSetNamesRule(home)
	if snapshot := registry.ProvimuxNamesState(nil); !snapshot.Home {
		t.Fatal("home mode is not recorded")
	}
}

// The reload clause: a reload forces every account through registration when the switch turns, or
// when it is on and the set of names changes; with it off on both sides, never.
func TestProvimuxOnlyMyNamesReloadClause(t *testing.T) {
	base := func(only *registry.OnlyMyNames, alias string) *config.Config {
		cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "c", Models: []config.ClaudeModel{{Name: "up", Alias: alias}}}}}
		cfg.Models.OnlyMyNames = only
		return cfg
	}
	for _, tc := range []struct {
		name     string
		old, new *config.Config
		want     bool
	}{
		{"absent both", base(nil, "a"), base(nil, "b"), false},
		{"off, alias changes", base(&registry.OnlyMyNames{}, "a"), base(&registry.OnlyMyNames{Keep: []string{"x"}}, "b"), false},
		{"turns on", base(nil, "a"), base(namesOn(), "a"), true},
		{"turns off", base(namesOn(), "a"), base(&registry.OnlyMyNames{}, "a"), true},
		{"on, alias changes", base(namesOn(), "a"), base(namesOn(), "b"), true},
		{"on, keep added", base(namesOn(), "a"), base(namesOn("k"), "a"), true},
		{"on, only case", base(namesOn("K "), "a"), base(namesOn("k"), "A"), false},
		{"on, a pattern added", base(namesOn(), "a"), base(namesOn("p*"), "a"), false},
	} {
		if got := config.ProvimuxOnlyMyNamesChanged(tc.old, tc.new); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// S5: GET /provimux/model-lists with the switch on. ?view=live (the vendor's list as it arrived) is
// byte-identical to the switch off; the fields that move are the ones read from the registry:
// registered_models, learned_models and the models rows.
func TestProvimuxOnlyMyNamesModelListsFields(t *testing.T) {
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
	row := func(body map[string]any) map[string]any {
		t.Helper()
		logins := body["logins"].([]any)
		if len(logins) != 1 {
			t.Fatalf("logins %v", logins)
		}
		return logins[0].(map[string]any)
	}
	namesReconfigure(svc, build(nil), auths)
	query := "?view=live&auth_index=" + auths[0].EnsureIndex()
	off := get(query)
	namesReconfigure(svc, build(namesOn(c1)), auths)
	on := get(query)

	offRow, onRow := row(off), row(on)
	if !reflect.DeepEqual(offRow["live_list"], onRow["live_list"]) || offRow["live_list"] == nil {
		t.Fatalf("live_list moved:\n off %v\n  on %v", offRow["live_list"], onRow["live_list"])
	}
	moved := []string{}
	for key := range offRow {
		if !reflect.DeepEqual(offRow[key], onRow[key]) {
			moved = append(moved, key)
		}
	}
	slices.Sort(moved)
	if !slices.Equal(moved, []string{"learned_models", "models", "registered_models"}) {
		t.Fatalf("fields that moved: %v\n off %v\n  on %v", moved, offRow, onRow)
	}
	delete(off, "logins")
	delete(on, "logins")
	if !reflect.DeepEqual(off, on) {
		t.Fatalf("the rest of the answer moved:\n off %v\n  on %v", off, on)
	}
	if offRow["registered_models"].(float64) <= onRow["registered_models"].(float64) || onRow["registered_models"].(float64) != 1 || onRow["learned_models"].(float64) != 0 {
		t.Fatalf("registered %v -> %v, learned %v -> %v", offRow["registered_models"], onRow["registered_models"], offRow["learned_models"], onRow["learned_models"])
	}
}
