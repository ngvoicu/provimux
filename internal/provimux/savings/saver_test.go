package savings

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const today = "2026-11-20"

func TestTotalsAreAbsolute(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	for i := 0; i < 3; i++ {
		env.record(claudeRecord("claude-opus-5-5", "key-a", 10, 100, 5, 7))
	}
	env.round()
	if got := sumRows(storedDay(t, store, today)); got.Req != 3 || got.In != 30 || got.Cr != 300 || got.Cw != 15 || got.Out != 21 {
		t.Fatalf("after 3: %+v", got)
	}
	for i := 0; i < 2; i++ {
		env.record(claudeRecord("claude-opus-5-5", "key-a", 10, 100, 5, 7))
	}
	env.round()
	if got := sumRows(storedDay(t, store, today)); got.Req != 5 || got.In != 50 {
		t.Fatalf("after 5: %+v", got)
	}
	first := store.raw(t, dayKey(today))
	again, err := storedDay(t, store, today).encode(today)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatalf("the same totals must encode identical bytes:\n%s\n%s", first, again)
	}
	if len(env.waiting()) != 0 {
		t.Fatal("after a successful write memory holds nothing for the day")
	}
}

func TestIdleRoundMakesNoStoreCall(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	before := store.calls.Load()
	pricesBefore := env.prices.calls.Load()
	env.round()
	env.round()
	if store.calls.Load() != before {
		t.Fatalf("an idle round made %d store calls", store.calls.Load()-before)
	}
	if env.prices.calls.Load() != pricesBefore {
		t.Fatal("an idle round asked for the price list")
	}
}

func TestRestart(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "key-a", 10, 0, 0, 1))
	env.record(claudeRecord("claude-opus-5-5", "key-a", 10, 0, 0, 1))
	env.round()
	second := newTestEnv(t, store)
	second.record(claudeRecord("claude-opus-5-5", "key-a", 10, 0, 0, 1))
	before := store.getCount(dayKey(today))
	second.round()
	second.record(claudeRecord("claude-opus-5-5", "key-a", 10, 0, 0, 1))
	second.round()
	if got := sumRows(storedDay(t, store, today)); got.Req != 4 || got.In != 40 {
		t.Fatalf("old totals plus new: %+v", got)
	}
	if reads := store.getCount(dayKey(today)) - before; reads != 2 {
		t.Fatalf("the day was read %d times, want once per round that writes it", reads)
	}
	if d := storedDay(t, store, today); len(d.rows) != 1 {
		t.Fatalf("the same key must keep its id across a restart: %d rows", len(d.rows))
	}
}

func TestCrash(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)) // never written: the process dies
	path := store.path
	store.close(t)
	reopened := openTestStore(t, path)
	fresh := newTestEnv(t, reopened)
	fresh.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	fresh.round()
	if got := sumRows(storedDay(t, reopened, today)).Req; got != 2 {
		t.Fatalf("a new instance builds on exactly the last written totals: %d, want 1 + 1", got)
	}
}

func TestCleanStop(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.r.flush(context.Background())
	if got := sumRows(storedDay(t, store, today)).Req; got != 1 {
		t.Fatalf("flush wrote %d", got)
	}
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	store.mu.Lock()
	store.block = make(chan struct{})
	store.mu.Unlock()
	start := time.Now()
	env.r.flush(context.Background())
	if elapsed := time.Since(start); elapsed > 5*env.r.opts.flushTimeout {
		t.Fatalf("flush with a stuck store took %v", elapsed)
	}
	store.mu.Lock()
	close(store.block)
	store.block = nil
	store.mu.Unlock()
}

func TestStoreOff(t *testing.T) {
	env := newTestEnv(t, nil)
	env.record(claudeRecord("claude-opus-5-5", "key-a", 10, 0, 0, 1))
	env.round()
	if inMemory := env.waiting()[today]; inMemory == nil || len(inMemory.rows) != 1 {
		t.Fatal("with the store off the totals stay in memory")
	}
	env.r.opts.maxMemoryDays = 400
	start := time.Date(2025, 1, 1, 12, 0, 0, 0, bucharest)
	for i := 0; i < 450; i++ {
		rec := claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)
		rec.RequestedAt = start.AddDate(0, 0, i)
		env.record(rec)
	}
	env.round()
	waiting := env.waiting()
	kept := len(waiting)
	_, oldest := waiting["2025-01-01"]
	if kept != 400 || oldest {
		t.Fatalf("at most 400 days are kept, the oldest dropped first: %d (oldest kept: %v)", kept, oldest)
	}
}

func TestStoreAppearsLater(t *testing.T) {
	store := openTestStore(t, "")
	first := newTestEnv(t, store)
	first.record(claudeRecord("claude-opus-5-5", "", 10, 0, 0, 1))
	first.round()
	env := newTestEnv(t, store)
	env.off.Store(true)
	env.record(claudeRecord("claude-opus-5-5", "", 10, 0, 0, 1))
	env.record(claudeRecord("claude-opus-5-5", "", 10, 0, 0, 1))
	env.round()
	env.off.Store(false)
	env.round()
	if got := sumRows(storedDay(t, store, today)); got.Req != 3 || got.In != 30 {
		t.Fatalf("pending is added to what the file has: %+v", got)
	}
}

func TestWriteFails(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	store.mu.Lock()
	store.failSet = errDiskFull
	store.mu.Unlock()
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	env.r.mu.Lock()
	ok := env.r.lastWriteOK
	env.r.mu.Unlock()
	if ok {
		t.Fatal("the last write failed")
	}
	if d := env.waiting()[today]; d == nil || d.requests() != 1 {
		t.Fatal("the delta of a failed write must stay in memory")
	}
	store.mu.Lock()
	store.failSet = nil
	store.mu.Unlock()
	env.round()
	if got := sumRows(storedDay(t, store, today)).Req; got != 2 {
		t.Fatalf("a later round writes it: %d", got)
	}
	env.r.mu.Lock()
	ok = env.r.lastWriteOK
	env.r.mu.Unlock()
	if !ok {
		t.Fatal("persistent again after a write succeeded")
	}
}

func TestAValueThatDoesNotDecode(t *testing.T) {
	store := openTestStore(t, "")
	garbage := []byte(`{"v":1,"rows":"secret-value-marker"`)
	if _, err := store.inner.KVSet(context.Background(), dayKey(today), garbage, kvstore.KVSetOptions{}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	restore := captureLogs(&logs)
	defer restore()
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if got := store.raw(t, dayKey(today)); !bytes.Equal(got, garbage) {
		t.Fatal("a value that does not decode must not be overwritten")
	}
	if n := strings.Count(logs.String(), dayKey(today)); n != 1 {
		t.Fatalf("logged %d times, want once: %s", n, logs.String())
	}
	if strings.Contains(logs.String(), "secret-value-marker") {
		t.Fatal("the log line carries the value")
	}
	if d := env.waiting()[today]; d == nil || d.requests() != 2 {
		t.Fatal("the deltas for the day are kept in memory")
	}
}

// homeClient stands for Home's KV client: any KVClient that is not a *kvstore.BoltStore.
type homeClient struct {
	kvstore.KVClient
	calls int
}

func (h *homeClient) KVGet(context.Context, string) ([]byte, bool, error) {
	h.calls++
	return nil, false, nil
}

func TestOnlyTheLocalStore(t *testing.T) {
	home := &homeClient{}
	kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) { return home, true, nil })
	defer kvstore.RegisterHomeProvider(nil)
	store, err := currentLocalStore()
	if err != nil || store != nil {
		t.Fatalf("home mode: store=%v err=%v", store, err)
	}
	r := newRecorder(options{location: bucharest, store: currentLocalStore})
	r.HandleUsage(context.Background(), claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	r.saveRound(context.Background())
	if home.calls != 0 {
		t.Fatalf("%d calls reached Home's client", home.calls)
	}
}

func TestRowLimit(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	var want counters
	for i := 0; i < 2001; i++ {
		rec := claudeRecord(fmt.Sprintf("model-%04d", i), "", 1, 2, 3, 4)
		env.record(rec)
		want.add(sampleNow(rec).n)
	}
	env.round()
	d := storedDay(t, store, today)
	if len(d.rows) != 2001 || !d.folded || d.regular != 2000 {
		t.Fatalf("rows=%d regular=%d folded=%v", len(d.rows), d.regular, d.folded)
	}
	if other := d.rows[rowKey{k: "oauth", p: "claude", m: otherValue, c: otherValue}]; other == nil || other.n.Req != 1 {
		t.Fatalf("~other row: %+v", other)
	}
	if got := sumRows(d); got != want {
		t.Fatalf("sums %+v, want %+v", got, want)
	}
	// A late record for an existing row is merged, not folded; a new one is folded.
	env.record(claudeRecord("model-0000", "", 1, 0, 0, 0))
	env.record(claudeRecord("model-new", "", 1, 0, 0, 0))
	env.round()
	d = storedDay(t, store, today)
	if d.rows[rowKey{k: "oauth", p: "claude", m: "model-0000"}].n.Req != 2 || d.rows[rowKey{k: "oauth", p: "claude", m: otherValue, c: otherValue}].n.Req != 2 {
		t.Fatal("existing rows merge, new ones fold")
	}
}

func TestOldDaysLeaveMemory(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	old := claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)
	old.RequestedAt = time.Date(2026, 11, 15, 12, 0, 0, 0, bucharest)
	env.record(old)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if waiting := env.waiting(); len(waiting) != 0 {
		t.Fatalf("written days leave memory, whatever their age: %d kept", len(waiting))
	}
	env.record(old)
	env.round()
	if got := sumRows(storedDay(t, store, "2026-11-15")).Req; got != 2 {
		t.Fatalf("a late record is merged with the file: %d", got)
	}
}

func readMeta(t *testing.T, store *testStore) metaValue {
	t.Helper()
	meta, _, err := decodeMeta(store.raw(t, metaKey))
	if err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestMeta(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "client-key-aaaaaaaaaaaa", 1, 0, 0, 1))
	env.round()
	meta := readMeta(t, store)
	if meta.V != 1 || meta.FirstDay != today || meta.CreatedAt == "" {
		t.Fatalf("meta %+v", meta)
	}
	idBefore := keyID(env.r.secret, "client-key-aaaaaaaaaaaa")
	later := claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)
	later.RequestedAt = time.Date(2026, 11, 21, 12, 0, 0, 0, bucharest)
	env.record(later)
	env.round()
	if got := readMeta(t, store); got.FirstDay != today || got.CreatedAt != meta.CreatedAt {
		t.Fatalf("first_day only moves earlier: %+v", got)
	}
	earlier := claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)
	earlier.RequestedAt = time.Date(2026, 11, 2, 12, 0, 0, 0, bucharest)
	env.record(earlier)
	env.round()
	if got := readMeta(t, store); got.FirstDay != "2026-11-02" || got.KeySecret != meta.KeySecret {
		t.Fatalf("first_day moves earlier, the secret stays: %+v", got)
	}
	restarted := newTestEnv(t, store)
	restarted.record(claudeRecord("claude-opus-5-5", "client-key-aaaaaaaaaaaa", 1, 0, 0, 1))
	restarted.round()
	if d := storedDay(t, store, today); len(d.keys) != 1 || d.keys[idBefore] == "" {
		t.Fatalf("key ids must be the same after a restart: %+v", d.keys)
	}
}

func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune("0123456789abcdef", rune(s[i])) {
			return false
		}
	}
	return true
}

func TestKeyIDAndTail(t *testing.T) {
	secretA, secretB := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	id := keyID(secretA, "sk-client-1")
	if !isHex16(id) || keyID(secretA, "sk-client-1") != id || keyID(secretB, "sk-client-1") == id {
		t.Fatalf("id %q", id)
	}
	if keyID(secretA, "") != "" || keyTail("") != "" {
		t.Fatal(`no key gives ""`)
	}
	if keyTail("123456789012345") != "" || keyTail("1234567890123456") != "3456" || keyTail("ééééééééééééééézy") != "éézy" {
		t.Fatal("tail only from 16 characters")
	}
}

func TestTheKeyNeverReachesTheDisk(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	key := "sk-provimux-client-secret-key-0042"
	env.record(claudeRecord("claude-opus-5-5", key, 1, 0, 0, 1))
	env.round()
	if bytes.Contains(store.file(t), []byte(key)) || bytes.Contains(store.file(t), []byte(key[:20])) {
		t.Fatal("the client key reached the store's file")
	}
	if d := storedDay(t, store, today); d.keys[keyID(env.r.secret, key)] != "0042" {
		t.Fatalf("keys %+v", d.keys)
	}
}

func TestIDsUseTheStoredSecret(t *testing.T) {
	store := openTestStore(t, "")
	stored := []byte("ssssssssssssssssssssssssssssssss")
	meta, _ := json.Marshal(metaValue{V: 1, CreatedAt: "2026-11-01T00:00:00Z", FirstDay: "2026-11-01", KeySecret: base64.StdEncoding.EncodeToString(stored)})
	if _, err := store.inner.KVSet(context.Background(), metaKey, meta, kvstore.KVSetOptions{}); err != nil {
		t.Fatal(err)
	}
	key := "client-key-before-the-store-was-up"
	env := newTestEnv(t, store)
	env.off.Store(true)
	env.record(claudeRecord("claude-opus-5-5", key, 1, 0, 0, 1))
	env.round()
	env.off.Store(false)
	want := keyID(stored, key)
	env.round()
	d := storedDay(t, store, today)
	if _, ok := d.keys[want]; !ok || len(d.keys) != 1 {
		t.Fatalf("written with the stored secret: %+v", d.keys)
	}

	// On a new file the process's secret becomes the file's.
	fresh := openTestStore(t, "")
	env2 := newTestEnv(t, fresh)
	process := append([]byte(nil), env2.r.secret...)
	env2.record(claudeRecord("claude-opus-5-5", key, 1, 0, 0, 1))
	env2.round()
	if readMeta(t, fresh).KeySecret != base64.StdEncoding.EncodeToString(process) {
		t.Fatal("the process's secret must become the file's")
	}
}

func TestADayKeepsItsPrices(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if d := storedDay(t, store, today); d.rows[rowKey{k: "oauth", p: "claude", m: "claude-opus-5-5"}].pm != "" {
		t.Fatal("no price list yet: no pm")
	}
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}, testNow)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	d := storedDay(t, store, today)
	r := d.rows[rowKey{k: "oauth", p: "claude", m: "claude-opus-5-5"}]
	if r.pm != "anthropic/claude-opus-5.5" || r.pmm != matchNormalized || d.prices["anthropic/claude-opus-5.5"].In != 4 {
		t.Fatalf("a row first written without a price gets one later: %+v %+v", r, d.prices)
	}
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 99, Output: 99}}, testNow.Add(time.Hour))
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if d = storedDay(t, store, today); d.prices["anthropic/claude-opus-5.5"].In != 4 {
		t.Fatalf("a later price change leaves the day's prices alone: %+v", d.prices)
	}
}

func TestThePriceListCopy(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	list := map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20, CacheRead: fptr(0.2)}}
	env.prices.set(list, testNow)
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	copyBefore := store.raw(t, openRouterListKey)
	saved, at, err := decodeListCopy(copyBefore)
	if err != nil || !at.Equal(testNow) || saved["anthropic/claude-opus-5.5"].Input != 4 {
		t.Fatalf("copy: %v %v %+v", err, at, saved)
	}
	writes := store.writes.Load()
	env.prices.set(list, testNow.Add(time.Hour)) // downloaded again, the same list
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if store.writes.Load()-writes != 1 {
		t.Fatalf("the same list must not be written again (%d writes, want only the day)", store.writes.Load()-writes)
	}
	// A restart without a download reads the copy.
	restarted := newTestEnv(t, store)
	restarted.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	restarted.round()
	if a := restarted.query(t, "period=today"); a.Prices.OpenRouter.FetchedAt == nil || *a.Prices.OpenRouter.FetchedAt != formatTime(testNow) ||
		a.Models[0].Price == nil || a.Models[0].Price.InputPerMTok != 4 {
		t.Fatalf("read from the store: %+v", a.Prices.OpenRouter)
	}
	// The later fetched_at wins: an older download does not replace the copy.
	restarted.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 1, Output: 1}}, testNow.Add(-time.Hour))
	restarted.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	restarted.round()
	if saved, at, _ := decodeListCopy(store.raw(t, openRouterListKey)); saved["anthropic/claude-opus-5.5"].Input != 4 || !at.Equal(testNow) {
		t.Fatal("the later fetched_at wins")
	}
	// A newer, different list is written.
	restarted.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 5, Output: 25}}, testNow.Add(2*time.Hour))
	restarted.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	restarted.round()
	if saved, _, _ = decodeListCopy(store.raw(t, openRouterListKey)); saved["anthropic/claude-opus-5.5"].Input != 5 {
		t.Fatalf("a different newer list is written: %+v", saved)
	}
}

func TestTheZoneChanges(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	at := time.Date(2026, 11, 20, 1, 30, 0, 0, bucharest) // 2026-11-19 23:30 UTC
	rec := claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)
	rec.RequestedAt = at
	env.record(rec)
	env.round()
	other := newTestEnv(t, store)
	other.r.opts.location = time.UTC
	other.record(rec)
	other.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)) // 2026-11-20 in UTC too
	other.round()
	if d := storedDay(t, store, "2026-11-19"); d.tz != "UTC" || sumRows(d).Req != 1 {
		t.Fatalf("new records are filed by the new zone: %+v", d)
	}
	if d := storedDay(t, store, today); d.tz != "Europe/Bucharest" || d.offset != 7200 || sumRows(d).Req != 2 {
		t.Fatalf("an existing day keeps its tz: tz=%s offset=%d req=%d", d.tz, d.offset, sumRows(d).Req)
	}
}

func TestConcurrent(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}, testNow)
	var wg sync.WaitGroup
	const workers, each = 8, 200
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				env.record(claudeRecord("claude-opus-5-5", fmt.Sprintf("client-key-%02d-xxxxxxxx", w), 1, 2, 3, 4))
				if i%50 == 0 {
					env.round()
				}
			}
		}(w)
	}
	wg.Wait()
	env.round()
	if got := sumRows(storedDay(t, store, today)); got.Req != workers*each || got.Cr != 2*workers*each || got.Out != 4*workers*each {
		t.Fatalf("totals %+v", got)
	}
}

func TestStartTwice(t *testing.T) {
	r := defaultRecorder
	r.opts.store = func() (localStore, error) { return nil, nil }
	r.opts.openRouter = func() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool) { return nil, time.Time{}, true }
	r.opts.openRouterKept = r.opts.openRouter
	r.opts.now = func() time.Time { return testNow }
	r.opts.location = bucharest
	recorded := func() int64 {
		var n int64
		for _, d := range (&testEnv{r: r}).waiting() {
			n += d.requests()
		}
		return n
	}

	// Before Start a published record is not recorded.
	before := make(waiter, 16)
	coreusage.RegisterNamedPlugin("provimux-savings-test-before", before)
	coreusage.PublishRecord(context.Background(), claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	<-before
	if recorded() != 0 {
		t.Fatal("a record published before Start was recorded")
	}
	FlushNow() // a no-op before Start

	Start()
	Start()
	after := make(waiter, 16)
	coreusage.RegisterNamedPlugin("provimux-savings-test-after", after) // after Savings'
	coreusage.PublishRecord(context.Background(), claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	coreusage.PublishRecord(context.Background(), claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	<-after
	<-after
	if got := recorded(); got != 2 {
		t.Fatalf("one plugin: %d requests, want 2", got)
	}
	if r.savers.Load() != 1 {
		t.Fatalf("savers = %d", r.savers.Load())
	}
}

// waiter is a usage plugin that signals each record it receives, without ever blocking.
type waiter chan struct{}

func (w waiter) HandleUsage(context.Context, coreusage.Record) {
	select {
	case w <- struct{}{}:
	default:
	}
}
