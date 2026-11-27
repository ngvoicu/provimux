package cliproxy

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: does an everyday event in the gateway as shipped cost an answer in flight its entry? (The lead's question
// on rule A.1 after the amendment of 2026-11-27 (2), part 3.) A Claude login's streaming answer is held at the stand-in
// vendor, after the request was handed to the transport and before the answer is stated; the event happens; the vendor
// answers. The event is made through the service's own entry points, as the watcher, a configuration save, the plugin
// host and the model-list refresh make it. Since part (5) (one standing rule per provider): every event keeps the entry,
// for the answer in flight and for the answers after it; the control (another kind of executor put in place for the
// answering provider) costs it, and every later answer of that provider, while another provider keeps its own.

// provimuxGate holds every Claude model request at the vendor until it is released.
type provimuxGate struct {
	next     http.RoundTripper
	arrived  chan struct{}
	released chan struct{}
	once     sync.Once
}

func (g *provimuxGate) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/v1/messages") {
		g.once.Do(func() { close(g.arrived) })
		<-g.released
	}
	return g.next.RoundTrip(r)
}

// provimuxPluginExecutor stands for an executor a plugin registers for a provider of its own.
type provimuxPluginExecutor struct {
	coreauth.ProviderExecutor
	provider string
}

func (e provimuxPluginExecutor) Identifier() string { return e.provider }

// provimuxRegistrationRevisions is the manager's executor revision per provider these tests touch: the rule of A.1
// before 2026-11-27 (2), part 4, counted their sum.
func provimuxRegistrationRevisions(m *coreauth.Manager) map[string]uint64 {
	out := map[string]uint64{}
	for _, provider := range []string{"claude", "codex", "gemini", "gemini-interactions", "vertex", "antigravity", "kimi", "xai", "copilot", "devin", "meta", "openai-compatibility", "aistudio", "provimux-test-plugin"} {
		out[provider] = m.ExecutorRevision(provider)
	}
	return out
}

func provimuxRevisionsMoved(before, after map[string]uint64) []string {
	var moved []string
	for provider, revision := range after {
		if before[provider] != revision {
			moved = append(moved, provider)
		}
	}
	sort.Strings(moved)
	return moved
}

type provimuxEverydayEvent struct {
	name string
	run  func(t *testing.T, h *sentHarness, logins map[string]*coreauth.Auth)
	// loses: the control that must cost the answer its entry (another kind of executor put in place for the answering
	// provider while the call ran).
	loses bool
}

func provimuxEverydayEvents() []provimuxEverydayEvent {
	login := func(provider, id string, metadata map[string]any) *coreauth.Auth {
		metadata["type"] = provider
		return &coreauth.Auth{ID: id, FileName: id, Provider: provider, Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/" + id, "source": "/provimux-test/auths/" + id}, Metadata: metadata}
	}
	apply := func(h *sentHarness, auth *coreauth.Auth) {
		// What the watcher hands the service when it reads a login file the loader parsed.
		auth.UpdatedAt = time.Now()
		h.service.applyCoreAuthAddOrUpdate(context.Background(), auth)
	}
	saved := func(t *testing.T, h *sentHarness, change func(*config.Config), withKeys bool) {
		cfg := h.service.currentConfig().CloneForRuntime()
		on := true
		cfg.Models.AnsweredBy = &on
		change(cfg)
		h.service.applyConfigUpdateWithAuthSynthesis(context.Background(), cfg, withKeys)
	}
	return []provimuxEverydayEvent{
		{"nothing", func(*testing.T, *sentHarness, map[string]*coreauth.Auth) {}, false},
		{"the answering login's token refreshed and its file saved", func(t *testing.T, h *sentHarness, logins map[string]*coreauth.Auth) {
			current, _ := h.service.coreManager.GetByID(logins["claude"].ID)
			refreshed := current.Clone()
			refreshed.Metadata["access_token"] = "provimux-test-claude-token-refreshed"
			apply(h, refreshed)
		}, false},
		{"a login of the same provider added", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			apply(h, login("claude", "provimux-test-claude-second.json", map[string]any{"access_token": "provimux-test-second", "expired": sentFar()}))
		}, false},
		{"a login of the same provider disabled", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			second := login("claude", "provimux-test-claude-third.json", map[string]any{"access_token": "provimux-test-third", "expired": sentFar()})
			apply(h, second)
			disabled := second.Clone()
			disabled.Disabled, disabled.Status = true, coreauth.StatusDisabled
			apply(h, disabled)
		}, false},
		{"a login of the same provider removed", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			second := login("claude", "provimux-test-claude-fourth.json", map[string]any{"access_token": "provimux-test-fourth", "expired": sentFar()})
			apply(h, second)
			h.service.applyCoreAuthRemoval(context.Background(), second.ID)
		}, false},
		{"a login of another provider added", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			apply(h, login("meta", "provimux-test-meta-second.json", map[string]any{"api_key": "provimux-test-meta-second", "user_email": "provimux-test-meta-second", "expired": sentFar()}))
		}, false},
		{"a login of another provider disabled", func(t *testing.T, h *sentHarness, logins map[string]*coreauth.Auth) {
			current, _ := h.service.coreManager.GetByID(logins["meta"].ID)
			disabled := current.Clone()
			disabled.Disabled, disabled.Status = true, coreauth.StatusDisabled
			apply(h, disabled)
		}, false},
		{"a login of another provider removed", func(t *testing.T, h *sentHarness, logins map[string]*coreauth.Auth) {
			h.service.applyCoreAuthRemoval(context.Background(), logins["devin"].ID)
		}, false},
		{"the configuration saved with an unrelated change", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			saved(t, h, func(cfg *config.Config) { cfg.RequestRetry++ }, false)
		}, false},
		{"the configuration saved with a change to Claude's keys", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			saved(t, h, func(cfg *config.Config) {
				cfg.ClaudeKey = append(cfg.ClaudeKey, config.ClaudeKey{APIKey: "sk-provimux-test-claude-added"})
			}, true)
		}, false},
		{"the configuration saved with a change to another provider's keys", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			saved(t, h, func(cfg *config.Config) {
				cfg.GeminiKey = append(cfg.GeminiKey, config.GeminiKey{APIKey: "provimux-test-gemini-added"})
			}, true)
		}, false},
		{"a plugin loaded, for a provider of its own", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			h.service.coreManager.RegisterExecutor(provimuxPluginExecutor{provider: "provimux-test-plugin"})
		}, false},
		{"a plugin reloaded, for a provider of its own", func(t *testing.T, h *sentHarness, _ map[string]*coreauth.Auth) {
			h.service.coreManager.RegisterExecutor(provimuxPluginExecutor{provider: "provimux-test-plugin"})
			h.service.coreManager.UnregisterExecutor("provimux-test-plugin")
			h.service.coreManager.RegisterExecutor(provimuxPluginExecutor{provider: "provimux-test-plugin"})
		}, false},
		{"the model lists refreshed", func(t *testing.T, h *sentHarness, logins map[string]*coreauth.Auth) {
			for _, auth := range h.service.coreManager.List() {
				h.service.refreshModelRegistrationForAuth(auth)
			}
			h.service.refreshLiveModelLists(context.Background(), true)
			liveModelLists.fetches.Wait()
		}, false},
		{"control: a plugin's executor put in place for the answering provider, then the service's again", func(t *testing.T, h *sentHarness, logins map[string]*coreauth.Auth) {
			h.service.coreManager.RegisterExecutor(provimuxPluginExecutor{provider: "claude"})
			current, _ := h.service.coreManager.GetByID(logins["claude"].ID)
			h.service.registerExecutorForAuth(current, true)
		}, true},
	}
}

func TestProvimuxEverydayEventsKeepTheEntry(t *testing.T) {
	for _, event := range provimuxEverydayEvents() {
		t.Run(event.name, func(t *testing.T) {
			h := newSentHarness(t, sentMatrixConfig)
			useAnsweredBook(t, newSentClock())
			logins := sentLogins(t, h)
			gate := &provimuxGate{next: h.stand, arrived: make(chan struct{}), released: make(chan struct{})}
			reviewSet(h, gate)
			model := "provimux-test-claude-login"
			answer := make(chan sentAnswer, 1)
			go func() { answer <- h.post("/v1/chat/completions", chatBody(model, true, ""), nil) }()
			select {
			case <-gate.arrived:
			case <-time.After(10 * time.Second):
				t.Fatal("fixture: the answer did not reach the vendor")
			}
			before := provimuxRegistrationRevisions(h.service.coreManager)
			event.run(t, h, logins)
			moved := provimuxRevisionsMoved(before, provimuxRegistrationRevisions(h.service.coreManager))
			close(gate.released)
			got := <-answer
			_, _ = io.Discard.Write([]byte(got.body))
			entry := got.get(logging.ProvimuxEntryHeader)
			t.Logf("EVENT %q: registrations moved for %v, HTTP %d, entry=%q", event.name, moved, got.status, entry)
			if got.status != http.StatusOK || got.get(logging.ProvimuxAccountHeader) == "" {
				t.Fatalf("fixture: %d %s", got.status, got.body)
			}
			// Later answers: the answering provider's, and another provider's. An event that re-reads the configuration
			// or the logins drops the names the test put on its in-memory logins (nothing on disk holds them): they are
			// put back first, with the logins where the event removed them.
			provimuxServeAgain(t, h, logins["claude"], "provimux-test-claude-login")
			provimuxServeAgain(t, h, logins["codex"], "provimux-test-codex-login")
			later := h.post("/v1/chat/completions", chatBody(model, true, ""), nil)
			other := h.post("/v1/chat/completions", chatBody("provimux-test-codex-login", true, ""), nil)
			t.Logf("EVENT %q: a later answer of the provider: entry=%q; another provider's: entry=%q", event.name, later.get(logging.ProvimuxEntryHeader), other.get(logging.ProvimuxEntryHeader))
			if later.status != http.StatusOK || later.get(logging.ProvimuxAccountHeader) == "" || other.status != http.StatusOK {
				t.Fatalf("fixture: %d %d", later.status, other.status)
			}
			if other.get(logging.ProvimuxEntryHeader) != "openai-codex/provimux-test-codex-login" {
				t.Errorf("another provider lost its entry")
			}
			if event.loses {
				if entry != "" || later.get(logging.ProvimuxEntryHeader) != "" {
					t.Errorf("the control kept its entry")
				}
				return
			}
			if entry != "claude-subscription/"+model || later.get(logging.ProvimuxEntryHeader) != "claude-subscription/"+model {
				t.Errorf("the answer in flight lost its entry")
			}
		})
	}
}

// provimuxServeAgain makes login serve model again after an event: registered (as the loader made it) where the event
// removed it, and the model's name on it.
func provimuxServeAgain(t *testing.T, h *sentHarness, login *coreauth.Auth, model string) {
	t.Helper()
	current, ok := h.service.coreManager.GetByID(login.ID)
	if !ok || current == nil || current.Disabled {
		current = h.register(login.Clone())
	}
	registry.GetGlobalRegistry().RegisterClient(current.ID, current.Provider, []*registry.ModelInfo{{ID: model, Object: "model", OwnedBy: "provimux-test"}})
}
