// Package modellists holds Provimux's disk cache of model lists (design: model-lists.md 5.4).
//
// The cache keeps, per login, the last live list its vendor returned, so a restart without
// network gives the lists of the last run. A file holds model ids and fields, the fetch time, the
// login type and a hash of the account identity: no token, email or file name. Files are written
// with mode 0600 to a temporary file and renamed. A damaged or foreign file is ignored with one
// warning and overwritten by the next successful fetch. On a read-only disk the lists live in
// memory only.
package modellists

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// FolderName is the cache folder, beside the console's static folder.
	FolderName = "model-lists"
	// Schema is the version of a live file.
	Schema = 1
	// maxFileBytes bounds a read, as the fetch bounds an answer.
	maxFileBytes = 8 << 20
)

// ErrNotCached means there is no file for the identity: not an error worth a warning.
var ErrNotCached = errors.New("model list not cached")

// Dir resolves the cache folder by the console's rule: WRITABLE_PATH, else the config file's
// folder. It returns "" when neither is known.
func Dir(configFilePath string) string {
	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, FolderName)
	}
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}
	base := filepath.Dir(configFilePath)
	if info, err := os.Stat(configFilePath); err == nil && info.IsDir() {
		base = configFilePath
	}
	return filepath.Join(base, FolderName)
}

// IdentityHash names an account identity without revealing it.
func IdentityHash(identity string) string {
	sum := sha256.Sum256([]byte("provimux-live-list\x00" + identity))
	return hex.EncodeToString(sum[:16])
}

// LiveFile is one login's last live list on disk.
type LiveFile struct {
	Schema    int                   `json:"schema"`
	Provider  string                `json:"provider"`
	Identity  string                `json:"identity"` // IdentityHash of the account
	FetchedAt time.Time             `json:"fetched_at"`
	IDs       []string              `json:"ids"`
	Models    []*registry.ModelInfo `json:"models"`
	// Stated names, per lower-case id, the boolean fields the vendor's answer stated.
	Stated map[string][]string `json:"stated,omitempty"`
}

func liveFileName(identityHash string) string { return "live-" + identityHash + ".json" }

// Store reads and writes live files in one folder. The zero Store (no folder) does nothing.
type Store struct {
	dir string

	mu     sync.Mutex
	warned map[string]bool // one warning per file and kind
}

// NewStore returns a store for dir; "" disables the cache.
func NewStore(dir string) *Store { return &Store{dir: dir, warned: make(map[string]bool)} }

// Dir is the store's folder, "" when disabled.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

func (s *Store) warnOnce(key, format string, args ...any) {
	s.mu.Lock()
	first := !s.warned[key]
	s.warned[key] = true
	s.mu.Unlock()
	if first {
		log.Warnf(format, args...)
	} else {
		log.Debugf(format, args...)
	}
}

func (s *Store) clearWarning(key string) {
	s.mu.Lock()
	delete(s.warned, key)
	s.mu.Unlock()
}

// ReadLive returns the cached list of the identity for the provider. ErrNotCached when there is
// none; any other error means the file was damaged or foreign and was ignored (warned once).
func (s *Store) ReadLive(provider, identity string) (LiveFile, error) {
	if s == nil || s.dir == "" {
		return LiveFile{}, ErrNotCached
	}
	hash := IdentityHash(identity)
	name := liveFileName(hash)
	file, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LiveFile{}, ErrNotCached
		}
		s.warnOnce("read:"+name, "model lists cache: cannot read %s: %v", name, err)
		return LiveFile{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err == nil && len(data) > maxFileBytes {
		err = fmt.Errorf("larger than %d bytes", maxFileBytes)
	}
	var live LiveFile
	if err == nil {
		err = json.Unmarshal(data, &live)
	}
	if err == nil {
		err = live.check(provider, hash)
	}
	if err != nil {
		s.warnOnce("bad:"+name, "model lists cache: ignoring %s (%v); the next successful fetch replaces it", name, err)
		return LiveFile{}, err
	}
	return live, nil
}

func (f LiveFile) check(provider, hash string) error {
	switch {
	case f.Schema != Schema:
		return fmt.Errorf("schema %d", f.Schema)
	case f.Identity != hash:
		return errors.New("another account")
	case !strings.EqualFold(f.Provider, provider):
		return fmt.Errorf("another login type (%s)", f.Provider)
	case f.FetchedAt.IsZero():
		return errors.New("no fetch time")
	case len(f.IDs) == 0:
		return errors.New("no models")
	}
	for i, model := range f.Models {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("model %d has no id", i)
		}
	}
	return nil
}

// WriteLive stores a login's list: mode 0600, a temporary file renamed over the old one.
func (s *Store) WriteLive(provider, identity string, fetchedAt time.Time, ids []string, models []*registry.ModelInfo, stated map[string][]string) error {
	if s == nil || s.dir == "" {
		return nil
	}
	hash := IdentityHash(identity)
	name := liveFileName(hash)
	if models == nil {
		models = []*registry.ModelInfo{}
	}
	data, err := json.Marshal(LiveFile{Schema: Schema, Provider: strings.ToLower(provider), Identity: hash,
		FetchedAt: fetchedAt.UTC(), IDs: ids, Models: models, Stated: stated})
	if err == nil {
		err = writeFileAtomic(s.dir, name, data)
	}
	if err != nil {
		s.warnOnce("write", "model lists cache: cannot write to %s (%v); lists stay in memory only", s.dir, err)
		return err
	}
	s.clearWarning("bad:" + name)
	s.clearWarning("write")
	return nil
}

func writeFileAtomic(dir, name string, data []byte) (err error) {
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}

// RemoveLive deletes the file of an identity, if any: the login now belongs to another identity
// (another account, or a credential that changed), so the old file would never be read again.
func (s *Store) RemoveLive(identity string) {
	if s == nil || s.dir == "" || identity == "" {
		return
	}
	if err := os.Remove(filepath.Join(s.dir, liveFileName(IdentityHash(identity)))); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Debugf("model lists cache: cannot remove an old file: %v", err)
	}
}

// PruneOlderThan deletes live files last written more than maxAge ago: a list that old is no
// longer used, and its login may be gone. It returns how many it deleted.
func (s *Store) PruneOlderThan(maxAge time.Duration, now time.Time) int {
	if s == nil || s.dir == "" {
		return 0
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "live-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(filepath.Join(s.dir, name)) == nil {
			removed++
		}
	}
	return removed
}
