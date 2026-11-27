package modellists

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists/pdentries"
)

// Step F2 (prices-and-cache.md 4 and 6.2): the entries request, the kept record and the rule of 4.2. Every
// fetch goes to pdentries, a stand-in of providers.dev's API on loopback; none reaches the real site.

// figuresFixture is testdata/figures-summaries.json: real entry summaries of providers.dev's dataset
// c37885999c67cfaa, the dataset the design's examples were built from.
type figuresFixture struct {
	Version   string            `json:"dataset_version"`
	Generated string            `json:"generated_at"`
	Entries   []json.RawMessage `json:"entries"`
}

func loadFiguresFixture(t *testing.T) (figuresFixture, map[string]json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "figures-summaries.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture figuresFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	byRef := map[string]json.RawMessage{}
	for _, raw := range fixture.Entries {
		var head struct {
			Ref string `json:"ref"`
		}
		if err = json.Unmarshal(raw, &head); err != nil {
			t.Fatal(err)
		}
		byRef[head.Ref] = raw
	}
	return fixture, byRef
}

func readFixtureEntry(t *testing.T, byRef map[string]json.RawMessage, ref string) Figures {
	t.Helper()
	raw, ok := byRef[ref]
	if !ok {
		t.Fatalf("fixture has no %s", ref)
	}
	entry, err := ReadEntrySummary(raw)
	if err != nil {
		t.Fatalf("%s: %v", ref, err)
	}
	return entry
}

// figuresVolume is the fixture plus copies, n entries over the fixed set's providers: three pages of 200
// for 450.
func figuresVolume(t *testing.T, n int) []json.RawMessage {
	t.Helper()
	fixture, _ := loadFiguresFixture(t)
	out := append([]json.RawMessage(nil), fixture.Entries...)
	for i := 0; len(out) < n; i++ {
		out = append(out, pdentries.Copy(fixture.Entries[i%len(fixture.Entries)], fmt.Sprintf("provimux-test-copy-%03d", i)))
	}
	return out
}

func newFiguresStandIn(t *testing.T, entries []json.RawMessage, version, generated string) *pdentries.Server {
	t.Helper()
	server := pdentries.New(entries, version, generated)
	t.Cleanup(server.Close)
	return server
}

// date-safe: the clock the fetch is given; nothing compares it with today.
var figuresTestNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func fetchFiguresFrom(server *pdentries.Server, held *FiguresDocument) (FiguresResult, error) {
	return FetchFigures(context.Background(), server.Client(), server.EntriesURL(), held, "provimux/test", figuresTestNow, 5*time.Second)
}

func asJSONValue(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err = json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func ptrText(v *int) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(*v)
}

// The request is the same for every user: the fixed set, a page of 200, an offset, and nothing else.
func TestFiguresRequestIsTheSameForEveryUser(t *testing.T) {
	want := []string{"anthropic", "antigravity", "claude-subscription", "devin", "github-copilot", "google", "google-vertex",
		"meta-subscription", "openai", "openai-codex", "openrouter", "xai"}
	if got := FigureProviders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed set = %v", got)
	}
	for _, offset := range []int{0, 200, 400} {
		raw := FiguresRequestURL(EntriesURL, offset)
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		query := u.Query()
		if len(query) != 3 || query.Get("provider") != strings.Join(want, ",") || query.Get("limit") != "200" ||
			query.Get("offset") != fmt.Sprint(offset) || u.Scheme != "https" || u.Host != "providers.dev" || u.Path != "/api/v1/entries" {
			t.Fatalf("request %s", raw)
		}
	}
}

// The design's example (examples/figures.json) is what the rules of 4.1 and 4.2 give for the four entries'
// summaries of the same dataset: every key and value of each record.
func TestFiguresExampleRecordsFromTheirSummaries(t *testing.T) {
	fixture, byRef := loadFiguresFixture(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "provimux", "design", "prices-and-cache", "examples", "figures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Figures struct {
			DatasetVersion string                     `json:"dataset_version"`
			Entries        map[string]json.RawMessage `json:"entries"`
		} `json:"figures"`
	}
	if err = json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	if example.Figures.DatasetVersion != fixture.Version || len(example.Figures.Entries) != 4 {
		t.Fatalf("example of dataset %s with %d entries; fixture %s", example.Figures.DatasetVersion, len(example.Figures.Entries), fixture.Version)
	}
	for ref, raw := range example.Figures.Entries {
		var want any
		if err = json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		got := asJSONValue(t, readFixtureEntry(t, byRef, ref))
		if !reflect.DeepEqual(got, want) {
			gotJSON, _ := json.Marshal(got)
			t.Errorf("%s:\n got %s\nwant %s", ref, gotJSON, raw)
		}
	}
}

// The rule of 4.2, row by row, on real entries (the six of finding 2 and the table "What the rule gives
// today"), with the plain probe never giving row 1 and always listed with counts false.
func TestFiguresRelyOnRowByRowFromFixtures(t *testing.T) {
	_, byRef := loadFiguresFixture(t)
	type want struct {
		ref, profile string
		seconds      *int
		basis        string
		date         string
		safe         int // providers.dev's safe value beside it; -1: none
	}
	n := func(v int) *int { return &v }
	// date-safe: measurement dates of the fixture, compared with the fixture's record, never with a clock.
	cases := []want{
		// Row 1: a real client through Provimux, also above the safe value, and the lowest where several measured.
		{"openai-codex/gpt-6.1-sol", "short", n(1800), BasisMeasuredThroughProvimux, "2026-11-07", 450},
		{"openai-codex/gpt-6.1-terra", "short", n(900), BasisMeasuredThroughProvimux, "2026-11-07", 1500},
		{"openai-codex/gpt-6-astra", "short", n(7200), BasisMeasuredThroughProvimux, "2026-11-11", 300},
		{"devin/glm-5-3", "short", n(300), BasisMeasuredThroughProvimux, "2026-11-10", 600},
		{"meta-subscription/muse-wonder-1.0", "short", n(7200), BasisMeasuredThroughProvimux, "2026-11-07", 0},
		{"meta-subscription/muse-ember-1.0", "long", n(14400), BasisMeasuredThroughProvimux, "2026-11-10", 7200},
		// 0 is a value where a real client measured it.
		{"devin/grok-4-7", "short", n(0), BasisMeasuredThroughProvimux, "2026-11-10", 150},
		// Row 2: the plain probe's 0 alone never gives row 1; the safe value does.
		{"openai-codex/gpt-6.2-sol", "short", n(150), BasisMeasured, "2026-11-24", 150},
		{"openai-codex/gpt-6.2-astra", "short", n(1200), BasisMeasured, "2026-11-23", 1200},
		{"devin/kimi-k3", "short", n(3000), BasisMeasured, "2026-11-23", 3000},
		{"anthropic/claude-opus-5-5", "long", n(3600), BasisMeasured, "2026-11-17", 3600},
		// Row 2 where the safe value is itself the probe's (decision 1b): kept, and named.
		{"claude-subscription/claude-opus-5-5", "short", n(3600), BasisMeasured, "2026-11-16", 3600},
		{"claude-subscription/claude-opus-5-5", "long", n(3600), BasisMeasured, "2026-11-16", 3600},
		// Row 4: nothing measured, nothing documented.
		{"openai-codex/gpt-6.2-sol", "long", nil, BasisNone, "", -1},
	}
	for _, tc := range cases {
		entry := readFixtureEntry(t, byRef, tc.ref)
		profile := entry.Cache.Short
		if tc.profile == "long" {
			profile = entry.Cache.Long
		}
		name := tc.ref + " " + tc.profile
		if !reflect.DeepEqual(profile.RelyOnS, tc.seconds) || profile.Basis != tc.basis {
			t.Errorf("%s: rely_on_s %s basis %s, want %s %s", name, ptrText(profile.RelyOnS), profile.Basis, ptrText(tc.seconds), tc.basis)
		}
		if (tc.date == "" && profile.Date != nil) || (tc.date != "" && (profile.Date == nil || *profile.Date != tc.date)) {
			t.Errorf("%s: date %v, want %q", name, profile.Date, tc.date)
		}
		if (tc.safe < 0 && profile.Safe.Seconds != nil) || (tc.safe >= 0 && (profile.Safe.Seconds == nil || *profile.Safe.Seconds != tc.safe)) {
			t.Errorf("%s: safe %s, want %d", name, ptrText(profile.Safe.Seconds), tc.safe)
		}
		for _, m := range profile.ThroughProvimux {
			if m.Counts != (m.Client == "agent+cliproxyapi") {
				t.Errorf("%s: %s counts=%v", name, m.Client, m.Counts)
			}
		}
	}
	// Every probe item is in the answer, marked: gpt-6.2-sol's 0, and Claude's 3600 that row 2 rests on.
	probe := readFixtureEntry(t, byRef, "openai-codex/gpt-6.2-sol").Cache.Short.ThroughProvimux
	if len(probe) != 1 || probe[0].Client != "cliproxyapi" || probe[0].Counts || probe[0].LifetimeS != 0 || probe[0].Checks != 10 || probe[0].Hits != 3 {
		t.Fatalf("gpt-6.2-sol probe items = %+v", probe)
	}
	claude := readFixtureEntry(t, byRef, "claude-subscription/claude-opus-5-5").Cache.Short
	if len(claude.Safe.MeasuredWith) != 1 || claude.Safe.MeasuredWith[0].Client != "cliproxyapi" || len(claude.ThroughProvimux) != 1 || claude.ThroughProvimux[0].Counts {
		t.Fatalf("claude-subscription short = %+v", claude)
	}
	// Newest first: the probe's measurement before the agent's older one.
	sol := readFixtureEntry(t, byRef, "openai-codex/gpt-6.1-sol").Cache.Short.ThroughProvimux
	if len(sol) != 2 || sol[0].Client != "cliproxyapi" || sol[1].Client != "agent+cliproxyapi" {
		t.Fatalf("gpt-6.1-sol order = %+v", sol)
	}
}

// The rule itself, on synthetic measurements: row by row, the lowest of two real clients, of equal ones the
// newest, and the probe never in row 1.
func TestFiguresRelyOnRule(t *testing.T) {
	n := func(v int) *int { return &v }
	d := func(v string) *string { return &v }
	agent := func(seconds int, date string) MeasuredLifetime {
		return MeasuredLifetime{Client: "agent+cliproxyapi", Counts: CountsForRelyOn("agent+cliproxyapi"), LifetimeS: seconds, Date: d(date)}
	}
	probe := func(seconds int, date string) MeasuredLifetime {
		return MeasuredLifetime{Client: "cliproxyapi", Counts: CountsForRelyOn("cliproxyapi"), LifetimeS: seconds, Date: d(date)}
	}
	// date-safe: dates of synthetic measurements, compared only with each other.
	cases := []struct {
		name      string
		safe      *int
		safeBasis string
		measured  []MeasuredLifetime
		seconds   *int
		basis     string
		date      string
	}{
		{"row 1: the lowest of two real clients", n(300), "measured", []MeasuredLifetime{agent(1800, "2026-11-07"), agent(900, "2026-11-01")}, n(900), BasisMeasuredThroughProvimux, "2026-11-01"},
		{"row 1: of equal ones the newest", n(300), "measured", []MeasuredLifetime{agent(900, "2026-11-01"), agent(900, "2026-11-09"), agent(900, "2026-11-03")}, n(900), BasisMeasuredThroughProvimux, "2026-11-09"},
		{"row 1: above the safe value", n(300), "measured", []MeasuredLifetime{agent(7200, "2026-11-11")}, n(7200), BasisMeasuredThroughProvimux, "2026-11-11"},
		{"row 1: 0 is a value", n(300), "measured", []MeasuredLifetime{agent(0, "2026-11-10")}, n(0), BasisMeasuredThroughProvimux, "2026-11-10"},
		{"row 1: the probe's lower value is not counted", n(3000), "measured", []MeasuredLifetime{probe(0, "2026-11-04"), agent(600, "2026-11-10")}, n(600), BasisMeasuredThroughProvimux, "2026-11-10"},
		{"row 2: the probe alone", n(150), "measured", []MeasuredLifetime{probe(0, "2026-11-17")}, n(150), BasisMeasured, "2026-11-24"},
		{"row 3: documented", n(300), "documented", []MeasuredLifetime{probe(0, "2026-11-17")}, n(300), BasisDocumented, "2026-11-24"},
		{"row 4: none", nil, "none", []MeasuredLifetime{probe(600, "2026-11-17")}, nil, BasisNone, ""},
		{"row 4: a basis without a value", nil, "measured", nil, nil, BasisNone, ""},
	}
	for _, tc := range cases {
		// date-safe: the safe value's date of the synthetic case.
		seconds, basis, date := RelyOn(tc.safe, tc.safeBasis, d("2026-11-24"), tc.measured)
		if !reflect.DeepEqual(seconds, tc.seconds) || basis != tc.basis || (tc.date == "") != (date == nil) || (date != nil && *date != tc.date) {
			t.Errorf("%s: got %s %s %v", tc.name, ptrText(seconds), basis, date)
		}
	}
	if CountsForRelyOn("cliproxyapi") || CountsForRelyOn("direct") || CountsForRelyOn("codex-cli") || !CountsForRelyOn("agent+cliproxyapi") {
		t.Fatal("only agent+cliproxyapi counts")
	}
}

// An entry with no rules, a rule the reader does not know, a rule it cannot read, a cache write not keyed
// by lifetime, a price in another unit, billing that applies no price, and tiers.
func TestFiguresPriceRules(t *testing.T) {
	_, byRef := loadFiguresFixture(t)
	base := byRef["anthropic/claude-opus-5-5"]
	noRules, err := ReadEntrySummary(pdentries.Edit(base, func(e map[string]any) { e["pricing"].(map[string]any)["rules"] = nil }))
	if err != nil {
		t.Fatal(err)
	}
	if noRules.Price.CacheRead != nil || len(noRules.Price.CacheWrite) != 0 || noRules.Price.OtherRates || noRules.Price.Input == nil || *noRules.Price.Input != 4 {
		t.Fatalf("no rules: %+v", noRules.Price)
	}
	unknown, err := ReadEntrySummary(pdentries.Edit(base, func(e map[string]any) {
		e["pricing"].(map[string]any)["rules"] = []any{
			map[string]any{"id": "base-write", "when": nil, "meter": "text.cache_write", "operation": "set", "amount": 3.75, "currency": "USD",
				"unit": map[string]any{"name": "token", "quantity": 1000000}},
			map[string]any{"id": "storage", "when": map[string]any{"kind": "unknown", "raw": "per hour"}, "meter": "text.cache_storage",
				"operation": "accrue", "amount": 1, "currency": "USD", "unit": map[string]any{"name": "token_hour", "quantity": 1000000}},
			map[string]any{"id": "unreadable", "when": nil, "meter": "text.input", "operation": "set", "amount": "four"},
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown.Price.CacheWrite) != 1 || unknown.Price.CacheWrite[0].LifetimeS != nil || unknown.Price.CacheWrite[0].Price != 3.75 || !unknown.Price.OtherRates {
		t.Fatalf("unknown rules: %+v", unknown.Price)
	}
	otherUnit, err := ReadEntrySummary(pdentries.Edit(base, func(e map[string]any) {
		e["pricing"].(map[string]any)["rules"] = []any{map[string]any{"id": "r", "when": nil, "meter": "text.cache_read", "operation": "set",
			"amount": 0.2, "currency": "EUR", "unit": map[string]any{"name": "token", "quantity": 1000000}}}
	}))
	if err != nil || otherUnit.Price.CacheRead != nil {
		t.Fatalf("a cache read price in euros is not a US dollar price: %+v %v", otherUnit.Price, err)
	}
	notApplicable, err := ReadEntrySummary(pdentries.Edit(base, func(e map[string]any) { e["pricing"].(map[string]any)["billing"] = "not_applicable" }))
	if err != nil || notApplicable.Price.Billing != "unknown" || notApplicable.Price.Input != nil || notApplicable.Price.Output != nil ||
		notApplicable.Price.CacheRead != nil || len(notApplicable.Price.CacheWrite) != 0 || len(notApplicable.Price.Tiers) != 0 {
		t.Fatalf("billing not_applicable: every price is null: %+v %v", notApplicable.Price, err)
	}
	// Rules a tier carries are no other rate; the tier's cache writes by lifetime are not in the tier, so
	// they are; a multiplier is.
	without := func(drop func(rule map[string]any) bool) Figures {
		entry, errRead := ReadEntrySummary(pdentries.Edit(byRef["openai-codex/gpt-6.1-sol"], func(e map[string]any) {
			pricing := e["pricing"].(map[string]any)
			var kept []any
			for _, rule := range pricing["rules"].([]any) {
				if !drop(rule.(map[string]any)) {
					kept = append(kept, rule)
				}
			}
			pricing["rules"] = kept
		}))
		if errRead != nil {
			t.Fatal(errRead)
		}
		return entry
	}
	multiplier := func(rule map[string]any) bool { return rule["operation"] == "multiply" }
	tierWrite := func(rule map[string]any) bool {
		when, _ := rule["when"].(map[string]any)
		return rule["meter"] == "text.cache_write" && when != nil && when["kind"] == "all"
	}
	if entry := without(multiplier); len(entry.Price.Tiers) != 1 || entry.Price.Tiers[0].InputTokensAbove != 272000 || !entry.Price.OtherRates {
		t.Fatalf("tier cache writes by lifetime: %+v", entry.Price)
	}
	if entry := without(func(rule map[string]any) bool { return multiplier(rule) || tierWrite(rule) }); entry.Price.OtherRates {
		t.Fatalf("rules the tiers carry are no other rate: %+v", entry.Price)
	}
}

// An entry Provimux cannot read, or of a provider it did not ask for, is left out alone.
func TestFiguresUnreadableEntryLeftOutAlone(t *testing.T) {
	fixture, byRef := loadFiguresFixture(t)
	bad := map[string]json.RawMessage{
		"a price that is not a number":     pdentries.Edit(byRef["openai-codex/gpt-6.2-sol"], func(e map[string]any) { e["price_in"] = "two" }),
		"a ref that is not provider/model": pdentries.Edit(byRef["devin/kimi-k3"], func(e map[string]any) { e["ref"] = "devin/other" }),
		"no cache_lifetime":                pdentries.Edit(byRef["devin/glm-5-3"], func(e map[string]any) { delete(e, "cache_lifetime") }),
		"a provider not asked for": pdentries.Copy(pdentries.Edit(byRef["devin/grok-4-7"], func(e map[string]any) { e["provider"] = "kimi-for-coding" }),
			"grok-4-7"),
		"not an object": json.RawMessage(`"openai/gpt-x"`),
	}
	skip := map[string]bool{"openai-codex/gpt-6.2-sol": true, "devin/kimi-k3": true, "devin/glm-5-3": true, "devin/grok-4-7": true}
	var entries []json.RawMessage
	for ref, raw := range byRef {
		if !skip[ref] {
			entries = append(entries, raw)
		}
	}
	for _, raw := range bad {
		entries = append(entries, raw)
	}
	page, _ := json.Marshal(map[string]any{"meta": map[string]any{"dataset_version": fixture.Version, "generated_at": fixture.Generated},
		"total": len(entries), "limit": 200, "offset": 0, "next_offset": nil, "data": entries})
	parsed, err := parseFiguresPage(page, 0)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.count != len(entries) || len(parsed.entries) != len(entries)-len(bad) {
		t.Fatalf("count %d kept %d of %d", parsed.count, len(parsed.entries), len(entries))
	}
	for _, entry := range parsed.entries {
		if skip[EntryRef(entry.Provider, entry.Model)] || entry.Provider == "kimi-for-coding" {
			t.Fatalf("an entry that cannot be read was kept: %s", EntryRef(entry.Provider, entry.Model))
		}
	}
	// Through a fetch: the page is accepted, the count of what was left out is kept. The stand-in, as the real
	// API, never sends the entry of another provider or one without a provider: three are left out.
	server := newFiguresStandIn(t, entries, fixture.Version, fixture.Generated)
	result, err := fetchFiguresFrom(server, nil)
	if err != nil || len(result.Document.Entries) != len(entries)-len(bad) || result.Document.Unread != 3 || result.Document.Total != len(entries)-2 {
		t.Fatalf("fetch: %+v %v", result.Document, err)
	}
}

// A first fetch of three pages; then every page answers 304; then a new dataset replaces the document.
func TestFiguresFetchPagesAnd304(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	entries := figuresVolume(t, 450)
	server := newFiguresStandIn(t, entries, fixture.Version, fixture.Generated)
	result, err := fetchFiguresFrom(server, nil)
	if err != nil || result.NotModified || result.Document == nil {
		t.Fatalf("first fetch: %+v %v", result, err)
	}
	doc := result.Document
	if len(doc.Entries) != 450 || doc.Total != 450 || len(doc.Pages) != 3 || doc.DatasetVersion != fixture.Version || doc.Unread != 0 || !doc.FetchedAt.Equal(figuresTestNow) {
		t.Fatalf("document: %d entries, total %d, %d pages, %s", len(doc.Entries), doc.Total, len(doc.Pages), doc.DatasetVersion)
	}
	for i, page := range doc.Pages {
		if page.Offset != 200*i || page.ETag == "" || len(page.Refs) != page.Count {
			t.Fatalf("page %d: %+v", i, page)
		}
	}
	requests := server.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d", len(requests))
	}
	for i, req := range requests {
		checkFiguresRequest(t, req, 200*i, "")
	}

	// The same dataset: every page conditional on its own ETag, every page 304, the held document stays.
	again, err := fetchFiguresFrom(server, doc)
	if err != nil || !again.NotModified {
		t.Fatalf("second fetch: %+v %v", again, err)
	}
	requests = server.Requests()[3:]
	if len(requests) != 3 {
		t.Fatalf("conditional requests = %d", len(requests))
	}
	for i, req := range requests {
		checkFiguresRequest(t, req, 200*i, doc.Pages[i].ETag)
	}

	// A new publication: 200s, a new document.
	// date-safe: a fixture's generated_at, compared only with other documents'.
	server.SetDataset(entries[:401], "ds-next", "2026-11-27T04:00:00Z")
	next, err := fetchFiguresFrom(server, doc)
	if err != nil || next.NotModified || next.Document == nil || len(next.Document.Entries) != 401 || next.Document.DatasetVersion != "ds-next" {
		t.Fatalf("new dataset: %+v %v", next, err)
	}
	if accepted, errAccept := AcceptFigures(doc, next.Document); errAccept != nil || accepted != next.Document {
		t.Fatalf("a newer document replaces the held one: %v", errAccept)
	}
}

func checkFiguresRequest(t *testing.T, req *http.Request, offset int, ifNoneMatch string) {
	t.Helper()
	query := req.URL.Query()
	if req.Method != http.MethodGet || len(query) != 3 || query.Get("provider") != strings.Join(FigureProviders(), ",") ||
		query.Get("limit") != "200" || query.Get("offset") != fmt.Sprint(offset) {
		t.Fatalf("request %s %s", req.Method, req.URL)
	}
	for name := range req.Header {
		// Accept-Encoding is Go's transport's own.
		if name != "User-Agent" && name != "If-None-Match" && name != "Accept-Encoding" {
			t.Fatalf("request sends header %s", name)
		}
	}
	if req.Header.Get("User-Agent") != "provimux/test" || req.Header.Get("If-None-Match") != ifNoneMatch {
		t.Fatalf("headers %v, want If-None-Match %q", req.Header, ifNoneMatch)
	}
}

// Every way a fetch fails leaves a code and no document: a 500, a dataset that changes between pages, a
// short page count, too many pages, an oversized page, a malformed page, a cut-off body, a 304 without a
// held ETag, unreachable, and a page that never answers.
func TestFiguresFetchFailures(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	entries := figuresVolume(t, 450)
	pageOf := func(body []byte, edit func(map[string]any)) []byte {
		var page map[string]any
		_ = json.Unmarshal(body, &page)
		edit(page)
		out, _ := json.Marshal(page)
		return out
	}
	at := func(offset int, status int, edit func(body []byte) []byte) func(int, []byte) (int, []byte) {
		return func(o int, body []byte) (int, []byte) {
			if o != offset {
				return 0, nil
			}
			return status, edit(body)
		}
	}
	cases := []struct {
		name     string
		page     func(offset int, body []byte) (int, []byte)
		category string
		status   int
	}{
		{"500 on the second page", at(200, http.StatusInternalServerError, func([]byte) []byte { return []byte(`{"error":{"code":"internal"}}`) }),
			FailureHTTP, 500},
		{"a dataset that changes between pages", at(400, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) {
				// date-safe: a fixture's generated_at, compared only with the other pages'.
				p["meta"] = map[string]any{"dataset_version": "ds-other", "generated_at": "2026-11-26T13:00:00Z", "upstream_fetched_at": "2026-11-26T13:00:00Z"}
			})
		}), FailureResponse, 0},
		{"a short page count: the last page never asked", at(200, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) { p["next_offset"] = nil })
		}), FailureResponse, 0},
		{"a short page: fewer entries than total says", at(400, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) { p["data"] = p["data"].([]any)[:10] })
		}), FailureResponse, 0},
		{"another total on one page", at(200, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) { p["total"] = 451 })
		}), FailureResponse, 0},
		{"next_offset that does not move on", at(200, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) { p["next_offset"] = 0 })
		}), FailureResponse, 0},
		{"an oversized page", at(0, http.StatusOK, func(body []byte) []byte {
			return append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"padding":"`+strings.Repeat("x", maxFiguresPageBytes)+`"}`)...)
		}), FailureResponse, 0},
		{"a malformed page", at(200, http.StatusOK, func([]byte) []byte { return []byte(`<html>maintenance</html>`) }), FailureResponse, 0},
		{"a page without its envelope", at(0, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) { delete(p, "meta") })
		}), FailureResponse, 0},
		{"a page of another offset", at(200, http.StatusOK, func(body []byte) []byte {
			return pageOf(body, func(p map[string]any) { p["offset"] = 0 })
		}), FailureResponse, 0},
		{"a body cut off at the half", at(0, http.StatusOK, func(body []byte) []byte { return body[:len(body)/2] }), FailureResponse, 0},
		{"a 304 with no ETag held", func(int, []byte) (int, []byte) { return http.StatusNotModified, nil }, FailureHTTP, 304},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := newFiguresStandIn(t, entries, fixture.Version, fixture.Generated)
			server.SetPage(c.page)
			result, err := fetchFiguresFrom(server, nil)
			var fetchErr *FetchError
			if err == nil || !errors.As(err, &fetchErr) || fetchErr.Category != c.category || fetchErr.Status != c.status || result.Document != nil {
				t.Fatalf("result %+v err %v", result, err)
			}
		})
	}

	t.Run("more than 10 pages", func(t *testing.T) {
		server := newFiguresStandIn(t, entries, fixture.Version, fixture.Generated)
		server.SetPage(func(offset int, body []byte) (int, []byte) {
			// A server that gives 20 a page whatever is asked: 23 pages.
			return http.StatusOK, pageOf(body, func(p map[string]any) {
				p["data"] = p["data"].([]any)[:20]
				p["next_offset"] = offset + 20
			})
		})
		_, err := fetchFiguresFrom(server, nil)
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) || fetchErr.Category != FailureResponse || len(server.Requests()) != FiguresMaxPages {
			t.Fatalf("err %v after %d requests", err, len(server.Requests()))
		}
	})

	t.Run("a body cut off in transfer", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100000")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"meta":`))
		}))
		t.Cleanup(server.Close)
		_, err := FetchFigures(context.Background(), server.Client(), server.URL, nil, "provimux/test", figuresTestNow, 5*time.Second)
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) || fetchErr.Category != FailureNetwork {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		closed.Close()
		_, err := FetchFigures(context.Background(), nil, closed.URL, nil, "provimux/test", figuresTestNow, 5*time.Second)
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) || fetchErr.Category != FailureNetwork {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("a page that never answers", func(t *testing.T) {
		server := newFiguresStandIn(t, entries, fixture.Version, fixture.Generated)
		hang := make(chan struct{})
		server.SetHang(hang)
		t.Cleanup(func() { close(hang) })
		started := time.Now()
		_, err := FetchFigures(context.Background(), server.Client(), server.EntriesURL(), nil, "provimux/test", figuresTestNow, 100*time.Millisecond)
		var fetchErr *FetchError
		if !errors.As(err, &fetchErr) || fetchErr.Category != FailureNetwork || time.Since(started) > 5*time.Second {
			t.Fatalf("err %v after %s", err, time.Since(started))
		}
	})
}

// A page that answers 304 while another answers 200 of the same dataset: one document, the 304 page's
// entries taken from the held one.
func TestFiguresFetchMixed304And200(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	entries := figuresVolume(t, 450)
	server := newFiguresStandIn(t, entries, fixture.Version, fixture.Generated)
	first, err := fetchFiguresFrom(server, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The middle page is served with another ETag (the same data): a 200 there, 304 on the others.
	server.SetPage(func(offset int, body []byte) (int, []byte) {
		if offset == 200 {
			return http.StatusOK, append(append([]byte(nil), body...), ' ')
		}
		return 0, nil
	})
	second, err := fetchFiguresFrom(server, first.Document)
	if err != nil || second.NotModified || second.Document == nil {
		t.Fatalf("mixed: %+v %v", second, err)
	}
	if !reflect.DeepEqual(asJSONValue(t, second.Document.Entries), asJSONValue(t, first.Document.Entries)) || second.Document.Pages[1].ETag == first.Document.Pages[1].ETag {
		t.Fatal("mixed pages give the same entries, with the middle page's new ETag")
	}
}

// A document generated before the held one is refused; one of the same time is not.
func TestFiguresOlderDocumentRefused(t *testing.T) {
	// date-safe: documents' generated_at, compared only with each other.
	held := &FiguresDocument{DatasetVersion: "held", GeneratedAt: time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)}
	older := &FiguresDocument{DatasetVersion: "older", GeneratedAt: held.GeneratedAt.Add(-time.Second)}
	if kept, err := AcceptFigures(held, older); !errors.Is(err, ErrOlder) || kept != held {
		t.Fatalf("older: %v", err)
	}
	same := &FiguresDocument{DatasetVersion: "same", GeneratedAt: held.GeneratedAt}
	if kept, err := AcceptFigures(held, same); err != nil || kept != same {
		t.Fatalf("same time: %v", err)
	}
	if kept, err := AcceptFigures(nil, older); err != nil || kept != older {
		t.Fatalf("nothing held: %v", err)
	}
}

// The disk copy: written 0600 through a temporary file, read back equal, its ETags still good after a
// restart; a damaged or foreign file is ignored with a warning and the next accepted document replaces it.
func TestFiguresDiskCopy(t *testing.T) {
	fixture, _ := loadFiguresFixture(t)
	server := newFiguresStandIn(t, figuresVolume(t, 450), fixture.Version, fixture.Generated)
	result, err := fetchFiguresFrom(server, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err = store.ReadFigures(figuresTestNow); !errors.Is(err, ErrNotCached) {
		t.Fatalf("nothing on disk: %v", err)
	}
	if err = store.WriteFigures(result.Document); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FiguresFileName)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
	back, err := store.ReadFigures(figuresTestNow)
	if err != nil || !reflect.DeepEqual(asJSONValue(t, back), asJSONValue(t, result.Document)) {
		t.Fatalf("read back: %v", err)
	}
	if again, errAgain := fetchFiguresFrom(server, back); errAgain != nil || !again.NotModified {
		t.Fatalf("after a restart the held ETags still give 304: %+v %v", again, errAgain)
	}
	// A copy made for another provider filter (an older Provimux's fixed set): its figures are held, its
	// ETags are not sent to this request's URL.
	other := *back
	other.Providers = "anthropic,openai"
	before := len(server.Requests())
	if fresh, errFresh := fetchFiguresFrom(server, &other); errFresh != nil || fresh.NotModified || fresh.Document == nil {
		t.Fatalf("another filter: %+v %v", fresh, errFresh)
	}
	for _, req := range server.Requests()[before:] {
		if req.Header.Get("If-None-Match") != "" {
			t.Fatal("an ETag of another URL was sent")
		}
	}

	published, _ := json.Marshal(Document{Schema: PublishedSchema, DatasetVersion: "x", GeneratedAt: figuresTestNow, FetchedAt: figuresTestNow})
	edited := func(edit func(doc map[string]any)) []byte {
		doc := asJSONValue(t, back).(map[string]any)
		edit(doc)
		data, _ := json.Marshal(doc)
		return data
	}
	for name, data := range map[string][]byte{
		"damaged":                         []byte(`{"schema":1,"dataset_version":"x","entries":{`),
		"empty":                           {},
		"foreign: the listings' file":     published,
		"foreign: another program's JSON": []byte(`{"hello":"world"}`),
		"another schema":                  edited(func(doc map[string]any) { doc["schema"] = FiguresSchema + 1 }),
		"an entry under another key": edited(func(doc map[string]any) {
			for _, entry := range doc["entries"].(map[string]any) {
				entry.(map[string]any)["model"] = "another"
				break
			}
		}),
		"a page naming an entry not held": edited(func(doc map[string]any) {
			page := doc["pages"].([]any)[0].(map[string]any)
			page["refs"] = append(page["refs"].([]any), "openai/not-held")
		}),
	} {
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		fresh := NewStore(dir)
		if doc, errRead := fresh.ReadFigures(figuresTestNow); errRead == nil || errors.Is(errRead, ErrNotCached) || doc != nil {
			t.Fatalf("%s: read %v", name, errRead)
		}
		if !fresh.warned["bad:"+FiguresFileName] {
			t.Fatalf("%s: no warning", name)
		}
		if err = fresh.WriteFigures(result.Document); err != nil {
			t.Fatal(err)
		}
		if doc, errRead := fresh.ReadFigures(figuresTestNow); errRead != nil || len(doc.Entries) != 450 {
			t.Fatalf("%s: the next accepted document replaces it: %v", name, errRead)
		}
	}
}

// The url of an entry is its page at providers.dev, each segment of the reference escaped.
func TestFiguresEntryPageURL(t *testing.T) {
	for ref, want := range map[[2]string]string{
		{"anthropic", "claude-opus-5-5"}:            "https://providers.dev/entries/anthropic/claude-opus-5-5/",
		{"openrouter", "anthropic/claude-opus-5-5"}: "https://providers.dev/entries/openrouter/anthropic/claude-opus-5-5/",
		{"openrouter", "x/model:free"}:              "https://providers.dev/entries/openrouter/x/model:free/",
		{"google", "a b"}:                           "https://providers.dev/entries/google/a%20b/",
	} {
		if got := EntryPageURL(ref[0], ref[1]); got != want {
			t.Errorf("%v: %s", ref, got)
		}
	}
}
