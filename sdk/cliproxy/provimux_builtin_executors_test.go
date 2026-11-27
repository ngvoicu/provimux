package cliproxy

import (
	"net/http"
	"os"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the closed list of built-in executors (the lead's amendment of 2026-11-27 (2), rule A.1). An answer names
// an entry only when the executor that answers is one the gateway's own service registered, of a type on the list,
// unwrapped, with no other registration for its provider between the service's previous one and it, nor after it.

// provimuxTestExecutor is an executor of a type not on the list.
type provimuxTestExecutor struct{ coreauth.ProviderExecutor }

func TestProvimuxBuiltinExecutorRecord(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{}
	service := &Service{coreManager: m}
	builtin := func() coreauth.ProviderExecutor { return executor.NewOpenAICompatExecutor("provimux-test-compat", cfg) }
	current := func() (coreauth.ProviderExecutor, uint64) {
		exec, _ := m.Executor("provimux-test-compat")
		return exec, m.ExecutorRevision("provimux-test-compat")
	}
	kind := func() string {
		exec, revision := current()
		return provimuxBuiltinExecutor(m, exec, revision)
	}

	service.registerExecutor(builtin())
	if got := kind(); got != "openai-compatibility" {
		t.Fatalf("the service's own registration: %q", got)
	}
	service.registerExecutor(builtin()) // a reload registers a new one: still the service's own
	if got := kind(); got != "openai-compatibility" {
		t.Fatalf("the service's next registration: %q", got)
	}

	// Another program registers an executor, even one of a built-in type: none from then on.
	m.RegisterExecutor(builtin())
	if got := kind(); got != "" {
		t.Fatalf("an executor another program registered: %q", got)
	}
	// The service registers its own again: an attempt may have started on the other's, so still none.
	service.registerExecutor(builtin())
	if got := kind(); got != "" {
		t.Fatalf("the service's registration right after another's: %q", got)
	}
	// Once more, with nothing between: the service's own again.
	service.registerExecutor(builtin())
	if got := kind(); got != "openai-compatibility" {
		t.Fatalf("the service's clean registration: %q", got)
	}
	// Unregistered and registered by the service: something came between.
	m.UnregisterExecutor("provimux-test-compat")
	service.registerExecutor(builtin())
	if got := kind(); got != "" {
		t.Fatalf("a registration after an unregistration: %q", got)
	}

	// An embedding's ExecutorWrapper wraps the service's built-in executors: none.
	wrapped := &Service{coreManager: coreauth.NewManager(nil, nil, nil), executorWrapper: func(_ string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		return &provimuxTestExecutor{exec}
	}}
	wrapped.registerExecutor(executor.NewClaudeExecutor(cfg))
	exec, _ := wrapped.coreManager.Executor("claude")
	if got := provimuxBuiltinExecutor(wrapped.coreManager, exec, wrapped.coreManager.ExecutorRevision("claude")); got != "" {
		t.Fatalf("a wrapped executor: %q", got)
	}
	// A type not on the list, registered by the service: none.
	service.registerExecutor(&provimuxTestExecutor{executor.NewMetaExecutor(cfg)})
	exec, _ = m.Executor("meta")
	if got := provimuxBuiltinExecutor(m, exec, m.ExecutorRevision("meta")); got != "" {
		t.Fatalf("a type not on the list: %q", got)
	}
	// Another manager knows nothing of this one's registrations.
	if got := provimuxBuiltinExecutor(coreauth.NewManager(nil, nil, nil), exec, 1); got != "" {
		t.Fatalf("another manager: %q", got)
	}
}

// On the wire: an answer of the gateway's own executor names its entry; once another program registered an executor
// for the provider (here even one of the built-in type), none does, until the service registered its own twice with
// nothing between.
func TestProvimuxBuiltinExecutorOnTheWire(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	a := sentKeyFor(t, h, "review-public")
	ask := func() string {
		h.stand.reset()
		ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
		if ans.status != http.StatusOK || ans.get(logging.ProvimuxAccountHeader) == "" || len(h.stand.answered()) != 1 {
			t.Fatalf("fixture %d %v %s", ans.status, ans.header, ans.body)
		}
		return ans.get(logging.ProvimuxEntryHeader)
	}
	if got := ask(); got != "openrouter/lab/native" {
		t.Fatalf("the gateway's own executor: %q", got)
	}
	orig, _ := h.service.coreManager.Executor(a.Provider)
	h.service.coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(orig.Identifier(), h.service.currentConfig()))
	if got := ask(); got != "" {
		t.Fatalf("an executor registered by another program: %q", got)
	}
	h.service.registerExecutorForAuth(a, true)
	if got := ask(); got != "" {
		t.Fatalf("the service's registration right after another's: %q", got)
	}
	h.service.registerExecutorForAuth(a, true)
	if got := ask(); got != "openrouter/lab/native" {
		t.Fatalf("the service's clean registration: %q", got)
	}
}

// The entry's hooks are set once, by this package, while the program initialises: a later call changes nothing.
func TestProvimuxAnswerEntryHooksAreSetOnce(t *testing.T) {
	coreauth.SetProvimuxAnswerEntry(func(*coreauth.Manager, coreauth.ProviderExecutor, uint64) string { return "claude" },
		func(*coreauth.Auth, cliproxyexecutor.ProvimuxSentRequest) string { return "provimux-test/replaced" })
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if ans.status != http.StatusOK || ans.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
		t.Fatalf("a later SetProvimuxAnswerEntry took effect: %d %q", ans.status, ans.get(logging.ProvimuxEntryHeader))
	}
}

// The closed list holds every executor the service's set-up makes for its own credentials (service_executors.go),
// but AI Studio's (a socket a browser opened, which no answer's entry is read from).
func TestProvimuxBuiltinExecutorListIsClosed(t *testing.T) {
	cfg := &config.Config{}
	made := map[string]coreauth.ProviderExecutor{
		"NewCodexAutoExecutor":          executor.NewCodexAutoExecutor(cfg),
		"NewGeminiExecutor":             executor.NewGeminiExecutor(cfg),
		"NewGeminiInteractionsExecutor": executor.NewGeminiInteractionsExecutor(cfg),
		"NewGeminiVertexExecutor":       executor.NewGeminiVertexExecutor(cfg),
		"NewAntigravityExecutor":        executor.NewAntigravityExecutor(cfg),
		"NewClaudeExecutor":             executor.NewClaudeExecutor(cfg),
		"NewKimiExecutor":               executor.NewKimiExecutor(cfg),
		"NewXAIAutoExecutor":            executor.NewXAIAutoExecutor(cfg),
		"NewCopilotExecutor":            executor.NewCopilotExecutor(cfg),
		"NewDevinExecutor":              executor.NewDevinExecutor(cfg),
		"NewMetaExecutor":               executor.NewMetaExecutor(cfg),
		"NewOpenAICompatExecutor":       executor.NewOpenAICompatExecutor("provimux-test", cfg),
	}
	source, err := os.ReadFile("service_executors.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`executor\.(New[A-Za-z]+Executor)\(`).FindAllStringSubmatch(string(source), -1) {
		name := match[1]
		if name == "NewAIStudioExecutor" {
			continue
		}
		exec, ok := made[name]
		if !ok {
			t.Errorf("the service makes %s, which the closed list does not know", name)
			continue
		}
		if provimuxBuiltinKind(exec) == "" {
			t.Errorf("%s is not on the closed list", name)
		}
	}
	if provimuxBuiltinKind(executor.NewAIStudioExecutor(cfg, "provimux-test", nil)) != "" {
		t.Error("AI Studio's executor is on the closed list")
	}
}
