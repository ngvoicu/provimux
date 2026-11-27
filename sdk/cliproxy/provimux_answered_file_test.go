package cliproxy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

// Provimux: the record's file (the lead's amendment of 2026-11-27 (2), rule B.4). One gateway owns it, by a lock held
// while it runs; another keeps its record in memory only, never reads or writes the file, and says so once. The owner
// writes over a file only when it is recognisably the record's own and unchanged since the owner last read or wrote
// it. The stop is final.

func answeredLogs(t *testing.T) *test.Hook {
	t.Helper()
	hook := test.NewLocal(logrus.StandardLogger())
	t.Cleanup(hook.Reset)
	return hook
}

func answeredSaid(hook *test.Hook, words string) int {
	n := 0
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, words) {
			n++
		}
	}
	return n
}

func TestProvimuxAnsweredOneOwnerPerFolder(t *testing.T) {
	hook := answeredLogs(t)
	clock := newSentClock()
	dir := t.TempDir()
	file := filepath.Join(dir, provimuxAnsweredFile)
	owner, second := newProvimuxAnsweredBook(clock.Now), newProvimuxAnsweredBook(clock.Now)
	owner.setPlace(dir)
	owner.note("k", "c", "m", "n", "p/owner")
	owner.flush()
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	second.setPlace(dir)
	if got := second.read("k", false, "c", "m", "n"); len(got) != 0 {
		t.Fatalf("the second gateway read the owner's file: %v", got)
	}
	for i := 0; i < 3; i++ {
		second.note("k", "c", "m", "n", "p/second")
		second.flush()
	}
	if got := second.read("k", false, "c", "m", "n"); len(got) != 1 || got[0].Entry != "p/second" {
		t.Fatalf("the second gateway's own record in memory: %v", got)
	}
	second.close()
	if now, _ := os.ReadFile(file); !bytes.Equal(now, written) {
		t.Fatalf("the second gateway wrote the file: %s", now)
	}
	if said := answeredSaid(hook, "another gateway keeps the record"); said != 1 {
		t.Fatalf("the second gateway said so %d times", said)
	}
	owner.note("k", "c", "m", "n", "p/owner-later")
	owner.close()
	reopened := newProvimuxAnsweredBook(clock.Now)
	reopened.setPlace(dir) // the owner stopped: its lock is free
	defer reopened.close()
	if got := reopened.read("k", false, "c", "m", "n"); len(got) != 2 || got[0].Entry != "p/owner-later" {
		t.Fatalf("after the owner stopped, its file: %v", got)
	}
}

// A file that is the record's own in form, but not the one the owner last read or wrote (changed underneath: another
// copy of a gateway, a restore), is left as it is; the record stays in memory and the log says so once.
func TestProvimuxAnsweredChangedUnderneath(t *testing.T) {
	hook := answeredLogs(t)
	clock := newSentClock()
	dir := t.TempDir()
	file := filepath.Join(dir, provimuxAnsweredFile)
	book := newProvimuxAnsweredBook(clock.Now)
	book.setPlace(dir)
	book.note("k", "c", "m", "n", "p/first")
	book.flush()
	mine, _ := os.ReadFile(file)
	other := bytes.Replace(mine, []byte("p/first"), []byte("p/other"), 1)
	if bytes.Equal(other, mine) {
		t.Fatal("fixture")
	}
	if _, ok := provimuxAnsweredParse(other); !ok {
		t.Fatal("fixture: the changed file is in the record's own form")
	}
	if err := os.WriteFile(file, other, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		book.note("k", "c", "m", "n", "p/second")
		book.flush()
	}
	book.close()
	if now, _ := os.ReadFile(file); !bytes.Equal(now, other) {
		t.Fatalf("a file changed underneath was written over: %s", now)
	}
	if got := book.read("k", false, "c", "m", "n"); len(got) != 2 || got[0].Entry != "p/second" {
		t.Fatalf("the record in memory: %v", got)
	}
	if said := answeredSaid(hook, "is left as it is"); said != 1 {
		t.Fatalf("said so %d times", said)
	}
}

// The stop is final: no note is accepted after it, and a change of place after it changes nothing.
func TestProvimuxAnsweredStopIsFinal(t *testing.T) {
	clock := newSentClock()
	first, second := t.TempDir(), t.TempDir()
	book := newProvimuxAnsweredBook(clock.Now)
	book.setPlace(first)
	book.note("k", "c", "m", "n", "p/before")
	book.close()
	book.setPlace(second)
	book.note("k", "c", "m", "n", "p/after")
	book.flush()
	if got := book.read("k", false, "c", "m", "n"); len(got) != 1 || got[0].Entry != "p/before" {
		t.Fatalf("after the stop: %v", got)
	}
	if _, err := os.Stat(filepath.Join(second, provimuxAnsweredFile)); !os.IsNotExist(err) {
		t.Fatalf("a place set after the stop was written: %v", err)
	}
}
