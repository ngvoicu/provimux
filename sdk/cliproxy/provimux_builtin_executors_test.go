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

// Provimux: the closed list of built-in executors and the standing rule (answered-by.md, rule A.1 as decided in part
// (5)). An answer names an entry only when the executor that answers is of a type on the list, and no executor that is
// not one of the gateway's own was ever put in place for its provider in this process.

// provimuxTestExecutor is an executor of a type not on the list.
type provimuxTestExecutor struct{ coreauth.ProviderExecutor }

// On the wire: an answer of the gateway's own executor names its entry, however often the service puts its own back;
// once an executor that is not the gateway's own was put in place for the provider (here an embedding's wrapper of
// the built-in one), none does, also after the service put its own back; another provider keeps its own.
func TestProvimuxBuiltinExecutorOnTheWire(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	useAnsweredBook(t, newSentClock())
	logins := sentLogins(t, h)
	claude, codex := "provimux-test-claude-login", "provimux-test-codex-login"
	ask := func(model string) string {
		h.stand.reset()
		ans := h.post("/v1/chat/completions", chatBody(model, false, ""), nil)
		if ans.status != http.StatusOK || ans.get(logging.ProvimuxAccountHeader) == "" || len(h.stand.answered()) != 1 {
			t.Fatalf("fixture %d %v %s", ans.status, ans.header, ans.body)
		}
		return ans.get(logging.ProvimuxEntryHeader)
	}
	if got := ask(claude); got != "claude-subscription/"+claude {
		t.Fatalf("the gateway's own executor: %q", got)
	}
	// The service, and other code of the gateway, putting a built-in executor back: no change.
	for i := 0; i < 3; i++ {
		h.service.registerExecutorForAuth(logins["claude"], true)
		h.service.coreManager.RegisterExecutor(executor.NewClaudeExecutor(h.service.currentConfig()))
	}
	if got := ask(claude); got != "claude-subscription/"+claude {
		t.Fatalf("after the gateway put its own executor back: %q", got)
	}
	// An executor that is not the gateway's own, for the answering provider (here a wrapper of the built-in one, which
	// still sends the same request): none, now and after the service's own is back, for every later answer.
	orig, _ := h.service.coreManager.Executor("claude")
	h.service.coreManager.RegisterExecutor(&provimuxTestExecutor{orig})
	if got := ask(claude); got != "" {
		t.Fatalf("an executor that is not the gateway's own: %q", got)
	}
	h.service.registerExecutorForAuth(logins["claude"], true)
	if exec, _ := h.service.coreManager.Executor("claude"); provimuxBuiltinKind(exec) == "" {
		t.Fatalf("fixture: the service did not put its own back: %T", exec)
	}
	for i := 0; i < 3; i++ {
		if got := ask(claude); got != "" {
			t.Fatalf("answer %d after the service put its own back: %q", i, got)
		}
	}
	// Another provider keeps its own.
	if got := ask(codex); got != "openai-codex/"+codex {
		t.Fatalf("another provider: %q", got)
	}
}

// The rule looks at what is registered, never at counts: an embedding's ExecutorWrapper around the service's built-in
// executors makes each a type not on the list; AI Studio's executor is the gateway's own and marks nothing.
func TestProvimuxOwnExecutors(t *testing.T) {
	cfg := &config.Config{}
	for _, exec := range []coreauth.ProviderExecutor{executor.NewClaudeExecutor(cfg), executor.NewOpenAICompatExecutor("provimux-test", cfg), executor.NewAIStudioExecutor(cfg, "provimux-test", nil)} {
		if !provimuxOwnExecutor(exec) {
			t.Errorf("%T is not the gateway's own", exec)
		}
		if provimuxOwnExecutor(&provimuxTestExecutor{exec}) {
			t.Errorf("a wrapper of %T is the gateway's own", exec)
		}
	}
	if provimuxOwnExecutor(provimuxPluginExecutor{provider: "claude"}) {
		t.Error("a plugin's executor is the gateway's own")
	}
}

// The entry's hooks are set once, by this package, while the program initialises: a later call changes nothing.
func TestProvimuxAnswerEntryHooksAreSetOnce(t *testing.T) {
	coreauth.SetProvimuxAnswerEntry(func(coreauth.ProviderExecutor) string { return "claude" }, func(coreauth.ProviderExecutor) bool { return true }, func(*coreauth.Auth) bool { return true },
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
