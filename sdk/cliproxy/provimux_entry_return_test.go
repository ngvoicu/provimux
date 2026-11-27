package cliproxy

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the automatic return (routing.session-affinity-return) and an answer's entry (models.answered-by)
// together, on the real server. A conversation falls back from its first account to a lower-priority one when the
// first refuses; its answers there name the lower account and its entry. Once no message of it was routed to an
// account for cold-after, its next message goes back by itself, and that answer names the first account and the
// entry of what was sent from it. Each account sends its own upstream model, so the two entries differ. Every key
// and model is invented; the vendor is the harness's stand-in in this process.

const entryReturnConfig = `api-keys:
  claude:
    - name: provimux-test-first
      priority: 10
      models:
        - name: provimux-test-claude-first
          alias: provimux-test-claude-chat
      keys:
        - api-key: sk-provimux-test-first
    - name: provimux-test-lower
      priority: 5
      models:
        - name: provimux-test-claude-lower
          alias: provimux-test-claude-chat
      keys:
        - api-key: sk-provimux-test-lower
routing:
  session-affinity: true
  session-affinity-ttl: "1h"
  session-affinity-return:
    cold-after: "6m"
`

func TestProvimuxReturnedAnswerNamesItsAccountAndEntry(t *testing.T) {
	t.Cleanup(func() { coreauth.ProvimuxSetSessionReturn(nil, 0) })
	h := newSentHarness(t, entryReturnConfig)
	m := h.service.coreManager
	keyed := func(key string) *coreauth.Auth {
		t.Helper()
		for _, auth := range m.List() {
			if auth.Attributes["api_key"] == key {
				return auth
			}
		}
		t.Fatalf("fixture: no account has %s", key)
		return nil
	}
	first, lower := keyed("sk-provimux-test-first"), keyed("sk-provimux-test-lower")
	conversation := sentHeader("X-Session-Affinity", "provimux-test-conversation")
	ask := func() sentAnswer {
		t.Helper()
		h.stand.reset()
		return h.post("/v1/chat/completions", chatBody("provimux-test-claude-chat", false, ""), conversation)
	}
	returned := func() int64 { return coreauth.ProvimuxSessionReturnStateOf(m, time.Now()).Counts.Returned }

	// The first account refuses the conversation's first message: it falls back to the lower one.
	h.stand.reset()
	h.stand.mu.Lock()
	h.stand.status = func(r *http.Request, _ int) (int, http.Header) {
		if r.Header.Get("x-api-key") == "sk-provimux-test-first" {
			return http.StatusTooManyRequests, nil
		}
		return 0, nil
	}
	h.stand.mu.Unlock()
	h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-claude-chat", false, ""), conversation), lower, "api.anthropic.com", "anthropic/provimux-test-claude-lower")

	// The first account is free again (a success clears what the refusal set on it).
	if current, ok := m.GetByID(first.ID); ok {
		for model := range current.ModelStates {
			m.MarkResult(t.Context(), coreauth.Result{AuthID: first.ID, Provider: first.Provider, Model: model, Success: true})
		}
	}

	// Within cold-after the conversation stays where it is, and its answer still names the lower account.
	before := returned()
	h.checkSent(ask(), lower, "api.anthropic.com", "anthropic/provimux-test-claude-lower")
	if got := returned() - before; got != 0 {
		t.Fatalf("within cold-after the rule returned %d conversations", got)
	}

	// No message of it was routed to an account for longer than cold-after: its next message goes back by itself.
	coreauth.ProvimuxSessionReturnAgeForTest(m, 7*time.Minute)
	h.checkSent(ask(), first, "api.anthropic.com", "anthropic/provimux-test-claude-first")
	if got := returned() - before; got != 1 {
		t.Fatalf("the return was counted %d times, want 1", got)
	}

	// It stays on the first account after the return.
	h.checkSent(ask(), first, "api.anthropic.com", "anthropic/provimux-test-claude-first")
}
