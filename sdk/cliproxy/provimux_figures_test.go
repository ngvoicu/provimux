package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	moejson "github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists/pdentries"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// The figures in the listings' round, held, on disk and shown on GET /v8/management/provimux/figures.
// Every fetch goes to pdentries, a stand-in of providers.dev's API on loopback; none reaches the real site.

// provimuxFiguresReset gives the test an empty figures store, a clock it sets, and puts everything back.
func provimuxFiguresReset(t *testing.T) {
	t.Helper()
	reset := func() {
		st := provimuxFigures
		st.mu.Lock()
		st.doc, st.fromCache, st.cacheRead, st.failure, st.failures, st.next, st.lastLogged = nil, false, false, nil, 0, time.Time{}, ""
		st.mu.Unlock()
	}
	reset()
	previousBase, previousTimeout := provimuxFiguresBase, provimuxFiguresPageTimeout
	t.Cleanup(func() {
		reset()
		provimuxFiguresBase, provimuxFiguresPageTimeout = previousBase, previousTimeout
	})
}

// provimuxFiguresEntries is n entry summaries: the real ones of testdata (dataset c37885999c67cfaa) and copies.
func provimuxFiguresEntries(t *testing.T, n int) (string, string, []moejson.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "provimux", "modellists", "testdata", "figures-summaries.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version   string               `json:"dataset_version"`
		Generated string               `json:"generated_at"`
		Entries   []moejson.RawMessage `json:"entries"`
	}
	if err = moejson.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	out := append([]moejson.RawMessage(nil), fixture.Entries...)
	for i := 0; len(out) < n; i++ {
		out = append(out, pdentries.Copy(fixture.Entries[i%len(fixture.Entries)], fmt.Sprintf("provimux-test-copy-%03d", i)))
	}
	return fixture.Version, fixture.Generated, out
}

// provimuxFiguresStandIn starts the stand-in with 450 entries (three pages) and points the store at it.
func provimuxFiguresStandIn(t *testing.T) *pdentries.Server {
	t.Helper()
	version, generated, entries := provimuxFiguresEntries(t, 450)
	server := pdentries.New(entries, version, generated)
	t.Cleanup(server.Close)
	provimuxFiguresBase = server.EntriesURL()
	return server
}

// provimuxCaptureLog records the standard logger's entries for the test and puts its hooks back after.
func provimuxCaptureLog(t *testing.T) *logtest.Hook {
	t.Helper()
	hook := new(logtest.Hook)
	previous := log.StandardLogger().ReplaceHooks(log.LevelHooks{})
	log.AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(previous) })
	return hook
}

// provimuxPublishedNowhere points the listings' request at a closed port, so a test that runs the round
// with the switch on never reaches providers.dev for the listings either.
func provimuxPublishedNowhere(t *testing.T) {
	t.Helper()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	previous := provimuxPublishedBase
	provimuxPublishedBase = closed.URL + "/api/v1/listings"
	t.Cleanup(func() { provimuxPublishedBase = previous })
}

// provimuxFiguresSeedDisk writes the last run's disk copy into dir's model-lists folder: the stand-in's 450
// entries, fetched at provimuxFiguresSeededAt (after the fixture's dataset was generated).
func provimuxFiguresSeedDisk(t *testing.T, dir string) {
	t.Helper()
	seedServer := provimuxFiguresStandIn(t)
	seed, err := modellists.FetchFigures(t.Context(), seedServer.Client(), seedServer.EntriesURL(), nil, "provimux/test",
		provimuxFiguresSeededAt, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = modellists.NewStore(filepath.Join(dir, modellists.FolderName)).WriteFigures(seed.Document); err != nil {
		t.Fatal(err)
	}
}

// date-safe: the time the seeded copy was fetched, the fixture's day; it is only read back, never compared with today.
var provimuxFiguresSeededAt = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func provimuxFiguresConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Models.PublishedLists = new(true)
	return cfg
}

type provimuxFiguresAnswer struct {
	ObservedAt string `json:"observed_at"`
	Options    struct {
		PublishedLists bool `json:"published_lists"`
	} `json:"options"`
	Figures struct {
		Contract       int                           `json:"contract"`
		Source         string                        `json:"source"`
		State          string                        `json:"state"`
		DatasetVersion *string                       `json:"dataset_version"`
		GeneratedAt    *string                       `json:"generated_at"`
		FetchedAt      *string                       `json:"fetched_at"`
		Entries        map[string]moejson.RawMessage `json:"entries"`
	} `json:"figures"`
	HeldEntries int  `json:"held_entries"`
	FromCache   bool `json:"from_cache"`
	LastFailure *struct {
		At         string `json:"at"`
		Category   string `json:"category"`
		HTTPStatus *int   `json:"http_status"`
	} `json:"last_failure"`
}

// provimuxFiguresGet answers GET /v8/management/provimux/figures as the server does, and how long it took.
func provimuxFiguresGet(t *testing.T, cfg *config.Config, query string) (provimuxFiguresAnswer, string, time.Duration) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := management.NewHandlerWithoutConfigFilePath(cfg, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/figures"+query, nil)
	started := time.Now()
	handler.ProvimuxFigures(ctx)
	elapsed := time.Since(started)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var answer provimuxFiguresAnswer
	if err := moejson.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer, recorder.Body.String(), elapsed
}

// A first round fetches the three pages, holds the figures, writes the disk copy and shows them; the next
// round, 6 hours later, sends every page with its ETag, gets 304s and keeps the document, with a new
// fetched_at. Until then nothing is asked again.
func TestProvimuxFiguresRoundFetchesHoldsAndWrites(t *testing.T) {
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	dir := t.TempDir()
	liveModelLists.cache.Store(modellists.NewStore(dir))
	server := provimuxFiguresStandIn(t)
	// date-safe: the store's clock is set to it.
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	liveModelListNow = func() time.Time { return now }
	cfg := provimuxFiguresConfig()
	svc := &Service{cfg: cfg}
	hook := provimuxCaptureLog(t)

	svc.provimuxFiguresRound(t.Context(), cfg)
	if got := len(server.Requests()); got != 3 {
		t.Fatalf("requests = %d", got)
	}
	// date-safe: the store's clock and the fixture's dataset, read back as the answer writes them.
	answer, _, _ := provimuxFiguresGet(t, cfg, "?all=true")
	if answer.Figures.State != "held" || answer.HeldEntries != 450 || len(answer.Figures.Entries) != 450 || answer.FromCache ||
		answer.LastFailure != nil || *answer.Figures.DatasetVersion != "c37885999c67cfaa" || *answer.Figures.FetchedAt != "2026-11-26T12:00:00Z" ||
		answer.Figures.Contract != 1 || answer.Figures.Source != "https://providers.dev" || !answer.Options.PublishedLists {
		t.Fatalf("held: %+v", answer)
	}
	if selected, _, _ := provimuxFiguresGet(t, cfg, ""); selected.HeldEntries != 450 || len(selected.Figures.Entries) != 0 || selected.Figures.State != "held" {
		t.Fatalf("without all=true no route names an entry yet: %+v", selected)
	}
	// date-safe: the fixture dataset's day in the log line, read from the document.
	line := false
	for _, entry := range hook.AllEntries() {
		if entry.Message == "prices and cache lifetimes from providers.dev: dataset c3788599… of 2026-11-26, 450 entries" {
			line = true
		}
	}
	if !line {
		t.Fatalf("log line missing: %v", hook.AllEntries())
	}
	file := filepath.Join(dir, modellists.FiguresFileName)
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("disk copy: %v %v", info, err)
	}

	// Not before 6 hours.
	now = now.Add(6*time.Hour - time.Second)
	svc.provimuxFiguresRound(t.Context(), cfg)
	if got := len(server.Requests()); got != 3 {
		t.Fatalf("asked again before 6 hours: %d", got)
	}
	now = now.Add(time.Second)
	svc.provimuxFiguresRound(t.Context(), cfg)
	requests := server.Requests()
	if len(requests) != 6 {
		t.Fatalf("requests = %d", len(requests))
	}
	doc := provimuxFiguresStateOf().Document
	for i, req := range requests[3:] {
		if req.Header.Get("If-None-Match") != doc.Pages[i].ETag || req.URL.Query().Get("offset") != fmt.Sprint(200*i) {
			t.Fatalf("conditional request %d: %v %v", i, req.URL, req.Header)
		}
	}
	// The User-Agent is exactly provimux/<version>, once, on every request: nothing more is said.
	for i, req := range requests {
		if agents := req.Header.Values("User-Agent"); len(agents) != 1 || agents[0] != "provimux/"+buildinfo.DisplayVersion() {
			t.Fatalf("request %d: User-Agent %q", i, agents)
		}
	}
	// date-safe: the store's clock, 6 hours on, read back as the answer writes it.
	answer, _, _ = provimuxFiguresGet(t, cfg, "?all=true")
	if answer.HeldEntries != 450 || *answer.Figures.FetchedAt != "2026-11-26T18:00:00Z" || answer.LastFailure != nil {
		t.Fatalf("after 304: %+v", answer)
	}
	stored, err := modellists.NewStore(dir).ReadFigures(liveModelListNow())
	if err != nil || !stored.FetchedAt.Equal(now) || len(stored.Entries) != 450 {
		t.Fatalf("the disk copy keeps the answer's time: %v", err)
	}
}

// A failure keeps the held figures and records a code; the retry follows 10 minutes, 1 hour, then 6 hours;
// a document older than the held one is refused; a success clears the failure.
func TestProvimuxFiguresFailuresKeepTheHeldDocument(t *testing.T) {
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	liveModelLists.cache.Store(modellists.NewStore(t.TempDir()))
	server := provimuxFiguresStandIn(t)
	// date-safe: the store's clock is set to it.
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	liveModelListNow = func() time.Time { return now }
	cfg := provimuxFiguresConfig()
	svc := &Service{cfg: cfg}
	svc.provimuxFiguresRound(t.Context(), cfg)
	version, _, entries := provimuxFiguresEntries(t, 450)

	cases := []struct {
		name     string
		page     func(offset int, body []byte) (int, []byte)
		dataset  func()
		category string
		status   *int
		wait     time.Duration
	}{
		{"500", func(int, []byte) (int, []byte) { return http.StatusInternalServerError, []byte("{}") }, nil, modellists.FailureHTTP, new(500), 10 * time.Minute},
		{"a dataset that changes between pages", func(offset int, body []byte) (int, []byte) {
			if offset == 0 {
				return 0, nil
			}
			return http.StatusOK, []byte(strings.Replace(string(body), version, "ds-other", 1))
		}, func() {
			// date-safe: a published dataset's generated_at, compared only with the held one's.
			server.SetDataset(entries, version, "2026-11-26T13:00:00Z")
		}, modellists.FailureResponse, nil, time.Hour},
		{"older than the held one", nil, func() {
			// date-safe: a published dataset's generated_at, compared only with the held one's.
			server.SetDataset(entries[:300], "ds-older", "2026-11-20T00:00:00Z")
		}, modellists.FailureResponse, nil, 6 * time.Hour},
	}
	for _, tc := range cases {
		server.SetPage(tc.page)
		if tc.dataset != nil {
			tc.dataset()
		}
		provimuxFigures.request()
		svc.provimuxFiguresRound(t.Context(), cfg)
		answer, _, _ := provimuxFiguresGet(t, cfg, "?all=true")
		if answer.Figures.State != "held" || answer.HeldEntries != 450 || *answer.Figures.DatasetVersion != version || answer.LastFailure == nil ||
			answer.LastFailure.Category != tc.category || (tc.status == nil) != (answer.LastFailure.HTTPStatus == nil) ||
			(tc.status != nil && *answer.LastFailure.HTTPStatus != *tc.status) || answer.LastFailure.At != "2026-11-26T12:00:00Z" {
			t.Fatalf("%s: %+v %+v", tc.name, answer, answer.LastFailure)
		}
		if provimuxFigures.due(now.Add(tc.wait-time.Second)) || !provimuxFigures.due(now.Add(tc.wait)) {
			t.Fatalf("%s: retry not after %s", tc.name, tc.wait)
		}
	}
	server.SetPage(nil)
	// date-safe: a published dataset's generated_at, compared only with the held one's.
	server.SetDataset(entries[:420], "ds-new", "2026-11-27T00:00:00Z")
	provimuxFigures.request()
	svc.provimuxFiguresRound(t.Context(), cfg)
	answer, _, _ := provimuxFiguresGet(t, cfg, "?all=true")
	if answer.LastFailure != nil || answer.HeldEntries != 420 || *answer.Figures.DatasetVersion != "ds-new" || provimuxFigures.failures != 0 {
		t.Fatalf("after a success: %+v", answer)
	}
}

// The status endpoint in its three states, as an operator reads it: off, none (with the failure), held.
func TestProvimuxFiguresStatusInThreeStates(t *testing.T) {
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	liveModelLists.cache.Store(modellists.NewStore(t.TempDir()))
	// date-safe: the store's and the handler's clock are set to it.
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	liveModelListNow = func() time.Time { return now }
	cfg := provimuxFiguresConfig()
	svc := &Service{cfg: cfg}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	provimuxFiguresBase = closed.URL + pdentries.Path
	svc.provimuxFiguresRound(t.Context(), cfg)
	none, noneBody, _ := provimuxFiguresGet(t, cfg, "?all=true")
	if none.Figures.State != "none" || none.HeldEntries != 0 || len(none.Figures.Entries) != 0 || none.Figures.DatasetVersion != nil ||
		none.LastFailure == nil || none.LastFailure.Category != "network" || none.LastFailure.HTTPStatus != nil {
		t.Fatalf("none: %s", noneBody)
	}

	provimuxFiguresStandIn(t)
	provimuxFigures.request()
	svc.provimuxFiguresRound(t.Context(), cfg)
	held, heldBody, _ := provimuxFiguresGet(t, cfg, "?all=true")
	if held.Figures.State != "held" || held.HeldEntries != 450 || held.LastFailure != nil {
		t.Fatalf("held: %.400s", heldBody)
	}

	off := provimuxFiguresConfig()
	off.Models.PublishedLists = new(false)
	offAnswer, offBody, _ := provimuxFiguresGet(t, off, "?all=true")
	if offAnswer.Figures.State != "off" || offAnswer.Options.PublishedLists || offAnswer.HeldEntries != 0 || len(offAnswer.Figures.Entries) != 0 ||
		offAnswer.Figures.DatasetVersion != nil || offAnswer.LastFailure != nil {
		t.Fatalf("off: %s", offBody)
	}
	home := provimuxFiguresConfig()
	home.Home.Enabled = true
	if homeAnswer, homeBody, _ := provimuxFiguresGet(t, home, "?all=true"); homeAnswer.Figures.State != "off" || homeAnswer.Options.PublishedLists {
		t.Fatalf("home: %s", homeBody)
	}
	t.Logf("off: %s", offBody)
	t.Logf("none: %s", noneBody)
	t.Logf("held (first 600 bytes): %.600s", heldBody)
}

// providers.dev unreachable at start, with a disk copy: the start never waits, the disk copy is held at
// once with its dates, a client's path answers while the request hangs, and the hanging request ends as a
// network failure that keeps the copy.
func TestProvimuxFiguresUnreachableAtStartWithDiskCopy(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxPublishedNowhere(t)
	provimuxFiguresReset(t)
	dir := t.TempDir()
	provimuxFiguresSeedDisk(t, dir) // the last run's copy, fetched from the stand-in

	// providers.dev hangs: it accepts the connection and never answers.
	server := provimuxFiguresStandIn(t)
	release := make(chan struct{})
	server.SetHang(release)
	t.Cleanup(func() { close(release) })
	provimuxFiguresPageTimeout = 2 * time.Second
	cfg := provimuxFiguresConfig()
	svc, auth := provimuxDevinLoginAt(t, filepath.Join(dir, "config.yaml"))
	svc.cfgMu.Lock()
	svc.cfg.Models.PublishedLists = new(true)
	svc.cfgMu.Unlock()

	started := time.Now()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		svc.runLiveModelListRefresh(ctx) // the round, in the background, as Run starts it
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	// The disk copy is held at once, before any answer.
	deadline := time.Now().Add(5 * time.Second)
	for provimuxFiguresStateOf().State != "held" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	held := time.Since(started)
	// Then the request is under way, and hangs.
	for len(server.Requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// A client's paths while it hangs: a login's registration and the status endpoint.
	registering := time.Now()
	svc.registerModelsForAuth(t.Context(), auth)
	registered := time.Since(registering)
	answer, _, answered := provimuxFiguresGet(t, cfg, "?all=true")
	if len(server.Requests()) == 0 || provimuxFiguresStateOf().Failure != nil {
		t.Fatal("premise: the request is under way and has not ended")
	}
	if answer.Figures.State != "held" || !answer.FromCache || answer.HeldEntries != 450 || *answer.Figures.FetchedAt != provimuxFiguresSeededAt.Format(time.RFC3339) {
		t.Fatalf("the disk copy, with its dates: %+v", answer)
	}
	if held > 250*time.Millisecond || registered > 250*time.Millisecond || answered > 50*time.Millisecond {
		t.Fatalf("the start waited: disk copy held after %s, registration %s, status answered in %s", held, registered, answered)
	}
	t.Logf("providers.dev hanging: the disk copy held %s after the round started; while the request hung, a login registered in %s and the status answered in %s",
		held, registered, answered)
	for provimuxFiguresStateOf().Failure == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	state := provimuxFiguresStateOf()
	if state.Failure == nil || state.Failure.Category != modellists.FailureNetwork || state.State != "held" || len(state.Document.Entries) != 450 {
		t.Fatalf("after the page timed out: %+v", state)
	}
	if ids := provimuxRegistered(auth); len(ids) != 4 {
		t.Fatalf("the login's models changed: %v", ids)
	}
}

// Unreachable at start with nothing on disk: state none, a network failure, and nothing else changes.
func TestProvimuxFiguresUnreachableAtStartWithoutDiskCopy(t *testing.T) {
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	liveModelLists.cache.Store(modellists.NewStore(t.TempDir()))
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	provimuxFiguresBase = closed.URL + pdentries.Path
	cfg := provimuxFiguresConfig()
	(&Service{cfg: cfg}).provimuxFiguresRound(t.Context(), cfg)
	state := provimuxFiguresStateOf()
	if state.State != "none" || state.Document != nil || state.FromCache || state.Failure == nil || state.Failure.Category != modellists.FailureNetwork {
		t.Fatalf("state = %+v", state)
	}
}

// A damaged or a foreign file on disk at start is ignored with one warning: nothing is held from it, and
// the next accepted document replaces it.
func TestProvimuxFiguresDamagedOrForeignFileAtStart(t *testing.T) {
	for name, content := range map[string]string{
		"damaged": `{"schema":1,"dataset_version":"c37885999c67cfaa","entries":{"anthropic/claude-opus-5-5":{"provider":`,
		// date-safe: a foreign file's dates, never read as times.
		"foreign": `{"schema":1,"etag":"\"v1\"","dataset_version":"x","generated_at":"2026-11-23T04:20:11Z","fetched_at":"2026-11-23T04:20:11Z","listings":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			provimuxLiveReset(t)
			provimuxFiguresReset(t)
			dir := t.TempDir()
			liveModelLists.cache.Store(modellists.NewStore(dir))
			file := filepath.Join(dir, modellists.FiguresFileName)
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			hook := provimuxCaptureLog(t)
			closed := httptest.NewServer(http.NotFoundHandler())
			closed.Close()
			provimuxFiguresBase = closed.URL + pdentries.Path
			cfg := provimuxFiguresConfig()
			svc := &Service{cfg: cfg}
			svc.provimuxFiguresRound(t.Context(), cfg)
			if state := provimuxFiguresStateOf(); state.State != "none" || state.FromCache {
				t.Fatalf("held from a %s file: %+v", name, state)
			}
			warnings := 0
			for _, entry := range hook.AllEntries() {
				if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "ignoring figures.json") {
					warnings++
				}
			}
			if warnings != 1 {
				t.Fatalf("warnings = %d: %v", warnings, hook.AllEntries())
			}
			provimuxFiguresStandIn(t)
			provimuxFigures.request()
			svc.provimuxFiguresRound(t.Context(), cfg)
			stored, err := modellists.NewStore(dir).ReadFigures(liveModelListNow())
			if err != nil || len(stored.Entries) != 450 {
				t.Fatalf("replaced by the next accepted document: %v", err)
			}
		})
	}
}

// The switch off, and Home mode: the round sends nothing and does not read the disk copy, and the status
// says off with no figure, whatever is held. Turned on again, the next round reads the copy and asks.
func TestProvimuxFiguresSwitchOffAndHomeMode(t *testing.T) {
	for _, mode := range []string{"published-lists false", "home mode"} {
		t.Run(mode, func(t *testing.T) {
			provimuxLiveReset(t)
			provimuxPublishedReset(t)
			provimuxPublishedNowhere(t)
			provimuxFiguresReset(t)
			dir := t.TempDir()
			provimuxFiguresSeedDisk(t, dir)
			server := provimuxFiguresStandIn(t)
			cfg := provimuxFiguresConfig()
			if mode == "home mode" {
				cfg.Home.Enabled = true
			} else {
				cfg.Models.PublishedLists = new(false)
			}
			if provimuxPublishedListsOn(cfg) {
				t.Fatal("the switch is on")
			}
			svc := &Service{cfg: cfg, configPath: filepath.Join(dir, "config.yaml"), coreManager: coreauth.NewManager(nil, nil, nil)}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { svc.runLiveModelListRefresh(ctx); close(done) }()
			// The loop returns only once providers.dev's round has returned: nothing writes into dir afterwards.
			defer func() {
				cancel()
				<-done
			}()
			for i := 0; i < 3; i++ {
				registry.RequestLiveModelListRefresh()
				time.Sleep(50 * time.Millisecond)
			}
			provimuxFigures.mu.Lock()
			read, held := provimuxFigures.cacheRead, provimuxFigures.doc
			provimuxFigures.mu.Unlock()
			if len(server.Requests()) != 0 || read || held != nil {
				t.Fatalf("off: %d requests, disk copy read %v", len(server.Requests()), read)
			}
			if answer, body, _ := provimuxFiguresGet(t, cfg, "?all=true"); answer.Figures.State != "off" || len(answer.Figures.Entries) != 0 || answer.HeldEntries != 0 {
				t.Fatalf("off: %s", body)
			}

			// On again (a reload): the disk copy, then the request.
			on := provimuxFiguresConfig()
			svc.cfgMu.Lock()
			svc.cfg = on
			svc.cfgMu.Unlock()
			registry.RequestLiveModelListRefresh()
			deadline := time.Now().Add(5 * time.Second)
			for len(server.Requests()) < 3 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			for provimuxFiguresStateOf().Document == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if len(server.Requests()) < 3 || provimuxFiguresStateOf().State != "held" {
				t.Fatalf("on again: %d requests, %+v", len(server.Requests()), provimuxFiguresStateOf())
			}
		})
	}
}

// In a test binary the switch is off when unset, and the figures' base is empty: a test that turns the
// listings on never reaches providers.dev for figures.
func TestProvimuxFiguresOffInATestBinary(t *testing.T) {
	if (registry.CatalogSources{}).PublishedListsOn() {
		t.Fatal("unset models.published-lists is on in a test binary")
	}
	if provimuxFiguresDefaultBase() != "" {
		t.Fatal("a test binary has a figures base")
	}
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	provimuxFiguresBase = provimuxFiguresDefaultBase()
	cfg := provimuxFiguresConfig()
	(&Service{cfg: cfg}).provimuxFiguresRound(t.Context(), cfg)
	if state := provimuxFiguresStateOf(); state.State != "none" || state.Failure != nil {
		t.Fatalf("a test binary fetched: %+v", state)
	}
}

// POST /v8/management/models/refresh reaches the figures through the registry's hook: the round asks again
// at once, with the held ETags.
func TestProvimuxFiguresRefreshHook(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxPublishedNowhere(t)
	provimuxFiguresReset(t)
	server := provimuxFiguresStandIn(t)
	// Its own model lists' folder, so that nothing of another test's round is read or written.
	svc := &Service{cfg: provimuxFiguresConfig(), configPath: filepath.Join(t.TempDir(), "config.yaml"), coreManager: coreauth.NewManager(nil, nil, nil)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { svc.runLiveModelListRefresh(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()
	waitFor := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for len(server.Requests()) < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(server.Requests()) < n {
			t.Fatalf("requests = %d, want %d", len(server.Requests()), n)
		}
	}
	waitFor(3) // at start
	deadline := time.Now().Add(5 * time.Second)
	for provimuxFiguresStateOf().State != "held" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond) // the first round has applied its answer
	}
	registry.RequestLiveModelListRefresh()
	waitFor(6) // on refresh
	cancel()
	<-done
	requests := server.Requests()
	if len(requests) != 6 || requests[3].Header.Get("If-None-Match") == "" {
		t.Fatalf("requests = %d", len(requests))
	}
}
