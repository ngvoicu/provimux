package cliproxy

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Finding 43, unchanged: closed by rule B.4 (write over a file only when it is recognisably the record's own and
// unchanged since the record last read or wrote it).
func TestFollowForeignAndPartialFile(t *testing.T) {
	for _, tc := range []struct {
		kind, contents string
		after          bool
	}{
		{"invalid", "another application's private fixture", false}, {"partial", `{"version":1,"rows":[`, false}, {"unknown-version", `{"version":2,"rows":[]}`, false}, {"foreign-version-one", `{"version":1,"application":"fixture-calendar","events":["retained"]}`, false}, {"foreign-after-start", "another application's private fixture", true}, {"partial-after-start", `{"version":1,"rows":[`, true}, {"damaged-rows", `{"version":1,"rows":[{"owner_setting":"retain"}]}`, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			c := newSentClock()
			b := newProvimuxAnsweredBook(c.Now)
			dir := t.TempDir()
			f := filepath.Join(dir, provimuxAnsweredFile)
			if tc.after {
				b.setPlace(dir)
			}
			if e := os.WriteFile(f, []byte(tc.contents), 0600); e != nil {
				t.Fatal(e)
			}
			if !tc.after {
				b.setPlace(dir)
			}
			b.note("client", "credential", "marker", "name", "p/mine")
			b.close()
			got, e := os.ReadFile(f)
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("foreign/partial file preserved=%v; record in memory=%d", string(got) == tc.contents, len(b.read("client", false, "credential", "marker", "name")))
			if string(got) != tc.contents {
				t.Error("an existing foreign or damaged file was overwritten")
			}
		})
	}
}

// Finding 41, first half, decided as intended (rule B.4 of the amendment of 2026-11-27 (2)): a note around a change of
// folder may be missed, and the documentation says so. Ported: the test pins what is decided: the move completes, the
// old file keeps what it had, the new place keeps its own and takes the next note; where the note made during the move
// went is logged, not asserted (the reviewer asked that it be kept).
func TestFollowReloadDuringNote(t *testing.T) {
	c := newSentClock()
	var gate atomic.Bool
	start := make(chan struct{})
	resume := make(chan struct{})
	b := newProvimuxAnsweredBook(func() time.Time {
		if gate.CompareAndSwap(true, false) {
			close(start)
			<-resume
		}
		return c.Now()
	})
	old, dest := t.TempDir(), t.TempDir()
	b.setPlace(old)
	b.note("k", "c", "m", "n", "p/before")
	b.flush()
	gate.Store(true)
	done := make(chan struct{})
	go func() { b.setPlace(dest); close(done) }()
	<-start
	b.note("k", "c", "m", "n", "p/during")
	accepted := b.read("k", false, "c", "m", "n")
	close(resume)
	<-done
	b.close()
	has := func(dir string) bool {
		x := newProvimuxAnsweredBook(c.Now)
		x.setPlace(dir)
		defer x.close() // Ported: a record holds its folder's lock until it stops
		for _, i := range x.read("k", false, "c", "m", "n") {
			if i.Entry == "p/during" {
				return true
			}
		}
		return false
	}
	v1, v2 := has(old), has(dest)
	t.Logf("accepted in memory=%v; after reopen old=%v new=%v", accepted, v1, v2)
	if len(accepted) != 2 {
		t.Fatal("fixture note not accepted")
	}
	reopen := func(dir string) []string {
		x := newProvimuxAnsweredBook(c.Now)
		x.setPlace(dir)
		defer x.close()
		var out []string
		for _, i := range x.read("k", false, "c", "m", "n") {
			out = append(out, i.Entry)
		}
		return out
	}
	if got := strings.Join(reopen(old), ","); got != "p/before" && got != "p/during,p/before" {
		t.Errorf("the old file holds %q", got)
	}
	next := newProvimuxAnsweredBook(c.Now)
	next.setPlace(dest)
	next.note("k", "c", "m", "n", "p/after-move")
	next.close()
	if got := reopen(dest); len(got) == 0 || got[0] != "p/after-move" {
		t.Errorf("the new place did not take the next note: %v", got)
	}
	t.Logf("the note made during the move: old=%v new=%v (may be missed, as decided)", v1, v2)
}

// The review's note, unchanged: with the switch off, or in Home mode, the file is not read at all.
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

type followProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr bytes.Buffer
	done   bool
}

func (p *followProc) until(t *testing.T, want string) {
	t.Helper()
	for p.stdout.Scan() {
		if strings.TrimSpace(p.stdout.Text()) == want {
			return
		}
	}
	t.Fatalf("child missing %s: %s", want, p.stderr.String())
}
func newFollowProc(t *testing.T, root, who string) *followProc {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	p := &followProc{cmd: exec.Command(exe, "-test.run=^TestFollowSharedFolder$", "-test.count=1")}
	p.cmd.Env = append(os.Environ(), "PMX_FOLLOW_CHILD="+who, "PMX_FOLLOW_FOLDER="+root)
	p.cmd.Stderr = &p.stderr
	out, e := p.cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	p.stdout = bufio.NewScanner(out)
	p.stdin, e = p.cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = p.cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if !p.done {
			p.stdin.Close()
			p.cmd.Process.Kill()
			p.cmd.Wait()
		}
	})
	p.until(t, "PMX_FOLLOW_READY")
	return p
}

// Finding 42. Ported (rule B.4 of the amendment of 2026-11-27 (2)): one gateway owns the file, by a lock held while it
// runs; a second on the same auth folder keeps its record in memory only, never writes the file, and says so once in
// its log. The reviewer asked that both records be in the file; the test pins the decided behaviour: the owner's
// record is in it whole, the second's is not, and the second said why.
func TestFollowSharedFolder(t *testing.T) {
	if who := os.Getenv("PMX_FOLLOW_CHILD"); who != "" {
		h := newSentHarness(t, reviewRouter)
		b := useAnsweredBook(t, newSentClock())
		cfg := *h.service.currentConfig()
		cfg.AuthDir = os.Getenv("PMX_FOLLOW_FOLDER")
		cfg.KVStore.Disabled = true
		h.service.applyKVStoreConfig(&cfg)
		ca.SetProvimuxMarkerKey([]byte("provimux-test-sent-marker-key-32"))
		fmt.Println("PMX_FOLLOW_READY")
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			switch scanner.Text() {
			case "answer":
				key := sentClientKey
				if who == "B" {
					key = sentOtherClientKey
				}
				ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), sentHeader("Authorization", "Bearer "+key))
				if ans.status != 200 || ans.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
					t.Fatal("gateway fixture", ans.status, ans.header)
				}
				b.flush()
				fmt.Println("PMX_FOLLOW_WROTE")
			case "stop":
				h.service.shutdownKVStore()
				return
			}
		}
		return
	}
	dir := t.TempDir()
	a := newFollowProc(t, dir, "A")
	b := newFollowProc(t, dir, "B")
	for _, p := range []*followProc{a, b} {
		fmt.Fprintln(p.stdin, "answer")
		p.until(t, "PMX_FOLLOW_WROTE")
	}
	for _, p := range []*followProc{a, b} {
		fmt.Fprintln(p.stdin, "stop")
		p.stdin.Close()
		if e := p.cmd.Wait(); e != nil {
			t.Fatal(e, p.stderr.String())
		}
		p.done = true
	}
	raw, e := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
	if e != nil {
		t.Fatal(e)
	}
	answeredKeyOn(t) // the children's markers' key, for the client references below
	n := bytes.Count(raw, []byte(`"client":`))
	owner, second := bytes.Contains(raw, []byte(ca.ProvimuxClientRef(sentClientKey))), bytes.Contains(raw, []byte(ca.ProvimuxClientRef(sentOtherClientKey)))
	said := strings.Count(b.stderr.String(), "another gateway keeps the record")
	t.Logf("two independent gateway processes, both HTTP 200, client records in the file=%d (owner %v, second %v); the second said so %d times", n, owner, second, said)
	if n != 1 || !owner || second {
		t.Error("the file is not the owner's record alone")
	}
	if said != 1 {
		t.Error("the second gateway did not say once that it keeps its record in memory only")
	}
}
