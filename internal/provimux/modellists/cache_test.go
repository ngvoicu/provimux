package modellists

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

var fetched = time.Date(2026, 11, 22, 12, 0, 3, 0, time.UTC)

func writeAda(t *testing.T, store *Store) {
	t.Helper()
	err := store.WriteLive("devin", "devin||email=office@providers.dev", fetched, []string{"devin/gemini-4-neon", "devin/swe-2"},
		[]*registry.ModelInfo{{ID: "devin/gemini-4-neon", ContextLength: 1048576, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}}},
		map[string][]string{"devin/gemini-4-neon": {"zero_allowed"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoundTripModeAndContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FolderName)
	store := NewStore(dir)
	writeAda(t, store)
	live, err := store.ReadLive("devin", "devin||email=office@providers.dev")
	if err != nil {
		t.Fatal(err)
	}
	if !live.FetchedAt.Equal(fetched) || len(live.IDs) != 2 || live.Models[0].ContextLength != 1048576 || live.Models[0].Thinking.Levels[1] != "high" ||
		len(live.Stated["devin/gemini-4-neon"]) != 1 {
		t.Fatalf("live = %+v", live)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "live-") {
		t.Fatalf("files = %v (no temporary file may stay)", entries)
	}
	info, _ := entries[0].Info()
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if strings.Contains(string(raw), "office@providers.dev") {
		t.Fatal("the file must hold a hash of the identity, not the identity")
	}
	if dirInfo, _ := os.Stat(dir); dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode = %v", dirInfo.Mode())
	}
}

func TestMissingIsNotCached(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.ReadLive("devin", "nobody"); !errors.Is(err, ErrNotCached) {
		t.Fatalf("err = %v", err)
	}
	if _, err := (*Store)(nil).ReadLive("devin", "x"); !errors.Is(err, ErrNotCached) {
		t.Fatal("a store without a folder holds nothing")
	}
	if err := NewStore("").WriteLive("devin", "x", fetched, []string{"a"}, nil, nil); err != nil {
		t.Fatal("a store without a folder writes nothing and fails nothing")
	}
}

func TestDamagedFileIsIgnoredThenOverwritten(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	identity := "devin||email=office@providers.dev"
	path := filepath.Join(dir, liveFileName(IdentityHash(identity)))
	for name, content := range map[string]string{
		"not json":   `{"schema":1,"provider":"devin"`,
		"old schema": `{"schema":0,"provider":"devin","identity":"` + IdentityHash(identity) + `","fetched_at":"2026-11-22T12:00:03Z","ids":["a"],"models":[]}`,
		"no models":  `{"schema":1,"provider":"devin","identity":"` + IdentityHash(identity) + `","fetched_at":"2026-11-22T12:00:03Z","ids":[],"models":[]}`,
		"no time":    `{"schema":1,"provider":"devin","identity":"` + IdentityHash(identity) + `","ids":["a"],"models":[]}`,
		"null model": `{"schema":1,"provider":"devin","identity":"` + IdentityHash(identity) + `","fetched_at":"2026-11-22T12:00:03Z","ids":["a"],"models":[null]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadLive("devin", identity); err == nil || errors.Is(err, ErrNotCached) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	writeAda(t, store)
	if _, err := store.ReadLive("devin", identity); err != nil {
		t.Fatalf("the next write replaces a damaged file: %v", err)
	}
}

func TestAnotherAccountOrLoginType(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	writeAda(t, store)
	// Bob's identity names another file: nothing is cached for him.
	if _, err := store.ReadLive("devin", "devin||account_id=acct-2"); !errors.Is(err, ErrNotCached) {
		t.Fatalf("another account: %v", err)
	}
	// A file copied under Bob's name still names Ada's account inside: foreign, ignored.
	ada := filepath.Join(dir, liveFileName(IdentityHash("devin||email=office@providers.dev")))
	bob := filepath.Join(dir, liveFileName(IdentityHash("devin||account_id=acct-2")))
	data, _ := os.ReadFile(ada)
	if err := os.WriteFile(bob, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadLive("devin", "devin||account_id=acct-2"); err == nil || errors.Is(err, ErrNotCached) {
		t.Fatalf("a file of another account must be ignored: %v", err)
	}
	if _, err := store.ReadLive("kimi", "devin||email=office@providers.dev"); err == nil {
		t.Fatal("a file of another login type must be ignored")
	}
}

func TestReadOnlyFolderKeepsListsInMemory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions are not enforced here")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	store := NewStore(filepath.Join(parent, FolderName))
	if err := store.WriteLive("devin", "x", fetched, []string{"a"}, nil, nil); err == nil {
		t.Fatal("a read-only disk must report the failed write")
	}
	if _, err := store.ReadLive("devin", "x"); err == nil {
		t.Fatal("nothing was written")
	}
	// A second failure is logged at debug level only; it still fails without panicking.
	if err := store.WriteLive("devin", "x", fetched, []string{"a"}, nil, nil); err == nil {
		t.Fatal("still read-only")
	}
}

func TestDirFollowsTheConsoleRule(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")
	config := filepath.Join(t.TempDir(), "config.yaml")
	if got := Dir(config); got != filepath.Join(filepath.Dir(config), FolderName) {
		t.Fatalf("beside the config file: %s", got)
	}
	if Dir("") != "" {
		t.Fatal("no config, no folder")
	}
	writable := t.TempDir()
	t.Setenv("WRITABLE_PATH", writable)
	if got := Dir(config); got != filepath.Join(writable, FolderName) {
		t.Fatalf("WRITABLE_PATH first: %s", got)
	}
}
