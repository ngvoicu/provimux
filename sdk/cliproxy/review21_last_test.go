package cliproxy

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestReview21SpeechNames(t *testing.T) {
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock())
	a := h.register(&ca.Auth{ID: "speech-fixture", FileName: "speech-fixture.json", Provider: "xai", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "base_url": "https://api.x.ai/v1", "api_key": "media-key"}}, "grok-tts", "tts-1")
	ans := h.post("/v1/audio/speech", `{"model":"tts-1","input":"fixture"}`, nil)
	if ans.status != 200 || ans.get(logging.ProvimuxAccountHeader) == "" {
		t.Fatal("fixture", ans.status, ans.body)
	}
	for _, name := range []string{"tts-1", "grok-tts"} {
		c, m, _ := h.listsRow(sentClientKey, name, a.ProvimuxAccountMarker())
		if c == nil || m == nil {
			t.Fatal("missing fixture row", name)
		}
		t.Logf("asked tts-1, row %s public=%v management=%v, entry on answer=%q", name, c.items(), m.items(), ans.get(logging.ProvimuxEntryHeader))
		if (name == "tts-1") != (len(c.items()) == 1) || (name == "tts-1") != (len(m.items()) == 1) {
			t.Error("note must land on the exact registered name the client asked for")
		}
	}
}

// Ported (finding 31; the record is one file): an item past 90 days is not shown, and is gone from the file at the
// next write (the documented promise: "not shown after 90 days, removed at the next write"). The next write here is
// the one a later answer brings.
func TestReview21StoredExpiry(t *testing.T) {
	now := newSentClock()
	dir := t.TempDir()
	b := newProvimuxAnsweredBook(now.Now)
	b.setPlace(dir)
	const k, cr, m = "k.client", "c.credential", "a.aaaaaaaaaaaaaaaaaaaa"
	b.note(k, cr, m, "old-name", "p/old-outcome")
	b.flush()
	now.add(89 * 24 * time.Hour)
	b.note(k, cr, m, "recent-name", "p/recent")
	b.flush()
	now.add(24*time.Hour + time.Nanosecond)
	if got := b.read(k, false, cr, m, "old-name"); len(got) != 0 {
		t.Fatal("old item on lists", got)
	}
	if got := b.read(k, false, cr, m, "recent-name"); len(got) != 1 {
		t.Fatal("lost live item", got)
	}
	now.add(time.Minute)
	b.note(k, cr, m, "recent-name", "p/recent")
	b.close()
	raw, err := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
	if err != nil {
		t.Fatal("fixture", err)
	}
	t.Logf("after the 90-day edge and the next write: the file holds old item=%v recent=%v", bytes.Contains(raw, []byte("p/old-outcome")), bytes.Contains(raw, []byte("p/recent")))
	if bytes.Contains(raw, []byte("p/old-outcome")) || !bytes.Contains(raw, []byte("p/recent")) {
		t.Error("the next write kept an expired item, or lost a live one")
	}
}

// Ported (the record is one file): a write that fails (the file's place is taken by a directory) is kept as pending
// and done at the next write once the place is usable; no note is lost.
func TestReview21WriteRecovery(t *testing.T) {
	now := newSentClock()
	dir := t.TempDir()
	blocked := filepath.Join(dir, provimuxAnsweredFile)
	b := newProvimuxAnsweredBook(now.Now)
	b.setPlace(dir)
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	const k, cr, m = "k.client", "c.credential", "a.aaaaaaaaaaaaaaaaaaaa"
	b.note(k, cr, m, "name", "p/entry")
	b.flush()
	if !b.logged.Load() {
		t.Fatal("fixture: the write did not fail")
	}
	if err := os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	now.add(time.Second)
	b.note(k, cr, m, "name", "p/entry")
	b.flush()
	after, err := os.ReadFile(blocked)
	if err != nil || !strings.Contains(string(after), "p/entry") {
		t.Fatal("graceful retry lost note", err)
	}
	t.Logf("same-day repeat after a failed write: persisted on the next flush=%v", len(after) > 0)
}

var _ = http.MethodGet
