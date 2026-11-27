package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Provimux: finding 57 and part (6). An answer's entry goes by the executor the attempt was handed to, never by what
// the manager's table holds when the answer is published.

// provimuxCandidateExecutor is a plugin's executor the plugin host offers for a provider key, which the native
// executor outranks: it is never put in place.
type provimuxCandidateExecutor struct {
	pluginapi.ProviderExecutor
	id string
}

func (c provimuxCandidateExecutor) Identifier() string { return c.id }

// The review's case: a native OpenRouter key; a loaded plugin offers an executor for the same provider key, which the
// native one outranks; while a native request is at the vendor, a login for that key is added and the service takes
// the native executor out of its table. The answer, plain and streaming, names its entry and both lists record it (at
// bb3bb0bce: none, and null).
func TestProvimuxServingExecutorPluginCandidate(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			useAnsweredBook(t, newSentClock())
			a := sentKeyFor(t, h, "review-public")
			m := h.service.coreManager
			native, ok := m.Executor(a.Provider)
			if !ok {
				t.Fatal("fixture: no native executor")
			}
			host := pluginhost.New()
			h.service.pluginHost = host
			host.RegisterPluginForTest("provimux-test-provider", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Executor: provimuxCandidateExecutor{id: a.Provider}}})
			registerPluginExecutors(host, m)
			if current, _ := m.Executor(a.Provider); current != native || !host.HasExecutorCandidateProvider(a.Provider) {
				t.Fatalf("fixture: the plugin's executor was put in place: %T", current)
			}
			ask := func() sentAnswer { return h.post("/v1/chat/completions", chatBody("review-public", stream, ""), nil) }
			if control := ask(); control.status != http.StatusOK || control.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
				t.Fatalf("fixture: the native control: %d %q", control.status, control.get(logging.ProvimuxEntryHeader))
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "provimux-test-login.json"), []byte(fmt.Sprintf(`{"type":%q,"access_token":"provimux-test-only"}`, a.Provider)), 0o600); err != nil {
				t.Fatal(err)
			}
			logins, err := synthesizer.NewFileSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: h.service.currentConfig(), AuthDir: dir, Now: time.Now()})
			if err != nil || len(logins) != 1 {
				t.Fatalf("fixture: the login file: %v %d", err, len(logins))
			}
			arrived, released := make(chan struct{}), make(chan struct{})
			var once sync.Once
			reviewSetVendor(h, reviewRT(func(r *http.Request) (*http.Response, error) {
				once.Do(func() { close(arrived); <-released })
				return h.stand.RoundTrip(r)
			}))
			answer := make(chan sentAnswer, 1)
			go func() { answer <- ask() }()
			<-arrived
			h.service.applyCoreAuthAddOrUpdate(context.Background(), logins[0])
			_, inPlace := m.Executor(a.Provider)
			close(released)
			got := <-answer
			client, management, _ := h.listsRow(sentClientKey, "review-public", got.get(logging.ProvimuxAccountHeader))
			t.Logf("executor in place after the login was added: %v; HTTP %d, entry %q, lists %v / %v", inPlace, got.status, got.get(logging.ProvimuxEntryHeader), provimuxItemsOf(client), provimuxItemsOf(management))
			if inPlace {
				t.Fatal("fixture: the service kept the native executor in its table")
			}
			if got.status != http.StatusOK || got.get(logging.ProvimuxAccountHeader) != a.ProvimuxAccountMarker() {
				t.Fatalf("fixture: %d %s", got.status, got.body)
			}
			if got.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
				t.Error("an answer the native executor served lost its entry when the table changed")
			}
			for _, list := range []*listedAnswered{client, management} {
				if items := provimuxItemsOf(list); len(items) == 0 || !strings.HasPrefix(items[0], "openrouter/lab/native@") {
					t.Errorf("the lists recorded %v", items)
				}
			}
		})
	}
}

// While a built-in attempt is at the vendor the table is emptied, the executor replaced by another of the same
// built-in kind, or replaced by a foreign one. The first two keep the entry (the attempt was served by the built-in
// executor); the third loses it, by the provider's standing flag that the foreign registration set.
func TestProvimuxServingExecutorTableChanges(t *testing.T) {
	for _, change := range []string{"emptied", "same-kind", "foreign"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", change, stream), func(t *testing.T) {
				h := newSentHarness(t, reviewRouter)
				useAnsweredBook(t, newSentClock())
				a := sentKeyFor(t, h, "review-public")
				m := h.service.coreManager
				native, _ := m.Executor(a.Provider)
				arrived, released := make(chan struct{}), make(chan struct{})
				var once sync.Once
				reviewSetVendor(h, reviewRT(func(r *http.Request) (*http.Response, error) {
					once.Do(func() { close(arrived); <-released })
					return h.stand.RoundTrip(r)
				}))
				answer := make(chan sentAnswer, 1)
				go func() { answer <- h.post("/v1/chat/completions", chatBody("review-public", stream, ""), nil) }()
				<-arrived
				switch change {
				case "emptied":
					m.UnregisterExecutor(a.Provider)
				case "same-kind":
					m.RegisterExecutor(executor.NewOpenAICompatExecutor(native.Identifier(), h.service.currentConfig()))
				case "foreign":
					m.RegisterExecutor(provimuxPluginExecutor{provider: a.Provider})
				}
				close(released)
				got := <-answer
				want := "openrouter/lab/native"
				if change == "foreign" {
					want = ""
				}
				t.Logf("%s: HTTP %d, entry %q", change, got.status, got.get(logging.ProvimuxEntryHeader))
				if got.status != http.StatusOK || got.get(logging.ProvimuxAccountHeader) == "" {
					t.Fatalf("fixture: %d %s", got.status, got.body)
				}
				if got.get(logging.ProvimuxEntryHeader) != want {
					t.Errorf("entry %q, want %q", got.get(logging.ProvimuxEntryHeader), want)
				}
			})
		}
	}
}
