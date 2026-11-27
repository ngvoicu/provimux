package cliproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	bolt "go.etcd.io/bbolt"
)

func pmxBool(v bool) *bool { return &v }

// pmxOn is routing.session-affinity-persist: true.
var pmxOn = pmxBool(true)

func pmxClaude(id, account string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"account_uuid": account, "organization_uuid": "org-" + account, "email": "shared@example.invalid"}}
}

// pmxService is a service with session affinity, its selector built the way Run builds it, its
// KV store at kvPath ("" = <dir>/kv.db). It ends with the test (pmxEnd).
func pmxService(t *testing.T, dir, kvPath string, persist *bool, auths ...*coreauth.Auth) *Service {
	t.Helper()
	cfg := &config.Config{}
	cfg.AuthDir = dir
	cfg.KVStore.Path = kvPath
	cfg.Routing.SessionAffinity = true
	cfg.Routing.SessionAffinityPersist = persist
	manager := coreauth.NewManager(nil, newRoutingSelector(normalizedRoutingRuntimeState(cfg)), nil)
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{cfg: cfg, coreManager: manager}
	t.Cleanup(func() { pmxEnd(s) })
	return s
}

// pmxEnd is the end of the service's process without a stop: its loop ends, nothing is written,
// and its lock on the store goes (as the kernel releases it when a process ends).
func pmxEnd(s *Service) {
	value, ok := provimuxAffinityRuntimes.Load(s)
	if !ok {
		return
	}
	rt := value.(*provimuxAffinityRuntime)
	rt.statusMu.Lock()
	stop, done := rt.stop, rt.done
	rt.stop, rt.done = nil, nil
	rt.statusMu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	rt.cancelled.Store(true)
	if rt.writeMu.TryLock() {
		rt.releaseLock()
		rt.writeMu.Unlock()
	}
	provimuxAffinityRuntimes.Delete(s)
}

func pmxSessionOpts(session string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{session}}, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "scope"}}
}

func pmxBind(t *testing.T, s *Service, session string, auths ...*coreauth.Auth) string {
	t.Helper()
	picked, err := s.coreManager.Selector().Pick(context.Background(), "mixed", "provimux-test-model", pmxSessionOpts(session), auths)
	if err != nil {
		t.Fatal(err)
	}
	return picked.ID
}

func pmxBound(s *Service, session string) (string, string) {
	affinity, _ := s.coreManager.Selector().(*coreauth.SessionAffinitySelector)
	return affinity.LookupAffinity("mixed", "provimux-test-model", session)
}

// pmxWrite writes a snapshot now, as a periodic write does.
func pmxWrite(s *Service) ProvimuxAffinityPersistStatus {
	rt := s.provimuxAffinityRuntime()
	s.provimuxAffinityTick(rt, true)
	return rt.currentStatus()
}

// pmxStart starts as Run does (restore once, then the loop) and returns the state.
func pmxStart(t *testing.T, s *Service) ProvimuxAffinityPersistStatus {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.provimuxAffinityStart(ctx)
	return s.provimuxAffinityRuntime().currentStatus()
}

func pmxPaths(t *testing.T, s *Service) (string, string) {
	t.Helper()
	snapshot, key, err := provimuxAffinityPaths(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, key
}

func pmxExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func pmxSum(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

// pmxSecret reads the installation key's secret straight from its file.
func pmxSecret(t *testing.T, key string) []byte {
	t.Helper()
	data, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	return data[len(provimuxAffinityKeyMagic) : len(provimuxAffinityKeyMagic)+provimuxAffinityKeySize]
}

// pmxSessions decrypts the snapshot and lists its sessions' ids.
func pmxSessions(t *testing.T, snapshot, key string) []string {
	t.Helper()
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, err := provimuxAffinityOpen(pmxSecret(t, key), data)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, session := range state.Sessions {
		out = append(out, session.SessionID)
	}
	return out
}

// The default: on whenever session affinity is on, by the owner's rule, in one constant; both values
// of the constant are covered. Not set, with session affinity on: the bindings are written and come
// back. Not set, with session affinity off, in Home mode or with the KV store disabled: nothing.
func TestProvimuxAffinityDefault(t *testing.T) {
	if !provimuxAffinityPersistDefault {
		t.Fatal("routing.session-affinity-persist is off by default; the owner's rule is on whenever session affinity is on")
	}
	cfg := &config.Config{}
	if setting, set, on, reason := provimuxAffinityMode(cfg); !setting || set || on || reason != provimuxReasonAffinityOff {
		t.Fatalf("not set, session affinity off: %v %v %v %s", setting, set, on, reason)
	}
	cfg.Routing.SessionAffinity = true
	if setting, set, on, reason := provimuxAffinityModeWith(cfg, false); setting || set || on || reason != provimuxReasonSwitchOff {
		t.Fatalf("default off: %v %v %v %s", setting, set, on, reason)
	}
	if setting, set, on, reason := provimuxAffinityModeWith(cfg, true); !setting || set || !on || reason != "" {
		t.Fatalf("default on: %v %v %v %s", setting, set, on, reason)
	}
	for _, byDefault := range []bool{false, true} {
		cfg.Routing.SessionAffinityPersist = pmxBool(!byDefault)
		if setting, set, on, _ := provimuxAffinityModeWith(cfg, byDefault); setting == byDefault || !set || on == byDefault {
			t.Fatalf("default %v, set to %v: %v %v %v", byDefault, !byDefault, setting, set, on)
		}
	}
	// Not set, session affinity on: the first start of this build finds no file, the stop writes
	// both, and the next start puts the conversation back on its account.
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	first := pmxService(t, dir, "", nil, a, b)
	snapshot, key := pmxPaths(t, first)
	if pmxExists(snapshot) || pmxExists(key) {
		t.Fatal("premise: files before the first start")
	}
	status := pmxStart(t, first)
	if !status.Setting || status.Set || !status.InEffect || status.Reason != "" || status.RestoredAtStart != nil || status.StartNote != "no snapshot" {
		t.Fatalf("first start, not set: %+v", status)
	}
	pmxBind(t, first, "provimux-test-primer", a, b)
	bound := pmxBind(t, first, "provimux-test-session", a, b)
	first.provimuxAffinityStop()
	if !pmxExists(snapshot) || !pmxExists(key) {
		t.Fatal("not set: the stop wrote no files")
	}
	if sessions := pmxSessions(t, snapshot, key); len(sessions) != 2 {
		t.Fatalf("sessions written: %v", sessions)
	}
	second := pmxService(t, dir, "", nil, a, b)
	status = pmxStart(t, second)
	if !status.InEffect || status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 2 {
		t.Fatalf("second start, not set: %+v", status)
	}
	if id, state := pmxBound(second, "provimux-test-session"); state != "bound" || id != bound {
		t.Fatalf("bound to %s, after the restart %s (%s)", bound, id, state)
	}
	second.provimuxAffinityStop()

	// Not set, with session affinity off, in Home mode or with the KV store disabled: no file, and
	// the state names the cause.
	for _, tc := range []struct {
		name   string
		change func(*config.Config)
		reason string
	}{
		{"session affinity off", func(c *config.Config) { c.Routing.SessionAffinity = false }, provimuxReasonAffinityOff},
		{"home mode", func(c *config.Config) { c.Home.Enabled = true }, provimuxReasonHome},
		{"kv store disabled", func(c *config.Config) { c.KVStore.Disabled = true }, provimuxReasonKVStoreOff},
	} {
		dir := t.TempDir()
		s := pmxService(t, dir, "", nil, a, b)
		tc.change(s.cfg)
		pmxBind(t, s, "provimux-test-session", a, b)
		status := pmxStart(t, s)
		pmxWrite(s)
		s.provimuxAffinityStop()
		if !status.Setting || status.Set || status.InEffect || status.Reason != tc.reason || status.RestoredAtStart != nil {
			t.Fatalf("%s, not set: state %+v", tc.name, status)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("%s, not set: files %v", tc.name, entries)
		}
	}
}

// Findings 12 and 6: the files are named from the store's own path; two stores in one directory
// never share them, and two services never touch each other's files or stop each other's writer.
func TestProvimuxAffinityFilesBelongToTheirStore(t *testing.T) {
	SetProvimuxAffinitySnapshotInterval(40 * time.Millisecond)
	defer SetProvimuxAffinitySnapshotInterval(0)
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	one := pmxService(t, dir, filepath.Join(dir, "one.db"), pmxOn, a, b)
	two := pmxService(t, dir, filepath.Join(dir, "two.db"), pmxOn, a, b)
	oneSnapshot, oneKey := pmxPaths(t, one)
	twoSnapshot, twoKey := pmxPaths(t, two)
	if oneSnapshot != filepath.Join(dir, "one.db.affinity") || oneKey != filepath.Join(dir, "one.db.affinity-key") || twoSnapshot == oneSnapshot || twoKey == oneKey {
		t.Fatalf("paths %s %s %s %s", oneSnapshot, oneKey, twoSnapshot, twoKey)
	}
	pmxBind(t, one, "provimux-test-primer", a, b)
	if got := pmxBind(t, one, "provimux-test-session", a, b); got != b.ID {
		t.Fatalf("premise: on %s", got)
	}
	pmxStart(t, one)
	pmxStart(t, two)
	deadline := time.Now().Add(30 * time.Second)
	for !(pmxExists(oneSnapshot) && pmxExists(twoSnapshot)) {
		if time.Now().After(deadline) {
			t.Fatal("no periodic snapshots")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stopping one ends its writer only: two keeps writing, its files stay.
	one.provimuxAffinityStop()
	twoRT, _ := provimuxAffinityRuntimes.Load(two)
	before := twoRT.(*provimuxAffinityRuntime).currentStatus().LastWrittenAt
	// Two's next periodic write is an event: wait for it, with a bound only a stopped writer reaches (a fixed
	// sleep failed 2 times in 50 on a loaded machine).
	for limit := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if after := twoRT.(*provimuxAffinityRuntime).currentStatus().LastWrittenAt; after != nil && before != nil && after.After(*before) {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("stopping one service stopped the other's writer")
		}
	}
	two.provimuxAffinityStop()
	for _, path := range []string{oneSnapshot, oneKey, twoSnapshot, twoKey} {
		if !pmxExists(path) {
			t.Fatalf("%s is gone", path)
		}
	}
	// Each store restores its own: two's snapshot holds no binding of one.
	fresh := pmxService(t, dir, filepath.Join(dir, "two.db"), pmxOn, a, b)
	if status := pmxStart(t, fresh); status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 0 {
		t.Fatalf("two restored %+v (%s)", status.RestoredAtStart, status.StartNote)
	}
	fresh.provimuxAffinityStop()
	again := pmxService(t, dir, filepath.Join(dir, "one.db"), pmxOn, a, b)
	if status := pmxStart(t, again); status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 2 {
		t.Fatalf("one restored %+v (%s)", status.RestoredAtStart, status.StartNote)
	}
	again.provimuxAffinityStop()
}

// Finding 12 and case 1 of the third review: ours means it authenticates. Anything else at our
// two names (another program's database, a file that merely begins with our header, a key of the
// right length with a wrong check value, a damaged snapshot of ours, one without its key or under
// another key, a link, a directory) is never replaced or removed, nothing is written or restored,
// and the state says so; with the switch on and off.
func TestProvimuxAffinityLeavesForeignFilesAlone(t *testing.T) {
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	bboltAt := func(t *testing.T, path string) {
		db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if errUpdate := db.Update(func(tx *bolt.Tx) error {
			bucket, errBucket := tx.CreateBucketIfNotExists([]byte("provimux-test"))
			if errBucket != nil {
				return errBucket
			}
			return bucket.Put([]byte("sentinel"), []byte("still here"))
		}); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		_ = db.Close()
	}
	// An earlier run of ours, then whatever the case puts at one name.
	ours := func(t *testing.T, dir string) {
		earlier := pmxService(t, dir, "", pmxOn, a, b)
		pmxStart(t, earlier)
		pmxBind(t, earlier, "provimux-test-session", a, b)
		pmxWrite(earlier)
		pmxEnd(earlier)
	}
	type fixture struct {
		name    string
		prepare func(t *testing.T, dir, snapshot, key string) string // returns the path left alone
	}
	fixtures := []fixture{
		{"bbolt at the snapshot's name", func(t *testing.T, dir, snapshot, key string) string { bboltAt(t, snapshot); return snapshot }},
		{"bbolt at the key's name", func(t *testing.T, dir, snapshot, key string) string { bboltAt(t, key); return key }},
		{"a file that begins with our snapshot header", func(t *testing.T, dir, snapshot, key string) string {
			_ = os.WriteFile(snapshot, []byte(provimuxAffinityMagic+"Notes for my independent fixture format\n"), 0o644)
			return snapshot
		}},
		{"a file that begins with our key header, of the wrong length", func(t *testing.T, dir, snapshot, key string) string {
			_ = os.WriteFile(key, []byte(provimuxAffinityKeyMagic+"Unrelated fixture"), 0o600)
			return key
		}},
		{"a key of the right format and length with a wrong check value", func(t *testing.T, dir, snapshot, key string) string {
			content := provimuxAffinityKeyBytes(bytes.Repeat([]byte("z"), provimuxAffinityKeySize))
			content[len(content)-1] ^= 1
			_ = os.WriteFile(key, content, 0o600)
			return key
		}},
		{"a damaged snapshot of ours (one bit)", func(t *testing.T, dir, snapshot, key string) string {
			ours(t, dir)
			data, _ := os.ReadFile(snapshot)
			data[len(data)/2] ^= 1
			_ = os.WriteFile(snapshot, data, 0o600)
			return snapshot
		}},
		{"a truncated snapshot of ours", func(t *testing.T, dir, snapshot, key string) string {
			ours(t, dir)
			data, _ := os.ReadFile(snapshot)
			_ = os.WriteFile(snapshot, data[:len(data)-1], 0o600)
			return snapshot
		}},
		{"a snapshot of ours without its key", func(t *testing.T, dir, snapshot, key string) string {
			ours(t, dir)
			_ = os.Remove(key)
			return snapshot
		}},
		{"a snapshot under another installation's key", func(t *testing.T, dir, snapshot, key string) string {
			ours(t, dir)
			_ = os.Remove(key)
			if _, err := provimuxAffinityMakeKey(key); err != nil {
				t.Fatal(err)
			}
			return snapshot
		}},
		{"a link at the snapshot's name", func(t *testing.T, dir, snapshot, key string) string {
			target := filepath.Join(dir, "elsewhere")
			_ = os.WriteFile(target, []byte(provimuxAffinityMagic+"not really"), 0o600)
			_ = os.Symlink(target, snapshot)
			return snapshot
		}},
		{"a directory at the snapshot's name", func(t *testing.T, dir, snapshot, key string) string {
			_ = os.Mkdir(snapshot, 0o700)
			_ = os.WriteFile(filepath.Join(snapshot, "inside"), []byte("x"), 0o600)
			return snapshot
		}},
	}
	describe := func(path string) string {
		info, err := os.Lstat(path)
		if err != nil {
			return "absent"
		}
		if info.IsDir() {
			entries, _ := os.ReadDir(path)
			return fmt.Sprintf("dir %d", len(entries))
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			return "link " + target
		}
		data, _ := os.ReadFile(path)
		return fmt.Sprintf("file %x", sha256.Sum256(data))
	}
	for _, f := range fixtures {
		for _, persist := range []*bool{pmxOn, pmxBool(false)} {
			t.Run(fmt.Sprintf("%s, setting %v", f.name, *persist), func(t *testing.T) {
				dir := t.TempDir()
				probe := pmxService(t, dir, "", persist, a, b)
				snapshot, key := pmxPaths(t, probe)
				foreign := f.prepare(t, dir, snapshot, key)
				other := key
				if foreign == key {
					other = snapshot
				}
				before, otherBefore := describe(foreign), describe(other)
				s := pmxService(t, dir, "", persist, a, b)
				pmxBind(t, s, "provimux-test-later", a, b)
				status := pmxStart(t, s)
				status2 := pmxWrite(s)
				s.provimuxAffinityStop()
				if after := describe(foreign); after != before {
					t.Fatalf("%s changed: %s -> %s", foreign, before, after)
				}
				if f.name == "bbolt at the snapshot's name" || f.name == "bbolt at the key's name" {
					db, err := bolt.Open(foreign, 0o600, &bolt.Options{Timeout: time.Second, ReadOnly: true})
					if err != nil {
						t.Fatalf("no longer bbolt: %v", err)
					}
					_ = db.Close()
				}
				if *persist {
					if status.InEffect || status.Reason != provimuxReasonForeignFile || status2.Reason != provimuxReasonForeignFile || !strings.Contains(status.Detail, foreign) || !strings.Contains(status.Detail, "remove it by hand") {
						t.Fatalf("state %+v / %+v", status, status2)
					}
					if status.RestoredAtStart != nil {
						t.Fatalf("restored %+v", status.RestoredAtStart)
					}
					if after := describe(other); after != otherBefore {
						t.Fatalf("%s changed beside a file that is not ours: %s -> %s", other, otherBefore, after)
					}
				}
			})
		}
	}
}

// Case 1: temporary files beside the snapshot are removed only when they are ours (they
// authenticate under our key), and only in our own directory: a neighbour whose name differs by a
// glob character keeps its files.
func TestProvimuxAffinityTemporaryFiles(t *testing.T) {
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	parent := t.TempDir()
	literal, neighbour := filepath.Join(parent, "cache[1]"), filepath.Join(parent, "cache1")
	var authenticated, partial, neighbours string
	for _, dir := range []string{literal, neighbour} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		s := pmxService(t, dir, "", pmxOn, a, b)
		pmxStart(t, s)
		pmxBind(t, s, "provimux-test-session", a, b)
		pmxWrite(s)
		pmxEnd(s)
		snapshot, _ := pmxPaths(t, s)
		data, _ := os.ReadFile(snapshot)
		whole := filepath.Join(dir, ".kv.db.affinity.tmp-crashed-whole")
		_ = os.WriteFile(whole, data, 0o600)
		if dir == literal {
			authenticated = whole
			partial = filepath.Join(dir, ".kv.db.affinity.tmp-crashed-partial")
			_ = os.WriteFile(partial, data[:len(data)/2], 0o600)
		} else {
			neighbours = whole
		}
	}
	off := pmxService(t, literal, "", pmxBool(false), a, b)
	pmxStart(t, off)
	off.provimuxAffinityStop()
	if pmxExists(authenticated) {
		t.Fatal("our own complete temporary file was not removed")
	}
	if !pmxExists(partial) {
		t.Fatal("a temporary file that does not authenticate was removed")
	}
	if !pmxExists(neighbours) {
		t.Fatal("a neighbour directory's file was removed")
	}
}

// Finding 9 and case 3: the key is made 0600; a key we own with a wider mode is set to 0600 and
// the state says so; a key owned by another user is not used. Access control lists are not
// inspected (the README says so).
func TestProvimuxAffinityKeyIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	dir := t.TempDir()
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	pmxBind(t, s, "provimux-test-primer", a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxWrite(s)
	pmxEnd(s)
	snapshot, key := pmxPaths(t, s)
	for _, path := range []string{snapshot, key} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}
	if data, _ := os.ReadFile(key); len(data) != len(provimuxAffinityKeyMagic)+provimuxAffinityKeySize+provimuxAffinityKeyCheck {
		t.Fatalf("key of %d bytes", len(data))
	}

	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	repaired := pmxService(t, dir, "", pmxOn, a, b)
	status := pmxStart(t, repaired)
	repaired.provimuxAffinityStop()
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 || !strings.Contains(status.KeyNote, "set to 0600") || status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 2 {
		t.Fatalf("mode %v, state %+v", info.Mode().Perm(), status)
	}

	previous := provimuxAffinityOwnedByUs
	provimuxAffinityOwnedByUs = func(fs.FileInfo) bool { return false }
	defer func() { provimuxAffinityOwnedByUs = previous }()
	written := pmxSum(t, snapshot)
	other := pmxService(t, dir, "", pmxOn, a, b)
	status = pmxStart(t, other)
	pmxBind(t, other, "provimux-test-later", a, b)
	pmxWrite(other)
	other.provimuxAffinityStop()
	if status.InEffect || status.Reason != provimuxReasonKeyPrivate || status.RestoredAtStart != nil {
		t.Fatalf("state %+v", status)
	}
	if id, state := pmxBound(other, "provimux-test-session"); state == "bound" {
		t.Fatalf("restored with another user's key (%s)", id)
	}
	if pmxSum(t, snapshot) != written {
		t.Fatal("written with another user's key")
	}
}

// Finding 9: what the file holds, decrypted, against the README's list: the session ids clients
// sent, as they sent them (an e-mail address a client sends as its session id included), model
// names, the derived identities of conversations without an id, account tokens; no client key,
// caller-scope hash, login file name, login e-mail, account id, or conversation content. Without
// the key the file shows none of them.
func TestProvimuxAffinityWhatTheFileHolds(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a@example.invalid.json", "acc-a-uuid"), pmxClaude("provimux-test-b@example.invalid.json", "acc-b-uuid")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	const clientKey = "provimux-test-client-key-secret"
	scope := cliproxysession.CallerScope(clientKey)
	selector := s.coreManager.Selector()
	explicit := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"provimux-test-session-id"}}, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}
	if _, err := selector.Pick(context.Background(), "mixed", "provimux-test-model", explicit, []*coreauth.Auth{a, b}); err != nil {
		t.Fatal(err)
	}
	// A client that sends an e-mail address as metadata.user_id: upstream uses it as the session id.
	userID := []byte(`{"metadata":{"user_id":"reader@example.invalid"},"messages":[{"role":"user","content":"provimux-test private prompt"}]}`)
	byUser := cliproxyexecutor.Options{OriginalRequest: userID, SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}
	_, byUser = cliproxysession.Enrich(cliproxyexecutor.Request{Payload: userID, Format: sdktranslator.FormatOpenAI}, byUser)
	if _, err := selector.Pick(context.Background(), "mixed", "provimux-test-model", byUser, []*coreauth.Auth{a, b}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"provimux-test-model","instructions":"provimux-test system prompt","input":[{"type":"message","role":"user","content":"provimux-test question text"}]}`)
	noID := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: body, Metadata: map[string]any{}}
	_, noID = cliproxysession.Enrich(cliproxyexecutor.Request{Payload: body, Format: sdktranslator.FormatOpenAIResponse}, noID)
	if _, err := selector.Pick(context.Background(), "mixed", "provimux-test-model", noID, []*coreauth.Auth{a, b}); err != nil {
		t.Fatal(err)
	}
	pmxWrite(s)
	snapshot, key := pmxPaths(t, s)
	data, _ := os.ReadFile(snapshot)
	plain, err := provimuxAffinityUnseal(pmxSecret(t, key), data)
	if err != nil {
		t.Fatal(err)
	}
	derived := cliproxysession.DerivedID(noID.Metadata)
	for _, held := range []string{"provimux-test-session-id", "reader@example.invalid", "provimux-test-model", derived} {
		if !bytes.Contains(plain, []byte(held)) {
			t.Errorf("the plaintext does not hold %q", held)
		}
		if bytes.Contains(data, []byte(held)) {
			t.Errorf("the file holds %q without the key", held)
		}
	}
	for _, absent := range []string{clientKey, scope, a.ID, b.ID, "provimux-test-a@example", "shared@example.invalid", "acc-a-uuid", "org-acc-a-uuid", "provimux-test question text", "provimux-test system prompt", "provimux-test private prompt"} {
		if bytes.Contains(plain, []byte(absent)) {
			t.Errorf("the plaintext holds %q", absent)
		}
	}
}

// Finding 13: the snapshot stays after a restore, so a second crash before the next write
// restores it again; the next write replaces it.
func TestProvimuxAffinitySecondCrashRestoresAgain(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	pmxBind(t, s, "provimux-test-primer", a, b)
	bound := pmxBind(t, s, "provimux-test-session", a, b)
	pmxWrite(s)
	pmxEnd(s)
	snapshot, _ := pmxPaths(t, s)
	written := pmxSum(t, snapshot)
	for start := 1; start <= 2; start++ {
		// A crash: no stop. The next process starts from the same snapshot.
		next := pmxService(t, dir, "", pmxOn, a, b)
		status := pmxStart(t, next)
		if status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 2 {
			t.Fatalf("start %d restored %+v (%s)", start, status.RestoredAtStart, status.StartNote)
		}
		if id, state := pmxBound(next, "provimux-test-session"); state != "bound" || id != bound {
			t.Fatalf("start %d: bound to %s (%s), was %s", start, id, state, bound)
		}
		if pmxSum(t, snapshot) != written {
			t.Fatalf("start %d changed the snapshot", start)
		}
		pmxEnd(next)
	}
	later := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, later)
	pmxBind(t, later, "provimux-test-new", a, b)
	pmxWrite(later)
	later.provimuxAffinityStop()
	if pmxSum(t, snapshot) == written {
		t.Fatal("the next write did not replace the snapshot")
	}
}

// Finding 11: whenever the feature is not in effect, for any reason and any combination of them
// (the setting false, session affinity off, Home mode, KV store disabled; each also with the setting
// not set, which alone means on), our files from an earlier run are removed, nothing is restored and
// nothing is written.
func TestProvimuxAffinityOffMeansGone(t *testing.T) {
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	causes := []string{"switch off", "not set", "session affinity off", "home mode", "kv store disabled"}
	for mask := 1; mask < 1<<len(causes); mask++ {
		if mask&3 == 3 {
			continue // off and not set at once
		}
		if mask == 2 {
			continue // not set alone: on (TestProvimuxAffinityDefault)
		}
		var names []string
		for i, cause := range causes {
			if mask&(1<<i) != 0 {
				names = append(names, cause)
			}
		}
		for _, earlier := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, files of an earlier run %v", strings.Join(names, " + "), earlier), func(t *testing.T) {
				dir := t.TempDir()
				if earlier {
					on := pmxService(t, dir, "", pmxOn, a, b)
					pmxStart(t, on)
					pmxBind(t, on, "provimux-test-session", a, b)
					pmxWrite(on)
					pmxEnd(on)
				}
				s := pmxService(t, dir, "", pmxOn, a, b)
				if mask&1 != 0 {
					s.cfg.Routing.SessionAffinityPersist = pmxBool(false)
				}
				if mask&2 != 0 {
					s.cfg.Routing.SessionAffinityPersist = nil
				}
				if mask&4 != 0 {
					s.cfg.Routing.SessionAffinity = false
				}
				if mask&8 != 0 {
					s.cfg.Home.Enabled = true
				}
				if mask&16 != 0 {
					s.cfg.KVStore.Disabled = true
				}
				pmxBind(t, s, "provimux-test-other", a, b)
				status := pmxStart(t, s)
				snapshot, key := pmxPaths(t, s)
				if pmxExists(snapshot) || pmxExists(key) {
					t.Fatal("files remain after the start")
				}
				if status.InEffect || status.RestoredAtStart != nil {
					t.Fatalf("state %+v", status)
				}
				if id, state := pmxBound(s, "provimux-test-session"); state == "bound" {
					t.Fatalf("restored to %s", id)
				}
				pmxWrite(s)
				s.provimuxAffinityStop()
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatalf("files written: %v", entries)
				}
			})
		}
	}
}

// Finding 11: in a directory that cannot be written, nothing is restored (the snapshot could
// never be replaced) and the state says why; with the switch off, the files that cannot be
// removed are named in the state.
func TestProvimuxAffinityReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix permissions, not root")
	}
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	on := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, on)
	pmxBind(t, on, "provimux-test-session", a, b)
	pmxWrite(on)
	pmxEnd(on)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	for start := 1; start <= 2; start++ {
		s := pmxService(t, dir, "", pmxOn, a, b)
		status := pmxStart(t, s)
		s.provimuxAffinityStop()
		if status.InEffect || status.Reason != provimuxReasonNotWritable || status.RestoredAtStart != nil {
			t.Fatalf("start %d: state %+v", start, status)
		}
		if _, state := pmxBound(s, "provimux-test-session"); state == "bound" {
			t.Fatalf("start %d restored from a directory it cannot write", start)
		}
	}
	off := pmxService(t, dir, "", pmxBool(false), a, b)
	status := pmxStart(t, off)
	off.provimuxAffinityStop()
	if status.Reason != provimuxReasonSwitchOff || !strings.Contains(status.LastError, "not removed") {
		t.Fatalf("state %+v", status)
	}
}

// Finding 2 and case 2: a login replaced by another account's while running. The uninterrupted
// process keeps the conversation on the auth ID (now the other account); the snapshot leaves out
// every binding of that auth ID for the rest of the run, those made after the replacement too.
func TestProvimuxAffinityLoginReplacedWhileRunning(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	if got := pmxBind(t, s, "provimux-test-on-a", a, b); got != a.ID {
		t.Fatalf("premise: on %s", got)
	}
	if got := pmxBind(t, s, "provimux-test-session", a, b); got != b.ID {
		t.Fatalf("premise: on %s", got)
	}
	replaced := pmxClaude(b.ID, "acc-c")
	if _, err := s.coreManager.Update(context.Background(), replaced); err != nil {
		t.Fatal(err)
	}
	if id, state := pmxBound(s, "provimux-test-session"); state != "bound" || id != b.ID {
		t.Fatalf("premise: upstream keeps %s (%s)", id, state)
	}
	s.provimuxAffinityTick(s.provimuxAffinityRuntime(), false) // the check that sees the replacement
	pmxBind(t, s, "provimux-test-after", replaced)
	rt := s.provimuxAffinityRuntime()
	s.provimuxAffinityStop()
	if status := rt.currentStatus(); status.LeftOut != 2 || status.LastBindings != 1 {
		t.Fatalf("state %+v", status)
	}
	next := pmxService(t, dir, "", pmxOn, a, replaced)
	pmxStart(t, next)
	defer next.provimuxAffinityStop()
	for _, session := range []string{"provimux-test-session", "provimux-test-after"} {
		if id, state := pmxBound(next, session); state == "bound" {
			t.Fatalf("%s came back on %s", session, id)
		}
	}
	if id, state := pmxBound(next, "provimux-test-on-a"); state != "bound" || id != a.ID {
		t.Fatalf("a's conversation: %s (%s)", id, state)
	}
}

// Case 2 of the third review, the paths it found: the replacement between the identity check and
// the write's own read of the accounts; a login added after the last check and replaced before
// the first that sees it; an old request completing after the replacement was seen; and, beside
// them, an account whose identity could not be read at one check, and one missing at one check.
// In each the old account's conversation never comes back on another account.
func TestProvimuxAffinityLoginRotationGaps(t *testing.T) {
	for _, scenario := range []string{"between the check and the write", "new login replaced before its first check", "old request completes after the replacement was seen", "identity unreadable at one check", "account missing at one check"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			a, b := pmxClaude("A", "acc-a"), pmxClaude("B", "acc-b")
			c := pmxClaude(b.ID, "acc-c")
			s := pmxService(t, dir, "", pmxOn, a)
			register := func(auth *coreauth.Auth) {
				if _, err := s.coreManager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			update := func(auth *coreauth.Auth) {
				if _, err := s.coreManager.Update(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "new login replaced before its first check" {
				register(b)
			}
			pmxStart(t, s)
			if scenario == "new login replaced before its first check" {
				register(b)
			} else {
				pmxWrite(s)
			}
			pmxBind(t, s, "prime", a, b)
			if got := pmxBind(t, s, "old-session", a, b); got != b.ID {
				t.Fatalf("premise: on %s", got)
			}
			rt := s.provimuxAffinityRuntime()
			switch scenario {
			case "between the check and the write":
				// The replacement lands inside the write, after its key check.
				original := provimuxAffinityOwnedByUs
				var once sync.Once
				provimuxAffinityOwnedByUs = func(info fs.FileInfo) bool { once.Do(func() { update(c) }); return original(info) }
				pmxWrite(s)
				provimuxAffinityOwnedByUs = original
			case "new login replaced before its first check":
				update(c)
				pmxWrite(s)
			case "old request completes after the replacement was seen":
				update(c)
				s.provimuxAffinityTick(rt, false)
				s.coreManager.Selector().(*coreauth.SessionAffinitySelector).OnResult(coreauth.Result{AuthID: b.ID, Provider: "mixed", Model: "provimux-test-model", Success: true, Options: pmxSessionOpts("old-session")})
				pmxWrite(s)
			case "identity unreadable at one check":
				unreadable := pmxClaude(b.ID, "")
				update(unreadable)
				s.provimuxAffinityTick(rt, false)
				update(b)
				pmxWrite(s)
				c = b // the same account again: still left out
			case "account missing at one check":
				s.coreManager.Remove(context.Background(), b.ID)
				s.provimuxAffinityTick(rt, false)
				register(b)
				pmxWrite(s)
				c = b
			}
			pmxEnd(s)
			next := pmxService(t, dir, "", pmxOn, a, c)
			status := pmxStart(t, next)
			if id, state := pmxBound(next, "old-session"); state == "bound" {
				t.Fatalf("the old conversation came back on %s (%s); restored %+v", id, state, status.RestoredAtStart)
			}
		})
	}
}

// A login replaced while stopped gets nothing.
func TestProvimuxAffinityLoginReplacedWhileStopped(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	first := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, first)
	pmxBind(t, first, "provimux-test-primer", a, b)
	if got := pmxBind(t, first, "provimux-test-session", a, b); got != b.ID {
		t.Fatalf("premise: on %s", got)
	}
	pmxWrite(first)
	pmxEnd(first)
	replaced := pmxClaude(b.ID, "acc-other")
	second := pmxService(t, dir, "", pmxOn, a, replaced)
	status := pmxStart(t, second)
	defer second.provimuxAffinityStop()
	if status.RestoredAtStart == nil || status.RestoredAtStart.UnknownAccounts != 1 {
		t.Fatalf("restored %+v", status.RestoredAtStart)
	}
	if id, state := pmxBound(second, "provimux-test-session"); state == "bound" && id == b.ID {
		t.Fatal("the replaced login got the old account's binding")
	}
}

// A failed write (full disk) leaves the last good snapshot and no partial file; the next one
// succeeds.
func TestProvimuxAffinityFullDisk(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxWrite(s)
	snapshot, _ := pmxPaths(t, s)
	good := pmxSum(t, snapshot)
	previous := provimuxAffinityWrite
	provimuxAffinityWrite = func(path string, data []byte, publish func(string) error) error {
		return fmt.Errorf("write %s: %w", path, syscall.ENOSPC)
	}
	defer func() { provimuxAffinityWrite = previous }()
	pmxBind(t, s, "provimux-test-later", a, b)
	if status := pmxWrite(s); !strings.Contains(status.LastError, "no space") || !status.InEffect {
		t.Fatalf("status %+v", status)
	}
	if pmxSum(t, snapshot) != good {
		t.Fatal("a failed write changed the last good snapshot")
	}
	provimuxAffinityWrite = previous
	if status := pmxWrite(s); status.LastError != "" || status.LastBindings != 2 {
		t.Fatalf("after the disk recovered: %+v", status)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("temporary file left: %s", entry.Name())
		}
	}
}

// The stop has a deadline on a stalled disk, and the state is read without waiting on the disk.
func TestProvimuxAffinityStopDeadline(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxStart(t, s)
	release, written := make(chan struct{}), make(chan struct{})
	previous := provimuxAffinityWrite
	provimuxAffinityWrite = func(path string, data []byte, publish func(string) error) error {
		defer close(written)
		<-release
		return previous(path, data, publish)
	}
	defer func() { provimuxAffinityWrite = previous }()
	// The abandoned write finishes before the test's directory is removed.
	defer func() { close(release); <-written }()
	start := time.Now()
	stopped := make(chan struct{})
	rt := s.provimuxAffinityRuntime()
	go func() { s.provimuxAffinityStop(); close(stopped) }()
	time.Sleep(100 * time.Millisecond)
	readStart := time.Now()
	_ = rt.currentStatus()
	if read := time.Since(readStart); read > 50*time.Millisecond {
		t.Fatalf("the state read waited %s on the disk", read)
	}
	<-stopped
	if took := time.Since(start); took > provimuxAffinityStopDeadline+500*time.Millisecond {
		t.Fatalf("the stop took %s on a stalled disk", took)
	}
}

// Case 4 of the third review, in one process: a stop gave up on a stalled write; a successor
// service of the same store starts, binds, writes and stops; then the stalled write is released.
// It never lands: the successor's file stands (or, while the old writer still held the store, the
// last one written before it). The successor's state says in-use while it waits.
func TestProvimuxAffinityGivenUpWriteNeverLands(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	pmxBind(t, s, "original", a, b)
	pmxWrite(s)
	pmxBind(t, s, "first-service-only", a, b)
	snapshot, key := pmxPaths(t, s)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	previous := provimuxAffinityWrite
	provimuxAffinityWrite = func(path string, data []byte, publish func(string) error) error {
		first := false
		once.Do(func() { first = true })
		if !first {
			return previous(path, data, publish)
		}
		close(entered)
		<-release
		defer close(finished)
		return previous(path, data, publish)
	}
	defer func() { provimuxAffinityWrite = previous }()
	ended := make(chan struct{})
	go func() { s.provimuxAffinityStop(); close(ended) }()
	<-entered
	<-ended

	next := pmxService(t, dir, "", pmxOn, a, b)
	status := pmxStart(t, next)
	pmxBind(t, next, "new-service-only", a, b)
	waiting := pmxWrite(next)
	next.provimuxAffinityStop()
	if status.RestoredAtStart == nil || waiting.Reason != provimuxReasonInUse || waiting.InEffect {
		t.Fatalf("successor: start %+v, write %+v", status, waiting)
	}
	close(release)
	<-finished
	if sessions := strings.Join(pmxSessions(t, snapshot, key), " "); strings.Contains(sessions, "first-service-only") {
		t.Fatalf("the given-up write landed: %s", sessions)
	}
	// The store is free again: the next service writes.
	third := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, third)
	pmxBind(t, third, "third-service", a, b)
	if written := pmxWrite(third); written.Reason != "" || written.LastError != "" {
		t.Fatalf("third: %+v", written)
	}
	third.provimuxAffinityStop()
	sessions := strings.Join(pmxSessions(t, snapshot, key), " ")
	if !strings.Contains(sessions, "third-service") || strings.Contains(sessions, "first-service-only") {
		t.Fatalf("sessions %s", sessions)
	}
}

// Case 4: two services of one store in one process: the second writes nothing while the first
// holds the store, and says so.
func TestProvimuxAffinityOneWriterPerStore(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	first := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, first)
	pmxBind(t, first, "first", a, b)
	pmxWrite(first)
	second := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, second)
	pmxBind(t, second, "second", a, b)
	if status := pmxWrite(second); status.InEffect || status.Reason != provimuxReasonInUse {
		t.Fatalf("second: %+v", status)
	}
	snapshot, key := pmxPaths(t, first)
	if sessions := strings.Join(pmxSessions(t, snapshot, key), " "); strings.Contains(sessions, "second") {
		t.Fatalf("the second service wrote: %s", sessions)
	}
	first.provimuxAffinityStop()
	if status := pmxWrite(second); status.Reason != "" || !status.InEffect {
		t.Fatalf("second after the first stopped: %+v", status)
	}
	second.provimuxAffinityStop()
}

// The switch: the reasons a client is told; a reload that turns it off removes the files within
// one check, on again writes.
func TestProvimuxAffinitySwitch(t *testing.T) {
	cfg := &config.Config{}
	if _, _, on, reason := provimuxAffinityMode(cfg); on || reason != provimuxReasonAffinityOff {
		t.Fatalf("affinity off: %v %s", on, reason)
	}
	cfg.Routing.SessionAffinity = true
	cfg.Routing.SessionAffinityPersist = pmxOn
	if setting, set, on, reason := provimuxAffinityMode(cfg); !setting || !set || !on || reason != "" {
		t.Fatalf("on: %v %v %v %s", setting, set, on, reason)
	}
	cfg.Routing.SessionAffinityPersist = pmxBool(false)
	if _, set, on, reason := provimuxAffinityMode(cfg); !set || on || reason != provimuxReasonSwitchOff {
		t.Fatalf("off: %v %v %s", set, on, reason)
	}
	cfg.Routing.SessionAffinityPersist = nil
	if setting, set, on, reason := provimuxAffinityMode(cfg); !setting || set || !on || reason != "" {
		t.Fatalf("not set: %v %v %v %s", setting, set, on, reason)
	}
	cfg.Routing.SessionAffinityPersist = pmxOn
	cfg.KVStore.Disabled = true
	if _, _, on, reason := provimuxAffinityMode(cfg); on || reason != provimuxReasonKVStoreOff {
		t.Fatalf("kv off: %v %s", on, reason)
	}
	cfg.Home.Enabled = true
	if _, _, on, reason := provimuxAffinityMode(cfg); on || reason != provimuxReasonHome {
		t.Fatalf("home: %v %s", on, reason)
	}

	SetProvimuxAffinitySnapshotInterval(50 * time.Millisecond)
	defer SetProvimuxAffinitySnapshotInterval(0)
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxStart(t, s)
	defer s.provimuxAffinityStop()
	snapshot, key := pmxPaths(t, s)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("a periodic snapshot", func() bool { return pmxExists(snapshot) })
	reload := func(persist *bool) {
		next := *s.cfg
		next.Routing.SessionAffinityPersist = persist
		s.cfgMu.Lock()
		s.cfg = &next
		s.cfgMu.Unlock()
	}
	// Off, then back on: by setting it true, and by removing the line (not set means on).
	for _, on := range []*bool{pmxOn, nil} {
		reload(pmxBool(false))
		waitFor("the files removed", func() bool { return !pmxExists(snapshot) && !pmxExists(key) })
		pmxWrite(s)
		if pmxExists(snapshot) || pmxExists(key) {
			t.Fatal("a file was written with the switch off")
		}
		if status := s.provimuxAffinityRuntime().currentStatus(); status.InEffect || status.Reason != provimuxReasonSwitchOff || status.Setting || !status.Set {
			t.Fatalf("status %+v", status)
		}
		reload(on)
		waitFor("a snapshot after the switch is back on", func() bool { return pmxExists(snapshot) })
		if status := s.provimuxAffinityRuntime().currentStatus(); !status.InEffect || status.Reason != "" || !status.Setting || status.Set != (on != nil) {
			t.Fatalf("back on (set %v): status %+v", on != nil, status)
		}
	}
}

// What one snapshot costs with 10,000 sessions, and how long the session cache's read lock is held
// for the copy.
func TestProvimuxAffinitySnapshotCost(t *testing.T) {
	if testing.Short() {
		t.Skip("cost measurement")
	}
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", pmxOn, a, b)
	pmxStart(t, s)
	selector := s.coreManager.Selector()
	for i := 0; i < 10000; i++ {
		if _, err := selector.Pick(context.Background(), "mixed", "provimux-test-model", pmxSessionOpts(fmt.Sprintf("provimux-test-session-%d", i)), []*coreauth.Auth{a, b}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := coreauth.ProvimuxAffinityCurrentPin(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	state, err := coreauth.ProvimuxAffinityCapture(selector, func(string, time.Time) (string, bool) { return "account", true }, time.Now())
	copied := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	status := pmxWrite(s)
	total := time.Since(start)
	if status.LastError != "" || !status.InEffect {
		t.Fatalf("state %+v", status)
	}
	snapshot, _ := pmxPaths(t, s)
	info, _ := os.Stat(snapshot)
	t.Logf("10,000 sessions (%d captured): copy %s; whole snapshot (copy, encrypt, write, sync, rename) %s; file %d bytes",
		len(state.Sessions), copied, total, info.Size())
}
