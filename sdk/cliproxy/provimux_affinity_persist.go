package cliproxy

// Provimux layer: routing.session-affinity-persist. Session affinity's session cache is written
// when the process is told to stop (just before upstream's StopAutoRefresh, before the HTTP drain)
// and every 5 minutes, and put back once at the next start, before the server exists
// (sdk/cliproxy/auth/provimux_affinity_snapshot.go). Nothing here runs on a request.
//
// On disk, named from the KV store's own path (so two stores never share them):
//   - <kv path>.affinity: "PMXAFF2\n", a nonce, then AES-256-GCM of the snapshot;
//   - <kv path>.affinity-key: "PMXAFFKEY1\n" and 32 random bytes, made once, 0600, owned by the
//     user running the gateway.
//
// A file at either path that does not start with its header is not ours: it is never replaced or
// removed, nothing is written or restored, and the state endpoint says so. The snapshot is not
// removed after a restore: it stays until the next write replaces it, so a second crash soon after
// a start restores it again. When the feature is not in effect (switch off, session affinity off,
// Home mode, KV store disabled) our files are removed and none is written.

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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	provimuxAffinitySuffix    = ".affinity"
	provimuxAffinityKeySuffix = ".affinity-key"
	provimuxAffinityMagic     = "PMXAFF2\n"
	provimuxAffinityKeyMagic  = "PMXAFFKEY1\n"
	provimuxAffinityKeySize   = 32
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
)

var (
	provimuxAffinityIntervalMu sync.RWMutex
	provimuxAffinityInterval   = provimuxAffinityPeriod
	// provimuxAffinityStopDeadline bounds what the stop adds to the exit on a stalled disk.
	provimuxAffinityStopDeadline = 1500 * time.Millisecond
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

func provimuxAffinitySnapshotInterval() time.Duration {
	provimuxAffinityIntervalMu.RLock()
	defer provimuxAffinityIntervalMu.RUnlock()
	return provimuxAffinityInterval
}

// ProvimuxAffinityPersistStatus is what GET /v8/management/provimux/session-affinity-persist answers.
type ProvimuxAffinityPersistStatus struct {
	// Setting is routing.session-affinity-persist as it applies (true when not set).
	Setting bool `json:"setting"`
	// Set reports whether the config file sets it.
	Set bool `json:"set"`
	// InEffect: bindings are being kept across restarts.
	InEffect bool `json:"in_effect"`
	// Reason when not in effect: session-affinity-off, home-mode, kv-store-off or switch-off (the
	// configuration; our files are removed), or foreign-file, key-not-private,
	// directory-not-writable or path-unusable (nothing is written, restored or removed).
	Reason string `json:"reason,omitempty"`
	// Detail says which file and why, for the last four reasons.
	Detail string `json:"detail,omitempty"`
	// Snapshot and Key are the two files' paths.
	Snapshot string `json:"snapshot,omitempty"`
	Key      string `json:"key,omitempty"`
	// KeyNote: the key's mode was repaired.
	KeyNote       string     `json:"key_note,omitempty"`
	LastWrittenAt *time.Time `json:"last_written_at,omitempty"`
	LastBindings  int        `json:"last_bindings"`
	// LeftOut counts bindings the last write left out because their account's login was replaced
	// by another account's after their last use.
	LeftOut         int                                `json:"left_out_replaced_login"`
	LastError       string                             `json:"last_error,omitempty"`
	RestoredAtStart *coreauth.ProvimuxAffinityRestored `json:"restored_at_start,omitempty"`
	StartNote       string                             `json:"start_note,omitempty"`
}

// provimuxAffinityIdentity is what an auth ID was seen to be: a hash of its stable vendor
// identity, and since when (zero: since the start or since it appeared).
type provimuxAffinityIdentity struct {
	identity string
	since    time.Time
}

// provimuxAffinityRuntime is one service's state. writeMu serializes disk work and is never held
// by a request or by a status read; statusMu is held only to copy the status.
type provimuxAffinityRuntime struct {
	writeMu    sync.Mutex
	identities map[string]provimuxAffinityIdentity // under writeMu
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
	setting = true
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

// provimuxAffinityOurs looks at path without following a link: absent, ours (a regular file
// starting with magic), or something else.
func provimuxAffinityOurs(path, magic string) (present, ours bool, err error) {
	info, errStat := os.Lstat(path)
	if errStat != nil {
		if errors.Is(errStat, fs.ErrNotExist) {
			return false, false, nil
		}
		return true, false, errStat
	}
	if !info.Mode().IsRegular() {
		return true, false, nil
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return true, false, errOpen
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, len(magic))
	if _, errRead := io.ReadFull(file, head); errRead != nil {
		return true, false, nil
	}
	return true, string(head) == magic, nil
}

// provimuxAffinityCheckOurs blocks when path holds a file that is not ours.
func provimuxAffinityCheckOurs(path, magic string) error {
	present, ours, errCheck := provimuxAffinityOurs(path, magic)
	if errCheck != nil {
		return &provimuxAffinityBlocked{provimuxReasonPathUnusable, errCheck.Error()}
	}
	if present && !ours {
		return &provimuxAffinityBlocked{provimuxReasonForeignFile, path + " is not a Provimux session-affinity file; it is left as it is"}
	}
	return nil
}

// provimuxAffinityKey reads the installation's key, or makes it when create is set. It is used
// only when it is ours, a regular file owned by the running user, with no group or other access
// (a key we own with a wider mode is set to 0600, and note says so).
func provimuxAffinityKey(path string, create bool) (secret []byte, note string, err error) {
	if errOurs := provimuxAffinityCheckOurs(path, provimuxAffinityKeyMagic); errOurs != nil {
		return nil, "", errOurs
	}
	info, errStat := os.Lstat(path)
	if errStat != nil {
		if !errors.Is(errStat, fs.ErrNotExist) {
			return nil, "", &provimuxAffinityBlocked{provimuxReasonPathUnusable, errStat.Error()}
		}
		if !create {
			return nil, "", errStat
		}
		return provimuxAffinityMakeKey(path)
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
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, "", &provimuxAffinityBlocked{provimuxReasonPathUnusable, errRead.Error()}
	}
	if len(data) != len(provimuxAffinityKeyMagic)+provimuxAffinityKeySize {
		return nil, "", &provimuxAffinityBlocked{provimuxReasonForeignFile, path + " is not a Provimux session-affinity key; it is left as it is"}
	}
	return data[len(provimuxAffinityKeyMagic):], note, nil
}

func provimuxAffinityMakeKey(path string) ([]byte, string, error) {
	secret := make([]byte, provimuxAffinityKeySize)
	if _, errRand := rand.Read(secret); errRand != nil {
		return nil, "", errRand
	}
	file, errCreate := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errCreate != nil {
		if errors.Is(errCreate, fs.ErrExist) {
			return provimuxAffinityKey(path, false)
		}
		return nil, "", errCreate
	}
	_, errWrite := file.Write(append([]byte(provimuxAffinityKeyMagic), secret...))
	errSync := file.Sync()
	errClose := file.Close()
	if errWrite != nil || errSync != nil || errClose != nil {
		_ = os.Remove(path)
		return nil, "", errors.Join(errWrite, errSync, errClose)
	}
	return secret, "", nil
}

// provimuxAffinityWrite writes the snapshot atomically: a temporary file of ours beside it, sync,
// rename over the snapshot (checked to be ours or absent just before), directory sync.
// A variable so a test can make the disk full or slow.
var provimuxAffinityWrite = func(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, errCreate := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
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
	if errOurs := provimuxAffinityCheckOurs(path, provimuxAffinityMagic); errOurs != nil {
		_ = os.Remove(name)
		return errOurs
	}
	if errRename := os.Rename(name, path); errRename != nil {
		_ = os.Remove(name)
		return errRename
	}
	if d, errOpen := os.Open(dir); errOpen == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
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

// provimuxAffinityRemove removes our files (and temporary files of ours a crash left); a file
// there that is not ours stays.
func provimuxAffinityRemove(snapshot, key string) error {
	var errs []error
	remove := func(path, magic string) {
		present, ours, errCheck := provimuxAffinityOurs(path, magic)
		if errCheck != nil {
			errs = append(errs, errCheck)
			return
		}
		if present && ours {
			if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, fs.ErrNotExist) {
				errs = append(errs, errRemove)
			}
		}
	}
	remove(snapshot, provimuxAffinityMagic)
	remove(key, provimuxAffinityKeyMagic)
	if leftovers, errGlob := filepath.Glob(filepath.Join(filepath.Dir(snapshot), "."+globEscape(filepath.Base(snapshot))+".tmp-*")); errGlob == nil {
		for _, leftover := range leftovers {
			remove(leftover, provimuxAffinityMagic)
		}
	}
	return errors.Join(errs...)
}

func globEscape(name string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(name)
}

func provimuxAffinityMAC(secret []byte, label string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// provimuxAffinityAccount names an account on disk: an HMAC of its stable identity.
func provimuxAffinityAccount(secret []byte, identity string) string {
	return hex.EncodeToString(provimuxAffinityMAC(secret, "provimux affinity account\x00"+identity)[:16])
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
		return nil, errors.New("snapshot does not open with this installation's key")
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

// provimuxAffinityIdentities hashes the stable identity of every auth now (in memory only).
func (s *Service) provimuxAffinityIdentities() map[string]string {
	out := map[string]string{}
	if s.coreManager == nil {
		return out
	}
	for _, auth := range s.coreManager.List() {
		if identity, ok := coreauth.ProvimuxAccountIdentity(auth); ok {
			sum := sha256.Sum256([]byte(identity))
			out[auth.ID] = hex.EncodeToString(sum[:])
		} else {
			out[auth.ID] = ""
		}
	}
	return out
}

// observe records what each auth ID is now. An auth ID whose identity differs from the one seen
// before (a login file replaced by another account's while running) is marked with the time it
// was seen changed: its bindings last used before then are left out of the next snapshots.
// Called under writeMu, every few seconds and before each write.
func (rt *provimuxAffinityRuntime) observe(identities map[string]string, now time.Time) {
	for authID, identity := range identities {
		previous, seen := rt.identities[authID]
		switch {
		case !seen:
			rt.identities[authID] = provimuxAffinityIdentity{identity: identity}
		case previous.identity != identity:
			rt.identities[authID] = provimuxAffinityIdentity{identity: identity, since: now}
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
	rt.observe(s.provimuxAffinityIdentities(), time.Time{})
	if snapshot, key, ok := s.provimuxAffinityApply(rt, cfg, true); ok {
		restored, note := s.provimuxAffinityRestore(snapshot, key, now)
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
// feature is not in effect our files are removed. When it is, the files are checked: a file that
// is not ours, a key that is not private, or (with probe, or while it was the reason) a directory
// that cannot be written blocks it. ok: the files may be read and written.
func (s *Service) provimuxAffinityApply(rt *provimuxAffinityRuntime, cfg *config.Config, probe bool) (snapshot, key string, ok bool) {
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
		return snapshot, key, false
	}
	if !inEffect {
		errRemove := provimuxAffinityRemove(snapshot, key)
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			if errRemove != nil {
				status.LastError = "files of an earlier run not removed: " + errRemove.Error()
			} else {
				status.LastError = ""
			}
		})
		return snapshot, key, false
	}
	if probe || previous == provimuxReasonNotWritable {
		if errWritable := provimuxAffinityWritable(snapshot); errWritable != nil {
			rt.block(&provimuxAffinityBlocked{provimuxReasonNotWritable, filepath.Dir(snapshot) + ": " + errWritable.Error()})
			return snapshot, key, false
		}
	}
	if errOurs := provimuxAffinityCheckOurs(snapshot, provimuxAffinityMagic); errOurs != nil {
		rt.block(errOurs)
		return snapshot, key, false
	}
	if _, keyNote, errKey := provimuxAffinityKey(key, false); errKey != nil && !errors.Is(errKey, fs.ErrNotExist) {
		if rt.block(errKey) {
			return snapshot, key, false
		}
		rt.update(func(status *ProvimuxAffinityPersistStatus) { status.LastError = "key: " + errKey.Error() })
		return snapshot, key, false
	} else if keyNote != "" {
		rt.update(func(status *ProvimuxAffinityPersistStatus) { status.KeyNote = keyNote })
	}
	return snapshot, key, true
}

// provimuxAffinityBlock records a reason the files cannot be used.
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

// provimuxAffinityRestore reads the snapshot and puts its bindings back. The snapshot stays: the
// next write replaces it. Called after provimuxAffinityApply allowed it.
func (s *Service) provimuxAffinityRestore(snapshot, key string, now time.Time) (*coreauth.ProvimuxAffinityRestored, string) {
	data, errRead := os.ReadFile(snapshot)
	if errRead != nil {
		if errors.Is(errRead, fs.ErrNotExist) {
			return nil, "no snapshot"
		}
		return nil, errRead.Error()
	}
	secret, _, errKey := provimuxAffinityKey(key, false)
	if errKey != nil {
		return nil, "nothing restored: key: " + errKey.Error()
	}
	state, errOpen := provimuxAffinityOpen(secret, data)
	if errOpen != nil {
		log.Warnf("session affinity: last run's bindings not restored: %v", errOpen)
		return nil, errOpen.Error()
	}
	accounts := make(map[string]string)
	ambiguous := make(map[string]bool)
	for authID, identity := range s.provimuxAffinityAccountIdentities() {
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

// provimuxAffinityAccountIdentities: auth ID -> stable vendor identity, for auths that have one.
func (s *Service) provimuxAffinityAccountIdentities() map[string]string {
	out := map[string]string{}
	for _, auth := range s.coreManager.List() {
		if identity, ok := coreauth.ProvimuxAccountIdentity(auth); ok {
			out[auth.ID] = identity
		}
	}
	return out
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
	rt.observe(s.provimuxAffinityIdentities(), time.Now())
	if snapshot, key, ok := s.provimuxAffinityApply(rt, cfg, false); ok && write {
		s.provimuxAffinityWriteLocked(rt, snapshot, key)
	}
}

// provimuxAffinityStop ends the loop and writes the bindings once more (or removes our files when
// the feature is off). Called by Shutdown when the process is told to stop, before upstream's
// StopAutoRefresh. On a stalled disk it gives up after provimuxAffinityStopDeadline: the last
// periodic snapshot stays.
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
	}()
	select {
	case <-finished:
	case <-deadline.C:
		log.Warnf("session affinity: the snapshot at stop did not finish within %s; the last one written stays", provimuxAffinityStopDeadline)
	}
	provimuxAffinityRuntimes.Delete(s)
}

// provimuxAffinityWriteLocked writes the bindings in memory now, under writeMu. The copy is taken
// under the session cache's read lock; encryption and the write happen after it is released.
func (s *Service) provimuxAffinityWriteLocked(rt *provimuxAffinityRuntime, snapshot, key string) {
	leftOut := 0
	errWrite := func() error {
		if errOurs := provimuxAffinityCheckOurs(snapshot, provimuxAffinityMagic); errOurs != nil {
			return errOurs
		}
		secret, keyNote, errKey := provimuxAffinityKey(key, true)
		if errKey != nil {
			return errKey
		}
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			if keyNote != "" {
				status.KeyNote = keyNote
			}
		})
		identities := s.provimuxAffinityAccountIdentities()
		now := time.Now()
		state, errCapture := coreauth.ProvimuxAffinityCapture(s.coreManager.Selector(), func(authID string, lastUse time.Time) (string, bool) {
			identity, ok := identities[authID]
			if !ok {
				return "", false
			}
			if seen := rt.identities[authID]; lastUse.Before(seen.since) {
				leftOut++
				return "", false
			}
			return provimuxAffinityAccount(secret, identity), true
		}, now)
		if errCapture != nil {
			return errCapture
		}
		sealed, errSeal := provimuxAffinitySeal(secret, state)
		if errSeal != nil {
			return errSeal
		}
		if errStore := provimuxAffinityWrite(snapshot, sealed); errStore != nil {
			return errStore
		}
		written := now
		rt.update(func(status *ProvimuxAffinityPersistStatus) {
			status.LastWrittenAt, status.LastBindings, status.LeftOut = &written, len(state.Sessions), leftOut
		})
		return nil
	}()
	if errWrite != nil {
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
