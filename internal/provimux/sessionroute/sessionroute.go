// Package sessionroute starts Provimux's session-route (.github/provimux/design/session-route.md): the book of each
// manager the server serves with, subscribed to the manager's results. In a test binary nothing is subscribed unless
// a test asks for it (EnableInTests), so every upstream test runs as on upstream.
package sessionroute

import (
	"sync"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var (
	mu        sync.Mutex
	books     = make(map[*coreauth.Manager]*coreauth.ProvimuxSessionBook)
	inTests   atomic.Bool
	observing = func() bool { return !testing.Testing() || inTests.Load() }
)

// EnableInTests makes Start subscribe in a test binary, until the returned function is called.
func EnableInTests() (restore func()) {
	previous := inTests.Swap(true)
	return func() { inTests.Store(previous) }
}

// Start returns the book of m, made once per manager and subscribed to m's results (outside a test binary, or with
// EnableInTests). The routes are registered with it; a manager the server never serves with has no book.
func Start(m *coreauth.Manager) *coreauth.ProvimuxSessionBook {
	if m == nil {
		return nil
	}
	mu.Lock()
	book := books[m]
	if book == nil {
		book = coreauth.NewProvimuxSessionBook(m)
		books[m] = book
	}
	mu.Unlock()
	if observing() {
		book.Subscribe()
	}
	return book
}

// Book returns the book of m: the one Start made, or, for a manager never started, an empty book that observes
// nothing (every conversation is "not-seen" there).
func Book(m *coreauth.Manager) *coreauth.ProvimuxSessionBook {
	if m == nil {
		return coreauth.NewProvimuxSessionBook(nil)
	}
	mu.Lock()
	defer mu.Unlock()
	if book := books[m]; book != nil {
		return book
	}
	return coreauth.NewProvimuxSessionBook(m)
}
