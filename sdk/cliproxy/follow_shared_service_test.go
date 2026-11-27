package cliproxy

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func followKVProcess(t *testing.T, dir, who string) *followProc {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &followProc{cmd: exec.Command(exe, "-test.run=^TestFollowSharedServiceFolder$", "-test.count=1")}
	p.cmd.Env = append(os.Environ(), "PMX_FOLLOW_KV_WHO="+who, "PMX_FOLLOW_KV_DIR="+dir)
	p.cmd.Stderr = &p.stderr
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.stdout = bufio.NewScanner(out)
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !p.done {
			p.stdin.Close()
			p.cmd.Process.Kill()
			p.cmd.Wait()
		}
	})
	p.until(t, "KV_READY")
	return p
}

// Finding 42, with each gateway's own key-value store. Ported (rule B.4 of the amendment of 2026-11-27 (2)): the first
// gateway owns the record's file; the second keeps its record in memory only and never writes the file. After both
// restart, the owner's client still sees its answer; the second's record was this run's only, as decided (the reviewer
// asked that both be kept).
func TestFollowSharedServiceFolder(t *testing.T) {
	if who := os.Getenv("PMX_FOLLOW_KV_WHO"); who != "" {
		h := newSentHarness(t, reviewRouter)
		useAnsweredBook(t, newSentClock())
		cfg := *h.service.currentConfig()
		cfg.AuthDir = os.Getenv("PMX_FOLLOW_KV_DIR")
		cfg.KVStore.Disabled = false
		cfg.KVStore.Path = filepath.Join(cfg.AuthDir, "local-"+who+".db")
		h.service.applyKVStoreConfig(&cfg)
		key := sentClientKey
		if who == "B" {
			key = sentOtherClientKey
		}
		fmt.Println("KV_READY")
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			switch s.Text() {
			case "write":
				a := h.post("/v1/chat/completions", chatBody("review-public", false, ""), sentHeader("Authorization", "Bearer "+key))
				if a.status != 200 || a.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
					t.Fatal("failed model call", a.status)
				}
				if who == "A" {
					limit := time.Now().Add(4 * time.Second)
					for {
						raw, _ := os.ReadFile(filepath.Join(cfg.AuthDir, provimuxAnsweredFile))
						if bytes.Contains(raw, []byte(ca.ProvimuxClientRef(key))) {
							break
						}
						if time.Now().After(limit) {
							t.Fatal("asynchronous writer did not persist its own answer")
						}
						time.Sleep(time.Millisecond)
					}
				}
				fmt.Println("KV_WRITTEN")
			case "read":
				a := sentKeyFor(t, h, "review-public")
				c, _, _ := h.listsRow(key, "review-public", a.ProvimuxAccountMarker())
				if c == nil {
					t.Fatal("missing route")
				}
				fmt.Printf("KV_HISTORY %d\n", len(c.items()))
			case "stop":
				if err := h.service.shutdownKVStore(); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		return
	}
	stop := func(p *followProc) {
		fmt.Fprintln(p.stdin, "stop")
		p.stdin.Close()
		if err := p.cmd.Wait(); err != nil {
			t.Fatal(err, p.stderr.String())
		}
		p.done = true
	}
	dir := t.TempDir()
	a, b := followKVProcess(t, dir, "A"), followKVProcess(t, dir, "B")
	for _, p := range []*followProc{a, b} {
		fmt.Fprintln(p.stdin, "write")
		p.until(t, "KV_WRITTEN")
	}
	stop(a)
	stop(b)
	counts := map[string]string{}
	for _, who := range []string{"A", "B"} {
		p := followKVProcess(t, dir, who)
		fmt.Fprintln(p.stdin, "read")
		for p.stdout.Scan() {
			line := p.stdout.Text()
			if strings.HasPrefix(line, "KV_HISTORY ") {
				counts[who] = strings.TrimPrefix(line, "KV_HISTORY ")
				break
			}
		}
		stop(p)
	}
	t.Logf("both stock-config keys answered 200; separate persistent marker databases, shared auth folder; public history after restart A=%s B=%s", counts["A"], counts["B"])
	if counts["A"] != "1" || counts["B"] != "0" {
		t.Error("the owner's record was not kept, or the second gateway wrote the file")
	}
}
