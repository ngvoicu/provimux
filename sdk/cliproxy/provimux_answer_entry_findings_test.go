package cliproxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Provimux: the second review of F1 (FIG-02, findings 13 to 17), each on the wire against the stand-in
// (answered-by.md 4.6). Each found an account row whose predicted entry named something other than what was sent.
// Now each answer names the entry of what the stand-in received, or none; never the configuration's. Every key, login
// and id is invented.

// Finding 13: one Vertex service account, one alias on the native route and one over OpenAPI; and a Vertex key on
// openrouter.ai over OpenAPI. Each answer names the model where its route put it.
func TestProvimuxFinding13VertexRoutesNameWhatWasSent(t *testing.T) {
	h := newSentHarness(t, `api-keys:
  vertex:
    - name: provimux-test-vertex-router
      base-url: https://openrouter.ai/api/v1
      models:
        - name: publishers/acme/models/provimux-test-base
          alias: provimux-test-router-alias
          openapi: true
      keys:
        - api-key: provimux-test-vertex-router-key
`)
	const base = "gemini-2.5-pro"
	on, off := true, false
	aliases := map[string][]config.OAuthModelAlias{"vertex": {
		{Name: base + "(high)", Alias: "provimux-test-priced", OpenAPI: &on},
		{Name: base, Alias: "provimux-test-plain", OpenAPI: &off},
	}}
	h.service.cfgMu.Lock()
	h.service.cfg.OAuthModelAlias = aliases
	h.service.cfgMu.Unlock()
	h.service.coreManager.SetConfig(h.service.cfg)
	h.service.coreManager.SetOAuthModelAlias(aliases)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	account := h.register(&coreauth.Auth{ID: "provimux-test-vertex-routes.json", FileName: "provimux-test-vertex-routes.json", Provider: "vertex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/provimux-test-vertex-routes.json"},
		Metadata: map[string]any{"type": "vertex", "project_id": "provimux-test-project", "location": "us", "service_account": map[string]any{
			"type": "service_account", "client_email": "vertex@provimux-test.invalid", "project_id": "provimux-test-project",
			"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), "token_uri": "https://oauth2.googleapis.com/token"}}})
	if !h.service.registerModelsForAuth(t.Context(), account) {
		t.Fatal("fixture: the account's models")
	}

	h.stand.reset()
	plain := h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-plain", false, ""), nil), account, "", "google-vertex/"+base)
	if !strings.Contains(plain.Path, "/publishers/google/models/"+base+":") {
		t.Fatalf("fixture: the native alias went to %s", plain.Path)
	}
	h.stand.reset()
	priced := h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-priced", false, ""), nil), account, "", "google-vertex/google/"+base)
	if !strings.Contains(priced.Path, "endpoints/openapi") || priced.Model != "google/"+base {
		t.Fatalf("fixture: the OpenAPI alias sent %q to %s", priced.Model, priced.Path)
	}

	router := sentKeyFor(t, h, "provimux-test-router-alias")
	h.stand.reset()
	sent := h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-router-alias", false, ""), nil), router, "openrouter.ai", "openrouter/acme/provimux-test-base")
	if sent.Model != "acme/provimux-test-base" {
		t.Fatalf("fixture: the Vertex key on openrouter.ai sent %q", sent.Model)
	}
}

// Finding 14: a plugin publishes a model for a Copilot account and sets the account's provider to meta in the same
// call. The native Meta executor sends it to api.meta.ai; the stand-in answers. The answer names its account and what
// was sent, and no entry: the credential is one a plugin changed (step 3), and stays one after the account's own
// executor refreshed it, and after a restart reads its file back. A Meta login no plugin touched names
// meta-subscription (the control).
func TestProvimuxFinding14APluginsCredentialHasNoEntry(t *testing.T) {
	h := newSentHarness(t, "")
	const model = "provimux-test-custom-registered"
	manager := h.service.coreManager
	host := pluginhost.New()
	host.RegisterPluginForTest(t.Name(), pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelProvider: copilotPluginCatalog{authProvider: "meta", models: []pluginapi.ModelInfo{{ID: model}}}}})
	host.RegisterModels(t.Context(), registry.GetGlobalRegistry())
	host.RegisterExecutors(manager, registry.GetGlobalRegistry())
	manager.SetPluginScheduler(host)
	h.service.pluginHost = host
	t.Cleanup(func() { h.service.pluginHost = nil })
	for _, provider := range []string{"meta", "copilot"} {
		h.service.registerExecutorForAuth(&coreauth.Auth{Provider: provider}, false)
	}
	authDir := filepath.Join(filepath.Dir(h.configPath), "auths")
	registered, err := manager.Register(t.Context(), &coreauth.Auth{ID: "provimux-test-cross-account.json", Provider: "copilot", FileName: "provimux-test-cross-account.json",
		Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "path": filepath.Join(authDir, "provimux-test-cross-account.json")},
		Metadata: map[string]any{"api_key": "provimux-test-meta-token", "access_token": "provimux-test-copilot-token", "expired": sentFar()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(registered.ID) })
	if !h.service.registerModelsForAuth(t.Context(), registered) {
		t.Fatal("fixture: the plugin's publication")
	}
	changed, _ := manager.GetByID(registered.ID)
	if changed.Provider != "meta" || !pluginhost.ProvimuxPluginCredential(changed) {
		t.Fatalf("fixture: the plugin did not change the account (%s, plugin's: %v)", changed.Provider, pluginhost.ProvimuxPluginCredential(changed))
	}

	h.stand.reset()
	sent := h.checkSent(h.post("/v1/chat/completions", chatBody(model, false, ""), nil), changed, "api.meta.ai", "")
	if sent.Model != model {
		t.Fatalf("fixture: the Meta executor sent %q", sent.Model)
	}

	// The account's own executor refreshes the credential, and the manager stores what it returns, as its refresh
	// loop does: the credential is still the plugin's (the assumption of answered-by.md 11), and the answer that
	// follows still names none.
	executor, ok := manager.Executor("meta")
	if !ok {
		t.Fatal("fixture: no Meta executor")
	}
	refreshed, errRefresh := executor.Refresh(t.Context(), changed.Clone())
	if errRefresh != nil || refreshed == nil {
		t.Fatalf("fixture: Meta's refresh: %v", errRefresh)
	}
	if _, errUpdate := manager.UpdateRefreshedAuth(t.Context(), changed, refreshed); errUpdate != nil {
		t.Fatalf("fixture: storing the refresh: %v", errUpdate)
	}
	after, _ := manager.GetByID(registered.ID)
	if after.Provider != "meta" || !pluginhost.ProvimuxPluginCredential(after) {
		t.Fatalf("after the account's own refresh the credential is no longer the plugin's (%s)", after.Provider)
	}
	h.stand.reset()
	h.checkSent(h.post("/v1/chat/completions", chatBody(model, false, ""), nil), after, "api.meta.ai", "")

	// The plugin's change was written to the account's file, as a Meta login's. At the next start the native loader
	// reads that file, with no plugin storage: the mark the change left in it still says whose it is.
	items, errSynth := synthesizer.NewFileSynthesizer().Synthesize(&synthesizer.SynthesisContext{AuthDir: authDir, Config: h.service.currentConfig(), Now: time.Now()})
	if errSynth != nil {
		t.Fatal(errSynth)
	}
	var reloaded *coreauth.Auth
	for _, item := range items {
		if item.FileName == registered.FileName || filepath.Base(item.Attributes["path"]) == registered.FileName {
			reloaded = item
		}
	}
	if reloaded == nil || reloaded.Provider != "meta" {
		t.Fatalf("fixture: the file is not read back as a Meta login: %+v", reloaded)
	}
	if !pluginhost.ProvimuxPluginCredential(reloaded) {
		t.Fatal("after a restart the plugin's change reads as a native Meta login")
	}
	// The account marker reads the same rule: the credential's own id before the restart and after, never the
	// e-mail-based marker of a Meta login.
	if before, after := changed.ProvimuxAccountMarker(), reloaded.ProvimuxAccountMarker(); !strings.HasPrefix(after, "s.") || (reloaded.ID == changed.ID && after != before) {
		t.Fatalf("the marker after a restart is %q (before %q, ids %s and %s)", after, before, changed.ID, reloaded.ID)
	}
	if entry := provimuxEntryOfAnswer(reloaded, cliproxyexecutor.ProvimuxSentRequest{Scheme: "https", Host: "api.meta.ai", Model: model, Signer: "authorization", Status: 200}); entry != "" {
		t.Fatalf("after a restart the plugin's change has the entry %s", entry)
	}

	// The control: a Meta login no plugin touched, the same model, the same host.
	native := h.register(&coreauth.Auth{ID: "provimux-test-meta-native.json", FileName: "provimux-test-meta-native.json", Provider: "meta", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/provimux-test-meta-native.json"},
		Metadata:   map[string]any{"type": "meta", "api_key": "provimux-test-meta-native-token", "user_email": "native@provimux-test.invalid"}}, "provimux-test-meta-native")
	h.stand.reset()
	h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-meta-native", false, ""), nil), native, "api.meta.ai", "meta-subscription/provimux-test-meta-native")
}

// Finding 15: a payload rule sets another model for requests with one header. The same account and name send two
// models; each answer names its own. A rule that deletes the model leaves nothing to read: the answer names its
// account and no model.
func TestProvimuxFinding15APayloadRuleByHeader(t *testing.T) {
	const native, alternate, bare = "provimux-test-native", "provimux-test-alternate", "provimux-test-bare"
	h := newSentHarness(t, `payload:
  override:
    - models:
        - name: `+native+`
          headers:
            X-Client-Tier: special
      params:
        model: `+alternate+`
  filter:
    - models:
        - name: `+bare+`
      params:
        - model
`)
	account := registerCopilotRoutingTestAuth(t, h.service.coreManager, "", native, alternate, bare)
	copilotTestPublisher(t)(append(registry.GetStaticModelDefinitionsByChannel("copilot"), &registry.ModelInfo{ID: native}, &registry.ModelInfo{ID: alternate}, &registry.ModelInfo{ID: bare}))
	h.service.registerExecutorForAuth(account, false)
	if !h.service.refreshModelRegistrationForAuth(account) {
		t.Fatal("fixture: Copilot's models")
	}
	route := "copilot/" + native
	for _, tc := range []struct{ tier, want string }{{"normal", native}, {"special", alternate}, {"normal", native}} {
		h.stand.reset()
		sent := h.checkSent(h.post("/v1/chat/completions", chatBody(route, false, ""), sentHeader("X-Client-Tier", tc.tier)), account, "api.individual.githubcopilot.com", "github-copilot/"+tc.want)
		if sent.Model != tc.want {
			t.Fatalf("fixture: tier %s sent %q, want %q", tc.tier, sent.Model, tc.want)
		}
	}
	h.stand.reset()
	h.checkNoModel(h.post("/v1/chat/completions", chatBody("copilot/"+bare, false, ""), nil), account)
	if received := h.stand.all(); len(received) == 0 || received[len(received)-1].Model != "" {
		t.Fatalf("fixture: the rule did not delete the model: %+v", received)
	}
}

// Finding 16: Antigravity chooses the variant it sends from its stored catalog at the moment of the request. Before
// the catalog is stored, after, and while its read fails, the answer names the variant the stand-in received.
func TestProvimuxFinding16AntigravityNamesTheVariantSent(t *testing.T) {
	h := newSentHarness(t, "")
	path := filepath.Join(t.TempDir(), "provimux-test-catalog.db")
	if errKV := kvstore.ConfigureLocal(true, path); errKV != nil {
		t.Fatal(errKV)
	}
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	const group = "provimux-test-ag-group"
	far := sentFar()
	account := h.register(&coreauth.Auth{ID: "provimux-test-ag-variant.json", FileName: "provimux-test-ag-variant.json", Provider: "antigravity", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/provimux-test-ag-variant.json"},
		Metadata: map[string]any{"type": "antigravity", "access_token": "provimux-test-ag-token", "refresh_token": "provimux-test-ag-refresh",
			"project_id": "provimux-test-project", "email": "variant@provimux-test.invalid", "expired": far, "expiry": far}},
		group, group+"-low")
	ask := func(stage, name string) sentReceived {
		h.stand.reset()
		answer := h.post("/v1/chat/completions", chatBody(name, false, `,"reasoning_effort":"high"`), nil)
		sent := h.checkSent(answer, account, "", "antigravity/"+sentModelOf(h))
		t.Logf("%s: asked %s with high effort, sent %s, entry %s", stage, name, sent.Model, answer.get("X-Provimux-Entry"))
		return sent
	}
	cold := ask("before the catalog", group+"-low")
	if !h.service.coreManager.StoreAntigravityModelCatalog(t.Context(), account, map[string]string{group + "-low": "MODEL_PLACEHOLDER_M525", group + "-high": "MODEL_PLACEHOLDER_M526"},
		map[string]map[string]int{group: {"low": 4096, "high": 8192}}, nil) {
		t.Fatal("fixture: the catalog was not stored")
	}
	held := ask("with the catalog", group+"-low")
	if cold.Model == held.Model {
		t.Fatalf("fixture: the stored catalog did not change what was sent (%s)", held.Model)
	}
	if held.Model != group+"-high" {
		t.Fatalf("fixture: with the catalog the high effort sent %s", held.Model)
	}
	// The store the catalog is read from fails, then is opened again without the catalog written anew (the review's
	// closed-store case): the answer names the variant that was sent, as it did before.
	kv, _, errKV := kvstore.CurrentKVClient()
	bolt, isBolt := kv.(*kvstore.BoltStore)
	if errKV != nil || !isBolt {
		t.Fatalf("fixture: the catalog's store is %T (%v)", kv, errKV)
	}
	if errClose := bolt.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if _, _, errRead := bolt.KVGet(t.Context(), "provimux-test-probe"); errRead == nil {
		t.Fatal("fixture: the closed store still reads")
	}
	if errKV := kvstore.ShutdownLocal(); errKV != nil {
		t.Fatal(errKV)
	}
	if errKV := kvstore.ConfigureLocal(true, path); errKV != nil {
		t.Fatal(errKV)
	}
	if again := ask("the store opened again", group+"-low"); again.Model != group+"-high" {
		t.Fatalf("fixture: after the store was opened again the high effort sent %s", again.Model)
	}
}

// Finding 17: an xAI key sent to api.x.ai, the same key marked as not the paid API (sent to the Grok CLI proxy), and a
// key whose metadata names a relay. Only the first is xai.
func TestProvimuxFinding17XAIDestinations(t *testing.T) {
	h := newSentHarness(t, "")
	for _, tc := range []struct {
		name, attributeBase, storedBase string
		usingAPI                        bool
		host, entry                     string
	}{
		{"the paid API", "https://api.x.ai/v1", "", true, "api.x.ai", "xai/provimux-test-grok-paid"},
		{"not the paid API: the Grok CLI proxy", "https://api.x.ai/v1", "", false, "cli-chat-proxy.grok.com", ""},
		{"a relay in its metadata", "", "https://provimux-test-relay.invalid/v1", true, "provimux-test-relay.invalid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.t = t
			model := "provimux-test-grok-" + strings.Fields(tc.name)[1]
			if tc.usingAPI && tc.attributeBase != "" {
				model = "provimux-test-grok-paid"
			}
			id := "provimux-test-xai-" + strings.ReplaceAll(tc.name, " ", "-")
			account := h.register(&coreauth.Auth{ID: id, FileName: id + ".json", Provider: "xai", Status: coreauth.StatusActive,
				Attributes: map[string]string{"auth_kind": "apikey", "api_key": "provimux-test-xai-key", "base_url": tc.attributeBase},
				Metadata:   map[string]any{"using_api": tc.usingAPI, "base_url": tc.storedBase}}, model)
			h.stand.reset()
			sent := h.checkSent(h.post("/v1/chat/completions", chatBody(model, false, ""), nil), account, tc.host, tc.entry)
			if sent.Model != model {
				t.Fatalf("fixture: sent %q", sent.Model)
			}
		})
	}
}
