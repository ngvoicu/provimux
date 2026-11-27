package modellists

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists/pdentries"
)

// The rules decided by the lead on 2026-11-27 after the review of F2 and F3 (prices-and-cache.md, the note of
// that day): what a fetch and the disk copy refuse, and the values a broken summary carries that are left out.
// Every fetch goes to a stand-in on loopback; none reaches the real site.

// figuresRefusal is the fetch's failure, or the test fails.
func figuresRefusal(t *testing.T, err error, category string, status int) *FetchError {
	t.Helper()
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Category != category || fetchErr.Status != status {
		t.Fatalf("want a %s/%d failure, got %v", category, status, err)
	}
	return fetchErr
}

// figuresEnvelope is one page of GET /api/v1/entries holding every entry given.
func figuresEnvelope(t *testing.T, version, generated string, entries []json.RawMessage) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"meta":  map[string]any{"dataset_version": version, "generated_at": generated},
		"total": len(entries), "limit": FiguresPageLimit, "offset": 0, "next_offset": nil, "data": entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// A document of which no entry can be read never replaces a held one, and is no document to hold: every entry
// unreadable, or a dataset of 0 entries, is refused as invalid_response (ErrNoEntries), held or not.
func TestFiguresDocumentWithoutAReadableEntryIsRefused(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	entries := figuresVolume(t, 450)
	seed, err := fetchFiguresFrom(newFiguresStandIn(t, entries, fixture.Version, fixture.Generated), nil)
	if err != nil || len(seed.Document.Entries) != 450 {
		t.Fatalf("seed: %v", err)
	}
	held := seed.Document
	unreadable := make([]json.RawMessage, len(entries))
	for i, raw := range entries {
		// providers.dev renames one field every summary has.
		unreadable[i] = pdentries.Edit(raw, func(entry map[string]any) {
			entry["cache_lifetimes"] = entry["cache_lifetime"]
			delete(entry, "cache_lifetime")
		})
	}
	for name, set := range map[string][]json.RawMessage{"every entry unreadable": unreadable, "total 0": {}} {
		for _, h := range []*FiguresDocument{held, nil} {
			server := newFiguresStandIn(t, set, "ds-next", fixture.Generated)
			result, errFetch := fetchFiguresFrom(server, h)
			figuresRefusal(t, errFetch, FailureResponse, 0)
			if !errors.Is(errFetch, ErrNoEntries) || result.Document != nil || result.NotModified {
				t.Fatalf("%s (held %v): %+v %v", name, h != nil, result, errFetch)
			}
		}
	}
	if len(held.Entries) != 450 || held.DatasetVersion != fixture.Version {
		t.Fatal("the held document was changed")
	}
}

// A generated_at more than 24 hours ahead of the gateway's clock is refused, from the network (at its first page)
// and from the disk copy; 24 hours ahead exactly is not.
func TestFiguresGeneratedFarAheadOfTheClockIsRefused(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	generated, err := time.Parse(time.RFC3339Nano, fixture.Generated)
	if err != nil {
		t.Fatal(err)
	}
	server := newFiguresStandIn(t, figuresVolume(t, 450), fixture.Version, fixture.Generated)
	tooEarly, inTime := generated.Add(-FiguresMaxAhead-time.Second), generated.Add(-FiguresMaxAhead)
	fetch := func(now time.Time) (FiguresResult, error) {
		return FetchFigures(context.Background(), server.Client(), server.EntriesURL(), nil, "provimux/test", now, 5*time.Second, nil)
	}
	result, err := fetch(tooEarly)
	figuresRefusal(t, err, FailureResponse, 0)
	if !errors.Is(err, ErrAhead) || result.Document != nil || len(server.Requests()) != 1 {
		t.Fatalf("ahead: %v after %d requests", err, len(server.Requests()))
	}
	result, err = fetch(inTime)
	if err != nil || len(result.Document.Entries) != 450 {
		t.Fatalf("24 hours ahead exactly: %v", err)
	}

	dir := t.TempDir()
	if err = NewStore(dir).WriteFigures(result.Document); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dir)
	if doc, errRead := store.ReadFigures(tooEarly); !errors.Is(errRead, ErrAhead) || doc != nil || !store.warned["bad:"+FiguresFileName] {
		t.Fatalf("a disk copy ahead of the clock: %v", errRead)
	}
	if doc, errRead := NewStore(dir).ReadFigures(inTime); errRead != nil || len(doc.Entries) != 450 {
		t.Fatalf("a disk copy 24 hours ahead exactly: %v", errRead)
	}

	// The bound is 24 hours, as decided, and not whatever the constant says: a clock 24 hours and a millisecond, or 25
	// hours, behind the dataset is refused on both paths.
	for _, behind := range []time.Duration{24*time.Hour + time.Millisecond, 25 * time.Hour} {
		clock := generated.Add(-behind)
		if _, errFetch := fetch(clock); !errors.Is(errFetch, ErrAhead) {
			t.Fatalf("%s behind: the fetch gave %v", behind, errFetch)
		}
		if _, errRead := NewStore(dir).ReadFigures(clock); !errors.Is(errRead, ErrAhead) {
			t.Fatalf("%s behind: the disk copy gave %v", behind, errRead)
		}
	}
}

// A redirect is followed only to the same host over https: one to another origin, and one to the same host over
// plain http, answer as the redirect itself (http_status 302), and the other origin is never asked.
func TestFiguresRedirectOnlyToTheSameHostOverHTTPS(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	other := newFiguresStandIn(t, fixture.Entries, "ds-elsewhere", fixture.Generated)
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.EntriesURL()+"?"+r.URL.RawQuery, http.StatusFound)
	}))
	t.Cleanup(elsewhere.Close)
	_, err := FetchFigures(context.Background(), elsewhere.Client(), elsewhere.URL+pdentries.Path, nil, "provimux/test", figuresTestNow, 5*time.Second, nil)
	figuresRefusal(t, err, FailureHTTP, http.StatusFound)
	if n := len(other.Requests()); n != 0 {
		t.Fatalf("the other origin was asked %d times", n)
	}

	page := figuresEnvelope(t, fixture.Version, fixture.Generated, fixture.Entries)
	moved := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pdentries.Path {
			http.Redirect(w, r, "/moved"+pdentries.Path+"?"+r.URL.RawQuery, http.StatusFound)
			return
		}
		_, _ = w.Write(page)
	}
	plain := httptest.NewServer(http.HandlerFunc(moved))
	t.Cleanup(plain.Close)
	_, err = FetchFigures(context.Background(), plain.Client(), plain.URL+pdentries.Path, nil, "provimux/test", figuresTestNow, 5*time.Second, nil)
	figuresRefusal(t, err, FailureHTTP, http.StatusFound)

	secure := httptest.NewTLSServer(http.HandlerFunc(moved))
	t.Cleanup(secure.Close)
	result, err := FetchFigures(context.Background(), secure.Client(), secure.URL+pdentries.Path, nil, "provimux/test", figuresTestNow, 5*time.Second, nil)
	if err != nil || len(result.Document.Entries) != len(fixture.Entries) {
		t.Fatalf("the same host over https: %v", err)
	}

	// Another origin over https (another port on the same address, whose certificate the client trusts as it trusts
	// the first's) answers as the redirect itself, and is never asked.
	var asked atomic.Int32
	otherSecure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = w.Write(page)
	}))
	t.Cleanup(otherSecure.Close)
	away := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherSecure.URL+pdentries.Path+"?"+r.URL.RawQuery, http.StatusFound)
	}))
	t.Cleanup(away.Close)
	_, err = FetchFigures(context.Background(), away.Client(), away.URL+pdentries.Path, nil, "provimux/test", figuresTestNow, 5*time.Second, nil)
	figuresRefusal(t, err, FailureHTTP, http.StatusFound)
	if n := asked.Load(); n != 0 {
		t.Fatalf("another origin over https was asked %d times", n)
	}
}

// Values only a broken summary carries are left out where they are read, never the entry or the document: a
// negative cache write price, a price_from not of the contract's form (the entry's price is left out), a real
// client's item without lifetime_s (no measurement), a window or a tier too large to carry. Every record read
// passes the reading the disk copy is put through.
func TestFiguresValuesOnlyABrokenSummaryCarriesAreLeftOut(t *testing.T) {
	_, byRef := loadFiguresFixture(t)
	perUse, reference := byRef["anthropic/claude-opus-5-5"], byRef["claude-subscription/claude-opus-5-5"]
	read := func(raw json.RawMessage) Figures {
		t.Helper()
		entry, err := ReadEntrySummary(raw)
		if err != nil {
			t.Fatalf("the entry was left out: %v", err)
		}
		if err = entry.check(); err != nil {
			t.Fatalf("a record read does not pass its own reading: %v", err)
		}
		return entry
	}
	base := read(perUse)
	usd := map[string]any{"name": "token", "quantity": 1000000}

	t.Run("a negative cache write price", func(t *testing.T) {
		entry := read(pdentries.Edit(perUse, func(m map[string]any) {
			pricing := m["pricing"].(map[string]any)
			pricing["rules"] = append(pricing["rules"].([]any),
				map[string]any{"when": nil, "meter": "text.cache_write", "operation": "set", "amount": -5, "currency": "USD", "unit": usd},
				map[string]any{"when": map[string]any{"kind": "predicate", "field": "cache.retention_s", "op": "eq", "value": 600},
					"meter": "text.cache_write", "operation": "set", "amount": -1, "currency": "USD", "unit": usd})
		}))
		if len(entry.Price.CacheWrite) != len(base.Price.CacheWrite) || entry.Price.OtherRates != base.Price.OtherRates {
			t.Fatalf("cache_write %+v", entry.Price.CacheWrite)
		}
		for _, rate := range entry.Price.CacheWrite {
			if rate.Price < 0 {
				t.Fatalf("a negative cache write price was kept: %+v", entry.Price.CacheWrite)
			}
		}
	})

	t.Run("price_from not of the contract's form", func(t *testing.T) {
		if entry := read(reference); entry.Price.Billing != "reference" || entry.Price.Reference == nil || *entry.Price.Reference != "anthropic/claude-opus-5-5" {
			t.Fatalf("premise: %+v", entry.Price)
		}
		for _, value := range []string{"", "anthropic", "/claude-opus-5-5", "anthropic/"} {
			price := read(pdentries.Edit(reference, func(m map[string]any) { m["price_from"] = value })).Price
			if price.Billing != "unknown" || price.Reference != nil || price.Input != nil || price.Output != nil || price.CacheRead != nil ||
				len(price.CacheWrite) != 0 || len(price.Tiers) != 0 {
				t.Fatalf("price_from %q: %+v", value, price)
			}
		}
	})

	t.Run("a real client's item without lifetime_s", func(t *testing.T) {
		for name, edit := range map[string]func(map[string]any){
			"missing": func(item map[string]any) { delete(item, "lifetime_s") },
			"null":    func(item map[string]any) { item["lifetime_s"] = nil },
		} {
			entry := read(pdentries.Edit(perUse, func(m map[string]any) {
				// date-safe: a measurement's day inside a summary; nothing compares it with today.
				item := map[string]any{"retention_profile": "short", "basis": "measured", "date": "2026-11-20", "lifetime_s": 0,
					"client": "agent+cliproxyapi", "client_label": "x", "client_kind": "proxy", "parameter": nil, "value": nil,
					"checks": 3, "hits": 3, "first_miss_s": nil, "chosen": false}
				edit(item)
				lifetime := m["cache_lifetime"].(map[string]any)
				lifetime["lifetimes"] = append(lifetime["lifetimes"].([]any), item)
			}))
			short := entry.Cache.Short
			if short.Basis != base.Cache.Short.Basis || !equalInt(short.RelyOnS, base.Cache.Short.RelyOnS) ||
				len(short.ThroughProvimux) != len(base.Cache.Short.ThroughProvimux) {
				t.Fatalf("lifetime_s %s became a measurement: %+v", name, short)
			}
		}
	})

	// The fetch reading and the disk copy's reading agree: a chosen measurement with an empty client id names no
	// client, so it is left out of measured_with on a fetch, and the record passes the disk copy's reading (read checks
	// it) instead of being dropped after every start.
	t.Run("a chosen measurement with an empty client id", func(t *testing.T) {
		if base.Cache.Short.Safe.Basis != BasisMeasured {
			t.Fatalf("premise: the short profile's safe value is not measured: %+v", base.Cache.Short.Safe)
		}
		entry := read(pdentries.Edit(perUse, func(m map[string]any) {
			lifetime := m["cache_lifetime"].(map[string]any)
			for _, client := range []string{"", "provimux-test-client"} {
				// date-safe: a measurement's day inside a summary; nothing compares it with today.
				lifetime["lifetimes"] = append(lifetime["lifetimes"].([]any), map[string]any{"retention_profile": "short", "basis": "measured",
					"date": "2026-11-20", "lifetime_s": 300, "client": client, "client_label": nil, "client_kind": nil,
					"parameter": nil, "value": nil, "checks": 3, "hits": 3, "first_miss_s": nil, "chosen": true})
			}
		}))
		want := append([]FiguresClient{}, base.Cache.Short.Safe.MeasuredWith...)
		want = append(want, FiguresClient{Client: "provimux-test-client"})
		got := entry.Cache.Short.Safe.MeasuredWith
		if len(got) != len(want) {
			t.Fatalf("measured_with %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i].Client != want[i].Client {
				t.Fatalf("measured_with %+v, want %+v", got, want)
			}
		}
		dir := t.TempDir()
		doc := &FiguresDocument{Schema: FiguresSchema, Providers: strings.Join(FigureProviders(), ","), DatasetVersion: "ds-provimux-test",
			GeneratedAt: time.Unix(0, 0).UTC(), FetchedAt: time.Unix(0, 0).UTC(), Total: 1,
			Pages:   []FiguresPage{{Offset: 0, ETag: `W/"provimux-test"`, Count: 1, Refs: []string{EntryRef(entry.Provider, entry.Model)}}},
			Entries: map[string]Figures{EntryRef(entry.Provider, entry.Model): entry}}
		if err := NewStore(dir).WriteFigures(doc); err != nil {
			t.Fatal(err)
		}
		back, err := NewStore(dir).ReadFigures(time.Unix(0, 0).UTC())
		if err != nil || len(back.Entries) != 1 || back.Unread != 0 {
			t.Fatalf("the disk copy: %v, %d entries, %d unread", err, len(back.Entries), back.Unread)
		}
	})

	t.Run("a window or a tier too large to carry", func(t *testing.T) {
		entry := read(pdentries.Edit(perUse, func(m map[string]any) {
			safe := m["safe"].(map[string]any)
			safe["input_window"].(map[string]any)["value"] = 1e30
			safe["max_output"].(map[string]any)["value"] = float64(1 << 54)
			price := safe["price"].(map[string]any)
			for _, source := range price["sources"].([]any) {
				if source.(map[string]any)["source"] == price["source"] {
					source.(map[string]any)["tiers"] = []any{
						map[string]any{"input_tokens_above": 1e30, "input": 1, "output": 2, "cache_read": nil, "cache_write": nil},
						map[string]any{"input_tokens_above": 200000, "input": 8, "output": 30, "cache_read": nil, "cache_write": nil},
					}
				}
			}
		}))
		if entry.InputWindow.Tokens != nil || entry.InputWindow.AtLeast || entry.MaxOutput.Tokens != nil {
			t.Fatalf("a window too large was kept: %+v %+v", entry.InputWindow, entry.MaxOutput)
		}
		if len(entry.Price.Tiers) != 1 || entry.Price.Tiers[0].InputTokensAbove != 200000 {
			t.Fatalf("tiers %+v", entry.Price.Tiers)
		}
	})
}

// The disk copy's entries pass the reading fetched ones pass: an entry that does not (its cache and price
// missing, a basis or a billing no contract knows, a rely_on_s the rule does not give, a negative price, a
// reference of another form) is left out alone, counted as unread, and its page is asked again without its ETag.
// A copy of which no entry passes is ignored. What an interrupted write left is removed when the copy is read.
func TestFiguresDiskCopyEntriesPassTheFetchedReading(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	server := newFiguresStandIn(t, figuresVolume(t, 450), fixture.Version, fixture.Generated)
	result, err := fetchFiguresFrom(server, nil)
	if err != nil {
		t.Fatal(err)
	}
	for ref, entry := range result.Document.Entries {
		if errCheck := entry.check(); errCheck != nil {
			t.Fatalf("a fetched record does not pass the disk copy's reading: %s: %v", ref, errCheck)
		}
	}
	whole := asJSONValue(t, result.Document).(map[string]any)
	pages := whole["pages"].([]any)
	refOn := func(page int) string { return pages[page].(map[string]any)["refs"].([]any)[0].(string) }
	referenceRef := "claude-subscription/claude-opus-5-5"
	pageOf := map[string]int{}
	for i, page := range result.Document.Pages {
		for _, ref := range page.Refs {
			pageOf[ref] = i
		}
	}
	cases := []struct {
		name string
		ref  string
		edit func(entry map[string]any)
	}{
		{"cache and price missing", refOn(0), func(e map[string]any) { delete(e, "cache"); delete(e, "price") }},
		{"a basis no contract knows", refOn(1), func(e map[string]any) {
			e["cache"].(map[string]any)["short"].(map[string]any)["basis"] = "guessed"
		}},
		{"a billing no contract knows", refOn(2), func(e map[string]any) { e["price"].(map[string]any)["billing"] = "free" }},
		{"rely_on_s written by hand", refOn(1), func(e map[string]any) {
			short := e["cache"].(map[string]any)["short"].(map[string]any)
			short["rely_on_s"], short["basis"] = 99999, BasisMeasuredThroughProvimux
		}},
		{"a negative price", refOn(2), func(e map[string]any) { e["price"].(map[string]any)["input"] = -1 }},
		{"a reference of another form", referenceRef, func(e map[string]any) { e["price"].(map[string]any)["reference"] = "anthropic" }},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, FiguresFileName)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := asJSONValue(t, result.Document).(map[string]any)
			c.edit(doc["entries"].(map[string]any)[c.ref].(map[string]any))
			data, _ := json.Marshal(doc)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			store := NewStore(dir)
			back, errRead := store.ReadFigures(figuresTestNow)
			if errRead != nil || len(back.Entries) != 449 || back.Unread != result.Document.Unread+1 || !store.warned["entries:"+FiguresFileName] {
				t.Fatalf("read: %v", errRead)
			}
			if _, kept := back.Entries[c.ref]; kept {
				t.Fatalf("%s was held", c.ref)
			}
			for i, page := range back.Pages {
				if (page.ETag == "") != (i == pageOf[c.ref]) {
					t.Fatalf("page %d ETag %q", i, page.ETag)
				}
			}
			// The next fetch asks that page anew and holds the entry again.
			before := len(server.Requests())
			again, errFetch := fetchFiguresFrom(server, back)
			if errFetch != nil || again.NotModified || len(again.Document.Entries) != 450 {
				t.Fatalf("the next fetch: %+v %v", again, errFetch)
			}
			for i, req := range server.Requests()[before:] {
				if (req.Header.Get("If-None-Match") == "") != (i == pageOf[c.ref]) {
					t.Fatalf("page %d asked with If-None-Match %q", i, req.Header.Get("If-None-Match"))
				}
			}
		})
	}

	t.Run("no entry passes", func(t *testing.T) {
		doc := asJSONValue(t, result.Document).(map[string]any)
		for _, entry := range doc["entries"].(map[string]any) {
			entry.(map[string]any)["access"] = "teleport"
		}
		data, _ := json.Marshal(doc)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(dir)
		if back, errRead := store.ReadFigures(figuresTestNow); !errors.Is(errRead, ErrNoEntries) || back != nil || !store.warned["bad:"+FiguresFileName] {
			t.Fatalf("held: %v", errRead)
		}
	})

	t.Run("leftovers of an interrupted write", func(t *testing.T) {
		if err := NewStore(dir).WriteFigures(result.Document); err != nil {
			t.Fatal(err)
		}
		leftovers := []string{".figures.json.123.tmp", ".figures.json.abc.tmp"}
		for _, name := range append(leftovers, ".published.json.7.tmp") {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"schema":`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if back, errRead := NewStore(dir).ReadFigures(figuresTestNow); errRead != nil || len(back.Entries) != 450 {
			t.Fatalf("read: %v", errRead)
		}
		for _, name := range leftovers {
			if _, errStat := os.Stat(filepath.Join(dir, name)); !errors.Is(errStat, os.ErrNotExist) {
				t.Fatalf("%s is still there: %v", name, errStat)
			}
		}
		if _, errStat := os.Stat(filepath.Join(dir, ".published.json.7.tmp")); errStat != nil {
			t.Fatalf("another file's leftover was removed: %v", errStat)
		}
	})
}

// A page of exactly 8 MB is read; one of 8 MB and one byte is refused. Both are whole JSON (the padding is
// whitespace after it), so the size check decides and not the decoder.
func TestFiguresPageOfEightMegabytes(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	page := figuresEnvelope(t, fixture.Version, fixture.Generated, fixture.Entries)
	for _, size := range []int{maxFiguresPageBytes, maxFiguresPageBytes + 1} {
		body := append(append([]byte(nil), page...), bytes.Repeat([]byte(" "), size-len(page))...)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		result, err := FetchFigures(context.Background(), server.Client(), server.URL+pdentries.Path, nil, "provimux/test", figuresTestNow, 10*time.Second, nil)
		server.Close()
		if size == maxFiguresPageBytes {
			if err != nil || len(result.Document.Entries) != len(fixture.Entries) {
				t.Fatalf("exactly 8 MB: %v", err)
			}
			continue
		}
		figuresRefusal(t, err, FailureResponse, 0)
		if result.Document != nil || !strings.Contains(err.Error(), "larger than 8 MB") {
			t.Fatalf("8 MB and one byte: %v", err)
		}
	}
}

// The same entry on two pages is refused, also when every count adds up.
func TestFiguresSameEntryOnTwoPages(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	server := newFiguresStandIn(t, figuresVolume(t, 450), fixture.Version, fixture.Generated)
	var first any
	server.SetPage(func(offset int, body []byte) (int, []byte) {
		var page map[string]any
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, nil
		}
		data := page["data"].([]any)
		if offset == 0 {
			first = data[0]
			return 0, nil
		}
		data[len(data)-1] = first // page 1 ends with page 0's first entry
		out, _ := json.Marshal(page)
		return http.StatusOK, out
	})
	result, err := fetchFiguresFrom(server, nil)
	figuresRefusal(t, err, FailureResponse, 0)
	if result.Document != nil || !strings.Contains(err.Error(), "on two pages") || len(server.Requests()) != 2 {
		t.Fatalf("%v after %d requests", err, len(server.Requests()))
	}
}

// The same entry on a page that answers 200 and on a held page that answers 304 is refused too: the 304 page's
// entries are the held ones, and one of them is already on this fetch's earlier page.
func TestFiguresSameEntryOnANewPageAndAHeldOne(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	server := newFiguresStandIn(t, figuresVolume(t, 450), fixture.Version, fixture.Generated)
	var second any
	server.SetPage(func(offset int, body []byte) (int, []byte) {
		if offset == FiguresPageLimit && second == nil {
			var page map[string]any
			if json.Unmarshal(body, &page) == nil {
				second = page["data"].([]any)[0] // page 1's first entry, as held
			}
		}
		return 0, nil
	})
	seed, err := fetchFiguresFrom(server, nil)
	if err != nil || len(seed.Document.Entries) != 450 || second == nil {
		t.Fatalf("seed: %v", err)
	}
	server.SetPage(func(offset int, body []byte) (int, []byte) {
		if offset != 0 {
			return 0, nil // pages 1 and 2 are as held: 304
		}
		var page map[string]any
		if err := json.Unmarshal(body, &page); err != nil {
			return 0, nil
		}
		data := page["data"].([]any)
		data[len(data)-1] = second // page 0 changed: it ends with page 1's first entry
		out, _ := json.Marshal(page)
		return http.StatusOK, out
	})
	before := len(server.Requests())
	result, err := fetchFiguresFrom(server, seed.Document)
	figuresRefusal(t, err, FailureResponse, 0)
	if result.Document != nil || !strings.Contains(err.Error(), "on two pages") || len(server.Requests())-before != 2 {
		t.Fatalf("%v after %d requests", err, len(server.Requests())-before)
	}
	if len(seed.Document.Entries) != 450 {
		t.Fatal("the held document was changed")
	}
}
