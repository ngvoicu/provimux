package modellists

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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
// client's item without lifetime_s (no measurement), a window or a tier too large to carry.
func TestFiguresValuesOnlyABrokenSummaryCarriesAreLeftOut(t *testing.T) {
	_, byRef := loadFiguresFixture(t)
	perUse, reference := byRef["anthropic/claude-opus-5-5"], byRef["claude-subscription/claude-opus-5-5"]
	read := func(raw json.RawMessage) Figures {
		t.Helper()
		entry, err := ReadEntrySummary(raw)
		if err != nil {
			t.Fatalf("the entry was left out: %v", err)
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
			if short.Basis != base.Cache.Short.Basis || !reflect.DeepEqual(short.RelyOnS, base.Cache.Short.RelyOnS) ||
				len(short.ThroughProvimux) != len(base.Cache.Short.ThroughProvimux) {
				t.Fatalf("lifetime_s %s became a measurement: %+v", name, short)
			}
		}
	})

	// A chosen measurement with an empty client id names no client, so it is left out of measured_with, and the record
	// is read back from the disk copy as it was written.
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
