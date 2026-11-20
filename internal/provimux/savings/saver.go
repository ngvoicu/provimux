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
// Under mu.
func (r *recorder) refreshPrices() {
	list, fetchedAt, remote := r.opts.openRouter()
	r.remote = remote
	if list != nil {
		r.downloaded, r.downloadedAt = list, fetchedAt
	}
}

// refreshKept takes the list as the catalog keeps it now, without asking for a download, so a list
// that arrived after the last round is used and saved. Under mu.
func (r *recorder) refreshKept() {
	list, fetchedAt, remote := r.opts.openRouterKept()
	r.remote = remote
	if list != nil && (r.downloaded == nil || fetchedAt.After(r.downloadedAt)) {
		r.downloaded, r.downloadedAt = list, fetchedAt
	}
}

// bookOf is the prices in use: the list with the later fetched_at of the downloaded one and the copy
// read from the store (nil when there is none), and the table.
func bookOf(downloaded map[string]registry.ProvimuxOpenRouterPrice, downloadedAt time.Time, remote bool,
	saved map[string]registry.ProvimuxOpenRouterPrice, savedAt time.Time, table *priceTable) *priceBook {
	book := &priceBook{remote: remote, table: table}
	switch {
	case downloaded != nil && (saved == nil || !downloadedAt.Before(savedAt)):
		book.openRouter, book.fetchedAt = downloaded, downloadedAt
	case saved != nil:
		book.openRouter, book.fetchedAt = saved, savedAt
	}
	return book
}

func (r *recorder) logOnce(key, format string, args ...any) {
	if !r.logged[key] {
		r.logged[key] = true
		log.Errorf(format, args...)
	}
}

// failed records that a store call failed: recording.persistent is false until a write succeeds.
// The error is logged once per streak of failures. Under mu.
func (r *recorder) failed(err error) {
	if !r.failing {
		r.failing = true
		log.Errorf("savings: local store call failed; what was recorded stays in memory and is retried every round: %v", err)
	}
	r.wroteOnce, r.lastWriteOK = true, false
}

// succeeded ends a failure streak: every day of a round was written. Under mu.
func (r *recorder) succeeded() {
	if r.failing {
		r.failing = false
		log.Info("savings: local store writes succeed again")
	}
	r.wroteOnce, r.lastWriteOK = true, true
}

// drainLocked moves the incoming deltas into r.deltas, then drops the oldest days beyond
// maxMemoryDays (the store off or failing for a long time). Under mu.
func (r *recorder) drainLocked() {
	var incoming map[string]*deltaDay
	r.inMu.Lock()
	if len(r.incoming) > 0 {
		incoming, r.incoming = r.incoming, map[string]*deltaDay{}
	}
	r.inMu.Unlock()
	for day, d := range incoming {
		if mine := r.deltas[day]; mine != nil {
			mine.addAll(d)
		} else {
			r.deltas[day] = d
		}
	}
	if len(r.deltas) <= r.opts.maxMemoryDays {
		return
	}
	days := slices.Sorted(maps.Keys(r.deltas))
	for _, day := range days[:len(days)-r.opts.maxMemoryDays] {
		delete(r.deltas, day)
	}
	if !r.dropped {
		r.dropped = true
		log.Warnf("savings: more than %d days are waiting for the local store; the oldest were dropped", r.opts.maxMemoryDays)
	}
}

// storeState is what a round or a query read of a store's meta and list copy.
type storeState struct {
	secret    []byte    // the store's key secret; the process's when meta does not decode
	meta      metaValue // valid when metaOK
	metaOK    bool
	saved     map[string]registry.ProvimuxOpenRouterPrice // the list copy; nil when none
	savedAt   time.Time
	savedSeen bool // the store holds a list copy (decoded or not)
}

// readState reads meta and the list copy in one call. With create, a store without meta gets one:
// the process's secret and firstDay (KVSetNX, then the value that won). Under mu.
func (r *recorder) readState(ctx context.Context, store localStore, create bool, firstDay string) (storeState, error) {
	values, found, errGet := store.KVMGet(ctx, metaKey, openRouterListKey)
	if errGet == nil && (len(values) != 2 || len(found) != 2) {
		errGet = fmt.Errorf("multi-get returned %d values for 2 keys", len(values))
	}
	if errGet != nil {
		return storeState{}, errGet
	}
	state := storeState{secret: r.secret}
	metaData, metaFound := values[0], found[0]
	if !metaFound && create {
		meta := metaValue{V: 1, CreatedAt: formatTime(r.opts.now()), FirstDay: firstDay, KeySecret: base64.StdEncoding.EncodeToString(r.secret)}
		data, errEncode := json.Marshal(meta)
		if errEncode != nil {
			return storeState{}, errEncode
		}
		if _, errSet := store.KVSetNX(ctx, metaKey, data, 0); errSet != nil {
			return storeState{}, errSet
		}
		var errRead error
		if metaData, metaFound, errRead = store.KVGet(ctx, metaKey); errRead != nil {
			return storeState{}, errRead
		}
	}
	if metaFound {
		if meta, secret, errMeta := decodeMeta(metaData); errMeta == nil {
			state.meta, state.secret, state.metaOK = meta, secret, true
		} else {
			r.logOnce(metaKey, "savings: %s does not decode; key ids use this process's secret", metaKey)
		}
	}
	if found[1] {
		state.savedSeen = true
		if list, at, errList := decodeListCopy(values[1]); errList == nil {
			state.saved, state.savedAt = list, at
		} else {
			r.logOnce(openRouterListKey, "savings: %s does not decode and is ignored", openRouterListKey)
		}
	}
	return state, nil
}

// readDays reads days from the store in one call: a day absent from the answer is not in the store;
// a day that does not decode is nil (logged once, never overwritten, skipped by reads).
func (r *recorder) readDays(ctx context.Context, store localStore, days []string) (map[string]*dayRows, error) {
	out := make(map[string]*dayRows, len(days))
	if len(days) == 0 {
		return out, nil
	}
	keys := make([]string, len(days))
	for i, day := range days {
		keys[i] = dayKey(day)
	}
	values, found, errGet := store.KVMGet(ctx, keys...)
	if errGet == nil && (len(values) != len(keys) || len(found) != len(keys)) {
		errGet = fmt.Errorf("multi-get returned %d values for %d keys", len(values), len(keys))
	}
	if errGet != nil {
		return nil, errGet
	}
	for i, day := range days {
		if !found[i] {
			continue
		}
		decoded, errDecode := decodeDay(values[i])
		if errDecode != nil {
			r.logOnce(dayKey(day), "savings: %s does not decode; it is kept as it is and skipped", dayKey(day))
			out[day] = nil
			continue
		}
		out[day] = decoded
	}
	return out, nil
}

// newDayRowsNow is an empty day in the zone in use now.
func (r *recorder) newDayRowsNow() *dayRows {
	_, offset := r.opts.now().In(r.location()).Zone()
	return newDayRows(r.location().String(), offset)
}

// saveRound is one round of the saver (01-recording.md section 5), all of it under mu: read meta and
// the days with deltas from the store in use now; move first_day back first when a day is earlier
// (an index too early is harmless, one too late hides a day from period=all); then for each day add
// the deltas, give rows without a price one, write the day, and only once the write succeeded forget
// those deltas. A failed read or write keeps them for the next round. Then the price list copy.
func (r *recorder) saveRound(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drainLocked()
	r.refreshKept()
	if len(r.deltas) == 0 {
		r.saveArrivedList(ctx)
		return
	}
	work := slices.Sorted(maps.Keys(r.deltas))
	r.refreshPrices()

	store, errStore := r.opts.store()
	if errStore != nil || store == nil {
		if errStore != nil {
			log.Debugf("savings: local store unavailable: %v", errStore)
		}
		return // the deltas wait in memory
	}
	state, errState := r.readState(ctx, store, true, work[0])
	if errState != nil {
		r.failed(errState)
		return
	}
	stored, errDays := r.readDays(ctx, store, work)
	if errDays != nil {
		r.failed(errDays)
		return
	}
	// The index before the days: no day is written while meta's first_day is later than it.
	if errFirst := r.moveFirstDay(ctx, store, state, work[0]); errFirst != nil {
		r.failed(errFirst)
		return
	}
	book := bookOf(r.downloaded, r.downloadedAt, r.remote, state.saved, state.savedAt, r.opts.table)
	wrote, failedAny := false, false
	for _, day := range work {
		rows, inStore := stored[day]
		if inStore && rows == nil {
			continue // does not decode: never overwritten; its deltas stay in memory
		}
		if rows == nil {
			rows = r.newDayRowsNow()
		}
		rows.mergePending(r.deltas[day].rows, state.secret)
		rows.assignPrices(book)
		data, errEncode := rows.encode(day)
		if errEncode != nil {
			log.Errorf("savings: encode %s: %v", dayKey(day), errEncode)
			continue
		}
		if _, errSet := store.KVSet(ctx, dayKey(day), data, kvstore.KVSetOptions{}); errSet != nil {
			r.failed(errSet)
			failedAny = true
			continue
		}
		delete(r.deltas, day) // only now: the store holds them
		wrote = true
		r.lastSavedAt = r.opts.now()
	}
	if wrote && !failedAny {
		r.succeeded()
	}
	r.saveListCopy(ctx, store, state)
}

// saveArrivedList saves, in a round with no day to write, the downloaded list when the store in use
// now has no copy or an older one: the first download often completes after the round that asked for
// it, and after a switch of server.kv-store.path the new store may have none (T-124 finding 7). It
// reads that store's copy every such round (one KVMGet, nothing written when it is current); no store
// call while no list was downloaded. Under mu.
func (r *recorder) saveArrivedList(ctx context.Context) {
	if r.downloaded == nil {
		return
	}
	store, errStore := r.opts.store()
	if errStore != nil || store == nil {
		return
	}
	state, errState := r.readState(ctx, store, false, "")
	if errState != nil {
		r.failed(errState)
		return
	}
	r.saveListCopy(ctx, store, state)
}

// moveFirstDay writes meta again when a day before its first_day is about to be written. Under mu.
func (r *recorder) moveFirstDay(ctx context.Context, store localStore, state storeState, earliest string) error {
	if !state.metaOK || state.meta.FirstDay == "" || earliest >= state.meta.FirstDay {
		return nil
	}
	meta := state.meta
	meta.FirstDay = earliest
	data, errEncode := json.Marshal(meta)
	if errEncode != nil {
		return errEncode
	}
	_, errSet := store.KVSet(ctx, metaKey, data, kvstore.KVSetOptions{})
	return errSet
}

// saveListCopy writes the downloaded list when it is newer than the store's copy and differs from it.
// Under mu.
func (r *recorder) saveListCopy(ctx context.Context, store localStore, state storeState) {
	list, fetchedAt := r.downloaded, r.downloadedAt
	if list == nil {
		return
	}
	if state.saved != nil && (!fetchedAt.After(state.savedAt) || reflect.DeepEqual(list, state.saved)) {
		return
	}
	data, errEncode := encodeListCopy(list, fetchedAt)
	if errEncode != nil {
		return
	}
	if _, errSet := store.KVSet(ctx, openRouterListKey, data, kvstore.KVSetOptions{}); errSet != nil {
		r.failed(errSet)
	}
}
