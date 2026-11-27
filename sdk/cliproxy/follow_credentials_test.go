package cliproxy

import (
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sa "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"os"
	"path/filepath"
	"testing"
)

// followLegacyPluginLogin is a Meta login as a plugin's conversion and the file token store wrote it before the plugin
// mark existed (the reviewer generated it with the earlier code; the e-mail field is left out here): nothing in it says
// a plugin made it.
const followLegacyPluginLogin = `{"access_token":"fixture-access","api_key":"fixture-key","auth_kind":"oauth","disabled":false,"keep_setting":"fixture","type":"meta"}`

// Finding 36, a written limit (the amendment of 2026-11-27 (2)): such a file cannot be told from a native login by its
// content, so the gateway's loader loads it as a native login and its answer names the entry of what the built-in
// executor sent to the vendor's own host. Ported: the test pins that decided behaviour; it failed on the earlier head
// because it asked for none.
func TestFollowLegacyCredential(t *testing.T) {
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock())
	raw := []byte(followLegacyPluginLogin)
	d := t.TempDir()
	if e := os.WriteFile(filepath.Join(d, "legacy.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	s := sa.NewFileTokenStore()
	s.SetBaseDir(d)
	records, e := s.List(t.Context())
	if e != nil || len(records) != 1 {
		t.Fatal(e, len(records))
	}
	a := records[0]
	if !a.ProvimuxLoaded() {
		t.Fatal("the file store's loader did not mark what it loaded")
	}
	h.register(a, "follow-legacy")
	answer := h.post("/v1/chat/completions", chatBody("follow-legacy", false, ""), nil)
	if answer.status != 200 || len(h.stand.answered()) != 1 {
		t.Fatal("fixture", answer.status)
	}
	t.Logf("old plugin file mark=%v, native reload marker=%s, entry=%q", a.Metadata[ca.ProvimuxPluginCredentialMark], answer.get(logging.ProvimuxAccountHeader), answer.get(logging.ProvimuxEntryHeader))
	if answer.get(logging.ProvimuxEntryHeader) != "meta-subscription/follow-legacy" {
		t.Error("the written limit changed: a plugin's earlier file, loaded natively, no longer names what was sent")
	}
}

// Finding 37, unchanged: closed by rule A.3 (the loader's mark).
func TestFollowPositiveOrigin(t *testing.T) {
	for _, kind := range []string{"none", "filename-only", "arbitrary-source"} {
		t.Run(kind, func(t *testing.T) {
			h := newSentHarness(t, "")
			useAnsweredBook(t, newSentClock())
			auth := &ca.Auth{ID: "memory-sdk-row", Provider: "xai", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "api_key": "fixture-key", "base_url": "https://api.x.ai/v1"}}
			if kind == "filename-only" {
				auth.FileName = "display-only.json"
			}
			if kind == "arbitrary-source" {
				auth.Attributes["source"] = "embedded-runtime-registry"
			}
			a, e := h.service.coreManager.Register(ca.WithSkipPersist(t.Context()), auth)
			if e != nil {
				t.Fatal(e)
			}
			h.service.registerExecutorForAuth(a, false)
			registry.GetGlobalRegistry().RegisterClient(a.ID, "xai", []*registry.ModelInfo{{ID: "follow-memory"}})
			defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
			ans := h.post("/v1/chat/completions", chatBody("follow-memory", false, ""), nil)
			if ans.status != 200 || len(h.stand.answered()) != 1 {
				t.Fatal("fixture", ans.status)
			}
			if a.FileName != "" {
				if _, e := os.Stat(filepath.Join(filepath.Dir(h.configPath), "auths", a.FileName)); !os.IsNotExist(e) {
					t.Fatal("fixture unexpectedly persisted", e)
				}
			}
			t.Logf("%s no credential file; source=%q entry=%q", kind, a.AuthSourceKind(), ans.get(logging.ProvimuxEntryHeader))
			if ans.get(logging.ProvimuxEntryHeader) != "" {
				t.Error("registration held in memory was vouched for")
			}
		})
	}
}

// Finding 40, unchanged: closed by rule B.3 (a sequence of the record's own).
func TestFollowEqualClock(t *testing.T) {
	c := newSentClock()
	b := newProvimuxAnsweredBook(c.Now)
	b.setPlace(t.TempDir())
	for i := 1; i <= 5; i++ {
		b.note("k", "c", "m", "n", fmt.Sprintf("p/%d", i))
	}
	b.close()
	got := b.read("k", false, "c", "m", "n")
	t.Logf("five accepted in order at one UTC tick, retained=%v", got)
	if len(got) != 4 || got[0].Entry != "p/5" || got[3].Entry != "p/2" {
		t.Error("fifth (latest) answer was discarded at a tied clock tick")
	}
}
