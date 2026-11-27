package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"net/http"
	"net/http/httptest"
	"testing"
)

type followStripEntry struct{ http.ResponseWriter }

func (w followStripEntry) WriteHeader(n int) {
	w.Header().Del(logging.ProvimuxEntryHeader)
	w.ResponseWriter.WriteHeader(n)
}
func (w followStripEntry) Write(b []byte) (int, error) {
	w.Header().Del(logging.ProvimuxEntryHeader)
	return w.ResponseWriter.Write(b)
}

// Finding 38. Ported (rule B.1 of the amendment of 2026-11-27 (2)): a writer of an embedding wraps the gateway's
// handler and drops the entry at the commit; it is not the stock output route, so the record keeps nothing (the
// reviewer asked for a null item). That the stock route records what it committed is pinned in
// TestProvimuxAnswerRecordsWhatTheStockRouteCommitted.
func TestFollowCommittedFields(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	handler := h.handler
	filtered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(followStripEntry{w}, r) }))
	defer filtered.Close()
	h.server = filtered
	a := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if a.status != 200 || a.get(logging.ProvimuxAccountHeader) == "" || a.get(logging.ProvimuxEntryHeader) != "" {
		t.Fatal("fixture", a.status, a.header)
	}
	c, m, _ := h.listsRow(sentClientKey, "review-public", a.get(logging.ProvimuxAccountHeader))
	if c == nil || m == nil {
		t.Fatal("missing rows")
	}
	t.Logf("committed entry absent, account present; keyed row=%v management=%v", c.items(), m.items())
	if len(c.items()) != 0 || len(m.items()) != 0 {
		t.Error("a writer other than the stock output route put an item in the record")
	}
}
