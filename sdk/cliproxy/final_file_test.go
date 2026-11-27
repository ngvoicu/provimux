package cliproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFinalIncompleteRecord(t *testing.T) {
	for _, field := range []string{"client", "entry", "unknown"} {
		t.Run(field, func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			clock := newSentClock()
			book := useAnsweredBook(t, clock)
			dir := t.TempDir()
			f := filepath.Join(dir, provimuxAnsweredFile)
			a := sentKeyFor(t, h, "review-public")
			credential, marker := ca.ProvimuxCredentialRef(a.ID), a.ProvimuxAccountMarker()
			writer := newProvimuxAnsweredBook(clock.Now)
			writer.setPlace(dir)
			writer.note(ca.ProvimuxClientRef(sentClientKey), credential, marker, "review-public", "openrouter/lab/native")
			writer.close()
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			row := data["rows"].([]any)[0].(map[string]any)
			switch field {
			case "client":
				delete(row, "client")
			case "entry":
				delete(row["items"].([]any)[0].(map[string]any), "entry")
			default:
				row["unexpected"] = "fixture"
			}
			damaged, err := json.MarshalIndent(data, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(f, damaged, 0600); err != nil {
				t.Fatal(err)
			}
			book.setPlace(dir)
			who := sentClientKey
			if field == "client" {
				followConfigureKeys(h)
				who = ""
			}
			status, body, _ := followCall(t, h, "GET", "/v1/models?include=accounts", "", who, "")
			if status != 200 {
				t.Fatal("list fixture", status)
			}
			existing := book.read(ca.ProvimuxClientRef(who), false, credential, marker, "review-public")
			leaked := bytes.Contains(body, []byte("openrouter/lab/native"))
			book.note("other", "other", "different", "new", "different/entry")
			book.close()
			final, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("damaged field=%s public list has original price=%v loaded outcomes=%v original file preserved=%v", field, leaked, existing, bytes.Equal(damaged, final))
			if len(existing) != 0 || leaked || !bytes.Equal(damaged, final) {
				t.Error("a damaged file supplied a row or was overwritten")
			}
		})
	}
}

func TestFinalDisablePendingWrite(t *testing.T) {
	for _, mode := range []string{"off", "home"} {
		t.Run(mode, func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			c := newSentClock()
			b := useAnsweredBook(t, c)
			dir := t.TempDir()
			b.setPlace(dir)
			b.note("k", "cred", "m", "n", "p/seen")
			b.flush()
			c.add(time.Minute)
			b.note("k", "cred", "m", "n", "p/seen")
			file := filepath.Join(dir, provimuxAnsweredFile)
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			// A third party updates this fixture while the gateway has a same-day in-memory timestamp.
			replacement := bytes.Replace(original, []byte("p/seen"), []byte("p/edit"), 1)
			if bytes.Equal(replacement, original) {
				t.Fatal("incorrect fixture")
			}
			if err = os.WriteFile(file, replacement, 0600); err != nil {
				t.Fatal(err)
			}
			cfg := *h.service.currentConfig()
			cfg.AuthDir = dir
			cfg.KVStore.Disabled = true
			enabled := mode == "home"
			cfg.Models.AnsweredBy = &enabled
			cfg.Home.Enabled = enabled
			provimuxSetAnsweredBy(&cfg)
			if logging.ProvimuxAnsweredBy() {
				t.Fatal("feature should be off")
			}
			h.service.applyKVStoreConfig(&cfg)
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			// This flag is raised only when the writer has read and compared the old file.
			read := b.foreignLog.Load()
			t.Logf("mode=%s, stale file detected after feature disabled=%v, foreign bytes preserved=%v", mode, read, bytes.Equal(after, replacement))
			b.close()
			if read {
				t.Error(fmt.Sprintf("history file was read on the transition to %s", mode))
			}
		})
	}
}
