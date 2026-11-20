package savings

// Deltas since the last save (T-122): the cases of the second review (T-121, findings 1, 5, 6 and 8,
// adapted from its focused_test.go) and the guarantees of the design that replaced the in-memory
// totals (01-recording.md section 5).

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func (s *testStore) setFail(errSet, errGet error) {
	s.mu.Lock()
	s.failSet, s.failGet = errSet, errGet
	s.mu.Unlock()
}

// Finding 1 of T-121: a failed flush before a store switch drops nothing. The deltas A could not take
// are written to the store in use at the next successful round (B); A keeps exactly what it had.
func TestDeltasFailedFlushBeforeSwitchLosesNothing(t *testing.T) {
	a, b := openTestStore(t, ""), openTestStore(t, "")
	seed := newTestEnv(t, b)
	seed.record(claudeRecord("b", "key-b", 1, 0, 0, 1))
	seed.record(claudeRecord("b", "key-b", 1, 0, 0, 1))
	seed.round()

	env := newTestEnv(t, a)
	env.record(claudeRecord("a", "key-a", 1, 0, 0, 1))
	env.round()
	aBefore := a.raw(t, dayKey(today))
	a.setFail(errDiskFull, nil)
	env.record(claudeRecord("a", "key-a", 1, 0, 0, 1))
	env.round()
	if got := env.query(t, "period=today").Totals.Requests; got != 2 {
		t.Fatalf("A with one delta waiting: %d", got)
	}
	env.r.flush(context.Background()) // the reload's flush fails too
	env.store = b
	if got := modelRequests(env.query(t, "period=today")); got["a"] != 1 || got["b"] != 2 {
		t.Fatalf("right after the switch: B's rows and the waiting delta: %v", got)
	}
	env.record(claudeRecord("b", "key-b", 1, 0, 0, 1))
	env.round()
	a.setFail(nil, nil)
	env.r.flush(context.Background())
	env.store = a
	env.round()

	as, bs := storedModels(t, a), storedModels(t, b)
	if !bytes.Equal(a.raw(t, dayKey(today)), aBefore) || as["a"] != 1 {
		t.Fatalf("A must be untouched after its failed write: %v", as)
	}
	if bs["a"] != 1 || bs["b"] != 3 {
		t.Fatalf("B: %v, want the delta A could not take (a 1) and its own 3", bs)
	}
	if total := sumRows(storedDay(t, a, today)).Req + sumRows(storedDay(t, b, today)).Req; total != 5 {
		t.Fatalf("recorded 5, stored %d", total)
	}
	if len(env.waiting()) != 0 {
		t.Fatal("nothing left in memory")
	}
}

// commitStore pauses a query's read of one day, and a round right after its day write committed.
type commitStore struct {
	localStore
	day                                  string
	readOnce, setOnce                    sync.Once
	atRead, resumeRead, atSet, resumeSet chan struct{}
	pauseRead, pauseSet                  bool
}

func newCommitStore(inner localStore, pauseRead, pauseSet bool) *commitStore {
	return &commitStore{localStore: inner, day: dayKey(today), pauseRead: pauseRead, pauseSet: pauseSet,
		atRead: make(chan struct{}), resumeRead: make(chan struct{}), atSet: make(chan struct{}), resumeSet: make(chan struct{})}
}

func (s *commitStore) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	if s.pauseRead && len(keys) == 1 && keys[0] == s.day {
		s.readOnce.Do(func() { close(s.atRead); <-s.resumeRead })
	}
	return s.localStore.KVMGet(ctx, keys...)
}

func (s *commitStore) KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error) {
	ok, err := s.localStore.KVSet(ctx, key, value, opts)
	if s.pauseSet && key == s.day && err == nil {
		s.setOnce.Do(func() { close(s.atSet); <-s.resumeSet })
	}
	return ok, err
}

func notWithin(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s while the other held the lock", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// Finding 5 of T-121: a query and a save never interleave, whichever starts first, so a request is
// counted once even when the query would read right after Bolt committed.
func TestDeltasQueryAndSaveDoNotInterleave(t *testing.T) {
	// The round commits, then pauses before it forgets the delta; a query arrives then.
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	gate := newCommitStore(store, false, true)
	env.r.opts.store = func() (localStore, error) { return gate, nil }
	env.record(claudeRecord("claude-opus-5-5", "client-key-commit-00001", 100, 1000, 10, 30))
	saved := make(chan struct{})
	go func() { env.round(); close(saved) }()
	<-gate.atSet
	if got := sumRows(storedDay(t, store, today)).Req; got != 1 {
		t.Fatalf("premise: Bolt has committed the request: %d", got)
	}
	answered := make(chan struct{})
	var during *Response
	go func() { during = env.query(t, "period=today&group=day"); close(answered) }()
	notWithin(t, answered, "a query answered")
	close(gate.resumeSet)
	<-saved
	<-answered
	if during.Totals.Requests != 1 || during.Totals.Tokens.Input.CacheRead != 1000 {
		t.Fatalf("query after the commit: %d requests, %d cache reads", during.Totals.Requests, during.Totals.Tokens.Input.CacheRead)
	}

	// The query reads first; the round waits for it.
	store2 := openTestStore(t, "")
	env2 := newTestEnv(t, store2)
	gate2 := newCommitStore(store2, true, false)
	env2.r.opts.store = func() (localStore, error) { return gate2, nil }
	env2.record(claudeRecord("claude-opus-5-5", "client-key-commit-00002", 100, 1000, 10, 30))
	answered2 := make(chan struct{})
	var first *Response
	go func() { first = env2.query(t, "period=today"); close(answered2) }()
	<-gate2.atRead
	saved2 := make(chan struct{})
	go func() { env2.round(); close(saved2) }()
	notWithin(t, saved2, "a round ran")
	close(gate2.resumeRead)
	<-answered2
	<-saved2
	if first.Totals.Requests != 1 {
		t.Fatalf("query before the round: %d", first.Totals.Requests)
	}
	if got := env2.query(t, "period=today").Totals.Requests; got != 1 {
		t.Fatalf("query after the round: %d", got)
	}
}

// Finding 6 of T-121: a row is "recorded" only once its price is on disk. While its first write is
// in flight a query waits for it; after a failed write the row floats with the current list.
func TestDeltasRecordedOnlyFromDisk(t *testing.T) {
	id, cr := "anthropic/claude-opus-5.5", 0.1
	list := func(in float64) map[string]registry.ProvimuxOpenRouterPrice {
		return map[string]registry.ProvimuxOpenRouterPrice{id: {Input: in, Output: 10, CacheRead: &cr}}
	}

	// A failed write.
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.prices.set(list(2), testNow)
	env.record(claudeRecord("anchor", "key", 1, 0, 0, 1))
	env.round()
	store.setFail(errDiskFull, nil)
	env.record(claudeRecord("claude-opus-5-5", "key", 1000, 100000, 0, 200))
	env.round()
	before := env.query(t, "period=today&model=claude-opus-5-5")
	env.prices.set(list(4), testNow.Add(time.Hour))
	after := env.query(t, "period=today&model=claude-opus-5-5")
	if storedModels(t, store)["claude-opus-5-5"] != 0 {
		t.Fatal("premise: the write failed")
	}
	for name, a := range map[string]*Response{"before": before, "after": after} {
		if a.Models[0].Price == nil || a.Models[0].Price.Basis != "current" || a.Totals.Estimate.FloatingRequests != 1 {
			t.Fatalf("%s the list changed: basis %+v, floating %d", name, a.Models[0].Price, a.Totals.Estimate.FloatingRequests)
		}
	}
	if before.Totals.Estimate.CostUSD == after.Totals.Estimate.CostUSD {
		t.Fatal("a row not on disk moves with the current list")
	}
	store.setFail(nil, nil)
	env.round()
	if a := env.query(t, "period=today&model=claude-opus-5-5"); a.Models[0].Price.Basis != "recorded" || a.Totals.Estimate.FloatingRequests != 0 ||
		a.Models[0].Price.InputPerMTok != 4 {
		t.Fatalf("once written, the price of the write is recorded: %+v", a.Models[0].Price)
	}

	// A write in flight.
	store2 := openTestStore(t, "")
	env2 := newTestEnv(t, store2)
	env2.prices.set(list(2), testNow)
	env2.record(claudeRecord("anchor", "first", 1, 0, 0, 1))
	env2.round()
	env2.record(claudeRecord("claude-opus-5-5", "second", 1000, 100000, 0, 200))
	store2.mu.Lock()
	store2.block, store2.entered = make(chan struct{}), make(chan struct{}, 1)
	release := store2.block
	store2.mu.Unlock()
	saved := make(chan struct{})
	go func() { env2.round(); close(saved) }()
	<-store2.entered
	answered := make(chan struct{})
	var during *Response
	go func() { during = env2.query(t, "period=today&model=claude-opus-5-5"); close(answered) }()
	notWithin(t, answered, "a query answered while the write was in flight")
	store2.mu.Lock()
	store2.block = nil
	store2.mu.Unlock()
	close(release)
	<-saved
	<-answered
	if storedModels(t, store2)["claude-opus-5-5"] != 1 || during.Models[0].Price.Basis != "recorded" || during.Totals.Estimate.FloatingRequests != 0 {
		t.Fatalf("after the commit: basis %s, floating %d", during.Models[0].Price.Basis, during.Totals.Estimate.FloatingRequests)
	}
}

// Finding 8 of T-121: after a switch from a full A to an empty B, B's first request takes a regular
// row: the row limit is applied against the store the deltas are written to.
func TestDeltasFullStoreDoesNotFoldInTheNext(t *testing.T) {
	a, b := openTestStore(t, ""), openTestStore(t, "")
	env := newTestEnv(t, a)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 2, Output: 10}}, testNow)
	for i := range maxRows {
		env.record(claudeRecord("claude-opus-5-5", fmt.Sprintf("key-%04d", i), 10, 0, 0, 2))
	}
	env.round()
	env.r.flush(context.Background())
	if d := storedDay(t, a, today); d.regular != maxRows || d.folded {
		t.Fatalf("premise: A is full, nothing folded: %d %v", d.regular, d.folded)
	}
	env.store = b
	env.record(claudeRecord("claude-opus-5-5", "first-on-b", 10, 0, 0, 2))
	env.round()
	answer := env.query(t, "period=today")
	if d := storedDay(t, b, today); d.regular != 1 || d.folded || answer.Totals.Estimate.UnpricedRequests != 0 || answer.Totals.Requests != 1 {
		t.Fatalf("B: regular %d, folded %v, unpriced %d, requests %d", d.regular, d.folded, answer.Totals.Estimate.UnpricedRequests, answer.Totals.Requests)
	}
	// And A full: a new combination folds there, a repeat does not.
	env.store = a
	env.record(claudeRecord("claude-opus-5-5", "key-0000", 10, 0, 0, 2))
	env.record(claudeRecord("claude-opus-5-5", "new-on-a", 10, 0, 0, 2))
	env.round()
	d := storedDay(t, a, today)
	if d.regular != maxRows || !d.folded || sumRows(d).Req != maxRows+2 {
		t.Fatalf("A: regular %d, folded %v, requests %d", d.regular, d.folded, sumRows(d).Req)
	}
}

// A failed write keeps the deltas, logs once for the streak, and the next good round writes them.
func TestDeltasFailedWriteKeptThenWritten(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	var logs bytes.Buffer
	restore := captureLogs(&logs)
	defer restore()
	store.setFail(errDiskFull, nil)
	for i := 0; i < 3; i++ {
		env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
		env.round()
	}
	if got := strings.Count(logs.String(), "local store call failed"); got != 1 {
		t.Fatalf("logged %d times in one streak, want once:\n%s", got, logs.String())
	}
	if d := env.waiting()[today]; d == nil || d.requests() != 3 {
		t.Fatal("the three deltas wait in memory")
	}
	if a := env.query(t, "period=today"); a.Recording.Persistent || a.Totals.Requests != 4 {
		t.Fatalf("during the failure: persistent %v, requests %d", a.Recording.Persistent, a.Totals.Requests)
	}
	// A failed read keeps them too.
	store.setFail(nil, errDiskFull)
	env.round()
	store.setFail(nil, nil)
	env.round()
	if got := sumRows(storedDay(t, store, today)).Req; got != 4 {
		t.Fatalf("written at the next good round: %d", got)
	}
	if len(env.waiting()) != 0 {
		t.Fatal("forgotten once written")
	}
	if a := env.query(t, "period=today"); !a.Recording.Persistent || a.Totals.Requests != 4 {
		t.Fatalf("after: persistent %v, requests %d", a.Recording.Persistent, a.Totals.Requests)
	}
	// A new streak logs again.
	store.setFail(errDiskFull, nil)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if got := strings.Count(logs.String(), "local store call failed"); got != 2 {
		t.Fatalf("a second streak: %d log lines", got)
	}
	store.setFail(nil, nil)
}

// slowStore makes every write take a while, so rounds and queries overlap with records.
type slowStore struct {
	localStore
	delay time.Duration
}

func (s *slowStore) KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error) {
	time.Sleep(s.delay)
	return s.localStore.KVSet(ctx, key, value, opts)
}

// A query during saves sees each request exactly once: never fewer than were recorded before it
// started, never more than were recorded when it ended.
func TestDeltasQueryDuringSavesCountsOnce(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	slow := &slowStore{localStore: store, delay: 2 * time.Millisecond}
	env.r.opts.store = func() (localStore, error) { return slow, nil }
	const total = 3000
	var recorded atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the usage manager
		defer wg.Done()
		for i := 0; i < total; i++ {
			env.record(claudeRecord("claude-opus-5-5", fmt.Sprintf("client-key-%02d-xxxxxxxx", i%7), 1, 2, 3, 4))
			recorded.Add(1)
		}
	}()
	go func() { // the saver
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				env.round()
			}
		}
	}()
	queries := 0
	for recorded.Load() < total {
		lo := recorded.Load()
		answer, err := env.r.query(context.Background(), mustParse(t, "period=today&group=key"))
		hi := recorded.Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := answer.Totals.Requests; got < lo || got > hi || answer.Totals.Tokens.Input.CacheRead != 2*got {
			t.Fatalf("query %d saw %d requests (%d cache reads), recorded between %d and %d", queries, got, answer.Totals.Tokens.Input.CacheRead, lo, hi)
		}
		queries++
	}
	close(stop)
	wg.Wait()
	env.round()
	if got := sumRows(storedDay(t, store, today)).Req; got != total {
		t.Fatalf("stored %d, want %d", got, total)
	}
	if got := env.query(t, "period=today").Totals.Requests; got != total {
		t.Fatalf("answer %d", got)
	}
	t.Logf("%d queries overlapped the saves", queries)
}

// A switch from A to B with deltas waiting: they land in B; A is not touched, not even read.
func TestDeltasSwitchLandsInTheNewStore(t *testing.T) {
	a, b := openTestStore(t, ""), openTestStore(t, "")
	seed := newTestEnv(t, b)
	seed.record(claudeRecord("model-b", "client-key-on-store-b-0001", 1, 0, 0, 1))
	seed.round()

	env := newTestEnv(t, a)
	env.record(claudeRecord("model-a", "client-key-on-store-a-0001", 1, 0, 0, 1))
	env.round()
	aBytes, aMeta := a.raw(t, dayKey(today)), a.raw(t, metaKey)
	aCalls := a.calls.Load()
	env.record(claudeRecord("model-a", "client-key-on-store-a-0001", 1, 0, 0, 1)) // waiting at the switch
	env.store = b                                                                 // no flush in between
	env.record(claudeRecord("model-a", "client-key-on-store-a-0001", 1, 0, 0, 1))
	env.round()
	if a.calls.Load() != aCalls || !bytes.Equal(a.raw(t, dayKey(today)), aBytes) || !bytes.Equal(a.raw(t, metaKey), aMeta) {
		t.Fatal("A was touched after the switch")
	}
	got := storedModels(t, b)
	if got["model-b"] != 1 || got["model-a"] != 2 {
		t.Fatalf("B: %v, want its own 1 and the 2 waiting", got)
	}
	// B's ids are made with B's secret, never A's.
	_, secretB, err := decodeMeta(b.raw(t, metaKey))
	if err != nil {
		t.Fatal(err)
	}
	if d := storedDay(t, b, today); d.keys[keyID(secretB, "client-key-on-store-a-0001")] != "0001" {
		t.Fatalf("B's keys: %+v", d.keys)
	}
}

// The store off, then on: the deltas wait in memory and are valued as current, then are added to
// what the store holds; key ids are the store's.
func TestDeltasStoreOffThenOn(t *testing.T) {
	store := openTestStore(t, "")
	first := newTestEnv(t, store)
	first.record(claudeRecord("claude-opus-5-5", "client-key-off-on-00001", 10, 0, 0, 1))
	first.round()
	_, secret, _ := decodeMeta(store.raw(t, metaKey))
	fileBefore := store.raw(t, dayKey(today))

	env := newTestEnv(t, store)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}, testNow)
	env.off.Store(true)
	env.record(claudeRecord("claude-opus-5-5", "client-key-off-on-00001", 10, 0, 0, 1))
	env.record(claudeRecord("claude-opus-5-5", "client-key-off-on-00001", 10, 0, 0, 1))
	env.round()
	if !bytes.Equal(store.raw(t, dayKey(today)), fileBefore) {
		t.Fatal("a round with the store off wrote")
	}
	off := env.query(t, "period=today&group=key")
	if off.Recording.Persistent || off.Totals.Requests != 2 || off.Totals.Estimate.FloatingRequests != 2 || off.Models[0].Price.Basis != "current" {
		t.Fatalf("off: persistent %v, requests %d, floating %d", off.Recording.Persistent, off.Totals.Requests, off.Totals.Estimate.FloatingRequests)
	}
	if off.Breakdowns[0].Rows[0].Key.ID != keyID(env.r.secret, "client-key-off-on-00001") {
		t.Fatal("with the store off, the process's own ids")
	}
	env.off.Store(false)
	on := env.query(t, "period=today&group=key")
	if on.Totals.Requests != 3 || len(on.Breakdowns[0].Rows) != 1 || on.Breakdowns[0].Rows[0].Key.ID != keyID(secret, "client-key-off-on-00001") {
		t.Fatalf("on, before the round: %d requests, %+v", on.Totals.Requests, on.Breakdowns[0].Rows)
	}
	env.round()
	d := storedDay(t, store, today)
	if sumRows(d).Req != 3 || len(d.rows) != 1 || d.keys[keyID(secret, "client-key-off-on-00001")] != "0001" {
		t.Fatalf("stored: %+v", d.keys)
	}
	if len(env.waiting()) != 0 {
		t.Fatal("forgotten once written")
	}
}

// A stuck store does not delay HandleUsage: with a round blocked in KVSet (holding mu) and a query
// waiting behind it, 10,000 records are taken while the store is still blocked, each well under a
// millisecond; once the store moves, all are written exactly once.
func TestDeltasStuckStoreDoesNotDelayHandleUsage(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "client-key-stuck-000001", 1, 0, 0, 1))
	store.mu.Lock()
	store.block, store.entered = make(chan struct{}), make(chan struct{}, 1)
	release := store.block
	store.mu.Unlock()
	saved := make(chan struct{})
	go func() { env.round(); close(saved) }()
	<-store.entered // the round holds mu and is stuck in the write
	answered := make(chan struct{})
	var answer *Response
	go func() { answer = env.query(t, "period=today"); close(answered) }()

	const n = 10_000
	var slowest time.Duration
	took := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		for i := 0; i < n; i++ {
			begin := time.Now()
			env.record(claudeRecord("claude-opus-5-5", "client-key-stuck-000001", 1, 0, 0, 1))
			slowest = max(slowest, time.Since(begin))
		}
		took <- time.Since(start)
	}()
	var total time.Duration
	select {
	case total = <-took:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleUsage waited for the stuck store")
	}
	select {
	case <-saved:
		t.Fatal("premise: the round is still stuck")
	case <-answered:
		t.Fatal("premise: the query waits for the round")
	default:
	}
	if slowest > 50*time.Millisecond {
		t.Fatalf("one HandleUsage took %v while the store was stuck", slowest)
	}
	t.Logf("%d records in %v with the store stuck (slowest %v)", n, total, slowest)

	store.mu.Lock()
	store.block = nil
	store.mu.Unlock()
	close(release)
	<-saved
	<-answered
	if answer.Totals.Requests != n+1 {
		t.Fatalf("the waiting query saw %d, want %d", answer.Totals.Requests, n+1)
	}
	env.round()
	if got := sumRows(storedDay(t, store, today)).Req; got != n+1 {
		t.Fatalf("stored %d, want %d", got, n+1)
	}
}

// BenchmarkHandleUsageDuringSave: HandleUsage while a save round holds the lock, stuck in a write.
func BenchmarkHandleUsageDuringSave(b *testing.B) {
	store := openTestStore(b, "")
	env := newTestEnv(b, store)
	env.record(claudeRecord("claude-opus-5-5", "k", 1, 0, 0, 1))
	store.mu.Lock()
	store.block, store.entered = make(chan struct{}), make(chan struct{}, 1)
	release := store.block
	store.mu.Unlock()
	done := make(chan struct{})
	go func() { env.round(); close(done) }()
	<-store.entered
	rec := claudeRecord("claude-opus-5-5", "client-key-0123456789", 100, 1000, 10, 50)
	rec.RequestedAt = testNow
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.r.HandleUsage(ctx, rec)
	}
	b.StopTimer()
	store.mu.Lock()
	store.block = nil
	store.mu.Unlock()
	close(release)
	<-done
}

// BenchmarkHandleUsageWithSaves: HandleUsage while rounds run back to back on a real bbolt file.
func BenchmarkHandleUsageWithSaves(b *testing.B) {
	store := openTestStore(b, "")
	env := newTestEnv(b, store)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				env.round()
			}
		}
	}()
	rec := claudeRecord("claude-opus-5-5", "client-key-0123456789", 100, 1000, 10, 50)
	rec.RequestedAt = testNow
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.r.HandleUsage(ctx, rec)
	}
	b.StopTimer()
	close(stop)
	<-done
}

// rejectKey fails every write of one key while fail is set; the rest of the store works.
type rejectKey struct {
	localStore
	key  string
	fail atomic.Bool
}

func (s *rejectKey) KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error) {
	if key == s.key && s.fail.Load() {
		return false, errDiskFull
	}
	return s.localStore.KVSet(ctx, key, value, opts)
}

// T-124 finding 7: after a switch from A (which has the list copy) to a populated B without one, an
// idle round and the clean flush save the list in B, so a restart on B still values its history.
func TestListFollowsTheStoreInUse(t *testing.T) {
	cr := 0.1
	list := map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 2, Output: 10, CacheRead: &cr}}
	a, b := openTestStore(t, ""), openTestStore(t, "")
	old := newTestEnv(t, b) // B: one request recorded without a price
	old.record(claudeRecord("claude-opus-5-5", "client-key-old-on-b-001", 1000, 100000, 0, 200))
	old.round()

	env := newTestEnv(t, a)
	env.prices.set(list, testNow)
	env.record(claudeRecord("claude-opus-5-5", "client-key-on-a-0000001", 1000, 100000, 0, 200))
	env.round()
	if _, found, _ := a.inner.KVGet(context.Background(), openRouterListKey); !found {
		t.Fatal("premise: A has the list copy")
	}
	env.r.flush(context.Background())
	env.store = b
	shown := env.query(t, "period=today")
	env.round() // idle: no deltas
	if _, found, err := b.inner.KVGet(context.Background(), openRouterListKey); err != nil || !found {
		t.Fatalf("the idle round did not save the list in B: found=%v err=%v", found, err)
	}
	writes := b.writes.Load()
	env.round()
	env.r.flush(context.Background())
	if b.writes.Load() != writes {
		t.Fatal("a list already current in the store is written again")
	}
	restarted := newTestEnv(t, b).query(t, "period=today")
	if shown.Totals.Estimate.PricedRequests != 1 || restarted.Totals.Estimate.PricedRequests != 1 ||
		restarted.Totals.Estimate.SavedByCachingUSD != shown.Totals.Estimate.SavedByCachingUSD {
		t.Fatalf("before the restart %+v, after %+v", shown.Totals.Estimate, restarted.Totals.Estimate)
	}
}

// T-124 finding 14: first_day never lags the days. When moving it back fails, the earlier day is not
// written either and its delta waits; once meta can be written, both are, and period=all and 7d agree.
func TestFirstDayBeforeTheDay(t *testing.T) {
	store := openTestStore(t, "")
	gate := &rejectKey{localStore: store, key: metaKey}
	env := newTestEnv(t, store)
	env.r.opts.store = func() (localStore, error) { return gate, nil }
	env.record(claudeRecord("claude-opus-5-5", "client-key-first-day-01", 42, 0, 0, 1))
	env.round()
	late := claudeRecord("claude-opus-5-5", "client-key-first-day-01", 12, 0, 0, 1)
	late.RequestedAt = testNow.AddDate(0, 0, -1) // began before midnight, recorded after today was saved
	yesterday := late.RequestedAt.In(bucharest).Format(time.DateOnly)
	env.record(late)
	gate.fail.Store(true)
	env.round()
	if _, found, _ := store.inner.KVGet(context.Background(), dayKey(yesterday)); found {
		t.Fatal("the earlier day was written while first_day could not move back")
	}
	if d := env.waiting()[yesterday]; d == nil || d.requests() != 1 {
		t.Fatal("its delta waits in memory")
	}
	if a := env.query(t, "period=all"); a.Totals.Requests != 2 || a.Recording.Persistent {
		t.Fatalf("during the failure: all=%d persistent=%v", a.Totals.Requests, a.Recording.Persistent)
	}
	gate.fail.Store(false)
	env.round()
	if got := readMeta(t, store).FirstDay; got != yesterday {
		t.Fatalf("first_day %s, want %s", got, yesterday)
	}
	fresh := newTestEnv(t, store)
	all, week := fresh.query(t, "period=all"), fresh.query(t, "period=7d")
	if all.Totals.Requests != 2 || week.Totals.Requests != 2 || all.Recording.Since == nil || *all.Recording.Since != yesterday {
		t.Fatalf("after recovery and a restart: all=%d 7d=%d since=%v", all.Totals.Requests, week.Totals.Requests, all.Recording.Since)
	}
}

// T-124 finding 15: a folded key leaves no id or tail in the day.
func TestFoldedKeysLeaveNoIDs(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	for i := range maxRows {
		env.record(claudeRecord("fixed-model", fmt.Sprintf("client-key-regular-%05d", i), 1, 0, 0, 1))
	}
	env.round()
	for i := range 50 {
		env.record(claudeRecord("fixed-model", fmt.Sprintf("client-key-folded-%06d", i), 1, 0, 0, 1))
	}
	env.round()
	d := storedDay(t, store, today)
	if !d.folded || d.regular != maxRows || sumRows(d).Req != maxRows+50 {
		t.Fatalf("premise: regular %d, folded %v, requests %d", d.regular, d.folded, sumRows(d).Req)
	}
	if len(d.keys) != maxRows {
		t.Fatalf("%d key ids for %d regular rows", len(d.keys), d.regular)
	}
	for id := range d.keys {
		if _, ok := d.rows[rowKey{k: "oauth", p: "claude", m: "fixed-model", c: id}]; !ok {
			t.Fatalf("key id %s has no row", id)
		}
	}
}
