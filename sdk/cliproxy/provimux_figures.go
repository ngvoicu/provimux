package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

// Provimux: providers.dev's prices and cache lifetimes of the entries (prices-and-cache.md 6.2; step F2).
//
// The second request of the listings' round (runPublishedListRefresh): at start in the background, every
// 6 hours, on POST /v8/management/models/refresh; after a failure 10 minutes, then 1 hour, then the normal
// 6 hours; the held document stays, however old. The switch is the listings' (models.published-lists, off
// in Home mode and, unset, in a test binary): off, nothing is fetched and the disk copy is not read. Nothing
// a client asks for waits for it, and nothing of a model list, a route or a request to a vendor depends on
// it: GET /v8/management/provimux/figures reads what is held (provimuxFiguresStateOf).

// provimuxFiguresBase is providers.dev's entries resource; tests point it at a stand-in. In a test binary it
// is empty, so a test that turns the switch on for the listings never reaches the real site for figures.
var provimuxFiguresBase = provimuxFiguresDefaultBase()

func provimuxFiguresDefaultBase() string {
	if testing.Testing() {
		return ""
	}
	return modellists.EntriesURL
}

// provimuxFiguresPageTimeout bounds one page (prices-and-cache.md 6.2: within 30 seconds a page).
var provimuxFiguresPageTimeout = 30 * time.Second

type provimuxFiguresStore struct {
	mu         sync.Mutex
	doc        *modellists.FiguresDocument // never changed once installed
	fromCache  bool
	cacheRead  bool
	failure    *management.ProvimuxLiveListFailure
	failures   int       // consecutive
	next       time.Time // the next fetch is due
	lastLogged string    // dataset version of the last accepted-document line
	fetching   sync.Mutex
}

var provimuxFigures = &provimuxFiguresStore{}

// request makes the next round fetch at once (POST /v8/management/models/refresh).
func (st *provimuxFiguresStore) request() {
	st.mu.Lock()
	st.next = time.Time{}
	st.mu.Unlock()
}

func (st *provimuxFiguresStore) due(now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return !now.Before(st.next)
}

// loadCache reads the disk copy once, before the first fetch of a run with the switch on. A damaged or
// foreign file is ignored with one warning (by the store).
func (st *provimuxFiguresStore) loadCache(cache *modellists.Store) {
	if cache == nil {
		return
	}
	st.mu.Lock()
	if st.cacheRead || st.doc != nil {
		st.cacheRead = true
		st.mu.Unlock()
		return
	}
	st.cacheRead = true
	st.mu.Unlock()
	doc, err := cache.ReadFigures()
	if err != nil || doc == nil {
		return
	}
	st.mu.Lock()
	if st.doc != nil { // a fetch finished meanwhile
		st.mu.Unlock()
		return
	}
	st.doc, st.fromCache = doc, true
	st.mu.Unlock()
	log.Infof("prices and cache lifetimes from providers.dev: dataset %s of %s, %d entries, read from the disk cache",
		provimuxShortVersion(doc.DatasetVersion), doc.GeneratedAt.UTC().Format(time.DateOnly), len(doc.Entries))
}

// provimuxFiguresRound is the figures' part of the listings' round, with the switch on: the disk copy once,
// then the request when it is due.
func (s *Service) provimuxFiguresRound(ctx context.Context, cfg *config.Config) {
	provimuxFigures.loadCache(liveModelLists.cache.Load())
	if provimuxFiguresBase == "" || !provimuxFigures.due(liveModelListNow()) {
		return
	}
	s.refreshFigures(ctx, cfg)
}

// refreshFigures sends the request, page after page, and applies its answer.
func (s *Service) refreshFigures(ctx context.Context, cfg *config.Config) {
	st := provimuxFigures
	st.fetching.Lock()
	defer st.fetching.Unlock()
	st.mu.Lock()
	held := st.doc
	st.mu.Unlock()
	client := &http.Client{}
	if cfg != nil {
		util.SetProxy(&cfg.SDKConfig, client)
	}
	now := liveModelListNow()
	result, err := modellists.FetchFigures(ctx, client, provimuxFiguresBase, held, "provimux/"+buildinfo.DisplayVersion(), now, provimuxFiguresPageTimeout)
	if ctx.Err() != nil {
		return
	}
	accepted := held
	if err == nil && result.NotModified {
		accepted = held.WithFetchedAt(now)
	} else if err == nil {
		accepted, err = modellists.AcceptFigures(held, result.Document)
	}
	if err != nil {
		s.provimuxFiguresFailed(err, held, now)
		return
	}
	st.mu.Lock()
	st.failure, st.failures = nil, 0
	st.next = now.Add(provimuxPublishedInterval)
	st.doc, st.fromCache = accepted, false
	logged := st.lastLogged == accepted.DatasetVersion
	st.lastLogged = accepted.DatasetVersion
	st.mu.Unlock()
	if cache := liveModelLists.cache.Load(); cache != nil {
		_ = cache.WriteFigures(accepted)
	}
	if !logged {
		line := "prices and cache lifetimes from providers.dev: dataset %s of %s, %d entries"
		args := []any{provimuxShortVersion(accepted.DatasetVersion), accepted.GeneratedAt.UTC().Format(time.DateOnly), len(accepted.Entries)}
		if accepted.Unread > 0 {
			line += " (%d left out: not readable)"
			args = append(args, accepted.Unread)
		}
		log.Infof(line, args...)
	}
}

func (s *Service) provimuxFiguresFailed(err error, held *modellists.FiguresDocument, now time.Time) {
	st := provimuxFigures
	category, status := modellists.FailureNetwork, 0
	var fetchErr *modellists.FetchError
	switch {
	case errors.As(err, &fetchErr):
		category, status = fetchErr.Category, fetchErr.Status
	case errors.Is(err, modellists.ErrOlder):
		category = modellists.FailureResponse
	}
	st.mu.Lock()
	st.failure = &management.ProvimuxLiveListFailure{At: now, Category: category, HTTPStatus: status}
	wait := provimuxPublishedInterval
	if st.failures < len(provimuxPublishedBackoff) {
		wait = provimuxPublishedBackoff[st.failures]
	}
	st.failures++
	st.next = now.Add(wait)
	st.mu.Unlock()
	kept := "no figures are held"
	if held != nil {
		kept = "keeping dataset " + provimuxShortVersion(held.DatasetVersion) + " of " + held.GeneratedAt.UTC().Format(time.DateOnly)
	}
	text := provimuxLiveFailureText(category, status)
	if errors.Is(err, modellists.ErrOlder) {
		text += ", older than the held document"
	}
	log.Warnf("prices and cache lifetimes from providers.dev: fetch failed (%s); %s", text, kept)
}

// provimuxFiguresStateOf reports the held figures for GET /v8/management/provimux/figures.
func provimuxFiguresStateOf() management.ProvimuxFiguresState {
	st := provimuxFigures
	st.mu.Lock()
	defer st.mu.Unlock()
	state := management.ProvimuxFiguresState{State: "none", FromCache: st.fromCache, Document: st.doc}
	if st.failure != nil {
		failure := *st.failure
		state.Failure = &failure
	}
	if st.doc != nil {
		state.State = "held"
	}
	return state
}

func init() { management.SetProvimuxFiguresSource(provimuxFiguresStateOf) }
