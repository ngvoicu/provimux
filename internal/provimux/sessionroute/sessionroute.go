// Package sessionroute starts and stops Provimux's session-route (.github/provimux/design/session-route.md): the book
// of the manager the server serves with. The book observes results only where session affinity runs: with
// routing.session-affinity false or in Home mode it is not subscribed and keeps nothing, and it follows every config
// the service commits (Follow), both ways. Under a plugin scheduler it stops at the first result it sees and keeps
// nothing. There is no timer. In a test binary nothing is subscribed unless a test asks for it (EnableInTests), so
// every upstream test runs as on upstream.
package sessionroute

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"weak"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// state is one manager's: its book (once the routes are registered) and whether the committed config wants it.
type state struct {
	book    *coreauth.ProvimuxSessionBook
	wanted  bool
	decided bool // a config was committed for this manager (Follow); before that the routes' start decides alone
}

var (
	mu     sync.Mutex
	states = make(map[weak.Pointer[coreauth.Manager]]*state) // weak: a manager nothing else holds leaves the map
	inTest atomic.Bool
)

// observing reports whether books may subscribe in this process: always, except in a test binary that did not ask.
func observing() bool { return !testing.Testing() || inTest.Load() }

// EnableInTests lets books subscribe in a test binary, until the returned function is called.
func EnableInTests() (restore func()) {
	previous := inTest.Swap(true)
	return func() { inTest.Store(previous) }
}

// Wanted reports whether cfg runs session affinity in this gateway: routing.session-affinity on, and not Home mode
// (Home selects).
func Wanted(cfg *config.Config) bool {
	return cfg != nil && cfg.Routing.SessionAffinity && !cfg.Home.Enabled
}

// stateOf returns m's state, made on first use; mu must be held.
func stateOf(m *coreauth.Manager) *state {
	key := weak.Make(m)
	if s := states[key]; s != nil {
		return s
	}
	s := &state{}
	states[key] = s
	runtime.AddCleanup(m, func(key weak.Pointer[coreauth.Manager]) {
		mu.Lock()
		delete(states, key)
		mu.Unlock()
	}, key)
	return s
}

// applyLocked subscribes or unsubscribes s's book as wanted; unsubscribing forgets everything the book held.
func applyLocked(m *coreauth.Manager, s *state) {
	if s.book == nil {
		return
	}
	wanted := s.wanted || !s.decided
	if wanted && observing() && coreauth.ProvimuxSessionAffinityMode(m) == "on" {
		s.book.Subscribe()
		return
	}
	if !wanted || !observing() {
		s.book.Unsubscribe()
	}
}

// Follow puts cfg in force for m's book: called where the service commits a config and for the first config of a
// start. With session affinity off or in Home mode the book stops observing and forgets what it held; on, it observes
// again from the next result.
func Follow(m *coreauth.Manager, cfg *config.Config) {
	if m == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	s := stateOf(m)
	s.wanted, s.decided = Wanted(cfg), true
	if s.book == nil {
		return
	}
	if s.wanted && observing() {
		// The selector the config asks for is set after the commit: subscribe now; a result seen before affinity
		// runs stops the book again (it keeps nothing), and the next show subscribes it once affinity runs.
		s.book.Subscribe()
		return
	}
	s.book.Unsubscribe()
}

// Start makes m's book, once, where the client routes are registered, and subscribes it when session affinity runs.
func Start(m *coreauth.Manager) *coreauth.ProvimuxSessionBook {
	if m == nil {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	s := stateOf(m)
	if s.book == nil {
		s.book = coreauth.NewProvimuxSessionBook(m)
	}
	applyLocked(m, s)
	return s.book
}

// Book returns m's book for a show or a release, subscribing it again where it stopped at a result while session
// affinity did not run (a plugin scheduler) and affinity runs now. A manager never started has an empty book that
// observes nothing (every conversation is "not-seen" there).
func Book(m *coreauth.Manager) *coreauth.ProvimuxSessionBook {
	if m == nil {
		return coreauth.NewProvimuxSessionBook(nil)
	}
	mu.Lock()
	defer mu.Unlock()
	s := states[weak.Make(m)]
	if s == nil || s.book == nil {
		return coreauth.NewProvimuxSessionBook(m)
	}
	if !s.book.Subscribed() {
		applyLocked(m, s)
	}
	return s.book
}

// Managers is the number of managers this process holds a state for: for tests of retention.
func Managers() int {
	mu.Lock()
	defer mu.Unlock()
	return len(states)
}
