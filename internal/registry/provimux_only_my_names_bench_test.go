package registry

import (
	"fmt"
	"testing"
)

// O2 of only-my-names.md 16.7: benchmarks that use upstream's API only, so the same file runs on the base
// (9ffc2a244) and on the head. Switch off (no rule is set in a benchmark process): 20 accounts of 500 names.

func namesBenchAccounts(tb testing.TB, accounts, names int) {
	tb.Helper()
	for a := range accounts {
		models := make([]*ModelInfo, 0, names)
		for n := range names {
			models = append(models, &ModelInfo{ID: fmt.Sprintf("bench-%d-%d", a, n), Object: "model", OwnedBy: "bench",
				Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}}})
		}
		id := fmt.Sprintf("bench-acct-%d", a)
		GetGlobalRegistry().RegisterClient(id, "bench", models)
		tb.Cleanup(func() { GetGlobalRegistry().UnregisterClient(id) })
	}
}

func BenchmarkProvimuxO2GetAvailableModelInfos(b *testing.B) {
	namesBenchAccounts(b, 20, 500)
	reg := GetGlobalRegistry()
	b.ReportAllocs()
	for b.Loop() {
		reg.GetAvailableModelInfos()
	}
}

func BenchmarkProvimuxO2UncachedGetAvailableModels(b *testing.B) {
	namesBenchAccounts(b, 20, 500)
	reg := GetGlobalRegistry()
	b.ReportAllocs()
	for b.Loop() {
		reg.mutex.Lock()
		reg.invalidateAvailableModelsCacheLocked()
		reg.mutex.Unlock()
		reg.GetAvailableModels("openai")
	}
}

// The registration of 100 models (418 allocations at the base, per the first review).
func BenchmarkProvimuxO2Register100(b *testing.B) {
	models := make([]*ModelInfo, 0, 100)
	for n := range 100 {
		models = append(models, &ModelInfo{ID: fmt.Sprintf("reg-%d", n), Object: "model", OwnedBy: "bench",
			Thinking: &ThinkingSupport{Levels: []string{"low", "medium", "high"}}})
	}
	reg := GetGlobalRegistry()
	b.Cleanup(func() { reg.UnregisterClient("reg-acct") })
	b.ReportAllocs()
	for b.Loop() {
		reg.RegisterClient("reg-acct", "bench", models)
		reg.UnregisterClient("reg-acct")
	}
}
