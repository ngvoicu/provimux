package store

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	sa "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"os"
	"path/filepath"
	"testing"
)

func TestFollowLoginStores(t *testing.T) {
	for _, kind := range []string{"file", "git", "object", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "local", "auths")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			var store ca.Store
			var mirrored []byte
			switch kind {
			case "file":
				s := sa.NewFileTokenStore()
				s.SetBaseDir(dir)
				store = s
			case "git":
				remote := setupGitRemoteRepository(t, root, "master", testBranchSpec{name: "master", contents: "fixture\n"})
				s := NewGitTokenStore(remote, "", "", "master")
				s.SetBaseDir(dir)
				if e := s.EnsureRepository(); e != nil {
					t.Fatal(e)
				}
				store = s
			case "postgres":
				store = &PostgresStore{authDir: dir, cfg: PostgresStoreConfig{AuthTable: "auth_tokens"}, persistAuthFn: func(_ context.Context, _ string, b []byte) error { mirrored = append([]byte(nil), b...); return nil }}
			case "object":
				store = &ObjectTokenStore{authDir: dir, cfg: ObjectStoreConfig{Bucket: "fixture"}, putObjectFn: func(_ context.Context, _ string, b []byte, _ string) error {
					mirrored = append([]byte(nil), b...)
					return nil
				}}
			}
			const file = "fixture-meta.json"
			pl := pluginhost.New().AuthDataToCoreAuth(pluginapi.AuthData{Provider: "meta", FileName: file, Metadata: map[string]any{"type": "meta", "api_key": "fixture-a", "email": "fixture-account", "unrelated": "unchanged"}}, filepath.Join(dir, file), file)
			check := func(a *ca.Auth, mark bool) {
				t.Helper()
				path, e := store.Save(ca.WithAuthCreationIntent(t.Context()), a)
				if e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				blobs := [][]byte{raw}
				if kind == "object" || kind == "postgres" {
					blobs = append(blobs, mirrored)
				}
				for _, b := range blobs {
					var m map[string]any
					if e = json.Unmarshal(b, &m); e != nil {
						t.Fatal(e)
					}
					if m[ca.ProvimuxPluginCredentialMark] != mark || m["unrelated"] != "unchanged" {
						t.Fatalf("mark=%v unrelated=%v want=%v", m[ca.ProvimuxPluginCredentialMark], m["unrelated"], mark)
					}
				}
			}
			check(pl, true)
			native := &ca.Auth{ID: file, FileName: file, Provider: "meta", Metadata: map[string]any{"type": "meta", "unrelated": "unchanged"}, Storage: &meta.MetaTokenStorage{APIKey: "fixture-native", AccessToken: "fixture-access", Email: "native-account"}}
			check(native, false)
		})
	}
}
