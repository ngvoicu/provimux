package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// provimuxFakeLiveSource stands in for sdk/cliproxy, which this package cannot import.
type provimuxFakeLiveSource struct {
	states map[string]ProvimuxLiveListState // by auth ID
}

func (provimuxFakeLiveSource) Providers() []string { return []string{"claude", "codex", "meta"} }

func (s provimuxFakeLiveSource) State(auth *coreauth.Auth) ProvimuxLiveListState {
	return s.states[auth.ID]
}

func provimuxUseLiveSource(t *testing.T, source ProvimuxLiveListSource) {
	t.Helper()
	previous := currentProvimuxLiveListSource()
	SetProvimuxLiveListSource(source)
	t.Cleanup(func() { SetProvimuxLiveListSource(previous) })
}

func provimuxLiveHandler(manager *coreauth.Manager, enabled bool) *Handler {
	cfg := &config.Config{}
	cfg.Models.LiveLists = new(enabled)
	return &Handler{cfg: cfg, authManager: manager}
}

func provimuxGetLiveLists(t *testing.T, h *Handler, want int) (provimuxLiveListsResponse, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/live-lists", nil)
	h.ProvimuxLiveLists(ctx)
	if recorder.Code != want {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, want, recorder.Body.String())
	}
	var body provimuxLiveListsResponse
	if want == http.StatusOK {
		if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode: %v", errDecode)
		}
	}
	return body, recorder.Body.String()
}

func provimuxLiveRow(t *testing.T, body provimuxLiveListsResponse, label string) provimuxLiveLogin {
	t.Helper()
	for _, row := range body.Logins {
		if provimuxValue(row.Login.Label) == label {
			return row
		}
	}
	t.Fatalf("no row labelled %s in %+v", label, body.Logins)
	return provimuxLiveLogin{}
}

var provimuxFetchedAt = time.Date(2026, 11, 19, 16, 10, 44, 0, time.UTC)

func TestProvimuxLiveListsLive(t *testing.T) {
	provimuxTestClock(t)
	login := provimuxTestLogin(t, "pmx-live-login", "ada@example.com")
	manager := provimuxTestManager(t, provimuxTestCredential{login, []*registry.ModelInfo{{ID: "pmx-live-a"}, {ID: "pmx-live-b"}}})
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		login.ID: {Covered: true, HasList: true, FetchedAt: provimuxFetchedAt, ListedModels: 11},
	}})
	body, _ := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	if !body.Enabled || strings.Join(body.Providers, ",") != "claude,codex,meta" || len(body.Logins) != 1 {
		t.Fatalf("top level = %+v", body)
	}
	row := provimuxLiveRow(t, body, "ada@example.com")
	if row.Source != "live" || provimuxValue(row.FetchedAt) != "2026-11-19T16:10:44Z" || intValue(row.ListedModels) != 11 || row.LastFailure != nil {
		t.Fatalf("live row = %+v", row)
	}
	if row.RegisteredModels != 2 || row.RegisteredModels != len(registry.GetGlobalRegistry().GetModelsForClient(login.ID)) {
		t.Fatalf("registered_models = %d", row.RegisteredModels)
	}
	if provimuxValue(row.Login.Name) != "claude-ada@example.com.json" || provimuxValue(row.Login.Email) != "ada@example.com" || provimuxValue(row.AuthIndex) != login.EnsureIndex() {
		t.Fatalf("row naming = %+v %v", row.Login, row.AuthIndex)
	}
}

func TestProvimuxLiveListsCatalog(t *testing.T) {
	provimuxTestClock(t)
	failure := &ProvimuxLiveListFailure{At: provimuxTestNow.Add(-2 * time.Minute), Category: "http_status", HTTPStatus: 401}
	disabled := provimuxTestLogin(t, "pmx-catalog-disabled", "disabled@example.com")
	disabled.Disabled = true
	failed := provimuxTestLogin(t, "pmx-catalog-failed", "failed@example.com")
	pending := provimuxTestLogin(t, "pmx-catalog-pending", "pending@example.com")
	manager := provimuxTestManager(t,
		provimuxTestCredential{disabled, nil},
		provimuxTestCredential{failed, nil},
		provimuxTestCredential{pending, nil},
	)
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		disabled.ID: {Covered: true},
		failed.ID:   {Covered: true, Failure: failure},
		pending.ID:  {Covered: true, Fetching: true},
	}})
	body, _ := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	for _, row := range body.Logins {
		if row.Source != "catalog" || row.FetchedAt != nil || row.ListedModels != nil {
			t.Fatalf("%s: %+v, want catalog without a list", provimuxValue(row.Login.Label), row)
		}
	}
	if row := provimuxLiveRow(t, body, "failed@example.com"); row.LastFailure == nil || intValue(row.LastFailure.HTTPStatus) != 401 {
		t.Fatalf("failed row = %+v", row)
	}
	if !provimuxLiveRow(t, body, "pending@example.com").Fetching || !provimuxLiveRow(t, body, "disabled@example.com").Disabled {
		t.Fatal("fetching/disabled not carried")
	}
}

func TestProvimuxLiveListsOptionOffWinsOverAHeldList(t *testing.T) {
	provimuxTestClock(t)
	login := provimuxTestLogin(t, "pmx-off-held", "ada@example.com")
	manager := provimuxTestManager(t, provimuxTestCredential{login, nil})
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		login.ID: {Covered: true, HasList: true, FetchedAt: provimuxFetchedAt, ListedModels: 3},
	}})
	body, _ := provimuxGetLiveLists(t, provimuxLiveHandler(manager, false), http.StatusOK)
	if row := provimuxLiveRow(t, body, "ada@example.com"); body.Enabled || row.Source != "catalog" || intValue(row.ListedModels) != 3 {
		t.Fatalf("option off with a held list: enabled=%v row=%+v", body.Enabled, row)
	}
}

func TestProvimuxLiveListsStale(t *testing.T) {
	provimuxTestClock(t)
	login := provimuxTestLogin(t, "pmx-stale", "ada@example.com")
	manager := provimuxTestManager(t, provimuxTestCredential{login, nil})
	failedAt := time.Date(2026, 11, 19, 17, 58, 2, 0, time.UTC)
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		login.ID: {Covered: true, HasList: true, FetchedAt: provimuxFetchedAt, ListedModels: 9,
			Failure: &ProvimuxLiveListFailure{At: failedAt, Category: "http_status", HTTPStatus: 401}},
	}})
	body, _ := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	row := provimuxLiveRow(t, body, "ada@example.com")
	if row.Source != "live" || row.LastFailure == nil || row.LastFailure.At != "2026-11-19T17:58:02Z" || intValue(row.LastFailure.HTTPStatus) != 401 ||
		row.LastFailure.Category != "http_status" {
		t.Fatalf("stale row = %+v failure %+v", row, row.LastFailure)
	}
}

func TestProvimuxLiveListsFailureWithoutStatus(t *testing.T) {
	provimuxTestClock(t)
	login := provimuxTestLogin(t, "pmx-nomsg", "ada@example.com")
	manager := provimuxTestManager(t, provimuxTestCredential{login, nil})
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		login.ID: {Covered: true, Failure: &ProvimuxLiveListFailure{At: provimuxTestNow, Category: "network"}},
	}})
	_, raw := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	if !strings.Contains(raw, `"last_failure":{"at":"2026-11-19T18:00:00Z","category":"network","http_status":null}`) {
		t.Fatalf("failure without a message: %s", raw)
	}
}

func TestProvimuxLiveListsRows(t *testing.T) {
	provimuxTestClock(t)
	login := provimuxTestLogin(t, "pmx-rows-login", "ada@example.com")
	key := provimuxTestKey("pmx-rows-key", "claude", "claude-apikey", "", "0")
	gemini := provimuxTestLogin(t, "pmx-rows-gemini", "gem@example.com")
	gemini.Provider = "gemini"
	manager := provimuxTestManager(t, provimuxTestCredential{login, nil}, provimuxTestCredential{key, nil}, provimuxTestCredential{gemini, nil})
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		login.ID:  {Covered: true},
		key.ID:    {},
		gemini.ID: {},
	}})
	body, _ := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	if len(body.Logins) != 1 || provimuxValue(body.Logins[0].Login.Label) != "ada@example.com" {
		t.Fatalf("rows = %+v, want only the covered login", body.Logins)
	}
}

func TestProvimuxLiveListsOrder(t *testing.T) {
	provimuxTestClock(t)
	b := provimuxTestLogin(t, "pmx-order-b", "b@example.com")
	a := provimuxTestLogin(t, "pmx-order-a", "A@example.com")
	codex := provimuxTestLogin(t, "pmx-order-codex", "c@example.com")
	codex.Provider = "codex"
	manager := provimuxTestManager(t, provimuxTestCredential{codex, nil}, provimuxTestCredential{b, nil}, provimuxTestCredential{a, nil})
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{
		a.ID: {Covered: true}, b.ID: {Covered: true}, codex.ID: {Covered: true},
	}})
	body, _ := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	got := make([]string, 0, len(body.Logins))
	for _, row := range body.Logins {
		got = append(got, provimuxValue(row.Login.Label))
	}
	if strings.Join(got, ",") != "A@example.com,b@example.com,c@example.com" {
		t.Fatalf("order = %v", got)
	}
}

func TestProvimuxLiveListsNoSource(t *testing.T) {
	provimuxTestClock(t)
	provimuxUseLiveSource(t, nil)
	_, raw := provimuxGetLiveLists(t, provimuxLiveHandler(nil, true), http.StatusServiceUnavailable)
	if !strings.Contains(raw, "live-list status unavailable") {
		t.Fatalf("body = %s", raw)
	}
}

func TestProvimuxLiveListsEmpty(t *testing.T) {
	provimuxTestClock(t)
	provimuxUseLiveSource(t, provimuxFakeLiveSource{})
	if _, raw := provimuxGetLiveLists(t, provimuxLiveHandler(nil, true), http.StatusOK); !strings.Contains(raw, `"logins":[]`) {
		t.Fatalf("no manager: %s", raw)
	}
}

func TestProvimuxLiveListsNeedsManagementKey(t *testing.T) {
	provimuxTestClock(t)
	provimuxUseLiveSource(t, provimuxFakeLiveSource{})
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, failedAttempts: make(map[string]*attemptInfo), envSecret: "test-secret"}
	router := gin.New()
	router.GET("/v8/management/provimux/live-lists", h.Middleware(), h.ProvimuxLiveLists)
	for _, tc := range []struct {
		key  string
		want int
	}{{"", http.StatusUnauthorized}, {"test-secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/v8/management/provimux/live-lists", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		if tc.key != "" {
			req.Header.Set("Authorization", "Bearer "+tc.key)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("key %q: status = %d, want %d", tc.key, rec.Code, tc.want)
		}
	}
}

// A login the credentials list hides (disabled, file removed) is still a row, but unnamed.
func TestProvimuxLiveListsHiddenLoginUnnamed(t *testing.T) {
	provimuxTestClock(t)
	gone := provimuxTestLogin(t, "pmx-hidden-gone", "gone@example.com")
	gone.Disabled = true
	gone.Status = coreauth.StatusDisabled
	gone.Attributes["path"] = gone.Attributes["path"] + ".removed"
	manager := provimuxTestManager(t, provimuxTestCredential{gone, nil})
	provimuxUseLiveSource(t, provimuxFakeLiveSource{states: map[string]ProvimuxLiveListState{gone.ID: {Covered: true}}})
	body, raw := provimuxGetLiveLists(t, provimuxLiveHandler(manager, true), http.StatusOK)
	if len(body.Logins) != 1 || strings.Contains(raw, "gone@example.com") || strings.Contains(raw, "pmx-hidden-gone") {
		t.Fatalf("hidden login named: %s", raw)
	}
}
