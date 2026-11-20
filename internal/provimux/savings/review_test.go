package savings

// Regressions of the critical review of Savings (T-112), one test per backend finding (T-116).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func modelRequests(a *Response) map[string]int64 {
	out := map[string]int64{}
	for _, m := range a.Models {
		out[m.Model] = m.Requests
	}
	return out
}

func storedModels(t *testing.T, s *testStore) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for k, r := range storedDay(t, s, today).rows {
		out[k.m] += r.n.Req
	}
	return out
}

// Finding 1: switching server.kv-store.path from A to a populated B keeps both histories.
func TestReviewStoreSwitchKeepsBothHistories(t *testing.T) {
	a, b := openTestStore(t, ""), openTestStore(t, "")
	other := newTestEnv(t, b) // B already holds two requests for model B, today
	other.record(claudeRecord("model-b", "client-key-on-store-b-0001", 1, 0, 0, 1))
	other.record(claudeRecord("model-b", "client-key-on-store-b-0001", 1, 0, 0, 1))
	other.round()

	env := newTestEnv(t, a)
	env.record(claudeRecord("model-a", "client-key-on-store-a-0001", 1, 0, 0, 1))
	env.round()
	env.record(claudeRecord("model-a", "client-key-on-store-a-0001", 1, 0, 0, 1))
	env.r.flush(context.Background()) // the config reload flushes to the old store first
	env.store = b                     // the path now points at B

	if got := modelRequests(env.query(t, "period=today")); got["model-b"] != 2 || got["model-a"] != 0 {
		t.Fatalf("right after the switch the answer is B's: %v", got)
	}
	env.record(claudeRecord("model-a", "client-key-on-store-a-0001", 1, 0, 0, 1))
	env.round()
	if got := storedModels(t, b); got["model-b"] != 2 || got["model-a"] != 1 {
		t.Fatalf("B on disk: %v, want its 2 model-b requests and the 1 recorded after the switch", got)
	}
	if got := storedModels(t, a); got["model-a"] != 2 || got["model-b"] != 0 {
		t.Fatalf("A on disk: %v, want the 2 model-a requests recorded before the switch", got)
	}
	if got := modelRequests(env.query(t, "period=today")); got["model-b"] != 2 || got["model-a"] != 1 {
		t.Fatalf("answer after the write: %v", got)
	}
}

// gatedStore pauses the first read of one day so a test can run a saver round in between.
type gatedStore struct {
	localStore
	day    string
	once   sync.Once
	atRead chan struct{}
	resume chan struct{}
}

func (s *gatedStore) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	if len(keys) == 1 && keys[0] == s.day {
		s.once.Do(func() {
			close(s.atRead)
			<-s.resume
		})
	}
	return s.localStore.KVMGet(ctx, keys...)
}

// Finding 5: a query whose read straddles the first save of a day counts each record once.
func TestReviewQueryOverlappingTheFirstSave(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	gated := &gatedStore{localStore: store, day: dayKey(today), atRead: make(chan struct{}), resume: make(chan struct{})}
	env.r.opts.store = func() (localStore, error) { return gated, nil }
	env.record(claudeRecord("claude-opus-5-5", "client-key-overlap-000001", 100, 1000, 10, 30))
	got := make(chan *Response, 1)
	go func() {
		answer, err := env.r.query(context.Background(), mustParse(t, "period=today&group=key&group=day"))
		if err != nil {
			t.Error(err)
		}
		got <- answer
	}()
	<-gated.atRead // the query copied pending and is about to read the day
	env.round()    // the saver writes that pending record
	close(gated.resume)
	during := <-got
	if during == nil || during.Totals.Requests != 1 || during.Totals.Tokens.Input.CacheRead != 1000 {
		t.Fatalf("overlapping query: %+v", during.Totals)
	}
}

// Finding 6: with the store off, a price can change with the list, so it is never "recorded".
func TestReviewStoreOffPricesFloat(t *testing.T) {
	env := newTestEnv(t, nil)
	cr := 0.1
	id := "anthropic/claude-opus-5.5"
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{id: {Input: 2, Output: 10, CacheRead: &cr}}, testNow)
	env.record(claudeRecord("claude-opus-5-5", "client-key-off-0000001", 1000, 100000, 0, 200))
	env.round()
	before := env.query(t, "period=today")
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{id: {Input: 4, Output: 10, CacheRead: &cr}}, testNow.Add(time.Hour))
	env.round()
	after := env.query(t, "period=today")
	for name, a := range map[string]*Response{"before": before, "after": after} {
		if a.Totals.Estimate.FloatingRequests != 1 || a.Models[0].Price == nil || a.Models[0].Price.Basis != "current" {
			t.Fatalf("%s: floating %d, price %+v", name, a.Totals.Estimate.FloatingRequests, a.Models[0].Price)
		}
	}
	if before.Totals.Estimate.SavedByCachingUSD == after.Totals.Estimate.SavedByCachingUSD {
		t.Fatal("premise: the amount moved with the list")
	}
}

// Finding 7: a list whose first download completes after the last write is saved by an idle round
// (or the clean stop), so a restart still values the recorded day.
func TestReviewFirstListAfterIdleIsSaved(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	cr := 0.1
	env.record(claudeRecord("claude-opus-5-5", "client-key-idle-0000001", 1000, 100000, 0, 200))
	env.round() // asks for the list; the download has not completed
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 2, Output: 10, CacheRead: &cr}}, testNow)
	before := env.query(t, "period=today")
	env.round() // idle: no day to write
	env.r.flush(context.Background())
	if _, found, err := store.inner.KVGet(context.Background(), openRouterListKey); err != nil || !found {
		t.Fatalf("the arrived list was not saved: found=%v err=%v", found, err)
	}
	restarted := newTestEnv(t, store) // no list downloaded in the new process
	after := restarted.query(t, "period=today")
	if before.Totals.Estimate.PricedRequests != 1 || after.Totals.Estimate.PricedRequests != 1 ||
		after.Totals.Estimate.SavedByCachingUSD != before.Totals.Estimate.SavedByCachingUSD {
		t.Fatalf("before %+v, after restart %+v", before.Totals.Estimate, after.Totals.Estimate)
	}
}

// Finding 8: at exactly 2000 distinct combinations nothing is folded; a repeat takes no slot.
func TestReviewRowLimitCountsDistinctCombinations(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 2, Output: 10}}, testNow)
	for i := 0; i < 1999; i++ {
		env.record(claudeRecord("claude-opus-5-5", fmt.Sprintf("key-%04d", i), 10, 0, 0, 2))
	}
	env.round()
	env.record(claudeRecord("claude-opus-5-5", "key-0000", 10, 0, 0, 2)) // a repeat
	env.record(claudeRecord("claude-opus-5-5", "new-key", 10, 0, 0, 2))  // the 2000th combination
	env.round()
	answer := env.query(t, "period=today")
	if d := storedDay(t, store, today); d.regular != 2000 || d.folded {
		t.Fatalf("regular rows %d, folded %v", d.regular, d.folded)
	}
	if answer.Recording.FoldedDays != 0 || answer.Totals.Estimate.UnpricedRequests != 0 || answer.Totals.Requests != 2001 {
		t.Fatalf("folded days %d, unpriced %d, requests %d", answer.Recording.FoldedDays, answer.Totals.Estimate.UnpricedRequests, answer.Totals.Requests)
	}
	// The 2001st combination is folded, as before.
	env.record(claudeRecord("claude-opus-5-5", "one-too-many", 10, 0, 0, 2))
	env.round()
	if d := storedDay(t, store, today); d.regular != 2000 || !d.folded {
		t.Fatalf("over the limit: regular %d, folded %v", d.regular, d.folded)
	}
}

// Finding 2: a dated snapshot is priced as itself when the list publishes it.
func TestReviewDatedModelIDs(t *testing.T) {
	book := bookWith(testList(t), nil)
	ref, p, ok := book.resolvePrice("codex", "gpt-4o-2024-05-13")
	if !ok || ref.ID != "openai/gpt-4o-2024-05-13" || ref.Match != matchNormalized || p.In != 5 || p.Out != 15 {
		t.Fatalf("got %+v %+v %v", ref, p, ok)
	}
	if cost, _ := amounts(counters{In: 100_000, Out: 20_000}, p); roundMoney(cost) != 0.8 {
		t.Fatalf("cost %v, want 0.80", cost)
	}
	// A date the list does not publish still falls back to the product without it.
	if ref, _, ok = book.resolvePrice("claude", "claude-opus-4-5-20251101"); !ok || ref.ID != "anthropic/claude-opus-4.5" {
		t.Fatalf("fallback: %+v", ref)
	}
}

// Finding 9: a Kimi login served through its Claude Messages route counts under kimi, the account's
// provider, not claude, the executor's. Its tokens keep the Claude route's semantics.
func TestReviewProviderIsTheAccounts(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("review-kimi-login", "kimi", []*registry.ModelInfo{{ID: "kimi-k2.6"}})
	t.Cleanup(func() { reg.UnregisterClient("review-kimi-login") })
	opts := defaultOptions()
	opts.store = func() (localStore, error) { return nil, nil }
	opts.now = func() time.Time { return testNow }
	opts.location = bucharest
	r := newRecorder(opts)
	r.HandleUsage(context.Background(), coreusage.Record{
		Provider: "claude", Model: "kimi-k2.6", AuthType: "oauth", AuthID: "review-kimi-login",
		Detail: coreusage.Detail{InputTokens: 100, CacheReadTokens: 900, OutputTokens: 10},
	})
	r.HandleUsage(context.Background(), coreusage.Record{
		Provider: "claude", Model: "claude-opus-5-5", AuthType: "oauth", AuthID: "review-unregistered",
		Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1},
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	got := map[string]counters{}
	for k, c := range r.days[today].pending {
		got[k.p+"/"+k.m] = *c
	}
	if c, ok := got["kimi/kimi-k2.6"]; !ok || c.In != 100 || c.Cr != 900 {
		t.Fatalf("pending %v: want kimi/kimi-k2.6 with in 100, cr 900", got)
	}
	if _, ok := got["claude/claude-opus-5-5"]; !ok {
		t.Fatalf("an auth the registry does not know keeps the executor's provider: %v", got)
	}
}

// Finding 10: the answer says its estimates use base list rates.
func TestReviewPricesSayBaseRates(t *testing.T) {
	answer := generic(t, newTestEnv(t, nil).query(t, "period=today")).(map[string]any)
	prices, _ := answer["prices"].(map[string]any)
	if prices["rates"] != "base" {
		t.Fatalf("prices.rates = %v, want base", prices["rates"])
	}
	for _, name := range []string{"examples/savings-month.json", "examples/savings-empty.json"} {
		if !strings.Contains(string(readDesign(t, name)), `"rates": "base"`) {
			t.Errorf("%s does not carry prices.rates", name)
		}
	}
}
