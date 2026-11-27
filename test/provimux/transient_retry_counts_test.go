package provimux

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Finding 6: the counts say what happened. A second try is counted when it is sent, not before the wait; a client that
// leaves during the wait is counted as left, with no second try sent; "answered" is a complete answer, or a stream that
// began (a stream that began and then failed is answered: completion is not what this counts); "failed" is a second
// try that failed before any answer.
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
				t.Fatalf("Retry-After %q: counts (sent, answered, failed, left) %v, login %d requests (warm-up + refusal); want [0 0 0 1] and 2", retryAfter, got, p.vendors["login"].callCount())
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
			t.Fatalf("counts %v, want [1 1 0 0] (sent, and answered: the stream began); body %s", got, body)
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
