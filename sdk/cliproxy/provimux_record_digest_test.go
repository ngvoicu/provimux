package cliproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: finding 56 and part (6). The record's file is its own only when the SHA-256 on its first line matches every
// byte after that line; only then is the content parsed, strictly, and its bytes must be valid UTF-8. Anything else is
// not loaded, not shown, never written over or removed, and the log says so once.

// provimuxDigestEntries are entries of every character class the record can hold: what the writer writes reads back.
var provimuxDigestEntries = []string{
	"openrouter/lab/native",
	"claude-subscription/claude-opus-5-5",
	`quote"back\slash/slash`,
	"control\x00\x01\x1f\x7f-tab\t-newline\n-return\r",
	"html<script>&amp;'",
	"line\u2028para\u2029sep",
	"accents-éàü-ß-ø",
	"cjk-模型-モデル-모델",
	"emoji-🧪-\U0001F600-flag-🇷🇴",
	"combining-e\u0301-zwj-\u200d-bom-\ufeff",
	"replacement-\ufffd",
	"noncharacter-\ufdd0-\U0010ffff",
	"", // an answer that named none: null
}

// provimuxDigestFile writes a real record file with several rows and returns its bytes.
func provimuxDigestFile(t *testing.T, dir string) []byte {
	t.Helper()
	clock := newSentClock()
	writer := newProvimuxAnsweredBook(clock.Now)
	writer.setPlace(dir)
	writer.note("client-a", "credential-a", "a.marker", "review-public", "openrouter/lab/native")
	writer.note("", "credential-a", "a.marker", "review-public", "")
	writer.note("client-b", "credential-b", "s.marker", "lab/other", "openrouter/lab/other")
	writer.close()
	raw, err := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, errDecode := provimuxAnsweredDecode(raw); errDecode != nil {
		t.Fatalf("fixture: the writer's own file is refused: %v", errDecode)
	}
	return raw
}

// Every single-byte change of a real file at every offset (every other value of every byte), a truncation at every
// length, and bytes appended: none is the record's own.
func TestProvimuxRecordDigestRefusesEveryChange(t *testing.T) {
	raw := provimuxDigestFile(t, t.TempDir())
	changed := append([]byte{}, raw...)
	accepted := 0
	for i := range raw {
		for v := 0; v < 256; v++ {
			if byte(v) == raw[i] {
				continue
			}
			changed[i] = byte(v)
			if _, err := provimuxAnsweredDecode(changed); err == nil {
				accepted++
				t.Errorf("offset %d, byte %#x for %#x: accepted", i, v, raw[i])
			}
		}
		changed[i] = raw[i]
	}
	for n := 0; n < len(raw); n++ {
		if _, err := provimuxAnsweredDecode(raw[:n]); err == nil {
			t.Errorf("truncated to %d bytes: accepted", n)
		}
	}
	for _, tail := range []string{"\n", " ", "\x00", "{}", "\xff", string(raw)} {
		if _, err := provimuxAnsweredDecode(append(append([]byte{}, raw...), tail...)); err == nil {
			t.Errorf("%q appended: accepted", tail)
		}
	}
	t.Logf("a file of %d bytes: %d single-byte changes, %d truncations, 6 appendings; accepted %d", len(raw), len(raw)*255, len(raw), accepted)
}

// The same, through the record itself: a file changed at any offset, or cut at any length, is not loaded and never
// written over, though answers are noted and written; the log says so once per file.
func TestProvimuxRecordDigestDamagedFileLeftAsItIs(t *testing.T) {
	hook := answeredLogs(t)
	raw := provimuxDigestFile(t, t.TempDir())
	try := func(what string, damaged []byte) {
		dir := t.TempDir()
		file := filepath.Join(dir, provimuxAnsweredFile)
		if err := os.WriteFile(file, damaged, 0o600); err != nil {
			t.Fatal(err)
		}
		hook.Reset()
		book := newProvimuxAnsweredBook(newSentClock().Now)
		book.setPlace(dir)
		loaded := book.read("client-a", false, "credential-a", "a.marker", "review-public")
		for i := 0; i < 3; i++ {
			book.note("client-a", "credential-a", "a.marker", "review-public", "provimux-test/after")
			book.note("client-a", "credential-a", "a.marker", "review-public", "provimux-test/again")
			book.flush()
		}
		book.close()
		now, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: the file is gone: %v", what, err)
		}
		if len(loaded) != 0 || !bytes.Equal(now, damaged) || answeredSaid(hook, "is left as it is") != 1 {
			t.Fatalf("%s: loaded %v, left as it is %v, said %d times", what, loaded, bytes.Equal(now, damaged), answeredSaid(hook, "is left as it is"))
		}
	}
	for i := range raw {
		damaged := append([]byte{}, raw...)
		damaged[i] ^= 0xff
		try("byte "+string(rune('0'+i%10)), damaged)
	}
	for n := 0; n < len(raw); n++ {
		try("truncated", raw[:n])
	}
	try("appended", append(append([]byte{}, raw...), '\n'))
	t.Logf("%d flipped bytes, %d truncations, 1 appending: none loaded, none written over", len(raw), len(raw))
}

// The review's two cases of finding 56, through the gateway's lists: a raw 0xff byte in the entry of the only row, and
// in a second row beside an intact one. Under the old digest the file is refused before parsing; sealed again (a file
// made by hand), its invalid UTF-8 is refused. Neither list shows anything of it, and it is never written over.
func TestProvimuxRecordInvalidUTF8(t *testing.T) {
	for _, mode := range []string{"digest", "resealed"} {
		for _, where := range []string{"only-row", "second-row"} {
			t.Run(mode+"/"+where, func(t *testing.T) {
				h := newSentHarness(t, reviewRouter)
				clock := newSentClock()
				book := useAnsweredBook(t, clock)
				dir := t.TempDir()
				file := filepath.Join(dir, provimuxAnsweredFile)
				a := sentKeyFor(t, h, "review-public")
				credential, marker := coreauth.ProvimuxCredentialRef(a.ID), a.ProvimuxAccountMarker()
				writer := newProvimuxAnsweredBook(clock.Now)
				writer.setPlace(dir)
				writer.note(coreauth.ProvimuxClientRef(sentClientKey), credential, marker, "review-public", "openrouter/lab/direct")
				if where == "second-row" {
					writer.note(coreauth.ProvimuxClientRef(sentClientKey), "provimux-test-second", "s.provimux-test", "lab/second", "openrouter/lab/second")
				}
				writer.close()
				genuine, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				target := "openrouter/lab/direct"
				if where == "second-row" {
					target = "openrouter/lab/second"
				}
				line, content, _ := bytes.Cut(genuine, []byte{'\n'})
				damagedContent := bytes.Replace(content, []byte(target), []byte(target[:len(target)-1]+"\xff"), 1)
				if bytes.Equal(damagedContent, content) {
					t.Fatal("fixture: no change")
				}
				damaged := append(append(append([]byte{}, line...), '\n'), damagedContent...)
				if mode == "resealed" {
					damaged = answeredSeal(damagedContent)
					if _, errDecode := provimuxAnsweredDecode(damaged); errDecode == nil || !strings.Contains(errDecode.Error(), "UTF-8") {
						t.Fatalf("fixture: not refused for its UTF-8: %v", errDecode)
					}
				}
				if err = os.WriteFile(file, damaged, 0o600); err != nil {
					t.Fatal(err)
				}
				book.setPlace(dir)
				client, management, raw := h.listsRow(sentClientKey, "review-public", marker)
				shown := strings.Contains(raw, "openrouter/lab/") || len(provimuxItemsOf(client)) != 0 || len(provimuxItemsOf(management)) != 0
				clock.add(time.Minute)
				answer := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
				if answer.status != 200 {
					t.Fatalf("fixture: %d", answer.status)
				}
				book.close()
				now, _ := os.ReadFile(file)
				t.Logf("%s/%s: shown %v, left as it is %v", mode, where, shown, bytes.Equal(now, damaged))
				if shown || !bytes.Equal(now, damaged) {
					t.Error("a file with a raw 0xff byte was shown or written over")
				}
			})
		}
	}
}

// What the writer writes always reads back: entries of every character class, rows of several clients, and a value
// that is not valid UTF-8 kept as the file holds it (the record keeps what it can give back).
func TestProvimuxRecordRoundTrip(t *testing.T) {
	clock := newSentClock()
	dir := t.TempDir()
	writer := newProvimuxAnsweredBook(clock.Now)
	writer.setPlace(dir)
	for i, entry := range provimuxDigestEntries {
		writer.note("client-"+entry, "credential", "a.marker", "name-"+entry, entry)
		clock.add(time.Duration(i) * time.Second)
	}
	writer.note("client", "credential", "a.marker", "invalid", "bad-\xff-\xc3")
	before := map[string][]coreauth.ProvimuxAnsweredItem{}
	for _, entry := range append(append([]string{}, provimuxDigestEntries...), "invalid") {
		client, name := "client-"+entry, "name-"+entry
		if entry == "invalid" {
			client, name = "client", "invalid"
		}
		before[entry] = writer.read(client, false, "credential", "a.marker", name)
	}
	writer.close()
	raw, err := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, errDecode := provimuxAnsweredDecode(raw); errDecode != nil {
		t.Fatalf("the writer's own file is refused: %v", errDecode)
	}
	reader := newProvimuxAnsweredBook(clock.Now)
	reader.setPlace(dir)
	defer reader.close()
	for _, entry := range append(append([]string{}, provimuxDigestEntries...), "invalid") {
		client, name := "client-"+entry, "name-"+entry
		if entry == "invalid" {
			client, name = "client", "invalid"
		}
		got := reader.read(client, false, "credential", "a.marker", name)
		want := before[entry]
		if len(got) != 1 || len(want) != 1 || got[0].Entry != want[0].Entry || !got[0].At.Equal(want[0].At) {
			t.Errorf("%q: wrote %v, read back %v", entry, want, got)
		}
		if entry != "invalid" && (len(got) != 1 || got[0].Entry != entry) {
			t.Errorf("%q: read back %v", entry, got)
		}
	}
	if got := reader.read("client", false, "credential", "a.marker", "invalid"); len(got) != 1 || got[0].Entry != "bad-\ufffd-\ufffd" {
		t.Errorf("a value not valid UTF-8: %v", got)
	}
}

// The earlier form (no digest, written only by unreleased builds) is not the record's own and is left alone. A file
// that is not the record's own is never removed; once removed by hand, the record writes a new one of its own.
func TestProvimuxRecordEarlierFormAndRemoval(t *testing.T) {
	hook := answeredLogs(t)
	clock := newSentClock()
	dir := t.TempDir()
	file := filepath.Join(dir, provimuxAnsweredFile)
	earlier := []byte(`{"format":"provimux answered-by record","version":1,"rows":[{"credential":"c","marker":"m","name":"n","client":"k","items":[{"entry":"p/earlier","at":"` + clock.Now().Format(time.RFC3339Nano) + `","seq":1}]}]}`)
	if err := os.WriteFile(file, earlier, 0o600); err != nil {
		t.Fatal(err)
	}
	book := newProvimuxAnsweredBook(clock.Now)
	book.setPlace(dir)
	defer book.close()
	if got := book.read("k", false, "c", "m", "n"); len(got) != 0 {
		t.Fatalf("the earlier form was loaded: %v", got)
	}
	for i := 0; i < 3; i++ {
		book.note("k", "c", "m", "n", "p/now")
		book.note("k", "c", "m", "n", "p/again")
		book.flush()
	}
	if now, _ := os.ReadFile(file); !bytes.Equal(now, earlier) {
		t.Fatalf("the earlier form was written over: %s", now)
	}
	if said := answeredSaid(hook, "is left as it is"); said != 1 || answeredSaid(hook, "earlier form") != 1 {
		t.Fatalf("said so %d times", said)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	book.note("k", "c", "m", "n", "p/after-removal")
	book.flush()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("no new file after the old one was removed: %v", err)
	}
	stored, errDecode := provimuxAnsweredDecode(raw)
	if errDecode != nil || len(stored.Rows) != 1 || len(stored.Rows[0].Items) != 3 {
		t.Fatalf("the new file: %v %+v", errDecode, stored)
	}
	t.Logf("the new file's first line: %s", bytes.SplitN(raw, []byte{'\n'}, 2)[0])
}
