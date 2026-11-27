package auth

import (
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the standing rule (answered-by.md, part (5)). An executor that is not one of the gateway's own marks its
// provider at the registration itself, before it is in place; the gateway's own, registered however often, never does;
// the mark is never cleared; the log says so once per provider.

// provimuxOwnTestExecutor stands for one of the gateway's built-in executors (the hooks below say so); any other type is
// not the gateway's own.
type provimuxOwnTestExecutor struct {
	ProviderExecutor
	provider string
}

func (e *provimuxOwnTestExecutor) Identifier() string { return e.provider }

type provimuxForeignTestExecutor struct {
	ProviderExecutor
	provider string
}

func (e *provimuxForeignTestExecutor) Identifier() string { return e.provider }

func provimuxForeignHooks(t *testing.T) {
	t.Helper()
	// In this package's test binary nothing else sets them: the first call holds.
	SetProvimuxAnswerEntry(func(exec ProviderExecutor) string {
		if _, ok := exec.(*provimuxOwnTestExecutor); ok {
			return "claude"
		}
		return ""
	}, func(exec ProviderExecutor) bool {
		_, ok := exec.(*provimuxOwnTestExecutor)
		return ok
	}, func(*Auth) bool { return true }, func(*Auth, cliproxyexecutor.ProvimuxSentRequest) string { return "" })
	t.Cleanup(origin.ShippedGatewayForTests(true))
}

func provimuxMarked(m *Manager, provider string) bool {
	_, marked := provimuxForeign.Load(provimuxForeignKey{m: m, provider: provider})
	return marked
}

// The gateway putting its own executor back, from many goroutines at once and many times, marks nothing.
func TestProvimuxOwnRegistrationsNeverMark(t *testing.T) {
	provimuxForeignHooks(t)
	m := NewManager(nil, nil, nil)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				m.RegisterExecutor(&provimuxOwnTestExecutor{provider: "claude"})
			}
		}()
	}
	wg.Wait()
	auth := &Auth{ID: "provimux-test", Provider: "claude"}
	if provimuxMarked(m, "claude") || m.provimuxForeignTookPart(auth, provimuxServingNow(m, auth)) {
		t.Fatal("the gateway's own registrations marked the provider")
	}
}

// The mark is set before the executor is in place: whoever first sees a foreign executor published sees its provider
// marked. A goroutine watches the manager while the executor is registered; it checks the mark at the first sight.
func TestProvimuxForeignMarkedBeforeInPlace(t *testing.T) {
	provimuxForeignHooks(t)
	for i := 0; i < 200; i++ {
		m := NewManager(nil, nil, nil)
		m.RegisterExecutor(&provimuxOwnTestExecutor{provider: "claude"})
		foreign := &provimuxForeignTestExecutor{provider: "claude"}
		seen := make(chan bool)
		go func() {
			for {
				if exec, ok := m.Executor("claude"); ok && exec == ProviderExecutor(foreign) {
					seen <- provimuxMarked(m, "claude")
					return
				}
			}
		}()
		m.RegisterExecutor(foreign)
		if !<-seen {
			t.Fatalf("run %d: the foreign executor was in place before its provider was marked", i)
		}
	}
}

// Set once, never cleared; per provider; per manager. The log says it once per provider.
func TestProvimuxForeignMarkStaysAndIsLoggedOnce(t *testing.T) {
	provimuxForeignHooks(t)
	_, hook := logtest.NewNullLogger()
	saved := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	log.AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(saved) })

	m, other := NewManager(nil, nil, nil), NewManager(nil, nil, nil)
	claude := &Auth{ID: "provimux-test-claude", Provider: "claude"}
	codex := &Auth{ID: "provimux-test-codex", Provider: "codex"}
	m.RegisterExecutor(&provimuxOwnTestExecutor{provider: "codex"})
	for i := 0; i < 3; i++ {
		m.RegisterExecutor(&provimuxForeignTestExecutor{provider: "claude"})
		m.RegisterExecutor(&provimuxOwnTestExecutor{provider: "claude"})
		m.UnregisterExecutor("claude")
		m.RegisterExecutor(&provimuxOwnTestExecutor{provider: "claude"})
	}
	other.RegisterExecutor(&provimuxOwnTestExecutor{provider: "claude"})
	if !m.provimuxForeignTookPart(claude, provimuxServingNow(m, claude)) {
		t.Error("the provider's mark was cleared by the gateway putting its own back")
	}
	if m.provimuxForeignTookPart(codex, provimuxServingNow(m, codex)) {
		t.Error("another provider was marked")
	}
	if other.provimuxForeignTookPart(claude, provimuxServingNow(other, claude)) {
		t.Error("another manager was marked")
	}
	logged := 0
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "a plugin took part") {
			logged++
			if !strings.Contains(entry.Message, `"claude"`) || !strings.Contains(entry.Message, "until the gateway restarts") {
				t.Errorf("log: %q", entry.Message)
			}
		}
	}
	if logged != 1 {
		t.Errorf("logged %d times, want once", logged)
	}
}

// provimuxServingNow is the executor an attempt handed over now would be served by, for auth.
func provimuxServingNow(m *Manager, auth *Auth) ProviderExecutor {
	m.mu.RLock()
	in := &Manager{executors: maps.Clone(m.executors)}
	m.mu.RUnlock()
	return provimuxAttempt{m: m, in: in}.provimuxServingExecutor(auth)
}
