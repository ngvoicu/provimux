package cliproxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Provimux: the key of the account markers (answered-by.md 2.2): made once, kept in the local store, read again after
// a restart; random for the run where no store keeps it, the log saying the true cause; none in Home mode, where the
// switch stays off. The login below is invented.

func provimuxKeyFixtureLogin() *coreauth.Auth {
	return &coreauth.Auth{ID: "antigravity-ada.json", Provider: "antigravity", Attributes: map[string]string{"path": "/fixture/auths/antigravity-ada.json"},
		Metadata: map[string]any{"type": "antigravity", "access_token": "fixture-a", "email": "ada@fixture.example.test"}}
}

func openProvimuxKeyStore(t *testing.T, path string) *kvstore.BoltStore {
	t.Helper()
	store, errOpen := kvstore.OpenBoltStore(path)
	if errOpen != nil {
		t.Fatalf("open store: %v", errOpen)
	}
	return store
}

// markerUnder is the fixture login's marker under key, the switch on. It leaves the markers without a key.
func markerUnder(key []byte) string {
	coreauth.SetProvimuxMarkerKey(key)
	wasOn := logging.SetProvimuxAnsweredBy(true)
	defer func() {
		coreauth.SetProvimuxMarkerKey(nil)
		logging.SetProvimuxAnsweredBy(wasOn)
	}()
	return provimuxKeyFixtureLogin().ProvimuxAccountMarker()
}

func TestProvimuxAnsweredKeyIsKeptAcrossARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kv.db")
	store := openProvimuxKeyStore(t, path)
	first, kept, made, errKey := provimuxAnsweredKeyFrom(ctx, store)
	if errKey != nil || !kept || !made || len(first) != provimuxAnsweredKeySize {
		t.Fatalf("first start: %d bytes, kept %v, made %v, %v", len(first), kept, made, errKey)
	}
	again, kept, made, errKey := provimuxAnsweredKeyFrom(ctx, store)
	if errKey != nil || !kept || made || !bytes.Equal(again, first) {
		t.Fatalf("second read in one run: kept %v, made %v, %v, equal %v", kept, made, errKey, bytes.Equal(again, first))
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	restarted := openProvimuxKeyStore(t, path)
	t.Cleanup(func() { _ = restarted.Close() })
	afterRestart, kept, made, errKey := provimuxAnsweredKeyFrom(ctx, restarted)
	if errKey != nil || !kept || made || !bytes.Equal(afterRestart, first) {
		t.Fatalf("after a restart: kept %v, made %v, %v, equal %v", kept, made, errKey, bytes.Equal(afterRestart, first))
	}
	if markerUnder(first) == "" || markerUnder(first) != markerUnder(afterRestart) {
		t.Fatal("the account's marker changed across a restart")
	}

	other := openProvimuxKeyStore(t, filepath.Join(t.TempDir(), "kv.db"))
	t.Cleanup(func() { _ = other.Close() })
	theirs, kept, _, errKey := provimuxAnsweredKeyFrom(ctx, other)
	if errKey != nil || !kept || bytes.Equal(theirs, first) || markerUnder(theirs) == markerUnder(first) {
		t.Fatalf("another installation: kept %v, %v; its key and marker must differ", kept, errKey)
	}
}

func TestProvimuxAnsweredKeyWithoutAStore(t *testing.T) {
	ctx := context.Background()
	one, kept, made, errKey := provimuxAnsweredKeyFrom(ctx, nil)
	two, _, _, _ := provimuxAnsweredKeyFrom(ctx, nil)
	if kept || made || errKey == nil || len(one) != provimuxAnsweredKeySize || bytes.Equal(one, two) {
		t.Fatalf("no store: kept %v, made %v, %v, %d bytes, two runs equal %v; want a random key per run", kept, made, errKey, len(one), bytes.Equal(one, two))
	}

	// A stored value that is not a key is left as it is; the run gets a random key.
	store := openProvimuxKeyStore(t, filepath.Join(t.TempDir(), "kv.db"))
	t.Cleanup(func() { _ = store.Close() })
	if _, errSet := store.KVSetNX(ctx, provimuxAnsweredKeyName, []byte("short"), 0); errSet != nil {
		t.Fatal(errSet)
	}
	random, kept, _, errKey := provimuxAnsweredKeyFrom(ctx, store)
	stored, _, _ := store.KVGet(ctx, provimuxAnsweredKeyName)
	if kept || errKey == nil || !strings.Contains(errKey.Error(), "not a key") || len(random) != provimuxAnsweredKeySize || string(stored) != "short" {
		t.Fatalf("bad stored value: kept %v, %v, stored now %q", kept, errKey, stored)
	}
}

// provimuxKeyLoad runs provimuxLoadAnsweredKey for cfg as a fresh process would, the new key being fixed so that the
// test can look for it, and returns the answered-by log lines it wrote.
func provimuxKeyLoad(t *testing.T, cfg *config.Config, fixed []byte) []*log.Entry {
	t.Helper()
	fixedMarker := markerUnder(fixed)
	provimuxAnsweredKeyState.Lock()
	provimuxAnsweredKeyState.loaded = false
	provimuxAnsweredKeyState.Unlock()
	coreauth.SetProvimuxMarkerKey(nil)
	random := provimuxAnsweredKeyRandom
	provimuxAnsweredKeyRandom = func(key []byte) { copy(key, fixed) }
	defer func() { provimuxAnsweredKeyRandom = random }()
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	provimuxLoadAnsweredKey(cfg)
	provimuxLoadAnsweredKey(cfg) // once per process: a second call reads and says nothing
	var lines []*log.Entry
	for _, entry := range hook.AllEntries() {
		if strings.HasPrefix(entry.Message, "answered-by:") {
			lines = append(lines, entry)
		}
	}
	for _, entry := range lines {
		line, _ := entry.String()
		for _, secret := range []string{string(fixed), hex.EncodeToString(fixed), base64.StdEncoding.EncodeToString(fixed), base64.RawURLEncoding.EncodeToString(fixed), fixedMarker, "ada@fixture.example.test"} {
			if strings.Contains(line, secret) {
				t.Fatalf("the log holds the key or something made from it: %q", line)
			}
		}
	}
	return lines
}

// The loader reads the store the service configured, once per process; says, once, when it made and kept a new key;
// says the true cause when the key is not kept (the store disabled, or not opened because another gateway holds its
// file); and never writes the key, or anything made from it, into the log.
func TestProvimuxLoadAnsweredKey(t *testing.T) {
	t.Cleanup(func() {
		provimuxAnsweredKeyState.Lock()
		provimuxAnsweredKeyState.loaded = false
		provimuxAnsweredKeyState.Unlock()
		coreauth.SetProvimuxMarkerKey(nil)
		_ = kvstore.ShutdownLocal()
	})
	wasOn := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() { logging.SetProvimuxAnsweredBy(wasOn) })
	fixed := []byte("provimux-fixed-new-key-of-32byte")
	want := markerUnder(fixed)

	path := filepath.Join(t.TempDir(), "kv.db")
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	lines := provimuxKeyLoad(t, &config.Config{}, fixed)
	if len(lines) != 1 || lines[0].Level != log.InfoLevel || !strings.Contains(lines[0].Message, "made the key") {
		t.Fatalf("first start: want one info line that the key was made and kept, got %d", len(lines))
	}
	if marker := provimuxKeyFixtureLogin().ProvimuxAccountMarker(); marker == "" || marker != want {
		t.Fatalf("the kept key is not the markers' key: %q", marker)
	}
	if lines = provimuxKeyLoad(t, &config.Config{}, []byte("another-fixed-new-key-of-32bytes")); len(lines) != 0 {
		t.Fatalf("a start that finds the key says nothing; got %q", lines[0].Message)
	}
	if marker := provimuxKeyFixtureLogin().ProvimuxAccountMarker(); marker != want {
		t.Fatal("a restart did not read the kept key")
	}

	// Another gateway holds the store's file: it cannot be opened, and the log says so.
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	holder := openProvimuxKeyStore(t, path)
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure == nil {
		t.Fatal("fixture: the held store opened")
	}
	lines = provimuxKeyLoad(t, &config.Config{}, fixed)
	if len(lines) != 1 || lines[0].Level != log.WarnLevel || !strings.Contains(lines[0].Message, "could not be opened") || !strings.Contains(lines[0].Message, "not kept") {
		t.Fatalf("a held store: want one warning that it could not be opened, got %v", lines)
	}
	_ = holder.Close()

	// The store is disabled.
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	disabled := &config.Config{}
	disabled.KVStore.Disabled = true
	lines = provimuxKeyLoad(t, disabled, fixed)
	if len(lines) != 1 || lines[0].Level != log.WarnLevel || !strings.Contains(lines[0].Message, "disabled") {
		t.Fatalf("a disabled store: want one warning that it is disabled, got %v", lines)
	}
	if provimuxKeyFixtureLogin().ProvimuxAccountMarker() == "" {
		t.Fatal("no marker with a key random for the run")
	}
}

// In Home mode nothing of this is written: the switch stays off whatever the config says, the log says so once, no
// key is made, no credential has a marker and no header is written. Out of Home mode the switch follows the config.
func TestProvimuxAnsweredByIsOffInHomeMode(t *testing.T) {
	wasOn := logging.SetProvimuxAnsweredBy(false)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(wasOn)
		provimuxAnsweredByHomeLogged.Store(false)
		provimuxAnsweredKeyState.Lock()
		provimuxAnsweredKeyState.loaded = false
		provimuxAnsweredKeyState.Unlock()
		coreauth.SetProvimuxMarkerKey(nil)
	})
	on := true
	homeCfg := &config.Config{}
	homeCfg.Home.Enabled = true
	homeCfg.Models.AnsweredBy = &on
	plain := &config.Config{}
	plain.Models.AnsweredBy = &on

	provimuxAnsweredByHomeLogged.Store(false)
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	provimuxSetAnsweredBy(homeCfg)
	provimuxSetAnsweredBy(homeCfg) // a reload: said once
	said := 0
	for _, entry := range hook.AllEntries() {
		if strings.HasPrefix(entry.Message, "answered-by: Home mode") {
			said++
		}
	}
	if logging.ProvimuxAnsweredBy() || said != 1 {
		t.Fatalf("Home mode: switch %v, said %d times; want off, said once", logging.ProvimuxAnsweredBy(), said)
	}
	if lines := provimuxKeyLoad(t, homeCfg, []byte("provimux-fixed-new-key-of-32byte")); len(lines) != 0 {
		t.Fatalf("Home mode makes no key and says nothing more; got %q", lines[0].Message)
	}
	coreauth.SetProvimuxMarkerKey([]byte("provimux-fixed-new-key-of-32byte")) // even with a key
	if marker := provimuxKeyFixtureLogin().ProvimuxAccountMarker(); marker != "" {
		t.Fatalf("Home mode: a marker %q", marker)
	}
	header := http.Header{"Access-Control-Expose-Headers": {"X-CPA-AUTH-KIND"}}
	logging.SetProvimuxAnswerHeaders(header, provimuxKeyFixtureLogin().AuthStatement().OnTheWire())
	if len(header) != 1 || header.Get("Access-Control-Expose-Headers") != "X-CPA-AUTH-KIND" {
		t.Fatalf("Home mode: headers %v", header)
	}

	// The service's own start in Home mode keeps it off too.
	service, errBuild := NewBuilder().WithConfig(homeCfg).WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).Build()
	if errBuild != nil || service == nil || logging.ProvimuxAnsweredBy() {
		t.Fatalf("a Home service: %v, switch %v", errBuild, logging.ProvimuxAnsweredBy())
	}

	provimuxSetAnsweredBy(plain)
	if !logging.ProvimuxAnsweredBy() {
		t.Fatal("out of Home mode the switch follows the config")
	}
}
