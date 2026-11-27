package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Provimux: the tests of session-route G1 (.github/provimux/design/session-route.md 12, rows 2, 3, 4 and 13, and the
// points I1 and I3 of section 14). Every account, key and session here is invented, and every executor is a stand-in
// in this process: nothing is sent anywhere. No test sleeps: an ordering is made by channels at the point of the race,
// and every time is taken from the clock the test sets or from the entry upstream made.

const provimuxSRModel = "provimux-sr-model"

// provimuxSRCall is one call of the stand-in executor: it may hold the call until the test lets it go.
type provimuxSRCall func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request) error

// provimuxSRExecutor answers every call with success unless call says otherwise.
type provimuxSRExecutor struct {
	provider string
	call     provimuxSRCall
}

func (e provimuxSRExecutor) Identifier() string { return e.provider }

func (e provimuxSRExecutor) run(ctx context.Context, auth *Auth, req cliproxyexecutor.Request) error {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if e.call != nil {
		return e.call(ctx, auth, req)
	}
	return nil
}

func (e provimuxSRExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.run(ctx, auth, req); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"sr","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)}, nil
}

func (e provimuxSRExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.run(ctx, auth, req); err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"id":"sr","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e provimuxSRExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.run(ctx, auth, req); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":1}`)}, nil
}

func (provimuxSRExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (provimuxSRExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_supported", Message: "stand-in"}
}

// provimuxSRFixture is a manager with session affinity and two accounts that serve one name: high (priority 10) and
// low (priority 5), of one provider or, mixed, of two.
type provimuxSRFixture struct {
	t         *testing.T
	m         *Manager
	affinity  *SessionAffinitySelector
	book      *ProvimuxSessionBook
	providers []string
	high, low string
}

func newProvimuxSRFixture(t *testing.T, strategy string, mixed bool, call provimuxSRCall) *provimuxSRFixture {
	t.Helper()
	var fallback Selector
	switch strategy {
	case "fill-first":
		fallback = &FillFirstSelector{}
	case "weighted-round-robin":
		fallback = &WeightedRoundRobinSelector{}
	default:
		fallback = &RoundRobinSelector{}
	}
	m := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: fallback, TTL: time.Hour})
	t.Cleanup(affinity.Stop)
	m.SetSelector(affinity)
	tag := strings.ReplaceAll(t.Name(), "/", "-")
	f := &provimuxSRFixture{t: t, m: m, affinity: affinity}
	providerHigh, providerLow := "provimux-sr-a-"+tag, "provimux-sr-a-"+tag
	if mixed {
		providerLow = "provimux-sr-b-" + tag
	}
	f.providers = []string{providerHigh}
	if mixed {
		f.providers = append(f.providers, providerLow)
	}
	for _, provider := range f.providers {
		m.RegisterExecutor(provimuxSRExecutor{provider: provider, call: call})
	}
	f.high, f.low = "sr-high-"+tag, "sr-low-"+tag
	for _, auth := range []*Auth{
		{ID: f.high, Provider: providerHigh, Status: StatusActive, Attributes: map[string]string{"priority": "10", "api_key": "sk-sr-high", "base_url": "https://high.example.test/v1"}},
		{ID: f.low, Provider: providerLow, Status: StatusActive, Attributes: map[string]string{"priority": "5", "api_key": "sk-sr-low", "base_url": "https://low.example.test/v1"}},
	} {
		if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: provimuxSRModel}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	f.book = NewProvimuxSessionBook(m)
	f.book.Subscribe()
	t.Cleanup(f.book.Unsubscribe)
	return f
}

// provimuxSRScope is the caller scope of an invented client key, as the handlers compute it.
func provimuxSRScope(key string) string { return cliproxysession.CallerScope(key) }

// turn sends one request of the conversation, as the handlers would, with the client key's caller scope.
func (f *provimuxSRFixture) turn(headers http.Header, body string, scope string, extra map[string]any) error {
	f.t.Helper()
	payload := []byte(`{"model":"` + provimuxSRModel + `","messages":[{"role":"user","content":"hi"}]` + body + `}`)
	metadata := map[string]any{}
	if scope != "" {
		metadata[cliproxyexecutor.CallerScopeMetadataKey] = scope
	}
	for k, v := range extra {
		metadata[k] = v
	}
	opts := cliproxyexecutor.Options{Headers: headers.Clone(), OriginalRequest: payload, SourceFormat: sdktranslator.FormatOpenAI, Metadata: metadata}
	_, err := f.m.Execute(context.Background(), f.providers, cliproxyexecutor.Request{Model: provimuxSRModel, Payload: payload}, opts)
	return err
}

// coolHigh records a quota cooldown on the high account for the name, as a 429 with Retry-After does.
func (f *provimuxSRFixture) coolHigh(retryAfter time.Duration) time.Time {
	f.t.Helper()
	f.m.MarkResult(context.Background(), Result{AuthID: f.high, Provider: f.providers[0], Model: provimuxSRModel, Success: false,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}, RetryAfter: &retryAfter})
	auth, _ := f.m.GetByID(f.high)
	view, ok := f.m.ProvimuxRouteCooldowns(auth, []string{provimuxSRModel}, time.Now())[provimuxSRModel]
	if !ok {
		f.t.Fatal("no cooldown recorded on the high account")
	}
	return view.RetryAt
}

// binding is upstream's binding under key now.
func (f *provimuxSRFixture) binding(key string) (string, bool) {
	b, ok := provimuxSeamLookup(f.affinity, key)
	return b.AuthID, ok
}

// aliasesOf is every key of the group upstream's cache holds key in.
func (f *provimuxSRFixture) aliasesOf(key string) []string {
	f.affinity.cache.mu.RLock()
	defer f.affinity.cache.mu.RUnlock()
	return append([]string(nil), f.affinity.cache.entries[key].aliases...)
}

// held is the subset of keys upstream's cache holds now.
func (f *provimuxSRFixture) held(keys []string) []string {
	f.affinity.cache.mu.RLock()
	defer f.affinity.cache.mu.RUnlock()
	var out []string
	for _, key := range keys {
		if _, ok := f.affinity.cache.entries[key]; ok {
			out = append(out, key)
		}
	}
	return out
}

// provimuxSRKinds are the conversations of section 4.2: each session header upstream's affinity reads, and each body
// field, with what the turn carries and what the show request carries for it.
var provimuxSRKinds = []struct {
	name     string
	header   http.Header
	body     string // fields added to the turn's body
	identity string // the show's identity object, or ""
	subagent bool
	codex    bool
}{
	{name: "X-Claude-Code-Session-Id", header: http.Header{"X-Claude-Code-Session-Id": {"cc-0001"}}},
	{name: "X-Claude-Code-Session-Id with X-Claude-Code-Agent-Id", header: http.Header{"X-Claude-Code-Session-Id": {"cc-0002"}, "X-Claude-Code-Agent-Id": {"agent-7"}}, subagent: true},
	{name: "metadata.user_id of Claude Code", body: `,"metadata":{"user_id":"user_abc_account__session_0c9b1f0e-6a51-4c58-9b1e-2f6f3f1f7a10"}`, identity: `{"metadata":{"user_id":"user_abc_account__session_0c9b1f0e-6a51-4c58-9b1e-2f6f3f1f7a10"}}`},
	{name: "Session-Id", header: http.Header{"Session-Id": {"codex-0003"}}, codex: true},
	{name: "Thread-Id", header: http.Header{"Thread-Id": {"thread-0004"}}, codex: true},
	{name: "X-Http-Session-Id", header: http.Header{"X-Http-Session-Id": {"http-0005"}}},
	{name: "X-Session-ID", header: http.Header{"X-Session-Id": {"main"}}},
	{name: "X-Session-Affinity", header: http.Header{"X-Session-Affinity": {"pi-0006"}}},
	{name: "X-Slot-Session-Id", header: http.Header{"X-Slot-Session-Id": {"slot-0007"}}},
	{name: "X-Task-ID", header: http.Header{"X-Task-Id": {"task-0008"}}},
	{name: "X-Conversation-Id", header: http.Header{"X-Conversation-Id": {"conv-0009"}}},
	{name: "X-Thread-Id", header: http.Header{"X-Thread-Id": {"xthread-0010"}}},
	{name: "X-Client-Request-Id", header: http.Header{"X-Client-Request-Id": {"req-0011"}}},
	{name: "cachedContent", body: `,"cachedContent":"cachedContents/sr-0012"`, identity: `{"cachedContent":"cachedContents/sr-0012"}`},
	{name: "thread_id", body: `,"thread_id":"sr-0013"`, identity: `{"thread_id":"sr-0013"}`},
	{name: "session_id", body: `,"session_id":"sr-0014"`, identity: `{"session_id":"sr-0014"}`},
	{name: "prompt_cache_key", body: `,"prompt_cache_key":"project-a"`, identity: `{"prompt_cache_key":"project-a"}`},
	{name: "prompt_cache_key with conversation (two aliases)", body: `,"prompt_cache_key":"project-b","conversation":"conv-b"`, identity: `{"prompt_cache_key":"project-b","conversation":"conv-b"}`},
	{name: "conversation", body: `,"conversation":"sr-0016"`, identity: `{"conversation":"sr-0016"}`},
	{name: "metadata.user_id", body: `,"metadata":{"user_id":"sr-plain-0017"}`, identity: `{"metadata":{"user_id":"sr-plain-0017"}}`},
	{name: "conversation_id", body: `,"conversation_id":"sr-0018"`, identity: `{"conversation_id":"sr-0018"}`},
}

// TestProvimuxSessionRouteSeam is the pin of section 7.3 (rows 2 and 3): for each kind of identity of section 4.2,
// under each strategy, on one provider and on two, a conversation that bound to the lower account while the higher
// one cooled is noted under the key upstream's cache holds; the show names it from the headers and body a turn
// carries; the release leaves no key of it in the cache; and the next turn binds at the highest available priority.
func TestProvimuxSessionRouteSeam(t *testing.T) {
	scope := provimuxSRScope("sk-client-one")
	for _, strategy := range []string{"round-robin", "fill-first", "weighted-round-robin"} {
		for _, mixed := range []bool{false, true} {
			for _, kind := range provimuxSRKinds {
				name := strategy + "/single/" + kind.name
				if mixed {
					name = strategy + "/mixed/" + kind.name
				}
				t.Run(name, func(t *testing.T) {
					f := newProvimuxSRFixture(t, strategy, mixed, nil)
					f.coolHigh(30 * time.Minute)
					if errTurn := f.turn(kind.header, kind.body, scope, nil); errTurn != nil {
						t.Fatalf("turn: %v", errTurn)
					}
					entries := f.book.Entries()
					if len(entries) != 1 {
						t.Fatalf("book has %d entries, want 1: %+v", len(entries), entries)
					}
					entry := entries[0]
					if authID, ok := f.binding(entry.Key); !ok || authID != f.low || entry.Current != f.low {
						t.Fatalf("book key %q: cache holds %q (%v), book says %q; want both %q", entry.Key, authID, ok, entry.Current, f.low)
					}
					if (entry.Parent != "") != kind.subagent {
						t.Fatalf("parent %q, want sub-agent %v", entry.Parent, kind.subagent)
					}

					show := f.book.Show(scope, kind.header, []byte(kind.identity), provimuxSRModel)
					if show.Conversation != "bound" || show.Bound == nil || show.Bound.Route.Priority != 5 || show.Bound.Origin != "first" {
						t.Fatalf("show: %s", mustJSON(t, show))
					}
					if !show.Position.BelowFirst || show.Position.HigherPriorityFree || show.Subagent != kind.subagent || show.WebsocketMayHold != kind.codex {
						t.Fatalf("show position or flags: %s", mustJSON(t, show))
					}

					aliases := f.aliasesOf(entry.Key)
					if strings.Contains(kind.name, "two aliases") && len(aliases) != 2 {
						t.Fatalf("group %v, want two aliases", aliases)
					}
					expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
					show = f.book.Show(scope, kind.header, []byte(kind.identity), provimuxSRModel)
					if !show.Position.HigherPriorityFree || len(show.First) != 1 || show.First[0].Priority != 10 {
						t.Fatalf("show after recovery: %s", mustJSON(t, show))
					}
					released, conflict := f.book.Release(scope, kind.header, []byte(kind.identity), provimuxSRModel, *show.Binding)
					if conflict != nil || !released.Released || released.Was == nil || released.Was.Priority != 5 || len(released.Next) != 1 || released.Next[0].Priority != 10 {
						t.Fatalf("release: %s %s", mustJSON(t, released), mustJSON(t, conflict))
					}
					if keys := f.held(aliases); len(keys) != 0 {
						t.Fatalf("cache still holds %v after the release", keys)
					}

					if errTurn := f.turn(kind.header, kind.body, scope, nil); errTurn != nil {
						t.Fatalf("turn after release: %v", errTurn)
					}
					if authID, ok := f.binding(entry.Key); !ok || authID != f.high {
						t.Fatalf("after the release the next turn bound %q (%v), want the first route %q", authID, ok, f.high)
					}
					show = f.book.Show(scope, kind.header, []byte(kind.identity), provimuxSRModel)
					if show.Bound == nil || show.Bound.Origin != "first" || show.Bound.Route.Priority != 10 || show.Position.BelowFirst {
						t.Fatalf("show after the next turn: %s", mustJSON(t, show))
					}
				})
			}
		}
	}
}

// TestProvimuxSessionRouteResultKeys settles I1: the result every hook receives carries the keys Pick wrote, on the
// non-streaming, streaming and counting paths; and where the keys are missing the seam derives them as OnResult does
// and the book notes a binding only when upstream's cache holds that key for the account that answered.
func TestProvimuxSessionRouteResultKeys(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", true, nil)
	var got []Result
	remove := f.m.AddHook(provimuxSRHook(func(res Result) { got = append(got, res) }))
	defer remove()
	header := http.Header{"X-Session-Affinity": {"keys-0001"}}
	payload := []byte(`{"model":"` + provimuxSRModel + `","messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{Headers: header, OriginalRequest: payload, SourceFormat: sdktranslator.FormatOpenAI}
	req := cliproxyexecutor.Request{Model: provimuxSRModel, Payload: payload}
	if _, err := f.m.Execute(context.Background(), f.providers, req, opts); err != nil {
		t.Fatal(err)
	}
	stream, err := f.m.ExecuteStream(context.Background(), f.providers, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	if _, err := f.m.ExecuteCount(context.Background(), f.providers, req, opts); err != nil {
		t.Fatal(err)
	}
	// The stream's result is recorded by the goroutine that closes Chunks, before it closes them.
	if len(got) != 3 {
		t.Fatalf("hook saw %d results, want 3", len(got))
	}
	for i, res := range got {
		key, ok := provimuxSeamResultKey(res)
		if !ok || key.Derived {
			t.Fatalf("result %d: keys not as Pick wrote them: %+v %v", i, key, ok)
		}
		if authID, bound := f.binding(key.Key); !bound || authID != res.AuthID {
			t.Fatalf("result %d: key %q holds %q (%v), result is %q", i, key.Key, authID, bound, res.AuthID)
		}
		if res.Provider == key.Provider {
			t.Fatalf("result %d: the result's provider %q is the key's: the derivation test below would not tell", i, res.Provider)
		}
	}

	// Keys lost: OnResult's derivation takes the result's own provider, which is not the key Pick made ("mixed"),
	// so the cache does not hold it and the book notes no binding, only the caller.
	stripped := got[0]
	stripped.Options.Metadata = map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "scope-lost"}
	key, ok := provimuxSeamResultKey(stripped)
	if !ok || !key.Derived || key.Provider != stripped.Provider {
		t.Fatalf("derived key: %+v %v", key, ok)
	}
	book := NewProvimuxSessionBook(f.m)
	book.OnResult(context.Background(), stripped)
	entries := book.Entries()
	if len(entries) != 1 || entries[0].Current != "" || !entries[0].hasScope("scope-lost") {
		t.Fatalf("a derived key the cache does not hold noted %+v", entries)
	}

	// Keys lost on a binding made under the result's own provider: the derived key is upstream's, and it is noted.
	if _, errPick := f.affinity.Pick(context.Background(), stripped.Provider, provimuxSRModel, cliproxyexecutor.Options{Headers: header}, []*Auth{mustAuth(t, f.m, stripped.AuthID)}); errPick != nil {
		t.Fatal(errPick)
	}
	book = NewProvimuxSessionBook(f.m)
	book.OnResult(context.Background(), stripped)
	entries = book.Entries()
	if len(entries) != 1 || entries[0].Current != stripped.AuthID || entries[0].Key != key.Key {
		t.Fatalf("a derived key the cache holds was not noted: %+v", entries)
	}
}

// TestProvimuxSessionRouteInFlight is row 4: an answer in flight at a release finishes where it started; its success
// does not bring the binding back, and its failure does not remove the newer binding the next turn made.
func TestProvimuxSessionRouteInFlight(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			entered := make(chan string, 1)
			gate := make(chan error)
			call := func(_ context.Context, auth *Auth, req cliproxyexecutor.Request) error {
				if !strings.Contains(string(req.Payload), `"hold":true`) || !strings.Contains(auth.ID, "low") {
					return nil
				}
				entered <- auth.ID
				return <-gate
			}
			f := newProvimuxSRFixture(t, "round-robin", false, call)
			scope := provimuxSRScope("sk-client-one")
			header := http.Header{"X-Session-Affinity": {"flight-0001"}}
			f.coolHigh(30 * time.Minute)
			if err := f.turn(header, "", scope, nil); err != nil {
				t.Fatal(err)
			}
			key := f.book.Entries()[0].Key

			done := make(chan error, 1)
			go func() { done <- f.turn(header, `,"hold":true`, scope, nil) }()
			if got := <-entered; got != f.low {
				t.Fatalf("held answer is on %q, want %q", got, f.low)
			}
			// The answer is in flight on low. Release now.
			if released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, ""); conflict != nil || !released.Released {
				t.Fatalf("release: %+v %+v", released, conflict)
			}
			if _, ok := f.binding(key); ok {
				t.Fatal("binding still there after the release")
			}

			if outcome == "success" {
				gate <- nil
				if err := <-done; err != nil {
					t.Fatalf("held answer: %v", err)
				}
				if authID, ok := f.binding(key); ok {
					t.Fatalf("the held answer's success brought the binding back to %q", authID)
				}
				if show := f.book.Show(scope, header, nil, provimuxSRModel); show.Conversation != "not-bound" {
					t.Fatalf("show after the held success: %s", mustJSON(t, show))
				}
				return
			}
			// A newer binding: the high account is free again and the next turn binds there.
			expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
			if err := f.turn(header, "", scope, nil); err != nil {
				t.Fatal(err)
			}
			if authID, _ := f.binding(key); authID != f.high {
				t.Fatalf("newer binding on %q, want %q", authID, f.high)
			}
			gate <- &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
			<-done // it may retry on another account; either way it ends
			if authID, ok := f.binding(key); !ok || authID != f.high {
				t.Fatalf("after the held failure the binding is %q (%v), want the newer %q", authID, ok, f.high)
			}
		})
	}
}

// TestProvimuxSessionRoutePinnedFollowUpRebinds pins I3 as upstream does it today (decided by the lead on 2026-11-27,
// option A): a follow-up turn pinned to its login, as on an open Codex WebSocket, still asks the selector, and after a
// release it binds the conversation to the pinned login again. So a release holds only once that connection is closed.
// Row 13 as decided: after a release and a closed socket, the next connection's first turn binds at the first route.
// If this test fails at a sync, upstream changed how pinned turns bind: correct the contract's websocket_may_hold.
func TestProvimuxSessionRoutePinnedFollowUpRebinds(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"Session-Id": {"ws-0001"}}
	f.coolHigh(30 * time.Minute)
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
	show := f.book.Show(scope, header, nil, provimuxSRModel)
	if !show.WebsocketMayHold {
		t.Fatalf("a conversation named by Codex headers must say websocket_may_hold: %s", mustJSON(t, show))
	}
	if released, _ := f.book.Release(scope, header, nil, provimuxSRModel, ""); !released.Released || !released.WebsocketMayHold {
		t.Fatalf("release: %s", mustJSON(t, released))
	}

	// The socket is still open: its next turn is pinned to the login that served it.
	if err := f.turn(header, "", scope, map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: f.low}); err != nil {
		t.Fatal(err)
	}
	if authID, ok := f.binding(key); !ok || authID != f.low {
		t.Fatalf("pinned follow-up: binding %q (%v); upstream re-binds to the pinned login %q today", authID, ok, f.low)
	}

	// Released again, then the socket closes: the next connection's first turn is not pinned and binds at the first route.
	if released, _ := f.book.Release(scope, header, nil, provimuxSRModel, ""); !released.Released {
		t.Fatalf("second release: %s", mustJSON(t, released))
	}
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	if authID, ok := f.binding(key); !ok || authID != f.high {
		t.Fatalf("first turn of the next connection bound %q (%v), want the first route %q", authID, ok, f.high)
	}
}

// TestProvimuxSessionRouteHigherPriorityFreeAt is the lead's addition of 2026-11-27: every show answers from the state
// at that moment, on the clock the test sets. Before the recorded cooldown of the higher account ends, no higher route
// is free and higher_priority_free_at is that end (the earliest of two); after it, one is free and the time is null.
// The show changes nothing: the binding's expiry and the book are the same after it.
func TestProvimuxSessionRouteHigherPriorityFreeAt(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	// A second higher account, of priority 8, that serves the name too, cooled for longer.
	second := &Auth{ID: f.high + "-second", Provider: f.providers[0], Status: StatusActive, Attributes: map[string]string{"priority": "8", "api_key": "sk-sr-second"}}
	if _, err := f.m.Register(WithSkipPersist(context.Background()), second); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(second.ID, second.Provider, []*registry.ModelInfo{{ID: provimuxSRModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(second.ID) })
	retryAfter := 2 * time.Hour
	f.m.MarkResult(context.Background(), Result{AuthID: second.ID, Provider: second.Provider, Model: provimuxSRModel, Success: false,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}, RetryAfter: &retryAfter})
	highEnd := f.coolHigh(30 * time.Minute)

	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"free-at-0001"}}
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	before, _ := provimuxSeamLookup(f.affinity, key)
	entriesBefore := f.book.Entries()

	restore := SetProvimuxSessionRouteClockForTest(func() time.Time { return highEnd.Add(-time.Second) })
	defer restore()
	show := f.book.Show(scope, header, nil, provimuxSRModel)
	if show.Position == nil || show.Position.HigherPriorityFree || show.Position.HigherPriorityFreeAt == nil || *show.Position.HigherPriorityFreeAt != provimuxTime(highEnd) {
		t.Fatalf("one second before the cooldown ends: %s, want free at %s", mustJSON(t, show), provimuxTime(highEnd))
	}
	if len(show.First) != 1 || show.First[0].Priority != 5 {
		t.Fatalf("first routes while both higher accounts cool: %s", mustJSON(t, show.First))
	}

	SetProvimuxSessionRouteClockForTest(func() time.Time { return highEnd.Add(time.Second) })
	show = f.book.Show(scope, header, nil, provimuxSRModel)
	if show.Position == nil || !show.Position.HigherPriorityFree || show.Position.HigherPriorityFreeAt != nil {
		t.Fatalf("one second after the cooldown ends: %s", mustJSON(t, show))
	}
	if len(show.First) != 1 || show.First[0].Priority != 10 {
		t.Fatalf("first routes after the cooldown ends: %s", mustJSON(t, show.First))
	}

	after, _ := provimuxSeamLookup(f.affinity, key)
	if !after.ExpiresAt.Equal(before.ExpiresAt) || after.AuthID != before.AuthID {
		t.Fatalf("the show moved the binding: %+v -> %+v", before, after)
	}
	if mustJSON(t, f.book.Entries()) != mustJSON(t, entriesBefore) {
		t.Fatal("the show changed the book")
	}
}

// TestProvimuxSessionRouteShowsFallback: a conversation that fell over from the higher account to the lower one is
// shown as such, with the route it fell from and when, on the clock the test sets; the cache's last use and coldness
// follow the binding's own expiry, read from the entry upstream made.
func TestProvimuxSessionRouteShowsFallback(t *testing.T) {
	fail := make(chan struct{}, 1)
	call := func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request) error {
		if strings.Contains(auth.ID, "high") {
			select {
			case <-fail:
				return &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
			default:
			}
		}
		return nil
	}
	f := newProvimuxSRFixture(t, "round-robin", false, call)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"fell-0001"}}
	base := time.Now()
	restore := SetProvimuxSessionRouteClockForTest(func() time.Time { return base })
	defer restore()
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	SetProvimuxSessionRouteClockForTest(func() time.Time { return base.Add(10 * time.Minute) })
	fail <- struct{}{}
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	if authID, _ := f.binding(key); authID != f.low {
		t.Fatalf("after the failover the binding is %q, want %q", authID, f.low)
	}
	binding, _ := provimuxSeamLookup(f.affinity, key)
	lastUse := binding.ExpiresAt.Add(-binding.TTL)
	if want := base.Add(10 * time.Minute); want.After(lastUse) {
		lastUse = want
	}
	coldAt := lastUse.Add(time.Hour + time.Minute)

	SetProvimuxSessionRouteClockForTest(func() time.Time { return coldAt.Add(-time.Second) })
	show := f.book.Show(scope, header, nil, provimuxSRModel)
	if show.Bound == nil || show.Bound.Origin != "fallback" || show.Bound.FellBackFrom == nil || show.Bound.FellBackFrom.Priority != 10 ||
		show.Bound.Since == nil || *show.Bound.Since != provimuxTime(base.Add(10*time.Minute)) || show.Bound.FellBackAt == nil || *show.Bound.FellBackAt != *show.Bound.Since {
		t.Fatalf("fell back: %s", mustJSON(t, show))
	}
	if show.Cache == nil || show.Cache.Cold || show.Cache.Basis != "setting" || show.Cache.LifetimeS != 3600 || show.Cache.ColdAt != provimuxTime(coldAt) || show.Cache.LastUseAt != provimuxTime(lastUse) {
		t.Fatalf("cache one second before cold: %s (last use %s)", mustJSON(t, show.Cache), provimuxTime(lastUse))
	}
	SetProvimuxSessionRouteClockForTest(func() time.Time { return coldAt.Add(time.Second) })
	if show = f.book.Show(scope, header, nil, provimuxSRModel); show.Cache == nil || !show.Cache.Cold {
		t.Fatalf("cache one second after cold: %s", mustJSON(t, show.Cache))
	}
	if show.Rule.InEffect || show.Rule.Will != "stay" {
		t.Fatalf("the rule is G2's; until then: %s", mustJSON(t, show.Rule))
	}
}

// TestProvimuxSessionRouteStrict: a key sees and releases only conversations it was seen using since the start
// (decided by the lead on 2026-11-27). Another key's conversation and one that does not exist get the same answer,
// and a release by another key removes nothing.
func TestProvimuxSessionRouteStrict(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	restore := SetProvimuxSessionRouteClockForTest(func() time.Time { return time.Unix(0, 0).Add(1000 * time.Hour) })
	defer restore()
	owner, other := provimuxSRScope("sk-client-one"), provimuxSRScope("sk-client-two")
	header := http.Header{"X-Session-Id": {"main"}}
	if err := f.turn(header, "", owner, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key

	theirs := mustJSON(t, f.book.Show(other, header, nil, provimuxSRModel))
	none := mustJSON(t, f.book.Show(other, http.Header{"X-Session-Id": {"never-used"}}, nil, provimuxSRModel))
	noKey := mustJSON(t, f.book.Show("", header, nil, provimuxSRModel))
	if theirs != none || theirs != noKey || !strings.Contains(theirs, `"conversation":"not-seen"`) {
		t.Fatalf("another key's conversation:\n%s\nnone:\n%s\nno key:\n%s", theirs, none, noKey)
	}
	releasedTheirs, _ := f.book.Release(other, header, nil, provimuxSRModel, "")
	releasedNone, _ := f.book.Release(other, http.Header{"X-Session-Id": {"never-used"}}, nil, provimuxSRModel, "")
	if mustJSON(t, releasedTheirs) != mustJSON(t, releasedNone) || releasedTheirs.Released || releasedTheirs.Conversation != "not-seen" {
		t.Fatalf("release by another key: %s / %s", mustJSON(t, releasedTheirs), mustJSON(t, releasedNone))
	}
	if authID, ok := f.binding(key); !ok || authID == "" {
		t.Fatal("another key's release removed the binding")
	}

	// Once the other key uses the conversation, it may see and release it too (one binding, two scopes).
	if err := f.turn(header, "", other, nil); err != nil {
		t.Fatal(err)
	}
	if show := f.book.Show(other, header, nil, provimuxSRModel); show.Conversation != "bound" {
		t.Fatalf("after its own turn: %s", mustJSON(t, show))
	}
	// Releasing twice: the second is not an error.
	if released, _ := f.book.Release(owner, header, nil, provimuxSRModel, ""); !released.Released {
		t.Fatalf("first release: %s", mustJSON(t, released))
	}
	if released, _ := f.book.Release(owner, header, nil, provimuxSRModel, ""); released.Released || released.Conversation != "not-bound" || released.Was != nil {
		t.Fatalf("second release: %s", mustJSON(t, released))
	}
}

// TestProvimuxSessionRouteExpect: the token stands for exactly this binding: the selection that made or last chose it,
// on this account. A show twice with nothing in between gives one token; a turn is a new selection, so it gives
// another, and the older token is refused (409) and releases nothing; so is a token of a binding released and made
// again. The current token releases.
func TestProvimuxSessionRouteExpect(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	clock := time.Now()
	restore := SetProvimuxSessionRouteClockForTest(func() time.Time { return clock })
	defer restore()
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"expect-0001"}}
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	first := *f.book.Show(scope, header, nil, provimuxSRModel).Binding
	if again := *f.book.Show(scope, header, nil, provimuxSRModel).Binding; again != first {
		t.Fatalf("two shows with nothing in between gave two tokens: %s, %s", first, again)
	}
	clock = clock.Add(time.Minute)
	SetProvimuxSessionRouteClockForTest(func() time.Time { return clock })
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	afterTurn := *f.book.Show(scope, header, nil, provimuxSRModel).Binding
	if afterTurn == first {
		t.Fatal("a turn (a new selection) kept the token")
	}
	if released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, first); conflict == nil || released.Released {
		t.Fatalf("token from before the turn: release %s conflict %s", mustJSON(t, released), mustJSON(t, conflict))
	}
	if released, _ := f.book.Release(scope, header, nil, provimuxSRModel, ""); !released.Released {
		t.Fatal("release")
	}
	clock = clock.Add(time.Minute)
	SetProvimuxSessionRouteClockForTest(func() time.Time { return clock })
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, afterTurn)
	if conflict == nil || conflict.Conversation != "bound" || released.Released {
		t.Fatalf("stale token: release %s conflict %s", mustJSON(t, released), mustJSON(t, conflict))
	}
	if _, ok := f.binding(key); !ok {
		t.Fatal("a refused release removed the binding")
	}
	if released, conflict = f.book.Release(scope, header, nil, provimuxSRModel, *conflict.Binding); conflict != nil || !released.Released {
		t.Fatalf("release with the current token: %s %s", mustJSON(t, released), mustJSON(t, conflict))
	}
}

// TestProvimuxSessionRouteReleaseComparesAccount: a binding that moved to another account between the judgement and
// the removal is left (never a wrong release); without expect the release looks again and removes what is bound now.
func TestProvimuxSessionRouteReleaseComparesAccount(t *testing.T) {
	f := newProvimuxSRFixture(t, "round-robin", false, nil)
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"moved-0001"}}
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	key := f.book.Entries()[0].Key
	token := *f.book.Show(scope, header, nil, provimuxSRModel).Binding
	moved := false
	provimuxSeamBeforeRemove = func() {
		if moved {
			return
		}
		moved = true
		f.affinity.cache.SetAliases(f.low, key)
	}
	defer func() { provimuxSeamBeforeRemove = nil }()
	if released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, token); conflict == nil || released.Released {
		t.Fatalf("a binding moved in between: release %s conflict %s", mustJSON(t, released), mustJSON(t, conflict))
	}
	if authID, _ := f.binding(key); authID != f.low {
		t.Fatalf("the moved binding is %q, want it left on %q", authID, f.low)
	}
	onLow := func(b provimuxSeamBinding) bool { return b.AuthID == f.low }
	if !provimuxSeamRelease(f.affinity, key, onLow) || provimuxSeamRelease(f.affinity, key, onLow) {
		t.Fatal("seam release: removes once, then nothing")
	}
}

// TestProvimuxSessionRouteNotRunning: with session affinity off, in Home mode, or under a plugin scheduler, the book
// notes nothing, and both answers say why: affinity off, home or not-supported, nothing bound, nothing released.
func TestProvimuxSessionRouteNotRunning(t *testing.T) {
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"idle-0001"}}
	for _, mode := range []string{"off", "home", "not-supported"} {
		t.Run(mode, func(t *testing.T) {
			f := newProvimuxSRFixture(t, "round-robin", false, nil)
			switch mode {
			case "off":
				f.m.SetSelector(&RoundRobinSelector{})
			case "home":
				cfg := &internalconfig.Config{}
				cfg.Home.Enabled = true
				f.m.runtimeConfig.Store(cfg)
			case "not-supported":
				f.m.SetPluginScheduler(provimuxSRScheduler{})
			}
			if mode != "home" {
				_ = f.turn(header, "", scope, nil)
			} else {
				// Home dispatches remotely; a result reported as Home's still reaches the hooks.
				f.book.OnResult(context.Background(), Result{AuthID: f.low, Provider: f.providers[0], Model: provimuxSRModel, Success: true,
					Options: cliproxyexecutor.Options{Headers: header, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}})
			}
			if entries := f.book.Entries(); len(entries) != 0 {
				t.Fatalf("book noted %+v", entries)
			}
			show := f.book.Show(scope, header, nil, provimuxSRModel)
			if show.Affinity != mode || show.Conversation != "not-bound" || show.Binding != nil || show.Bound != nil || show.Position != nil || show.Cache != nil || len(show.First) != 0 || show.Rule.InEffect {
				t.Fatalf("show: %s", mustJSON(t, show))
			}
			released, conflict := f.book.Release(scope, header, nil, provimuxSRModel, "b.aaaaaaaaaaaaaaaa")
			if conflict != nil || released.Affinity != mode || released.Released || released.Conversation != "not-bound" || released.Was != nil || len(released.Next) != 0 {
				t.Fatalf("release: %s", mustJSON(t, released))
			}
		})
	}
}

// TestProvimuxSessionRouteRouteFields is row 6 at this level: a route holds exactly the fields of a row of
// GET /v1/models?include=accounts (the handler's own test compares the JSON of the two answers).
func TestProvimuxSessionRouteRouteFields(t *testing.T) {
	auth := &Auth{ID: "sr-route-fields", Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "3", "api_key": "sk-x", "base_url": "https://api.example.test:8443/v1?q=1"}}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "sr-route-fields-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if raw := mustJSON(t, provimuxRouteOf(auth, "sr-route-fields-model")); raw != `{"provider":"claude","kind":"apikey","priority":3,"host":"api.example.test"}` {
		t.Fatalf("route: %s", raw)
	}
	// An account the list does not show for the name (it does not serve it, or it is disabled): nothing of it.
	if raw := mustJSON(t, provimuxRouteOf(auth, "another-model")); raw != `{"listed":false}` {
		t.Fatalf("unlisted route: %s", raw)
	}
	auth.Disabled = true
	if raw := mustJSON(t, provimuxRouteOf(auth, "sr-route-fields-model")); raw != `{"listed":false}` {
		t.Fatalf("disabled route: %s", raw)
	}
	var back ProvimuxRoute
	if err := json.Unmarshal([]byte(`{"listed":false}`), &back); err != nil || !back.Unlisted {
		t.Fatalf("read back: %+v %v", back, err)
	}
}

type provimuxSRScheduler struct{}

func (provimuxSRScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

type provimuxSRHook func(Result)

func (provimuxSRHook) OnAuthRegistered(context.Context, *Auth)  {}
func (provimuxSRHook) OnAuthUpdated(context.Context, *Auth)     {}
func (h provimuxSRHook) OnResult(_ context.Context, res Result) { h(res) }

func mustAuth(t *testing.T, m *Manager, id string) *Auth {
	t.Helper()
	auth, ok := m.GetByID(id)
	if !ok {
		t.Fatalf("no auth %q", id)
	}
	return auth
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
