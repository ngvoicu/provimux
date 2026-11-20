// Package savings records daily usage totals and values them at list prices, for the console's
// Savings page. Design: .github/provimux/design/savings/.
package savings

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// pluginName is the usage plugin's name.
const pluginName = "provimux-savings"

// ErrStoreUnavailable: the local store is on and could not be read.
var ErrStoreUnavailable = errors.New("savings: store unavailable")

// localStore is the part of *kvstore.BoltStore Savings uses. Tests wrap a real one to block or fail.
type localStore interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error)
}

// currentLocalStore returns kvstore.CurrentKVClient() when it is a *kvstore.BoltStore; nil when the
// local store is off or the client is Home's; an error when the local store cannot be opened.
// Savings never writes to Home's store (D15).
func currentLocalStore() (localStore, error) {
	client, _, errClient := kvstore.CurrentKVClient()
	if bolt, ok := client.(*kvstore.BoltStore); ok && bolt != nil {
		if errClient != nil {
			return nil, errClient
		}
		return bolt, nil
	}
	if client != nil {
		return nil, nil // Home's client
	}
	return nil, errClient
}

// options are what tests replace: the clock, the zone, the store, the prices, the interval.
type options struct {
	now      func() time.Time
	location *time.Location
	store    func() (localStore, error) // currentLocalStore
	// openRouter is registry.ProvimuxOpenRouterPrices.
	openRouter func() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool)
	// openRouterKept is registry.ProvimuxOpenRouterPricesKept: the same, never asking for a download.
	openRouterKept func() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool)
	interval       time.Duration // 10 s
	flushTimeout   time.Duration // 2 s
	table          *priceTable
	// maxMemoryDays bounds the days kept in memory while the store is off.
	maxMemoryDays int
}

func defaultOptions() options {
	return options{
		now:            time.Now,
		location:       serverLocation(),
		store:          currentLocalStore,
		openRouter:     registry.ProvimuxOpenRouterPrices,
		openRouterKept: registry.ProvimuxOpenRouterPricesKept,
		interval:       10 * time.Second,
		flushTimeout:   2 * time.Second,
		table:          defaultTable,
		maxMemoryDays:  400,
	}
}

// serverLocation is the server's zone: $TZ when it is set and time.LoadLocation accepts it, so the
// answer can name it; else time.Local, named Local or UTC (01-recording.md section 6).
func serverLocation() *time.Location {
	if name, ok := os.LookupEnv("TZ"); ok && name != "" {
		if loc, errLoad := time.LoadLocation(name); errLoad == nil {
			return loc
		}
	}
	return time.Local
}

// dayState is one day in memory (01-recording.md section 5, "Absolute totals, written whole").
type dayState struct {
	rows           *dayRows // the stored rows, keyed by key id
	loaded         bool     // rows were read from the store in this process
	pending        map[pendingKey]*counters
	pendingRegular int  // pending combinations that are not folded
	dirty          bool // rows differ from the store
	version        uint64
	broken         bool // the stored value does not decode: never overwritten, skipped by reads
}

// recorder holds the totals in memory and writes them to the local store.
type recorder struct {
	opts options

	saveMu sync.Mutex // one round at a time; held across store calls, never by HandleUsage

	mu   sync.Mutex // HandleUsage's one mutex; never held across a store call
	days map[string]*dayState
	// secret makes key ids. The process's own until the store's meta is known (metaKnown).
	secret    []byte
	metaKnown bool
	meta      metaValue
	metaOK    bool // meta decoded; false keeps first_day from being rewritten
	// The OpenRouter list: as last downloaded (asked for by the saver) and the saved copy.
	downloaded   map[string]registry.ProvimuxOpenRouterPrice
	downloadedAt time.Time
	remote       bool
	saved        map[string]registry.ProvimuxOpenRouterPrice
	savedAt      time.Time
	savedKnown   bool
	// What the answer says about the record itself.
	wroteOnce   bool
	lastWriteOK bool
	lastSavedAt time.Time
	storeWasOn  bool
	logged      map[string]bool // keys whose value did not decode, logged once

	savers atomic.Int32
}

func newRecorder(opts options) *recorder {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.store == nil {
		opts.store = func() (localStore, error) { return nil, nil }
	}
	if opts.openRouter == nil {
		opts.openRouter = func() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool) { return nil, time.Time{}, false }
	}
	if opts.openRouterKept == nil {
		opts.openRouterKept = func() (map[string]registry.ProvimuxOpenRouterPrice, time.Time, bool) { return nil, time.Time{}, false }
	}
	if opts.table == nil {
		opts.table = defaultTable
	}
	if opts.interval <= 0 {
		opts.interval = 10 * time.Second
	}
	if opts.flushTimeout <= 0 {
		opts.flushTimeout = 2 * time.Second
	}
	if opts.maxMemoryDays <= 0 {
		opts.maxMemoryDays = 400
	}
	secret := make([]byte, 32)
	if _, errRand := rand.Read(secret); errRand != nil {
		log.Errorf("savings: no random key secret: %v", errRand)
	}
	return &recorder{opts: opts, days: map[string]*dayState{}, secret: secret, logged: map[string]bool{}}
}

func (r *recorder) location() *time.Location {
	if r.opts.location != nil {
		return r.opts.location
	}
	return time.Local
}

// dayLocked returns the day's state, made with the zone of now when it is new.
func (r *recorder) dayLocked(day string) *dayState {
	if st, ok := r.days[day]; ok {
		return st
	}
	now := r.opts.now().In(r.location())
	_, offset := now.Zone()
	st := &dayState{rows: newDayRows(r.location().String(), offset), pending: map[pendingKey]*counters{}}
	r.days[day] = st
	return st
}

var (
	startOnce       sync.Once
	started         atomic.Bool
	defaultRecorder = newRecorder(defaultOptions())
)

// Start registers the usage plugin "provimux-savings" and starts the saver. Idempotent.
// Called when the management routes are registered; before it, nothing is recorded.
func Start() {
	startOnce.Do(func() {
		r := defaultRecorder
		// OpenRouter's list is asked for by the saver's rounds that have a day to write, not here:
		// a server that records nothing never contacts openrouter.ai.
		coreusage.RegisterNamedPlugin(pluginName, r)
		started.Store(true)
		r.savers.Add(1)
		go r.run(context.Background())
	})
}

// FlushNow writes every changed day to the local store and returns when that is done, or after
// 2 seconds. Safe to call at any time and more than once; a no-op before Start.
func FlushNow() {
	if !started.Load() {
		return
	}
	defaultRecorder.flush(context.Background())
}

func (r *recorder) flush(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.safeRound(ctx)
	}()
	timer := time.NewTimer(r.opts.flushTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Warn("savings: flush did not finish in time")
	}
}

// run is the saver: a round every interval, for the life of the process.
func (r *recorder) run(ctx context.Context) {
	ticker := time.NewTicker(r.opts.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.safeRound(ctx)
		}
	}
}

func (r *recorder) safeRound(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("savings: saver panic recovered: %v", recovered)
		}
	}()
	r.saveRound(ctx)
}

// RecordForTest hands a record to the started recorder as the usage manager would. For tests of
// other packages whose default usage manager an earlier test has stopped. A no-op before Start.
func RecordForTest(ctx context.Context, record coreusage.Record) {
	if started.Load() {
		defaultRecorder.HandleUsage(ctx, record)
	}
}
