package cliproxy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// A clean stop writes the day totals before the local store closes: shutdownKVStore, the step of
// Service.Shutdown that closes the store, flushes Savings first.
func TestProvimuxSavingsCleanStopWrites(t *testing.T) {
	registry.SetLocalModelCatalogs(true) // no download from openrouter.ai in this test
	t.Cleanup(func() {
		registry.SetLocalModelCatalogs(false)
		home.ClearCurrent()
		_ = kvstore.ShutdownLocal()
	})
	path := filepath.Join(t.TempDir(), "kv.db")
	svc := &Service{}
	svc.applyKVStoreConfig(&config.Config{KVStore: config.KVStoreConfig{Path: path}})

	savings.Start()
	// Directly, not through the usage manager: kv_store_test.go's Shutdown has stopped this binary's.
	savings.RecordForTest(context.Background(), coreusage.Record{
		Provider: "claude", Model: "claude-opus-5-5", AuthType: "oauth",
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5},
	})

	if err := svc.shutdownKVStore(); err != nil {
		t.Fatal(err)
	}
	store, err := kvstore.OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	values, found, err := store.KVMGet(context.Background(), "provimux:savings:v1:meta")
	if err != nil || !found[0] || len(values[0]) == 0 {
		t.Fatalf("meta not written before the store closed: %v %v", found, err)
	}
	var day bool
	for offset := -1; offset <= 1 && !day; offset++ {
		key := "provimux:savings:v1:day:" + savingsDay(offset)
		_, ok, errGet := store.KVGet(context.Background(), key)
		if errGet != nil {
			t.Fatal(errGet)
		}
		day = ok
	}
	if !day {
		t.Fatal("the file opened again does not hold the day")
	}
}

func savingsDay(offset int) string {
	return time.Now().AddDate(0, 0, offset).Format(time.DateOnly)
}

// provimuxSavingsModels reads one day of a closed store file: requests per model.
func provimuxSavingsModels(t *testing.T, path, day string) map[string]int64 {
	t.Helper()
	store, err := kvstore.OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	data, found, err := store.KVGet(context.Background(), "provimux:savings:v1:day:"+day)
	if err != nil || !found {
		t.Fatalf("%s: day %s found=%v err=%v", path, day, found, err)
	}
	var value struct {
		Rows []struct {
			M string `json:"m"`
			N struct {
				Req int64 `json:"req"`
			} `json:"n"`
		} `json:"rows"`
	}
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, row := range value.Rows {
		out[row.M] += row.N.Req
	}
	return out
}

// A config reload that moves server.kv-store.path from a populated A to a populated B keeps both:
// what was recorded before the reload is written to A, what comes after joins B's own rows.
func TestProvimuxSavingsStorePathReloadKeepsBothStores(t *testing.T) {
	registry.SetLocalModelCatalogs(true)
	t.Cleanup(func() {
		registry.SetLocalModelCatalogs(false)
		home.ClearCurrent()
		_ = kvstore.ShutdownLocal()
	})
	dir := t.TempDir()
	pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
	day := savingsDay(0)
	seed, err := kvstore.OpenBoltStore(pathB)
	if err != nil {
		t.Fatal(err)
	}
	value := `{"v":1,"day":"` + day + `","tz":"Local","utc_offset":0,"keys":{},"prices":{},"rows":[{"k":"oauth","p":"claude","m":"model-b","n":{"req":2}}]}`
	if _, err = seed.KVSet(context.Background(), "provimux:savings:v1:day:"+day, []byte(value), kvstore.KVSetOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}

	record := coreusage.Record{Provider: "claude", Model: "model-a", AuthType: "oauth",
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5}}
	svc := &Service{}
	svc.applyKVStoreConfig(&config.Config{KVStore: config.KVStoreConfig{Path: pathA}})
	savings.Start()
	savings.RecordForTest(context.Background(), record)
	svc.applyKVStoreConfig(&config.Config{KVStore: config.KVStoreConfig{Path: pathB}})
	savings.RecordForTest(context.Background(), record)
	if err = svc.shutdownKVStore(); err != nil {
		t.Fatal(err)
	}
	if got := provimuxSavingsModels(t, pathA, day); got["model-a"] != 1 || len(got) != 1 {
		t.Fatalf("A: %v, want model-a 1", got)
	}
	if got := provimuxSavingsModels(t, pathB, day); got["model-b"] != 2 || got["model-a"] != 1 || len(got) != 2 {
		t.Fatalf("B: %v, want model-b 2 and model-a 1", got)
	}
}
