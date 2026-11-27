package cliproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// The process mark (the lead's amendment of 2026-11-27 (2), part 3): the entry and the record exist only in a process
// Provimux's own main started. A program that embeds the SDK (here the same gateway, without the mark) answers with
// the account marker and none of the entry headers, records nothing, neither reads nor writes the record's file, and
// its lists carry no `answered`.
func TestProvimuxNoProcessMarkNoEntryNoRecord(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	book := useAnsweredBook(t, newSentClock())
	dir := t.TempDir()
	file := filepath.Join(dir, provimuxAnsweredFile)
	seed := newProvimuxAnsweredBook(newSentClock().Now)
	seed.setPlace(dir)
	seed.note("k", "c", "m", "n", "p/seed")
	seed.close()
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	asEmbedding(t)
	cfg := *h.service.currentConfig()
	cfg.AuthDir = dir
	h.service.applyKVStoreConfig(&cfg)
	book.mu.Lock()
	place, rows := book.place, len(book.rows)
	book.mu.Unlock()
	if place != "" || rows != 0 {
		t.Fatalf("the record took the place %q and %d rows", place, rows)
	}
	key := sentKeyFor(t, h, "review-public")
	ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if ans.status != 200 || len(h.stand.answered()) != 1 {
		t.Fatalf("fixture %d %s", ans.status, ans.body)
	}
	if ans.get(logging.ProvimuxAccountHeader) != key.ProvimuxAccountMarker() || key.ProvimuxAccountMarker() == "" {
		t.Fatalf("the account marker is not the process mark's: %q", ans.get(logging.ProvimuxAccountHeader))
	}
	for _, name := range logging.ProvimuxFieldNames()[1:] {
		if values := ans.header.Values(name.Header); len(values) != 0 {
			t.Fatalf("%s written without the process mark: %v", name.Header, values)
		}
	}
	book.mu.Lock()
	seq := book.seq
	book.mu.Unlock()
	if seq != 0 {
		t.Fatalf("%d notes without the process mark", seq)
	}
	_, _, raw := h.listsRow(sentClientKey, "review-public", "")
	if strings.Contains(raw, `"answered"`) || !strings.Contains(raw, `"account"`) {
		t.Fatalf("the lists without the process mark: %s", raw)
	}
	book.close()
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("the record's file was written without the process mark")
	}
}
