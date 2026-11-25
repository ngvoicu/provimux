package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// C1-C3 (only-my-names.md section 9): the switch and the names through the config API the console
// uses, the watcher's reload, and the registry, with no restart. The keys point at a closed local
// port; nothing is sent to them.

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

// await waits until every account's registered names are want (by provider).
func (h *namesHarness) await(what string, want map[string][]string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := map[string][]string{}
		for provider := range want {
			got[provider] = provimuxRegistered(h.auth(provider))
			if got[provider] == nil {
				got[provider] = []string{}
			}
		}
		if reflect.DeepEqual(got, want) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: registered %v, want %v", what, got, want)
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

// C1 and C2: off, on and off again through PUT /v8/management/config/models/only-my-names; the
// registry follows each time without a restart, and the last state equals the first. The writer
// puts the object at a path the file lacked and leaves the rest of the file as it was.
func TestProvimuxOnlyMyNamesSwitchThroughTheConfigAPI(t *testing.T) {
	h := newNamesHarness(t, namesReloadConfig)
	compat := "standin"
	h.await("at start", map[string][]string{"claude": {"mine", "plain-a"}, compat: {"shared"}})
	keyA, keyB := h.auth("claude"), h.auth(compat)
	firstA := registry.GetGlobalRegistry().GetModelsForClient(keyA.ID)
	firstB := registry.GetGlobalRegistry().GetModelsForClient(keyB.ID)
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

	h.put("models/only-my-names", `{"enabled":true,"keep":["shared"]}`)
	h.await("on, keep shared", map[string][]string{"claude": {"mine"}, compat: {"shared"}})

	h.put("models/only-my-names", `{"enabled":false,"keep":["shared"]}`)
	h.await("off", map[string][]string{"claude": {"mine", "plain-a"}, compat: {"shared"}})
	if a, b := registry.GetGlobalRegistry().GetModelsForClient(keyA.ID), registry.GetGlobalRegistry().GetModelsForClient(keyB.ID); !reflect.DeepEqual(a, firstA) || !reflect.DeepEqual(b, firstB) {
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

// C3 with a login in the auth folder as account B: its file does not change, so only the reload
// clause (internal/watcher configChangeForcesAuthRefresh) sends it through registration again.
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
			got := provimuxRegistered(loginAuth)
			if slices.Equal(got, want) || (len(got) == 0 && len(want) == 0) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the login registers %v, want %v", what, got, want)
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
