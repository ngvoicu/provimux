package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"gopkg.in/yaml.v3"
)

// Key model lists (key-model-lists.md, section 10). Every service here is an httptest stand-in;
// the answers under testdata/provimux-keys are in each service's documented shape, none recorded.

func keyFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "provimux-keys", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// keyStandIn is a service whose answers the test sets per path, recording every request.
type keyStandIn struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	answer   func(w http.ResponseWriter, r *http.Request)
}

func newKeyStandIn(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *keyStandIn {
	t.Helper()
	stand := &keyStandIn{answer: answer}
	stand.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stand.mu.Lock()
		stand.requests = append(stand.requests, r.Clone(context.Background()))
		answer := stand.answer
		stand.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(stand.Close)
	return stand
}

func (s *keyStandIn) set(answer func(w http.ResponseWriter, r *http.Request)) {
	s.mu.Lock()
	s.answer = answer
	s.mu.Unlock()
}

func (s *keyStandIn) seen() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.requests...)
}

func keyServe(body []byte) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }
}

// keyService builds a service from cfg the way a start does: the config is sanitised, its keys are
// synthesized and registered, and every key type's real executor is bound (so the list request
// carries what a model request carries: the key, the entry's headers, the proxy).
func keyService(t *testing.T, cfg *config.Config) (*Service, []*coreauth.Auth) {
	t.Helper()
	cfg.SanitizeOpenAICompatibility()
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	svc.coreManager.RegisterExecutor(executor.NewClaudeExecutor(cfg))
	svc.coreManager.RegisterExecutor(executor.NewGeminiExecutor(cfg))
	svc.coreManager.RegisterExecutor(executor.NewXAIExecutor(cfg))
	svc.coreManager.RegisterExecutor(executor.NewCodexExecutor(cfg))
	for i := range cfg.OpenAICompatibility {
		name := strings.ToLower(strings.TrimSpace(cfg.OpenAICompatibility[i].Name))
		svc.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(util.OpenAICompatibleProviderKey(name), cfg))
	}
	auths := keySynthesize(t, cfg)
	var registered []*coreauth.Auth
	for _, auth := range auths {
		got, err := svc.coreManager.Register(t.Context(), auth)
		if err != nil {
			t.Fatal(err)
		}
		registered = append(registered, got)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(got.ID) })
	}
	return svc, registered
}

func keySynthesize(t *testing.T, cfg *config.Config) []*coreauth.Auth {
	t.Helper()
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	return auths
}

func keyRefresh(svc *Service, force bool) {
	svc.refreshLiveModelLists(context.Background(), force)
	liveModelLists.fetches.Wait()
}

func keyCompatConfig(base string, models ...config.OpenAICompatibilityModel) *config.Config {
	cfg := provimuxOn()
	cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: "acme", BaseURL: base,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-acme"}}, Models: models}}
	return cfg
}

// --- The merge ---------------------------------------------------------------------------------

func keyList(ids ...string) liveModelList {
	list := liveModelList{ids: map[string]struct{}{}, fetchedAt: time.Now()}
	for _, id := range ids {
		list.ids[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
		list.extra = append(list.extra, &ModelInfo{ID: id})
	}
	return list
}

func TestProvimuxKeyLearnedModels(t *testing.T) {
	rows := []config.OpenAICompatibilityModel{
		{Name: "anthropic/claude-opus-5.5(max)", Alias: "opus-max"}, // names the model, with a suffix
		{Name: "gpt-5.5-2026-03-01", Alias: "gpt-5.5"},              // takes the client name gpt-5.5
		{Name: "Mixed-Case"},
	}
	list := keyList("ANTHROPIC/claude-opus-5.5", "gpt-5.5", "mixed-case", "new-model", "", "think(high)", "New-Model", "other")
	list.extra[3].DisplayName = "New Model"
	var got []keyLearnedModel
	got = keyLearnedModels(rows, list)
	want := []keyLearnedModel{{ID: "new-model", DisplayName: "New Model"}, {ID: "other"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("learned = %+v, want %+v", got, want)
	}
	if unlisted := keyUnlistedRows(rows, keyList("gpt-5.5-2026-03-01")); !slices.Equal(unlisted, []string{"anthropic/claude-opus-5.5(max)", "Mixed-Case"}) {
		t.Fatalf("unlisted = %v", unlisted)
	}
}

func TestProvimuxParseOpenAIModelList(t *testing.T) {
	for name, body := range map[string]string{
		"data":          `{"object":"list","data":[{"id":"a","display_name":"Model A"},{"id":"b","display_name":"b"}]}`,
		"models":        `{"models":[{"name":"a","displayName":"Model A"},{"model":"b"}]}`,
		"bare array":    `[{"value":"a","display_name":"Model A"},"b"]`,
		"strings":       `{"data":["a","b","A"]}`,
		"skips empties": `{"data":[{"id":""},{"id":"a","display_name":"Model A"},{},"b",3]}`,
	} {
		list, err := parseOpenAIModelList([]byte(body), time.Unix(1, 0))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := provimuxSortedKeys(list.ids); !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("%s: ids = %v", name, got)
		}
		if len(list.extra) != 2 || list.extra[0].ID == "" || list.extra[1].DisplayName != "" {
			t.Fatalf("%s: extra = %+v %+v", name, list.extra[0], list.extra[1])
		}
		if name != "strings" && list.extra[0].DisplayName != "Model A" {
			t.Fatalf("%s: display name = %q", name, list.extra[0].DisplayName)
		}
	}
	for name, body := range map[string]string{"invalid": `{"data":`, "empty": `{"data":[]}`, "no list": `{"object":"list"}`} {
		_, err := parseOpenAIModelList([]byte(body), time.Now())
		if category, _ := provimuxLiveListFailureOf(err); err == nil || category != provimuxLiveFailureResponse {
			t.Fatalf("%s: err = %v (%s)", name, err, category)
		}
	}
}

// keyComparable drops the creation time, the only field two builds differ in.
func keyComparable(models []*ModelInfo) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		clone := *model
		clone.Created = 0
		out = append(out, clone)
	}
	return out
}

// What a key offers equals the builder's result for its rows followed by one bare row per learned
// id (key-model-lists.md 3.4), on OpenRouter with OpenRouter's values.
func TestProvimuxKeyMergeIsTheBuildersRows(t *testing.T) {
	provimuxLiveReset(t)
	restore := registry.SetOpenRouterModelsForTest(map[string]registry.OpenRouterModel{
		"openai/gpt-5.5":            {ContextLength: 400000, MaxCompletionTokens: 128000, ReasoningEfforts: []string{"high", "medium"}},
		"anthropic/claude-opus-5.5": {ContextLength: 1000000},
	})
	defer restore()
	cfg := provimuxOn()
	cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-or"}},
		Models:        []config.OpenAICompatibilityModel{{Name: "anthropic/claude-opus-5.5", Alias: "or-opus"}}}}
	cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-ant", Models: []config.ClaudeModel{{Name: "claude-opus-5-5", Alias: "opus"}}}}
	svc, auths := keyService(t, cfg)
	list := keyList("anthropic/claude-opus-5.5", "openai/gpt-5.5")
	list.extra[1].DisplayName = "GPT-5.5"
	for _, auth := range auths {
		liveModelLists.put(auth.ID, liveModelList{identity: liveModelListIdentity(auth), ids: list.ids, extra: list.extra, fetchedAt: liveModelListNow()})
	}
	compat, claude := auths[1], auths[0]
	if _, _, ok := openAICompatInfoFromAuth(compat); !ok {
		compat, claude = claude, compat
	}

	entry := cfg.OpenAICompatibility[0]
	got := svc.applyKeyModelList(cfg, compat, buildOpenAICompatibilityConfigModels(&entry))
	entry.Models = append(entry.Models, config.OpenAICompatibilityModel{Name: "openai/gpt-5.5", DisplayName: "GPT-5.5"})
	if want := buildOpenAICompatibilityConfigModels(&entry); !reflect.DeepEqual(keyComparable(got), keyComparable(want)) {
		t.Fatalf("openrouter:\n got %+v\nwant %+v", keyComparable(got), keyComparable(want))
	}
	if learned := got[1]; learned.ContextLength != 400000 || learned.OwnedBy != "openai" || learned.Thinking == nil {
		t.Fatalf("a learned OpenRouter model has OpenRouter's values: %+v", learned)
	}

	claudeEntry := cfg.ClaudeKey[0]
	gotClaude := svc.applyKeyModelList(cfg, claude, buildClaudeConfigModels(&claudeEntry))
	claudeEntry.Models = append(claudeEntry.Models, config.ClaudeModel{Name: "anthropic/claude-opus-5.5"},
		config.ClaudeModel{Name: "openai/gpt-5.5", DisplayName: "GPT-5.5"})
	if want := buildClaudeConfigModels(&claudeEntry); !reflect.DeepEqual(keyComparable(gotClaude), keyComparable(want)) {
		t.Fatalf("claude:\n got %+v\nwant %+v", keyComparable(gotClaude), keyComparable(want))
	}
}

// The OpenRouter example of key-model-lists.md 3.5, registered end to end.
func TestProvimuxKeyOpenRouterExample(t *testing.T) {
	provimuxLiveReset(t)
	stand := newKeyStandIn(t, keyServe(keyFixture(t, "openrouter-models.json")))
	cfg := keyCompatConfig(stand.URL+"/api/v1",
		config.OpenAICompatibilityModel{Name: "anthropic/claude-opus-5.5", Alias: "or-opus"},
		config.OpenAICompatibilityModel{Name: "acme/retired-model", Alias: "old"})
	cfg.OpenAICompatibility[0].ExcludedModels = []string{"x-ai/*"}
	svc, auths := keyService(t, cfg)
	key := auths[0]
	svc.registerModelsForAuth(t.Context(), key)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"old", "or-opus"}) {
		t.Fatalf("before the first list: %v", got)
	}
	keyRefresh(svc, false)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"old", "openai/gpt-5.5", "or-opus"}) {
		t.Fatalf("after the refresh: %v", got)
	}
	requests := stand.seen()
	if len(requests) != 1 || requests[0].URL.Path != "/api/v1/models" || requests[0].Header.Get("Authorization") != "Bearer sk-acme" {
		t.Fatalf("requests = %v", requests)
	}

	// fixed-models: true offers the rows, and the service is never asked again.
	fixed := keyCompatConfig(stand.URL+"/api/v1", cfg.OpenAICompatibility[0].Models...)
	fixed.OpenAICompatibility[0].FixedModels = true
	fixed.OpenAICompatibility[0].ExcludedModels = []string{"x-ai/*"}
	resetLiveModelLists()
	fixedSvc, fixedAuths := keyService(t, fixed)
	fixedSvc.registerModelsForAuth(t.Context(), fixedAuths[0])
	keyRefresh(fixedSvc, true)
	if got := provimuxRegistered(fixedAuths[0]); !slices.Equal(got, []string{"old", "or-opus"}) {
		t.Fatalf("fixed: %v", got)
	}
	if len(stand.seen()) != 1 {
		t.Fatalf("a fixed key was asked: %d requests", len(stand.seen()))
	}
}

func TestProvimuxKeyEmptyEntries(t *testing.T) {
	provimuxLiveReset(t)
	stand := newKeyStandIn(t, keyServe(keyFixture(t, "openai-models.json")))
	cfg := keyCompatConfig(stand.URL + "/v1")
	cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-ant", BaseURL: stand.URL}}
	svc, auths := keyService(t, cfg)
	for _, auth := range auths {
		svc.registerModelsForAuth(t.Context(), auth)
	}
	keyRefresh(svc, false)
	for _, auth := range auths {
		got := provimuxRegistered(auth)
		if _, _, compat := openAICompatInfoFromAuth(auth); compat {
			if !slices.Equal(got, []string{"gpt-5.5", "gpt-5.5-2026-03-01", "text-embedding-4-large"}) {
				t.Fatalf("an openai-compatibility entry without models offers the whole list: %v", got)
			}
			continue
		}
		// A built-in key without rows keeps the catalog and is never asked.
		var catalog []string
		for _, model := range registry.GetClaudeModels() {
			catalog = append(catalog, model.ID)
		}
		slices.Sort(catalog)
		if !slices.Equal(got, catalog) {
			t.Fatalf("claude key without rows: %v", got)
		}
	}
	if requests := stand.seen(); len(requests) != 1 || requests[0].URL.Path != "/v1/models" {
		t.Fatalf("requests = %d", len(requests))
	}
}

// Two keys of one entry hold their own lists; the registration cache's shared slice is never changed.
func TestProvimuxKeyRegistrationCacheIsNotShared(t *testing.T) {
	provimuxLiveReset(t)
	cfg := keyCompatConfig("https://models.invalid/v1", config.OpenAICompatibilityModel{Name: "row-model", Alias: "row"})
	cfg.OpenAICompatibility[0].APIKeyEntries = append(cfg.OpenAICompatibility[0].APIKeyEntries, config.OpenAICompatibilityAPIKey{APIKey: "sk-two"})
	cfg.OpenAICompatibility[0].ExcludedModels = []string{"hidden-*"}
	svc, auths := keyService(t, cfg)
	if len(auths) != 2 {
		t.Fatalf("auths = %d", len(auths))
	}
	liveModelLists.put(auths[0].ID, liveModelList{identity: liveModelListIdentity(auths[0]), ids: keyList("one-a", "hidden-1").ids,
		extra: keyList("one-a", "hidden-1").extra, fetchedAt: liveModelListNow()})
	liveModelLists.put(auths[1].ID, liveModelList{identity: liveModelListIdentity(auths[1]), ids: keyList("two-a").ids,
		extra: keyList("two-a").extra, fetchedAt: liveModelListNow()})
	cache := newOpenAICompatibilityRegistrationCacheForConfig(cfg)
	cached, _ := cache.lookup(auths[0], "acme")
	before := append([]*ModelInfo(nil), cached.models...)
	for _, auth := range auths {
		svc.registerModelsForAuthWithCache(t.Context(), auth, cache)
	}
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"one-a", "row"}) {
		t.Fatalf("first key: %v", got)
	}
	if got := provimuxRegistered(auths[1]); !slices.Equal(got, []string{"row", "two-a"}) {
		t.Fatalf("second key: %v", got)
	}
	if !reflect.DeepEqual(cached.models, before) || len(cached.models) != 1 {
		t.Fatalf("the cached slice changed: %+v", cached.models)
	}
	// Without the cache (the entry path) the result is the same.
	for _, auth := range auths {
		svc.registerModelsForAuth(t.Context(), auth)
	}
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"one-a", "row"}) {
		t.Fatalf("first key, entry path: %v", got)
	}
}

// --- excluded-models on openai-compatibility entries (6.1) ---------------------------------------

func TestProvimuxKeyCompatExcludedModels(t *testing.T) {
	provimuxLiveReset(t)
	rows := []config.OpenAICompatibilityModel{
		{Name: "vendor/alpha", Alias: "alpha-alias"}, // matched by its alias
		{Name: "beta-preview"},                       // matched by its name
		{Name: "vendor/keep", Alias: "keep"},
	}
	patterns := []string{" X-AI/* ", "*-preview", "alpha-alias", "x-ai/*", "gamma"}
	listed := []string{"vendor/alpha", "x-ai/grok-5", "gamma", "Delta"}

	compatCfg := keyCompatConfig("https://models.invalid/v1", rows...)
	compatCfg.OpenAICompatibility[0].ExcludedModels = patterns
	compatCfg.OpenAICompatibility[0].Prefix = "team"
	svc, auths := keyService(t, compatCfg)
	if got := compatCfg.OpenAICompatibility[0].ExcludedModels; !slices.Equal(got, []string{"x-ai/*", "*-preview", "alpha-alias", "gamma"}) {
		t.Fatalf("normalised = %v", got)
	}
	key := auths[0]
	if key.Attributes["excluded_models"] == "" || key.Attributes["excluded_models_hash"] == "" || key.Attributes["auth_kind"] != "" {
		t.Fatalf("credential attributes = %v", key.Attributes)
	}
	list := keyList(listed...)
	liveModelLists.put(key.ID, liveModelList{identity: liveModelListIdentity(key), ids: list.ids, extra: list.extra, fetchedAt: liveModelListNow()})
	svc.registerModelsForAuth(t.Context(), key)
	want := []string{"Delta", "keep", "team/Delta", "team/keep"}
	if got := provimuxRegistered(key); !slices.Equal(got, want) {
		t.Fatalf("compat = %v, want %v", got, want)
	}

	// The same list hides the same models on a Claude key with the same rows.
	claudeCfg := provimuxOn()
	claudeRows := make([]config.ClaudeModel, 0, len(rows))
	for _, row := range rows {
		claudeRows = append(claudeRows, config.ClaudeModel{Name: row.Name, Alias: row.Alias})
	}
	claudeCfg.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-ant", Models: claudeRows, ExcludedModels: config.NormalizeExcludedModels(patterns)}}
	claudeSvc, claudeAuths := keyService(t, claudeCfg)
	liveModelLists.put(claudeAuths[0].ID, liveModelList{identity: liveModelListIdentity(claudeAuths[0]), ids: list.ids, extra: list.extra, fetchedAt: liveModelListNow()})
	claudeSvc.registerModelsForAuth(t.Context(), claudeAuths[0])
	if got := provimuxRegistered(claudeAuths[0]); !slices.Equal(got, []string{"Delta", "keep"}) {
		t.Fatalf("claude = %v", got)
	}

	// Fixed, no list held, live lists off: the list still hides rows.
	for name, mutate := range map[string]func(*config.Config){
		"fixed":   func(c *config.Config) { c.OpenAICompatibility[0].FixedModels = true },
		"no list": func(*config.Config) { resetLiveModelLists() },
		"off":     func(c *config.Config) { c.Models.LiveLists = new(false) },
	} {
		cfg := keyCompatConfig("https://models.invalid/v1", rows...)
		cfg.OpenAICompatibility[0].ExcludedModels = patterns
		mutate(cfg)
		s, a := keyService(t, cfg)
		s.registerModelsForAuth(t.Context(), a[0])
		if got := provimuxRegistered(a[0]); !slices.Equal(got, []string{"keep"}) {
			t.Fatalf("%s: %v", name, got)
		}
	}
}

func TestProvimuxKeyCompatExcludesEverything(t *testing.T) {
	provimuxLiveReset(t)
	cfg := keyCompatConfig("https://models.invalid/v1", config.OpenAICompatibilityModel{Name: "a"})
	svc, auths := keyService(t, cfg)
	svc.registerModelsForAuth(t.Context(), auths[0])
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("before: %v", got)
	}
	cfg.OpenAICompatibility[0].ExcludedModels = []string{"*"}
	svc.registerModelsForAuth(t.Context(), auths[0])
	if got := provimuxRegistered(auths[0]); len(got) != 0 {
		t.Fatalf("a list that excludes everything leaves the key with nothing: %v", got)
	}
}

// An entry without the new fields synthesizes and registers exactly as before.
func TestProvimuxKeyCompatWithoutTheFieldsIsUnchanged(t *testing.T) {
	provimuxLiveReset(t)
	cfg := keyCompatConfig("https://models.invalid/v1", config.OpenAICompatibilityModel{Name: "a", Alias: "b"})
	cfg.Models.LiveLists = new(false)
	auths := keySynthesize(t, cfg)
	for _, attr := range []string{"excluded_models", "excluded_models_hash", "fixed_models", "auth_kind"} {
		if _, ok := auths[0].Attributes[attr]; ok {
			t.Fatalf("attribute %s written for an entry without the fields: %v", attr, auths[0].Attributes)
		}
	}
	svc, registered := keyService(t, cfg)
	svc.registerModelsForAuth(t.Context(), registered[0])
	entry := cfg.OpenAICompatibility[0]
	var want []string
	for _, model := range buildOpenAICompatibilityConfigModels(&entry) {
		want = append(want, model.ID)
	}
	if got := provimuxRegistered(registered[0]); !slices.Equal(got, want) {
		t.Fatalf("registered = %v, want %v", got, want)
	}
}

// A changed excluded-models or fixed-models changes the synthesized credential, so a config
// reload dispatches the key and registers it again; the reload log names both.
func TestProvimuxKeyReloadSeesBothFields(t *testing.T) {
	base := keyCompatConfig("https://models.invalid/v1", config.OpenAICompatibilityModel{Name: "a"})
	base.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-ant", Models: []config.ClaudeModel{{Name: "c"}}}}
	base.GeminiKey = []config.GeminiKey{{APIKey: "AIza", Models: []config.GeminiModel{{Name: "g"}}}}
	base.XAIKey = []config.XAIKey{{APIKey: "xai", BaseURL: "https://api.x.ai/v1", Models: []config.CodexModel{{Name: "x"}}}}
	before := keySynthesize(t, base)
	changed := keyCompatConfig("https://models.invalid/v1", config.OpenAICompatibilityModel{Name: "a"})
	changed.OpenAICompatibility[0].FixedModels = true
	changed.OpenAICompatibility[0].ExcludedModels = []string{"b"}
	changed.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-ant", Models: []config.ClaudeModel{{Name: "c"}}, FixedModels: true}}
	changed.GeminiKey = []config.GeminiKey{{APIKey: "AIza", Models: []config.GeminiModel{{Name: "g"}}, FixedModels: true}}
	changed.XAIKey = []config.XAIKey{{APIKey: "xai", BaseURL: "https://api.x.ai/v1", Models: []config.CodexModel{{Name: "x"}}, FixedModels: true}}
	after := keySynthesize(t, changed)
	if len(before) != 4 || len(after) != 4 {
		t.Fatalf("auths = %d, %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || reflect.DeepEqual(before[i].Attributes, after[i].Attributes) {
			t.Fatalf("%s: the credential did not change: %v", before[i].Provider, after[i].Attributes)
		}
		if after[i].Attributes["fixed_models"] != "true" {
			t.Fatalf("%s: %v", after[i].Provider, after[i].Attributes)
		}
	}
	lines := strings.Join(diff.DiffOpenAICompatibility(base.OpenAICompatibility, changed.OpenAICompatibility), "\n")
	if !strings.Contains(lines, "fixed-models false -> true") || !strings.Contains(lines, "excluded-models updated (0 -> 1 entries)") {
		t.Fatalf("reload log = %q", lines)
	}
}

// --- The fetch, per key type ---------------------------------------------------------------------

func TestProvimuxKeyListURLs(t *testing.T) {
	for _, tc := range []struct{ kind, base, want string }{
		{keyKindCompat, "https://h.example/v1", "https://h.example/v1/models"},
		{keyKindCompat, "https://h.example/v1/", "https://h.example/v1/models"},
		{keyKindCompat, "https://h.example/v1/models", "https://h.example/v1/models"},
		{keyKindClaude, "", "https://api.anthropic.com/v1/models?limit=1000"},
		{keyKindClaude, "https://gw.example/v1", "https://gw.example/v1/models?limit=1000"},
		{keyKindClaude, "https://gw.example", "https://gw.example/v1/models?limit=1000"},
		{keyKindCodex, "https://api.openai.com/v1", "https://api.openai.com/v1/models"},
		{keyKindCodex, "https://gw.example", "https://gw.example/v1/models"},
		{keyKindXAI, "https://api.x.ai/v1", "https://api.x.ai/v1/models"},
		{keyKindMeta, "", "https://api.meta.ai/v1/models"},
		{keyKindMeta, "https://gw.example/", "https://gw.example/v1/models"},
		{keyKindGemini, "https://gw.example", "https://gw.example/v1beta/models"},
		{keyKindGemini, "https://gw.example/v1beta", "https://gw.example/v1beta/models"},
		{keyKindGemini, "", ""},
		{keyKindCompat, "", ""},
		{"vertex", "https://gw.example", ""},
	} {
		auth := &coreauth.Auth{Attributes: map[string]string{"base_url": tc.base}}
		if got := keyModelListURL(tc.kind, auth); got != tc.want {
			t.Errorf("%s %q: %q, want %q", tc.kind, tc.base, got, tc.want)
		}
	}
}

func TestProvimuxKeyCompatRequest(t *testing.T) {
	provimuxLiveReset(t)
	var refuseKey atomic.Bool
	stand := newKeyStandIn(t, nil)
	stand.set(func(w http.ResponseWriter, r *http.Request) {
		if refuseKey.Load() && r.Header.Get("Authorization") != "" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(keyFixture(t, "openai-models.json"))
	})
	cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "gpt-5.5"})
	cfg.OpenAICompatibility[0].Headers = map[string]string{"X-Team": "blue"}
	svc, auths := keyService(t, cfg)
	key := auths[0]
	if _, err := svc.fetchLiveModelList(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	first := stand.seen()[0]
	if first.URL.Path != "/v1/models" || first.Header.Get("Authorization") != "Bearer sk-acme" || first.Header.Get("X-Team") != "blue" {
		t.Fatalf("request = %s %v", first.URL, first.Header)
	}

	// 401 on the key: once more without the key and the entry's headers.
	refuseKey.Store(true)
	list, err := svc.fetchLiveModelList(t.Context(), key)
	if err != nil || len(list.ids) != 3 {
		t.Fatalf("open retry: %v %v", err, list.ids)
	}
	requests := stand.seen()
	if len(requests) != 3 || requests[2].Header.Get("Authorization") != "" || requests[2].Header.Get("X-Team") != "" {
		t.Fatalf("retry = %d requests, last headers %v", len(requests), requests[len(requests)-1].Header)
	}

	// 500 and 404 get no retry.
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		count := len(stand.seen())
		stand.set(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", status) })
		_, err := svc.fetchLiveModelList(t.Context(), key)
		if category, got := provimuxLiveListFailureOf(err); category != provimuxLiveFailureHTTP || got != status || len(stand.seen()) != count+1 {
			t.Fatalf("%d: %v (%s %d), %d requests", status, err, category, got, len(stand.seen())-count)
		}
	}
}

// The request goes through the key's proxy.
func TestProvimuxKeyRequestUsesTheKeysProxy(t *testing.T) {
	provimuxLiveReset(t)
	var proxied atomic.Value
	proxy := newKeyStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(r.URL.String())
		_, _ = w.Write(keyFixture(t, "openai-models.json"))
	})
	cfg := keyCompatConfig("http://models.invalid/v1", config.OpenAICompatibilityModel{Name: "gpt-5.5"})
	cfg.OpenAICompatibility[0].APIKeyEntries[0].ProxyURL = proxy.URL
	svc, auths := keyService(t, cfg)
	if _, err := svc.fetchLiveModelList(t.Context(), auths[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := proxied.Load().(string); got != "http://models.invalid/v1/models" {
		t.Fatalf("proxy saw %q", got)
	}
}

func TestProvimuxKeyFailuresKeepTheHeldList(t *testing.T) {
	provimuxLiveReset(t)
	previous := liveModelListFetchTimeout
	liveModelListFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { liveModelListFetchTimeout = previous })
	stand := newKeyStandIn(t, keyServe(keyFixture(t, "openai-models.json")))
	cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "own-row"})
	svc, auths := keyService(t, cfg)
	key := auths[0]

	// With no list held, a failure leaves the rows.
	stand.set(func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	svc.registerModelsForAuth(t.Context(), key)
	keyRefresh(svc, true)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"own-row"}) {
		t.Fatalf("no list: %v", got)
	}
	stand.set(keyServe(keyFixture(t, "openai-models.json")))
	keyRefresh(svc, true)
	held := provimuxRegistered(key)
	if len(held) != 4 {
		t.Fatalf("held = %v", held)
	}
	big := make([]byte, 9<<20)
	for i := range big {
		big[i] = ' '
	}
	for name, tc := range map[string]struct {
		answer   func(http.ResponseWriter, *http.Request)
		category string
	}{
		"404":      {func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }, provimuxLiveFailureHTTP},
		"500":      {func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", 500) }, provimuxLiveFailureHTTP},
		"invalid":  {keyServe([]byte(`{"data":[`)), provimuxLiveFailureResponse},
		"empty":    {keyServe([]byte(`{"data":[]}`)), provimuxLiveFailureResponse},
		"9 MB":     {keyServe(append(append([]byte(`{"data":["a"]}`), big...), '\n')), provimuxLiveFailureResponse},
		"deadline": {func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, provimuxLiveFailureNetwork},
	} {
		stand.set(tc.answer)
		keyRefresh(svc, true)
		if got := provimuxRegistered(key); !slices.Equal(got, held) {
			t.Fatalf("%s: the held list changed: %v", name, got)
		}
		liveModelLists.mu.Lock()
		provimuxLiveListNotes.mu.Lock()
		note := provimuxLiveListNotes.notes[key.ID]
		provimuxLiveListNotes.mu.Unlock()
		liveModelLists.mu.Unlock()
		if note.category != tc.category {
			t.Fatalf("%s: category %q, want %q", name, note.category, tc.category)
		}
	}
}

func TestProvimuxKeyClaudeRequest(t *testing.T) {
	provimuxLiveReset(t)
	var more atomic.Bool
	stand := newKeyStandIn(t, nil)
	stand.set(func(w http.ResponseWriter, r *http.Request) {
		if more.Load() {
			_, _ = w.Write([]byte(`{"data":[{"id":"a"}],"has_more":true}`))
			return
		}
		_, _ = w.Write(keyFixture(t, "claude-models.json"))
	})
	cfg := provimuxOn()
	cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "sk-gw", BaseURL: stand.URL + "/v1", Headers: map[string]string{"X-Gw": "1"},
		Models: []config.ClaudeModel{{Name: "claude-opus-5-5", Alias: "opus"}}}}
	svc, auths := keyService(t, cfg)
	key := auths[0]
	svc.registerModelsForAuth(t.Context(), key)
	keyRefresh(svc, false)
	request := stand.seen()[0]
	if request.URL.Path != "/v1/models" || request.URL.Query().Get("limit") != "1000" || request.Header.Get("Anthropic-Version") != "2023-06-01" ||
		request.Header.Get("X-Gw") != "1" || (request.Header.Get("Authorization") == "" && request.Header.Get("X-Api-Key") == "") {
		t.Fatalf("request = %s %v", request.URL, request.Header)
	}
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"claude-gateway-only-1", "opus"}) {
		t.Fatalf("registered = %v", got)
	}
	// A gateway's list never teaches the catalog.
	if registry.LookupStaticModelInfoByChannel("claude-gateway-only-1", "claude") != nil {
		t.Fatal("a key's list fed the catalog's learned models")
	}
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(key.ID) {
		if model.ID == "claude-gateway-only-1" && model.DisplayName != "Gateway Only 1" {
			t.Fatalf("display name = %q", model.DisplayName)
		}
	}
	more.Store(true)
	if _, err := svc.fetchLiveModelList(t.Context(), key); err == nil {
		t.Fatal("has_more is an incomplete list")
	}
}

func TestProvimuxKeyXAIRequest(t *testing.T) {
	provimuxLiveReset(t)
	stand := newKeyStandIn(t, keyServe([]byte(`{"data":[{"id":"grok-5"},{"id":"grok-row"}]}`)))
	cfg := provimuxOn()
	cfg.XAIKey = []config.XAIKey{{APIKey: "xai-key", BaseURL: stand.URL + "/v1", Models: []config.CodexModel{{Name: "grok-row"}}}}
	svc, auths := keyService(t, cfg)
	svc.registerModelsForAuth(t.Context(), auths[0])
	keyRefresh(svc, false)
	request := stand.seen()[0]
	if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer xai-key" {
		t.Fatalf("request = %s %v", request.URL, request.Header)
	}
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"grok-5", "grok-row"}) {
		t.Fatalf("registered = %v", got)
	}
}

func TestProvimuxKeyGeminiOwnHost(t *testing.T) {
	provimuxLiveReset(t)
	var endless atomic.Bool
	stand := newKeyStandIn(t, nil)
	stand.set(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" || r.Header.Get("X-Goog-Api-Key") != "AIza-gw" || r.URL.Query().Get("pageSize") != "1000" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if endless.Load() {
			_, _ = w.Write([]byte(`{"models":[{"name":"models/x"}],"nextPageToken":"again"}`))
			return
		}
		switch r.URL.Query().Get("pageToken") {
		case "":
			_, _ = w.Write(keyFixture(t, "gemini-key-page1.json"))
		case "p2":
			_, _ = w.Write(keyFixture(t, "gemini-key-page2.json"))
		default:
			_, _ = w.Write(keyFixture(t, "gemini-key-page3.json"))
		}
	})
	cfg := provimuxOn()
	cfg.GeminiKey = []config.GeminiKey{{APIKey: "AIza-gw", BaseURL: stand.URL, Models: []config.GeminiModel{{Name: "gemini-4-pro", Alias: "pro"}}}}
	svc, auths := keyService(t, cfg)
	key := auths[0]
	if kind, _ := keyModelListKind(cfg, key); kind != keyKindGemini {
		t.Fatalf("kind = %q", kind)
	}
	svc.registerModelsForAuth(t.Context(), key)
	keyRefresh(svc, false)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"gateway-flash", "gemini-4-flash", "pro"}) {
		t.Fatalf("registered = %v", got)
	}
	if len(stand.seen()) != 3 {
		t.Fatalf("pages = %d", len(stand.seen()))
	}
	endless.Store(true)
	if _, err := svc.fetchLiveModelList(t.Context(), key); !errors.Is(err, errLiveModelListIncomplete) {
		t.Fatalf("a list that never ends: %v", err)
	}
}

// A Gemini key on Google's host with rows: the list its key already fetches is added to the rows.
func TestProvimuxKeyGeminiGoogleHostWithRows(t *testing.T) {
	provimuxLiveReset(t)
	cfg := provimuxOn()
	cfg.GeminiKey = []config.GeminiKey{{APIKey: "AIza-google", Models: []config.GeminiModel{{Name: "gemini-4-pro", Alias: "pro"}}}}
	svc, auths := keyService(t, cfg)
	key := auths[0]
	if kind, _ := keyModelListKind(cfg, key); kind != "gemini" {
		t.Fatalf("kind = %q", kind)
	}
	list := keyList("gemini-4-pro", "gemini-4-flash")
	liveModelLists.put(key.ID, liveModelList{identity: liveModelListIdentity(key), provider: "gemini", ids: list.ids, extra: list.extra, fetchedAt: liveModelListNow()})
	svc.registerModelsForAuth(t.Context(), key)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"gemini-4-flash", "pro"}) {
		t.Fatalf("registered = %v", got)
	}
	cfg.GeminiKey[0].FixedModels = true
	if liveModelListKindFor(cfg, key) != "" {
		t.Fatal("fixed-models turns the key's live list off too")
	}
	svc.registerModelsForAuth(t.Context(), key)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"pro"}) {
		t.Fatalf("fixed: %v", got)
	}
}

// --- The lifecycle -------------------------------------------------------------------------------

func TestProvimuxKeyLifecycle(t *testing.T) {
	provimuxLiveReset(t)
	stand := newKeyStandIn(t, keyServe([]byte(`{"data":["row-model","learned-1","learned-2"]}`)))
	cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "row-model", Alias: "row"})
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	written, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(configPath, written, 0o600); err != nil {
		t.Fatal(err)
	}
	svc, auths := keyService(t, cfg)
	svc.configPath = configPath
	key := auths[0]
	snapshot, _ := yaml.Marshal(cfg)
	svc.registerModelsForAuth(t.Context(), key)
	keyRefresh(svc, false)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"learned-1", "learned-2", "row"}) {
		t.Fatalf("refreshed: %v", got)
	}

	// The service drops a learned model and the row's model: the learned one goes, the row stays.
	stand.set(keyServe([]byte(`{"data":["learned-1"]}`)))
	keyRefresh(svc, true)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"learned-1", "row"}) {
		t.Fatalf("after the drop: %v", got)
	}
	// Nothing reached the config.
	if after, _ := yaml.Marshal(cfg); string(after) != string(snapshot) {
		t.Fatalf("the config changed:\n%s", after)
	}
	if onDisk, _ := os.ReadFile(configPath); string(onDisk) != string(written) {
		t.Fatal("config.yaml was written")
	}

	// A restart without network offers the last run's models before any request.
	resetLiveModelLists()
	liveModelLists.mu.Lock()
	liveModelLists.cacheTried = map[string]string{}
	liveModelLists.mu.Unlock()
	stand.Close()
	restarted, restartedAuths := keyService(t, cfg)
	restarted.configPath = configPath
	restarted.registerModelsForAuth(t.Context(), restartedAuths[0])
	if got := provimuxRegistered(restartedAuths[0]); !slices.Equal(got, []string{"learned-1", "row"}) {
		t.Fatalf("after the restart: %v", got)
	}

	// Seven days without a refresh: the rows alone.
	now := liveModelListNow()
	liveModelListNow = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	keyRefresh(restarted, false)
	restarted.registerModelsForAuth(t.Context(), restartedAuths[0])
	if got := provimuxRegistered(restartedAuths[0]); !slices.Equal(got, []string{"row"}) {
		t.Fatalf("after 7 days: %v", got)
	}
}

func TestProvimuxKeyNotAsked(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config){
		"fixed":     func(c *config.Config) { c.OpenAICompatibility[0].FixedModels = true },
		"live off":  func(c *config.Config) { c.Models.LiveLists = new(false) },
		"home mode": func(c *config.Config) { c.Home.Enabled = true },
		"asked":     nil,
	} {
		provimuxLiveReset(t)
		stand := newKeyStandIn(t, keyServe([]byte(`{"data":["x"]}`)))
		cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "row"})
		if mutate != nil {
			mutate(cfg)
		}
		svc, auths := keyService(t, cfg)
		svc.registerModelsForAuth(t.Context(), auths[0])
		keyRefresh(svc, true)
		got := provimuxRegistered(auths[0])
		if mutate == nil {
			if !slices.Equal(got, []string{"row", "x"}) {
				t.Fatalf("%s: %v", name, got)
			}
			continue
		}
		if len(stand.seen()) != 0 || !slices.Equal(got, []string{"row"}) {
			t.Fatalf("%s: %d requests, registered %v", name, len(stand.seen()), got)
		}
	}
}

// fixed-models switched in a running instance applies at the next registration, both ways.
func TestProvimuxKeyFixedSwitchedWhileRunning(t *testing.T) {
	provimuxLiveReset(t)
	stand := newKeyStandIn(t, keyServe([]byte(`{"data":["row","learned"]}`)))
	cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "row"})
	svc, auths := keyService(t, cfg)
	key := auths[0]
	svc.registerModelsForAuth(t.Context(), key)
	keyRefresh(svc, false)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"learned", "row"}) {
		t.Fatalf("on: %v", got)
	}
	fixed := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "row"})
	fixed.OpenAICompatibility[0].FixedModels = true
	svc.cfgMu.Lock()
	svc.cfg = fixed
	svc.cfgMu.Unlock()
	svc.registerModelsForAuth(t.Context(), key)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"row"}) {
		t.Fatalf("fixed: %v", got)
	}
	keyRefresh(svc, true)
	if len(stand.seen()) != 1 {
		t.Fatalf("a fixed key was asked")
	}
	svc.cfgMu.Lock()
	svc.cfg = cfg
	svc.cfgMu.Unlock()
	svc.registerModelsForAuth(t.Context(), key)
	keyRefresh(svc, false)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"learned", "row"}) {
		t.Fatalf("unfixed: %v", got)
	}

	// Turning live lists off registers the key again with its rows.
	off := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "row"})
	off.Models.LiveLists = new(false)
	svc.cfgMu.Lock()
	svc.cfg = off
	svc.cfgMu.Unlock()
	keyRefresh(svc, false)
	if got := provimuxRegistered(key); !slices.Equal(got, []string{"row"}) {
		t.Fatalf("off: %v", got)
	}
}

// Registration never waits for a fetch.
func TestProvimuxKeyRegistrationNeverWaits(t *testing.T) {
	provimuxLiveReset(t)
	release := make(chan struct{})
	stand := newKeyStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"data":["x"]}`))
	})
	cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "row"})
	svc, auths := keyService(t, cfg)
	t.Cleanup(liveModelLists.fetches.Wait)
	t.Cleanup(func() { close(release) })
	svc.refreshLiveModelLists(context.Background(), true) // the fetch hangs in its goroutine
	started := time.Now()
	svc.registerModelsForAuth(t.Context(), auths[0])
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("registration waited %s", elapsed)
	}
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"row"}) {
		t.Fatalf("registered = %v", got)
	}
}

// A request for a learned model reaches the service under the service's id; a row's alias under the
// row's name. No routing code knows learned models: a name no row names passes through.
func TestProvimuxKeyRequestsReachTheService(t *testing.T) {
	provimuxLiveReset(t)
	var mu sync.Mutex
	var sent []string
	stand := newKeyStandIn(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":["upstream-row","learned-x"]}`))
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, r.URL.Path+" "+body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"` + body.Model +
			`","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	})
	cfg := keyCompatConfig(stand.URL+"/v1", config.OpenAICompatibilityModel{Name: "upstream-row", Alias: "row"})
	svc, auths := keyService(t, cfg)
	svc.coreManager.SetConfig(cfg)
	svc.coreManager.RefreshAPIKeyModelAlias()
	svc.registerModelsForAuth(t.Context(), auths[0])
	keyRefresh(svc, false)
	if got := provimuxRegistered(auths[0]); !slices.Equal(got, []string{"learned-x", "row"}) {
		t.Fatalf("registered = %v", got)
	}
	providerKey := keyModelListExecutorKey(auths[0])
	for _, model := range []string{"learned-x", "row"} {
		payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
		_, err := svc.coreManager.Execute(t.Context(), []string{providerKey},
			cliproxyexecutor.Request{Model: model, Payload: payload, Format: sdktranslator.FormatOpenAI},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(sent, []string{"/v1/chat/completions learned-x", "/v1/chat/completions upstream-row"}) {
		t.Fatalf("the service received %v", sent)
	}
}
