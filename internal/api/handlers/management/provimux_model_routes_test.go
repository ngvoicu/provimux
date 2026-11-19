package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var provimuxTestNow = time.Date(2026, 11, 19, 18, 0, 0, 0, time.UTC)

// provimuxTestClock fixes the endpoints' clock.
func provimuxTestClock(t *testing.T) {
	t.Helper()
	prevNow := provimuxNow
	provimuxNow = func() time.Time { return provimuxTestNow }
	t.Cleanup(func() { provimuxNow = prevNow })
}

type provimuxTestCredential struct {
	auth   *coreauth.Auth
	models []*registry.ModelInfo
}

// provimuxTestManager registers credentials on a new manager and their models in the global registry.
func provimuxTestManager(t *testing.T, credentials ...provimuxTestCredential) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, credential := range credentials {
		if _, errRegister := manager.Register(context.Background(), credential.auth); errRegister != nil {
			t.Fatalf("register %s: %v", credential.auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.auth.ID, credential.auth.Provider, credential.models)
		id := credential.auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return manager
}

func provimuxTestLogin(t *testing.T, id, email string) *coreauth.Auth {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-"+email+".json")
	if errWrite := os.WriteFile(path, []byte("{}"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return &coreauth.Auth{
		ID:         id,
		Provider:   "claude",
		FileName:   filepath.Base(path),
		Label:      email,
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth, "path": path, "priority": "10"},
		Metadata:   map[string]any{"email": email, "access_token": "secret-access-token", "refresh_token": "secret-refresh-token"},
	}
}

func provimuxTestKey(id, provider, label, baseURL string, priority string) *coreauth.Auth {
	attributes := map[string]string{coreauth.AttributeAPIKey: "secret-key", "priority": priority, "source": "config:" + provider + "[0]"}
	if baseURL != "" {
		attributes["base_url"] = baseURL
	}
	return &coreauth.Auth{ID: id, Provider: provider, Label: label, Status: coreauth.StatusActive, Attributes: attributes}
}

func provimuxGetModelRoutes(t *testing.T, h *Handler, target string) (provimuxModelRoutesResponse, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h.ProvimuxModelRoutes(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, recorder.Code, recorder.Body.String())
	}
	var body provimuxModelRoutesResponse
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	return body, recorder.Body.String()
}

func provimuxFindModel(t *testing.T, body provimuxModelRoutesResponse, id string) provimuxModel {
	t.Helper()
	for _, model := range body.Models {
		if model.ID == id {
			return model
		}
	}
	t.Fatalf("model %s not in the response", id)
	return provimuxModel{}
}

func provimuxRouteOf(t *testing.T, model provimuxModel, label string) provimuxRoute {
	t.Helper()
	for _, route := range model.Routes {
		if provimuxValue(route.Login.Label) == label {
			return route
		}
	}
	t.Fatalf("model %s has no route labelled %s", model.ID, label)
	return provimuxRoute{}
}

func intValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func TestProvimuxModelRoutesListsRoutesOfEachModel(t *testing.T) {
	provimuxTestClock(t)
	model := "pmx-routes-model"
	login := provimuxTestLogin(t, "pmx-routes-login", "ada@example.com")
	key := provimuxTestKey("pmx-routes-key", "claude", "claude-apikey", "https://API.Anthropic.com:443/v1", "0")
	compat := provimuxTestKey("pmx-routes-compat", "openai-compatible-openrouter", "openrouter", "https://openrouter.ai/api/v1", "5")
	off := provimuxTestLogin(t, "pmx-routes-off", "off@example.com")
	off.Disabled = true
	manager := provimuxTestManager(t,
		provimuxTestCredential{login, []*registry.ModelInfo{{ID: model}}},
		provimuxTestCredential{key, []*registry.ModelInfo{{ID: model}}},
		provimuxTestCredential{compat, []*registry.ModelInfo{{ID: model}}},
		provimuxTestCredential{off, []*registry.ModelInfo{{ID: model}}},
	)
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	routes := provimuxFindModel(t, body, model).Routes
	if len(routes) != 3 {
		t.Fatalf("routes = %d, want 3 (the disabled login absent): %+v", len(routes), routes)
	}
	want := []struct {
		provider, kind, host string
		priority             int
	}{
		{"claude", "oauth", "", 10},
		{"openai-compatible-openrouter", "apikey", "openrouter.ai", 5},
		{"claude", "apikey", "api.anthropic.com", 0},
	}
	for i, w := range want {
		r := routes[i]
		if r.Provider != w.provider || provimuxValue(r.Kind) != w.kind || provimuxValue(r.Host) != w.host || r.Priority != w.priority {
			t.Fatalf("route %d = %s/%s/%s/%d, want %+v", i, r.Provider, provimuxValue(r.Kind), provimuxValue(r.Host), r.Priority, w)
		}
		if r.AuthIndex == nil || *r.AuthIndex == "" {
			t.Fatalf("route %d has no auth_index", i)
		}
		if r.RecordedCooldown != nil {
			t.Fatalf("route %d: recorded_cooldown=%+v", i, r.RecordedCooldown)
		}
	}
	if *routes[0].AuthIndex != login.EnsureIndex() {
		t.Fatalf("auth_index = %s, want the credentials list's %s", *routes[0].AuthIndex, login.EnsureIndex())
	}
}

func TestProvimuxModelRoutesNamesLoginsAsCredentialsList(t *testing.T) {
	provimuxTestClock(t)
	model := "pmx-names-model"
	login := provimuxTestLogin(t, "pmx-names-login", "ada@example.com")
	key := provimuxTestKey("pmx-names-key", "claude", "claude-apikey", "", "0")
	manager := provimuxTestManager(t,
		provimuxTestCredential{login, []*registry.ModelInfo{{ID: model}}},
		provimuxTestCredential{key, []*registry.ModelInfo{{ID: model}}},
	)
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	m := provimuxFindModel(t, body, model)
	got := provimuxRouteOf(t, m, "ada@example.com").Login
	if provimuxValue(got.Name) != "claude-ada@example.com.json" || provimuxValue(got.Email) != "ada@example.com" {
		t.Fatalf("login = %+v", got)
	}
	keyLogin := provimuxRouteOf(t, m, "claude-apikey").Login
	if keyLogin.Name != nil || keyLogin.Email != nil {
		t.Fatalf("configured key login = name %v email %v, want both null", keyLogin.Name, keyLogin.Email)
	}
	if host := provimuxRouteOf(t, m, "claude-apikey").Host; host != nil {
		t.Fatalf("key without base URL has host %q", *host)
	}
}

func TestProvimuxModelRoutesCarriesNoSecret(t *testing.T) {
	provimuxTestClock(t)
	model := "pmx-hidden-model"
	compat := provimuxTestKey("pmx-secret-compat-id", "openai-compatible-mine", "mine", "https://user:secret-pass@OpenRouter.ai:443/api/v1?token=secret-token", "0")
	compat.Metadata = map[string]any{"access_token": "secret-access", "refresh_token": "secret-refresh"}
	compat.StatusMessage = "upstream said secret-body"
	compat.LastError = &coreauth.Error{Message: "upstream said secret-body", HTTPStatus: 429}
	compat.ModelStates = map[string]*coreauth.ModelState{model: {
		Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: provimuxTestNow.Add(time.Minute),
		StatusMessage: "upstream said secret-body", LastError: &coreauth.Error{Message: "upstream said secret-body", HTTPStatus: 429},
	}}
	login := provimuxTestLogin(t, "pmx-hidden-login", "ada@example.com")
	manager := provimuxTestManager(t,
		provimuxTestCredential{compat, []*registry.ModelInfo{{ID: model}}},
		provimuxTestCredential{login, []*registry.ModelInfo{{ID: model}}},
	)
	body, raw := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	for _, secret := range []string{"secret", "pmx-secret-compat-id", "pmx-hidden-login", "/api/v1", "user:", "token"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("response contains %q: %s", secret, raw)
		}
	}
	if host := provimuxValue(provimuxRouteOf(t, provimuxFindModel(t, body, model), "mine").Host); host != "openrouter.ai" {
		t.Fatalf("host = %q, want openrouter.ai", host)
	}
}

func TestProvimuxModelRoutesLimitsPerRouteAndModel(t *testing.T) {
	provimuxTestClock(t)
	model := "pmx-limits-model"
	big := provimuxTestKey("pmx-limits-big", "claude", "big", "", "0")
	small := provimuxTestKey("pmx-limits-small", "claude", "small", "", "0")
	manager := provimuxTestManager(t,
		provimuxTestCredential{big, []*registry.ModelInfo{{ID: model, ContextLength: 1_000_000, MaxCompletionTokens: 64_000}}},
		provimuxTestCredential{small, []*registry.ModelInfo{{ID: model, ContextLength: 200_000, MaxCompletionTokens: 32_000}}},
	)
	h := &Handler{cfg: &config.Config{}, authManager: manager}
	body, _ := provimuxGetModelRoutes(t, h, "/x")
	m := provimuxFindModel(t, body, model)
	if got := intValue(provimuxRouteOf(t, m, "big").Limits.Context); got != 1_000_000 {
		t.Fatalf("big route context = %d", got)
	}
	if got := intValue(provimuxRouteOf(t, m, "small").Limits.Context); got != 200_000 {
		t.Fatalf("small route context = %d", got)
	}
	if intValue(m.Limits.Context) != 200_000 || intValue(m.Limits.Output) != 32_000 {
		t.Fatalf("model limits = %d/%d, want 200000/32000", intValue(m.Limits.Context), intValue(m.Limits.Output))
	}

	silent := provimuxTestKey("pmx-limits-silent", "claude", "silent", "", "0")
	if _, errRegister := manager.Register(context.Background(), silent); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(silent.ID, silent.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(silent.ID) })
	body, _ = provimuxGetModelRoutes(t, h, "/x")
	m = provimuxFindModel(t, body, model)
	if m.Limits.Context != nil {
		t.Fatalf("model context = %d with a route declaring none, want null", *m.Limits.Context)
	}
	if route := provimuxRouteOf(t, m, "silent"); route.Limits.Context != nil || route.Limits.Output != nil {
		t.Fatalf("silent route limits = %+v, want null", route.Limits)
	}
	if got := intValue(provimuxRouteOf(t, m, "big").Limits.Context); got != 1_000_000 {
		t.Fatalf("big route context after a silent route = %d", got)
	}
}

func TestProvimuxModelRoutesLimitsReadEveryField(t *testing.T) {
	provimuxTestClock(t)
	gemini := provimuxTestKey("pmx-fields-gemini", "gemini", "gemini", "", "0")
	override := provimuxTestKey("pmx-fields-override", "codex", "override", "", "0")
	manager := provimuxTestManager(t,
		provimuxTestCredential{gemini, []*registry.ModelInfo{{ID: "pmx-fields-gemini-model", InputTokenLimit: 30_000, OutputTokenLimit: 8_000}}},
		provimuxTestCredential{override, []*registry.ModelInfo{{ID: "pmx-fields-override-model", ContextLength: 400_000, MaxContextLength: 272_000, MaxCompletionTokens: 128_000}}},
	)
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	g := provimuxFindModel(t, body, "pmx-fields-gemini-model")
	if r := g.Routes[0].Limits; intValue(r.Context) != 30_000 || intValue(r.Output) != 8_000 {
		t.Fatalf("gemini-style limits = %d/%d", intValue(r.Context), intValue(r.Output))
	}
	if intValue(g.Limits.Context) != 30_000 || intValue(g.Limits.Output) != 8_000 {
		t.Fatalf("gemini-style model limits = %d/%d", intValue(g.Limits.Context), intValue(g.Limits.Output))
	}
	o := provimuxFindModel(t, body, "pmx-fields-override-model")
	if r := o.Routes[0].Limits; intValue(r.Context) != 272_000 || intValue(r.Output) != 128_000 {
		t.Fatalf("override limits = %d/%d", intValue(r.Context), intValue(r.Output))
	}
}

func TestProvimuxModelRoutesReasoning(t *testing.T) {
	provimuxTestClock(t)
	key := provimuxTestKey("pmx-reasoning-key", "claude", "k", "", "0")
	manager := provimuxTestManager(t, provimuxTestCredential{key, []*registry.ModelInfo{
		{ID: "pmx-reasoning-full", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}, Min: 1024, Max: 64000, ZeroAllowed: true}},
		{ID: "pmx-reasoning-levels", Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}},
		{ID: "pmx-reasoning-budget", Thinking: &registry.ThinkingSupport{Min: 128, Max: 32768, DynamicAllowed: true}},
		{ID: "pmx-reasoning-none"},
	}})
	body, raw := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	full := provimuxFindModel(t, body, "pmx-reasoning-full").Routes[0].Reasoning
	if full == nil || strings.Join(full.Levels, ",") != "low,high" || full.Budget == nil || full.Budget.Min != 1024 || full.Budget.Max != 64000 || !full.Budget.ZeroAllowed || full.Budget.DynamicAllowed {
		t.Fatalf("full reasoning = %+v", full)
	}
	if levels := provimuxFindModel(t, body, "pmx-reasoning-levels").Routes[0].Reasoning; levels == nil || levels.Budget != nil || len(levels.Levels) != 1 {
		t.Fatalf("levels-only reasoning = %+v", levels)
	}
	budget := provimuxFindModel(t, body, "pmx-reasoning-budget").Routes[0].Reasoning
	if budget == nil || budget.Levels == nil || len(budget.Levels) != 0 || budget.Budget == nil || !budget.Budget.DynamicAllowed {
		t.Fatalf("budget-only reasoning = %+v", budget)
	}
	if !strings.Contains(raw, `"levels":[]`) {
		t.Fatalf("budget-only levels not an empty array: %s", raw)
	}
	if none := provimuxFindModel(t, body, "pmx-reasoning-none").Routes[0].Reasoning; none != nil {
		t.Fatalf("reasoning without Thinking = %+v, want null", none)
	}
}

func provimuxQuotaModelState(until time.Time, status int) *coreauth.ModelState {
	return &coreauth.ModelState{
		Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: until,
		Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: until, BackoffLevel: 2},
		LastError: &coreauth.Error{HTTPStatus: status, Message: "quota"},
	}
}

func TestProvimuxModelRoutesCooldownOfOneModel(t *testing.T) {
	provimuxTestClock(t)
	key := provimuxTestKey("pmx-cool-key", "claude", "k", "", "0")
	key.ModelStates = map[string]*coreauth.ModelState{"pmx-cool-a": provimuxQuotaModelState(provimuxTestNow.Add(10*time.Minute), 429)}
	manager := provimuxTestManager(t, provimuxTestCredential{key, []*registry.ModelInfo{{ID: "pmx-cool-a"}, {ID: "pmx-cool-b"}}})
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	cooldown := provimuxFindModel(t, body, "pmx-cool-a").Routes[0].RecordedCooldown
	if cooldown == nil || cooldown.Scope != "model" || provimuxValue(cooldown.Model) != "pmx-cool-a" || cooldown.Reason != "quota" || cooldown.Until != "2026-11-19T18:10:00Z" {
		t.Fatalf("cooldown of a = %+v", cooldown)
	}
	if intValue(cooldown.HTTPStatus) != 429 || intValue(cooldown.BackoffLevel) != 2 {
		t.Fatalf("cooldown status/backoff = %d/%d", intValue(cooldown.HTTPStatus), intValue(cooldown.BackoffLevel))
	}
	if other := provimuxFindModel(t, body, "pmx-cool-b").Routes[0].RecordedCooldown; other != nil {
		t.Fatalf("cooldown of b = %+v, want null", other)
	}
}

func TestProvimuxModelRoutesCooldownOfCredential(t *testing.T) {
	provimuxTestClock(t)
	key := provimuxTestKey("pmx-credcool-key", "claude", "k", "", "0")
	key.Quota = coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: provimuxTestNow.Add(time.Hour)}
	manager := provimuxTestManager(t, provimuxTestCredential{key, []*registry.ModelInfo{{ID: "pmx-credcool-a"}, {ID: "pmx-credcool-b"}}})
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	for _, id := range []string{"pmx-credcool-a", "pmx-credcool-b"} {
		cooldown := provimuxFindModel(t, body, id).Routes[0].RecordedCooldown
		if cooldown == nil || cooldown.Scope != "credential" || cooldown.Model != nil || cooldown.Reason != "credential_quota" {
			t.Fatalf("%s cooldown = %+v, want the credential's", id, cooldown)
		}
	}
}

func TestProvimuxModelRoutesModelFilter(t *testing.T) {
	provimuxTestClock(t)
	key := provimuxTestKey("pmx-filter-key", "claude", "k", "", "0")
	manager := provimuxTestManager(t, provimuxTestCredential{key, []*registry.ModelInfo{{ID: "pmx-filter-a"}, {ID: "pmx-filter-b"}, {ID: "pmx-filter-c"}}})
	h := &Handler{cfg: &config.Config{}, authManager: manager}
	ids := func(target string) string {
		body, _ := provimuxGetModelRoutes(t, h, target)
		out := make([]string, 0, len(body.Models))
		for _, model := range body.Models {
			out = append(out, model.ID)
		}
		return strings.Join(out, ",")
	}
	for target, want := range map[string]string{
		"/x?model=pmx-filter-b":                    "pmx-filter-b",
		"/x?model=pmx-filter-c&model=pmx-filter-a": "pmx-filter-a,pmx-filter-c",
		"/x?model=":                   "",
		"/x?model=PMX-FILTER-A":       "",
		"/x?model=pmx-filter-unknown": "",
	} {
		if got := ids(target); got != want {
			t.Fatalf("%s: models = %q, want %q", target, got, want)
		}
	}
	if _, raw := provimuxGetModelRoutes(t, h, "/x?model="); !strings.Contains(raw, `"models":[]`) {
		t.Fatalf("probe body = %s", raw)
	}
}

func TestProvimuxModelRoutesEmpty(t *testing.T) {
	provimuxTestClock(t)
	for name, h := range map[string]*Handler{
		"no credentials": {cfg: &config.Config{}, authManager: coreauth.NewManager(nil, nil, nil)},
		"no manager":     {cfg: &config.Config{}},
	} {
		body, raw := provimuxGetModelRoutes(t, h, "/x")
		if !strings.Contains(raw, `"models":[]`) || body.ObservedAt != "2026-11-19T18:00:00Z" {
			t.Fatalf("%s: body = %s", name, raw)
		}
	}
}

func TestProvimuxModelRoutesNeedsManagementKey(t *testing.T) {
	provimuxTestClock(t)
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, failedAttempts: make(map[string]*attemptInfo), envSecret: "test-secret"}
	router := gin.New()
	router.GET("/v8/management/provimux/model-routes", h.Middleware(), h.ProvimuxModelRoutes)
	for _, tc := range []struct {
		key  string
		want int
	}{{"", http.StatusUnauthorized}, {"wrong", http.StatusUnauthorized}, {"test-secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-routes", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		if tc.key != "" {
			req.Header.Set("Authorization", "Bearer "+tc.key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("key %q: status = %d, want %d (%s)", tc.key, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// Runtime credentials (AI Studio uses its channel ID as ID, label and email) and logins the
// credentials list hides carry no name: auth_index identifies them.
func TestProvimuxModelRoutesNamesOnlyListedCredentials(t *testing.T) {
	provimuxTestClock(t)
	model := "pmx-unnamed-model"
	channel := &coreauth.Auth{ID: "aistudio-channel-7f3a", Provider: "aistudio", Label: "aistudio-channel-7f3a", Status: coreauth.StatusActive,
		Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"email": "aistudio-channel-7f3a"}}
	gone := provimuxTestLogin(t, "pmx-unnamed-gone", "gone@example.com")
	gone.Status = coreauth.StatusDisabled
	gone.Attributes["path"] = gone.Attributes["path"] + ".removed"
	manager := provimuxTestManager(t,
		provimuxTestCredential{channel, []*registry.ModelInfo{{ID: model}}},
		provimuxTestCredential{gone, []*registry.ModelInfo{{ID: model}}},
	)
	body, raw := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	if strings.Contains(raw, "aistudio-channel-7f3a") || strings.Contains(raw, "gone@example.com") || strings.Contains(raw, "pmx-unnamed-gone") {
		t.Fatalf("an unlisted credential is named: %s", raw)
	}
	for _, route := range provimuxFindModel(t, body, model).Routes {
		if route.Login.Name != nil || route.Login.Email != nil || route.Login.Label != nil || route.AuthIndex == nil {
			t.Fatalf("route %+v, want an unnamed login with an auth_index", route)
		}
	}
}

// provimuxWireTime parses a time from the response.
func provimuxWireTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, errParse := time.Parse(time.RFC3339Nano, value)
	if errParse != nil {
		t.Fatalf("time %q: %v", value, errParse)
	}
	return parsed
}

// A cooldown in its last second still ends after observed_at on the wire.
func TestProvimuxModelRoutesCooldownLastSecond(t *testing.T) {
	prevNow := provimuxNow
	now := time.Date(2026, 11, 19, 17, 27, 4, 100_000_000, time.UTC)
	provimuxNow = func() time.Time { return now }
	t.Cleanup(func() { provimuxNow = prevNow })
	key := provimuxTestKey("pmx-lastsec-key", "claude", "k", "", "0")
	key.ModelStates = map[string]*coreauth.ModelState{"pmx-lastsec": provimuxQuotaModelState(now.Add(775*time.Millisecond), 429)}
	manager := provimuxTestManager(t, provimuxTestCredential{key, []*registry.ModelInfo{{ID: "pmx-lastsec"}}})
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	cooldown := provimuxFindModel(t, body, "pmx-lastsec").Routes[0].RecordedCooldown
	if cooldown == nil {
		t.Fatal("no recorded cooldown")
	}
	observed, until := provimuxWireTime(t, body.ObservedAt), provimuxWireTime(t, cooldown.Until)
	if left := until.Sub(observed); left != 775*time.Millisecond {
		t.Fatalf("observed_at=%s until=%s: %s left on the wire, want 775ms", body.ObservedAt, cooldown.Until, left)
	}
}

// The wire carries the recorded cooldown's end exactly, fraction of a second included.
func TestProvimuxModelRoutesCooldownKeepsFraction(t *testing.T) {
	provimuxTestClock(t)
	stored := provimuxTestNow.Add(10*time.Minute + 875*time.Millisecond)
	key := provimuxTestKey("pmx-fraction-key", "claude", "k", "", "0")
	key.Quota = coreauth.QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: stored}
	manager := provimuxTestManager(t, provimuxTestCredential{key, []*registry.ModelInfo{{ID: "pmx-fraction"}}})
	body, _ := provimuxGetModelRoutes(t, &Handler{cfg: &config.Config{}, authManager: manager}, "/x")
	cooldown := provimuxFindModel(t, body, "pmx-fraction").Routes[0].RecordedCooldown
	if cooldown == nil || !provimuxWireTime(t, cooldown.Until).Equal(stored) {
		t.Fatalf("until on the wire = %+v, stored %s", cooldown, stored.Format(time.RFC3339Nano))
	}
}
