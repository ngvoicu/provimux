package provimux

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: routing.retry.same-account-retry, on the real executors, manager and Claude handler in front of stand-in
// vendors (transient_retry_harness_test.go). The wait is the test's: waits are recorded and return at once.

// waitClock records each wait the rule asks for and returns at once; jitter, when set, is the jitter used.
type waitClock struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (c *waitClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

func installClock(t *testing.T, jitter func() time.Duration) *waitClock {
	t.Helper()
	clock := &waitClock{}
	restore := coreauth.ProvimuxSetRetryClockForTest(func(ctx context.Context, wait time.Duration) error {
		clock.mu.Lock()
		clock.waits = append(clock.waits, wait)
		clock.mu.Unlock()
		return ctx.Err()
	}, jitter)
	t.Cleanup(restore)
	return clock
}

func streamName(stream bool) string {
	if stream {
		return "stream"
	}
	return "nonstream"
}

func countsDelta(before, after coreauth.ProvimuxSameAccountRetryCounts) [4]uint64 {
	return [4]uint64{after.SecondCalls - before.SecondCalls, after.SecondCallsWithoutError - before.SecondCallsWithoutError,
		after.SecondCallsWithError - before.SecondCallsWithError, after.LeftDuringWait - before.LeftDuringWait}
}

// loopbackVendors lets a loopback stand-in count as the vendor's own API host for the test (the allow-list applies only
// to Anthropic's and OpenAI's own hosts).
func loopbackVendors(t *testing.T) {
	t.Helper()
	t.Cleanup(coreauth.ProvimuxSetVendorHostsForTest())
}

// The owner's report with the rule on: the login's 502 overloaded_error gets one second try, the login answers, and
// the conversation stays on it; no other account is called, the login carries no cooldown, and the counts say one
// second try sent and answered. With and without session affinity, stream and non-stream.
func TestTransientRetryReportRuleOn(t *testing.T) {
	for _, affinity := range []bool{true, false} {
		for _, stream := range []bool{true, false} {
			t.Run(streamName(stream)+map[bool]string{true: "/affinity", false: "/no-affinity"}[affinity], func(t *testing.T) {
				clock := installClock(t, nil)
				p := newPool(t, poolOptions{affinity: affinity, rebindOn: "any", rule: ruleOn})
				if code, body := p.send("conversation", stream); code != 200 || answeredBy(body) != "login" {
					t.Fatalf("first turn: %d answered by %q", code, answeredBy(body))
				}
				scriptReport(p)
				statsBefore := coreauth.ProvimuxSameAccountRetryStats()
				before := p.calls()
				code, body := p.send("conversation", stream)
				after := p.calls()
				if code != 200 || answeredBy(body) != "login" {
					t.Fatalf("second turn: %d answered by %q, want 200 from the login; body %s", code, answeredBy(body), body)
				}
				want := map[string]int{"login": 2, "devin": 0, "key1": 0, "key2": 0, "openrouter": 0}
				for name, n := range want {
					if got := after[name] - before[name]; got != n {
						t.Errorf("%s called %d times, want %d", name, got, n)
					}
				}
				if cd := p.cooldown("login"); cd != 0 {
					t.Errorf("login cooldown %v after an answered second try, want none", cd)
				}
				if waits := clock.recorded(); len(waits) != 1 || waits[0] < 500*time.Millisecond || waits[0] > time.Second {
					t.Errorf("waits %v, want one between 500 ms and 1 s", waits)
				}
				if got := countsDelta(statsBefore, coreauth.ProvimuxSameAccountRetryStats()); got != [4]uint64{1, 1, 0, 0} {
					t.Errorf("counts (calls, without error, with error, left) %v, want [1 1 0 0]", got)
				}
				for i := 0; i < 3; i++ {
					if code, body := p.send("conversation", stream); code != 200 || answeredBy(body) != "login" {
						t.Fatalf("turn %d: %d answered by %q, want the login", i+3, code, answeredBy(body))
					}
				}
			})
		}
	}
}

// The rule off, set false or not set (a test binary's default), is today's behaviour: the same as
// TestTransientRetryReportToday, and the clock never asked.
func TestTransientRetryReportRuleOff(t *testing.T) {
	for _, rule := range []*bool{ruleOff, nil} {
		for _, stream := range []bool{true, false} {
			t.Run(streamName(stream), func(t *testing.T) {
				clock := installClock(t, nil)
				p := newPool(t, poolOptions{affinity: true, rebindOn: "any", rule: rule})
				p.send("conversation", stream)
				scriptReport(p)
				before := p.calls()
				code, body := p.send("conversation", stream)
				after := p.calls()
				if code != 200 || answeredBy(body) != "openrouter" {
					t.Fatalf("%d answered by %q, want openrouter", code, answeredBy(body))
				}
				for _, name := range p.order {
					if got := after[name] - before[name]; got != 1 {
						t.Errorf("%s called %d times, want 1", name, got)
					}
				}
				if code, body := p.send("conversation", stream); code != 200 || answeredBy(body) != "openrouter" {
					t.Fatalf("next turn %d answered by %q, want openrouter", code, answeredBy(body))
				}
				if waits := clock.recorded(); len(waits) != 0 {
					t.Errorf("the rule off waited %v", waits)
				}
			})
		}
	}
}

// Each answer of the table, once on the Claude login, the other accounts healthy. A refusal: the login twice, it
// answers, no cooldown. Anything else: as today (one call, the next account answers, upstream's cooldown).
func TestTransientRetryFaultTable(t *testing.T) {
	cases := []struct {
		name    string
		f       fault
		refusal bool
		client  int // the client's status when the request stops at the login
	}{
		{name: "529 overloaded_error", f: fault{status: 529, body: bodyOverloaded}, refusal: true},
		{name: "502 overloaded_error (the report)", f: fault{status: 502, body: bodyOverloaded}, refusal: true},
		{name: "503 overloaded_error", f: fault{status: 503, body: bodyOverloaded}, refusal: true},
		{name: "500 overloaded_error: not a status the page names", f: fault{status: 500, body: bodyOverloaded}},
		{name: "529 overloaded_error, x-should-retry false", f: fault{status: 529, body: bodyOverloaded, headers: map[string]string{"X-Should-Retry": "false"}}},
		{name: "529 overloaded_error, x-should-retry true", f: fault{status: 529, body: bodyOverloaded, headers: map[string]string{"X-Should-Retry": "true"}}, refusal: true},
		{name: "529 overloaded_error, insufficient capacity", f: fault{status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Insufficient server capacity. Try again shortly."}}`}, refusal: true},
		{name: "529 overloaded_error, Retry-After 2", f: fault{status: 529, body: bodyOverloaded, headers: map[string]string{"Retry-After": "2"}}, refusal: true},

		{name: "529 overloaded_error, Retry-After 3", f: fault{status: 529, body: bodyOverloaded, headers: map[string]string{"Retry-After": "3"}}},
		{name: "529 overloaded_error, Retry-After unreadable", f: fault{status: 529, body: bodyOverloaded, headers: map[string]string{"Retry-After": "soon"}}},
		{name: "429 overloaded_error", f: fault{status: 429, body: bodyOverloaded}},
		{name: "529 api_error saying overloaded", f: fault{status: 529, body: `{"type":"error","error":{"type":"api_error","message":"Overloaded"}}`}},
		{name: "502 plain", f: fault{status: 502, body: bodyGatewayPlain}},
		{name: "503 api_error", f: fault{status: 503, body: bodyUnavailable}},
		{name: "504 plain", f: fault{status: 504, body: bodyGatewayTimout}},
		{name: "500 api_error", f: fault{status: 500, body: bodyAPIError}},
		{name: "503 quota code", f: fault{status: 503, body: `{"type":"error","error":{"type":"billing_error","message":"overloaded"}}`}},
		{name: "connection reset after the request was read", f: fault{kind: "reset"}},
		{name: "closed after the request was read", f: fault{kind: "eof"}},
		{name: "429 rate limit", f: fault{status: 429, body: bodyRateLimit}},
		{name: "429 weekly quota", f: fault{status: 429, body: bodyWeeklyQuota}},
		{name: "400", f: fault{status: 400, body: bodyBadRequest}, client: 400},
		{name: "400 words only", f: fault{status: 400, body: `{"error":{"message":"unknown model overloaded-model"}}`}, client: 400},
		{name: "400 credit", f: fault{status: 400, body: bodyCreditTooLow}},
		{name: "401", f: fault{status: 401, body: bodyUnauthorized}},
		{name: "402", f: fault{status: 402, body: bodyPayment}},
		{name: "403", f: fault{status: 403, body: bodyForbidden}},
		{name: "404", f: fault{status: 404, body: bodyNotFound}},
	}
	for _, stream := range []bool{true, false} {
		for _, c := range cases {
			t.Run(streamName(stream)+"/"+c.name, func(t *testing.T) {
				clock := installClock(t, nil)
				p := newPool(t, poolOptions{affinity: true, rebindOn: "any", rule: ruleOn})
				p.send("conversation", stream)
				p.vendors["login"].setScript(c.f)
				before := p.calls()
				code, body := p.send("conversation", stream)
				after := p.calls()
				login, devin := after["login"]-before["login"], after["devin"]-before["devin"]
				switch {
				case c.refusal:
					if code != 200 || answeredBy(body) != "login" || login != 2 || devin != 0 {
						t.Fatalf("%d answered by %q, login %d devin %d calls; want the login twice, answering", code, answeredBy(body), login, devin)
					}
					if cd := p.cooldown("login"); cd != 0 {
						t.Errorf("cooldown %v, want none", cd)
					}
					if len(clock.recorded()) != 1 {
						t.Errorf("waits %v, want one", clock.recorded())
					}
				case c.client != 0:
					if code != c.client || login != 1 || devin != 0 {
						t.Fatalf("%d, login %d devin %d; want %d to the client after one call", code, login, devin, c.client)
					}
				default:
					if code != 200 || answeredBy(body) != "devin" || login != 1 || devin != 1 {
						t.Fatalf("%d answered by %q, login %d devin %d; want today's: one login call, devin answers", code, answeredBy(body), login, devin)
					}
					if len(clock.recorded()) != 0 {
						t.Errorf("waited %v for an answer that is not a refusal", clock.recorded())
					}
				}
			})
		}
	}
}

// The wait: no Retry-After or 0 is 750 ms ± 250 ms; 1 and 2 are waited exactly; the longest is 2 s.
func TestTransientRetryWaitBounds(t *testing.T) {
	cases := []struct {
		name       string
		jitter     time.Duration
		retryAfter string
		want       time.Duration
	}{
		{"shortest", -250 * time.Millisecond, "", 500 * time.Millisecond},
		{"longest", 250 * time.Millisecond, "", time.Second},
		{"Retry-After 0 is the jittered wait", 100 * time.Millisecond, "0", 850 * time.Millisecond},
		{"Retry-After 1", 250 * time.Millisecond, "1", time.Second},
		{"Retry-After 1 above the jittered wait", -250 * time.Millisecond, "1", time.Second},
		{"Retry-After 2: the most a client waits", -250 * time.Millisecond, "2", 2 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			jitter := c.jitter
			clock := installClock(t, func() time.Duration { return jitter })
			p := newPool(t, poolOptions{rule: ruleOn})
			f := fault{status: 529, body: bodyOverloaded}
			if c.retryAfter != "" {
				f.headers = map[string]string{"Retry-After": c.retryAfter}
			}
			p.vendors["login"].setScript(f)
			if code, body := p.send("", true); code != 200 || answeredBy(body) != "login" {
				t.Fatalf("%d answered by %q", code, answeredBy(body))
			}
			if waits := clock.recorded(); len(waits) != 1 || waits[0] != c.want {
				t.Fatalf("waits %v, want [%v]", waits, c.want)
			}
		})
	}
}

// A fault after the first answer byte is never retried: the client has the beginning, the error follows, the login
// was called once.
func TestTransientRetryNotAfterFirstByte(t *testing.T) {
	clock := installClock(t, nil)
	p := newPool(t, poolOptions{affinity: true, rule: ruleOn})
	p.vendors["login"].setScript(fault{kind: "midstream", body: bodyOverloaded})
	code, body := p.send("conversation", true)
	if code != 200 || !strings.Contains(body, "partial from login") || p.vendors["login"].callCount() != 1 || p.vendors["devin"].callCount() != 0 {
		t.Fatalf("%d, login %d devin %d calls; body %s", code, p.vendors["login"].callCount(), p.vendors["devin"].callCount(), body)
	}
	if len(clock.recorded()) != 0 {
		t.Fatalf("waited %v", clock.recorded())
	}
}

// A second refusal goes on as today: the next account answers, the login carries upstream's transient cooldown (60 s)
// and the binding moves; the counts say one second try sent, failed.
func TestTransientRetrySecondRefusal(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(streamName(stream), func(t *testing.T) {
			installClock(t, nil)
			p := newPool(t, poolOptions{affinity: true, rebindOn: "any", rule: ruleOn})
			p.send("conversation", stream)
			p.vendors["login"].setScript(fault{status: 502, body: bodyOverloaded}, fault{status: 529, body: bodyOverloaded})
			before := coreauth.ProvimuxSameAccountRetryStats()
			code, body := p.send("conversation", stream)
			if code != 200 || answeredBy(body) != "devin" {
				t.Fatalf("%d answered by %q, want devin", code, answeredBy(body))
			}
			if got := p.vendors["login"].callCount(); got != 3 {
				t.Fatalf("login called %d times (1 warm-up + 2), want 3", got)
			}
			if cd := p.cooldown("login"); cd < 50*time.Second || cd > 61*time.Second {
				t.Errorf("login cooldown %v, want upstream's 60 s", cd)
			}
			if got := countsDelta(before, coreauth.ProvimuxSameAccountRetryStats()); got != [4]uint64{1, 0, 1, 0} {
				t.Errorf("counts (calls, without error, with error, left) %v, want [1 0 1 0]", got)
			}
			if code, body := p.send("conversation", stream); code != 200 || answeredBy(body) != "devin" {
				t.Fatalf("next turn %d answered by %q, want devin (the binding moved, as today)", code, answeredBy(body))
			}
		})
	}
}

// Two client requests at once on one account, each refused as overloaded: each is its own client call and gets its
// own second try on that account, and both get the login's answer.
func TestTransientRetryTwoAtOnce(t *testing.T) {
	clock := installClock(t, nil)
	p := newPool(t, poolOptions{rule: ruleOn})
	login := p.vendors["login"]
	login.setScript(fault{status: 529, body: bodyOverloaded}, fault{status: 529, body: bodyOverloaded})
	gate := make(chan struct{})
	login.setGate(gate)
	var wg sync.WaitGroup
	answers := make([]string, 2)
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body := p.send("", i == 0)
			answers[i] = answeredBy(body)
		}()
	}
	deadline := time.Now().Add(30 * time.Second)
	for login.callCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the two first tries never both reached the login")
		}
		time.Sleep(time.Millisecond)
	}
	close(gate) // both first tries are in flight: both get the refusal
	wg.Wait()
	if answers[0] != "login" || answers[1] != "login" || login.callCount() != 4 || p.vendors["devin"].callCount() != 0 || len(clock.recorded()) != 2 {
		t.Fatalf("answers %v, login %d devin %d calls, waits %v; want both from the login after 4 calls, 2 waits", answers, login.callCount(), p.vendors["devin"].callCount(), clock.recorded())
	}
}

// A caller who meets no fault gets the same bytes with the rule on as off (main), on every path, and the vendors
// receive the same requests.
func TestTransientRetryNoFaultSameBytes(t *testing.T) {
	answeredBySwitch(t, true)
	type outcome struct {
		answers  []string
		received map[string][]string
	}
	run := func(t *testing.T, rule *bool, affinity, stream bool) outcome {
		installClock(t, nil)
		p := newPool(t, poolOptions{affinity: affinity, rebindOn: "any", rule: rule, trace: true})
		// A stand-in's port is part of a key's marker: each marker is written as its account's name.
		var markers []string
		for _, name := range p.order {
			auth, _ := p.manager.GetByID(p.authID(name))
			markers = append(markers, auth.ProvimuxAccountMarker(), "<marker of "+name+">")
		}
		named := strings.NewReplacer(markers...)
		var out outcome
		for _, session := range []string{"conversation", "conversation", ""} {
			rec := p.sendRecorded(session, stream)
			out.answers = append(out.answers, named.Replace(dumpAnswer(rec.Code, rec.Header(), rec.Body.String())))
		}
		// A fault that is not a refusal takes the same path on and off too.
		p.vendors["login"].setScript(fault{status: 429, body: bodyRateLimit})
		rec := p.sendRecorded("conversation", stream)
		out.answers = append(out.answers, named.Replace(dumpAnswer(rec.Code, rec.Header(), rec.Body.String())))
		out.received = map[string][]string{}
		for name, vendor := range p.vendors {
			out.received[name] = vendor.requests()
		}
		return out
	}
	for _, affinity := range []bool{true, false} {
		for _, stream := range []bool{true, false} {
			t.Run(streamName(stream), func(t *testing.T) {
				off := run(t, ruleOff, affinity, stream)
				on := run(t, ruleOn, affinity, stream)
				for i := range off.answers {
					if off.answers[i] != on.answers[i] {
						t.Errorf("answer %d differs:\noff: %s\non:  %s", i, off.answers[i], on.answers[i])
					}
				}
				for name := range off.received {
					if strings.Join(off.received[name], "\n--\n") != strings.Join(on.received[name], "\n--\n") {
						t.Errorf("%s received other requests:\noff: %q\non:  %q", name, off.received[name], on.received[name])
					}
				}
			})
		}
	}
}

// dumpAnswer writes an answer out with every header but the ones that hold the test's own names of the accounts.
var testNameInAnswer = regexp.MustCompile(`TestTransientRetry[A-Za-z0-9_/-]*`)

func dumpAnswer(code int, header map[string][]string, body string) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(code))
	for _, name := range sortedKeys(header) {
		b.WriteString("\n" + name + ": " + strings.Join(header[name], ","))
	}
	b.WriteString("\n\n" + body)
	return testNameInAnswer.ReplaceAllString(b.String(), "<test>")
}

func sortedKeys(header map[string][]string) []string {
	keys := make([]string, 0, len(header))
	for name := range header {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

// The account marker of an answer after a second try is the account's (X-Provimux-Account), as without one.
func TestTransientRetryAccountMarker(t *testing.T) {
	answeredBySwitch(t, true)
	installClock(t, nil)
	p := newPool(t, poolOptions{rule: ruleOn, trace: true})
	auth, ok := p.manager.GetByID(p.authID("login"))
	if !ok {
		t.Fatal("no login")
	}
	marker := auth.ProvimuxAccountMarker()
	if !answeredMarkerPattern.MatchString(marker) {
		t.Fatalf("fixture: marker %q", marker)
	}
	for _, stream := range []bool{true, false} {
		plain := p.sendRecorded("", stream)
		p.vendors["login"].setScript(fault{status: 502, body: bodyOverloaded})
		retried := p.sendRecorded("", stream)
		if answeredBy(retried.Body.String()) != "login" || p.vendors["devin"].callCount() != 0 {
			t.Fatalf("%s: answered by %q", streamName(stream), answeredBy(retried.Body.String()))
		}
		if got := plain.Header().Get(logging.ProvimuxAccountHeader); got != marker {
			t.Errorf("%s without a second try: X-Provimux-Account %q, want %q", streamName(stream), got, marker)
		}
		if got := retried.Header().Get(logging.ProvimuxAccountHeader); got != marker {
			t.Errorf("%s after a second try: X-Provimux-Account %q, want %q", streamName(stream), got, marker)
		}
	}
}
