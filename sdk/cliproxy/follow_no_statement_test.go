package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

type followOmitStatementWriter struct{ http.ResponseWriter }

func (w followOmitStatementWriter) omitStatement() {
	for name := range w.Header() {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Provimux-") {
			w.Header().Del(name)
		}
	}
}

func (w followOmitStatementWriter) WriteHeader(status int) {
	w.omitStatement()
	w.ResponseWriter.WriteHeader(status)
}

func (w followOmitStatementWriter) Write(data []byte) (int, error) {
	w.omitStatement()
	return w.ResponseWriter.Write(data)
}

// Finding 38, second writer, unchanged: closed by rule B.1.
func TestFollowCommittedNoStatement(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	gateway := h.handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.ServeHTTP(followOmitStatementWriter{w}, r)
	}))
	defer server.Close()
	h.server = server

	a := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if a.status != 200 || a.get(logging.ProvimuxAccountHeader) != "" || a.get(logging.ProvimuxEntryHeader) != "" {
		t.Fatal("unexpected response", a.status, a.header)
	}

	credential := sentKeyFor(t, h, "review-public")
	c, m, _ := h.listsRow(sentClientKey, "review-public", credential.ProvimuxAccountMarker())
	if c == nil || m == nil {
		t.Fatal("fixture missing rows")
	}
	t.Logf("no committed statement; client=%v management=%v", c.items(), m.items())
	if len(c.items()) != 0 || len(m.items()) != 0 {
		t.Error("an unissued statement created history")
	}
}
