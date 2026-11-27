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

// Finding 4, the reviewer's reproduction: a Claude login whose file holds no account ids at the start; upstream
// fills them in memory at its first request. "No identity, then one" is a login first seen then: its bindings made
// from then on are written and come back; those from before are not. "One identity, then another" (and "none, one,
// another") stays a change, left out for the rest of the run.
func TestProvimuxAffinityIdentityAfterStart(t *testing.T) {
	dir := t.TempDir()
	a := pmxClaude("provimux-test-a.json", "acc-a")
	idless := &coreauth.Auth{ID: "provimux-test-b.json", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "shared@example.invalid"}}
	filled := pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", nil, a, idless)
	pmxStart(t, s)
	rt := s.provimuxAffinityRuntime()
	pmxBindOn(t, s, "provimux-test-before", idless, a, idless)
	if _, err := s.coreManager.Update(context.Background(), filled); err != nil { // the first request fills the ids
		t.Fatal(err)
	}
	s.provimuxAffinityTick(rt, false) // the next check sees them
	pmxBindOn(t, s, "provimux-test-after", filled, a, filled)
	status := pmxWrite(s)
	snapshot, key := pmxPaths(t, s)
	written := " " + strings.Join(pmxSessions(t, snapshot, key), " ") + " "
	if !strings.Contains(written, " header:provimux-test-after ") {
		t.Fatalf("a binding made after the login got its ids is left out (%+v); written: %s", status, written)
	}
	if strings.Contains(written, " header:provimux-test-before ") {
		t.Fatalf("a binding of the login before it had ids is written: %s", written)
	}
	s.provimuxAffinityStop()

	// The next start (the ids are in the file now): the conversation comes back to the login.
	next := pmxService(t, dir, "", nil, a, filled)
	pmxStart(t, next)
	if id, state := pmxBound(next, "provimux-test-after"); state != "bound" || id != filled.ID {
		t.Fatalf("after the restart: %s (%s), want %s", id, state, filled.ID)
	}
	next.provimuxAffinityStop()

	// None, then one, then another account's: tainted from the change on, as before.
	dir2 := t.TempDir()
	idless2 := &coreauth.Auth{ID: "provimux-test-b.json", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": "shared@example.invalid"}}
	r := pmxService(t, dir2, "", nil, a, idless2)
	pmxStart(t, r)
	rrt := r.provimuxAffinityRuntime()
	if _, err := r.coreManager.Update(context.Background(), pmxClaude("provimux-test-b.json", "acc-b")); err != nil {
		t.Fatal(err)
	}
	r.provimuxAffinityTick(rrt, false)
	other := pmxClaude("provimux-test-b.json", "acc-c")
	if _, err := r.coreManager.Update(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	r.provimuxAffinityTick(rrt, false)
	pmxBindOn(t, r, "provimux-test-replaced", other, a, other)
	status = pmxWrite(r)
	snapshot2, key2 := pmxPaths(t, r)
	if written := " " + strings.Join(pmxSessions(t, snapshot2, key2), " ") + " "; strings.Contains(written, " header:provimux-test-replaced ") {
		t.Fatalf("a login replaced by another account's after it got its ids is written (%+v): %s", status, written)
	}
	r.provimuxAffinityStop()
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
