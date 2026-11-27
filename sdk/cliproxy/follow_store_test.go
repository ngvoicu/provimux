package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"testing"
	"time"
)

// With the switch off, or in Home mode, the file is not read at all.
func TestFollowRecordOff(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	b := useAnsweredBook(t, newSentClock())
	dir := t.TempDir()
	seed := newProvimuxAnsweredBook(time.Now)
	seed.setPlace(dir)
	seed.note("client", "credential", "marker", "name", "p/seed")
	seed.close()
	off := false
	cfg := *h.service.currentConfig()
	cfg.Models.AnsweredBy = &off
	cfg.AuthDir = dir
	cfg.KVStore.Disabled = true
	for _, home := range []bool{false, true} {
		cfg.Home.Enabled = home
		provimuxSetAnsweredBy(&cfg)
		if logging.ProvimuxAnsweredBy() {
			t.Fatal("fixture switch")
		}
		h.service.applyKVStoreConfig(&cfg)
		b.mu.Lock()
		loaded := len(b.rows)
		b.mu.Unlock()
		t.Logf("switch off home=%v loaded disk rows=%d", home, loaded)
		if loaded > 0 {
			t.Error("disabled feature read the record on config application")
		}
		b.setPlace("")
	}
}
