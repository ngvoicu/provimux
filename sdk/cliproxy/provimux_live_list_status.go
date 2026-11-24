package cliproxy

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the live-model-list fetcher's state for GET /v8/management/provimux/live-lists. It reads
// the store of live_model_lists.go (same package, unexported) and copies scalars only. The one line
// the fetcher calls, noteLiveModelListFailure, keeps what kind of failure the last fetch was, which
// the store does not hold. No error text is kept: errors can carry a provider's response body or a
// private URL.

// Failure categories, a closed set of codes.
const (
	provimuxLiveFailureHTTP        = "http_status"      // the provider answered with a non-200 status
	provimuxLiveFailureCredentials = "credentials"      // preparing the login's credentials failed
	provimuxLiveFailureResponse    = "invalid_response" // the answer was not a usable model list
	provimuxLiveFailureNetwork     = "network"          // the request did not get an answer
	provimuxLiveFailureOther       = "other"
)

// provimuxLiveListNote is one failed fetch: the account it was made for, the retry time the store
// recorded for it (which identifies the failure), its category and HTTP status.
type provimuxLiveListNote struct {
	identity string
	retry    time.Time
	category string
	status   int
}

var provimuxLiveListNotes = struct {
	mu    sync.Mutex
	notes map[string]provimuxLiveListNote // by auth ID
}{notes: make(map[string]provimuxLiveListNote)}

// noteLiveModelListFailure records the category of a failed fetch for the account identity, bound to
// the retry time the store just recorded. Lock order: store, then notes.
func noteLiveModelListFailure(authID, identity string, err error) {
	if authID == "" || err == nil {
		return
	}
	category, status := provimuxLiveListFailureOf(err)
	provimuxNotePrunerOnce.Do(func() { go provimuxRunNotePruner(provimuxNotePrunerTick()) })
	liveModelLists.mu.Lock()
	defer liveModelLists.mu.Unlock()
	provimuxLiveListNotes.mu.Lock()
	defer provimuxLiveListNotes.mu.Unlock()
	if retry, failed := liveModelLists.nextRetry[authID]; failed {
		provimuxLiveListNotes.notes[authID] = provimuxLiveListNote{identity: identity, retry: retry, category: category, status: status}
	}
	provimuxPruneLiveListNotesLocked()
}

// provimuxPruneLiveListNotesLocked drops notes whose failure the store no longer holds: a later
// success, a newer failure or a removed login. Callers hold both locks.
func provimuxPruneLiveListNotesLocked() {
	for id, note := range provimuxLiveListNotes.notes {
		if retry, failed := liveModelLists.nextRetry[id]; !failed || !retry.Equal(note.retry) {
			delete(provimuxLiveListNotes.notes, id)
		}
	}
}

// provimuxPruneLiveListNotes takes both locks and drops stale notes.
func provimuxPruneLiveListNotes() {
	liveModelLists.mu.Lock()
	defer liveModelLists.mu.Unlock()
	provimuxLiveListNotes.mu.Lock()
	defer provimuxLiveListNotes.mu.Unlock()
	provimuxPruneLiveListNotesLocked()
}

var provimuxNotePrunerOnce sync.Once

// provimuxNotePrunerTick is the pruner's clock: a tick every scan interval. Tests replace it.
var provimuxNotePrunerTick = func() <-chan time.Time {
	return time.NewTicker(liveModelListScanInterval).C
}

// provimuxRunNotePruner drops stale notes on every tick, so notes of removed logins go within one
// scan interval whether or not anyone reads the endpoint. Started once, by the first failure.
func provimuxRunNotePruner(tick <-chan time.Time) {
	for range tick {
		provimuxPruneLiveListNotes()
	}
}

// provimuxLiveListFailureOf classifies a fetch error by the fetcher's own messages. The status is
// read only from the fetcher's own top-level "HTTP <status> from <path>" error.
func provimuxLiveListFailureOf(err error) (string, int) {
	if errors.Is(err, errLiveModelListTimeout) {
		return provimuxLiveFailureNetwork, 0
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "HTTP "):
		field, _, _ := strings.Cut(strings.TrimPrefix(message, "HTTP "), " ")
		if status, errAtoi := strconv.Atoi(field); errAtoi == nil && status >= 100 && status <= 599 {
			return provimuxLiveFailureHTTP, status
		}
		return provimuxLiveFailureOther, 0
	case strings.HasPrefix(message, "prepare credentials:"):
		return provimuxLiveFailureCredentials, 0
	case errors.Is(err, errLiveModelListMalformed), errors.Is(err, errLiveModelListIncomplete), errors.Is(err, errLiveModelListTooLarge),
		strings.HasSuffix(message, "model list: invalid JSON"),
		strings.HasPrefix(message, "claude model list: incomplete"),
		strings.HasSuffix(message, " returned no models"):
		return provimuxLiveFailureResponse, 0
	}
	var urlErr *url.Error
	var netErr net.Error
	if errors.As(err, &urlErr) || errors.As(err, &netErr) {
		return provimuxLiveFailureNetwork, 0
	}
	return provimuxLiveFailureOther, 0
}

type provimuxLiveListSource struct{}

func (provimuxLiveListSource) Providers() []string {
	return liveModelListProviders()
}

// State reads the store and the failure note under both locks, so a failure recorded meanwhile is
// either seen whole or not at all. Only observations of the login's current account are reported:
// a list or failure of an earlier account in the same auth file is not.
func (provimuxLiveListSource) State(auth *coreauth.Auth) management.ProvimuxLiveListState {
	if liveModelListProvider(auth) == "" || keyFixedAttr(auth) {
		return management.ProvimuxLiveListState{}
	}
	state := management.ProvimuxLiveListState{Covered: true}
	identity := liveModelListIdentity(auth)
	liveModelLists.mu.Lock()
	defer liveModelLists.mu.Unlock()
	provimuxLiveListNotes.mu.Lock()
	defer provimuxLiveListNotes.mu.Unlock()
	provimuxPruneLiveListNotesLocked()
	if list, ok := liveModelLists.lists[auth.ID]; ok && list.identity == identity && liveModelListHeld(list, liveModelListNow()) {
		state.HasList = true
		state.FetchedAt = list.fetchedAt
		state.ListedModels = len(list.ids)
	}
	_, state.Fetching = liveModelLists.inFlight[auth.ID]
	// A note counts while the store still holds the failure it was made for (the prune above) and
	// only for the account it was made for.
	if note, ok := provimuxLiveListNotes.notes[auth.ID]; ok && note.identity == identity {
		state.Failure = &management.ProvimuxLiveListFailure{At: note.retry.Add(-liveModelListRetry), Category: note.category, HTTPStatus: note.status}
	}
	return state
}

func init() { management.SetProvimuxLiveListSource(provimuxLiveListSource{}) }
