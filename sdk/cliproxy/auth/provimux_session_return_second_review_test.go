package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the second review of the rule (findings 1, 2, 4 and 7), each as the reviewer's own case, against the rule
// as it now reads: it never moves a conversation that had a message routed to an account within cold-after. An answer that itself takes
// longer than cold-after does not count as use while it streams. Every account and key is invented; the executors
// are stand-ins in this process.

// provimuxRTStream answers a streamed request with the chunks the test sends.
type provimuxRTStream struct {
	provimuxSRExecutor
	chunks chan cliproxyexecutor.StreamChunk
}

func (e provimuxRTStream) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

// TestProvimuxSessionReturnFinding1TerminalEvent is finding 1: a streamed answer of 90 seconds whose client leaves
// right after the terminal event, so upstream never refreshes the binding from the result. With cold-after 3m the
// follow-up stays; with cold-after 1m it goes back, as intended: the message was sent 90 seconds before.
func TestProvimuxSessionReturnFinding1TerminalEvent(t *testing.T) {
	for _, row := range []struct {
		cold     time.Duration
		returned bool
	}{{cold: 3 * time.Minute}, {cold: time.Minute, returned: true}} {
		t.Run(row.cold.String(), func(t *testing.T) {
			f := newProvimuxSRFixture(t, "fill-first", false, nil)
			f.rule(row.cold)
			header := http.Header{"X-Session-Affinity": {"terminal"}}
			key := f.fellBack(header, "")
			chunks := make(chan cliproxyexecutor.StreamChunk, 2)
			f.m.RegisterExecutor(provimuxRTStream{provimuxSRExecutor{provider: f.providers[0]}, chunks})
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := []byte(`{"model":"` + provimuxSRModel + `","messages":[{"role":"user","content":"hi"}]}`)
			stream, err := f.m.ExecuteStream(ctx, f.providers, cliproxyexecutor.Request{Model: provimuxSRModel, Payload: body}, cliproxyexecutor.Options{Headers: header, OriginalRequest: body})
			if err != nil {
				t.Fatal(err)
			}
			<-stream.Chunks
			f.age(90 * time.Second) // the answer takes 90 seconds
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"status":"completed"}}`)}
			<-stream.Chunks
			cancel()
			close(chunks)
			for range stream.Chunks {
			}
			before := provimuxReturned.Load()
			if err := f.turn(header, "", "", nil); err != nil {
				t.Fatal(err)
			}
			got, _ := f.binding(key)
			returned := provimuxReturned.Load() - before
			if row.returned && (got != f.high || returned != 1) || !row.returned && (got != f.low || returned != 0) {
				t.Fatalf("cold-after %s: the follow-up is bound %q, counted %d; want returned %v", row.cold, got, returned, row.returned)
			}
		})
	}
}

// TestProvimuxSessionReturnFinding2TokenRotated is finding 2: an OAuth access token rotated during a 90-second answer,
// so upstream records the result under an older credential version and does not refresh the binding. With cold-after
// 3m the follow-up stays; with 1m it goes back, as intended.
func TestProvimuxSessionReturnFinding2TokenRotated(t *testing.T) {
	for _, row := range []struct {
		cold     time.Duration
		returned bool
	}{{cold: 3 * time.Minute}, {cold: time.Minute, returned: true}} {
		t.Run(row.cold.String(), func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			f := newProvimuxSRFixture(t, "fill-first", false, func(_ context.Context, _ *Auth, req cliproxyexecutor.Request) error {
				if strings.Contains(string(req.Payload), "hold-long-answer") {
					close(entered)
					<-finish
				}
				return nil
			})
			low, _ := f.m.GetByID(f.low)
			delete(low.Attributes, "api_key")
			low.Metadata = map[string]any{"access_token": "fixture-before", "refresh_token": "fixture-unchanged"}
			if _, err := f.m.Update(WithSkipPersist(t.Context()), low); err != nil {
				t.Fatal(err)
			}
			f.rule(row.cold)
			header := http.Header{"X-Session-Affinity": {"refresh"}}
			key := f.fellBack(header, "")
			done := make(chan error, 1)
			go func() { done <- f.turn(header, `,"note":"hold-long-answer"`, "", nil) }()
			<-entered
			f.age(90 * time.Second)
			base, _ := f.m.GetByID(f.low)
			updated := base.Clone()
			updated.Metadata["access_token"] = "fixture-after"
			next, err := f.m.UpdateRefreshedAuth(WithSkipPersist(t.Context()), base, updated)
			close(finish)
			if errAnswer := <-done; err != nil || next == nil || next.CredentialVersion <= base.CredentialVersion || errAnswer != nil {
				t.Fatalf("setup: %v %+v %v", err, next, errAnswer)
			}
			before := provimuxReturned.Load()
			if err := f.turn(header, "", "", nil); err != nil {
				t.Fatal(err)
			}
			got, _ := f.binding(key)
			returned := provimuxReturned.Load() - before
			if row.returned && (got != f.high || returned != 1) || !row.returned && (got != f.low || returned != 0) {
				t.Fatalf("cold-after %s: the follow-up is bound %q, counted %d; want returned %v", row.cold, got, returned, row.returned)
			}
		})
	}
}

// TestProvimuxSessionReturnFinding4RetryHistory is finding 4: the rule returned the conversation, the high account
// failed and the retry came back to the same low login; while that answer runs, the show gives no token and no
// history of the earlier stretch, and the old token gets 409.
func TestProvimuxSessionReturnFinding4RetryHistory(t *testing.T) {
	scope := provimuxSRScope("sk-client-one")
	header := http.Header{"X-Session-Affinity": {"retry"}}
	entered, finish := make(chan struct{}), make(chan struct{})
	fail := false
	f := newProvimuxSRFixture(t, "fill-first", false, func(_ context.Context, a *Auth, req cliproxyexecutor.Request) error {
		if fail && strings.HasPrefix(a.ID, "sr-high-") {
			return &Error{HTTPStatus: http.StatusTooManyRequests, Message: "fixture retry"}
		}
		if strings.Contains(string(req.Payload), "hold-retry") {
			close(entered)
			<-finish
		}
		return nil
	})
	f.rule(time.Minute)
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	f.coolHigh(time.Minute)
	if err := f.turn(header, "", scope, nil); err != nil {
		t.Fatal(err)
	}
	expireSessionAffinityPriorityModelCooldown(t, f.m, f.high, provimuxSRModel)
	old := f.book.Show(scope, header, nil, provimuxSRModel)
	if old.Binding == nil || old.Bound.Origin != "fallback" {
		t.Fatalf("old %s", mustJSON(t, old))
	}
	f.age(90 * time.Second)
	fail = true
	done := make(chan error, 1)
	go func() { done <- f.turn(header, `,"note":"hold-retry"`, scope, nil) }()
	<-entered
	current := f.book.Show(scope, header, nil, provimuxSRModel)
	_, conflict := f.book.Release(scope, header, nil, provimuxSRModel, *old.Binding)
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if current.Binding != nil || conflict == nil || current.Bound == nil || current.Bound.Since != nil || current.Bound.Origin != "not-known" {
		t.Fatalf("while the retry answers: %s, old token conflict %v; want no token, no history, 409", mustJSON(t, current), conflict != nil)
	}
}

// TestProvimuxSessionReturnFinding7NewObservation is finding 7: the rule switched off and on again does not judge a
// conversation's first message after that by a note from before; and a commit that switches it off while a selection
// is already past its first check is seen before anything is dropped.
func TestProvimuxSessionReturnFinding7NewObservation(t *testing.T) {
	t.Run("off, then on", func(t *testing.T) {
		f := newProvimuxSRFixture(t, "fill-first", false, nil)
		f.rule(time.Minute)
		header := http.Header{"X-Session-Affinity": {"toggle"}}
		key := f.fellBack(header, "")
		ProvimuxSetSessionReturn(f.m, ProvimuxSessionReturnSetting{Affinity: true, TTL: time.Hour})
		f.age(90 * time.Second)
		f.rule(time.Minute)
		before := provimuxReturned.Load()
		if err := f.turn(header, "", "", nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.binding(key); got != f.low || provimuxReturned.Load() != before {
			t.Fatalf("first request after switching on: bound %q; want it left alone", got)
		}
		f.age(90 * time.Second)
		if err := f.turn(header, "", "", nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.binding(key); got != f.high || provimuxReturned.Load()-before != 1 {
			t.Fatalf("second request, 90 s after the first: bound %q; want returned", got)
		}
	})
	t.Run("off while a selection runs", func(t *testing.T) {
		f := newProvimuxSRFixture(t, "fill-first", false, nil)
		f.rule(time.Minute)
		key := f.fellBack(http.Header{"X-Session-Affinity": {"disable"}}, "")
		f.age(90 * time.Second)
		p := provimuxRTPluginCheck{entered: make(chan struct{}), finish: make(chan struct{})}
		f.m.SetPluginScheduler(p)
		before := provimuxReturned.Load()
		done := make(chan struct{})
		go func() { defer close(done); f.affinity.provimuxReturn(true, key, "", f.auths(), time.Now()) }()
		<-p.entered
		ProvimuxSetSessionReturn(f.m, ProvimuxSessionReturnSetting{Affinity: true, TTL: time.Hour})
		close(p.finish)
		<-done
		f.m.SetPluginScheduler(nil)
		if _, present := f.binding(key); !present || provimuxReturned.Load() != before {
			t.Fatal("dropped after the setting was removed")
		}
	})
}

// provimuxRTPluginCheck holds the rule's runtime check until the test lets it go (it reports no plugin scheduler).
type provimuxRTPluginCheck struct {
	provimuxSRScheduler
	entered, finish chan struct{}
}

func (p provimuxRTPluginCheck) HasScheduler() bool { close(p.entered); <-p.finish; return false }

// TestProvimuxSessionReturnFinding8RefusedMessage is finding 8 of the third check, stated as intended: a message the
// gateway refuses before choosing an account (every account that serves the name is cooling, so it answers 429
// before the selector runs) reaches no provider and reads no cache, so it is not use. The retry after the cooldowns
// end, more than cold-after after the last message routed to an account, goes back.
func TestProvimuxSessionReturnFinding8RefusedMessage(t *testing.T) {
	calls := &provimuxRTCalls{}
	f := newProvimuxSRFixture(t, "fill-first", false, calls.call)
	f.rule(time.Minute)
	header := http.Header{"X-Session-Affinity": {"retry-after-cooldown"}}
	key := f.fellBack(header, "")
	calls.take()
	f.age(90 * time.Second) // the last message routed to an account was 90 seconds ago
	before, routed := provimuxReturned.Load(), f.noteOf(key)
	retry := time.Minute
	for _, id := range []string{f.low, f.high} {
		f.m.MarkResult(t.Context(), Result{AuthID: id, Provider: f.providers[0], Model: provimuxSRModel,
			Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "synthetic quota"}, RetryAfter: &retry})
	}
	if err := f.turn(header, "", "", nil); err == nil || len(calls.take()) != 0 || !f.noteOf(key).Equal(routed) {
		t.Fatalf("want a refusal before selection, no call, and no note: %v", err)
	}
	for _, id := range []string{f.low, f.high} {
		expireSessionAffinityPriorityModelCooldown(t, f.m, id, provimuxSRModel)
	}
	if err := f.turn(header, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.binding(key); got != f.high || provimuxReturned.Load()-before != 1 {
		t.Fatalf("the retry is bound %q, counted %d; want it returned (the refused message was not use)", got, provimuxReturned.Load()-before)
	}
}
