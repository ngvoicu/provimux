package cliproxy

// The fourth review of routing.session-affinity-persist: a blocked state logged once (finding 2), the lock after the
// store's path moves (finding 3), a login whose identity appears after the start (finding 4), what a write cut short
// left (note 6). Each of these tests fails on the commit before the mend.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// pmxAffinityLines counts the lines of this feature at level (exactly).
func pmxAffinityLines(hook *logtest.Hook, level log.Level) int {
	n := 0
	for _, entry := range hook.AllEntries() {
		if entry.Level == level && strings.HasPrefix(entry.Message, "session affinity:") {
			n++
		}
	}
	return n
}

// pmxBindOn binds session to target, of two auths: a probe session is bound first until one lands on the other auth,
// so that the next new binding goes to target (the fallback takes turns).
func pmxBindOn(t *testing.T, s *Service, session string, target *coreauth.Auth, auths ...*coreauth.Auth) {
	t.Helper()
	for i := 0; i < 4; i++ {
		probe := session + "-probe-" + string(rune('a'+i))
		if got := pmxBind(t, s, probe, auths...); got == target.ID {
			continue
		}
		if got := pmxBind(t, s, session, auths...); got != target.ID {
			t.Fatalf("premise: %s went to %s after a probe on the other auth", session, got)
		}
		return
	}
	t.Fatalf("could not bind %s to %s", session, target.ID)
}

// Finding 2: a blocked state is logged when it or its detail changes, not at every 5 s check; once more when it
// clears. A minute of checks and a periodic write add no line.
func TestProvimuxAffinityWarnsOnce(t *testing.T) {
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	for _, state := range []string{"foreign-file", "in-use", "key-not-private", "directory-not-writable"} {
		t.Run(state, func(t *testing.T) {
			if state == "directory-not-writable" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("Unix permissions, not root")
			}
			if state == "key-not-private" && runtime.GOOS == "windows" {
				t.Skip("no Unix owner")
			}
			dir := t.TempDir()
			s := pmxService(t, dir, "", nil, a, b)
			snapshot, _ := pmxPaths(t, s)
			var holder *Service
			var clear func()
			switch state {
			case "foreign-file":
				if err := os.WriteFile(snapshot, []byte("another program's file"), 0o600); err != nil {
					t.Fatal(err)
				}
				clear = func() { _ = os.Remove(snapshot) }
			case "in-use":
				holder = pmxService(t, dir, "", nil, a, b)
				pmxStart(t, holder)
				pmxWrite(holder)
				clear = func() { pmxEnd(holder) }
			case "key-not-private":
				earlier := pmxService(t, dir, "", nil, a, b)
				pmxStart(t, earlier)
				pmxWrite(earlier)
				pmxEnd(earlier)
				original := provimuxAffinityOwnedByUs
				provimuxAffinityOwnedByUs = func(fs.FileInfo) bool { return false }
				t.Cleanup(func() { provimuxAffinityOwnedByUs = original })
				clear = func() { provimuxAffinityOwnedByUs = original }
			case "directory-not-writable":
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
				clear = func() { _ = os.Chmod(dir, 0o700) }
			}
			hook := logtest.NewGlobal()
			defer hook.Reset()
			status := pmxStart(t, s)
			rt := s.provimuxAffinityRuntime()
			for i := 0; i < 12; i++ { // a minute of checks
				s.provimuxAffinityTick(rt, false)
			}
			s.provimuxAffinityTick(rt, true) // a periodic write
			if status = rt.currentStatus(); status.Reason != state {
				t.Fatalf("premise: state %+v", status)
			}
			if got := pmxAffinityLines(hook, log.WarnLevel); got != 1 {
				t.Fatalf("%d warnings over a start, 12 checks and a write; want 1", got)
			}
			clear()
			for i := 0; i < 3; i++ {
				s.provimuxAffinityTick(rt, i == 0)
			}
			if status = rt.currentStatus(); status.Reason != "" || !status.InEffect {
				t.Fatalf("not cleared: %+v", status)
			}
			cleared := 0
			for _, entry := range hook.AllEntries() {
				if entry.Level == log.InfoLevel && strings.HasPrefix(entry.Message, "session affinity: cleared") {
					cleared++
				}
			}
			if cleared != 1 || pmxAffinityLines(hook, log.WarnLevel) != 1 {
				t.Fatalf("after it cleared: %d cleared lines (want 1), %d warnings (want still 1)", cleared, pmxAffinityLines(hook, log.WarnLevel))
			}
			s.provimuxAffinityStop()
		})
	}
	// A detail that changes is a new state: logged again.
	t.Run("the detail changes", func(t *testing.T) {
		dir := t.TempDir()
		s := pmxService(t, dir, "", nil, a, b)
		snapshot, key := pmxPaths(t, s)
		if err := os.WriteFile(snapshot, []byte("another program's file"), 0o600); err != nil {
			t.Fatal(err)
		}
		hook := logtest.NewGlobal()
		defer hook.Reset()
		pmxStart(t, s)
		rt := s.provimuxAffinityRuntime()
		s.provimuxAffinityTick(rt, false)
		_ = os.Remove(snapshot)
		if err := os.WriteFile(key, []byte("another program's key"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			s.provimuxAffinityTick(rt, false)
		}
		if got := pmxAffinityLines(hook, log.WarnLevel); got != 2 {
			t.Fatalf("%d warnings for two foreign files one after the other; want 2", got)
		}
		s.provimuxAffinityStop()
	})
}

// Finding 3, the reviewer's reproduction: a gateway whose store moves, while running, to the path of another
// running gateway's store. The lock belongs to the path: the mover lets the old path's lock go, writes nothing at the
// new path while the other holds it (in-use), and with the setting false removes none of the other's files; a new
// gateway on the old path writes.
func TestProvimuxAffinityStoreMovedWhileRunning(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	one, two := filepath.Join(dir, "one.db"), filepath.Join(dir, "two.db")
	mover := pmxService(t, dir, one, nil, a, b)
	pmxStart(t, mover)
	pmxBind(t, mover, "provimux-test-mover", a, b)
	if status := pmxWrite(mover); status.Reason != "" {
		t.Fatalf("premise: mover %+v", status)
	}
	resident := pmxService(t, dir, two, nil, a, b)
	pmxStart(t, resident)
	pmxBind(t, resident, "provimux-test-resident", a, b)
	if status := pmxWrite(resident); status.Reason != "" {
		t.Fatalf("premise: resident %+v", status)
	}
	residentSnapshot, residentKey := pmxSum(t, two+".affinity"), pmxSum(t, two+".affinity-key")

	moveTo := func(path string, persist *bool) {
		cfg := mover.currentConfig().CloneForRuntime()
		cfg.KVStore.Path = path
		cfg.Routing.SessionAffinityPersist = persist
		mover.cfgMu.Lock()
		mover.cfg = cfg
		mover.cfgMu.Unlock()
	}
	moveTo(two, nil)
	moved := pmxWrite(mover)
	if moved.InEffect || moved.Reason != provimuxReasonInUse {
		t.Fatalf("the mover at the resident's path: %+v", moved)
	}
	if pmxSum(t, two+".affinity") != residentSnapshot || pmxSum(t, two+".affinity-key") != residentKey {
		t.Fatal("the mover wrote the resident's store while the resident held its lock")
	}
	if sessions := strings.Join(pmxSessions(t, two+".affinity", two+".affinity-key"), " "); strings.Contains(sessions, "provimux-test-mover") {
		t.Fatalf("the resident's file holds the mover's sessions: %s", sessions)
	}

	// The old path's lock was let go: a new gateway there writes.
	fresh := pmxService(t, dir, one, nil, a, b)
	pmxStart(t, fresh)
	if status := pmxWrite(fresh); status.Reason != "" || !status.InEffect {
		t.Fatalf("a new gateway on the old path: %+v", status)
	}
	fresh.provimuxAffinityStop()

	// The setting false at the moved path: none of the resident's files goes.
	moveTo(two, pmxBool(false))
	off := pmxWrite(mover)
	if off.Reason != provimuxReasonSwitchOff || !strings.Contains(off.LastError, "locked by another") {
		t.Fatalf("the mover set to false: %+v", off)
	}
	if !pmxExists(two+".affinity") || !pmxExists(two+".affinity-key") || pmxSum(t, two+".affinity") != residentSnapshot {
		t.Fatal("the mover removed or changed the resident's files")
	}
	mover.provimuxAffinityStop()
	resident.provimuxAffinityStop()
}

// Finding 4: a Claude login whose file holds no account ids at the start; upstream fills them in memory at its first
// request (and saves them to the login file). An identity appearing where there was none is a change like any other:
// the login's bindings are left out for the rest of the run, those made after the ids appeared included, because the
// file may now be another account's. From the next start, with the ids in the file, its conversations are kept.
func TestProvimuxAffinityIdentityAfterStart(t *testing.T) {
	dir := t.TempDir()
	a := pmxClaude("provimux-test-a.json", "acc-a")
	filled := pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", nil, a, pmxNoIDs("provimux-test-b.json"))
	pmxStart(t, s)
	rt := s.provimuxAffinityRuntime()
	pmxBindOn(t, s, "provimux-test-before", pmxNoIDs("provimux-test-b.json"), a, pmxNoIDs("provimux-test-b.json"))
	if _, err := s.coreManager.Update(context.Background(), filled); err != nil { // the first request fills the ids
		t.Fatal(err)
	}
	s.provimuxAffinityTick(rt, false) // the next check sees them: a change
	pmxBindOn(t, s, "provimux-test-after", filled, a, filled)
	status := pmxWrite(s)
	snapshot, key := pmxPaths(t, s)
	written := " " + strings.Join(pmxSessions(t, snapshot, key), " ") + " "
	if strings.Contains(written, " header:provimux-test-before ") || strings.Contains(written, " header:provimux-test-after ") {
		t.Fatalf("a binding of the login whose ids appeared during the run is written (%+v): %s", status, written)
	}
	if status.LeftOut < 2 {
		t.Fatalf("the left-out bindings are not counted as unverified: %+v", status)
	}
	s.provimuxAffinityStop()

	// The next start, the ids in the login file: bindings made in this run are kept and come back.
	next := pmxService(t, dir, "", nil, a, filled)
	pmxStart(t, next)
	pmxBindOn(t, next, "provimux-test-next-run", filled, a, filled)
	pmxWrite(next)
	next.provimuxAffinityStop()
	third := pmxService(t, dir, "", nil, a, filled)
	pmxStart(t, third)
	if id, state := pmxBound(third, "provimux-test-next-run"); state != "bound" || id != filled.ID {
		t.Fatalf("a conversation of the run after the ids were saved: %s (%s), want %s", id, state, filled.ID)
	}
	third.provimuxAffinityStop()
}

// pmxNoIDs is a Claude login whose file holds no account ids yet.
func pmxNoIDs(id string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "shared@example.invalid"}}
}

// Finding 4, the reviewer's nine paths: B starts without ids (vendor account acc-b) and holds old-session; then, per
// path, its ids appear, or another account's login (acc-c) takes the file. After a restart old-session may come back
// only to acc-b, never to another account. The three paths that "no identity, then one" reopened are marked; the
// other six stay closed.
func TestProvimuxAffinityReplacedLoginPaths(t *testing.T) {
	type tools struct {
		s      *Service
		b      *coreauth.Auth
		update func(*coreauth.Auth)
		touch  func() // the answer of old-session on B ends now
		check  func() // a 5 s check
	}
	for _, path := range []struct {
		name  string
		run   func(t *testing.T, x tools)
		after string // the account of B at the restart
	}{
		{"ids of the same account appear, the conversation not used again", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-b"))
			x.check()
		}, "acc-b"},
		{"ids of the same account appear, its request completes after the check", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-b"))
			x.check()
			x.touch()
		}, "acc-b"},
		{"replaced by another account's login with ids, the conversation not used again", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-c"))
			x.check()
		}, "acc-c"},
		{"reopened 1: replaced by another account's login with ids, the old request completes after the check", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-c"))
			x.check()
			x.touch()
		}, "acc-c"},
		{"replaced by another account's login with ids, the old request completes before the check", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-c"))
			x.touch()
			x.check()
		}, "acc-c"},
		{"replaced inside the write (between its two reads of the accounts)", func(t *testing.T, x tools) {
			original := provimuxAffinityOwnedByUs
			var once sync.Once
			provimuxAffinityOwnedByUs = func(f fs.FileInfo) bool {
				once.Do(func() { x.update(pmxClaude("B", "acc-c")) })
				return original(f)
			}
			defer func() { provimuxAffinityOwnedByUs = original }()
			pmxWrite(x.s)
		}, "acc-c"},
		{"ids appear (acc-b), then replaced by acc-c", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-b"))
			x.check()
			x.touch()
			x.update(pmxClaude("B", "acc-c"))
			x.check()
			x.touch()
		}, "acc-c"},
		{"reopened 2: ids appear and are replaced between two checks (acc-b never seen)", func(t *testing.T, x tools) {
			x.update(pmxClaude("B", "acc-b"))
			x.touch()
			x.update(pmxClaude("B", "acc-c"))
			x.check()
			x.touch()
		}, "acc-c"},
		{"reopened 3: replaced by another login without ids, whose ids appear at its first request", func(t *testing.T, x tools) {
			x.update(pmxNoIDs("B")) // acc-c's login, no ids yet
			x.check()
			x.update(pmxClaude("B", "acc-c")) // its first request filled them
			x.check()
			x.touch() // an answer of the old account still in flight
		}, "acc-c"},
	} {
		t.Run(path.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := pmxClaude("A", "acc-a"), pmxNoIDs("B")
			s := pmxService(t, dir, "", nil, a, b)
			pmxStart(t, s)
			pmxWrite(s)
			pmxBindOn(t, s, "old-session", b, a, b)
			x := tools{s: s, b: b,
				update: func(auth *coreauth.Auth) {
					if _, err := s.coreManager.Update(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
				},
				touch: func() {
					s.coreManager.Selector().(*coreauth.SessionAffinitySelector).OnResult(coreauth.Result{AuthID: b.ID, Provider: "mixed",
						Model: "provimux-test-model", Success: true, Options: pmxSessionOpts("old-session")})
				},
				check: func() { s.provimuxAffinityTick(s.provimuxAffinityRuntime(), false) },
			}
			path.run(t, x)
			pmxWrite(s)
			pmxEnd(s)
			next := pmxService(t, dir, "", nil, pmxClaude("A", "acc-a"), pmxClaude("B", path.after))
			pmxStart(t, next)
			id, state := pmxBound(next, "old-session")
			if state != "bound" {
				return
			}
			auth, _ := next.coreManager.GetByID(id)
			if account, _ := auth.Metadata["account_uuid"].(string); account != "acc-b" {
				t.Fatalf("old-session, last answered by acc-b, came back on %s", account)
			}
		})
	}
}

// Note 6: a temporary file a process left when it ended mid-write is removed at the next start, when it
// authenticates; another program's file of that name stays.
func TestProvimuxAffinityStartRemovesCutShortWrite(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	earlier := pmxService(t, dir, "", nil, a, b)
	pmxStart(t, earlier)
	pmxBind(t, earlier, "provimux-test-session", a, b)
	pmxWrite(earlier)
	snapshot, key := pmxPaths(t, earlier)
	pmxEnd(earlier)
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ours := filepath.Join(dir, "."+filepath.Base(snapshot)+".tmp-1234567")
	foreign := filepath.Join(dir, "."+filepath.Base(snapshot)+".tmp-7654321")
	if errWrite := os.WriteFile(ours, data, 0o600); errWrite != nil { // a complete write that never got renamed
		t.Fatal(errWrite)
	}
	if errWrite := os.WriteFile(foreign, []byte("another program's file"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	s := pmxService(t, dir, "", nil, a, b)
	status := pmxStart(t, s)
	if !status.InEffect || status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 1 {
		t.Fatalf("state %+v", status)
	}
	if pmxExists(ours) {
		t.Fatal("the temporary file a cut-short write left is still there after the start")
	}
	if !pmxExists(foreign) || !pmxExists(key) || !pmxExists(snapshot) {
		t.Fatal("the start removed a file it should have kept")
	}
	s.provimuxAffinityStop()
}
