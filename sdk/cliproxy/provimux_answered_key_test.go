package cliproxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Provimux: the key of the account markers (answered-by.md 2.2): made once, kept in the local store, read again after
// a restart; random for the run where no store keeps it. The login below is invented.

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
	first, kept, errKey := provimuxAnsweredKeyFrom(ctx, store)
	if errKey != nil || !kept || len(first) != provimuxAnsweredKeySize {
		t.Fatalf("first start: %d bytes, kept %v, %v", len(first), kept, errKey)
	}
	again, kept, errKey := provimuxAnsweredKeyFrom(ctx, store)
	if errKey != nil || !kept || !bytes.Equal(again, first) {
		t.Fatalf("second read in one run: kept %v, %v, equal %v", kept, errKey, bytes.Equal(again, first))
	}
	if errClose := store.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	restarted := openProvimuxKeyStore(t, path)
	t.Cleanup(func() { _ = restarted.Close() })
	afterRestart, kept, errKey := provimuxAnsweredKeyFrom(ctx, restarted)
	if errKey != nil || !kept || !bytes.Equal(afterRestart, first) {
		t.Fatalf("after a restart: kept %v, %v, equal %v", kept, errKey, bytes.Equal(afterRestart, first))
	}
	if markerUnder(first) == "" || markerUnder(first) != markerUnder(afterRestart) {
		t.Fatal("the account's marker changed across a restart")
	}

	other := openProvimuxKeyStore(t, filepath.Join(t.TempDir(), "kv.db"))
	t.Cleanup(func() { _ = other.Close() })
	theirs, kept, errKey := provimuxAnsweredKeyFrom(ctx, other)
	if errKey != nil || !kept || bytes.Equal(theirs, first) || markerUnder(theirs) == markerUnder(first) {
		t.Fatalf("another installation: kept %v, %v; its key and marker must differ", kept, errKey)
	}
}

func TestProvimuxAnsweredKeyWithoutAStore(t *testing.T) {
	ctx := context.Background()
	one, kept, errKey := provimuxAnsweredKeyFrom(ctx, nil)
	two, _, _ := provimuxAnsweredKeyFrom(ctx, nil)
	if kept || errKey == nil || len(one) != provimuxAnsweredKeySize || bytes.Equal(one, two) {
		t.Fatalf("no store: kept %v, %v, %d bytes, two runs equal %v; want a random key per run", kept, errKey, len(one), bytes.Equal(one, two))
	}

	// A stored value that is not a key is left as it is; the run gets a random key.
	store := openProvimuxKeyStore(t, filepath.Join(t.TempDir(), "kv.db"))
	t.Cleanup(func() { _ = store.Close() })
	if _, errSet := store.KVSetNX(ctx, provimuxAnsweredKeyName, []byte("short"), 0); errSet != nil {
		t.Fatal(errSet)
	}
	random, kept, errKey := provimuxAnsweredKeyFrom(ctx, store)
	stored, _, _ := store.KVGet(ctx, provimuxAnsweredKeyName)
	if kept || errKey == nil || len(random) != provimuxAnsweredKeySize || string(stored) != "short" {
		t.Fatalf("bad stored value: kept %v, %v, stored now %q", kept, errKey, stored)
	}
}

// provimuxLoadAnsweredKey reads the store the service configured, once per process, and gives the key to the
// markers; the log never holds the key, in any encoding.
func TestProvimuxLoadAnsweredKey(t *testing.T) {
	reset := func() {
		provimuxAnsweredKeyState.Lock()
		provimuxAnsweredKeyState.loaded = false
		provimuxAnsweredKeyState.Unlock()
		coreauth.SetProvimuxMarkerKey(nil)
		_ = kvstore.ShutdownLocal()
	}
	reset()
	t.Cleanup(reset)
	wasOn := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() { logging.SetProvimuxAnsweredBy(wasOn) })
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })

	path := filepath.Join(t.TempDir(), "kv.db")
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	provimuxLoadAnsweredKey()
	marker := provimuxKeyFixtureLogin().ProvimuxAccountMarker()
	client, _, _ := kvstore.CurrentKVClient()
	stored, found, _ := client.KVGet(context.Background(), provimuxAnsweredKeyName)
	if !found || marker == "" || marker != markerUnder(stored) {
		t.Fatalf("kept key: found %v, marker %q", found, marker)
	}
	coreauth.SetProvimuxMarkerKey(stored)
	provimuxLoadAnsweredKey() // once per process: a second call reads nothing
	answeredLines := func() []*log.Entry {
		var lines []*log.Entry
		for _, entry := range hook.AllEntries() {
			if strings.HasPrefix(entry.Message, "answered-by:") {
				lines = append(lines, entry)
			}
		}
		return lines
	}
	if lines := answeredLines(); len(lines) != 0 {
		t.Fatalf("a kept key logs nothing; got %d lines", len(lines))
	}

	reset()
	provimuxLoadAnsweredKey() // the store is off: a random key for the run, said once
	if provimuxKeyFixtureLogin().ProvimuxAccountMarker() == "" {
		t.Fatal("no marker with a key random for the run")
	}
	provimuxLoadAnsweredKey()
	if lines := answeredLines(); len(lines) != 1 || !strings.Contains(lines[0].Message, "not kept") {
		t.Fatalf("want one line saying the key is not kept; got %d", len(lines))
	}
	for _, entry := range hook.AllEntries() {
		line, _ := entry.String()
		for _, secret := range []string{string(stored), hex.EncodeToString(stored), base64.StdEncoding.EncodeToString(stored), base64.RawURLEncoding.EncodeToString(stored), "ada@fixture.example.test"} {
			if strings.Contains(line, secret) {
				t.Fatalf("the log holds a secret: %q", line)
			}
		}
	}
}
