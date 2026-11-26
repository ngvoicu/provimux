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

func pmxClaude(id, account string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"account_uuid": account, "organization_uuid": "org-" + account, "email": "shared@example.invalid"}}
}

// pmxService is a service with session affinity, its selector built the way Run builds it, its
// KV store at kvPath ("" = <dir>/kv.db).
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
	t.Cleanup(func() { provimuxAffinityRuntimes.Delete(s) })
	return s
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

// Findings 12 and 6: the files are named from the store's own path; two stores in one directory
// never share them, and two services never touch each other's files or stop each other's writer.
func TestProvimuxAffinityFilesBelongToTheirStore(t *testing.T) {
	SetProvimuxAffinitySnapshotInterval(40 * time.Millisecond)
	defer SetProvimuxAffinitySnapshotInterval(0)
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	one := pmxService(t, dir, filepath.Join(dir, "one.db"), nil, a, b)
	two := pmxService(t, dir, filepath.Join(dir, "two.db"), nil, a, b)
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
	deadline := time.Now().Add(3 * time.Second)
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
	time.Sleep(200 * time.Millisecond)
	if after := twoRT.(*provimuxAffinityRuntime).currentStatus().LastWrittenAt; after == nil || before == nil || !after.After(*before) {
		t.Fatal("stopping one service stopped the other's writer")
	}
	two.provimuxAffinityStop()
	for _, path := range []string{oneSnapshot, oneKey, twoSnapshot, twoKey} {
		if !pmxExists(path) {
			t.Fatalf("%s is gone", path)
		}
	}
	// Each store restores its own: two's snapshot holds no binding of one.
	fresh := pmxService(t, dir, filepath.Join(dir, "two.db"), nil, a, b)
	if status := pmxStart(t, fresh); status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 0 {
		t.Fatalf("two restored %+v (%s)", status.RestoredAtStart, status.StartNote)
	}
	fresh.provimuxAffinityStop()
	again := pmxService(t, dir, filepath.Join(dir, "one.db"), nil, a, b)
	if status := pmxStart(t, again); status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 2 {
		t.Fatalf("one restored %+v (%s)", status.RestoredAtStart, status.StartNote)
	}
	again.provimuxAffinityStop()
}

// Finding 12: a file at our names that is not ours (a bbolt database, a key of something else, a
// link) is never replaced or removed, nothing is written or restored, and the state says so; with
// the switch on or off.
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
	bboltIntact := func(t *testing.T, path string) {
		db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second, ReadOnly: true})
		if err != nil {
			t.Fatalf("%s no longer opens as bbolt: %v", path, err)
		}
		defer func() { _ = db.Close() }()
		if errView := db.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("provimux-test"))
			if bucket == nil || string(bucket.Get([]byte("sentinel"))) != "still here" {
				return fmt.Errorf("sentinel lost")
			}
			return nil
		}); errView != nil {
			t.Fatal(errView)
		}
	}
	for _, which := range []string{"snapshot", "key", "link"} {
		for _, persist := range []*bool{nil, pmxBool(false)} {
			t.Run(fmt.Sprintf("%s, setting %v", which, persist != nil), func(t *testing.T) {
				dir := t.TempDir()
				s := pmxService(t, dir, "", persist, a, b)
				snapshot, key := pmxPaths(t, s)
				foreign := snapshot
				if which == "key" {
					foreign = key
				}
				if which == "link" {
					target := filepath.Join(dir, "elsewhere")
					if err := os.WriteFile(target, []byte(provimuxAffinityMagic+"not really"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, snapshot); err != nil {
						t.Fatal(err)
					}
				} else {
					bboltAt(t, foreign)
				}
				sum := func() [32]byte {
					data, _ := os.ReadFile(foreign)
					return sha256.Sum256(data)
				}
				before := sum()
				pmxBind(t, s, "provimux-test-session", a, b)
				status := pmxStart(t, s)
				pmxWrite(s)
				status2 := s.provimuxAffinityRuntime().currentStatus()
				s.provimuxAffinityStop()
				if sum() != before {
					t.Fatalf("%s changed", foreign)
				}
				if which == "link" {
					if info, err := os.Lstat(snapshot); err != nil || info.Mode()&fs.ModeSymlink == 0 {
						t.Fatal("the link was replaced")
					}
				} else {
					bboltIntact(t, foreign)
				}
				if persist == nil {
					if status.InEffect || status.Reason != provimuxReasonForeignFile || status2.Reason != provimuxReasonForeignFile || !strings.Contains(status.Detail, foreign) {
						t.Fatalf("state %+v / %+v", status, status2)
					}
					if status.RestoredAtStart != nil {
						t.Fatalf("restored %+v", status.RestoredAtStart)
					}
					other := snapshot
					if which == "snapshot" || which == "link" {
						other = key
					}
					if pmxExists(other) {
						t.Fatalf("%s was written beside a foreign file", other)
					}
				}
			})
		}
	}
}

// Finding 9: the key is ours and private: made 0600; a key we own with a wider mode is set to
// 0600 and the state says so; a key owned by another user is not used.
func TestProvimuxAffinityKeyIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	dir := t.TempDir()
	s := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, s, "provimux-test-primer", a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxWrite(s)
	snapshot, key := pmxPaths(t, s)
	for _, path := range []string{snapshot, key} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}

	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	repaired := pmxService(t, dir, "", nil, a, b)
	status := pmxStart(t, repaired)
	repaired.provimuxAffinityStop()
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 || !strings.Contains(status.KeyNote, "set to 0600") || status.RestoredAtStart == nil || status.RestoredAtStart.Sessions != 2 {
		t.Fatalf("mode %v, state %+v", info.Mode().Perm(), status)
	}

	previous := provimuxAffinityOwnedByUs
	provimuxAffinityOwnedByUs = func(fs.FileInfo) bool { return false }
	defer func() { provimuxAffinityOwnedByUs = previous }()
	written := pmxSum(t, snapshot)
	other := pmxService(t, dir, "", nil, a, b)
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
// sent, model names, the derived identities of conversations without an id, account tokens; no
// client key, caller-scope hash, login file name, e-mail, account id, or conversation content.
// Without the key the file shows none of them.
func TestProvimuxAffinityWhatTheFileHolds(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a@example.invalid.json", "acc-a-uuid"), pmxClaude("provimux-test-b@example.invalid.json", "acc-b-uuid")
	s := pmxService(t, dir, "", nil, a, b)
	const clientKey = "provimux-test-client-key-secret"
	scope := cliproxysession.CallerScope(clientKey)
	selector := s.coreManager.Selector()
	explicit := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"provimux-test-session-id"}}, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: scope}}
	if _, err := selector.Pick(context.Background(), "mixed", "provimux-test-model", explicit, []*coreauth.Auth{a, b}); err != nil {
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
	keyData, _ := os.ReadFile(key)
	plain, err := provimuxAffinityUnseal(keyData[len(provimuxAffinityKeyMagic):], data)
	if err != nil {
		t.Fatal(err)
	}
	derived := cliproxysession.DerivedID(noID.Metadata)
	for _, held := range []string{"provimux-test-session-id", "provimux-test-model", derived} {
		if !bytes.Contains(plain, []byte(held)) {
			t.Errorf("the plaintext does not hold %q", held)
		}
		if bytes.Contains(data, []byte(held)) {
			t.Errorf("the file holds %q without the key", held)
		}
	}
	for _, absent := range []string{clientKey, scope, a.ID, b.ID, "provimux-test-a@example", "shared@example.invalid", "acc-a-uuid", "org-acc-a-uuid", "provimux-test question text", "provimux-test system prompt"} {
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
	s := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, s, "provimux-test-primer", a, b)
	bound := pmxBind(t, s, "provimux-test-session", a, b)
	pmxWrite(s)
	snapshot, _ := pmxPaths(t, s)
	written := pmxSum(t, snapshot)
	for start := 1; start <= 2; start++ {
		// A crash: no stop. The next process starts from the same snapshot.
		next := pmxService(t, dir, "", nil, a, b)
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
	}
	later := pmxService(t, dir, "", nil, a, b)
	pmxStart(t, later)
	pmxBind(t, later, "provimux-test-new", a, b)
	pmxWrite(later)
	later.provimuxAffinityStop()
	if pmxSum(t, snapshot) == written {
		t.Fatal("the next write did not replace the snapshot")
	}
}

// Finding 11: whenever the feature is not in effect, for any reason and any combination of them,
// our files from an earlier run are removed, nothing is restored and nothing is written; with the
// KV store disabled an upgrade creates no file.
func TestProvimuxAffinityOffMeansGone(t *testing.T) {
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	causes := []string{"switch off", "session affinity off", "home mode", "kv store disabled"}
	for mask := 1; mask < 1<<len(causes); mask++ {
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
					on := pmxService(t, dir, "", nil, a, b)
					pmxBind(t, on, "provimux-test-session", a, b)
					pmxWrite(on)
				}
				s := pmxService(t, dir, "", nil, a, b)
				if mask&1 != 0 {
					s.cfg.Routing.SessionAffinityPersist = pmxBool(false)
				}
				if mask&2 != 0 {
					s.cfg.Routing.SessionAffinity = false
				}
				if mask&4 != 0 {
					s.cfg.Home.Enabled = true
				}
				if mask&8 != 0 {
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
	on := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, on, "provimux-test-session", a, b)
	pmxWrite(on)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	for start := 1; start <= 2; start++ {
		s := pmxService(t, dir, "", nil, a, b)
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

// Finding 2: a login replaced by another account's while running. The uninterrupted process keeps
// a conversation on the auth ID (now the other account); the snapshot leaves out bindings last
// used before the replacement was seen, and keeps those made after it.
func TestProvimuxAffinityLoginReplacedWhileRunning(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", nil, a, b)
	pmxStart(t, s)
	pmxBind(t, s, "provimux-test-primer", a, b)
	if got := pmxBind(t, s, "provimux-test-session", a, b); got != b.ID {
		t.Fatalf("premise: on %s", got)
	}
	replaced := pmxClaude(b.ID, "acc-c")
	if _, err := s.coreManager.Update(context.Background(), replaced); err != nil {
		t.Fatal(err)
	}
	// The uninterrupted process: the session stays on the auth ID, now account c.
	if id, state := pmxBound(s, "provimux-test-session"); state != "bound" || id != b.ID {
		t.Fatalf("premise: upstream keeps %s (%s)", id, state)
	}
	s.provimuxAffinityTick(s.provimuxAffinityRuntime(), false) // the check that sees the replacement
	time.Sleep(5 * time.Millisecond)
	pmxBind(t, s, "provimux-test-after", replaced, a)
	afterOn, _ := pmxBound(s, "provimux-test-after")
	rt := s.provimuxAffinityRuntime()
	s.provimuxAffinityStop()
	status := rt.currentStatus()
	if status.LeftOut != 1 {
		t.Fatalf("left out %d, want 1: %+v", status.LeftOut, status)
	}
	next := pmxService(t, dir, "", nil, a, replaced)
	pmxStart(t, next)
	defer next.provimuxAffinityStop()
	if id, state := pmxBound(next, "provimux-test-session"); state == "bound" {
		t.Fatalf("the old account's conversation came back on %s", id)
	}
	if id, state := pmxBound(next, "provimux-test-after"); state != "bound" || id != afterOn {
		t.Fatalf("a conversation made after the replacement: %s (%s), was %s", id, state, afterOn)
	}
}

// A login replaced while stopped gets nothing.
func TestProvimuxAffinityLoginReplacedWhileStopped(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	first := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, first, "provimux-test-primer", a, b)
	if got := pmxBind(t, first, "provimux-test-session", a, b); got != b.ID {
		t.Fatalf("premise: on %s", got)
	}
	pmxWrite(first)
	replaced := pmxClaude(b.ID, "acc-other")
	second := pmxService(t, dir, "", nil, a, replaced)
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
	s := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxWrite(s)
	snapshot, _ := pmxPaths(t, s)
	good := pmxSum(t, snapshot)
	previous := provimuxAffinityWrite
	provimuxAffinityWrite = func(path string, data []byte) error {
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
	s := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxStart(t, s)
	release, written := make(chan struct{}), make(chan struct{})
	previous := provimuxAffinityWrite
	provimuxAffinityWrite = func(path string, data []byte) error {
		defer close(written)
		<-release
		return previous(path, data)
	}
	defer func() { provimuxAffinityWrite = previous }()
	// The abandoned write finishes before the test's directory is removed.
	defer func() { close(release); <-written }()
	start := time.Now()
	stopped := make(chan struct{})
	go func() { s.provimuxAffinityStop(); close(stopped) }()
	time.Sleep(100 * time.Millisecond)
	readStart := time.Now()
	_ = s.provimuxAffinityRuntime().currentStatus()
	if read := time.Since(readStart); read > 50*time.Millisecond {
		t.Fatalf("the state read waited %s on the disk", read)
	}
	<-stopped
	if took := time.Since(start); took > provimuxAffinityStopDeadline+500*time.Millisecond {
		t.Fatalf("the stop took %s on a stalled disk", took)
	}
}

// The switch: on by default with session affinity; the reasons a client is told; a reload that
// turns it off removes the files within one check, on again writes.
func TestProvimuxAffinitySwitch(t *testing.T) {
	cfg := &config.Config{}
	if _, _, on, reason := provimuxAffinityMode(cfg); on || reason != provimuxReasonAffinityOff {
		t.Fatalf("affinity off: %v %s", on, reason)
	}
	cfg.Routing.SessionAffinity = true
	if setting, set, on, reason := provimuxAffinityMode(cfg); !setting || set || !on || reason != "" {
		t.Fatalf("default: %v %v %v %s", setting, set, on, reason)
	}
	cfg.Routing.SessionAffinityPersist = pmxBool(false)
	if _, set, on, reason := provimuxAffinityMode(cfg); !set || on || reason != provimuxReasonSwitchOff {
		t.Fatalf("off: %v %v %s", set, on, reason)
	}
	cfg.Routing.SessionAffinityPersist = nil
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
	s := pmxService(t, dir, "", nil, a, b)
	pmxBind(t, s, "provimux-test-session", a, b)
	pmxStart(t, s)
	defer s.provimuxAffinityStop()
	snapshot, key := pmxPaths(t, s)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
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
	reload(pmxBool(false))
	waitFor("the files removed", func() bool { return !pmxExists(snapshot) && !pmxExists(key) })
	time.Sleep(200 * time.Millisecond)
	if pmxExists(snapshot) || pmxExists(key) {
		t.Fatal("a file was written with the switch off")
	}
	if status := s.provimuxAffinityRuntime().currentStatus(); status.InEffect || status.Reason != provimuxReasonSwitchOff {
		t.Fatalf("status %+v", status)
	}
	reload(nil)
	waitFor("a snapshot after the switch is back on", func() bool { return pmxExists(snapshot) })
}

// What one snapshot costs with 10,000 sessions, and how long the session cache's read lock is held
// for the copy.
func TestProvimuxAffinitySnapshotCost(t *testing.T) {
	if testing.Short() {
		t.Skip("cost measurement")
	}
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	s := pmxService(t, dir, "", nil, a, b)
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
