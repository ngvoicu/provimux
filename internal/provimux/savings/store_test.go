package savings

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var bucharest = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		panic(err)
	}
	return loc
}()

// testStore wraps a real bbolt store so a test can count, block or fail its calls.
type testStore struct {
	inner *kvstore.BoltStore
	path  string

	calls   atomic.Int32
	writes  atomic.Int32
	mu      sync.Mutex
	block   chan struct{} // when set, KVSet waits on it
	entered chan struct{} // receives when a blocked KVSet starts
	failSet error
	failGet error
	gets    map[string]int
}

func openTestStore(t testing.TB, path string) *testStore {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "kv.db")
	}
	inner, err := kvstore.OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &testStore{inner: inner, path: path, gets: map[string]int{}}
	t.Cleanup(func() { _ = inner.Close() })
	return s
}

func (s *testStore) close(t testing.TB) {
	t.Helper()
	if err := s.inner.Close(); err != nil {
		t.Fatal(err)
	}
}

func (s *testStore) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.gets[key]++
	errGet := s.failGet
	s.mu.Unlock()
	if errGet != nil {
		return nil, false, errGet
	}
	return s.inner.KVGet(ctx, key)
}

func (s *testStore) KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error) {
	s.calls.Add(1)
	s.mu.Lock()
	block, entered, errSet := s.block, s.entered, s.failSet
	s.mu.Unlock()
	if block != nil {
		if entered != nil {
			entered <- struct{}{}
		}
		<-block
	}
	if errSet != nil {
		return false, errSet
	}
	s.writes.Add(1)
	return s.inner.KVSet(ctx, key, value, opts)
}

func (s *testStore) KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	s.calls.Add(1)
	s.mu.Lock()
	errSet := s.failSet
	s.mu.Unlock()
	if errSet != nil {
		return false, errSet
	}
	return s.inner.KVSetNX(ctx, key, value, ttl)
}

func (s *testStore) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	s.calls.Add(1)
	s.mu.Lock()
	for _, key := range keys {
		s.gets[key]++
	}
	errGet := s.failGet
	s.mu.Unlock()
	if errGet != nil {
		return nil, nil, errGet
	}
	return s.inner.KVMGet(ctx, keys...)
}

func (s *testStore) getCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets[key]
}

func (s *testStore) raw(t testing.TB, key string) []byte {
	t.Helper()
	data, found, err := s.inner.KVGet(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("%s: found=%v err=%v", key, found, err)
	}
	return data
}

func (s *testStore) file(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var errDiskFull = errors.New("disk full")

// testClock is a settable clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// testPrices is a settable OpenRouter list.
type testPrices struct {
	mu        sync.Mutex
	list      map[string]registry.ProvimuxOpenRouterPrice
	fetchedAt time.Time
	remote    bool
	calls     atomic.Int32
}

func (p *testPrices) get() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool) {
	p.calls.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.list, p.fetchedAt, p.remote
}

// kept is the list as the registry keeps it, read without asking for a download.
func (p *testPrices) kept() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.list, p.fetchedAt, p.remote
}

func (p *testPrices) set(list map[string]registry.ProvimuxOpenRouterPrice, at time.Time) {
	p.mu.Lock()
	p.list, p.fetchedAt = list, at
	p.mu.Unlock()
}

type testEnv struct {
	r      *recorder
	clock  *testClock
	prices *testPrices
	store  *testStore // nil: the store is off
	off    atomic.Bool
}

// newTestEnv builds a recorder on the given store (nil: none), at 2026-11-20 11:14 in Bucharest.
func newTestEnv(t testing.TB, store *testStore) *testEnv {
	t.Helper()
	env := &testEnv{
		clock:  &testClock{now: time.Date(2026, 11, 20, 11, 14, 7, 512_000_000, bucharest)},
		prices: &testPrices{remote: true},
		store:  store,
	}
	env.r = newRecorder(options{
		now:      env.clock.Now,
		location: bucharest,
		store: func() (localStore, error) {
			if env.store == nil || env.off.Load() {
				return nil, nil
			}
			return env.store, nil
		},
		openRouter:     env.prices.get,
		openRouterKept: env.prices.kept,
		interval:       time.Hour,
		flushTimeout:   200 * time.Millisecond,
		table:          &priceTable{V: 1, Updated: "2026-11-20", byPair: map[string]tableEntry{}},
	})
	return env
}

func (e *testEnv) round() { e.r.saveRound(context.Background()) }

// waiting is what memory holds for the store, per day: the deltas, the incoming ones moved in first.
func (e *testEnv) waiting() map[string]*deltaDay {
	e.r.mu.Lock()
	defer e.r.mu.Unlock()
	e.r.drainLocked()
	out := make(map[string]*deltaDay, len(e.r.deltas))
	for day, d := range e.r.deltas {
		copied := newDeltaDay()
		copied.addAll(d)
		out[day] = copied
	}
	return out
}

func (d *deltaDay) requests() int64 {
	var n int64
	for _, c := range d.rows {
		n += c.Req
	}
	return n
}

func (e *testEnv) record(rec coreusage.Record) { e.r.HandleUsage(context.Background(), rec) }

// claudeRecord is a login request to Claude with the given token counts.
func claudeRecord(model, key string, in, cacheRead, cacheWrite, out int64) coreusage.Record {
	return coreusage.Record{
		Provider: "claude", Model: model, AuthType: "oauth", APIKey: key,
		Detail: coreusage.Detail{InputTokens: in, CacheReadTokens: cacheRead, CacheCreationTokens: cacheWrite, OutputTokens: out},
	}
}

// storedDay decodes the stored value of a day.
func storedDay(t testing.TB, s *testStore, day string) *dayRows {
	t.Helper()
	d, err := decodeDay(s.raw(t, dayKey(day)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sumRows(d *dayRows) counters {
	var total counters
	for _, r := range d.rows {
		total.add(r.n)
	}
	return total
}

// captureLogs sends logrus output to w until the returned function is called.
func captureLogs(w io.Writer) func() {
	logger := log.StandardLogger()
	old := logger.Out
	logger.SetOutput(w)
	return func() { logger.SetOutput(old) }
}
