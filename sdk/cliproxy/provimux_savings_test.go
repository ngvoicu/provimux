package cliproxy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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
