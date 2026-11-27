package cliproxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Three rules of providers.dev's round that no test held before the review of F3 (its note 16): what a fetch does
// after the switch went off during its last page, when the next fetch is due after one that succeeded, and that the
// loop returns only after its round returned. Every request goes to a stand-in on loopback; every clock is the test's.

// date-safe: the round's clock; the stand-in's dataset was generated 38 minutes before it. Nothing compares it with
// today.
var provimuxGapsStart = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// provimuxGapsClock sets the round's clock to a clock the test moves, and returns its setter.
func provimuxGapsClock(t *testing.T) (set func(time.Time), add func(time.Duration)) {
	t.Helper()
	var mu sync.Mutex
	now := provimuxGapsStart
	liveModelListNow = func() time.Time { // provimuxLiveReset puts the clock back
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return func(at time.Time) {
			mu.Lock()
			now = at
			mu.Unlock()
		}, func(d time.Duration) {
			mu.Lock()
			now = now.Add(d)
			mu.Unlock()
		}
}

// The switch goes off while the last of three pages is answered: nothing of the fetch is applied (nothing held, no
// failure, no disk copy); on again, the next round asks at once and holds.
func TestProvimuxFiguresSwitchOffDuringTheLastPage(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxPublishedNowhere(t)
	provimuxFiguresReset(t)
	provimuxGapsClock(t)
	dir := t.TempDir()
	liveModelLists.cache.Store(modellists.NewStore(dir))
	server := provimuxFiguresStandIn(t)
	on := provimuxFiguresConfig()
	off := provimuxFiguresConfig()
	off.Models.PublishedLists = new(false)
	svc := &Service{cfg: on}
	server.SetPage(func(offset int, body []byte) (int, []byte) {
		if offset == 2*modellists.FiguresPageLimit { // the last page, while it is answered
			svc.cfgMu.Lock()
			svc.cfg = off
			svc.cfgMu.Unlock()
		}
		return 0, nil
	})
	svc.provimuxFiguresRound(t.Context(), on)
	state := provimuxFiguresStateOf()
	stored, errRead := modellists.NewStore(dir).ReadFigures(provimuxGapsStart)
	if len(server.Requests()) != 3 || state.Document != nil || state.Failure != nil || stored != nil {
		t.Fatalf("after %d requests: held %v, failure %+v, disk copy %v (%v)", len(server.Requests()), state.Document != nil, state.Failure, stored != nil, errRead)
	}
	server.SetPage(nil)
	svc.cfgMu.Lock()
	svc.cfg = on
	svc.cfgMu.Unlock()
	svc.provimuxFiguresRound(t.Context(), on)
	if state = provimuxFiguresStateOf(); state.State != "held" || len(server.Requests()) != 6 {
		t.Fatalf("on again: %s after %d requests in all", state.State, len(server.Requests()))
	}
}

// After a fetch that succeeded, the next is due 6 hours after the round it belonged to, not 6 hours after the fetch:
// the listings' request takes two minutes on the gateway's clock, and the figures are asked again at 6 hours, not at
// 6 hours and two minutes. The round loop is the binary's; the test moves its clock and wakes it.
func TestProvimuxFiguresIntervalCountsFromTheRound(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxFiguresReset(t)
	setClock, addClock := provimuxGapsClock(t)
	// date-safe: a published dataset's generated_at, compared only with the held one's.
	page := provimuxPDPage("2026-11-26T11:00:00Z", provimuxPDListing("devin", provimuxPDUnstated(32768)))
	listings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addClock(2 * time.Minute) // the listings' request takes two minutes
		_, _ = w.Write(page)
	}))
	t.Cleanup(listings.Close)
	previousBase := provimuxPublishedBase
	t.Cleanup(func() { provimuxPublishedBase = previousBase })
	provimuxPublishedBase = listings.URL + "/api/v1/listings"
	server := provimuxFiguresStandIn(t)
	fetches := func() int { // a fetch begins with its first page
		n := 0
		for _, request := range server.Requests() {
			if offset := request.URL.Query().Get("offset"); offset == "" || offset == "0" {
				n++
			}
		}
		return n
	}

	svc := &Service{cfg: provimuxFiguresConfig(), coreManager: coreauth.NewManager(nil, nil, nil)}
	rounds := provimuxPublished.rounds.Load()
	provimuxRoundLoop(t, svc) // its first round runs at once, at start
	provimuxRoundsWait(t, rounds)
	if got := fetches(); got != 1 || provimuxFiguresStateOf().State != "held" {
		t.Fatalf("at start: %d fetches, state %s", got, provimuxFiguresStateOf().State)
	}
	for _, step := range []struct {
		at   time.Duration
		want int
	}{
		{6*time.Hour - time.Second, 1},
		{6 * time.Hour, 2}, // 6 hours after the first round, though its fetch began two minutes into it
	} {
		setClock(provimuxGapsStart.Add(step.at))
		before := provimuxPublished.rounds.Load()
		provimuxPublished.wake <- struct{}{}
		provimuxRoundsWait(t, before)
		if got := fetches(); got != step.want {
			t.Fatalf("at %s: %d fetches, want %d", step.at, got, step.want)
		}
	}
}

// The loop returns only after providers.dev's round has returned, so nothing of the round writes to the model lists'
// folder afterwards. The round is held where it waits for the figures' fetch (the test holds that lock), the loop is
// stopped, and the test waits for one of two things, each certain to come in its case: the loop parked waiting for the
// round (then the lock is let go and the loop must return), or the loop returned while the round was still held (the
// failure). No time decides it.
func TestProvimuxRoundLoopReturnsAfterItsRound(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxPublishedNowhere(t)
	provimuxFiguresReset(t)
	provimuxGapsClock(t)
	provimuxFiguresStandIn(t)
	fetching := &provimuxFigures.fetching
	fetching.Lock()
	held := true
	defer func() {
		if held {
			fetching.Unlock()
		}
	}()
	svc := &Service{cfg: provimuxFiguresConfig(), coreManager: coreauth.NewManager(nil, nil, nil)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { svc.runLiveModelListRefresh(ctx); close(done) }()

	provimuxWaitForGoroutine(t, done, "[sync.Mutex.Lock", "(*Service).refreshFigures")
	cancel()
	provimuxWaitForGoroutine(t, done, "[chan receive", "(*Service).runLiveModelListRefresh")
	held = false
	fetching.Unlock()
	<-done
}

// provimuxWaitForGoroutine waits, yielding, until a goroutine's stack begins with state and holds frame. done closing
// first (the loop returned) fails the test.
func provimuxWaitForGoroutine(t *testing.T, done <-chan struct{}, state, frame string) {
	t.Helper()
	buffer := make([]byte, 1<<20)
	for {
		select {
		case <-done:
			t.Fatalf("the loop returned while its round was still held (waiting for a goroutine %s in %s)", state, frame)
		default:
		}
		n := runtime.Stack(buffer, true)
		if n == len(buffer) { // cut short: every goroutine is read, however many the package left running
			buffer = make([]byte, 2*len(buffer))
			continue
		}
		for _, stack := range bytes.Split(buffer[:n], []byte("\n\n")) {
			header, _, _ := bytes.Cut(stack, []byte("\n"))
			if bytes.Contains(header, []byte(state)) && bytes.Contains(stack, []byte(frame)) {
				return
			}
		}
		runtime.Gosched()
	}
}
