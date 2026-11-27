package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sa "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const finalRecordPayload = `{"type":"meta","api_key":"fixture-local","auth_kind":"oauth"}`

type finalSQL struct{}

func (finalSQL) Open(string) (driver.Conn, error)    { return finalSQL{}, nil }
func (finalSQL) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (finalSQL) Close() error                        { return nil }
func (finalSQL) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }
func (finalSQL) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &finalRows{}, nil
}

type finalRows struct{ done bool }

func (*finalRows) Columns() []string { return []string{"id", "content", "created_at", "updated_at"} }
func (*finalRows) Close() error      { return nil }
func (r *finalRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	values[0] = "fixture-native.json"
	values[1] = finalRecordPayload
	values[2] = time.Now().UTC()
	values[3] = values[2]
	return nil
}

var finalSQLInit sync.Once

func TestFinalNativeLoaders(t *testing.T) {
	for _, kind := range []string{"file", "git", "object", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "local", "auths")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			var store ca.Store
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
			case "object":
				store = &ObjectTokenStore{authDir: dir}
			case "postgres":
				finalSQLInit.Do(func() { sql.Register("final-mark-reader", finalSQL{}) })
				db, e := sql.Open("final-mark-reader", "")
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				store = &PostgresStore{authDir: dir, cfg: PostgresStoreConfig{AuthTable: "fixture"}, db: db}
			}
			if kind != "postgres" {
				if e := os.WriteFile(filepath.Join(dir, "fixture-native.json"), []byte(finalRecordPayload), 0600); e != nil {
					t.Fatal(e)
				}
			}
			list, e := store.List(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			if len(list) != 1 || !list[0].ProvimuxLoaded() {
				t.Fatalf("stock %s reader returned %d records or no mark", kind, len(list))
			}
		})
	}
}
