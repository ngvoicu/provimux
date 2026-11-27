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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: what each credential's answers said to each client (.github/provimux/design/answered-by.md, "The record").
// A cache of what answers named, rebuilt by the next answer; nothing a user owns. A statement is noted once it has been
// written to its client (the response's headers, a WebSocket turn's response.created), never when it is only offered:
// under the client's key reference, the credential that answered, its marker, and the name called, exactly. Both
// lists read it back: a client's list gives what answers to its own key said, by day; the management list gives every
// key's together, with the time.
//
//   - Per client, credential, marker and name, at most four distinct outcomes (an entry, or none), newest first. The
//     order is the order the notes were accepted in (a sequence of the record's own, under its lock), never the
//     clock's. Each item keeps the time of the last answer that had it. A fifth drops the oldest. An item older than
//     90 days is not shown, and is dropped from the file at the next write.
//   - Under the name called where the credential registers exactly that id, else that id without its thinking suffix
//     where it registers that; else nothing. No case folding.
//   - A client key is kept as a keyed hash under the markers' key, the credential's id likewise; callers without a key
//     share one record. Another account's login in the same file has another marker and starts empty.
//   - Kept in a cache file of its own in the auth folder (provimux-answered.v1, not a login file: no .json), JSON with
//     a version, written whole and atomically after each change. A file that does not parse, or is of another version,
//     is ignored (said once in the log) and replaced at the next write. When the auth folder changes, memory is written
//     to the old file and replaced by what the new one holds. The stop is final: the last write runs, and no note is
//     accepted after it.
//   - With models.answered-by off, in Home mode, and in a process Provimux's own main did not start, nothing is noted
//     and the file is neither read nor written; switching to off or Home drops what is in memory, unwritten.
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

// provimuxAnsweredItem is one outcome: the entry ("" = none), the time of the last answer that had it, and the
// sequence of that answer's note (the order of the list).
type provimuxAnsweredItem struct {
	entry string
	at    time.Time
	seq   uint64
}

// provimuxAnsweredBook is the record: memory, and the folder its file is kept in.
type provimuxAnsweredBook struct {
	now func() time.Time

	mu     sync.Mutex
	rows   map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem // row, client -> items
	seq    uint64                                                    // the last note's sequence
	dir    string                                                    // the auth folder; "" = no file
	closed bool                                                      // the stop has begun: no note is accepted, ever again
	dirty  bool                                                      // memory differs from the file

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

// note records one written answer's outcome for its client, in memory, and wakes the writer when what the lists
// show changed.
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
	now := b.now().UTC()
	b.seq++ // under the lock: the order of the list is the order the notes were accepted in
	clients := b.rows[row]
	if clients == nil {
		clients = map[string][]provimuxAnsweredItem{}
		b.rows[row] = clients
	}
	items, changed := provimuxAnsweredAdd(clients[client], provimuxAnsweredItem{entry: entry, at: now, seq: b.seq}, now)
	clients[client] = items
	b.dirty = true
	b.mu.Unlock()
	if changed { // a new outcome or a new day is written at once; a later time on the same day at the next write
		b.writerOnce.Do(func() {
			go func() {
				for range b.wake {
					b.flush()
				}
			}()
		})
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

// provimuxAnsweredAdd puts item (the newest note) in items, newest first: an item of the same outcome moves to the front
// with the new note, a new outcome is added in front and the oldest of five goes, items past the kept time go. changed is true when the file must learn
// of the result: an item added or dropped, or an item's day changed.
func provimuxAnsweredAdd(items []provimuxAnsweredItem, item provimuxAnsweredItem, now time.Time) (out []provimuxAnsweredItem, changed bool) {
	found := false
	out = make([]provimuxAnsweredItem, 0, provimuxAnsweredMaxItems+1)
	for _, old := range items {
		if old.entry == item.entry {
			// The item takes the newest note's time as it is: the day and time shown are the last answer's by
			// sequence, also when the clock was set back between the two (the amendment of 2026-11-27 (2), part 3).
			found = true
			changed = changed || provimuxAnsweredDay(old.at) != provimuxAnsweredDay(item.at)
			continue
		}
		if now.Sub(old.at) > provimuxAnsweredKept {
			changed = true
			continue
		}
		out = append(out, old)
	}
	out = append(out, item)
	sort.SliceStable(out, func(i, j int) bool { return out[i].seq > out[j].seq })
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
		latest := map[string]provimuxAnsweredItem{}
		for _, list := range clients {
			for _, item := range list {
				if have, ok := latest[item.entry]; !ok || item.seq > have.seq {
					latest[item.entry] = item
				}
			}
		}
		for _, item := range latest {
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].seq > items[j].seq })
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

// setPlace puts the record in the file of dir: memory is written to the old file first, then replaced by what the
// new one holds. The same place again changes nothing; after the stop nothing changes.
func (b *provimuxAnsweredBook) setPlace(dir string) {
	b.writing.Lock()
	defer b.writing.Unlock()
	b.mu.Lock()
	same, stopped := dir == b.dir, b.closed
	b.mu.Unlock()
	if same || stopped {
		return
	}
	b.writeLocked()
	rows, seq := map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}, uint64(0)
	if dir != "" {
		var errLoad error
		rows, seq, errLoad = provimuxAnsweredLoad(filepath.Join(dir, provimuxAnsweredFile), b.now().UTC())
		if errLoad != nil {
			log.Warnf("answered-by: %s is ignored (%v); the next answer writes a new one", filepath.Join(dir, provimuxAnsweredFile), errLoad)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dir, b.rows, b.seq, b.dirty = dir, rows, seq, false
}

// drop puts the record out of use without reading or writing any file: memory is emptied. Switching
// models.answered-by off, or to Home mode, does this.
func (b *provimuxAnsweredBook) drop() {
	b.writing.Lock()
	defer b.writing.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dir, b.rows, b.dirty = "", map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}, false
}

// close stops the intake for good, then writes: a note that comes after the stop began is not accepted.
func (b *provimuxAnsweredBook) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.flush()
}

// flush writes what changed.
func (b *provimuxAnsweredBook) flush() {
	b.writing.Lock()
	defer b.writing.Unlock()
	b.writeLocked()
}

// provimuxAnsweredVersion is the version of the file's form this build writes and reads.
const provimuxAnsweredVersion = 1

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
	Entry *string   `json:"entry"`
	At    time.Time `json:"at"`
	Seq   uint64    `json:"seq"`
}

// writeLocked writes the whole record to its file, atomically, when memory changed since the last write. Items past
// the kept time are left out. The caller holds b.writing.
func (b *provimuxAnsweredBook) writeLocked() {
	b.mu.Lock()
	if !b.dirty || b.dir == "" {
		b.dirty = false
		b.mu.Unlock()
		return
	}
	dir, now := b.dir, b.now().UTC()
	stored := provimuxAnsweredStored{Version: provimuxAnsweredVersion, Rows: []provimuxAnsweredStoredRow{}}
	for row, clients := range b.rows {
		for client, items := range clients {
			out := provimuxAnsweredStoredRow{Credential: row.credential, Marker: row.marker, Name: row.name, Client: client}
			for _, item := range items {
				if now.Sub(item.at) > provimuxAnsweredKept {
					continue
				}
				kept := provimuxAnsweredStoredItem{At: item.at, Seq: item.seq}
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
	b.dirty = false
	b.mu.Unlock()
	data, err := json.Marshal(stored)
	if err == nil {
		err = modellists.WriteFileAtomic(dir, provimuxAnsweredFile, data)
	}
	if err != nil {
		b.mu.Lock()
		b.dirty = true
		b.mu.Unlock()
		if b.logged.CompareAndSwap(false, true) {
			log.Warnf("answered-by: what each account last answered as is not kept: %v; it holds until this process ends", err)
		}
	}
}

// provimuxAnsweredLoad reads the record's file, items past the kept time left out. A missing file is an empty
// record; a file that cannot be read or parsed, or is of another version, is an error (and an empty record).
func provimuxAnsweredLoad(place string, now time.Time) (map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem, uint64, error) {
	rows := map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}
	raw, err := os.ReadFile(place)
	if errors.Is(err, os.ErrNotExist) {
		return rows, 0, nil
	}
	var stored provimuxAnsweredStored
	if err == nil {
		err = json.Unmarshal(raw, &stored)
	}
	if err == nil && stored.Version != provimuxAnsweredVersion {
		err = fmt.Errorf("version %d", stored.Version)
	}
	if err != nil {
		return rows, 0, err
	}
	var seq uint64
	for _, kept := range stored.Rows {
		row := provimuxAnsweredRow{credential: kept.Credential, marker: kept.Marker, name: kept.Name}
		var items []provimuxAnsweredItem
		for _, item := range kept.Items {
			seq = max(seq, item.Seq)
			if now.Sub(item.At) > provimuxAnsweredKept {
				continue
			}
			entry := ""
			if item.Entry != nil {
				entry = *item.Entry
			}
			items = append(items, provimuxAnsweredItem{entry: entry, at: item.At.UTC(), seq: item.Seq})
		}
		if len(items) == 0 || row.credential == "" || row.marker == "" || row.name == "" {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].seq > items[j].seq })
		if rows[row] == nil {
			rows[row] = map[string][]provimuxAnsweredItem{}
		}
		rows[row][kept.Client] = items[:min(len(items), provimuxAnsweredMaxItems)]
	}
	return rows, seq, nil
}

// provimuxAnsweredPlace puts the record in the auth folder of cfg, where the service applies a config (the start and
// every reload): beside the local store, in kv_store.go. With models.answered-by off as in force (the service puts a
// config's switch in force before it applies the config's store), and in Home mode, it has no place, and the file is
// not read.
func provimuxAnsweredPlace(cfg *config.Config) {
	if cfg == nil || !coreauth.ProvimuxRecordOn() || provimuxHomeMode(cfg) {
		provimuxAnswered.drop()
		return
	}
	dir := ""
	if resolved, errDir := util.ResolveAuthDir(cfg.AuthDir); errDir == nil {
		dir = resolved
	}
	provimuxAnswered.setPlace(dir)
}

// provimuxCloseAnswered stops the record's intake for good and writes it, where the service stops.
func provimuxCloseAnswered() { provimuxAnswered.close() }
