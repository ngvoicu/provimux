package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The third review's reproductions as regressions (only-my-names.md section 17): a real service, its config
// API, its watcher and HTTP handlers over a local stand-in vendor. Nothing leaves the machine.

func reviewManagement(h *namesHarness, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/v8/management/"+path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+namesManagementKey)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	return recorder
}

// reviewHoldReload pauses every reload after its commit, before the accounts register again, until resume.
func reviewHoldReload(h *namesHarness) (resume func()) {
	release := make(chan struct{})
	var once sync.Once
	h.service.updateServerClientsContextFn = func(ctx context.Context, cfg *config.Config) bool {
		ok := h.service.server.UpdateClientsContext(ctx, cfg)
		<-release
		return ok
	}
	return func() { once.Do(func() { close(release) }) }
}

func reviewDoor(t *testing.T, setup string, vendor *namesVendor) *namesDoor {
	t.Helper()
	h := newNamesHarness(t, setup)
	server := httptest.NewServer(h.handler)
	t.Cleanup(server.Close)
	return &namesDoor{namesHarness: h, server: server, vendor: vendor}
}

func reviewLogin(t *testing.T, d *namesDoor, attrs map[string]string) *coreauth.Auth {
	t.Helper()
	auth := namesLogin("review-local.json", "claude", attrs)
	auth.FileName = auth.ID
	registered, err := d.service.coreManager.Register(coreauth.WithSkipPersist(t.Context()), auth)
	if err != nil {
		t.Fatal(err)
	}
	d.service.registerModelsForAuth(t.Context(), registered)
	d.service.coreManager.RefreshSchedulerEntry(registered.ID)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(registered.ID) })
	return registered
}

// reviewAwaitETag polls the status until it shows etag, and returns that first answer.
func reviewAwaitETag(t *testing.T, h *namesHarness, etag string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		answer := reviewManagement(h, "GET", "provimux/names", "").Body.String()
		if ojson.Get(answer, "applied.config_etag").String() == etag {
			return answer
		}
		if time.Now().After(deadline) {
			t.Fatalf("the status never showed %s: %s", etag, ojson.Get(answer, "applied"))
		}
		time.Sleep(time.Millisecond)
	}
}

// Review 1 (A): a login takes held-compat from a global oauth-model-alias row; a key offers the same spelling.
// A write removes the row; with registration paused, the first status read that shows the write's ETag is
// followed by a list without held-compat and a chat refused with the setting's message, and the stand-in
// receives nothing. The login still registers the alias: the flag decides nothing.
func TestProvimuxOnlyMyNamesRemovedGlobalAliasAtTheCommit(t *testing.T) {
	vendor := newNamesVendor(t)
	source, _ := namesClaudeCatalog(t)
	setup := fmt.Sprintf("  model-alias:\n    claude:\n      - name: %s\n        alias: held-compat\n", source) + namesDoorConfig(vendor.URL, true)
	d := reviewDoor(t, setup, vendor)
	login := reviewLogin(t, d, map[string]string{"priority": "-99", "base_url": vendor.URL + "/local-oauth"})
	before := reviewManagement(d.namesHarness, "GET", "provimux/names", "").Body.String()
	if registry.GetGlobalRegistry().GetModelCount("held-compat") != 2 || ojson.Get(before, `names.#(name=="held-compat").accounts`).Int() != 2 || !registry.ProvimuxServes("held-compat") {
		t.Fatalf("premise: held-compat is a config alias on two accounts: %s", ojson.Get(before, "names"))
	}
	resume := reviewHoldReload(d.namesHarness)
	defer resume()
	write := reviewManagement(d.namesHarness, "PUT", "config/oauth/model-alias/claude", "[]")
	if write.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", write.Code, write.Body.String())
	}
	answer := reviewAwaitETag(t, d.namesHarness, write.Header().Get("ETag"))
	_, list := d.do("GET", "/v1/models", nil, "")
	status, body := d.do("POST", "/v1/chat/completions", nil, namesChat("held-compat", false))
	if strings.Contains(list, `"held-compat"`) || status != http.StatusBadRequest || !strings.Contains(body, namesNotServed) || len(d.vendor.seen()) != 0 {
		t.Fatalf("at the first read showing the write: listed %v, chat %d %s, vendor %v", strings.Contains(list, `"held-compat"`), status, body, d.vendor.seen())
	}
	if ojson.Get(answer, `names.#(name=="held-compat")`).Exists() || ojson.Get(answer, `others.#(name=="held-compat").accounts.#`).Int() != 2 {
		t.Fatalf("status: %s / %s", ojson.Get(answer, "names"), ojson.Get(answer, `others.#(name=="held-compat")`))
	}
	if models := registry.GetGlobalRegistry().GetModelsForClient(login.ID); len(models) == 0 {
		t.Fatal("premise: the login is still registered")
	}
}

// Review 3, first case (A): an enabled login's own file names an alias whose source left its catalog, and another
// account offers the alias's spelling. The name is in others only, unlisted and refused.
func TestProvimuxOnlyMyNamesUnavailableLoginFileAlias(t *testing.T) {
	vendor := newNamesVendor(t)
	d := reviewDoor(t, namesDoorConfig(vendor.URL, true), vendor)
	reviewLogin(t, d, map[string]string{"base_url": vendor.URL + "/local-oauth", "model_aliases": `[{"name":"local-source-no-longer-in-catalog","alias":"held-compat"}]`})
	answer := reviewManagement(d.namesHarness, "GET", "provimux/names", "").Body.String()
	_, list := d.do("GET", "/v1/models", nil, "")
	status, _ := d.do("POST", "/v1/chat/completions", nil, namesChat("held-compat", false))
	if ojson.Get(answer, `names.#(name=="held-compat")`).Exists() || !ojson.Get(answer, `others.#(name=="held-compat")`).Exists() ||
		strings.Contains(list, `"held-compat"`) || status != http.StatusBadRequest {
		t.Fatalf("names %s, others %s, listed %v, chat %d", ojson.Get(answer, "names"), ojson.Get(answer, `others.#(name=="held-compat")`), strings.Contains(list, `"held-compat"`), status)
	}
}

// Review 3, second case (B): keep mixed-case with only Mixed-Case registered: the status shows no account for
// mixed-case, Mixed-Case is under others, unlisted and refused; mixed-case is refused as nobody's.
func TestProvimuxOnlyMyNamesKeepIsExact(t *testing.T) {
	vendor := newNamesVendor(t)
	source := strings.Replace(namesDoorConfig(vendor.URL, true), "name: held-compat", "name: Mixed-Case", 1)
	d := reviewDoor(t, strings.Replace(source, "enabled: true", "enabled: true\n    keep: [mixed-case]", 1), vendor)
	answer := reviewManagement(d.namesHarness, "GET", "provimux/names", "").Body.String()
	_, list := d.do("GET", "/v1/models", nil, "")
	lower, _ := d.do("POST", "/v1/chat/completions", nil, namesChat("mixed-case", false))
	upper, body := d.do("POST", "/v1/chat/completions", nil, namesChat("Mixed-Case", false))
	if ojson.Get(answer, `names.#(name=="mixed-case").accounts`).Int() != 0 || !ojson.Get(answer, `others.#(name=="Mixed-Case")`).Exists() ||
		strings.Contains(list, "Mixed-Case") || strings.Contains(list, `"mixed-case"`) || lower != http.StatusBadRequest || upper != http.StatusBadRequest || !strings.Contains(body, namesNotServed) {
		t.Fatalf("status %s / %s, listed %v, chat lower %d upper %d", ojson.Get(answer, "names"), ojson.Get(answer, "others.#.name"), strings.Contains(list, "ixed-"), lower, upper)
	}
}

// Review 3, the case-only save (B): an alias Mine written as mine. From the first status read showing the
// write's ETag, with registration paused, the status gives mine no account and the door refuses mine, as it
// refuses Mine (no longer a name); after registration mine is served and counted.
func TestProvimuxOnlyMyNamesCaseOnlySave(t *testing.T) {
	vendor := newNamesVendor(t)
	d := reviewDoor(t, strings.Replace(namesDoorConfig(vendor.URL, true), "alias: mine\n", "alias: Mine\n", 1), vendor)
	if status, _ := d.do("POST", "/v1/chat/completions", nil, namesChat("Mine", false)); status != http.StatusOK {
		t.Fatalf("premise: Mine is served: %d", status)
	}
	resume := reviewHoldReload(d.namesHarness)
	defer resume()
	write := reviewManagement(d.namesHarness, "PUT", "config/api-keys/openai-compatibility", fmt.Sprintf(`[{"name":"standin","base-url":%q,"models":[{"name":"up-mine","alias":"mine"}],"keys":[{"api-key":"sk-standin"}]}]`, vendor.URL+"/v1"))
	if write.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", write.Code, write.Body.String())
	}
	answer := reviewAwaitETag(t, d.namesHarness, write.Header().Get("ETag"))
	lower, _ := d.do("POST", "/v1/chat/completions", nil, namesChat("mine", false))
	upper, _ := d.do("POST", "/v1/chat/completions", nil, namesChat("Mine", false))
	if accounts := ojson.Get(answer, `names.#(name=="mine").accounts`).Int(); accounts != 0 || lower != http.StatusBadRequest || upper != http.StatusBadRequest {
		t.Fatalf("committed: mine has %d accounts, chat mine %d, Mine %d", accounts, lower, upper)
	}
	resume()
	deadline := time.Now().Add(10 * time.Second)
	for registry.GetGlobalRegistry().GetModelCount("mine") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	answer = reviewManagement(d.namesHarness, "GET", "provimux/names", "").Body.String()
	if lower, _ := d.do("POST", "/v1/chat/completions", nil, namesChat("mine", false)); lower != http.StatusOK || ojson.Get(answer, `names.#(name=="mine").accounts`).Int() != 1 {
		t.Fatalf("settled: chat mine %d, %s", lower, ojson.Get(answer, "names"))
	}
}

// Review 3, third case (C): local-base and local-base(high) registered, only the base kept. A request for
// local-base(high) is served (the provider receives the base), so the status puts that id under names, as a
// copy of local-base, never under others; the lists show it too.
func TestProvimuxOnlyMyNamesSuffixedIdIsJudgedAsTheDoorJudgesIt(t *testing.T) {
	vendor := newNamesVendor(t)
	source := strings.Replace(namesDoorConfig(vendor.URL, true), "name: held-compat", "name: local-base\n        - name: local-base(high)", 1)
	d := reviewDoor(t, strings.Replace(source, "enabled: true", "enabled: true\n    keep: [local-base]", 1), vendor)
	answer := reviewManagement(d.namesHarness, "GET", "provimux/names", "").Body.String()
	_, list := d.do("GET", "/v1/models", nil, "")
	status, body := d.do("POST", "/v1/chat/completions", nil, namesChat("local-base(high)", false))
	if status != http.StatusOK {
		t.Fatalf("premise: local-base(high) is served: %d %s", status, body)
	}
	if ojson.Get(answer, `others.#(name=="local-base(high)")`).Exists() || !ojson.Get(answer, `names.#(name=="local-base").copies.#(id=="local-base(high)")`).Exists() ||
		!strings.Contains(list, `"local-base(high)"`) {
		t.Fatalf("status %s / others %s, listed %v", ojson.Get(answer, "names"), ojson.Get(answer, "others.#.name"), strings.Contains(list, "local-base(high)"))
	}
}
