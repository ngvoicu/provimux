package cliproxy

// Provimux layer: routing.session-affinity-persist (off unless set; provimuxAffinityPersistDefault).
// Session affinity's session cache is written when the process is told to stop (just before
// upstream's StopAutoRefresh, before the HTTP drain) and every 5 minutes, and put back once at the
// next start, before the server exists (sdk/cliproxy/auth/provimux_affinity_snapshot.go). Nothing
// here runs on a request. Design: .github/provimux/design/session-affinity-persist.md.
//
// On disk, named from the KV store's own path (so two stores never share them):
//   - <kv path>.affinity-key: "PMXAFFKEY2\n", 32 random bytes, and a 16-byte check value (an HMAC of
//     the bytes under themselves); made once, 0600, owned by the user running the gateway;
//   - <kv path>.affinity: "PMXAFF2\n", a nonce, then AES-256-GCM of the snapshot under the key.
//
// Ours means: it authenticates. A key file is ours only with exactly that format, length and check
// value; a snapshot only when it decrypts and authenticates under the key beside it. Anything else
// at those names, a damaged file of ours included, is never replaced, renamed over or removed;
// nothing is written or restored, and the state endpoint says so.
//
// One writer per store: the writer holds an exclusive lock on the key file for the life of its
// service (the kernel releases it when the process ends), and checks, just before the rename that
// publishes a snapshot, that it still holds it and was not cancelled (a stop that gave up).

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// provimuxAffinityPersistDefault is routing.session-affinity-persist when the configuration does not
// set it.
const provimuxAffinityPersistDefault = false

const (
	provimuxAffinitySuffix    = ".affinity"
	provimuxAffinityKeySuffix = ".affinity-key"
	provimuxAffinityMagic     = "PMXAFF2\n"
	provimuxAffinityKeyMagic  = "PMXAFFKEY2\n"
	provimuxAffinityKeySize   = 32
	provimuxAffinityKeyCheck  = 16
	provimuxAffinityCheck     = 5 * time.Second
	provimuxAffinityPeriod    = 5 * time.Minute
)

// Reasons the state endpoint gives when the bindings are not being kept.
const (
	provimuxReasonAffinityOff  = "session-affinity-off"
	provimuxReasonHome         = "home-mode"
	provimuxReasonKVStoreOff   = "kv-store-off"
	provimuxReasonSwitchOff    = "switch-off"
	provimuxReasonForeignFile  = "foreign-file"
	provimuxReasonKeyPrivate   = "key-not-private"
	provimuxReasonNotWritable  = "directory-not-writable"
	provimuxReasonPathUnusable = "path-unusable"
	provimuxReasonInUse        = "in-use"
)

var (
	provimuxAffinityIntervalMu sync.RWMutex
	provimuxAffinityInterval   = provimuxAffinityPeriod
	// provimuxAffinityStopDeadline bounds what the stop adds to the exit on a stalled disk.
	provimuxAffinityStopDeadline = 1500 * time.Millisecond
	// provimuxAffinityBeforeWrite, when set, runs before each snapshot's temporary file is
	// written (SetProvimuxAffinityBeforeWrite). For tests.
	provimuxAffinityBeforeWrite atomic.Pointer[func()]
)

// SetProvimuxAffinitySnapshotInterval sets how often the bindings are written while running
// (default 5 minutes). For tests.
func SetProvimuxAffinitySnapshotInterval(interval time.Duration) {
	provimuxAffinityIntervalMu.Lock()
	defer provimuxAffinityIntervalMu.Unlock()
	if interval <= 0 {
		interval = provimuxAffinityPeriod
	}
	provimuxAffinityInterval = interval
}

// SetProvimuxAffinityBeforeWrite runs hook before each snapshot write (nil: none). For tests that
// stall the disk.
func SetProvimuxAffinityBeforeWrite(hook func()) {
	if hook == nil {
		provimuxAffinityBeforeWrite.Store(nil)
		return
	}
	provimuxAffinityBeforeWrite.Store(&hook)
}

func provimuxAffinitySnapshotInterval() time.Duration {
	provimuxAffinityIntervalMu.RLock()
	defer provimuxAffinityIntervalMu.RUnlock()
	return provimuxAffinityInterval
}

// ProvimuxAffinityPersistStatus is what GET /v8/management/provimux/session-affinity-persist answers.
type ProvimuxAffinityPersistStatus struct {
	// Setting is routing.session-affinity-persist as it applies (the default when not set).
	Setting bool `json:"setting"`
	// Set reports whether the config file sets it.
	Set bool `json:"set"`
	// InEffect: bindings are being kept across restarts.
	InEffect bool `json:"in_effect"`
	// Reason when not in effect: session-affinity-off, home-mode, kv-store-off or switch-off (the
	// configuration; our files are removed), or foreign-file, key-not-private,
	// directory-not-writable, path-unusable or in-use (nothing is written or removed).
	Reason string `json:"reason,omitempty"`
	// Detail says which file and why, for the file reasons.
	Detail string `json:"detail,omitempty"`
	// Snapshot and Key are the two files' paths.
	Snapshot string `json:"snapshot,omitempty"`
	Key      string `json:"key,omitempty"`
	// KeyNote: the key's mode was set back to 0600.
	KeyNote       string     `json:"key_note,omitempty"`
	LastWrittenAt *time.Time `json:"last_written_at,omitempty"`
	LastBindings  int        `json:"last_bindings"`
	// LeftOut counts bindings the last write left out because their account's identity was not
	// the same throughout, from their last use to the write.
	LeftOut         int                                `json:"left_out_account_unverified"`
	LastError       string                             `json:"last_error,omitempty"`
	RestoredAtStart *coreauth.ProvimuxAffinityRestored `json:"restored_at_start,omitempty"`
	StartNote       string                             `json:"start_note,omitempty"`
}

// provimuxAffinityIdentity is what an auth ID was seen to be during this run: a hash of its stable
// vendor identity ("" when it has none), since when it has been watched (zero: since the start),
// and whether it ever changed or could not be read.
type provimuxAffinityIdentity struct {
	identity string
	since    time.Time
	tainted  bool
}

// provimuxAffinityRuntime is one service's state. writeMu serializes disk work and is never held
// by a request or by a status read; statusMu is held only to copy the status.
type provimuxAffinityRuntime struct {
	writeMu    sync.Mutex
	identities map[string]provimuxAffinityIdentity // under writeMu
	lock       *os.File                            // the held lock on the key file; under writeMu
	verified   fs.FileInfo                         // the snapshot as last authenticated; under writeMu
	cancelled  atomic.Bool                         // a stop gave up: no write may land any more
	statusMu   sync.Mutex
	status     ProvimuxAffinityPersistStatus
	stop       chan struct{}
	done       chan struct{}
}

var provimuxAffinityRuntimes sync.Map // *Service -> *provimuxAffinityRuntime

func (s *Service) provimuxAffinityRuntime() *provimuxAffinityRuntime {
	value, _ := provimuxAffinityRuntimes.LoadOrStore(s, &provimuxAffinityRuntime{identities: map[string]provimuxAffinityIdentity{}})
	return value.(*provimuxAffinityRuntime)
}

func (rt *provimuxAffinityRuntime) currentStatus() ProvimuxAffinityPersistStatus {
	rt.statusMu.Lock()
	defer rt.statusMu.Unlock()
	return rt.status
}

func (rt *provimuxAffinityRuntime) update(change func(*ProvimuxAffinityPersistStatus)) {
	rt.statusMu.Lock()
	defer rt.statusMu.Unlock()
	change(&rt.status)
}

// provimuxAffinityMode says whether persistence applies under cfg, and why not.
func provimuxAffinityMode(cfg *config.Config) (setting, set, on bool, reason string) {
	return provimuxAffinityModeWith(cfg, provimuxAffinityPersistDefault)
}

// provimuxAffinityModeWith is provimuxAffinityMode with the default given (tests cover both).
func provimuxAffinityModeWith(cfg *config.Config, byDefault bool) (setting, set, on bool, reason string) {
	setting = byDefault
	if cfg != nil && cfg.Routing.SessionAffinityPersist != nil {
		set = true
		setting = *cfg.Routing.SessionAffinityPersist
	}
	switch {
	case cfg == nil || !cfg.Routing.SessionAffinity:
		return setting, set, false, provimuxReasonAffinityOff
	case cfg.Home.Enabled || home.Current() != nil:
		return setting, set, false, provimuxReasonHome
	case cfg.KVStore.Disabled:
		return setting, set, false, provimuxReasonKVStoreOff
	case !setting:
		return setting, set, false, provimuxReasonSwitchOff
	}
	return setting, set, true, ""
}

// provimuxAffinityPaths are the snapshot and the key, named from the KV store's own path.
func provimuxAffinityPaths(cfg *config.Config) (snapshot, key string, err error) {
	if cfg == nil {
		return "", "", errors.New("no config")
	}
	resolved, errResolve := kvstore.ResolvePath(cfg.EffectiveKVStorePath())
	if errResolve != nil {
		return "", "", errResolve
	}
	return resolved + provimuxAffinitySuffix, resolved + provimuxAffinityKeySuffix, nil
}

// provimuxAffinityBlocked is a reason not to touch the files, with its detail.
type provimuxAffinityBlocked struct{ reason, detail string }

func (b *provimuxAffinityBlocked) Error() string { return b.reason + ": " + b.detail }

func provimuxAffinityNotOurs(path, what string) error {
	return &provimuxAffinityBlocked{provimuxReasonForeignFile, fmt.Sprintf(
		"%s is not a Provimux session-affinity %s that authenticates (another program's file, or a damaged one): it is left as it is; remove it by hand", path, what)}
}

func provimuxAffinityMAC(secret []byte, label string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

func provimuxAffinityKeyCheckValue(secret []byte) []byte {
	return provimuxAffinityMAC(secret, "provimux affinity key check v2")[:provimuxAffinityKeyCheck]
}

// provimuxAffinityKeyBytes is the key file's content for secret.
func provimuxAffinityKeyBytes(secret []byte) []byte {
	out := append([]byte(provimuxAffinityKeyMagic), secret...)
	return append(out, provimuxAffinityKeyCheckValue(secret)...)
}

// provimuxAffinityReadKey reads the key without creating it: (nil, "", nil) when absent. It is
// used only when it is ours (format, length, check value) and a regular file owned by the running
// user with no group or other access; a key of ours with a wider mode is set to 0600 (note).
func provimuxAffinityReadKey(path string) (secret []byte, note string, err error) {
	info, errStat := os.Lstat(path)
	if errStat != nil {
		if errors.Is(errStat, fs.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", &provimuxAffinityBlocked{provimuxReasonPathUnusable, errStat.Error()}
	}
	if !info.Mode().IsRegular() {
		return nil, "", provimuxAffinityNotOurs(path, "key")
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, "", &provimuxAffinityBlocked{provimuxReasonPathUnusable, errRead.Error()}
	}
	size := len(provimuxAffinityKeyMagic) + provimuxAffinityKeySize + provimuxAffinityKeyCheck
	if len(data) != size || !bytes.HasPrefix(data, []byte(provimuxAffinityKeyMagic)) {
		return nil, "", provimuxAffinityNotOurs(path, "key")
	}
	secret = data[len(provimuxAffinityKeyMagic) : len(provimuxAffinityKeyMagic)+provimuxAffinityKeySize]
	if !hmac.Equal(data[len(data)-provimuxAffinityKeyCheck:], provimuxAffinityKeyCheckValue(secret)) {
		return nil, "", provimuxAffinityNotOurs(path, "key")
	}
	if !provimuxAffinityOwnedByUs(info) {
		return nil, "", &provimuxAffinityBlocked{provimuxReasonKeyPrivate, path + " belongs to another user"}
	}
	if provimuxAffinityUnixModes && info.Mode().Perm()&0o077 != 0 {
		if errChmod := os.Chmod(path, 0o600); errChmod != nil {
			return nil, "", &provimuxAffinityBlocked{provimuxReasonKeyPrivate, fmt.Sprintf("%s is %04o and cannot be made 0600: %v", path, info.Mode().Perm(), errChmod)}
		}
		note = fmt.Sprintf("%s was %04o; set to 0600", path, info.Mode().Perm())
	}
	return secret, note, nil
}

// provimuxAffinityMakeKey creates the key (it must not exist).
func provimuxAffinityMakeKey(path string) ([]byte, error) {
	secret := make([]byte, provimuxAffinityKeySize)
	if _, errRand := rand.Read(secret); errRand != nil {
		return nil, errRand
	}
	file, errCreate := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errCreate != nil {
		return nil, errCreate
	}
	_, errWrite := file.Write(provimuxAffinityKeyBytes(secret))
	errSync := file.Sync()
	errClose := file.Close()
	if errWrite != nil || errSync != nil || errClose != nil {
		// The file is the one this call created, still unused.
		_ = os.Remove(path)
		return nil, errors.Join(errWrite, errSync, errClose)
	}
	return secret, nil
}

func provimuxAffinityAEAD(secret []byte) (cipher.AEAD, error) {
	block, errBlock := aes.NewCipher(provimuxAffinityMAC(secret, "provimux affinity snapshot v2"))
	if errBlock != nil {
		return nil, errBlock
	}
	return cipher.NewGCM(block)
}

func provimuxAffinitySeal(secret []byte, state *coreauth.ProvimuxAffinityState) ([]byte, error) {
	plain, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		return nil, errMarshal
	}
	aead, errAEAD := provimuxAffinityAEAD(secret)
	if errAEAD != nil {
		return nil, errAEAD
	}
	nonce := make([]byte, aead.NonceSize())
	if _, errRand := rand.Read(nonce); errRand != nil {
		return nil, errRand
	}
	out := append([]byte(provimuxAffinityMagic), nonce...)
	return aead.Seal(out, nonce, plain, []byte(provimuxAffinityMagic)), nil
}

func provimuxAffinityUnseal(secret, data []byte) ([]byte, error) {
	if len(secret) != provimuxAffinityKeySize {
		return nil, errors.New("no key")
	}
	aead, errAEAD := provimuxAffinityAEAD(secret)
	if errAEAD != nil {
		return nil, errAEAD
	}
	if !bytes.HasPrefix(data, []byte(provimuxAffinityMagic)) || len(data) < len(provimuxAffinityMagic)+aead.NonceSize() {
		return nil, errors.New("not a Provimux session-affinity snapshot")
	}
	body := data[len(provimuxAffinityMagic):]
	plain, errOpen := aead.Open(nil, body[:aead.NonceSize()], body[aead.NonceSize():], []byte(provimuxAffinityMagic))
	if errOpen != nil {
		return nil, errors.New("snapshot does not authenticate with this installation's key")
	}
	return plain, nil
}

func provimuxAffinityOpen(secret, data []byte) (*coreauth.ProvimuxAffinityState, error) {
	plain, errUnseal := provimuxAffinityUnseal(secret, data)
	if errUnseal != nil {
		return nil, errUnseal
	}
	var state coreauth.ProvimuxAffinityState
	if errDecode := json.Unmarshal(plain, &state); errDecode != nil {
		return nil, errDecode
	}
	return &state, nil
}

// provimuxAffinityAuthenticates reports whether path holds a regular file that authenticates
// under secret: (present, ours). An unreadable file is present and not ours.
func provimuxAffinityAuthenticates(path string, secret []byte) (present, ours bool, info fs.FileInfo) {
	info, errStat := os.Lstat(path)
	if errStat != nil {
		return !errors.Is(errStat, fs.ErrNotExist), false, nil
	}
	if !info.Mode().IsRegular() {
		return true, false, info
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return true, false, info
	}
	_, errUnseal := provimuxAffinityUnseal(secret, data)
	return true, errUnseal == nil, info
}

// checkSnapshot: the snapshot is absent, or authenticates under secret. A file already
// authenticated and unchanged since is not read again.
func (rt *provimuxAffinityRuntime) checkSnapshot(snapshot string, secret []byte) error {
	if info, errStat := os.Lstat(snapshot); errStat == nil && rt.verified != nil && os.SameFile(info, rt.verified) &&
		info.Size() == rt.verified.Size() && info.ModTime().Equal(rt.verified.ModTime()) {
		return nil
	}
	present, ours, info := provimuxAffinityAuthenticates(snapshot, secret)
	if !present {
		rt.verified = nil
		return nil
	}
	if !ours {
		rt.verified = nil
		return provimuxAffinityNotOurs(snapshot, "snapshot")
	}
	rt.verified = info
	return nil
}

// takeLock holds the store's lock (on the key file) for this service, or says who holds it.
func (rt *provimuxAffinityRuntime) takeLock(key string) error {
	if rt.lock != nil {
		return nil
	}
	file, errOpen := os.OpenFile(key, os.O_RDONLY, 0)
	if errOpen != nil {
		return &provimuxAffinityBlocked{provimuxReasonPathUnusable, errOpen.Error()}
	}
	if errLock := provimuxAffinityLockFile(file); errLock != nil {
		_ = file.Close()
		if errors.Is(errLock, errProvimuxAffinityLocked) {
			return &provimuxAffinityBlocked{provimuxReasonInUse, key + " is locked by another Provimux writing this store; this one writes when it is released"}
		}
		return &provimuxAffinityBlocked{provimuxReasonPathUnusable, errLock.Error()}
	}
	rt.lock = file
	return nil
}

// releaseLock lets the store's lock go (closing the file releases it).
func (rt *provimuxAffinityRuntime) releaseLock() {
	if rt.lock != nil {
		_ = rt.lock.Close()
		rt.lock = nil
	}
}

// provimuxAffinityWrite writes data to a temporary file of ours beside path (write, sync, close)
// and hands it to publish, which renames it over path or removes it. A variable so a test can make
// the disk full or slow.
var provimuxAffinityWrite = func(path string, data []byte, publish func(tmp string) error) error {
	if hook := provimuxAffinityBeforeWrite.Load(); hook != nil {
		(*hook)()
	}
	tmp, errCreate := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if errCreate != nil {
		return errCreate
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		return fail(errChmod)
	}
	if _, errWrite := tmp.Write(data); errWrite != nil {
		return fail(errWrite)
	}
	if errSync := tmp.Sync(); errSync != nil {
		return fail(errSync)
	}
	if errClose := tmp.Close(); errClose != nil {
		_ = os.Remove(name)
		return errClose
	}
	return publish(name)
}

var errProvimuxAffinityCancelled = errors.New("the write was given up (the service stopped, or another holds the store)")

// publish renames tmp over snapshot, only when this writer still holds the lock, was not cancelled,
// and what is at snapshot is absent or authenticates under secret. Otherwise it removes tmp.
func (rt *provimuxAffinityRuntime) publish(snapshot string, secret []byte) func(tmp string) error {
	return func(tmp string) error {
		if rt.cancelled.Load() || rt.lock == nil {
			_ = os.Remove(tmp)
			return errProvimuxAffinityCancelled
		}
		rt.verified = nil
		if errOurs := rt.checkSnapshot(snapshot, secret); errOurs != nil {
			_ = os.Remove(tmp)
			return errOurs
		}
		if errRename := os.Rename(tmp, snapshot); errRename != nil {
			_ = os.Remove(tmp)
			return errRename
		}
		if d, errOpen := os.Open(filepath.Dir(snapshot)); errOpen == nil {
			_ = d.Sync()
			_ = d.Close()
		}
		rt.verified = nil
		return nil
	}
}

// provimuxAffinityWritable checks that the directory takes a file of ours and lets it go again.
func provimuxAffinityWritable(snapshot string) error {
	probe, errCreate := os.CreateTemp(filepath.Dir(snapshot), "."+filepath.Base(snapshot)+".probe-*")
	if errCreate != nil {
		return errCreate
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}

// removeOurs removes our files: the snapshot and our temporary files when they authenticate under
// our key, then the key. Nothing is removed without our key, or while another holds the store.
func (rt *provimuxAffinityRuntime) removeOurs(snapshot, key string) error {
	secret, _, errKey := provimuxAffinityReadKey(key)
	if errKey != nil {
		return errKey
	}
	if secret == nil {
		return nil
	}
	if errLock := rt.takeLock(key); errLock != nil {
		return errLock
	}
	defer rt.releaseLock()
	var errs []error
	if present, ours, _ := provimuxAffinityAuthenticates(snapshot, secret); present && ours {
		if errRemove := os.Remove(snapshot); errRemove != nil && !errors.Is(errRemove, fs.ErrNotExist) {
			errs = append(errs, errRemove)
		}
	} else if present {
		errs = append(errs, provimuxAffinityNotOurs(snapshot, "snapshot"))
	}
	rt.verified = nil
	prefix := "." + filepath.Base(snapshot) + ".tmp-"
	if entries, errDir := os.ReadDir(filepath.Dir(snapshot)); errDir == nil {
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			leftover := filepath.Join(filepath.Dir(snapshot), entry.Name())
			if _, ours, _ := provimuxAffinityAuthenticates(leftover, secret); ours {
				_ = os.Remove(leftover)
			}
		}
	}
	rt.releaseLock()
	if errRemove := os.Remove(key); errRemove != nil && !errors.Is(errRemove, fs.ErrNotExist) {
		errs = append(errs, errRemove)
	}
	return errors.Join(errs...)
}

// provimuxAffinityAccount names an account on disk: an HMAC of its stable identity.
func provimuxAffinityAccount(secret []byte, identity string) string {
	return hex.EncodeToString(provimuxAffinityMAC(secret, "provimux affinity account\x00"+identity)[:16])
}

// provimuxAffinityAccountIdentities: auth ID -> stable vendor identity ("" when it has none).
func (s *Service) provimuxAffinityAccountIdentities() map[string]string {
	out := map[string]string{}
	if s.coreManager == nil {
		return out
	}
	for _, auth := range s.coreManager.List() {
		identity, _ := coreauth.ProvimuxAccountIdentity(auth)
		out[auth.ID] = identity
	}
	return out
}

// observe records what each auth ID is now. An auth ID first seen after the start is watched from
// now; one whose identity differs from before, or that is missing, is tainted for the rest of the
// run: none of its bindings is written again. Called under writeMu, every few seconds and twice
// around each capture.
func (rt *provimuxAffinityRuntime) observe(identities map[string]string, now time.Time, atStart bool) {
	for authID, identity := range identities {
		sum := ""
		if identity != "" {
			digest := sha256.Sum256([]byte(identity))
			sum = hex.EncodeToString(digest[:])
		}
		previous, seen := rt.identities[authID]
		switch {
		case !seen:
			since := now
			if atStart {
				since = time.Time{}
			}
			rt.identities[authID] = provimuxAffinityIdentity{identity: sum, since: since}
		case previous.identity != sum:
			previous.identity, previous.tainted = sum, true
			rt.identities[authID] = previous
		}
	}
	for authID, previous := range rt.identities {
		if _, present := identities[authID]; !present && !previous.tainted {
			previous.tainted = true
			rt.identities[authID] = previous
		}
	}
}

// provimuxAffinityStart puts the last run's bindings back (once), then watches the setting and
// writes the bindings every few minutes. Called by Run before the server is created.
func (s *Service) provimuxAffinityStart(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	rt := s.provimuxAffinityRuntime()
	management.SetProvimuxAffinityPersistSource(func() any { return rt.currentStatus() })
	cfg := s.currentConfig()
	now := time.Now()
	rt.writeMu.Lock()
	rt.observe(s.provimuxAffinityAccountIdentities(), now, true)
	if snapshot, secret, ok := s.provimuxAffinityApply(rt, cfg, true); ok {
		restored, note := s.provimuxAffinityRestore(snapshot, secret, now)
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			status.RestoredAtStart, status.StartNote = restored, note
		})
	} else {
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			status.StartNote = "nothing restored: " + status.Reason
		})
	}
	rt.writeMu.Unlock()

	stop, done := make(chan struct{}), make(chan struct{})
	rt.statusMu.Lock()
	rt.stop, rt.done = stop, done
	rt.statusMu.Unlock()
	go s.provimuxAffinityLoop(ctx, rt, stop, done)
}

// provimuxAffinityApply evaluates the configuration and the files, under writeMu. When the
// feature is not in effect our files are removed. When it is, the files are checked: a key or a
// snapshot that does not authenticate, a key that is not private, or (with probe, or while it was
// the reason) a directory that cannot be written blocks it. ok: the snapshot may be read (secret is
// nil when there is no key yet). Taking the store's lock is tried too: held by another, the state
// says in-use and nothing is written, but the snapshot may still be read.
func (s *Service) provimuxAffinityApply(rt *provimuxAffinityRuntime, cfg *config.Config, probe bool) (snapshot string, secret []byte, ok bool) {
	setting, set, inEffect, reason := provimuxAffinityMode(cfg)
	snapshot, key, errPaths := provimuxAffinityPaths(cfg)
	previous := rt.currentStatus().Reason
	rt.update(func(status *ProvimuxAffinityPersistStatus) {
		status.Setting, status.Set, status.InEffect, status.Reason, status.Detail = setting, set, inEffect, reason, ""
		status.Snapshot, status.Key = snapshot, key
	})
	if errPaths != nil {
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			status.InEffect, status.Detail = false, errPaths.Error()
			if status.Reason == "" {
				status.Reason = provimuxReasonPathUnusable
			}
		})
		return snapshot, nil, false
	}
	if !inEffect {
		errRemove := rt.removeOurs(snapshot, key)
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			if errRemove != nil {
				status.LastError = "files of an earlier run not removed: " + errRemove.Error()
			} else {
				status.LastError = ""
			}
		})
		return snapshot, nil, false
	}
	if probe || previous == provimuxReasonNotWritable {
		if errWritable := provimuxAffinityWritable(snapshot); errWritable != nil {
			rt.block(&provimuxAffinityBlocked{provimuxReasonNotWritable, filepath.Dir(snapshot) + ": " + errWritable.Error()})
			return snapshot, nil, false
		}
	}
	secret, keyNote, errKey := provimuxAffinityReadKey(key)
	if errKey != nil {
		rt.block(errKey)
		return snapshot, nil, false
	}
	if keyNote != "" {
		rt.update(func(status *ProvimuxAffinityPersistStatus) { status.KeyNote = keyNote })
	}
	if errOurs := rt.checkSnapshot(snapshot, secret); errOurs != nil {
		rt.block(errOurs)
		return snapshot, nil, false
	}
	if secret != nil {
		if errLock := rt.takeLock(key); errLock != nil {
			rt.block(errLock)
		}
	}
	return snapshot, secret, true
}

// block records a reason the files cannot be used.
func (rt *provimuxAffinityRuntime) block(err error) bool {
	var blocked *provimuxAffinityBlocked
	if !errors.As(err, &blocked) {
		return false
	}
	rt.update(func(status *ProvimuxAffinityPersistStatus) {
		status.InEffect, status.Reason, status.Detail = false, blocked.reason, blocked.detail
	})
	log.Warnf("session affinity: bindings not kept across restarts: %s", blocked.Error())
	return true
}

// provimuxAffinityRestore reads the snapshot (authenticated by provimuxAffinityApply) and puts its
// bindings back. The snapshot stays: the next write replaces it.
func (s *Service) provimuxAffinityRestore(snapshot string, secret []byte, now time.Time) (*coreauth.ProvimuxAffinityRestored, string) {
	data, errRead := os.ReadFile(snapshot)
	if errRead != nil {
		if errors.Is(errRead, fs.ErrNotExist) {
			return nil, "no snapshot"
		}
		return nil, errRead.Error()
	}
	state, errOpen := provimuxAffinityOpen(secret, data)
	if errOpen != nil {
		log.Warnf("session affinity: last run's bindings not restored: %v", errOpen)
		return nil, errOpen.Error()
	}
	accounts := make(map[string]string)
	ambiguous := make(map[string]bool)
	for authID, identity := range s.provimuxAffinityAccountIdentities() {
		if identity == "" {
			continue
		}
		token := provimuxAffinityAccount(secret, identity)
		if previous, seen := accounts[token]; seen && previous != authID {
			ambiguous[token] = true
			continue
		}
		accounts[token] = authID
	}
	for token := range ambiguous {
		delete(accounts, token)
	}
	restored, errRestore := coreauth.ProvimuxAffinityRestore(s.coreManager.Selector(), state, accounts, now)
	if errRestore != nil {
		log.Warnf("session affinity: last run's bindings not restored: %v", errRestore)
		return &restored, errRestore.Error()
	}
	log.Infof("session affinity: %d bindings of the last run restored (%d expired, %d for accounts no longer here, %d of conversations this build names differently %v)", restored.Sessions, restored.Expired, restored.UnknownAccounts, restored.OtherBuild, restored.OtherBuildKinds)
	return &restored, ""
}

func (s *Service) provimuxAffinityLoop(ctx context.Context, rt *provimuxAffinityRuntime, stop, done chan struct{}) {
	defer close(done)
	check := provimuxAffinityCheck
	if interval := provimuxAffinitySnapshotInterval(); interval < check {
		check = interval
	}
	ticker := time.NewTicker(check)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
		}
		due := time.Since(last) >= provimuxAffinitySnapshotInterval()
		if due {
			last = time.Now()
		}
		s.provimuxAffinityTick(rt, due)
	}
}

// provimuxAffinityTick applies the configuration now and, when due and allowed, writes.
func (s *Service) provimuxAffinityTick(rt *provimuxAffinityRuntime, write bool) {
	rt.writeMu.Lock()
	defer rt.writeMu.Unlock()
	cfg := s.currentConfig()
	rt.observe(s.provimuxAffinityAccountIdentities(), time.Now(), false)
	if snapshot, secret, ok := s.provimuxAffinityApply(rt, cfg, false); ok && write {
		_, key, _ := provimuxAffinityPaths(cfg)
		s.provimuxAffinityWriteLocked(rt, snapshot, key, secret)
	}
}

// provimuxAffinityStop ends the loop and writes the bindings once more (or removes our files when
// the feature is off). Called by Shutdown when the process is told to stop, before upstream's
// StopAutoRefresh. On a stalled disk it gives up after provimuxAffinityStopDeadline: the write
// is cancelled (it never lands), the last one written stays, and the store's lock is released
// when that write returns (or the process ends).
func (s *Service) provimuxAffinityStop() {
	if s == nil {
		return
	}
	value, ok := provimuxAffinityRuntimes.Load(s)
	if !ok {
		return
	}
	rt := value.(*provimuxAffinityRuntime)
	deadline := time.NewTimer(provimuxAffinityStopDeadline)
	defer deadline.Stop()
	rt.statusMu.Lock()
	stop, done := rt.stop, rt.done
	rt.stop, rt.done = nil, nil
	rt.statusMu.Unlock()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if stop != nil {
			close(stop)
			<-done
		}
		s.provimuxAffinityTick(rt, true)
		rt.cancelled.Store(true)
		rt.writeMu.Lock()
		rt.releaseLock()
		rt.writeMu.Unlock()
	}()
	select {
	case <-finished:
	case <-deadline.C:
		rt.cancelled.Store(true)
		log.Warnf("session affinity: the snapshot at stop did not finish within %s; it is given up and the last one written stays", provimuxAffinityStopDeadline)
	}
	provimuxAffinityRuntimes.Delete(s)
}

// provimuxAffinityWriteLocked writes the bindings in memory now, under writeMu. The identities
// are read before and after the copy; a binding is written only for an account whose identity
// was the same at every check from the binding's last use to this write. The copy is taken under
// the session cache's read lock; encryption and the write happen after it is released.
func (s *Service) provimuxAffinityWriteLocked(rt *provimuxAffinityRuntime, snapshot, key string, secret []byte) {
	leftOut := 0
	errWrite := func() error {
		if secret == nil {
			made, errMake := provimuxAffinityMakeKey(key)
			if errMake != nil {
				if errors.Is(errMake, fs.ErrExist) {
					return &provimuxAffinityBlocked{provimuxReasonInUse, key + " appeared while writing; the next check reads it"}
				}
				return errMake
			}
			secret = made
		}
		if errLock := rt.takeLock(key); errLock != nil {
			return errLock
		}
		before := s.provimuxAffinityAccountIdentities()
		rt.observe(before, time.Now(), false)
		now := time.Now()
		owner := map[string]string{} // token -> auth ID
		state, errCapture := coreauth.ProvimuxAffinityCapture(s.coreManager.Selector(), func(authID string, lastUse time.Time) (string, bool) {
			identity := before[authID]
			seen, watched := rt.identities[authID]
			if identity == "" {
				return "", false
			}
			if !watched || seen.tainted || lastUse.Before(seen.since) {
				leftOut++
				return "", false
			}
			token := provimuxAffinityAccount(secret, identity)
			owner[token] = authID
			return token, true
		}, now)
		if errCapture != nil {
			return errCapture
		}
		rt.observe(s.provimuxAffinityAccountIdentities(), time.Now(), false)
		kept := state.Sessions[:0]
		for _, session := range state.Sessions {
			if rt.identities[owner[session.Account]].tainted {
				leftOut++
				continue
			}
			kept = append(kept, session)
		}
		state.Sessions = kept
		sealed, errSeal := provimuxAffinitySeal(secret, state)
		if errSeal != nil {
			return errSeal
		}
		if errStore := provimuxAffinityWrite(snapshot, sealed, rt.publish(snapshot, secret)); errStore != nil {
			return errStore
		}
		written := now
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			status.LastWrittenAt, status.LastBindings, status.LeftOut = &written, len(state.Sessions), leftOut
		})
		return nil
	}()
	if errWrite != nil {
		if errors.Is(errWrite, errProvimuxAffinityCancelled) {
			log.Warnf("session affinity: %v", errWrite)
			return
		}
		if errors.Is(errWrite, fs.ErrPermission) {
			errWrite = &provimuxAffinityBlocked{provimuxReasonNotWritable, errWrite.Error()}
		}
		if rt.block(errWrite) {
			return
		}
		rt.update(func(status *ProvimuxAffinityPersistStatus) { status.LastError = errWrite.Error() })
		log.Warnf("session affinity: bindings not written: %v", errWrite)
		return
	}
	rt.update(func(status *ProvimuxAffinityPersistStatus) { status.LastError = "" })
}
