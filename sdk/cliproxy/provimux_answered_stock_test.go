package cliproxy

import (
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the record keeps exactly what the stock output route wrote to the client, read at the moment it wrote it
// (the lead's amendment of 2026-11-27 (2), rule B.1): the account and an entry, the account and no entry (null), or
// nothing. The gateway's own handler served by another server (an embedding) records nothing.
func TestProvimuxAnswerRecordsWhatTheStockRouteCommitted(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	key := sentKeyFor(t, h, "review-public")

	// The account and an entry.
	ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if ans.status != 200 || ans.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
		t.Fatalf("fixture %d %v", ans.status, ans.header)
	}
	client, management, _ := h.listsRow(sentClientKey, "review-public", key.ProvimuxAccountMarker())
	if client == nil || management == nil || len(client.items()) != 1 || client.items()[0][:len("openrouter/lab/native@")] != "openrouter/lab/native@" || len(management.items()) != 1 {
		t.Fatalf("the entry the stock route wrote: client %v management %v", client.items(), management.items())
	}

	// The account and no entry: a credential the gateway's loader did not make answers (rule A.3).
	memory, err := h.service.coreManager.Register(ca.WithSkipPersist(t.Context()), &ca.Auth{ID: "provimux-test-stock-memory", Provider: "xai", Status: ca.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "provimux-test-memory-key", "base_url": "https://api.x.ai/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	h.service.registerExecutorForAuth(memory, false)
	registry.GetGlobalRegistry().RegisterClient(memory.ID, "xai", []*registry.ModelInfo{{ID: "provimux-test-stock-memory-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(memory.ID) })
	ans = h.post("/v1/chat/completions", chatBody("provimux-test-stock-memory-model", false, ""), nil)
	if ans.status != 200 || ans.get(logging.ProvimuxAccountHeader) == "" || ans.get(logging.ProvimuxEntryHeader) != "" {
		t.Fatalf("fixture %d %v", ans.status, ans.header)
	}
	client, _, _ = h.listsRow(sentClientKey, "provimux-test-stock-memory-model", memory.ProvimuxAccountMarker())
	if client == nil || len(client.items()) != 1 || client.items()[0][:len("null@")] != "null@" {
		t.Fatalf("the account without an entry the stock route wrote: %v", client.items())
	}

	// The same handler served by another server: nothing.
	stock := h.server
	other := httptest.NewServer(h.handler)
	t.Cleanup(other.Close)
	h.server = other
	h.stand.reset()
	ans = h.post("/v1/chat/completions", chatBody("review-public", false, "")+" ", nil)
	h.server = stock
	if ans.status != 200 || ans.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
		t.Fatalf("fixture %d %v", ans.status, ans.header)
	}
	client, management, _ = h.listsRow(sentClientKey, "review-public", key.ProvimuxAccountMarker())
	if len(client.items()) != 1 || len(management.items()) != 1 {
		t.Fatalf("an answer another server wrote changed the record: client %v management %v", client.items(), management.items())
	}
	book := provimuxAnswered
	book.mu.Lock()
	seq := book.seq
	book.mu.Unlock()
	if seq != 2 {
		t.Fatalf("%d notes, want the two the stock route wrote", seq)
	}
}
