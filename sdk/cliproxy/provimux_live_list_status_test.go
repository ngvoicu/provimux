package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func resetProvimuxLiveListNotes() {
	provimuxLiveListNotes.mu.Lock()
	provimuxLiveListNotes.notes = make(map[string]provimuxLiveListNote)
	provimuxLiveListNotes.mu.Unlock()
}

func provimuxLiveTestReset(t *testing.T) {
	t.Helper()
	resetLiveModelLists()
	resetProvimuxLiveListNotes()
	t.Cleanup(func() {
		resetLiveModelLists()
		resetProvimuxLiveListNotes()
	})
}

func provimuxLiveLogin(id, provider string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: provider,
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"email": id + "@example.com"}}
}

func TestProvimuxLiveListStateCovered(t *testing.T) {
	provimuxLiveTestReset(t)
	source := provimuxLiveListSource{}
	for _, provider := range source.Providers() {
		auth := provimuxLiveLogin("pmx-"+provider, provider)
		if provider == "gemini" {
			// Gemini has no login here: a key on Google's own host is asked.
			auth = &coreauth.Auth{ID: "pmx-gemini-key", Provider: "gemini", Attributes: map[string]string{"api_key": "k"}}
		}
		if !source.State(auth).Covered {
			t.Fatalf("%s not covered", provider)
		}
	}
	ownHost := &coreauth.Auth{ID: "pmx-gemini-own", Provider: "gemini", Attributes: map[string]string{"api_key": "k", "base_url": "https://gemini.internal.example"}}
	if source.State(ownHost).Covered {
		t.Fatal("a Gemini key with its own base URL must never be asked")
	}
	key := &coreauth.Auth{ID: "pmx-key", Provider: "claude", Attributes: map[string]string{"api_key": "k"}}
	for name, auth := range map[string]*coreauth.Auth{"claude api key": key, "gemini login": provimuxLiveLogin("pmx-gemini", "gemini")} {
		if source.State(auth).Covered {
			t.Fatalf("%s covered", name)
		}
	}
}

func TestProvimuxLiveListStateAfterPut(t *testing.T) {
	provimuxLiveTestReset(t)
	auth := provimuxLiveLogin("pmx-put", "claude")
	now := time.Date(2026, 11, 19, 16, 10, 44, 0, time.UTC)
	liveModelLists.put(auth.ID, liveModelList{identity: liveModelListIdentity(auth), ids: map[string]struct{}{"a": {}, "b": {}}, fetchedAt: now})
	state := provimuxLiveListSource{}.State(auth)
	if !state.HasList || !state.FetchedAt.Equal(now) || state.ListedModels != 2 || state.Failure != nil || state.Fetching {
		t.Fatalf("state after put = %+v", state)
	}
	other := provimuxLiveLogin("pmx-put", "claude")
	other.Metadata["email"] = "someone-else@example.com"
	if (provimuxLiveListSource{}).State(other).HasList {
		t.Fatal("a list of another account counts as held")
	}
}

// provimuxFail does what the fetcher does on a failed fetch.
func provimuxFail(auth *coreauth.Auth, now time.Time, err error) {
	liveModelLists.fail(auth.ID, now)
	noteLiveModelListFailure(auth.ID, liveModelListIdentity(auth), err)
}

func TestProvimuxLiveListStateAfterFail(t *testing.T) {
	provimuxLiveTestReset(t)
	auth := provimuxLiveLogin("pmx-fail", "codex")
	now := time.Date(2026, 11, 19, 17, 58, 2, 0, time.UTC)
	for _, tc := range []struct {
		err      error
		category string
		status   int
	}{
		{errors.New("HTTP 401 from /private-gateway/tenant-a/v1/models"), "http_status", 401},
		{errors.New("claude model list: invalid JSON"), "invalid_response", 0},
		{errors.New("codex returned no models"), "invalid_response", 0},
		{fmt.Errorf("prepare credentials: %w", errors.New(`meta key exchange: HTTP 403 {"error":"secret-body"}`)), "credentials", 0},
		{&url.Error{Op: "Get", URL: "https://gw.internal/private/v1/models", Err: errors.New("connection refused")}, "network", 0},
		{errors.New("no claude executor"), "other", 0},
	} {
		provimuxFail(auth, now, tc.err)
		state := provimuxLiveListSource{}.State(auth)
		if state.Failure == nil {
			t.Fatalf("%v: state = %+v", tc.err, state)
		}
		if !state.Failure.At.Equal(now) || state.Failure.Category != tc.category || state.Failure.HTTPStatus != tc.status {
			t.Fatalf("%v: failure = %+v, want %s/%d", tc.err, state.Failure, tc.category, tc.status)
		}
	}
}

// The failure is the account's: another account in the same auth file waits for the retry the
// fetcher enforces on the file, without inheriting the failure.
func TestProvimuxLiveListStateReplacedAccount(t *testing.T) {
	provimuxLiveTestReset(t)
	old := provimuxLiveLogin("pmx-replaced", "claude")
	now := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	provimuxFail(old, now, errors.New("HTTP 401 from /v1/models"))
	replacement := provimuxLiveLogin("pmx-replaced", "claude")
	replacement.Metadata["email"] = "someone-else@example.com"
	state := provimuxLiveListSource{}.State(replacement)
	if state.Failure != nil || state.HasList {
		t.Fatalf("replacement state = %+v (failure %+v)", state, state.Failure)
	}
	if (provimuxLiveListSource{}).State(old).Failure == nil {
		t.Fatal("the old account's own failure is gone")
	}
}

func TestProvimuxLiveListStateFailureWithoutNote(t *testing.T) {
	provimuxLiveTestReset(t)
	auth := provimuxLiveLogin("pmx-nonote", "meta")
	now := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	liveModelLists.fail(auth.ID, now)
	state := provimuxLiveListSource{}.State(auth)
	if state.Failure != nil {
		t.Fatalf("a retry without a note of this account = %+v", state)
	}
}

func TestProvimuxLiveListSuccessClearsFailure(t *testing.T) {
	provimuxLiveTestReset(t)
	auth := provimuxLiveLogin("pmx-clear", "claude")
	now := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	provimuxFail(auth, now, errors.New("HTTP 500 from /v1/models"))
	liveModelLists.put(auth.ID, liveModelList{identity: liveModelListIdentity(auth), ids: map[string]struct{}{"a": {}}, fetchedAt: now.Add(time.Minute)})
	if state := (provimuxLiveListSource{}).State(auth); state.Failure != nil || !state.HasList {
		t.Fatalf("state after success = %+v", state)
	}
	if n := provimuxLiveListNoteCount(); n != 0 {
		t.Fatalf("notes kept after success: %d", n)
	}
}

func provimuxLiveListNoteCount() int {
	provimuxLiveListNotes.mu.Lock()
	defer provimuxLiveListNotes.mu.Unlock()
	return len(provimuxLiveListNotes.notes)
}

// Notes of removed logins go on the pruner's next tick, with no read and no new failure.
func TestProvimuxLiveListNotePrunerWithoutPolling(t *testing.T) {
	provimuxLiveTestReset(t)
	now := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	for i := 0; i < 1000; i++ {
		provimuxFail(provimuxLiveLogin(fmt.Sprintf("pmx-unpolled-%d", i), "claude"), now, errors.New("HTTP 401 from /v1/models"))
	}
	liveModelLists.forget(map[string]struct{}{})
	if n := provimuxLiveListNoteCount(); n != 1000 {
		t.Fatalf("notes before the tick = %d, want 1000", n)
	}
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		provimuxRunNotePruner(tick)
		close(done)
	}()
	tick <- now
	tick <- now // returns once the first prune has finished
	close(tick)
	<-done
	if n := provimuxLiveListNoteCount(); n != 0 {
		t.Fatalf("notes after the tick = %d, want 0", n)
	}
}

// Notes of removed logins also go at the next read.
func TestProvimuxLiveListNotesOfRemovedLogins(t *testing.T) {
	provimuxLiveTestReset(t)
	now := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	for i := 0; i < 1000; i++ {
		provimuxFail(provimuxLiveLogin(fmt.Sprintf("pmx-removed-%d", i), "claude"), now, errors.New("HTTP 401 from /v1/models"))
	}
	liveModelLists.forget(map[string]struct{}{})
	provimuxLiveListSource{}.State(provimuxLiveLogin("pmx-other", "claude"))
	if n := provimuxLiveListNoteCount(); n != 0 {
		t.Fatalf("notes of removed logins kept: %d", n)
	}
}

// A newer failure recorded between two reads is reported whole by the second read.
func TestProvimuxLiveListNewerFailureSurvivesReads(t *testing.T) {
	provimuxLiveTestReset(t)
	auth := provimuxLiveLogin("pmx-newer", "claude")
	first := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	provimuxFail(auth, first, errors.New("HTTP 500 from /v1/models"))
	provimuxLiveListSource{}.State(auth)
	liveModelLists.fail(auth.ID, first.Add(time.Hour)) // a newer failure: the old note no longer matches
	if state := (provimuxLiveListSource{}).State(auth); state.Failure != nil {
		t.Fatalf("an older failure's note reported for a newer failure: %+v", state.Failure)
	}
	noteLiveModelListFailure(auth.ID, liveModelListIdentity(auth), errors.New("HTTP 401 from /v1/models"))
	state := provimuxLiveListSource{}.State(auth)
	if state.Failure == nil || state.Failure.HTTPStatus != 401 || !state.Failure.At.Equal(first.Add(time.Hour)) {
		t.Fatalf("newer failure = %+v", state.Failure)
	}
}

func TestProvimuxLiveListStateInFlight(t *testing.T) {
	provimuxLiveTestReset(t)
	auth := provimuxLiveLogin("pmx-flight", "claude")
	if !liveModelLists.start(auth.ID) {
		t.Fatal("start")
	}
	if !(provimuxLiveListSource{}).State(auth).Fetching {
		t.Fatal("running fetch not reported")
	}
	liveModelLists.finish(auth.ID)
	if (provimuxLiveListSource{}).State(auth).Fetching {
		t.Fatal("finished fetch still reported")
	}
}

// Registered at init: in this binary, which links sdk/cliproxy, the endpoint answers 200, not 503.
func TestProvimuxLiveListSourceRegisteredAtInit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/live-lists", nil)
	management.NewHandlerWithoutConfigFilePath(&config.Config{}, nil).ProvimuxLiveLists(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}

// End to end through the fetcher: a 401 from the provider becomes the failure the page shows.
func TestProvimuxLiveListFetcherRecordsFailure(t *testing.T) {
	provimuxLiveTestReset(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "token expired", http.StatusUnauthorized)
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Models.LiveLists = new(true)
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	executor := &liveListExecutor{provider: "claude"}
	svc.coreManager.RegisterExecutor(executor)
	auth, errRegister := svc.coreManager.Register(t.Context(), &coreauth.Auth{ID: "pmx-e2e", Provider: "claude",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL},
		Metadata:   map[string]any{"email": "ada@example.com"}})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	svc.refreshLiveModelLists(t.Context(), true)
	liveModelLists.fetches.Wait()
	state := provimuxLiveListSource{}.State(auth)
	if state.HasList || state.Failure == nil || state.Failure.HTTPStatus != http.StatusUnauthorized || state.Failure.Category != "http_status" {
		t.Fatalf("state after a 401 = %+v (failure %+v)", state, state.Failure)
	}
}

// provimuxStatusExecutor answers the model-list request with a fixed status.
type provimuxStatusExecutor struct {
	coreauth.ProviderExecutor
	status int
}

func (provimuxStatusExecutor) Identifier() string { return "claude" }

func (e provimuxStatusExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: e.status, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"pmx-listed-model"}],"has_more":false}`))}, nil
}

// A forced refresh that ends 250 ms after the click reads, on the wire, as later than the click:
// the console waits for fetched_at or last_failure.at after its click.
func TestProvimuxLiveListRefreshLaterThanClick(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			provimuxLiveTestReset(t)
			click := time.Date(2026, 11, 19, 10, 0, 0, 200_000_000, time.UTC)
			done := click.Add(250 * time.Millisecond)
			prevNow := liveModelListNow
			liveModelListNow = func() time.Time { return done }
			t.Cleanup(func() { liveModelListNow = prevNow })
			cfg := &config.Config{}
			cfg.Models.LiveLists = new(true)
			svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
			svc.coreManager.RegisterExecutor(provimuxStatusExecutor{status: status})
			auth, errRegister := svc.coreManager.Register(t.Context(), provimuxLiveLogin("pmx-click", "claude"))
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			svc.refreshLiveModelLists(t.Context(), true)
			liveModelLists.fetches.Wait()

			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/live-lists", nil)
			management.NewHandlerWithoutConfigFilePath(cfg, svc.coreManager).ProvimuxLiveLists(ctx)
			var body struct {
				Logins []struct {
					Fetching    bool    `json:"fetching"`
					FetchedAt   *string `json:"fetched_at"`
					LastFailure *struct {
						At string `json:"at"`
					} `json:"last_failure"`
				} `json:"logins"`
			}
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &body); errDecode != nil || len(body.Logins) != 1 {
				t.Fatalf("decode %s: %v", recorder.Body.String(), errDecode)
			}
			row := body.Logins[0]
			field := ""
			switch {
			case status == http.StatusOK && row.FetchedAt != nil:
				field = *row.FetchedAt
			case status != http.StatusOK && row.LastFailure != nil:
				field = row.LastFailure.At
			default:
				t.Fatalf("no completion field in %s", recorder.Body.String())
			}
			at, errParse := time.Parse(time.RFC3339Nano, field)
			if errParse != nil || row.Fetching || !at.After(click) || !at.Equal(done) {
				t.Fatalf("click=%s done=%s wire=%s fetching=%v: not recognised as later than the click",
					click.Format(time.RFC3339Nano), done.Format(time.RFC3339Nano), field, row.Fetching)
			}
		})
	}
}

// The first failure starts the production pruner, which then drops notes of removed logins on its
// own clock. Fails if the startup in noteLiveModelListFailure is removed.
func TestProvimuxLiveListPrunerStartsOnFirstFailure(t *testing.T) {
	provimuxLiveTestReset(t)
	tick := make(chan time.Time)
	prevTick := provimuxNotePrunerTick
	provimuxNotePrunerTick = func() <-chan time.Time { return tick }
	provimuxNotePrunerOnce = sync.Once{}
	t.Cleanup(func() {
		provimuxNotePrunerTick = prevTick
		close(tick) // ends the test's pruner
	})
	now := time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC)
	for i := 0; i < 1000; i++ {
		provimuxFail(provimuxLiveLogin(fmt.Sprintf("pmx-started-%d", i), "claude"), now, errors.New("HTTP 401 from /v1/models"))
	}
	liveModelLists.forget(map[string]struct{}{})
	for i := 0; i < 2; i++ { // the second send returns once the first prune has finished
		select {
		case tick <- now:
		case <-time.After(5 * time.Second):
			t.Fatal("no pruner was started by the first failure")
		}
	}
	if n := provimuxLiveListNoteCount(); n != 0 {
		t.Fatalf("notes after the production pruner's tick = %d, want 0", n)
	}
}
