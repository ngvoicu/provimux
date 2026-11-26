package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: what each account's answers for a name said of themselves (.github/provimux/design/answered-by.md 4.4,
// step W6b). For every statement that names an account, the conductor notes here the account's marker, the name the
// client asked for and the answer's entry ("" for none); both account lists read the items back as `answered`.
//
//   - Per marker and name, at most four distinct outcomes (an entry, or none), newest first, each with the time of the
//     last answer that had it. A fifth drops the oldest. An item older than 90 days is gone.
//   - Under the name the client asked for where the account registers exactly that id, else that id without its
//     thinking suffix where the account registers that; else nothing is noted: there is no row it could stand on.
//     Names are compared without regard to case, as the registry compares them.
//   - Keyed by the marker: another account's login in the same file, or a lost marker key, gives another marker and
//     starts with nothing.
//   - Noting is a few map operations under a short lock, on the answer's path; nothing there reads or writes the
//     store. A writer of its own keeps each marker's items in the gateway's local key-value store, beside the
//     marker's key, when an item is added and when an item's day changes, and everything when the store is about to
//     close or change (at most once a day per item while it runs). After a crash an item's time can be hours old; its
//     day is right. Where no local store is in use or it fails, the record lives for this run, and the log says so
//     once.
//   - With models.answered-by off (and so in Home mode, where it stays off) nothing is noted, read or written.
//
// It holds markers, names, entries and times: nothing of a request's content, no client key, and not who asked.

// provimuxAnsweredKept is how long an item is kept after the last answer that had it: providers.dev's threshold for
// "old".
const provimuxAnsweredKept = 90 * 24 * time.Hour

// provimuxAnsweredMaxItems is how many distinct outcomes a marker and name keep.
const provimuxAnsweredMaxItems = 4

// provimuxAnsweredStorePrefix is the local store's key of a marker's items: the prefix and the marker.
const provimuxAnsweredStorePrefix = "provimux:answered:v1:record:"

// provimuxAnsweredStore is the part of the local store the record needs; *kvstore.BoltStore is one.
type provimuxAnsweredStore interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts kvstore.KVSetOptions) (bool, error)
}

// provimuxAnsweredItem is one outcome: the entry ("" = none) and the time of the last answer that had it.
type provimuxAnsweredItem struct {
	entry string
	at    time.Time
}

// provimuxAnsweredStored is an item as the store holds it.
type provimuxAnsweredStored struct {
	Entry *string `json:"entry"`
	At    string  `json:"at"`
}

// provimuxAnsweredAccount is one marker's items, by name (lower case).
type provimuxAnsweredAccount struct {
	mu     sync.Mutex
	names  map[string][]provimuxAnsweredItem
	loaded bool // the store's items were merged in
	dirty  bool // something the store must hold changed: an item added, an item's day changed
	unsent bool // anything changed since the last write (times within a day included)
}

// provimuxAnsweredBook is the record.
type provimuxAnsweredBook struct {
	now   func() time.Time
	store func() (provimuxAnsweredStore, error)

	mu       sync.RWMutex
	accounts map[string]*provimuxAnsweredAccount

	wake       chan struct{}
	writerOnce sync.Once
	writing    sync.Mutex // one write pass at a time
	logged     atomic.Bool
}

func newProvimuxAnsweredBook(now func() time.Time, store func() (provimuxAnsweredStore, error)) *provimuxAnsweredBook {
	return &provimuxAnsweredBook{now: now, store: store, accounts: map[string]*provimuxAnsweredAccount{}, wake: make(chan struct{}, 1)}
}

// provimuxLocalAnsweredStore is the gateway's local store when one is in use.
func provimuxLocalAnsweredStore() (provimuxAnsweredStore, error) {
	client, enabled, errClient := kvstore.CurrentKVClient()
	if errClient != nil {
		return nil, fmt.Errorf("the local key-value store could not be opened (another gateway may hold its file): %w", errClient)
	}
	if bolt, ok := client.(*kvstore.BoltStore); ok && bolt != nil {
		return bolt, nil
	}
	if !enabled {
		return nil, errors.New("the local key-value store is disabled or not in use")
	}
	return nil, errors.New("the key-value store in use is not local")
}

// provimuxAnswered is the gateway's record.
var provimuxAnswered = newProvimuxAnsweredBook(time.Now, provimuxLocalAnsweredStore)

func init() {
	coreauth.SetProvimuxAnswered(provimuxAnsweredNote, func(marker, model string) []coreauth.ProvimuxAnsweredItem {
		return provimuxAnswered.read(marker, model)
	})
}

// provimuxAnsweredNote is the conductor's call: the name the row stands under, then the book.
func provimuxAnsweredNote(marker string, auth *coreauth.Auth, requestedModel, entry string) {
	if auth == nil || !logging.ProvimuxAnsweredBy() {
		return
	}
	if name := provimuxAnsweredName(auth.ID, requestedModel); name != "" {
		provimuxAnswered.note(marker, name, entry)
	}
}

// provimuxAnsweredName is the id of the row an answer for requestedModel stands on: requestedModel where the account
// registers it, else requestedModel without its thinking suffix where it registers that; "" else.
func provimuxAnsweredName(authID, requestedModel string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	models := registry.GetGlobalRegistry()
	if models.ClientSupportsModel(authID, requestedModel) {
		return requestedModel
	}
	if parsed := thinking.ParseSuffix(requestedModel); parsed.HasSuffix {
		if base := strings.TrimSpace(parsed.ModelName); base != "" && models.ClientSupportsModel(authID, base) {
			return base
		}
	}
	return ""
}

// account returns marker's items, made empty when there are none yet.
func (b *provimuxAnsweredBook) account(marker string) *provimuxAnsweredAccount {
	b.mu.RLock()
	account := b.accounts[marker]
	b.mu.RUnlock()
	if account != nil {
		return account
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if account = b.accounts[marker]; account == nil {
		account = &provimuxAnsweredAccount{names: map[string][]provimuxAnsweredItem{}}
		b.accounts[marker] = account
	}
	return account
}

// note records one answer's outcome. It touches memory only and wakes the writer when the store must learn of it.
func (b *provimuxAnsweredBook) note(marker, name, entry string) {
	if marker == "" || name == "" {
		return
	}
	now := b.now().UTC()
	key := strings.ToLower(name)
	account := b.account(marker)
	account.mu.Lock()
	items, changedDay := provimuxAnsweredAdd(account.names[key], provimuxAnsweredItem{entry: entry, at: now}, now)
	account.names[key] = items
	account.unsent = true
	if changedDay {
		account.dirty = true
	}
	account.mu.Unlock()
	if changedDay {
		b.startWriter()
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

// provimuxAnsweredAdd puts item first in items: an item of the same outcome moves up with the new time, a new outcome
// is added and the oldest of five goes, items older than the kept time go. changedDay is true when the store must
// hold the result: an item added, one dropped, or an item's day changed.
func provimuxAnsweredAdd(items []provimuxAnsweredItem, item provimuxAnsweredItem, now time.Time) (out []provimuxAnsweredItem, changedDay bool) {
	out = make([]provimuxAnsweredItem, 0, provimuxAnsweredMaxItems)
	out = append(out, item)
	found := false
	for _, old := range items {
		if old.entry == item.entry {
			found = true
			changedDay = changedDay || provimuxAnsweredDay(old.at) != provimuxAnsweredDay(item.at)
			continue
		}
		if now.Sub(old.at) > provimuxAnsweredKept {
			changedDay = true
			continue
		}
		if len(out) == provimuxAnsweredMaxItems {
			changedDay = true
			continue
		}
		out = append(out, old)
	}
	return out, changedDay || !found
}

// provimuxAnsweredDay is the day, in UTC, of t.
func provimuxAnsweredDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// provimuxAnsweredMerge joins two lists of items of one name: per outcome the later time, newest first, at most four,
// none older than the kept time.
func provimuxAnsweredMerge(a, b []provimuxAnsweredItem, now time.Time) []provimuxAnsweredItem {
	latest := map[string]time.Time{}
	for _, item := range append(append([]provimuxAnsweredItem(nil), a...), b...) {
		if at, ok := latest[item.entry]; !ok || item.at.After(at) {
			latest[item.entry] = item.at
		}
	}
	out := make([]provimuxAnsweredItem, 0, len(latest))
	for entry, at := range latest {
		if now.Sub(at) <= provimuxAnsweredKept {
			out = append(out, provimuxAnsweredItem{entry: entry, at: at})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.After(out[j].at)
		}
		return out[i].entry < out[j].entry
	})
	if len(out) > provimuxAnsweredMaxItems {
		out = out[:provimuxAnsweredMaxItems]
	}
	return out
}

// read returns marker's items under model, newest first, none older than the kept time. A marker the store has not
// been read for is read first (a local read, on a list's path, never an answer's).
func (b *provimuxAnsweredBook) read(marker, model string) []coreauth.ProvimuxAnsweredItem {
	if marker == "" || model == "" {
		return nil
	}
	account := b.account(marker)
	b.load(marker, account)
	now := b.now().UTC()
	account.mu.Lock()
	items := account.names[strings.ToLower(model)]
	out := make([]coreauth.ProvimuxAnsweredItem, 0, len(items))
	for _, item := range items {
		if now.Sub(item.at) <= provimuxAnsweredKept {
			out = append(out, coreauth.ProvimuxAnsweredItem{Entry: item.entry, At: item.at})
		}
	}
	account.mu.Unlock()
	return out
}

// load merges the store's items of marker into account once. The store is read without the account's lock held, so
// a note never waits on it.
func (b *provimuxAnsweredBook) load(marker string, account *provimuxAnsweredAccount) {
	account.mu.Lock()
	loaded := account.loaded
	account.mu.Unlock()
	if loaded {
		return
	}
	store, errStore := b.store()
	if errStore != nil || store == nil {
		b.logNotKept(errStore)
		account.mu.Lock()
		account.loaded = true
		account.mu.Unlock()
		return
	}
	raw, found, errGet := store.KVGet(context.Background(), provimuxAnsweredStorePrefix+marker)
	if errGet != nil {
		b.logNotKept(fmt.Errorf("the local key-value store could not be read: %w", errGet))
		return // read again next time
	}
	stored := map[string][]provimuxAnsweredItem{}
	if found {
		var decoded map[string][]provimuxAnsweredStored
		if errDecode := json.Unmarshal(raw, &decoded); errDecode == nil {
			for name, items := range decoded {
				for _, item := range items {
					at, errAt := time.Parse(time.RFC3339Nano, item.At)
					if errAt != nil {
						continue
					}
					entry := ""
					if item.Entry != nil {
						entry = *item.Entry
					}
					stored[strings.ToLower(name)] = append(stored[strings.ToLower(name)], provimuxAnsweredItem{entry: entry, at: at.UTC()})
				}
			}
		}
	}
	now := b.now().UTC()
	account.mu.Lock()
	if !account.loaded {
		for name, items := range stored {
			account.names[name] = provimuxAnsweredMerge(account.names[name], items, now)
		}
		account.loaded = true
	}
	account.mu.Unlock()
}

// startWriter starts the writer: it writes what changed each time it is woken.
func (b *provimuxAnsweredBook) startWriter() {
	b.writerOnce.Do(func() {
		go func() {
			for range b.wake {
				b.write(false)
			}
		}()
	})
}

// flush writes every marker whose items changed since the last write: before the store closes or changes.
func (b *provimuxAnsweredBook) flush() { b.write(true) }

// write keeps in the store each marker whose items it must hold (all that changed, with all).
func (b *provimuxAnsweredBook) write(all bool) {
	b.writing.Lock()
	defer b.writing.Unlock()
	b.mu.RLock()
	markers := make([]string, 0, len(b.accounts))
	for marker, account := range b.accounts {
		account.mu.Lock()
		if account.dirty || (all && account.unsent) {
			markers = append(markers, marker)
		}
		account.mu.Unlock()
	}
	b.mu.RUnlock()
	if len(markers) == 0 {
		return
	}
	store, errStore := b.store()
	if errStore != nil || store == nil {
		b.logNotKept(errStore)
		return
	}
	for _, marker := range markers {
		account := b.account(marker)
		b.load(marker, account)
		now := b.now().UTC()
		account.mu.Lock()
		if !account.loaded {
			// The store's items could not be read: writing now would put this run's in their place. Next time.
			account.mu.Unlock()
			continue
		}
		encoded := map[string][]provimuxAnsweredStored{}
		for name, items := range account.names {
			for _, item := range provimuxAnsweredMerge(items, nil, now) {
				stored := provimuxAnsweredStored{At: item.at.UTC().Format(time.RFC3339Nano)}
				if item.entry != "" {
					entry := item.entry
					stored.Entry = &entry
				}
				encoded[name] = append(encoded[name], stored)
			}
		}
		account.dirty, account.unsent = false, false
		account.mu.Unlock()
		raw, errEncode := json.Marshal(encoded)
		if errEncode != nil {
			continue
		}
		if _, errSet := store.KVSet(context.Background(), provimuxAnsweredStorePrefix+marker, raw, kvstore.KVSetOptions{EX: provimuxAnsweredKept}); errSet != nil {
			account.mu.Lock()
			account.dirty = true
			account.mu.Unlock()
			b.logNotKept(fmt.Errorf("the local key-value store could not be written: %w", errSet))
		}
	}
}

// logNotKept says once why the record is not kept beyond this run.
func (b *provimuxAnsweredBook) logNotKept(why error) {
	if why == nil {
		why = errors.New("no local key-value store")
	}
	if b.logged.CompareAndSwap(false, true) {
		log.Warnf("answered-by: what each account last answered as is not kept: %v; it holds until this process ends", why)
	}
}

// provimuxFlushAnswered writes the record before the local store closes or changes.
func provimuxFlushAnswered() { provimuxAnswered.flush() }
