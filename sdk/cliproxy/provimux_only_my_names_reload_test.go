package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// C1-C3 and K8 (only-my-names.md sections 9 and 16.7): the switch and the names through the config API the
// console uses, the watcher's reload, and the gate, with no restart. The keys point at a closed local port;
// nothing is sent to them.

const namesManagementKey = "only-my-names-management-key"

type namesHarness struct {
	t          *testing.T
	service    *Service
	handler    http.Handler
	configPath string
}

func newNamesHarness(t *testing.T, body string) *namesHarness {
	t.Helper()
	namesReset(t)
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(namesManagementKey), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	// The v8 layout: the config API rewrites a file of the older layout into it at its first write.
	raw := fmt.Sprintf("server:\n  port: 54321\nmanagement:\n  allow-remote: true\n  secret-key: %q\noauth:\n  auth-dir: %q\n%sconfig-version: 8\n", string(hashed), filepath.ToSlash(authDir), body)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := internalconfig.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcherWrapper, err := service.watcherFactory(configPath, authDir, func(newCfg *config.Config) { service.applyWatcherConfigUpdate(newCfg) })
	if err != nil {
		t.Fatal(err)
	}
	service.setWatcher(watcherWrapper)
	service.ensureAuthUpdateQueue(ctx)
	watcherWrapper.SetAuthUpdateQueue(service.authUpdates)
	watcherWrapper.SetConfig(service.currentConfig())
	server := api.NewServer(service.currentConfig(), service.coreManager, service.accessManager, configPath, service.serverOptions...)
	service.server = server
	t.Cleanup(func() {
		if service.authQueueStop != nil {
			service.authQueueStop()
		}
		_ = watcherWrapper.Stop()
		// A reload still registering would register the accounts again after they are removed below, for the
		// tests that follow to see: wait for it (it holds configRuntimeMu while it registers).
		service.configRuntimeMu.Lock()
		cancel()
		service.configRuntimeMu.Unlock()
		for _, auth := range service.coreManager.List() {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		}
	})
	// What Run does for the config's keys at a start.
	service.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(ctx), service.currentConfig())
	return &namesHarness{t: t, service: service, handler: server.Handler(), configPath: configPath}
}

func (h *namesHarness) put(path, body string) {
	h.t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/v8/management/config/"+path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+namesManagementKey)
	request.Header.Set("Content-Type", "application/json")
	h.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("PUT %s: %d %s", path, recorder.Code, recorder.Body.String())
	}
}

// auth returns the account of provider (a key family, or an openai-compatibility entry's name).
func (h *namesHarness) auth(provider string) *coreauth.Auth {
	h.t.Helper()
	for _, auth := range h.service.coreManager.List() {
		if strings.EqualFold(auth.Provider, provider) || strings.EqualFold(auth.Attributes["compat_name"], provider) {
			return auth
		}
	}
	h.t.Fatalf("no %s account among %d", provider, len(h.service.coreManager.List()))
	return nil
}

// await waits until every account's served names (registered, listed and let through the door) are want
// (by provider).
func (h *namesHarness) await(what string, want map[string][]string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := map[string][]string{}
		for provider := range want {
			got[provider] = namesServed(h.t, h.auth(provider))
			if got[provider] == nil {
				got[provider] = []string{}
			}
		}
		if reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: served %v, want %v", what, got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const namesReloadConfig = `api-keys:
  claude:
    - name: claude-a
      base-url: http://127.0.0.1:1
      models:
        - name: up-a
          alias: mine
        - name: plain-a
      keys:
        - api-key: sk-names-a
  openai-compatibility:
    - name: standin
      base-url: http://127.0.0.1:1/v1
      models:
        - name: shared
      keys:
        - api-key: sk-names-b
`

// C1, C2 and K5: off, on and off again through PUT /v8/management/config/models/only-my-names; what is
// served follows each time without a restart, what is registered never moves, and the last state equals the
// first. The writer puts the object at a path the file lacked and leaves the rest of the file as it was.
func TestProvimuxOnlyMyNamesSwitchThroughTheConfigAPI(t *testing.T) {
	h := newNamesHarness(t, namesReloadConfig)
	compat := "standin"
	h.await("at start", map[string][]string{"claude": {"mine", "plain-a"}, compat: {"shared"}})
	keyA, keyB := h.auth("claude"), h.auth(compat)
	firstA := namesRegisteredStable(keyA.ID)
	firstB := namesRegisteredStable(keyB.ID)
	before, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatal(err)
	}

	h.put("models/only-my-names", `{"enabled":true,"keep":[]}`)
	after, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after), string(before)) {
		t.Fatalf("C2: the file changed beyond the new key:\n--- before\n%s\n--- after\n%s", before, after)
	}
	added := strings.TrimPrefix(string(after), string(before))
	for _, line := range strings.Split(strings.TrimRight(added, "\n"), "\n") {
		if line != "models:" && !strings.HasPrefix(line, "  ") {
			t.Fatalf("C2: added a line outside models.only-my-names: %q\n%s", line, added)
		}
	}
	if !strings.Contains(added, "only-my-names:") || !strings.Contains(added, "enabled: true") {
		t.Fatalf("C2: added %q", added)
	}
	h.await("on", map[string][]string{"claude": {"mine"}, compat: {}})
	if a, b := namesRegisteredStable(keyA.ID), namesRegisteredStable(keyB.ID); !reflect.DeepEqual(a, firstA) || !reflect.DeepEqual(b, firstB) {
		t.Fatalf("K5: on changed what is registered:\n%+v\n%+v", a, b)
	}

	h.put("models/only-my-names", `{"enabled":true,"keep":["shared"]}`)
	h.await("on, keep shared", map[string][]string{"claude": {"mine"}, compat: {"shared"}})

	h.put("models/only-my-names", `{"enabled":false,"keep":["shared"]}`)
	h.await("off", map[string][]string{"claude": {"mine", "plain-a"}, compat: {"shared"}})
	if a, b := namesRegisteredStable(keyA.ID), namesRegisteredStable(keyB.ID); !reflect.DeepEqual(a, firstA) || !reflect.DeepEqual(b, firstB) {
		t.Fatalf("off again differs from the start:\n%+v\n%+v", a, b)
	}
	if got := h.service.currentConfig().Models.OnlyMyNames; got == nil || got.Enabled || !slices.Equal(got.Keep, []string{"shared"}) {
		t.Fatalf("keep stays in the file for the next time: %+v", got)
	}
}

// C3: with the switch on, an alias added on key A in one reload makes key B's same-named model
// served; removing it holds it back again. Key B's own entry does not change.
func TestProvimuxOnlyMyNamesAliasOnOneKeyOpensTheName(t *testing.T) {
	h := newNamesHarness(t, namesReloadConfig+"models:\n  only-my-names:\n    enabled: true\n")
	compat := "standin"
	h.await("at start", map[string][]string{"claude": {"mine"}, compat: {}})
	claude := func(models string) string {
		return `[{"name":"claude-a","base-url":"http://127.0.0.1:1","keys":[{"api-key":"sk-names-a"}],"models":` + models + `}]`
	}
	h.put("api-keys/claude", claude(`[{"name":"up-a","alias":"mine"},{"name":"plain-a"},{"name":"up-shared","alias":"shared"}]`))
	h.await("alias added", map[string][]string{"claude": {"mine", "shared"}, compat: {"shared"}})
	h.put("api-keys/claude", claude(`[{"name":"up-a","alias":"mine"},{"name":"plain-a"}]`))
	h.await("alias removed", map[string][]string{"claude": {"mine"}, compat: {}})
}

// C3 with a login in the auth folder as account B: its file does not change and nothing makes it register
// again (the reload clause of the earlier build is gone); the name it registers is served from the commit
// that names it.
func TestProvimuxOnlyMyNamesAliasOnAKeyOpensALoginsName(t *testing.T) {
	c1, _ := namesClaudeCatalog(t)
	h := newNamesHarness(t, namesReloadConfig+"models:\n  only-my-names:\n    enabled: true\n")
	authDir := h.service.currentConfig().AuthDir
	login := `{"type":"claude","email":"names-b@example.com","access_token":"names-token","expired":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(authDir, "claude-names-b.json"), []byte(login), 0o600); err != nil {
		t.Fatal(err)
	}
	claude := func(models string) string {
		return `[{"name":"claude-a","base-url":"http://127.0.0.1:1","keys":[{"api-key":"sk-names-a"}],"models":` + models + `}]`
	}
	// A first reload loads the login; nothing names its models yet.
	h.put("api-keys/claude", claude(`[{"name":"up-a","alias":"mine"},{"name":"plain-a"},{"name":"x","alias":"y"}]`))
	loginAuth := func() *coreauth.Auth {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			for _, auth := range h.service.coreManager.List() {
				if auth.Provider == "claude" && auth.AuthKind() == "oauth" {
					return auth
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the login was not loaded")
		return nil
	}()
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(loginAuth.ID) })
	awaitLogin := func(what string, want []string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := namesServed(t, loginAuth)
			if slices.Equal(got, want) || (len(got) == 0 && len(want) == 0) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the login serves %v, want %v", what, got, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	awaitLogin("before", nil)
	h.put("api-keys/claude", claude(`[{"name":"up-a","alias":"mine"},{"name":"plain-a"},{"name":"x","alias":"y"},{"name":"up-c","alias":"`+c1+`"}]`))
	awaitLogin("alias added on the key", []string{c1})
	h.put("api-keys/claude", claude(`[{"name":"up-a","alias":"mine"},{"name":"plain-a"},{"name":"x","alias":"y"}]`))
	awaitLogin("alias removed", nil)
}

// K8: after a write through the config API, the first status read whose applied.config_etag is the write's
// ETag is followed at once, with no wait and with registration paused, by a list and a request that obey the
// new rule. 100 rounds, the switch turning each round; every other round writes twice with no status read in
// between (the second review's "two writes"), and the status then shows the second. model-routes has no
// applied block any more.
func TestProvimuxOnlyMyNamesStatusAndServedAgreeAfterAWrite(t *testing.T) {
	vendor := newNamesVendor(t)
	h := newNamesHarness(t, strings.Replace(namesDoorConfig(vendor.URL, false), "enabled: false", "enabled: false\n    keep: []", 1))
	server := httptest.NewServer(h.handler)
	defer server.Close()
	d := &namesDoor{namesHarness: h, server: server, vendor: vendor}
	type status struct {
		OnlyMyNames struct {
			Enabled bool `json:"enabled"`
		} `json:"only_my_names"`
		Applied struct {
			Generation uint64  `json:"generation"`
			ConfigETag *string `json:"config_etag"`
			Applying   bool    `json:"applying"`
		} `json:"applied"`
	}
	get := func(path string, into any) []byte {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+namesManagementKey)
		h.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, recorder.Code)
		}
		if into != nil {
			if err := json.Unmarshal(recorder.Body.Bytes(), into); err != nil {
				t.Fatal(err)
			}
		}
		return recorder.Body.Bytes()
	}
	if routes := get("/v8/management/provimux/model-routes", nil); strings.Contains(string(routes), `"applied"`) {
		t.Fatalf("model-routes still carries applied: %s", routes)
	}
	// Registration is paused: every reload stops after its commit, before the accounts register again, until
	// the test releases it after its checks. Nothing needs the accounts to register again.
	svc := h.service
	entered := make(chan struct{}, 1024)
	release := make(chan struct{}, 1024)
	var completed atomic.Int64
	svc.hooks.OnConfigApplied = func(*Service, *config.Config) { completed.Add(1) }
	svc.updateServerClientsContextFn = func(ctx context.Context, cfg *config.Config) bool {
		ok := svc.server.UpdateClientsContext(ctx, cfg)
		entered <- struct{}{}
		<-release
		return ok
	}
	released := int64(0)
	releaseOne := func() {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("a reload did not reach its pause")
		}
		released++
		release <- struct{}{}
	}
	defer func() { // the reloads end before the harness's cleanup unregisters the accounts
		deadline := time.Now().Add(10 * time.Second)
		for completed.Load() < released && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}()
	put := func(body string) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/v8/management/config/models/only-my-names", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+namesManagementKey)
		h.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") == "" {
			t.Fatalf("PUT: %d %s", recorder.Code, recorder.Body.String())
		}
		return recorder.Header().Get("ETag")
	}
	var last uint64
	for round := range 100 {
		on := round%2 == 0
		written := put(fmt.Sprintf(`{"enabled":%t,"keep":["round-%d"]}`, on, round))
		if round%4 == 1 { // two writes, no status read between them: the second is what counts
			releaseOne() // the first write's reload, once committed (watcher reloads run one at a time)
			on = !on
			written = put(fmt.Sprintf(`{"enabled":%t,"keep":["round-%d-b"]}`, on, round))
		}
		deadline := time.Now().Add(10 * time.Second)
		var now status
		for {
			get("/v8/management/provimux/names", &now)
			if now.Applied.ConfigETag != nil && *now.Applied.ConfigETag == written {
				break
			}
			if now.Applied.Applying {
				t.Fatalf("round %d: applying %+v", round, now.Applied)
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: the status never showed the write %s: %+v", round, written, now.Applied)
			}
			time.Sleep(time.Millisecond)
		}
		// At once: no wait, registration paused.
		_, list := d.do("GET", "/v1/models", nil, "")
		status, body := d.do("POST", "/v1/chat/completions", nil, namesChat("held-compat", false))
		listed := strings.Contains(list, `"held-compat"`)
		refused := status == http.StatusBadRequest && strings.Contains(body, namesNotServed)
		if now.OnlyMyNames.Enabled != on || listed == on || refused != on || now.Applied.Applying || now.Applied.Generation <= last {
			t.Fatalf("round %d (on %v): status %+v, held-compat listed %v, refused %v (%d %s)", round, on, now, listed, refused, status, body)
		}
		last = now.Applied.Generation
		releaseOne()
	}
}
