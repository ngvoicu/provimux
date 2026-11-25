package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"google.golang.org/protobuf/encoding/protowire"
)

// The answers under testdata/provimux-live are fixtures in the shape each vendor documents or its
// own client reads (see each fetcher's comment); none was recorded with a real login.

func provimuxFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "provimux-live", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func provimuxLiveReset(t *testing.T) {
	t.Helper()
	resetLiveModelLists()
	liveModelLists.mu.Lock()
	liveModelLists.cacheTried = make(map[string]string)
	liveModelLists.expiredNoted = make(map[string]time.Time)
	liveModelLists.mu.Unlock()
	previousCache := liveModelLists.cache.Swap(nil)
	previousNow := liveModelListNow
	t.Cleanup(func() {
		resetLiveModelLists()
		liveModelLists.cache.Store(previousCache)
		liveModelListNow = previousNow
		for _, section := range []string{"claude", "codex", "meta", "devin", "kimi", "xai", "gemini", "copilot", "antigravity"} {
			registry.SetProvimuxLearnedModels(section, nil)
		}
		provimuxCopilotLists.mu.Lock()
		provimuxCopilotLists.byAuth = map[string]provimuxCopilotList{}
		provimuxCopilotLists.mu.Unlock()
		provimuxAntigravityLists.mu.Lock()
		provimuxAntigravityLists.byAuth = map[string]provimuxAntigravityList{}
		provimuxAntigravityLists.mu.Unlock()
	})
}

// provimuxAsk turns on the internal switch of login types that are off by default (Kimi, xAI),
// so the code kept behind it stays tested.
func provimuxAsk(t *testing.T, providers ...string) {
	t.Helper()
	previous := make(map[string]bool, len(providers))
	for _, provider := range providers {
		previous[provider] = liveModelListAsked[provider]
		liveModelListAsked[provider] = true
	}
	t.Cleanup(func() {
		for provider, value := range previous {
			liveModelListAsked[provider] = value
		}
	})
}

func provimuxOn() *config.Config {
	cfg := &config.Config{}
	cfg.Models.LiveLists = new(true)
	return cfg
}

// provimuxRewriteExecutor signs like a real executor and sends every request to the test server,
// keeping path and query, so fixed vendor hosts (Gemini) can be served locally.
type provimuxRewriteExecutor struct {
	coreauth.ProviderExecutor
	provider string
	server   *url.URL
	calls    atomic.Int32
	paths    []string
}

func (e *provimuxRewriteExecutor) Identifier() string { return e.provider }

func (e *provimuxRewriteExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *provimuxRewriteExecutor) HttpRequest(ctx context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	e.paths = append(e.paths, req.URL.Host+req.URL.Path+"?"+req.URL.RawQuery)
	target := *req.URL
	target.Scheme, target.Host = e.server.Scheme, e.server.Host
	out := req.Clone(ctx)
	out.URL = &target
	out.Host = ""
	out.Header.Set("Authorization", "Bearer test")
	return http.DefaultClient.Do(out)
}

func provimuxServe(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := routes[r.URL.Path]; ok {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func provimuxBody(body []byte) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }
}

func provimuxService(t *testing.T, cfg *config.Config, executor coreauth.ProviderExecutor, auth *coreauth.Auth) (*Service, *coreauth.Auth) {
	t.Helper()
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	svc.coreManager.RegisterExecutor(executor)
	registered, err := svc.coreManager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(registered.ID) })
	return svc, registered
}

func provimuxRefresh(svc *Service, executor coreauth.ProviderExecutor, force bool) {
	svc.coreManager.RegisterExecutor(executor)
	svc.refreshLiveModelLists(context.Background(), force)
	liveModelLists.fetches.Wait()
}

func provimuxRegistered(auth *coreauth.Auth) []string {
	var ids []string
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		ids = append(ids, model.ID)
	}
	slices.Sort(ids)
	return ids
}

// --- Devin -------------------------------------------------------------------------------------

type provimuxDevinVariant struct {
	id, label       string
	context, output int
	multimodal      bool
	vendor          uint64
}

func provimuxDevinAnswer(variants ...provimuxDevinVariant) []byte {
	var out []byte
	for _, v := range variants {
		var m []byte
		m = protowire.AppendTag(m, 1, protowire.BytesType)
		m = protowire.AppendString(m, v.label)
		if v.multimodal {
			m = protowire.AppendTag(m, 5, protowire.VarintType)
			m = protowire.AppendVarint(m, 1)
		}
		m = protowire.AppendTag(m, 10, protowire.VarintType)
		m = protowire.AppendVarint(m, v.vendor)
		m = protowire.AppendTag(m, 18, protowire.VarintType)
		m = protowire.AppendVarint(m, uint64(v.context))
		m = protowire.AppendTag(m, 22, protowire.BytesType)
		m = protowire.AppendString(m, v.id)
		// an unknown field the parser must skip
		m = protowire.AppendTag(m, 40, protowire.BytesType)
		m = protowire.AppendString(m, "ignored")
		if v.output > 0 {
			var config []byte
			config = protowire.AppendTag(config, 13, protowire.VarintType)
			config = protowire.AppendVarint(config, uint64(v.output))
			m = protowire.AppendTag(m, 23, protowire.BytesType)
			m = protowire.AppendBytes(m, config)
		}
		out = protowire.AppendTag(out, 1, protowire.BytesType)
		out = protowire.AppendBytes(out, m)
	}
	return out
}

// provimuxDevinRecorded is a recorded Devin answer with one catalog model (swe-2) and two models the
// catalog lacks. The learned ones are named provimux-test-*, ids no vendor's catalog holds: a fixture
// that borrowed a plausible name (gemini-4-neon) broke when upstream's 8.1.4 catalog added that model.
var provimuxDevinRecorded = provimuxDevinAnswer(
	provimuxDevinVariant{id: "swe-2-medium", label: "SWE-2 Medium", context: 262000, output: 128000, multimodal: true, vendor: 1},
	provimuxDevinVariant{id: "swe-2-high", label: "SWE-2 High", context: 262000, output: 128000, multimodal: true, vendor: 1},
	provimuxDevinVariant{id: "provimux-test-learned-low", label: "Provimux Test Learned Low", context: 1048576, output: 65536, multimodal: true, vendor: 4},
	provimuxDevinVariant{id: "provimux-test-learned-medium", label: "Provimux Test Learned Medium", context: 1048576, output: 65536, multimodal: true, vendor: 4},
	provimuxDevinVariant{id: "provimux-test-learned-high", label: "Provimux Test Learned High", context: 1048576, output: 65536, multimodal: true, vendor: 4},
	provimuxDevinVariant{id: "provimux-test-learned-high", label: "duplicate", context: 1, vendor: 4},
	provimuxDevinVariant{id: "provimux-test-unstated", label: "Provimux Test Unstated", context: 262144, vendor: 7},
)

// The Devin tests' fixture holds only if upstream's catalog has swe-2 and none of the test ids; this
// says so in one place when a catalog update breaks that, instead of as six unrelated failures.
func TestProvimuxDevinFixtureAgainstTheCatalog(t *testing.T) {
	provimuxLiveReset(t) // no learned model of an earlier test
	if registry.LookupDevinModel("swe-2") == nil {
		t.Fatal("the fixture's catalog model swe-2 is no longer in upstream's Devin catalog")
	}
	for _, id := range []string{"provimux-test-learned", "provimux-test-learned-high", "provimux-test-unstated"} {
		if model := registry.LookupDevinModel(id); model != nil {
			t.Fatalf("the fixture's learned model %s is in the catalog: %+v", id, model)
		}
	}
}

// provimuxFixtureAgainstTheCatalog states, in one place per provider, what a fixture assumes of upstream's
// embedded catalog of a section: each id of inCatalog is a model of that catalog (or one Provimux adds in
// code), each id of learned is not. A learned id is always provimux-test-…, a name no vendor ships, so a
// catalog update can only break the first list, and then here, with the fixture named, instead of in the
// tests that use it. data, when given, is the fixture, which must name every id.
func provimuxFixtureAgainstTheCatalog(t *testing.T, section, fixture string, data []byte, inCatalog, learned []string) {
	t.Helper()
	provimuxLiveReset(t) // no learned model of an earlier test
	catalog := provimuxCatalogIDs(section)
	builtins := liveModelListBuiltins(section)
	known := func(id string) bool {
		_, inSection := catalog[strings.ToLower(id)]
		_, builtin := builtins[strings.ToLower(id)]
		return inSection || builtin
	}
	if len(catalog) == 0 {
		t.Fatalf("upstream's %s catalog is empty", section)
	}
	for _, id := range append(append([]string(nil), inCatalog...), learned...) {
		if data != nil && !strings.Contains(string(data), `"`+id+`"`) && !strings.Contains(string(data), `/`+id+`"`) { // Gemini: "models/<id>"
			t.Errorf("%s does not name %s", fixture, id)
		}
	}
	for _, id := range inCatalog {
		if !known(id) {
			t.Errorf("%s: its catalog model %s is no longer in upstream's %s catalog; give the fixture a model the catalog has", fixture, id, section)
		}
	}
	for _, id := range learned {
		if !strings.HasPrefix(id, "provimux-test-") {
			t.Errorf("%s: its learned model %s must be named provimux-test-…, an id no vendor ships", fixture, id)
		}
		if known(id) {
			t.Errorf("%s: its learned model %s is in upstream's %s catalog", fixture, id, section)
		}
	}
}

func TestProvimuxDevinParser(t *testing.T) {
	list, err := parseDevinLiveModelList(provimuxDevinRecorded, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(provimuxSortedKeys(list.ids), ","); got != "provimux-test-learned,provimux-test-unstated,swe-2" {
		t.Fatalf("ids = %s", got)
	}
	neon := hasLiveID(list.extra, "provimux-test-learned")
	if neon == nil || neon.ContextLength != 1048576 || neon.MaxCompletionTokens != 65536 || neon.OwnedBy != "google" ||
		neon.Thinking == nil || strings.Join(neon.Thinking.Levels, ",") != "low,medium,high" ||
		strings.Join(neon.SupportedInputModalities, ",") != "text,image" || neon.DisplayName != "Provimux Test Learned" {
		t.Fatalf("neon = %+v thinking=%+v", neon, neon.Thinking)
	}
	if k9 := hasLiveID(list.extra, "provimux-test-unstated"); k9 == nil || k9.MaxCompletionTokens != 0 || k9.Thinking != nil {
		t.Fatalf("no output limit is assumed where the answer states none: %+v", k9)
	}
	if _, errEmpty := parseDevinLiveModelList(nil, time.Now()); errEmpty == nil {
		t.Fatal("an empty answer must fail")
	}
	if _, errCut := parseDevinLiveModelList(provimuxDevinRecorded[:len(provimuxDevinRecorded)-7], time.Now()); errCut == nil {
		t.Fatal("a cut-off answer must fail as a whole")
	}
	// Devin's answer has no pages; a second top-level message field is skipped like any unknown field.
	extra := protowire.AppendTag(append([]byte(nil), provimuxDevinRecorded...), 2, protowire.BytesType)
	extra = protowire.AppendString(extra, "next-page-token-is-not-a-thing")
	if again, errAgain := parseDevinLiveModelList(extra, time.Now()); errAgain != nil || len(again.ids) != 3 {
		t.Fatalf("unknown top-level field: %v %v", errAgain, again.ids)
	}
	if category, _ := provimuxLiveListFailureOf(func() error {
		_, e := parseDevinLiveModelList(provimuxDevinRecorded[:10], time.Now())
		return e
	}()); category != provimuxLiveFailureResponse {
		t.Fatalf("a cut-off answer is an invalid response, got %s", category)
	}
}

func provimuxSortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestProvimuxDevinFetchRegistersLearnedModel(t *testing.T) {
	provimuxLiveReset(t)
	var requests atomic.Int32
	var seen http.Header
	previous := devinLiveModelListDo
	devinLiveModelListDo = func(_ context.Context, _ *config.Config, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
		requests.Add(1)
		seen = req.Header.Clone()
		if req.URL.Path != devinLiveModelListPath || req.Method != http.MethodPost {
			t.Errorf("request %s %s", req.Method, req.URL)
		}
		recorder := httptest.NewRecorder()
		_, _ = recorder.Write(provimuxDevinRecorded)
		return recorder.Result(), nil
	}
	t.Cleanup(func() { devinLiveModelListDo = previous })
	executor := &liveListExecutor{provider: "devin"}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "devin-live-test", Provider: "devin",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"api_key": "devin-session", "email": "office@providers.dev"}})
	svc.registerModelsForAuth(t.Context(), auth)
	if slices.Contains(provimuxRegistered(auth), "devin/provimux-test-learned") {
		t.Fatal("before the first fetch the catalog applies")
	}
	provimuxRefresh(svc, executor, false)
	if requests.Load() != 1 || seen.Get("Authorization") != "Basic devin-session-devin-session" || seen.Get("Content-Type") != "application/proto" {
		t.Fatalf("requests=%d headers=%v", requests.Load(), seen)
	}
	ids := provimuxRegistered(auth)
	if !slices.Contains(ids, "devin/provimux-test-learned") || !slices.Contains(ids, "devin/swe-2") || !slices.Contains(ids, "devin/swe-1-6-slow") {
		t.Fatalf("registered = %v", ids)
	}
	if len(ids) != 4 {
		t.Fatalf("a login with a live list offers exactly its list plus Provimux's own models: %v", ids)
	}
	// The executor resolves a variant through LookupDevinModel: the learned model must be found.
	if model := registry.LookupDevinModel("provimux-test-learned"); model == nil || strings.Join(model.Thinking.Levels, ",") != "low,medium,high" {
		t.Fatalf("learned Devin model not found by the executor's lookup: %+v", model)
	}
	if model := registry.LookupDevinModel("provimux-test-learned-high"); model == nil {
		t.Fatal("a learned model's variant id must resolve to it")
	}

	// 8.1.1: the Devin catalog and the list hold bare ids; the login registers them as devin/<id>.
	if model := registry.LookupStaticModelInfoByChannel("devin/provimux-test-learned", "devin"); model == nil || model.ID != "devin/provimux-test-learned" {
		t.Fatalf("a channel-qualified id finds the learned model, qualified: %+v", model)
	}
	if model := registry.LookupStaticModelInfoByChannel("provimux-test-learned", "devin"); model == nil || model.ID != "provimux-test-learned" {
		t.Fatalf("a bare id finds the learned model: %+v", model)
	}
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if strings.HasPrefix(model.ID, "devin/devin/") {
			t.Fatalf("namespaced twice: %s", model.ID)
		}
	}
	state, _ := provimuxLiveListSource{}.ModelListLogin(auth, true, true, true)
	if state.Source != "live" || state.Listed != 3 || state.Learned != 2 || len(state.Models) != 4 {
		t.Fatalf("status = %+v", state)
	}
	sources := map[string]string{}
	for _, model := range state.Models {
		sources[model.ID] = model.Source
		if learned := model.ID == "devin/provimux-test-learned" || model.ID == "devin/provimux-test-unstated"; model.Learned != learned {
			t.Fatalf("learned %s = %v", model.ID, model.Learned)
		}
	}
	if sources["devin/swe-2"] != "live" || sources["devin/provimux-test-learned"] != "live" || sources["devin/swe-1-6-slow"] != "provimux" {
		t.Fatalf("status sources = %v", sources)
	}
	for _, vendor := range state.Live {
		if !strings.HasPrefix(vendor.ID, "devin/") || strings.HasPrefix(vendor.ID, "devin/devin/") {
			t.Fatalf("the vendor's list shows the ids the login registers: %s", vendor.ID)
		}
	}
}

// A configured alias of a Devin model is never one of the vendor's or the catalog's ids, whatever its
// name: the status endpoint reports it as "other" and never counts it as learned (OpenAPI
// ModelListModel.source), as 8.1.0-pmx.2 did. On 8.1.1 the alias's bare name equals a native id
// without its devin/ namespace, so it must be classified before native ids are matched.
func TestProvimuxDevinAliasStatus(t *testing.T) {
	for _, tc := range []struct {
		name, from, alias string
		fork, live        bool
		learned           int
		registered        []string
	}{
		{"catalog, fork under the bare name", "devin/swe-2", "swe-2", true, false, 0, nil},
		{"live, fork a learned model under its bare name", "devin/provimux-test-learned", "provimux-test-learned", true, true, 2,
			[]string{"devin/provimux-test-learned", "devin/provimux-test-unstated", "devin/swe-1-6-slow", "devin/swe-2", "provimux-test-learned"}},
		{"live, fork an unrelated model under a listed bare name", "devin/swe-2", "provimux-test-learned", true, true, 2,
			[]string{"devin/provimux-test-learned", "devin/provimux-test-unstated", "devin/swe-1-6-slow", "devin/swe-2", "provimux-test-learned"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provimuxLiveReset(t)
			previous := devinLiveModelListDo
			devinLiveModelListDo = func(_ context.Context, _ *config.Config, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
				recorder := httptest.NewRecorder()
				_, _ = recorder.Write(provimuxDevinRecorded)
				return recorder.Result(), nil
			}
			t.Cleanup(func() { devinLiveModelListDo = previous })
			cfg := provimuxOn()
			cfg.OAuthModelAlias = map[string][]config.OAuthModelAlias{"devin": {{Name: tc.from, Alias: tc.alias, Fork: tc.fork}}}
			executor := &liveListExecutor{provider: "devin"}
			svc, auth := provimuxService(t, cfg, executor, &coreauth.Auth{ID: "devin-alias-" + strings.ReplaceAll(tc.name, " ", "-"), Provider: "devin",
				Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"api_key": "devin-session", "email": "office@providers.dev"}})
			svc.registerModelsForAuth(t.Context(), auth)
			if tc.live {
				provimuxRefresh(svc, executor, false)
				if got := provimuxRegistered(auth); !slices.Equal(got, tc.registered) {
					t.Fatalf("registered = %v, want %v", got, tc.registered)
				}
			}
			state, _ := provimuxLiveListSource{}.ModelListLogin(auth, true, true, true)
			if state.Learned != tc.learned {
				t.Fatalf("learned_models = %d, want %d (an alias is never vendor-listed): %+v", state.Learned, tc.learned, state)
			}
			found := false
			for _, model := range state.Models {
				if model.ID != tc.alias {
					continue
				}
				found = true
				if model.Source != "other" || model.Learned || len(model.Sources) != 0 {
					t.Fatalf("the alias %s must be other, not learned, without field sources: %+v", tc.alias, model)
				}
			}
			if !found {
				t.Fatalf("no row for the alias %s: %+v", tc.alias, state.Models)
			}
			if tc.live {
				for _, vendor := range state.Live {
					if vendor.ID == tc.alias {
						t.Fatalf("the vendor's list never shows an alias: %+v", state.Live)
					}
				}
			}
		})
	}
}

// A cache file written by 8.1.0-pmx.2 holds Devin ids with the devin/ namespace; 8.1.1's catalog has
// bare ids. The file still applies, and nothing is namespaced twice.
func TestProvimuxDevinCacheOf810(t *testing.T) {
	provimuxLiveReset(t)
	store := modellists.NewStore(t.TempDir())
	liveModelLists.cache.Store(store)
	auth := &coreauth.Auth{ID: "devin-cache-810", Provider: "devin", Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata: map[string]any{"api_key": "devin-session", "email": "office@providers.dev"}}
	if err := store.WriteLive("devin", liveModelListIdentity(auth), time.Now(), []string{"devin/provimux-test-learned", "devin/swe-2"},
		[]*ModelInfo{{ID: "devin/provimux-test-learned", DisplayName: "Provimux Test Learned"}, {ID: "devin/swe-2", DisplayName: "SWE-2"}}); err != nil {
		t.Fatal(err)
	}
	got := applyLiveModelList(provimuxOn(), auth, []*ModelInfo{{ID: "swe-2"}, {ID: "swe-1-5"}, {ID: "swe-1-6-slow"}})
	if ids := strings.Join(liveIDs(got), ","); ids != "swe-2,swe-1-6-slow,provimux-test-learned" {
		t.Fatalf("ids = %s", ids)
	}
}

// --- Kimi --------------------------------------------------------------------------------------

// The Kimi tests' fixture: kimi-k2.8 and kimi-k2.8-code are catalog models (the catalog's values win, one
// learned model of three), provimux-test-kimi-learned is learned.
func TestProvimuxKimiFixtureAgainstTheCatalog(t *testing.T) {
	provimuxFixtureAgainstTheCatalog(t, "kimi", "kimi-models.json", provimuxFixture(t, "kimi-models.json"),
		[]string{"kimi-k2.8", "kimi-k2.8-code"}, []string{"provimux-test-kimi-learned"})
}

func TestProvimuxKimiParser(t *testing.T) {
	list, err := parseKimiLiveModelList(provimuxFixture(t, "kimi-models.json"), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.ids) != 3 {
		t.Fatalf("ids = %v", list.ids)
	}
	k28 := hasLiveID(list.extra, "kimi-k2.8")
	if k28.ContextLength != 262144 || k28.DisplayName != "Kimi K2.8 (live)" || strings.Join(k28.SupportedInputModalities, ",") != "text,image,video" || k28.MaxCompletionTokens != 0 {
		t.Fatalf("k2.8 = %+v", k28)
	}
	if code := hasLiveID(list.extra, "kimi-k2.8-code"); code.SupportedInputModalities != nil || code.Thinking != nil {
		t.Fatalf("a false flag and a yes-or-no reasoning flag state nothing: %+v", code)
	}
	for name, body := range map[string]string{
		"empty":   `{"object":"list","data":[]}`,
		"cut off": string(provimuxFixture(t, "kimi-models.json"))[:200],
		"paged":   `{"data":[{"id":"kimi-k2.8"}],"has_more":true}`,
		"no data": `{"object":"list"}`,
	} {
		if _, errBad := parseKimiLiveModelList([]byte(body), time.Now()); errBad == nil {
			t.Fatalf("%s answer must fail", name)
		}
	}
}

func TestProvimuxKimiFetchKeepsTheCatalogsValues(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "kimi")
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){"/coding/v1/models": provimuxBody(provimuxFixture(t, "kimi-models.json"))})
	executor := &liveListExecutor{provider: "kimi"}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "kimi-live-test", Provider: "kimi",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/coding"}, Metadata: map[string]any{"email": "office@providers.dev"}})
	svc.registerModelsForAuth(t.Context(), auth)
	catalogK28 := registry.LookupStaticModelInfoByChannel("kimi-k2.8", "kimi")
	provimuxRefresh(svc, executor, false)
	if got := strings.Join(provimuxRegistered(auth), ","); got != "kimi-k2.8,kimi-k2.8-code,provimux-test-kimi-learned" {
		t.Fatalf("registered = %s", got)
	}
	var k28 *ModelInfo
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if model.ID == "kimi-k2.8" {
			k28 = model
		}
	}
	if !reflect.DeepEqual(k28, catalogK28) {
		t.Fatalf("a catalog model keeps the catalog's values:\n got %+v\nwant %+v", k28, catalogK28)
	}
	if !registry.ProvimuxIsLearned("kimi", "provimux-test-kimi-learned") || registry.LookupStaticModelInfoByChannel("provimux-test-kimi-learned", "kimi") == nil {
		t.Fatal("the learned model must be recorded for the lookups")
	}
	if slices.ContainsFunc(registry.GetKimiModels(), func(m *ModelInfo) bool { return m.ID == "provimux-test-kimi-learned" }) {
		t.Fatal("the catalog a login without a live list takes must not contain learned models")
	}
}

// --- xAI ---------------------------------------------------------------------------------------

// The xAI tests' fixtures (the two answers, and TestProvimuxSectionRules' list): grok-imagine-image is one of
// Provimux's own xAI models, provimux-test-xai-learned is learned.
func TestProvimuxXAIFixtureAgainstTheCatalog(t *testing.T) {
	answers := append(provimuxFixture(t, "xai-language-models.json"), provimuxFixture(t, "xai-models.json")...)
	provimuxFixtureAgainstTheCatalog(t, "xai", "xai-language-models.json and xai-models.json", answers,
		[]string{"grok-imagine-image"}, []string{"provimux-test-xai-learned"})
}

func TestProvimuxXAIParser(t *testing.T) {
	list, err := parseXAILiveModelList(provimuxFixture(t, "xai-language-models.json"), provimuxFixture(t, "xai-models.json"), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(provimuxSortedKeys(list.ids), ","); got != "grok-4.7,provimux-test-xai-learned" {
		t.Fatalf("only chat models of /language-models are listed: %s", got)
	}
	grok := hasLiveID(list.extra, "grok-4.7")
	if grok.ContextLength != 2000000 || strings.Join(grok.SupportedInputModalities, ",") != "text,image" ||
		grok.Thinking == nil || strings.Join(grok.Thinking.Levels, ",") != "none,low,medium,high" || !grok.Thinking.ZeroAllowed {
		t.Fatalf("grok = %+v %+v", grok, grok.Thinking)
	}
	if neon := hasLiveID(list.extra, "provimux-test-xai-learned"); neon.Thinking != nil || neon.ContextLength != 256000 {
		t.Fatalf("neon = %+v", neon)
	}
	models := provimuxFixture(t, "xai-models.json")
	for name, language := range map[string]string{
		"empty":   `{"models":[]}`,
		"cut off": string(provimuxFixture(t, "xai-language-models.json"))[:300],
		"other":   `{"data":[]}`,
	} {
		if _, errBad := parseXAILiveModelList([]byte(language), models, time.Now()); errBad == nil {
			t.Fatalf("%s answer must fail", name)
		}
	}
	if _, errCut := parseXAILiveModelList(provimuxFixture(t, "xai-language-models.json"), models[:40], time.Now()); errCut == nil {
		t.Fatal("a cut-off /v1/models answer must fail")
	}
}

func TestProvimuxXAIPartialFetchChangesNothing(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "xai")
	var modelsFail atomic.Bool
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1/language-models": provimuxBody(provimuxFixture(t, "xai-language-models.json")),
		"/v1/models": func(w http.ResponseWriter, r *http.Request) {
			if modelsFail.Load() {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			_, _ = w.Write(provimuxFixture(t, "xai-models.json"))
		},
	})
	executor := &liveListExecutor{provider: "xai"}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "xai-live-test", Provider: "xai",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/v1"}, Metadata: map[string]any{"email": "office@providers.dev"}})
	svc.registerModelsForAuth(t.Context(), auth)
	modelsFail.Store(true)
	provimuxRefresh(svc, executor, false)
	if _, held := liveModelLists.get(auth.ID, liveModelListIdentity(auth)); held {
		t.Fatal("a fetch with one of its two answers must not be published")
	}
	modelsFail.Store(false)
	provimuxRefresh(svc, executor, true)
	before := provimuxRegistered(auth)
	if !slices.Contains(before, "provimux-test-xai-learned") || !slices.Contains(before, "grok-imagine-image") {
		t.Fatalf("registered = %v", before)
	}
	modelsFail.Store(true)
	provimuxRefresh(svc, executor, true)
	if after := provimuxRegistered(auth); !slices.Equal(after, before) {
		t.Fatalf("a failed fetch changed the list: %v -> %v", before, after)
	}
}

func TestProvimuxXAIBaseIsTheAPIHost(t *testing.T) {
	for base, want := range map[string]string{
		"":                                   "https://api.x.ai/v1",
		"https://cli-chat-proxy.grok.com/v1": "https://api.x.ai/v1",
		"https://api.x.ai/v1/":               "https://api.x.ai/v1",
		"https://xai.internal.example/v1":    "https://xai.internal.example/v1",
	} {
		auth := &coreauth.Auth{Provider: "xai", Attributes: map[string]string{"base_url": base}}
		if got := xaiLiveModelListBase(auth); got != want {
			t.Fatalf("%q -> %q, want %q", base, got, want)
		}
	}
}

// --- Gemini keys -------------------------------------------------------------------------------

// The Gemini key tests' fixture: provimux-test-gemini-learned is learned.
func TestProvimuxGeminiFixtureAgainstTheCatalog(t *testing.T) {
	provimuxFixtureAgainstTheCatalog(t, "gemini", "gemini-page2.json", provimuxFixture(t, "gemini-page2.json"),
		nil, []string{"provimux-test-gemini-learned"})
}

func TestProvimuxGeminiParser(t *testing.T) {
	pages := [][]byte{provimuxFixture(t, "gemini-page1.json"), provimuxFixture(t, "gemini-page2.json")}
	list, err := parseGeminiLiveModelList(pages, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(provimuxSortedKeys(list.ids), ","); got != "gemini-2.5-flash,gemini-3-flash-preview,provimux-test-gemini-learned" {
		t.Fatalf("ids over both pages, chat models only: %s", got)
	}
	neon := hasLiveID(list.extra, "provimux-test-gemini-learned")
	if neon.InputTokenLimit != 2097152 || neon.OutputTokenLimit != 131072 || neon.Name != "models/provimux-test-gemini-learned" || neon.Thinking != nil {
		t.Fatalf("neon = %+v", neon)
	}
	if _, errPartial := parseGeminiLiveModelList(pages[:1], time.Now()); errPartial == nil {
		t.Fatal("a fetch that stopped before the last page must fail")
	}
	if _, errEmpty := parseGeminiLiveModelList([][]byte{[]byte(`{"models":[]}`)}, time.Now()); errEmpty == nil {
		t.Fatal("an empty answer must fail")
	}
	if _, errCut := parseGeminiLiveModelList([][]byte{pages[0][:120]}, time.Now()); errCut == nil {
		t.Fatal("a cut-off answer must fail")
	}
}

func TestProvimuxGeminiKeyFollowsPages(t *testing.T) {
	provimuxLiveReset(t)
	var secondFails atomic.Bool
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1beta/models": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("pageSize") != "1000" {
				t.Errorf("pageSize = %q", r.URL.Query().Get("pageSize"))
			}
			if r.URL.Query().Get("pageToken") == "" {
				_, _ = w.Write(provimuxFixture(t, "gemini-page1.json"))
				return
			}
			if secondFails.Load() {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(provimuxFixture(t, "gemini-page2.json"))
		},
	})
	serverURL, _ := url.Parse(server.URL)
	executor := &provimuxRewriteExecutor{provider: "gemini", server: serverURL}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "gemini-key-live-test", Provider: "gemini",
		Attributes: map[string]string{"api_key": "AIza-test"}})
	svc.registerModelsForAuth(t.Context(), auth)
	secondFails.Store(true)
	provimuxRefresh(svc, executor, false)
	if _, held := liveModelLists.get(auth.ID, liveModelListIdentity(auth)); held {
		t.Fatal("a fetch that lost its second page must not be published")
	}
	secondFails.Store(false)
	provimuxRefresh(svc, executor, true)
	if !strings.HasPrefix(executor.paths[0], "generativelanguage.googleapis.com/v1beta/models?") {
		t.Fatalf("the key is asked on Google's own host: %v", executor.paths)
	}
	ids := provimuxRegistered(auth)
	if !slices.Contains(ids, "provimux-test-gemini-learned") || slices.Contains(ids, "text-embedding-005") || len(ids) != 3 {
		t.Fatalf("registered = %v", ids)
	}
	if !registry.ProvimuxIsLearned("gemini", "provimux-test-gemini-learned") {
		t.Fatal("the model the catalog lacks must be learned")
	}
	own := &coreauth.Auth{Provider: "gemini", Attributes: map[string]string{"api_key": "k", "base_url": "https://proxy.example/v1beta"}}
	if liveModelListProvider(own) != "" {
		t.Fatal("a key with its own base URL is never asked")
	}
}

// --- Copilot and Antigravity -------------------------------------------------------------------

// The Copilot test's fixture: claude-fable-5 is a catalog model (an account's catalog routes only ids the
// section names), provimux-test-copilot-learned and provimux-test-copilot-disabled are not.
func TestProvimuxCopilotFixtureAgainstTheCatalog(t *testing.T) {
	provimuxFixtureAgainstTheCatalog(t, "copilot", "copilot-models.json", provimuxFixture(t, "copilot-models.json"),
		[]string{"claude-fable-5"}, []string{"provimux-test-copilot-learned", "provimux-test-copilot-disabled"})
}

// The Antigravity test's fixture: gemini-4-argon-high is a catalog model, provimux-test-antigravity-learned
// is learned.
func TestProvimuxAntigravityFixtureAgainstTheCatalog(t *testing.T) {
	provimuxFixtureAgainstTheCatalog(t, "antigravity", "antigravity-models.json", provimuxFixture(t, "antigravity-models.json"),
		[]string{"gemini-4-argon-high"}, []string{"provimux-test-antigravity-learned"})
}

func TestProvimuxCopilotLearnsOfferedModels(t *testing.T) {
	provimuxLiveReset(t)
	var payload struct {
		Models []copilot.Model `json:"data"`
	}
	if err := json.Unmarshal(provimuxFixture(t, "copilot-models.json"), &payload); err != nil {
		t.Fatal(err)
	}
	provimuxNoteCopilotList("copilot-a", payload.Models, time.Unix(100, 0))
	learned := registry.ProvimuxLearnedModels("copilot")
	if len(learned) != 1 || learned[0].ID != "provimux-test-copilot-learned" || learned[0].ContextLength != 400000 || learned[0].OutputTokenLimit != 128000 {
		t.Fatalf("only offered chat models the section lacks are learned: %+v", learned)
	}
	if registry.LookupStaticModelInfoByChannel("provimux-test-copilot-learned", "copilot") == nil {
		t.Fatal("the resolver's lookup must find the learned model")
	}
	account := copilot.NewCatalog(payload.Models, time.Now())
	var routed []string
	for _, route := range provimuxCopilotRoutes(provimuxOn(), account) {
		routed = append(routed, route.Info.ID)
	}
	if !slices.Contains(routed, "provimux-test-copilot-learned") || !slices.Contains(routed, "claude-fable-5") || slices.Contains(routed, "provimux-test-copilot-disabled") {
		t.Fatalf("routes = %v", routed)
	}
	for _, route := range provimuxCopilotRoutes(provimuxOn(), copilot.PresetCatalog) {
		if route.Info.ID == "provimux-test-copilot-learned" {
			t.Fatal("a login whose account has not answered must not offer learned models")
		}
	}
	off := &config.Config{}
	off.Models.LiveLists = new(false)
	for _, route := range provimuxCopilotRoutes(off, copilot.PresetCatalog) {
		if route.Info.ID == "provimux-test-copilot-learned" {
			t.Fatal("with live lists off no learned model is offered")
		}
	}
	// GET /v8/management/routing/model-definitions/copilot lists the learned model, as on 8.1.0-pmx.2.
	if !slices.Contains(liveIDs(registry.GetStaticModelDefinitionsByChannel("copilot")), "provimux-test-copilot-learned") {
		t.Fatal("the copilot section's definitions must include learned models")
	}
	if slices.Contains(liveIDs(registry.ProvimuxCatalogModels("copilot")), "provimux-test-copilot-learned") {
		t.Fatal("the catalog alone has no learned model")
	}
	provimuxForgetForeignLists(func(string) bool { return false })
	if len(registry.ProvimuxLearnedModels("copilot")) != 0 {
		t.Fatal("learned models of removed logins must go")
	}
}

func TestProvimuxAntigravityLearnsWithTheVendorsFields(t *testing.T) {
	provimuxLiveReset(t)
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){
		sdkauth.AntigravityModelsPath: provimuxBody(provimuxFixture(t, "antigravity-models.json")),
	})
	hints, status := sdkauth.FetchAntigravityAvailableModels(t.Context(), sdkauth.AntigravityModelProbeRequest{
		BaseURLs: []string{server.URL}, AccessToken: "token", ProjectID: "project", UserAgent: "test"})
	if status != sdkauth.AntigravityProbeStatusSuccess {
		t.Fatalf("status = %v", status)
	}
	auth := &coreauth.Auth{ID: "antigravity-a", Provider: "antigravity"}
	models := provimuxAntigravityModels(provimuxOn(), auth, registry.GetAntigravityModels(),
		antigravityModelCapabilityHints{ModelIDs: hints.ModelIDs, WebSearchModelIDs: hints.WebSearchModelIDs, updatedAt: time.Unix(100, 0)})
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	slices.Sort(ids)
	if strings.Join(ids, ",") != "gemini-4-argon-high,provimux-test-antigravity-learned" {
		t.Fatalf("models = %v (internal ids are never added)", ids)
	}
	neon := hasLiveID(models, "provimux-test-antigravity-learned")
	if neon.DisplayName != "Provimux Test Antigravity Learned" || neon.ContextLength != 2097152 || neon.MaxCompletionTokens != 131072 || neon.Thinking != nil {
		t.Fatalf("neon = %+v", neon)
	}
	if registry.LookupStaticModelInfoByChannel("provimux-test-antigravity-learned", "antigravity") == nil {
		t.Fatal("learned Antigravity model not recorded")
	}
	off := &config.Config{}
	off.Models.LiveLists = new(false)
	if got := provimuxAntigravityModels(off, auth, registry.GetAntigravityModels(), antigravityModelCapabilityHints{ModelIDs: hints.ModelIDs}); len(got) != 1 {
		t.Fatalf("with live lists off only the catalog's models: %v", liveIDs(got))
	}
}

// --- 5.1 row by row ----------------------------------------------------------------------------

func TestProvimuxSectionRules(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "xai")
	catalog := []*ModelInfo{
		{ID: "grok-4.7", Type: "xai", ContextLength: 1, MaxCompletionTokens: 30000, Thinking: &registry.ThinkingSupport{Levels: []string{"low"}, Min: 1, Max: 2}},
		{ID: "grok-retired", Type: "xai"},
		{ID: "grok-imagine-image", Type: "xai"},
		{ID: "grok-sparse", Type: "xai", MaxCompletionTokens: 8000},
	}
	list := liveModelList{ids: map[string]struct{}{"grok-4.7": {}, "provimux-test-xai-learned": {}, "grok-sparse": {}}, extra: []*ModelInfo{
		{ID: "grok-4.7", ContextLength: 2000000, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}},
		{ID: "grok-sparse", ContextLength: 131072, MaxCompletionTokens: 4000, SupportedInputModalities: []string{"text"},
			Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}},
		{ID: "provimux-test-xai-learned", ContextLength: 256000},
	}}
	out := filterByLiveModelList("xai", catalog, list)
	// Row 3: a login with a live list offers exactly the models it names, plus Provimux's own.
	if got := strings.Join(liveIDs(out), ","); got != "grok-4.7,grok-imagine-image,grok-sparse,provimux-test-xai-learned" {
		t.Fatalf("models = %s", got)
	}
	// Row 2: a model the catalog has keeps the catalog's values (the very entry); the vendor fills
	// only fields the catalog leaves empty, never the thinking block; nothing invented.
	if grok := hasLiveID(out, "grok-4.7"); grok != catalog[0] {
		t.Fatalf("grok = %+v %+v", grok, grok.Thinking)
	}
	if sparse := hasLiveID(out, "grok-sparse"); sparse.ContextLength != 131072 || sparse.MaxCompletionTokens != 8000 ||
		strings.Join(sparse.SupportedInputModalities, ",") != "text" || sparse.Thinking != nil || catalog[3].ContextLength != 0 {
		t.Fatalf("sparse = %+v", sparse)
	}
	if neon := hasLiveID(out, "provimux-test-xai-learned"); neon.MaxCompletionTokens != 0 || neon.Thinking != nil {
		t.Fatalf("a field no source states stays unknown: %+v", neon)
	}
	// Row 1: the learned model is recorded in the section; row 4: a login without a list does not get it.
	list.provider, list.identity, list.fetchedAt = "xai", "x", time.Now()
	liveModelLists.put("xai-a", list)
	provimuxUpdateLearnedModels("xai")
	if !registry.ProvimuxIsLearned("xai", "provimux-test-xai-learned") {
		t.Fatal("learned model not in the section")
	}
	without := &coreauth.Auth{ID: "xai-b", Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "acct-2"}}
	if got := applyLiveModelList(provimuxOn(), without, registry.GetXAIModels()); slices.ContainsFunc(got, func(m *ModelInfo) bool { return m.ID == "provimux-test-xai-learned" }) {
		t.Fatal("a login without a live list must not offer learned models")
	}
}

func TestProvimuxExclusionsStillApply(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "kimi")
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){"/coding/v1/models": provimuxBody(provimuxFixture(t, "kimi-models.json"))})
	cfg := provimuxOn()
	cfg.OAuthExcludedModels = map[string][]string{"kimi": {"provimux-test-kimi-learned"}}
	executor := &liveListExecutor{provider: "kimi"}
	svc, auth := provimuxService(t, cfg, executor, &coreauth.Auth{ID: "kimi-excl-test", Provider: "kimi",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/coding"}, Metadata: map[string]any{"email": "office@providers.dev"}})
	svc.registerModelsForAuth(t.Context(), auth)
	provimuxRefresh(svc, executor, false)
	if ids := provimuxRegistered(auth); slices.Contains(ids, "provimux-test-kimi-learned") || !slices.Contains(ids, "kimi-k2.8") {
		t.Fatalf("Row 5: the user's exclusions apply on top: %v", ids)
	}
}

// --- the 7-day limit, the disk cache, a restart without network --------------------------------

func TestProvimuxSevenDayLimit(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "kimi")
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){"/coding/v1/models": provimuxBody(provimuxFixture(t, "kimi-models.json"))})
	executor := &liveListExecutor{provider: "kimi"}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "kimi-age-test", Provider: "kimi",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/coding"}, Metadata: map[string]any{"email": "office@providers.dev"}})
	start := time.Now()
	liveModelListNow = func() time.Time { return start }
	svc.registerModelsForAuth(t.Context(), auth)
	provimuxRefresh(svc, executor, false)
	if !slices.Contains(provimuxRegistered(auth), "provimux-test-kimi-learned") {
		t.Fatal("list not applied")
	}
	server.Close()
	liveModelListNow = func() time.Time { return start.Add(liveModelListMaxAge - time.Minute) }
	provimuxRefresh(svc, executor, true)
	if !slices.Contains(provimuxRegistered(auth), "provimux-test-kimi-learned") {
		t.Fatal("a list that cannot be refreshed stays in use for 7 days")
	}
	liveModelListNow = func() time.Time { return start.Add(liveModelListMaxAge + time.Minute) }
	provimuxRefresh(svc, executor, true)
	if slices.Contains(provimuxRegistered(auth), "provimux-test-kimi-learned") {
		t.Fatal("after 7 days the login takes the catalog")
	}
	if registry.ProvimuxIsLearned("kimi", "provimux-test-kimi-learned") {
		t.Fatal("an expired list's models are no longer learned")
	}
	if again := liveModelLists.newlyExpired(liveModelListNow()); len(again) != 0 {
		t.Fatalf("an expired list is announced once: %v", again)
	}
}

func TestProvimuxRestartWithoutNetworkKeepsTheLastLists(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "kimi")
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	var down atomic.Bool
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){"/coding/v1/models": func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "no network", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(provimuxFixture(t, "kimi-models.json"))
	}})
	login := func() *coreauth.Auth {
		return &coreauth.Auth{ID: "kimi-restart-test", Provider: "kimi",
			Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/coding"}, Metadata: map[string]any{"email": "office@providers.dev"}}
	}
	executor := &liveListExecutor{provider: "kimi"}
	svc, auth := provimuxService(t, provimuxOn(), executor, login())
	svc.registerModelsForAuth(t.Context(), auth)
	provimuxRefresh(svc, executor, false)
	before := provimuxRegistered(auth)
	files, _ := filepath.Glob(filepath.Join(dir, modellists.FolderName, "live-*.json"))
	if len(files) != 1 {
		t.Fatalf("cache files = %v", files)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	raw, _ := os.ReadFile(files[0])
	for _, secret := range []string{"office@providers.dev", "kimi-restart-test", "Bearer"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the cache must not hold %q", secret)
		}
	}

	// A new process: empty memory, no network.
	registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	resetLiveModelLists()
	liveModelLists.mu.Lock()
	liveModelLists.cacheTried = map[string]string{}
	liveModelLists.mu.Unlock()
	liveModelLists.cache.Store(nil)
	registry.SetProvimuxLearnedModels("kimi", nil)
	down.Store(true)
	svc2, auth2 := provimuxService(t, provimuxOn(), executor, login())
	svc2.registerModelsForAuth(t.Context(), auth2)
	if after := provimuxRegistered(auth2); !slices.Equal(after, before) {
		t.Fatalf("restart without network: %v, last run %v", after, before)
	}
	if !registry.ProvimuxIsLearned("kimi", "provimux-test-kimi-learned") {
		t.Fatal("learned models come back from the cache")
	}
	provimuxRefresh(svc2, executor, false)
	if after := provimuxRegistered(auth2); !slices.Equal(after, before) {
		t.Fatalf("the failed fetch after the restart changed the list: %v", after)
	}
	list, _ := liveModelLists.get(auth2.ID, liveModelListIdentity(auth2))
	if !list.fromCache {
		t.Fatal("the list still comes from the cache")
	}
}

func TestProvimuxCacheOfAnotherAccountIsIgnored(t *testing.T) {
	provimuxLiveReset(t)
	provimuxAsk(t, "kimi")
	store := modellists.NewStore(t.TempDir())
	liveModelLists.cache.Store(store)
	ada := &coreauth.Auth{ID: "kimi-cache", Provider: "kimi", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "office@providers.dev"}}
	bob := &coreauth.Auth{ID: "kimi-cache", Provider: "kimi", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "acct-2"}}
	if err := store.WriteLive("kimi", liveModelListIdentity(ada), time.Now(), []string{"provimux-test-kimi-learned"}, []*ModelInfo{{ID: "provimux-test-kimi-learned"}}); err != nil {
		t.Fatal(err)
	}
	if got := applyLiveModelList(provimuxOn(), bob, []*ModelInfo{{ID: "kimi-k2.8"}}); strings.Join(liveIDs(got), ",") != "kimi-k2.8" {
		t.Fatalf("another account's cached list must not apply: %v", liveIDs(got))
	}
	if got := applyLiveModelList(provimuxOn(), ada, []*ModelInfo{{ID: "kimi-k2.8"}}); strings.Join(liveIDs(got), ",") != "provimux-test-kimi-learned" {
		t.Fatalf("the account's own cached list applies: %v", liveIDs(got))
	}
}

func TestProvimuxLiveListsDefaultOn(t *testing.T) {
	previous := registry.SetProvimuxLiveListsDefault(true)
	t.Cleanup(func() { registry.SetProvimuxLiveListsDefault(previous) })
	if !(&config.Config{}).Models.LiveListsOn() {
		t.Fatal("an unset models.live-lists is on")
	}
	off := &config.Config{}
	off.Models.LiveLists = new(false)
	if off.Models.LiveListsOn() {
		t.Fatal("live-lists: false turns it off")
	}
}

func TestProvimuxLogLine(t *testing.T) {
	provimuxLiveReset(t)
	auth := &coreauth.Auth{ID: "devin-log", Provider: "devin", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "office@providers.dev"}}
	list, err := parseDevinLiveModelList(provimuxDevinRecorded, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	line := provimuxLiveLogLine("devin", auth, list)
	if !strings.Contains(line, "model list for devin login ") || !strings.Contains(line, ": live, 3 models, 2 not in the catalog yet (devin/provimux-test-learned, devin/provimux-test-unstated)") {
		t.Fatalf("line = %q", line)
	}
	if strings.Contains(line, "office@providers.dev") || strings.Contains(line, "devin-log") {
		t.Fatalf("the log names the login by its auth index only: %q", line)
	}
}

// --- the review's fixes ------------------------------------------------------------------------

// Kimi and xAI are not asked for now: no request, nothing cached, and their logins get exactly the
// models e5cf5a2d5 gives them (the catalog through the same registration path).
func TestProvimuxKimiAndXAIAreNotAsked(t *testing.T) {
	provimuxLiveReset(t)
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be asked", http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		provider string
		base     string
		catalog  func() []*ModelInfo
	}{
		{"kimi", server.URL + "/coding", registry.GetKimiModels},
		{"xai", server.URL + "/v1", registry.GetXAIModels},
	} {
		executor := &liveListExecutor{provider: tc.provider}
		svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: tc.provider + "-not-asked", Provider: tc.provider,
			Attributes: map[string]string{"auth_kind": "oauth", "base_url": tc.base}, Metadata: map[string]any{"access_token": "t"}})
		svc.registerModelsForAuth(t.Context(), auth)
		provimuxRefresh(svc, executor, true)
		svc.registerModelsForAuth(t.Context(), auth)
		var want []string
		for _, model := range tc.catalog() {
			want = append(want, model.ID)
		}
		slices.Sort(want)
		if got := provimuxRegistered(auth); !slices.Equal(got, want) {
			t.Fatalf("%s: registered %v, the catalog is %v", tc.provider, got, want)
		}
		if executor.calls.Load() != 0 {
			t.Fatalf("%s was asked", tc.provider)
		}
		state, row := provimuxLiveListSource{}.ModelListLogin(auth, true, false, true)
		if !row || state.Supported || state.Source != "built-in" || state.Live != nil {
			t.Fatalf("%s status = %+v", tc.provider, state)
		}
		if slices.Contains(provimuxLiveListSource{}.Providers(), tc.provider) {
			t.Fatalf("live-lists names %s as asked", tc.provider)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("%d requests reached the vendor", requests.Load())
	}
	if files, _ := filepath.Glob(filepath.Join(dir, modellists.FolderName, "*")); len(files) != 0 {
		t.Fatalf("nothing may be cached: %v", files)
	}
}

// A vendor that never answers: the fetch ends at its deadline, the held list stays, the failure is
// recorded as network, and the login is no longer "fetching".
func TestProvimuxFetchDeadline(t *testing.T) {
	provimuxLiveReset(t)
	previous := liveModelListFetchTimeout
	liveModelListFetchTimeout = 200 * time.Millisecond
	t.Cleanup(func() { liveModelListFetchTimeout = previous })
	var hang atomic.Bool
	server := provimuxServe(t, map[string]func(http.ResponseWriter, *http.Request){
		"/v1beta/models": func(w http.ResponseWriter, r *http.Request) {
			if hang.Load() {
				<-r.Context().Done() // never answers
				return
			}
			_, _ = w.Write(provimuxFixture(t, "gemini-page2.json"))
		},
	})
	serverURL, _ := url.Parse(server.URL)
	executor := &provimuxRewriteExecutor{provider: "gemini", server: serverURL}
	svc, auth := provimuxService(t, provimuxOn(), executor, &coreauth.Auth{ID: "gemini-deadline-test", Provider: "gemini",
		Attributes: map[string]string{"api_key": "AIza-test"}})
	svc.registerModelsForAuth(t.Context(), auth)
	provimuxRefresh(svc, executor, false)
	before := provimuxRegistered(auth)
	if !slices.Contains(before, "provimux-test-gemini-learned") {
		t.Fatalf("registered = %v", before)
	}
	hang.Store(true)
	started := time.Now()
	provimuxRefresh(svc, executor, true)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the fetch outlived its deadline: %s", elapsed)
	}
	if after := provimuxRegistered(auth); !slices.Equal(after, before) {
		t.Fatalf("a timed-out fetch changed the list: %v -> %v", before, after)
	}
	state := provimuxLiveListSource{}.State(auth)
	if state.Fetching || !state.HasList || state.Failure == nil || state.Failure.Category != provimuxLiveFailureNetwork {
		t.Fatalf("state = %+v failure = %+v", state, state.Failure)
	}

	// Devin's request is sent by its own client: it gets the same deadline.
	var deadline atomic.Bool
	previousDo := devinLiveModelListDo
	devinLiveModelListDo = func(ctx context.Context, _ *config.Config, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
		_, has := req.Context().Deadline()
		deadline.Store(has)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	t.Cleanup(func() { devinLiveModelListDo = previousDo })
	devinExecutor := &liveListExecutor{provider: "devin"}
	devinSvc, devin := provimuxService(t, provimuxOn(), devinExecutor, &coreauth.Auth{ID: "devin-deadline-test", Provider: "devin",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"api_key": "s", "user_id": "u-1"}})
	if _, err := devinSvc.fetchLiveModelList(context.Background(), devin); err == nil || !deadline.Load() {
		t.Fatalf("devin: err=%v deadline=%v", err, deadline.Load())
	}
}
