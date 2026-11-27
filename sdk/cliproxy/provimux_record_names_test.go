package cliproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Provimux: finding 51. The record's file is read with its field names exactly as the record writes them, at every
// level (document, row, item): a name in another case (Go's decoder matched `Client` for `client`), a name written with
// an escape, a name twice, a field the record does not write. Any of them makes the whole file not the record's: not
// loaded, not shown in either list, never written over, a warning in the log. The same caller's genuine record, changed
// only so, is the fixture (at 494df49e3 the case changes and the duplicates were loaded, shown and written over).
func TestProvimuxRecordFieldNamesExact(t *testing.T) {
	duplicate := func(pattern string) func([]byte) []byte {
		re := regexp.MustCompile(pattern)
		return func(raw []byte) []byte {
			at := re.FindIndex(raw)
			if at == nil {
				return raw
			}
			pair := raw[at[0]:at[1]]
			return append(append(append(append([]byte{}, raw[:at[1]]...), ','), pair...), raw[at[1]:]...)
		}
	}
	rename := func(from, to string) func([]byte) []byte {
		return func(raw []byte) []byte { return bytes.Replace(raw, []byte(from), []byte(to), 1) }
	}
	cases := []struct {
		name   string
		change func([]byte) []byte // nil: the genuine file (the control)
	}{
		{"genuine", nil},
		{"document/Rows", rename(`"rows":`, `"Rows":`)},
		{"document/FORMAT", rename(`"format":`, `"FORMAT":`)},
		{"document/version-twice", duplicate(`"version":\s*1`)},
		{"row/Client", rename(`"client":`, `"Client":`)},
		{"row/escaped-client", rename(`"client":`, `"\u0063lient":`)},
		{"row/name-and-Name", func(raw []byte) []byte {
			pair := regexp.MustCompile(`"name":\s*"[^"]*"`).Find(raw)
			return bytes.Replace(raw, pair, append(append(append([]byte{}, pair...), ','), bytes.Replace(pair, []byte(`"name"`), []byte(`"Name"`), 1)...), 1)
		}},
		{"row/marker-twice", duplicate(`"marker":\s*"[^"]*"`)},
		{"item/Entry", rename(`"entry":`, `"Entry":`)},
		{"item/seq-twice", duplicate(`"seq":\s*[0-9]+`)},
		{"item/AT", rename(`"at":`, `"AT":`)},
		{"row/unknown-field", rename(`"client":`, `"unrecognized-fixture":true,"client":`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			clock := newSentClock()
			book := useAnsweredBook(t, clock)
			dir := t.TempDir()
			file := filepath.Join(dir, provimuxAnsweredFile)
			a := sentKeyFor(t, h, "review-public")
			credential, marker := coreauth.ProvimuxCredentialRef(a.ID), a.ProvimuxAccountMarker()
			writer := newProvimuxAnsweredBook(clock.Now)
			writer.setPlace(dir)
			writer.note(coreauth.ProvimuxClientRef(sentClientKey), credential, marker, "review-public", "openrouter/lab/native")
			writer.close()
			genuine, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			written := genuine
			if tc.change != nil {
				if written = tc.change(genuine); bytes.Equal(written, genuine) {
					t.Fatalf("fixture: the change did not apply to %s", genuine)
				}
				if err = os.WriteFile(file, written, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, hook := logtest.NewNullLogger()
			saved := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
			log.AddHook(hook)
			defer log.StandardLogger().ReplaceHooks(saved)

			book.setPlace(dir)
			client, management, raw := h.listsRow(sentClientKey, "review-public", marker)
			loaded := book.read(coreauth.ProvimuxClientRef(sentClientKey), false, credential, marker, "review-public")
			shown := strings.Contains(raw, "openrouter/lab/native") || len(provimuxItemsOf(client)) != 0 || len(provimuxItemsOf(management)) != 0
			book.note("provimux-test-other", "provimux-test-other", "provimux-test-other", "provimux-test-other", "provimux-test/other")
			book.close()
			final, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			warned := false
			for _, entry := range hook.AllEntries() {
				warned = warned || (entry.Level == log.WarnLevel && strings.Contains(entry.Message, "is left as it is"))
			}
			t.Logf("%s: loaded %v, shown %v, left as it is %v, warned %v", tc.name, loaded, shown, bytes.Equal(final, written), warned)
			if tc.change == nil {
				if len(loaded) != 1 || !shown || bytes.Equal(final, written) || warned {
					t.Errorf("the genuine file was not the record's own")
				}
				return
			}
			if len(loaded) != 0 || shown || !bytes.Equal(final, written) || !warned {
				t.Errorf("a file with a field name other than the record's was taken for the record's own")
			}
		})
	}
}
