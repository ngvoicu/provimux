package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Provimux: the review's cases of session-route G1, each as a test (findings 1, 3, 4 and the book's growth). They use
// only the fixture of provimux_session_route_test.go as it was before the review, so that the same file runs on the
// reviewed commit, where they fail, and here, where they pass. Every account, key and session is invented.

// turnModel sends one turn of the conversation for model, with the client key's caller scope.
func (f *provimuxSRFixture) turnModel(model string, headers http.Header, scope string, extra map[string]any) error {
	f.t.Helper()
	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	metadata := map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}
	for k, v := range extra {
		metadata[k] = v
	}
	opts := cliproxyexecutor.Options{Headers: headers.Clone(), OriginalRequest: payload, SourceFormat: sdktranslator.FormatOpenAI, Metadata: metadata}
	_, err := f.m.Execute(context.Background(), f.providers, cliproxyexecutor.Request{Model: model, Payload: payload}, opts)
	return err
}

// serve registers models for both accounts besides the fixture's.
func (f *provimuxSRFixture) serve(models ...string) {
	f.t.Helper()
	infos := []*registry.ModelInfo{{ID: provimuxSRModel}}
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	for _, id := range []string{f.high, f.low} {
		auth, _ := f.m.GetByID(id)
		registry.GetGlobalRegistry().RegisterClient(id, auth.Provider, infos)
	}
}

// TestProvimuxSessionRouteDelimiterOwnership is finding 1: upstream joins provider, session and model into one string
// with "::", which a session id and a configured name may hold, so two different (session, name) pairs can make one
// string. Key A uses (x, "sr::m"); key B uses ("x::sr", "m"): the same upstream string. B may never show or release
// A's pair, which B never used; and the book never notes a pair whose name holds the delimiter (it could not tell
// whose it is), so A is answered "not-seen" for it, safely.
func TestProvimuxSessionRouteDelimiterOwnership(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	f.serve("sr::m", "m")
	scopeA, scopeB := provimuxSRScope("sk-client-a"), provimuxSRScope("sk-client-b")
	pairA := http.Header{"X-Session-Affinity": {"x"}}
	pairB := http.Header{"X-Session-Affinity": {"x::sr"}}
	if err := f.turnModel("sr::m", pairA, scopeA, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.turnModel("m", pairB, scopeB, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.book.Show(scopeB, pairA, nil, "sr::m"); got.Conversation != "not-seen" {
		t.Fatalf("key B shows the pair of key A, which it never used: %s", mustJSON(t, got))
	}
	if released, _ := f.book.Release(scopeB, pairA, nil, "sr::m", ""); released.Released || released.Conversation != "not-seen" {
		t.Fatalf("key B releases the pair of key A: %s", mustJSON(t, released))
	}
	key := "mixed::affinity:x::sr::m"
	if _, ok := f.binding(key); !ok {
		t.Fatal("the shared upstream binding is gone")
	}
	if got := f.book.Show(scopeA, pairA, nil, "sr::m"); got.Conversation != "not-seen" {
		t.Fatalf("a name holding the delimiter is noted: %s", mustJSON(t, got))
	}
	for _, entry := range f.book.Entries() {
		if strings.Contains(entry.ModelKey, "::") {
			t.Fatalf("the book noted a name holding the delimiter: %+v", entry)
		}
		if entry.hasScope(scopeA) && entry.Session != "affinity:x" {
			t.Fatalf("key A's scope is on another pair: %+v", entry)
		}
		if entry.hasScope(scopeB) && (entry.Session != "affinity:x::sr" || entry.ModelKey != "m") {
			t.Fatalf("key B's scope is on another pair: %+v", entry)
		}
	}
}

// TestProvimuxSessionRouteExpectUnderLock is finding 3 (a): between the check and the removal the binding is released
// and made again on the same login (a pinned turn); its token is another. The older token must not remove it: the
// answer is the conflict, with the state now, and the new binding stays.
func TestProvimuxSessionRouteExpectUnderLock(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"under-lock"}}
	f.coolHigh(30 * time.Minute)
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	old := *f.book.Show(scope, header, nil, provimuxSRModel).Binding
	var current string
	provimuxSeamBeforeRemove = func() {
		provimuxSeamBeforeRemove = nil
		if inner, _ := f.book.Release(scope, header, nil, provimuxSRModel, ""); !inner.Released {
			t.Fatal("inner release")
		}
		if err := f.turn(header, "", scope, map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: f.low}); err != nil {
			t.Fatal(err)
		}
		current = *f.book.Show(scope, header, nil, provimuxSRModel).Binding
	}
	defer func() { provimuxSeamBeforeRemove = nil }()
	released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, old)
	if current == old {
		t.Fatal("the binding made again has the old token")
	}
	if released.Released || conflict == nil {
		t.Fatalf("the old token released the new binding: release %s conflict %s", mustJSON(t, released), mustJSON(t, conflict))
	}
	if authID, ok := f.binding(key); !ok || authID != f.low {
		t.Fatalf("the new binding is gone: %q %v", authID, ok)
	}
}

// TestProvimuxSessionRouteSameLoginFailureRemovesNewer pins finding 4 as upstream's behaviour (decided by the lead):
// a request still running when its conversation was released and bound again to the same login, which then fails
// with a fault of that login, removes the newer binding (upstream removes a binding to a login that just failed). The
// conversation is bound afresh at its next message. A binding to another login is untouched
// (TestProvimuxSessionRouteInFlight).
func TestProvimuxSessionRouteSameLoginFailureRemovesNewer(t *testing.T) {
	entered := make(chan struct{}, 1)
	gate := make(chan error)
	call := func(_ context.Context, auth *Auth, req cliproxyexecutor.Request) error {
		if !strings.Contains(string(req.Payload), `"hold":true`) || !strings.Contains(auth.ID, "low") {
			return nil
		}
		entered <- struct{}{}
		return <-gate
	}
	f := newProvimuxSRFixture(t, "round-robin", false, call)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"same-login"}}
	f.coolHigh(30 * time.Minute)
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	done := make(chan error, 1)
	go func() { done <- f.turn(header, `,"hold":true`, scope, nil) }()
	<-entered
	if released, _ := f.book.Release(scope, header, nil, provimuxSRModel, ""); !released.Released {
		t.Fatal("release")
	}
	// Bound again, to the same login: the higher account still cools.
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	if authID, _ := f.binding(key); authID != f.low {
		t.Fatalf("bound again to %q, want %q", authID, f.low)
	}
	gate <- &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
	<-done
	if authID, ok := f.binding(key); ok {
		t.Fatalf("upstream keeps a binding to the login that just failed: %q", authID)
	}
	// Its next message binds afresh: the higher account is free again.
	expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	if authID, ok := f.binding(key); !ok || authID != f.high {
		t.Fatalf("next message bound %q (%v), want %q", authID, ok, f.high)
	}
}

// TestProvimuxSessionRouteBookForgetsExpired is the review's growth finding: an entry leaves the book once its
// binding is gone upstream (expired, released or removed) and the binding's lifetime since its last answer has passed;
// an entry whose binding upstream still holds stays.
func TestProvimuxSessionRouteBookForgetsExpired(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	scope := provimuxSRScope("sk-client-one")
	gone := http.Header{"X-Session-Affinity": {"expires"}}
	kept := http.Header{"X-Session-Affinity": {"stays"}}
	start := time.Now()
	restore := SetProvimuxSessionRouteClockForTest(func() time.Time { return start })
	defer restore()
	for _, header := range []http.Header{gone, kept} {
		if err := f.turn(header, "", scope, nil); err != nil {
			t.Fatal(err)
		}
	}
	var goneKey string
	for _, entry := range f.book.Entries() {
		if entry.Session == "affinity:expires" {
			goneKey = entry.Key
		}
	}
	// Upstream's binding ends (as at its expiry).
	f.affinity.cache.Invalidate(goneKey)
	SetProvimuxSessionRouteClockForTest(func() time.Time { return start.Add(2 * time.Hour) })
	if show := f.book.Show(scope, gone, nil, provimuxSRModel); show.Conversation != "not-seen" {
		t.Fatalf("an expired conversation is still in the book: %s", mustJSON(t, show))
	}
	entries := f.book.Entries()
	if len(entries) != 1 || entries[0].Session != "affinity:stays" {
		t.Fatalf("after the expiry the book holds %+v, want only the live one", entries)
	}
}

// TestProvimuxSessionRouteForkIsNotSubagent: a Codex fork names its source as its parent; it is a fork, not a
// sub-agent (the review's seam cases).
func TestProvimuxSessionRouteForkIsNotSubagent(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Codex-Turn-Metadata": {`{"session_id":"s","thread_id":"t","forked_from_thread_id":"p"}`}}
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	show := f.book.Show(scope, header, nil, provimuxSRModel)
	if show.Conversation != "bound" || show.Subagent {
		t.Fatalf("fork: %s", mustJSON(t, show))
	}
}
