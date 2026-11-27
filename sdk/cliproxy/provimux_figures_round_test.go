package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	moejson "github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists/pdentries"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// The figures in providers.dev's round, as the lead decided on 2026-11-27 after the review of F2 and F3
// (prices-and-cache.md, the note of that day). Every request goes to a stand-in on loopback.

// providers.dev accepts both of the round's connections and never answers them (the listings' and the figures'),
// and the last run left a disk copy: the copy is held before any request of the round, so both paths give the
// figures of the last run at once, while the listings' request still hangs (30 seconds in the binary), and not
// after it. What an interrupted write left in the folder is gone by then.
func TestProvimuxFiguresDiskCopyHeldWhileBothRequestsHang(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxFiguresReset(t)
	dir := t.TempDir()
	provimuxFiguresSeedDisk(t, dir)
	leftover := filepath.Join(dir, modellists.FolderName, ".figures.json.4242.tmp")
	if err := os.WriteFile(leftover, []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var listings, figures atomic.Int32
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, pdentries.Path) {
			figures.Add(1)
		} else {
			listings.Add(1)
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); hang.Close() })
	previousBase, previousTimeout := provimuxPublishedBase, provimuxPublishedFetchTimeout
	t.Cleanup(func() { provimuxPublishedBase, provimuxPublishedFetchTimeout = previousBase, previousTimeout })
	provimuxPublishedBase = hang.URL + "/api/v1/listings"
	provimuxPublishedFetchTimeout = 30 * time.Second // the binary's
	provimuxFiguresBase = hang.URL + pdentries.Path

	cfg := provimuxFiguresConfig()
	svc := &Service{cfg: cfg, configPath: filepath.Join(dir, "config.yaml"), coreManager: coreauth.NewManager(nil, nil, nil)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { svc.runLiveModelListRefresh(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(10 * time.Second)
	for provimuxFiguresStateOf().State != "held" {
		if time.Now().After(deadline) {
			t.Fatalf("the disk copy is not held 10 s after the start (listings requests %d, figures requests %d)", listings.Load(), figures.Load())
		}
		time.Sleep(time.Millisecond)
	}
	// Held while the listings' request still hangs, before the figures' own request.
	if figures.Load() != 0 || provimuxPublishedStateOf().Failure != nil {
		t.Fatalf("premise: the listings' request is under way (figures requests %d, listings failure %+v)", figures.Load(), provimuxPublishedStateOf().Failure)
	}
	// The operator's path and the client's: the copy, with its dates.
	answer, body, _ := provimuxFiguresGet(t, cfg, "?all=true")
	if answer.Figures.State != "held" || !answer.FromCache || answer.HeldEntries != 450 || len(answer.Figures.Entries) != 450 ||
		*answer.Figures.FetchedAt != provimuxFiguresSeededAt.Format(time.RFC3339) || answer.LastFailure != nil {
		t.Fatalf("status: %.400s", body)
	}
	if view := modellists.BuildFiguresView(true, modellists.HeldFigures(), modellists.SelectAll); view.State != "held" || len(view.Entries) != 450 {
		t.Fatalf("the client list's source: %s, %d entries", view.State, len(view.Entries))
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover of an interrupted write is still there: %v", err)
	}
}

// provimuxFiguresUnreadable is the stand-in's 450 entries with a field every summary has renamed.
func provimuxFiguresUnreadable(t *testing.T) []moejson.RawMessage {
	t.Helper()
	_, _, entries := provimuxFiguresEntries(t, 450)
	out := make([]moejson.RawMessage, len(entries))
	for i, raw := range entries {
		out[i] = pdentries.Edit(raw, func(entry map[string]any) {
			entry["cache_lifetimes"] = entry["cache_lifetime"]
			delete(entry, "cache_lifetime")
		})
	}
	return out
}

// provimuxFiguresWarned reports whether the figures' failure line says why.
func provimuxFiguresWarned(entries []*log.Entry, why string) bool {
	for _, entry := range entries {
		if entry.Level == log.WarnLevel && strings.HasPrefix(entry.Message, "prices and cache lifetimes from providers.dev: fetch failed (invalid_response, "+why+")") {
			return true
		}
	}
	return false
}

// A dataset of which no entry can be read, or of 0 entries, never replaces the 450 held: the held document
// stays, in memory and on disk, and the status and the log say why, as for the other refusals.
func TestProvimuxFiguresNoReadableEntryKeepsTheHeld(t *testing.T) {
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	dir := t.TempDir()
	liveModelLists.cache.Store(modellists.NewStore(dir))
	server := provimuxFiguresStandIn(t)
	liveModelListNow = func() time.Time { return provimuxFiguresSeededAt }
	cfg := provimuxFiguresConfig()
	svc := &Service{cfg: cfg}
	svc.provimuxFiguresRound(t.Context(), cfg)
	version, generated, _ := provimuxFiguresEntries(t, 0)
	for name, set := range map[string][]moejson.RawMessage{"every entry unreadable": provimuxFiguresUnreadable(t), "total 0": {}} {
		hook := provimuxCaptureLog(t)
		server.SetDataset(set, "ds-"+strings.ReplaceAll(name, " ", "-"), generated)
		provimuxFigures.request()
		svc.provimuxFiguresRound(t.Context(), cfg)
		answer, body, _ := provimuxFiguresGet(t, cfg, "?all=true")
		if answer.Figures.State != "held" || answer.HeldEntries != 450 || *answer.Figures.DatasetVersion != version ||
			answer.LastFailure == nil || answer.LastFailure.Category != modellists.FailureResponse {
			t.Fatalf("%s: %.400s", name, body)
		}
		if !provimuxFiguresWarned(hook.AllEntries(), "no entry could be read") {
			t.Fatalf("%s: the log does not say why: %v", name, hook.AllEntries())
		}
		stored, err := modellists.NewStore(dir).ReadFigures(provimuxFiguresSeededAt)
		if err != nil || len(stored.Entries) != 450 || stored.DatasetVersion != version {
			t.Fatalf("%s: the disk copy: %v", name, err)
		}
	}
}

// A generated_at more than 24 hours ahead of the gateway's clock is refused from the network (state, log) and from
// the disk copy at start; a day later on the gateway's clock the same document is taken.
func TestProvimuxFiguresFarFutureDocumentRefused(t *testing.T) {
	provimuxLiveReset(t)
	provimuxFiguresReset(t)
	_, generatedText, _ := provimuxFiguresEntries(t, 0)
	generated, err := time.Parse(time.RFC3339Nano, generatedText)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	liveModelLists.cache.Store(modellists.NewStore(dir))
	var mu sync.Mutex
	now := generated.Add(-modellists.FiguresMaxAhead - time.Minute)
	liveModelListNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	provimuxFiguresStandIn(t)
	cfg := provimuxFiguresConfig()
	svc := &Service{cfg: cfg}
	hook := provimuxCaptureLog(t)
	svc.provimuxFiguresRound(t.Context(), cfg)
	answer, body, _ := provimuxFiguresGet(t, cfg, "?all=true")
	if answer.Figures.State != "none" || answer.LastFailure == nil || answer.LastFailure.Category != modellists.FailureResponse ||
		!provimuxFiguresWarned(hook.AllEntries(), "generated more than 24 hours ahead of this gateway's clock") {
		t.Fatalf("from the network: %.400s %v", body, hook.AllEntries())
	}
	if _, errStat := os.Stat(filepath.Join(dir, modellists.FiguresFileName)); !os.IsNotExist(errStat) {
		t.Fatalf("written to disk: %v", errStat)
	}

	// The disk copy of such a document, at a start: ignored with one warning, nothing held from it.
	provimuxFiguresSeedDisk(t, dir)
	provimuxFiguresReset(t)
	liveModelLists.cache.Store(modellists.NewStore(filepath.Join(dir, modellists.FolderName)))
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	provimuxFiguresBase = closed.URL + pdentries.Path
	svc.provimuxFiguresRound(t.Context(), cfg)
	if state := provimuxFiguresStateOf(); state.State != "none" || state.FromCache {
		t.Fatalf("held from a disk copy ahead of the clock: %+v", state)
	}

	// A day later on the gateway's clock, the same copy is taken at the next start.
	mu.Lock()
	now = now.Add(24 * time.Hour)
	mu.Unlock()
	provimuxFiguresReset(t)
	liveModelLists.cache.Store(modellists.NewStore(filepath.Join(dir, modellists.FolderName)))
	provimuxFiguresBase = closed.URL + pdentries.Path
	svc.provimuxFiguresRound(t.Context(), cfg)
	if state := provimuxFiguresStateOf(); state.State != "held" || !state.FromCache || len(state.Document.Entries) != 450 {
		t.Fatalf("a day later: %+v", state)
	}
}
