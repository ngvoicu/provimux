package cliproxy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: what each credential's answers said to each client (.github/provimux/design/answered-by.md 4.4, as
// amended by the lead on 2026-11-27). A statement is noted once it has been written to its client (the response's
// headers, a WebSocket turn's response.created), never when it is only offered: under the client's key reference, the
// credential that answered, its marker, and the name called, exactly. Both lists read it back: a client's list gives
// what answers to its own key said, by day; the management list gives every key's together, with the time.
//
//   - Per client, credential, marker and name, at most four distinct outcomes (an entry, or none), newest first, each
//     with the time of the last answer that had it, taken under the lock that orders the list. A fifth drops the
//     oldest. An item older than 90 days is not shown, and is dropped from the file at the next write.
//   - Under the name called where the credential registers exactly that id, else that id without its thinking suffix
//     where it registers that; else nothing. No case folding.
//   - A client key is kept as a keyed hash under the markers' key, the credential's id likewise; callers without a key
//     share one record. Another account's login in the same file has another marker and starts empty.
//   - Kept in one file of its own in the auth folder (provimux-answered.v1, not a login file: no .json), rewritten whole
//     and atomically by one writer: when an item is added or an item's day changes, before the place changes, and at
//     the stop. What is in memory belongs to one file: when the auth folder changes, memory is written to the old file
//     and replaced by what the new one holds, never written over it. At the stop no note is accepted once the last
//     write has begun. Without a usable place the record lives for this run, and the log says so once.
//   - With models.answered-by off (and in Home mode, where it stays off) nothing is noted or read.
//
// It holds markers, names, entries, times and keyed hashes: nothing of a request's content and no client key.

// provimuxAnsweredKept is how long an item is shown after the last answer that had it.
const provimuxAnsweredKept = 90 * 24 * time.Hour

// provimuxAnsweredMaxItems is how many distinct outcomes a row keeps for one client.
const provimuxAnsweredMaxItems = 4

// provimuxAnsweredFile is the record's file in the auth folder.
const provimuxAnsweredFile = "provimux-answered.v1"

// provimuxAnsweredRow is a row of the lists: a credential (its keyed hash), its marker and a name.
type provimuxAnsweredRow struct {
	credential, marker, name string
}

// provimuxAnsweredItem is one outcome: the entry ("" = none) and the time of the last answer that had it.
type provimuxAnsweredItem struct {
	entry string
	at    time.Time
}

// provimuxAnsweredBook is the record.
type provimuxAnsweredBook struct {
	now func() time.Time

	mu     sync.Mutex
	rows   map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem // row, client -> items
	place  string                                                    // the file; "" = none
	broken bool                                                      // the file exists and could not be read: never written over
	closed bool                                                      // the stop has begun: no note is accepted
	dirty  bool                                                      // the file must learn of a change
	unsent bool                                                      // anything changed since the last write

	wake       chan struct{}
	writerOnce sync.Once
	writing    sync.Mutex // one writer at a time
	logged     atomic.Bool
}

func newProvimuxAnsweredBook(now func() time.Time) *provimuxAnsweredBook {
	return &provimuxAnsweredBook{now: now, rows: map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}, wake: make(chan struct{}, 1)}
}

// provimuxAnswered is the gateway's record.
var provimuxAnswered = newProvimuxAnsweredBook(time.Now)

func init() {
	coreauth.SetProvimuxAnswered(provimuxAnsweredName, func(client string, all bool, authID, marker, name string) []coreauth.ProvimuxAnsweredItem {
		return provimuxAnswered.read(client, all, coreauth.ProvimuxCredentialRef(authID), marker, name)
	})
	logging.SetProvimuxAnsweredWritten(func(statement logging.AuthStatement) {
		provimuxAnswered.note(statement.Provimux.Client, coreauth.ProvimuxCredentialRef(statement.Provimux.Credential), statement.Provimux.Account, statement.Provimux.Name, statement.Provimux.Entry)
	})
}

// provimuxAnsweredName is the id of the row an answer for the name called stands on: the name where the credential
// registers exactly it, else the name without its thinking suffix where it registers exactly that; "" else.
func provimuxAnsweredName(authID, called string) string {
	if called == "" || authID == "" {
		return ""
	}
	models := registry.GetGlobalRegistry()
	if models.LookupClientModelRoute(authID, called).ID == called {
		return called
	}
	if parsed := thinking.ParseSuffix(called); parsed.HasSuffix && parsed.ModelName != "" && models.LookupClientModelRoute(authID, parsed.ModelName).ID == parsed.ModelName {
		return parsed.ModelName
	}
	return ""
}

// note records one written answer's outcome for its client. Memory only; the writer is woken when the file must learn
// of it.
func (b *provimuxAnsweredBook) note(client, credential, marker, name, entry string) {
	if credential == "" || marker == "" || name == "" {
		return
	}
	row := provimuxAnsweredRow{credential: credential, marker: marker, name: name}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	now := b.now().UTC() // under the lock: the order of the list is the order of the times
	clients := b.rows[row]
	if clients == nil {
		clients = map[string][]provimuxAnsweredItem{}
		b.rows[row] = clients
	}
	items, changed := provimuxAnsweredAdd(clients[client], provimuxAnsweredItem{entry: entry, at: now}, now)
	clients[client] = items
	b.unsent = true
	b.dirty = b.dirty || changed
	b.mu.Unlock()
	if changed {
		b.startWriter()
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

// provimuxAnsweredAdd puts item in items, newest first: an item of the same outcome moves with the new time, a new
// outcome is added and the oldest of five goes, items past the kept time go. changed is true when the file must learn
// of the result: an item added or dropped, or an item's day changed.
func provimuxAnsweredAdd(items []provimuxAnsweredItem, item provimuxAnsweredItem, now time.Time) (out []provimuxAnsweredItem, changed bool) {
	found := false
	out = make([]provimuxAnsweredItem, 0, provimuxAnsweredMaxItems+1)
	for _, old := range items {
		if old.entry == item.entry {
			found = true
			changed = changed || provimuxAnsweredDay(old.at) != provimuxAnsweredDay(item.at)
			if old.at.After(item.at) {
				item.at = old.at
			}
			continue
		}
		if now.Sub(old.at) > provimuxAnsweredKept {
			changed = true
			continue
		}
		out = append(out, old)
	}
	out = append(out, item)
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	if len(out) > provimuxAnsweredMaxItems {
		out, changed = out[:provimuxAnsweredMaxItems], true
	}
	return out, changed || !found
}

// provimuxAnsweredDay is the day, in UTC, of t.
func provimuxAnsweredDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// read returns the items of a client (of every client with all) for a row, newest first, none past the kept time.
func (b *provimuxAnsweredBook) read(client string, all bool, credential, marker, name string) []coreauth.ProvimuxAnsweredItem {
	if credential == "" || marker == "" || name == "" {
		return nil
	}
	b.mu.Lock()
	now := b.now().UTC()
	clients := b.rows[provimuxAnsweredRow{credential: credential, marker: marker, name: name}]
	var items []provimuxAnsweredItem
	if all {
		latest := map[string]time.Time{}
		for _, list := range clients {
			for _, item := range list {
				if at, ok := latest[item.entry]; !ok || item.at.After(at) {
					latest[item.entry] = item.at
				}
			}
		}
		for entry, at := range latest {
			items = append(items, provimuxAnsweredItem{entry: entry, at: at})
		}
		sort.Slice(items, func(i, j int) bool {
			if !items[i].at.Equal(items[j].at) {
				return items[i].at.After(items[j].at)
			}
			return items[i].entry < items[j].entry
		})
	} else {
		items = append(items, clients[client]...)
	}
	b.mu.Unlock()
	out := make([]coreauth.ProvimuxAnsweredItem, 0, len(items))
	for _, item := range items {
		if now.Sub(item.at) <= provimuxAnsweredKept && len(out) < provimuxAnsweredMaxItems {
			out = append(out, coreauth.ProvimuxAnsweredItem{Entry: item.entry, At: item.at})
		}
	}
	return out
}

// setPlace puts the record in the file of dir: memory is written to the old file first, then replaced by what the new
// one holds. The same place again changes nothing; a new place after a stop opens the record again.
func (b *provimuxAnsweredBook) setPlace(dir string) {
	b.writing.Lock()
	defer b.writing.Unlock()
	place := ""
	if dir != "" {
		place = filepath.Join(dir, provimuxAnsweredFile)
	}
	b.mu.Lock()
	same, old := place == b.place && !b.closed, b.place
	b.mu.Unlock()
	if same {
		return
	}
	if old != "" {
		b.writeLocked(true)
	}
	rows, broken, errLoad := provimuxAnsweredLoad(place, b.now().UTC())
	if errLoad != nil {
		b.logNotKept(errLoad)
	}
	b.mu.Lock()
	b.place, b.broken, b.closed = place, broken, false
	b.rows = rows
	b.dirty, b.unsent = false, false
	b.mu.Unlock()
}

// close stops the intake, then writes: a note that comes after the last write began is not accepted.
func (b *provimuxAnsweredBook) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.writing.Lock()
	defer b.writing.Unlock()
	b.writeLocked(true)
}

// flush writes what changed.
func (b *provimuxAnsweredBook) flush() {
	b.writing.Lock()
	defer b.writing.Unlock()
	b.writeLocked(true)
}

// startWriter starts the writer: it writes what the file must learn each time it is woken.
func (b *provimuxAnsweredBook) startWriter() {
	b.writerOnce.Do(func() {
		go func() {
			for range b.wake {
				b.writing.Lock()
				b.writeLocked(false)
				b.writing.Unlock()
			}
		}()
	})
}

// provimuxAnsweredStored is the file's form.
type provimuxAnsweredStored struct {
	Version int                         `json:"version"`
	Rows    []provimuxAnsweredStoredRow `json:"rows"`
}

type provimuxAnsweredStoredRow struct {
	Credential string                       `json:"credential"`
	Marker     string                       `json:"marker"`
	Name       string                       `json:"name"`
	Client     string                       `json:"client"`
	Items      []provimuxAnsweredStoredItem `json:"items"`
}

type provimuxAnsweredStoredItem struct {
	Entry *string `json:"entry"`
	At    string  `json:"at"`
}

// writeLocked writes the whole record to its file, atomically, when it changed (anything with all, else only what
// the file must learn). Items past the kept time are left out. The caller holds b.writing.
func (b *provimuxAnsweredBook) writeLocked(all bool) {
	b.mu.Lock()
	if !(b.dirty || (all && b.unsent)) {
		b.mu.Unlock()
		return
	}
	place, broken := b.place, b.broken
	if place == "" || broken {
		b.dirty, b.unsent = false, false
		b.mu.Unlock()
		if place == "" {
			b.logNotKept(errors.New("no auth folder to keep it in"))
		}
		return
	}
	now := b.now().UTC()
	stored := provimuxAnsweredStored{Version: 1}
	for row, clients := range b.rows {
		for client, items := range clients {
			out := provimuxAnsweredStoredRow{Credential: row.credential, Marker: row.marker, Name: row.name, Client: client}
			for _, item := range items {
				if now.Sub(item.at) > provimuxAnsweredKept {
					continue
				}
				kept := provimuxAnsweredStoredItem{At: item.at.Format(time.RFC3339Nano)}
				if item.entry != "" {
					entry := item.entry
					kept.Entry = &entry
				}
				out.Items = append(out.Items, kept)
			}
			if len(out.Items) > 0 {
				stored.Rows = append(stored.Rows, out)
			}
		}
	}
	b.dirty, b.unsent = false, false
	b.mu.Unlock()
	sort.Slice(stored.Rows, func(i, j int) bool {
		a, c := stored.Rows[i], stored.Rows[j]
		if a.Credential != c.Credential {
			return a.Credential < c.Credential
		}
		if a.Marker != c.Marker {
			return a.Marker < c.Marker
		}
		if a.Name != c.Name {
			return a.Name < c.Name
		}
		return a.Client < c.Client
	})
	if errWrite := provimuxAnsweredWrite(place, stored); errWrite != nil {
		b.mu.Lock()
		b.dirty, b.unsent = true, true
		b.mu.Unlock()
		b.logNotKept(errWrite)
	}
}

// provimuxAnsweredWrite writes stored to place whole and atomically: a temporary file beside it, then a rename.
func provimuxAnsweredWrite(place string, stored provimuxAnsweredStored) error {
	raw, errMarshal := json.Marshal(stored)
	if errMarshal != nil {
		return errMarshal
	}
	if errDir := os.MkdirAll(filepath.Dir(place), 0o700); errDir != nil {
		return fmt.Errorf("the auth folder could not be made: %w", errDir)
	}
	tmp, errTmp := os.CreateTemp(filepath.Dir(place), "."+provimuxAnsweredFile+".*")
	if errTmp != nil {
		return fmt.Errorf("the record could not be written: %w", errTmp)
	}
	name := tmp.Name()
	_, errWrite := tmp.Write(raw)
	errSync := tmp.Sync()
	errClose := tmp.Close()
	if errWrite != nil || errSync != nil || errClose != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the record could not be written: %v", errors.Join(errWrite, errSync, errClose))
	}
	if errRename := os.Rename(name, place); errRename != nil {
		_ = os.Remove(name)
		return fmt.Errorf("the record could not be written: %w", errRename)
	}
	return nil
}

// provimuxAnsweredLoad reads the record of place, items past the kept time left out. A missing file is an empty
// record; one that cannot be read is an empty record that is never written over (broken).
func provimuxAnsweredLoad(place string, now time.Time) (rows map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem, broken bool, err error) {
	rows = map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}
	if place == "" {
		return rows, false, nil
	}
	raw, errRead := os.ReadFile(place)
	if errors.Is(errRead, os.ErrNotExist) {
		return rows, false, nil
	}
	if errRead != nil {
		return rows, true, fmt.Errorf("the record's file could not be read (it is left as it is): %w", errRead)
	}
	var stored provimuxAnsweredStored
	if errDecode := json.Unmarshal(raw, &stored); errDecode != nil || stored.Version != 1 {
		return rows, true, errors.New("the record's file is not one this gateway writes (it is left as it is)")
	}
	for _, kept := range stored.Rows {
		row := provimuxAnsweredRow{credential: kept.Credential, marker: kept.Marker, name: kept.Name}
		var items []provimuxAnsweredItem
		for _, item := range kept.Items {
			at, errAt := time.Parse(time.RFC3339Nano, item.At)
			if errAt != nil || now.Sub(at) > provimuxAnsweredKept {
				continue
			}
			entry := ""
			if item.Entry != nil {
				entry = *item.Entry
			}
			items = append(items, provimuxAnsweredItem{entry: entry, at: at.UTC()})
		}
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
		if len(items) > provimuxAnsweredMaxItems {
			items = items[:provimuxAnsweredMaxItems]
		}
		if rows[row] == nil {
			rows[row] = map[string][]provimuxAnsweredItem{}
		}
		rows[row][kept.Client] = items
	}
	return rows, false, nil
}

// logNotKept says once why the record is not kept beyond this run.
func (b *provimuxAnsweredBook) logNotKept(why error) {
	if b.logged.CompareAndSwap(false, true) {
		log.Warnf("answered-by: what each account last answered as is not kept: %v; it holds until this process ends", why)
	}
}

// provimuxAnsweredPlace puts the record in the auth folder of cfg, where the service applies a config (the start and
// every reload): beside the local store, in kv_store.go.
func provimuxAnsweredPlace(cfg *config.Config) {
	dir := ""
	if cfg != nil {
		if resolved, errDir := util.ResolveAuthDir(cfg.AuthDir); errDir == nil {
			dir = resolved
		}
	}
	provimuxAnswered.setPlace(dir)
}

// provimuxCloseAnswered stops the record's intake and writes it, where the service stops.
func provimuxCloseAnswered() { provimuxAnswered.close() }
