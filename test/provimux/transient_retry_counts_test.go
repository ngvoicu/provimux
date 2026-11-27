package provimux

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The counts say what the gateway did: second_calls when the executor is called a second time (after the wait); a client
// that leaves during the wait is counted as left, with no second call; "without error" when that call returned without
// an error (a stream that began and then failed is one: completion is not what this counts); "with error" when it
// returned an error.
func TestTransientRetryFinding6Counts(t *testing.T) {
	t.Run("left during the wait", func(t *testing.T) {
		for _, retryAfter := range []string{"", "1"} {
			p := newPool(t, poolOptions{affinity: true, rebindOn: "any", rule: ruleOn})
			p.send("conversation", false)
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+testModel+`","max_tokens":30,"messages":[{"role":"user","content":"hello"}]}`))
			ctx, cancel := context.WithCancel(req.Context())
			req = req.WithContext(ctx)
			req.Header.Set("X-Session-ID", "conversation")
			f := fault{status: 529, body: bodyOverloaded}
			if retryAfter != "" {
				f.headers = map[string]string{"Retry-After": retryAfter}
			}
			p.vendors["login"].setScript(f)
			restore := coreauth.ProvimuxSetRetryClockForTest(func(c context.Context, _ time.Duration) error {
				cancel() // the client leaves during the wait
				<-c.Done()
				return c.Err()
			}, nil)
			before := coreauth.ProvimuxSameAccountRetryStats()
			p.router.ServeHTTP(httptest.NewRecorder(), req)
			got := countsDelta(before, coreauth.ProvimuxSameAccountRetryStats())
			restore()
			if got != [4]uint64{0, 0, 0, 1} || p.vendors["login"].callCount() != 2 {
				t.Fatalf("Retry-After %q: counts (calls, without error, with error, left) %v, login %d requests (warm-up + refusal); want [0 0 0 1] and 2", retryAfter, got, p.vendors["login"].callCount())
			}
		}
	})
	t.Run("a second stream that began, then failed", func(t *testing.T) {
		installClock(t, nil)
		p := newPool(t, poolOptions{rule: ruleOn})
		p.vendors["login"].setScript(fault{status: 529, body: bodyOverloaded}, fault{kind: "midstream", body: bodyOverloaded})
		before := coreauth.ProvimuxSameAccountRetryStats()
		_, body := p.send("", true)
		if got := countsDelta(before, coreauth.ProvimuxSameAccountRetryStats()); got != [4]uint64{1, 1, 0, 0} || !strings.Contains(body, "partial from login") {
			t.Fatalf("counts %v, want [1 1 0 0] (called, and returned without an error: the stream was handed on); body %s", got, body)
		}
	})
	t.Run("a second try refused again", func(t *testing.T) {
		installClock(t, nil)
		p := newPool(t, poolOptions{rule: ruleOn})
		p.vendors["login"].setScript(fault{status: 529, body: bodyOverloaded}, fault{status: 529, body: bodyOverloaded})
		before := coreauth.ProvimuxSameAccountRetryStats()
		p.send("", false)
		if got := countsDelta(before, coreauth.ProvimuxSameAccountRetryStats()); got != [4]uint64{1, 0, 1, 0} {
			t.Fatalf("counts %v, want [1 0 1 0]", got)
		}
	})
}
