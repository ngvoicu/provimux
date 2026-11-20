package savings

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
)

// metaValue is provimux:savings:v1:meta.
type metaValue struct {
	V         int    `json:"v"`
	CreatedAt string `json:"created_at"`
	FirstDay  string `json:"first_day"`
	KeySecret string `json:"key_secret"`
}

func decodeMeta(data []byte) (metaValue, []byte, error) {
	var meta metaValue
	if errDecode := json.Unmarshal(data, &meta); errDecode != nil {
		return metaValue{}, nil, errDecode
	}
	secret, errSecret := base64.StdEncoding.DecodeString(meta.KeySecret)
	if errSecret != nil || len(secret) < 16 {
		return metaValue{}, nil, fmt.Errorf("meta has no usable key_secret")
	}
	return meta, secret, nil
}

// listCopy is provimux:savings:v1:prices:openrouter, the last downloaded OpenRouter list.
type listCopy struct {
	V         int                  `json:"v"`
	FetchedAt string               `json:"fetched_at"`
	Models    map[string]listPrice `json:"models"`
}

type listPrice struct {
	In   float64  `json:"in"`
	Out  float64  `json:"out"`
	Cr   *float64 `json:"cr,omitempty"`
	Cw   *float64 `json:"cw,omitempty"`
	Cw1h *float64 `json:"cw1h,omitempty"`
}

func encodeListCopy(list map[string]registry.ProvimuxOpenRouterPrice, fetchedAt time.Time) ([]byte, error) {
	value := listCopy{V: 1, FetchedAt: formatTime(fetchedAt), Models: make(map[string]listPrice, len(list))}
	for id, p := range list {
		value.Models[id] = listPrice{In: p.Input, Out: p.Output, Cr: p.CacheRead, Cw: p.CacheWrite, Cw1h: p.CacheWrite1h}
	}
	return json.Marshal(value)
}

func decodeListCopy(data []byte) (map[string]registry.ProvimuxOpenRouterPrice, time.Time, error) {
	var value listCopy
	if errDecode := json.Unmarshal(data, &value); errDecode != nil {
		return nil, time.Time{}, errDecode
	}
	fetchedAt, errTime := time.Parse(time.RFC3339Nano, value.FetchedAt)
	if errTime != nil {
		return nil, time.Time{}, errTime
	}
	list := make(map[string]registry.ProvimuxOpenRouterPrice, len(value.Models))
	for id, p := range value.Models {
		list[id] = registry.ProvimuxOpenRouterPrice{Input: p.In, Output: p.Out, CacheRead: p.Cr, CacheWrite: p.Cw, CacheWrite1h: p.Cw1h}
	}
	return list, fetchedAt, nil
}

// formatTime is the answer's and the store's time format: RFC 3339 in UTC, as the other Provimux
// endpoints write it (provimuxTime).
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// refreshPrices asks for OpenRouter's list (the catalog's own cadence and policy) and keeps it.
func (r *recorder) refreshPrices() {
	list, fetchedAt, remote := r.opts.openRouter()
	r.mu.Lock()
	r.remote = remote
	if list != nil {
		r.downloaded, r.downloadedAt = list, fetchedAt
	}
	r.mu.Unlock()
}

// refreshKept takes the list as the catalog keeps it now, without asking for a download, so a list
// that arrived after the last round is used and saved.
func (r *recorder) refreshKept() {
	list, fetchedAt, remote := r.opts.openRouterKept()
	r.mu.Lock()
	r.remote = remote
	if list != nil && (r.downloaded == nil || fetchedAt.After(r.downloadedAt)) {
		r.downloaded, r.downloadedAt = list, fetchedAt
	}
	r.mu.Unlock()
}

// bindLocked binds the in-memory state to the store it describes. On a switch to another store
// (server.kv-store.path changed), what memory held as stored belongs to the old store: it is dropped,
// and each day is read from the new store before its next write. Records not yet written stay in
// pending and go to the new store. Clean stops and config reloads flush to the old store first
// (provimuxFlushSavings in sdk/cliproxy). A store that is off keeps what memory holds.
func (r *recorder) bindLocked(store localStore) {
	if store == nil || store == r.bound {
		return
	}
	if r.bound != nil {
		now := r.opts.now().In(r.location())
		_, offset := now.Zone()
		for day, st := range r.days {
			if st.dirty {
				log.Warnf("savings: %s was not written before the local store changed", dayKey(day))
			}
			st.rows = newDayRows(r.location().String(), offset)
			st.loaded, st.dirty, st.broken = false, false, false
			st.version++
		}
		r.metaKnown, r.meta, r.metaOK = false, metaValue{}, false
		r.saved, r.savedAt, r.savedKnown = nil, time.Time{}, false
		r.wroteOnce, r.lastWriteOK, r.lastSavedAt = false, false, time.Time{}
		r.generation++
	}
	r.bound = store
}

// bookLocked is the prices in use: the list with the later fetched_at of the downloaded and the saved
// copy, and the table.
func (r *recorder) bookLocked() *priceBook {
	book := &priceBook{remote: r.remote, table: r.opts.table}
	switch {
	case r.downloaded != nil && (r.saved == nil || !r.downloadedAt.Before(r.savedAt)):
		book.openRouter, book.fetchedAt = r.downloaded, r.downloadedAt
	case r.saved != nil:
		book.openRouter, book.fetchedAt = r.saved, r.savedAt
	}
	return book
}

func (r *recorder) logOnce(key, format string, args ...any) {
	r.mu.Lock()
	seen := r.logged[key]
	r.logged[key] = true
	r.mu.Unlock()
	if !seen {
		log.Errorf(format, args...)
	}
}

// saveRound is one round of the saver: 01-recording.md section 5, steps 1 to 3, for every day
// with pending or dirty rows; then the price list copy.
func (r *recorder) saveRound(ctx context.Context) {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()

	r.mu.Lock()
	var work []string
	for day, st := range r.days {
		if !st.broken && (len(st.pending) > 0 || st.dirty) {
			work = append(work, day)
		}
	}
	r.mu.Unlock()
	r.refreshKept()
	if len(work) == 0 {
		r.saveArrivedList(ctx)
		r.evict()
		return
	}
	slices.Sort(work)
	r.refreshPrices()

	store, errStore := r.opts.store()
	if errStore != nil || store == nil {
		if errStore != nil {
			log.Debugf("savings: local store unavailable: %v", errStore)
		}
		r.mu.Lock()
		r.storeWasOn = false
		r.mu.Unlock()
		r.evict()
		return
	}
	r.mu.Lock()
	r.storeWasOn = true
	r.bindLocked(store)
	r.mu.Unlock()
	if !r.ensureMeta(ctx, store, work[0]) {
		return
	}
	for _, day := range work {
		r.saveDay(ctx, store, day)
	}
	r.moveFirstDay(ctx, store, work[0])
	r.saveListCopy(ctx, store)
	r.evict()
}

// saveArrivedList saves, in a round with no day to write, a downloaded list that is newer than the
// saved copy: the first download often completes after the round that asked for it. No store call
// unless there is such a list.
func (r *recorder) saveArrivedList(ctx context.Context) {
	r.mu.Lock()
	pending := r.downloaded != nil && (!r.savedKnown || r.saved == nil || r.downloadedAt.After(r.savedAt))
	r.mu.Unlock()
	if !pending {
		return
	}
	store, errStore := r.opts.store()
	if errStore != nil || store == nil {
		return
	}
	r.mu.Lock()
	r.bindLocked(store)
	known := r.savedKnown
	r.mu.Unlock()
	if !known {
		data, found, errGet := store.KVGet(ctx, openRouterListKey)
		if errGet != nil {
			r.failed(errGet)
			return
		}
		var saved map[string]registry.ProvimuxOpenRouterPrice
		var savedAt time.Time
		if found {
			var errList error
			if saved, savedAt, errList = decodeListCopy(data); errList != nil {
				saved = nil
			}
		}
		r.mu.Lock()
		if r.bound == store {
			if saved != nil {
				r.saved, r.savedAt = saved, savedAt
			}
			r.savedKnown = true
		}
		r.mu.Unlock()
	}
	r.saveListCopy(ctx, store)
}

// ensureMeta reads meta (and the saved list copy) the first time the saver reaches a store; on a new
// store it writes the process's secret as meta, then takes the value that won.
func (r *recorder) ensureMeta(ctx context.Context, store localStore, firstDay string) bool {
	r.mu.Lock()
	known := r.metaKnown
	r.mu.Unlock()
	if known {
		return true
	}
	values, found, errGet := store.KVMGet(ctx, metaKey, openRouterListKey)
	if errGet != nil || len(values) != 2 || len(found) != 2 {
		r.failed(errGet)
		return false
	}
	metaData, metaFound := values[0], found[0]
	if !metaFound {
		r.mu.Lock()
		meta := metaValue{V: 1, CreatedAt: formatTime(r.opts.now()), FirstDay: firstDay, KeySecret: base64.StdEncoding.EncodeToString(r.secret)}
		r.mu.Unlock()
		data, errEncode := json.Marshal(meta)
		if errEncode != nil {
			return false
		}
		if _, errSet := store.KVSetNX(ctx, metaKey, data, 0); errSet != nil {
			r.failed(errSet)
			return false
		}
		var errRead error
		metaData, metaFound, errRead = store.KVGet(ctx, metaKey)
		if errRead != nil || !metaFound {
			r.failed(errRead)
			return false
		}
	}
	meta, secret, errMeta := decodeMeta(metaData)
	var saved map[string]registry.ProvimuxOpenRouterPrice
	var savedAt time.Time
	if found[1] {
		var errList error
		if saved, savedAt, errList = decodeListCopy(values[1]); errList != nil {
			r.logOnce(openRouterListKey, "savings: %s does not decode and is ignored", openRouterListKey)
			saved = nil
		}
	}
	if errMeta != nil {
		r.logOnce(metaKey, "savings: %s does not decode; key ids use this process's secret", metaKey)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bound != store {
		return false
	}
	r.metaKnown = true
	if errMeta == nil {
		r.meta, r.secret, r.metaOK = meta, secret, true
	}
	if saved != nil {
		r.saved, r.savedAt = saved, savedAt
	}
	r.savedKnown = true
	return true
}

// failed records that a store call failed: recording.persistent is false until a write succeeds.
func (r *recorder) failed(err error) {
	if err != nil {
		log.Debugf("savings: local store call failed: %v", err)
	}
	r.mu.Lock()
	r.wroteOnce = true
	r.lastWriteOK = false
	r.mu.Unlock()
}

// saveDay is steps 1 to 3 for one day.
func (r *recorder) saveDay(ctx context.Context, store localStore, day string) {
	r.mu.Lock()
	st := r.days[day]
	if st == nil || st.broken {
		r.mu.Unlock()
		return
	}
	needLoad := !st.loaded
	r.mu.Unlock()

	// 1. Read the day once per process, outside the mutex.
	if needLoad {
		data, found, errGet := store.KVGet(ctx, dayKey(day))
		if errGet != nil {
			r.failed(errGet)
			return
		}
		var loaded *dayRows
		if found {
			var errDecode error
			if loaded, errDecode = decodeDay(data); errDecode != nil {
				r.mu.Lock()
				st.broken = true
				r.mu.Unlock()
				r.logOnce(dayKey(day), "savings: %s does not decode; it is kept as it is and skipped", dayKey(day))
				return
			}
		}
		r.mu.Lock()
		if r.bound != store || r.days[day] != st {
			r.mu.Unlock()
			return
		}
		if loaded != nil {
			st.rows = loaded
		}
		st.loaded = true
		r.mu.Unlock()
	}

	// 2. Under the mutex: pending into rows by key id, prices for rows without one, a copy.
	r.mu.Lock()
	if r.bound != store || !st.loaded {
		r.mu.Unlock()
		return
	}
	st.rows.mergePending(st.pending, r.secret)
	st.pending = map[pendingKey]*counters{}
	st.pendingRegular = 0
	st.rows.assignPrices(r.bookLocked())
	st.dirty = true
	st.version++
	version := st.version
	snapshot := st.rows.clone()
	r.mu.Unlock()

	// 3. Outside the mutex: encode and write the whole value.
	data, errEncode := snapshot.encode(day)
	if errEncode != nil {
		log.Errorf("savings: encode %s: %v", dayKey(day), errEncode)
		return
	}
	if _, errSet := store.KVSet(ctx, dayKey(day), data, kvstore.KVSetOptions{}); errSet != nil {
		r.failed(errSet)
		return
	}
	r.mu.Lock()
	if st.version == version {
		st.dirty = false
	}
	r.wroteOnce, r.lastWriteOK, r.lastSavedAt = true, true, r.opts.now()
	r.generation++ // a query that read around this write reads again
	r.mu.Unlock()
}

// moveFirstDay writes meta again when a day before its first_day was recorded.
func (r *recorder) moveFirstDay(ctx context.Context, store localStore, earliest string) {
	r.mu.Lock()
	if !r.metaOK || r.meta.FirstDay == "" || earliest >= r.meta.FirstDay {
		r.mu.Unlock()
		return
	}
	meta := r.meta
	meta.FirstDay = earliest
	r.mu.Unlock()
	data, errEncode := json.Marshal(meta)
	if errEncode != nil {
		return
	}
	if _, errSet := store.KVSet(ctx, metaKey, data, kvstore.KVSetOptions{}); errSet != nil {
		r.failed(errSet)
		return
	}
	r.mu.Lock()
	r.meta = meta
	r.mu.Unlock()
}

// saveListCopy writes the downloaded list when it is newer than the saved copy and differs from it.
func (r *recorder) saveListCopy(ctx context.Context, store localStore) {
	r.mu.Lock()
	list, fetchedAt := r.downloaded, r.downloadedAt
	newer := list != nil && (r.saved == nil || fetchedAt.After(r.savedAt))
	same := newer && r.saved != nil && reflect.DeepEqual(list, r.saved)
	r.mu.Unlock()
	if !newer || same {
		return
	}
	data, errEncode := encodeListCopy(list, fetchedAt)
	if errEncode != nil {
		return
	}
	if _, errSet := store.KVSet(ctx, openRouterListKey, data, kvstore.KVSetOptions{}); errSet != nil {
		r.failed(errSet)
		return
	}
	r.mu.Lock()
	if r.bound == store {
		r.saved, r.savedAt, r.savedKnown = maps.Clone(list), fetchedAt, true
	}
	r.mu.Unlock()
}

// evict drops from memory the clean days more than 2 days old while the store is on, and the oldest
// days beyond the memory bound while it is off.
func (r *recorder) evict() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storeWasOn {
		today := r.opts.now().In(r.location())
		limit := time.Date(today.Year(), today.Month(), today.Day()-2, 12, 0, 0, 0, time.UTC).Format(time.DateOnly)
		for day, st := range r.days {
			if day < limit && len(st.pending) == 0 && !st.dirty {
				delete(r.days, day)
			}
		}
		return
	}
	if len(r.days) <= r.opts.maxMemoryDays {
		return
	}
	days := slices.Sorted(maps.Keys(r.days))
	for _, day := range days[:len(days)-r.opts.maxMemoryDays] {
		delete(r.days, day)
	}
}
