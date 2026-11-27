package provimux

// The three pieces of this release together: the second try after an overload refusal
// (routing.retry.same-account-retry), a client's show of its conversation's route (session-route) and the bindings
// kept across a restart (routing.session-affinity-persist), with the account marker of the answer (models.answered-by).
//
//   - an answer after a second try names the account that gave it (X-Provimux-Account), on the account the second try
//     went to, and on the next account when the second try is refused too;
//   - the show after such an answer names, as bound, the account that answered: the same marker as the answer's
//     header; the refused first try binds nothing and notes nothing;
//   - a stop that arrives while a second try waits writes the bindings file first, the waiting conversation included,
//     then lets the second try go and the answer finish; the next start puts the conversation back on its account.
//
// Every vendor is a stand-in on loopback; no credential is real; nothing waits on a clock but the test's own gates.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// togetherClientKey is the invented client key the requests of the in-process tests carry, as the access layer sets it.
const togetherClientKey = "sk-provimux-test-together"

// togetherPool is the transient-retry pool with session affinity, the rule on, the answered-by middleware, a client
// key on every request, and session-route's book subscribed to the manager's results.
func togetherPool(t *testing.T) (*pool, *coreauth.ProvimuxSessionBook) {
	t.Helper()
	answeredBySwitch(t, true)
	installClock(t, nil)
	p := newPool(t, poolOptions{affinity: true, rebindOn: "any", rule: ruleOn, trace: true})
	router := gin.New()
	router.Use(logging.CPATraceIDMiddleware())
	router.Use(func(c *gin.Context) { c.Set("userApiKey", togetherClientKey) })
	router.POST("/v1/messages", claudehandlers.NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, p.manager)).ClaudeMessages)
	p.router = router
	book := coreauth.NewProvimuxSessionBook(p.manager)
	book.Subscribe()
	t.Cleanup(book.Unsubscribe)
	if !book.Subscribed() {
		t.Fatal("premise: the book does not observe the pool's manager")
	}
	return p, book
}

// togetherJSON is v as JSON, for a failure message.
func togetherJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

func (p *pool) marker(name string) string {
	p.t.Helper()
	auth, ok := p.manager.GetByID(p.authID(name))
	if !ok {
		p.t.Fatalf("no auth %s", name)
	}
	marker := auth.ProvimuxAccountMarker()
	if !answeredMarkerPattern.MatchString(marker) {
		p.t.Fatalf("fixture: marker of %s %q", name, marker)
	}
	return marker
}

// A second try that the vendor answers: the answer, the binding and the show all name the account the second try went
// to. A second try refused too: all three name the next account by priority, and the show says a higher-priority
// account serves the name and when it is expected free. Stream and non-stream.
func TestTogetherSecondTryAnswerAndShow(t *testing.T) {
	for _, stream := range []bool{true, false} {
		for _, secondRefused := range []bool{false, true} {
			name := streamName(stream) + map[bool]string{false: "/second-try-answered", true: "/second-try-refused"}[secondRefused]
			t.Run(name, func(t *testing.T) {
				p, book := togetherPool(t)
				script := []fault{{status: 529, body: bodyOverloaded}}
				want := "login"
				if secondRefused {
					script = append(script, fault{status: 529, body: bodyOverloaded})
					want = "devin"
				}
				p.vendors["login"].setScript(script...)
				statsBefore := coreauth.ProvimuxSameAccountRetryStats()
				rec := p.sendRecorded("together", stream)
				if rec.Code != http.StatusOK || answeredBy(rec.Body.String()) != want {
					t.Fatalf("answer: %d answered by %q, want %s; body %s", rec.Code, answeredBy(rec.Body.String()), want, rec.Body.String())
				}
				if got := p.vendors["login"].callCount(); got != 2 {
					t.Fatalf("login was called %d times, want 2 (the refused first try and the second try)", got)
				}
				if delta := countsDelta(statsBefore, coreauth.ProvimuxSameAccountRetryStats()); delta[0] != 1 {
					t.Fatalf("second calls %v, want one", delta)
				}

				// 1. The answer names the account that gave it.
				marker := p.marker(want)
				if got := rec.Header().Get(logging.ProvimuxAccountHeader); got != marker {
					t.Fatalf("X-Provimux-Account %q, want %s's %q", got, want, marker)
				}

				// 2. The show names it as bound: the same marker, and upstream's cache holds the same account.
				show := book.Show(cliproxysession.CallerScope(togetherClientKey), http.Header{"X-Session-Id": {"together"}}, nil, p.model)
				if show.Conversation != "bound" || show.Bound == nil || show.Bound.Route.Unlisted {
					t.Fatalf("show: %s", togetherJSON(show))
				}
				if show.Bound.Route.Account != marker {
					t.Fatalf("show bound to %q, the answer came from %q: %s", show.Bound.Route.Account, marker, togetherJSON(show))
				}
				entries := book.Entries()
				if len(entries) != 1 || entries[0].Current != p.authID(want) {
					t.Fatalf("book entries %+v, want one, bound to %s", entries, want)
				}
				if show.Binding == nil || !strings.HasPrefix(*show.Binding, "b.") {
					t.Fatalf("show without a binding token: %s", togetherJSON(show))
				}
				if show.Position == nil {
					t.Fatalf("show without a position: %s", togetherJSON(show))
				}
				if !secondRefused {
					// The refusal left no pause on the login: nothing is above it, nothing is said to be.
					if show.Position.BelowFirst || p.cooldown("login") != 0 {
						t.Fatalf("after an answered second try: below_first %v, login cooldown %v: %s", show.Position.BelowFirst, p.cooldown("login"), togetherJSON(show))
					}
					if len(show.First) == 0 || show.First[0].Account != marker {
						t.Fatalf("first routes %s, want the login first", togetherJSON(show.First))
					}
					return
				}
				// Refused twice: the login cools as without the rule, the conversation went to the next account, and
				// the show says the login serves the name above it, is not free, and when it is expected free.
				if p.cooldown("login") == 0 {
					t.Fatal("premise: the login carries no cooldown after two refusals")
				}
				if !show.Position.BelowFirst || show.Position.HigherPriorityFree || show.Position.HigherPriorityFreeAt == nil {
					t.Fatalf("position after a refused second try: %s", togetherJSON(show))
				}
				for _, route := range show.First {
					if route.Account == p.marker("login") {
						t.Fatalf("the cooling login among the first routes now: %s", togetherJSON(show.First))
					}
				}
			})
		}
	}
}

// --- 3. A stop during a second try's wait -----------------------------------------------------------------------

const (
	// togetherRetryHold: in the gateway process, while the file it names exists, the wait before a second try lasts;
	// the gateway writes <file>.waiting when the wait begins.
	togetherRetryHold = "PROVIMUX_TEST_RETRY_HOLD"
	togetherModel     = affinityModel // the name the gateway's readiness check looks for
	togetherSession   = "provimux-test-retry-conversation"
)

// togetherGatewayHooks is called by the gateway process (TestProvimuxAffinityGatewayProcess): with togetherRetryHold
// set, every host counts as the vendor's own (the stand-in is on loopback) and the wait before a second try holds
// while the named file exists.
func togetherGatewayHooks() {
	hold := os.Getenv(togetherRetryHold)
	if hold == "" {
		return
	}
	coreauth.ProvimuxSetVendorHostsForTest()
	coreauth.ProvimuxSetRetryClockForTest(func(ctx context.Context, _ time.Duration) error {
		_ = os.WriteFile(hold+".waiting", nil, 0o600)
		affinityWaitWhile(hold)
		return ctx.Err()
	}, nil)
}

// togetherVendor is a stand-in for Anthropic's messages API with two keys: the first request it gets is refused with
// Anthropic's documented overload refusal, every later one answered, naming the key that asked.
type togetherVendor struct {
	mu    sync.Mutex
	calls []string // the key of each request, in order
}

func (v *togetherVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.ReadAll(r.Body)
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	v.mu.Lock()
	v.calls = append(v.calls, key)
	first := len(v.calls) == 1
	v.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if first {
		w.WriteHeader(529)
		_, _ = io.WriteString(w, bodyOverloaded)
		return
	}
	_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-fixture-upstream","content":[{"type":"text","text":"answer from `+key+`"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
}

func (v *togetherVendor) keys() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

// togetherSend sends one Claude messages request of the conversation (no session id: a probe) and returns the status
// and the body.
func togetherSend(t *testing.T, port int, session, text string) (int, string) {
	t.Helper()
	body := `{"model":"` + togetherModel + `","max_tokens":64,"messages":[{"role":"user","content":"` + text + `"}]}`
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+affinityClient1)
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Session-Affinity", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(answer)
}

func TestTogetherStopDuringSecondTryWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the gateway with SIGTERM")
	}
	if os.Getenv(affinityGatewayEnv) != "" {
		t.Skip("inside the gateway process")
	}
	vendor := &togetherVendor{}
	upstream := httptest.NewServer(vendor)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	g := &affinityGateway{t: t, dir: dir, config: filepath.Join(dir, "config.yaml"), port: freePort(t)}
	yaml := fmt.Sprintf(`config-version: 8
models:
  live-lists: false
  published-lists: false
server:
  host: "127.0.0.1"
  port: %d
  restart-drain-seconds: 20
access:
  api-keys: [%q]
routing:
  strategy: "round-robin"
  session-affinity: true
  retry:
    request-retry: 0
    same-account-retry: true
oauth:
  auth-dir: %q
observability:
  logs:
    debug: true
api-keys:
  claude:
    - name: "provimux-test-claude"
      base-url: %q
      fixed-models: true
      keys:
        - api-key: "provimux-test-claude-a"
        - api-key: "provimux-test-claude-b"
      models:
        - name: "claude-fixture-upstream"
          alias: %q
`, g.port, affinityClient1, authDir, upstream.URL, togetherModel)
	if err := os.WriteFile(g.config, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.stop)
	defer g.fail()

	hold := filepath.Join(dir, "retry-hold")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			_ = os.Remove(hold)
		}
	}()
	g.startEvery("1h", togetherRetryHold+"="+hold) // no periodic write: the file appears at the stop only

	type answer struct {
		status int
		body   string
	}
	answered := make(chan answer, 1)
	go func() {
		status, body := togetherSend(t, g.port, togetherSession, "a long answer, please")
		answered <- answer{status, body}
	}()
	// The first try was refused; the second waits.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(hold + ".waiting"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no second try waits:\n%s", g.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	calls := vendor.keys()
	if len(calls) != 1 {
		t.Fatalf("premise: the vendor got %d requests before the wait, want the refused one", len(calls))
	}
	bound := calls[0]
	if _, err := os.Stat(g.snapshot()); err == nil {
		t.Fatal("premise: a bindings file before the stop")
	}

	_ = g.cmd.Process.Signal(syscall.SIGTERM)
	deadline = time.Now().Add(60 * time.Second)
	for {
		if data, err := os.ReadFile(g.snapshot()); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no bindings file written at the stop:\n%s", g.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-g.stopped:
		t.Fatal("the gateway ended while a second try waited, with a drain of 20 seconds")
	case got := <-answered:
		t.Fatalf("the answer came before the second try was let go: %d %s", got.status, got.body)
	default:
	}
	if plain := affinityDecrypt(t, g.key(), mustRead(t, g.snapshot())); !bytes.Contains(plain, []byte(togetherSession)) {
		t.Fatal("the conversation whose second try waits is not in the bindings file written at the stop")
	}

	_ = os.Remove(hold)
	held = false
	got := <-answered
	if got.status != http.StatusOK || !strings.Contains(got.body, "answer from "+bound) {
		t.Fatalf("the answer after the stop: %d %s; want the second try's, from %s", got.status, got.body, bound)
	}
	if calls := vendor.keys(); len(calls) != 2 || calls[1] != bound {
		t.Fatalf("vendor requests %v, want the refused one and the second try, both on %s", calls, bound)
	}
	<-g.stopped
	g.cmd = nil
	g.logMu.Lock()
	stopLog := g.log.String()[g.logFrom:]
	g.logMu.Unlock()
	for _, line := range []string{"stopping: waiting up to 20 seconds for the answers in flight", "same-account retry:"} {
		if !strings.Contains(stopLog, line) {
			t.Fatalf("the log has no %q:\n%s", line, stopLog)
		}
	}

	// The next start puts the conversation back on the account that answered. New conversations go round first, so
	// that the next new binding would go to the other key: the conversation's turn cannot land on its key by turn.
	g.startEvery("1h")
	state := g.expectState("after a stop during a second try's wait", map[string]any{"in_effect": true})
	restored, _ := state["restored_at_start"].(map[string]any)
	if sessions, _ := restored["sessions"].(float64); int(sessions) < 1 {
		t.Fatalf("restored %v, want the conversation", restored)
	}
	landed := false
	for i := 0; i < 3 && !landed; i++ {
		status, body := togetherSend(t, g.port, "", fmt.Sprintf("probe %d: what is %d+%d?", i, i, i))
		if status != http.StatusOK {
			t.Fatalf("probe: %d %s", status, body)
		}
		landed = strings.Contains(body, "answer from "+bound)
	}
	if !landed {
		t.Fatalf("no probe landed on %s", bound)
	}
	if status, body := togetherSend(t, g.port, togetherSession, "after the restart: what is 5+5?"); status != http.StatusOK || !strings.Contains(body, "answer from "+bound) {
		t.Fatalf("after the restart: %d %s; want the conversation back on %s", status, body, bound)
	}
	g.stop()
}
