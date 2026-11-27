package cliproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// Finding 41, first half, through the service's reload, decided as intended (rule B.4 of the amendment of 2026-11-27
// (2)): an answer around a change of folder is written to its client and shown on the list at once; whether the file
// of either folder keeps it is not promised, and the documentation says so. Ported: the test pins the answer, the
// list, and that the reload completes with both folders' files left readable; the files' content is logged (the
// reviewer asked that one of them keep it).
func TestFollowReloadWithCommittedHTTP(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	c := newSentClock()
	var armed atomic.Bool
	loading, resume := make(chan struct{}), make(chan struct{})
	book := newProvimuxAnsweredBook(func() time.Time {
		if armed.CompareAndSwap(true, false) {
			close(loading)
			<-resume
		}
		return c.Now()
	})
	prior := provimuxAnswered
	provimuxAnswered = book
	t.Cleanup(func() { provimuxAnswered = prior })
	cfg := *h.service.currentConfig()
	cfg.KVStore.Disabled = false
	cfg.KVStore.Path = filepath.Join(t.TempDir(), "persistent.db")
	old, destination := t.TempDir(), t.TempDir()
	cfg.AuthDir = old
	h.service.applyKVStoreConfig(&cfg)
	book.note("fixture-key", "credential", "account", "other", "p/already-present")
	book.flush()
	next := cfg
	next.AuthDir = destination
	armed.Store(true)
	moved := make(chan struct{})
	go func() { h.service.applyKVStoreConfig(&next); close(moved) }()
	<-loading
	a := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	entry := a.get(logging.ProvimuxEntryHeader)
	client, _, _ := h.listsRow(sentClientKey, "review-public", a.get(logging.ProvimuxAccountHeader))
	close(resume)
	<-moved
	if err := h.service.shutdownKVStore(); err != nil {
		t.Fatal(err)
	}
	if a.status != 200 || entry != "openrouter/lab/native" || client == nil || len(client.items()) != 1 {
		t.Fatal("fixture did not publish and record the original answer", a.status, entry)
	}
	holds := func(dir string) bool {
		b, err := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return bytes.Contains(b, []byte(entry))
	}
	inOld, inNew := holds(old), holds(destination)
	t.Logf("HTTP=%d committed=%q initial public row=%v retained old=%v new=%v (may be missed, as decided)", a.status, entry, client.items(), inOld, inNew)
	for _, dir := range []string{old, destination} {
		raw, err := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
		if err == nil {
			if _, errDecode := provimuxAnsweredDecode(raw); errDecode != nil {
				t.Errorf("the record's file in %s is not readable after the reload", dir)
			}
		}
	}
}
