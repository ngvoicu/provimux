package cliproxy

import (
	"bytes"
	"crypto/sha256"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gofrs/flock"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: what each credential's answers said to each client (.github/provimux/design/answered-by.md 4.4, as
// amended by the lead on 2026-11-27, twice). A best-effort memory of what answers named, rebuilt by the next answer;
// nothing a user owns. A statement is noted once the stock output route has written it to its client (the response's
// headers as they were committed, a WebSocket turn's response.created), never when it is only offered: under the
// client's key reference, the credential that answered, its marker, and the name called, exactly. Both lists read it
// back: a client's list gives what answers to its own key said, by day; the management list gives every key's
// together, with the time.
//
//   - Per client, credential, marker and name, at most four distinct outcomes (an entry, or none), newest first. The
//     order is the order the notes were accepted in (a sequence of the record's own, under its lock), never the
//     clock's: the newest answer is always kept and always first, also when several share a clock tick. Each item
//     keeps the time of the last answer that had it. A fifth drops the oldest. An item older than 90 days is not
//     shown, and is dropped from the file at the next write.
//   - Under the name called where the credential registers exactly that id, else that id without its thinking suffix
//     where it registers that; else nothing. No case folding.
//   - A client key is kept as a keyed hash under the markers' key, the credential's id likewise; callers without a key
//     share one record. Another account's login in the same file has another marker and starts empty.
//   - Kept in one file of its own in the auth folder (provimux-answered.v1, not a login file: no .json). One gateway
//     owns it, by a lock on provimux-answered.v1.lock held while it runs; a second gateway on the same folder keeps
//     its record in memory only, never reads or writes the file, and says so once. The owner rewrites the file whole
//     and atomically (a temporary file, then a rename), and only over a file that is recognisably its own and
//     unchanged since it last read or wrote it: anything else at that name (another program's file, a damaged or
//     truncated one, one that changed underneath) is left as it is, the record stays in memory, and the log says so.
//   - When the auth folder changes, memory is written to the old file and replaced by what the new one holds; a note
//     that arrives around the change may be missed. The stop is final: the intake closes, the last write runs, and
//     nothing opens the intake again in this process.
//   - With models.answered-by off, and in Home mode, nothing is noted and the file is not read.
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

// provimuxAnsweredBook is the record.
type provimuxAnsweredBook struct {
	now func() time.Time

	mu      sync.Mutex
	rows    map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem // row, client -> items
	seq     uint64                                                    // the last note's sequence
	place   string                                                    // the file; "" = none
	owner   bool                                                      // this gateway holds the place's lock
	foreign bool                                                      // the file at place is not (or no longer) this record's: never written over
	seen    provimuxAnsweredSeen                                      // the file as this record last read or wrote it
	closed  bool                                                      // the stop has begun: no note is accepted, ever again
	dirty   bool                                                      // the file must learn of a change
	unsent  bool                                                      // anything changed since the last write

	lock *flock.Flock // the place's lock, while held

	wake       chan struct{}
	writerOnce sync.Once
	writing    sync.Mutex // one writer at a time
	logged     atomic.Bool
	sharedLog  atomic.Bool
	foreignLog atomic.Bool
}

// provimuxAnsweredSeen is a file's fingerprint: whether it existed, and the hash of its bytes.
type provimuxAnsweredSeen struct {
	exists bool
	sum    [sha256.Size]byte
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
	now := b.now().UTC()
	b.seq++ // under the lock: the order of the list is the order the notes were accepted in
	clients := b.rows[row]
	if clients == nil {
		clients = map[string][]provimuxAnsweredItem{}
		b.rows[row] = clients
	}
	items, changed := provimuxAnsweredAdd(clients[client], provimuxAnsweredItem{entry: entry, at: now, seq: b.seq}, now)
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

// provimuxAnsweredAdd puts item (the newest note) in items, newest first: an item of the same outcome moves to the front
// with the new note, a new outcome is added in front and the oldest of five goes, items past the kept time go. changed is true when the file must learn
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

// setPlace puts the record in the file of dir: memory is written to the old file first (where this gateway owns it),
// then replaced by what the new one holds. The new place is this gateway's when it gets the place's lock; else the
// record is kept in memory only. The same place again changes nothing; after the stop nothing changes.
func (b *provimuxAnsweredBook) setPlace(dir string) {
	b.writing.Lock()
	defer b.writing.Unlock()
	place := ""
	if dir != "" {
		place = filepath.Join(dir, provimuxAnsweredFile)
	}
	b.mu.Lock()
	same, stopped, old := place == b.place, b.closed, b.place
	b.mu.Unlock()
	if same || stopped {
		return
	}
	if old != "" {
		b.writeLocked(true)
		b.release()
	}
	rows := map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}
	var seq uint64
	owner, foreign := false, false
	var seen provimuxAnsweredSeen
	var lock *flock.Flock
	if place != "" {
		var errLock error
		lock, errLock = provimuxAnsweredLock(place)
		switch {
		case errLock != nil:
			b.logNotKept(errLock)
		case lock == nil:
			if b.sharedLog.CompareAndSwap(false, true) {
				log.Warnf("answered-by: another gateway keeps the record of what each account last answered as in %s; this one keeps its own in memory only, until it stops", filepath.Dir(place))
			}
		default:
			owner = true
			var errLoad error
			rows, seq, seen, errLoad = provimuxAnsweredLoad(place, b.now().UTC())
			if errLoad != nil {
				foreign = true
				b.logForeign(place, errLoad)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed { // the stop came while the place changed: it is final, and the move is not made
		if lock != nil {
			_ = lock.Unlock()
		}
		return
	}
	b.place, b.owner, b.foreign, b.seen, b.lock = place, owner, foreign, seen, lock
	b.rows, b.seq = rows, seq
	b.dirty, b.unsent = false, false
}

// release gives up the place's lock. The caller holds b.writing.
func (b *provimuxAnsweredBook) release() {
	b.mu.Lock()
	lock := b.lock
	b.lock, b.owner = nil, false
	b.mu.Unlock()
	if lock != nil {
		_ = lock.Unlock()
	}
}

// provimuxAnsweredLock takes the lock of place's record, for as long as this gateway runs: nil without an error when
// another process (or another record in this one) holds it.
func provimuxAnsweredLock(place string) (*flock.Flock, error) {
	if errDir := os.MkdirAll(filepath.Dir(place), 0o700); errDir != nil {
		return nil, fmt.Errorf("the auth folder could not be made: %w", errDir)
	}
	lock := flock.New(place+".lock", flock.SetPermissions(0o600))
	locked, errLock := lock.TryLock()
	if errLock != nil {
		return nil, fmt.Errorf("the record's lock could not be taken: %w", errLock)
	}
	if !locked {
		return nil, nil
	}
	return lock, nil
}

// close stops the intake for good, then writes, and gives up the place: a note that comes after the stop began is not
// accepted, and nothing opens the intake again.
func (b *provimuxAnsweredBook) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.writing.Lock()
	defer b.writing.Unlock()
	b.writeLocked(true)
	b.release()
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

// provimuxAnsweredFormat is the record file's own mark: a file without it, or with anything this record does not
// write, is not this record's.
const provimuxAnsweredFormat = "provimux answered-by record"

// provimuxAnsweredStored is the file's form.
type provimuxAnsweredStored struct {
	Format  string                      `json:"format"`
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
	Seq   uint64  `json:"seq"`
}

// writeLocked writes the whole record to its file, atomically, when it changed (anything with all, else only what
// the file must learn), and only where this gateway owns the place and the file there is still the one it last read
// or wrote. Items past the kept time are left out. The caller holds b.writing.
func (b *provimuxAnsweredBook) writeLocked(all bool) {
	b.mu.Lock()
	if !(b.dirty || (all && b.unsent)) {
		b.mu.Unlock()
		return
	}
	place, owner, foreign, seen := b.place, b.owner, b.foreign, b.seen
	if place == "" || !owner || foreign {
		b.dirty, b.unsent = false, false
		b.mu.Unlock()
		if place == "" {
			b.logNotKept(errors.New("no auth folder to keep it in"))
		}
		return
	}
	now := b.now().UTC()
	stored := provimuxAnsweredStored{Format: provimuxAnsweredFormat, Version: 1, Rows: []provimuxAnsweredStoredRow{}}
	for row, clients := range b.rows {
		for client, items := range clients {
			out := provimuxAnsweredStoredRow{Credential: row.credential, Marker: row.marker, Name: row.name, Client: client}
			for _, item := range items {
				if now.Sub(item.at) > provimuxAnsweredKept {
					continue
				}
				kept := provimuxAnsweredStoredItem{At: item.at.Format(time.RFC3339Nano), Seq: item.seq}
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
	written, errWrite := provimuxAnsweredWrite(place, stored, seen)
	b.mu.Lock()
	switch {
	case errors.Is(errWrite, errProvimuxAnsweredNotOurs):
		b.foreign = true
	case errWrite != nil:
		b.dirty, b.unsent = true, true
	default:
		b.seen = written
	}
	b.mu.Unlock()
	switch {
	case errors.Is(errWrite, errProvimuxAnsweredNotOurs):
		b.logForeign(place, errWrite)
	case errWrite != nil:
		b.logNotKept(errWrite)
	}
}

// errProvimuxAnsweredNotOurs: the file at the record's place is not the one the record last read or wrote.
var errProvimuxAnsweredNotOurs = errors.New("the file there is not the one this gateway last read or wrote")

// provimuxAnsweredWrite writes stored to place whole and atomically (a temporary file, then a rename), when the file
// at place is still seen (or there is none). It returns what it wrote.
func provimuxAnsweredWrite(place string, stored provimuxAnsweredStored, seen provimuxAnsweredSeen) (provimuxAnsweredSeen, error) {
	raw, errMarshal := json.Marshal(stored)
	if errMarshal != nil {
		return seen, errMarshal
	}
	if errDir := os.MkdirAll(filepath.Dir(place), 0o700); errDir != nil {
		return seen, fmt.Errorf("the auth folder could not be made: %w", errDir)
	}
	tmp, errTmp := os.CreateTemp(filepath.Dir(place), "."+provimuxAnsweredFile+".*")
	if errTmp != nil {
		return seen, fmt.Errorf("the record could not be written: %w", errTmp)
	}
	name := tmp.Name()
	_, errWrite := tmp.Write(raw)
	errSync := tmp.Sync()
	errClose := tmp.Close()
	if errWrite != nil || errSync != nil || errClose != nil {
		_ = os.Remove(name)
		return seen, fmt.Errorf("the record could not be written: %v", errors.Join(errWrite, errSync, errClose))
	}
	// What is at place now must be what this record last read or wrote there, or nothing: checked right before the
	// rename, so another program's file, or one changed underneath, is left as it is.
	current, errRead := os.ReadFile(place)
	switch {
	case errors.Is(errRead, os.ErrNotExist):
	case errRead != nil:
		_ = os.Remove(name)
		return seen, fmt.Errorf("the record's file could not be read (it is left as it is): %w", errRead)
	case !seen.exists || sha256.Sum256(current) != seen.sum:
		_ = os.Remove(name)
		return seen, errProvimuxAnsweredNotOurs
	}
	if errRename := os.Rename(name, place); errRename != nil {
		_ = os.Remove(name)
		return seen, fmt.Errorf("the record could not be written: %w", errRename)
	}
	return provimuxAnsweredSeen{exists: true, sum: sha256.Sum256(raw)}, nil
}

// provimuxAnsweredLoad reads the record of place, items past the kept time left out. A missing file is an empty
// record. A file that cannot be read, or is not recognisably this record's (its mark, its version and nothing it does
// not write), is an error: it is never written over.
func provimuxAnsweredLoad(place string, now time.Time) (rows map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem, seq uint64, seen provimuxAnsweredSeen, err error) {
	rows = map[provimuxAnsweredRow]map[string][]provimuxAnsweredItem{}
	raw, errRead := os.ReadFile(place)
	if errors.Is(errRead, os.ErrNotExist) {
		return rows, 0, seen, nil
	}
	if errRead != nil {
		return rows, 0, seen, fmt.Errorf("the record's file could not be read: %w", errRead)
	}
	stored, ok := provimuxAnsweredParse(raw)
	if !ok {
		return rows, 0, seen, errors.New("the file there is not one this gateway writes")
	}
	seen = provimuxAnsweredSeen{exists: true, sum: sha256.Sum256(raw)}
	for _, kept := range stored.Rows {
		row := provimuxAnsweredRow{credential: kept.Credential, marker: kept.Marker, name: kept.Name}
		var items []provimuxAnsweredItem
		for _, item := range kept.Items {
			at, _ := time.Parse(time.RFC3339Nano, item.At)
			if item.Seq > seq {
				seq = item.Seq
			}
			if now.Sub(at) > provimuxAnsweredKept {
				continue
			}
			entry := ""
			if item.Entry != nil {
				entry = *item.Entry
			}
			items = append(items, provimuxAnsweredItem{entry: entry, at: at.UTC(), seq: item.Seq})
		}
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].seq > items[j].seq })
		if len(items) > provimuxAnsweredMaxItems {
			items = items[:provimuxAnsweredMaxItems]
		}
		if rows[row] == nil {
			rows[row] = map[string][]provimuxAnsweredItem{}
		}
		rows[row][kept.Client] = items
	}
	return rows, seq, seen, nil
}

// provimuxAnsweredParse reads raw as this record's file, strictly: one JSON object with the record's mark and version
// and nothing else, every row with its four strings and its items, every item with a time, a positive sequence and an
// entry or null. Anything else is not this record's.
func provimuxAnsweredParse(raw []byte) (provimuxAnsweredStored, bool) {
	var stored provimuxAnsweredStored
	decoder := stdjson.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&stored); errDecode != nil {
		return stored, false
	}
	if _, errMore := decoder.Token(); !errors.Is(errMore, io.EOF) {
		return stored, false
	}
	if stored.Format != provimuxAnsweredFormat || stored.Version != 1 || stored.Rows == nil {
		return stored, false
	}
	for _, row := range stored.Rows {
		if row.Credential == "" || row.Marker == "" || row.Name == "" || len(row.Items) == 0 {
			return stored, false
		}
		for _, item := range row.Items {
			if _, errAt := time.Parse(time.RFC3339Nano, item.At); errAt != nil || item.Seq == 0 {
				return stored, false
			}
		}
	}
	return stored, true
}

// logNotKept says once why the record is not kept beyond this run.
func (b *provimuxAnsweredBook) logNotKept(why error) {
	if b.logged.CompareAndSwap(false, true) {
		log.Warnf("answered-by: what each account last answered as is not kept: %v; it holds until this process ends", why)
	}
}

// logForeign says once that the file at the record's place is left as it is.
func (b *provimuxAnsweredBook) logForeign(place string, why error) {
	if b.foreignLog.CompareAndSwap(false, true) {
		log.Warnf("answered-by: %s is left as it is (%v); what each account last answered as is kept in memory only, until this process ends", place, why)
	}
}

// provimuxAnsweredPlace puts the record in the auth folder of cfg, where the service applies a config (the start and
// every reload): beside the local store, in kv_store.go. With models.answered-by off as in force (the service puts a
// config's switch in force before it applies the config's store), and in Home mode, it has no place, and the file is
// not read.
func provimuxAnsweredPlace(cfg *config.Config) {
	dir := ""
	if cfg != nil && logging.ProvimuxAnsweredBy() && !provimuxHomeMode(cfg) {
		if resolved, errDir := util.ResolveAuthDir(cfg.AuthDir); errDir == nil {
			dir = resolved
		}
	}
	provimuxAnswered.setPlace(dir)
}

// provimuxCloseAnswered stops the record's intake for good and writes it, where the service stops.
func provimuxCloseAnswered() { provimuxAnswered.close() }
