package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Provimux: one standing rule per provider in place of watching each call (answered-by.md, part (5)). Findings 52 (the
// gateway's own re-registration at the moment an answer is published cost it its entry) and 53 (a plugin's executor put
// in place during a first attempt, the call failing over to a second credential, and the answer naming an entry).

// provimuxOpenRouterTwoKeys is the review's OpenRouter key with a second key of the same provider, for a failover.
const provimuxOpenRouterTwoKeys = reviewRouter + "        - api-key: review-synthetic-second\n"

// Finding 52, the moment itself: an answer held at the vendor; while it is held the manager publishes the gateway's own
// built-in executor for the provider, the first thing the service's registration does (at 494df49e3 the review held the
// service right there, before it recorded the registration: the answer lost its entry 25 of 25). Nothing is recorded
// afterwards any more, so this state is the whole registration. Then the vendor answers: the entry is named, and both
// lists record it. The same with a whole configuration save while the answer is held.
func TestProvimuxReregistrationAtPublication(t *testing.T) {
	cases := []struct {
		name, config, model, entry string
		builtin                    func(h *sentHarness) coreauth.ProviderExecutor
		gate                       func(r *http.Request) bool
	}{
		{"openrouter", reviewRouter, "review-public", "openrouter/lab/native",
			func(h *sentHarness) coreauth.ProviderExecutor {
				exec, _ := h.service.coreManager.Executor(sentKeyFor(h.t, h, "review-public").Provider)
				return executor.NewOpenAICompatExecutor(exec.Identifier(), h.service.currentConfig())
			}, func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/chat/completions") }},
		{"claude-login", sentMatrixConfig, "provimux-test-claude-login", "claude-subscription/provimux-test-claude-login",
			func(h *sentHarness) coreauth.ProviderExecutor {
				return executor.NewClaudeExecutor(h.service.currentConfig())
			},
			func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/v1/messages") }},
	}
	for _, tc := range cases {
		for _, event := range []string{"manager-published", "configuration-saved"} {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", tc.name, event, stream), func(t *testing.T) {
					h := newSentHarness(t, tc.config)
					useAnsweredBook(t, newSentClock())
					if tc.name == "claude-login" {
						sentLogins(t, h)
					}
					arrived, released := make(chan struct{}), make(chan struct{})
					var once sync.Once
					reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
						if tc.gate(r) {
							once.Do(func() { close(arrived) })
							<-released
						}
						return h.stand.RoundTrip(r)
					}))
					answer := make(chan sentAnswer, 1)
					go func() { answer <- h.post("/v1/chat/completions", chatBody(tc.model, stream, ""), nil) }()
					select {
					case <-arrived:
					case <-time.After(10 * time.Second):
						t.Fatal("fixture: the answer did not reach the vendor")
					}
					switch event {
					case "manager-published":
						h.service.coreManager.RegisterExecutor(tc.builtin(h))
					case "configuration-saved":
						cfg := h.service.currentConfig().CloneForRuntime()
						on := true
						cfg.Models.AnsweredBy = &on
						cfg.RequestRetry++
						h.service.applyConfigUpdateWithAuthSynthesis(context.Background(), cfg, false)
						if tc.name == "claude-login" {
							// The save re-read the configuration and dropped the name the test put on its in-memory
							// login (nothing on disk holds it): put back, for the record to have the row.
							current, _ := h.service.coreManager.GetByID("provimux-test-claude-login.json")
							provimuxServeAgain(t, h, current, tc.model)
						}
					}
					close(released)
					got := <-answer
					account := got.get(logging.ProvimuxAccountHeader)
					if got.status != http.StatusOK || account == "" {
						t.Fatalf("fixture: %d %s", got.status, got.body)
					}
					client, management, _ := h.listsRow(sentClientKey, tc.model, account)
					t.Logf("%s while the answer was held: entry=%q client=%v management=%v", event, got.get(logging.ProvimuxEntryHeader), provimuxItemsOf(client), provimuxItemsOf(management))
					if got.get(logging.ProvimuxEntryHeader) != tc.entry {
						t.Errorf("the gateway's own registration cost the answer its entry")
					}
					if c, mg := provimuxItemsOf(client), provimuxItemsOf(management); len(c) != 1 || !strings.HasPrefix(c[0], tc.entry+"@") ||
						len(mg) != 1 || !strings.HasPrefix(mg[0], tc.entry+"@") {
						t.Errorf("the lists did not record the entry")
					}
				})
			}
		}
	}
}

// Finding 52, continuously: the gateway's own executor put back without pause beside 300 streaming answers, by the
// service as a save, a login change or a model-list refresh does ("service"; at 494df49e3 the review saw 62 of 300 lose
// their entry), and by the service and directly at the manager in turn ("service-and-manager"). All 300 name it.
func TestProvimuxReregistrationsBesideStreams(t *testing.T) {
	for _, who := range []string{"service", "service-and-manager"} {
		t.Run(who, func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			useAnsweredBook(t, newSentClock())
			a := sentKeyFor(t, h, "review-public")
			exec, _ := h.service.coreManager.Executor(a.Provider)
			provider := exec.Identifier()
			var registrations atomic.Int64
			stop, stopped := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(stopped)
				for {
					select {
					case <-stop:
						return
					default:
					}
					h.service.registerExecutorForAuth(a, true)
					registrations.Add(1)
					if who == "service-and-manager" {
						h.service.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(provider, h.service.currentConfig()))
						registrations.Add(1)
					}
				}
			}()
			const answers = 300
			missing := 0
			for i := 0; i < answers; i++ {
				got := h.post("/v1/chat/completions", chatBody("review-public", true, ""), nil)
				if got.status != http.StatusOK || got.get(logging.ProvimuxAccountHeader) == "" {
					close(stop)
					<-stopped
					t.Fatalf("fixture: answer %d: %d %s", i, got.status, got.body)
				}
				if got.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
					missing++
				}
			}
			close(stop)
			<-stopped
			t.Logf("the gateway's own registrations (%s)=%d beside %d streaming answers: %d without their entry", who, registrations.Load(), answers, missing)
			if missing != 0 || registrations.Load() < answers {
				t.Errorf("%d of %d answers lost their entry (registrations %d)", missing, answers, registrations.Load())
			}
		})
	}
}

// provimuxAuthCapability is a plugin's login capability for a provider, as a plugin declares it to the real plugin host.
type provimuxAuthCapability struct {
	pluginapi.AuthProvider
	id string
}

func (c provimuxAuthCapability) Identifier() string { return c.id }

// Finding 53 and the like: two keys of one OpenRouter provider; the first is answered 429, the call fails over to the
// second. An executor that is not the gateway's own (the real plugin host's plugin-refresh executor, or a plugin's own
// executor) is put in place for the answering provider, and the service's built-in one put back, at a moment relative
// to the call: before it, while its first attempt is at the vendor, while its second is. No answer of the provider names
// an entry from then on (this call's, before or after its failover, and every later one), and both lists record null;
// a provider for which nothing foreign was registered keeps its entry (the control "other", and "none").
func TestProvimuxForeignExecutorAtAnyMoment(t *testing.T) {
	for _, foreign := range []string{"plugin-refresh", "plugin-executor"} {
		for _, moment := range []string{"none", "other", "before", "first-attempt", "second-attempt"} {
			for _, stream := range []bool{false, true} {
				if foreign == "plugin-executor" && moment == "other" {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/stream=%v", foreign, moment, stream), func(t *testing.T) {
					provimuxForeignAtMoment(t, foreign, moment, stream)
				})
			}
		}
	}
}

func provimuxForeignAtMoment(t *testing.T, foreign, moment string, stream bool) {
	h := newSentHarness(t, provimuxOpenRouterTwoKeys)
	useAnsweredBook(t, newSentClock())
	var a *coreauth.Auth
	for _, candidate := range h.service.coreManager.List() {
		if strings.Contains(candidate.ID, "review-router") {
			a = candidate
			break
		}
	}
	if a == nil {
		t.Fatal("fixture: no OpenRouter key")
	}
	m := h.service.coreManager
	// put puts the foreign executor in place for provider and the service's own back.
	put := func(provider string) {
		switch foreign {
		case "plugin-refresh":
			host := pluginhost.New()
			h.service.pluginHost = host
			host.RegisterPluginForTest("provimux-test-auth", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: provimuxAuthCapability{id: provider}}})
			if !host.HasAuthProvider(provider) {
				t.Fatalf("fixture: the plugin host has no login capability for %s", provider)
			}
			h.service.ensureExecutorsForAuthWithMode(a, true)
			if exec, ok := m.Executor(a.Provider); !ok || pluginhost.IsPluginRefreshCompatExecutor(exec) != (provider == a.Provider) {
				t.Fatalf("fixture: %T in place for %s", exec, a.Provider)
			}
			host.ApplyConfig(context.Background(), &config.Config{})
			if host.HasAuthProvider(provider) {
				t.Fatal("fixture: the plugin is still active")
			}
		case "plugin-executor":
			m.RegisterExecutor(provimuxPluginExecutor{provider: provider})
		}
		h.service.ensureExecutorsForAuthWithMode(a, true)
		if exec, ok := m.Executor(a.Provider); !ok || provimuxBuiltinKind(exec) == "" {
			t.Fatalf("fixture: the service did not put its own back: %T", exec)
		}
	}
	event := func() {
		switch moment {
		case "other":
			put("provimux-test-other-provider")
		case "before", "first-attempt", "second-attempt":
			put(a.Provider)
		}
	}
	var mu sync.Mutex
	var keys []string
	held := map[string]int{"first-attempt": 1, "second-attempt": 2}[moment]
	arrived, released := make(chan struct{}), make(chan struct{})
	reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Authorization"))
		n := len(keys)
		mu.Unlock()
		if n == held {
			close(arrived)
			<-released
		}
		response, err := h.stand.RoundTrip(r)
		if n == 1 && err == nil {
			_ = response.Body.Close()
			response = reviewOK(r, `{"error":{"message":"provimux-test busy"}}`, "application/json")
			response.StatusCode = http.StatusTooManyRequests
		}
		return response, err
	}))
	if moment == "before" || moment == "other" || moment == "none" {
		event()
		close(released)
	}
	answer := make(chan sentAnswer, 1)
	go func() { answer <- h.post("/v1/chat/completions", chatBody("review-public", stream, ""), nil) }()
	if held != 0 {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("fixture: the attempt did not reach the vendor")
		}
		event()
		close(released)
	}
	got := <-answer
	mu.Lock()
	failedOver := len(keys) == 2 && keys[0] != keys[1] && keys[0] != "" && keys[1] != ""
	mu.Unlock()
	account := got.get(logging.ProvimuxAccountHeader)
	if got.status != http.StatusOK || !failedOver || account == "" {
		t.Fatalf("fixture: %d, failed over %v (%d requests), account %q: %s", got.status, failedOver, len(keys), account, got.body)
	}
	// A later answer of the provider, without a failover.
	mu.Lock()
	keys = []string{"provimux-test-later-0", "provimux-test-later-1"}
	mu.Unlock()
	later := h.post("/v1/chat/completions", chatBody("review-public", stream, ""), nil)
	client, management, _ := h.listsRow(sentClientKey, "review-public", account)
	t.Logf("%s at %s: this answer's entry=%q, a later one's=%q; client=%v management=%v", foreign, moment, got.get(logging.ProvimuxEntryHeader), later.get(logging.ProvimuxEntryHeader), provimuxItemsOf(client), provimuxItemsOf(management))
	if later.status != http.StatusOK || later.get(logging.ProvimuxAccountHeader) == "" {
		t.Fatalf("fixture: a later answer %d %s", later.status, later.body)
	}
	want := "openrouter/lab/native"
	if moment == "before" || moment == "first-attempt" || moment == "second-attempt" {
		want = ""
	}
	if got.get(logging.ProvimuxEntryHeader) != want || later.get(logging.ProvimuxEntryHeader) != want {
		t.Errorf("entries %q and %q, want %q", got.get(logging.ProvimuxEntryHeader), later.get(logging.ProvimuxEntryHeader), want)
	}
	if want == "" {
		for _, name := range []string{logging.ProvimuxModelHeader, logging.ProvimuxModelSentHeader, logging.ProvimuxModelBasisHeader} {
			if len(got.header.Values(name)) != 0 || len(later.header.Values(name)) != 0 {
				t.Errorf("%s written", name)
			}
		}
	}
	recorded := "null@"
	if want != "" {
		recorded = want + "@"
	}
	for _, list := range []*listedAnswered{client, management} {
		if items := provimuxItemsOf(list); len(items) == 0 || !strings.HasPrefix(items[0], recorded) {
			t.Errorf("the lists recorded %v, want %s", items, recorded)
		}
	}
}

// provimuxItemsOf is a listed row's items, nil for no row.
func provimuxItemsOf(row *listedAnswered) []string {
	if row == nil {
		return nil
	}
	return row.items()
}
