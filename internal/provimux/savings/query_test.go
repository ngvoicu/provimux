package savings

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func mustParse(t testing.TB, raw string) Request {
	t.Helper()
	values, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseRequest(values)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return req
}

func (e *testEnv) query(t testing.TB, raw string) *Response {
	t.Helper()
	answer, err := e.r.query(context.Background(), mustParse(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

// generic is an answer as JSON values, without the properties named.
func generic(t testing.TB, value any, drop ...string) any {
	t.Helper()
	data, ok := value.([]byte)
	if !ok {
		var err error
		if data, err = json.Marshal(value); err != nil {
			t.Fatal(err)
		}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	for _, path := range drop {
		parts := strings.Split(path, ".")
		m := out
		for _, part := range parts[:len(parts)-1] {
			m, _ = m[part].(map[string]any)
		}
		if m != nil {
			delete(m, parts[len(parts)-1])
		}
	}
	return out
}

func diffJSON(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("answer differs\n--- got\n%s\n--- want\n%s", g, w)
	}
}

// putExampleDays stores the three days of examples/days.json and a meta whose first day is the first.
func putExampleDays(t *testing.T, store *testStore) {
	t.Helper()
	var days []map[string]any
	if err := json.Unmarshal(readDesign(t, "examples/days.json"), &days); err != nil {
		t.Fatal(err)
	}
	for _, day := range days {
		data, err := json.Marshal(day)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.inner.KVSet(context.Background(), dayKey(day["day"].(string)), data, kvstore.KVSetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	meta, _ := json.Marshal(metaValue{V: 1, CreatedAt: "2026-11-18T06:00:00Z", FirstDay: "2026-11-18",
		KeySecret: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))})
	if _, err := store.inner.KVSet(context.Background(), metaKey, meta, kvstore.KVSetOptions{}); err != nil {
		t.Fatal(err)
	}
}

var exampleExceptions = []string{"observed_at", "recording.saved_at", "prices"}

// The examples of the design are what the code gives (05, section 4: "the examples").
func TestTheExamples(t *testing.T) {
	store := openTestStore(t, "")
	putExampleDays(t, store)
	env := newTestEnv(t, store)
	answer := env.query(t, "period=month&group=kind&group=key&group=kind,provider,host")
	diffJSON(t, generic(t, answer, exampleExceptions...), generic(t, readDesign(t, "examples/savings-month.json"), exampleExceptions...))

	empty := newTestEnv(t, openTestStore(t, ""))
	diffJSON(t, generic(t, empty.query(t, "period=today"), exampleExceptions...),
		generic(t, readDesign(t, "examples/savings-empty.json"), exampleExceptions...))
}

func TestPeriods(t *testing.T) {
	day := func(s string) time.Time { d, _ := parseDay(s); return d }
	for _, tc := range []struct{ today, period, from, to string }{
		{"2026-11-20", "today", "2026-11-20", "2026-11-20"},
		{"2026-11-20", "yesterday", "2026-11-19", "2026-11-19"},
		{"2026-11-20", "7d", "2026-11-14", "2026-11-20"},
		{"2026-11-20", "30d", "2026-10-22", "2026-11-20"},
		{"2026-11-20", "month", "2026-11-01", "2026-11-20"},
		{"2026-11-20", "last_month", "2026-10-01", "2026-10-31"},
		{"2026-11-20", "year", "2026-01-01", "2026-11-20"},
		{"2027-01-15", "last_month", "2026-12-01", "2026-12-31"},
		{"2028-03-10", "last_month", "2028-02-01", "2028-02-29"},
		{"2028-02-29", "month", "2028-02-01", "2028-02-29"},
		{"2028-02-29", "yesterday", "2028-02-28", "2028-02-28"},
		{"2026-11-01", "month", "2026-11-01", "2026-11-01"},
		{"2026-11-01", "yesterday", "2026-10-31", "2026-10-31"},
		{"2026-03-30", "7d", "2026-03-24", "2026-03-30"},
		{"2026-10-26", "yesterday", "2026-10-25", "2026-10-25"},
	} {
		from, to := resolveRange(mustParse(t, "period="+tc.period), day(tc.today), "")
		if got := from.Format(time.DateOnly) + " " + to.Format(time.DateOnly); got != tc.from+" "+tc.to {
			t.Errorf("%s on %s: %s, want %s %s", tc.period, tc.today, got, tc.from, tc.to)
		}
	}
	if req := mustParse(t, ""); req.Period != "month" {
		t.Fatalf("default period %q", req.Period)
	}
	// Through the query, on the two daylight-saving days of Bucharest.
	env := newTestEnv(t, nil)
	env.clock.Set(time.Date(2026, 3, 29, 23, 30, 0, 0, bucharest))
	if a := env.query(t, "period=7d"); a.Today != "2026-03-29" || a.Range.From != "2026-03-23" || a.UTCOffsetSeconds != 3*3600 {
		t.Fatalf("spring: %+v %d", a.Range, a.UTCOffsetSeconds)
	}
	env.clock.Set(time.Date(2026, 10, 25, 0, 30, 0, 0, bucharest))
	if a := env.query(t, "period=today"); a.Today != "2026-10-25" || a.UTCOffsetSeconds != 3*3600 {
		t.Fatalf("autumn: %+v %d", a.Range, a.UTCOffsetSeconds)
	}
}

func TestPeriodAll(t *testing.T) {
	store := openTestStore(t, "")
	putExampleDays(t, store)
	env := newTestEnv(t, store)
	if a := env.query(t, "period=all"); a.Range.From != "2026-11-18" || a.Totals.Requests != 2245 {
		t.Fatalf("all starts at first_day: %+v %d", a.Range, a.Totals.Requests)
	}
	early := claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1)
	early.RequestedAt = time.Date(2026, 11, 10, 12, 0, 0, 0, bucharest)
	env.record(early)
	if a := env.query(t, "period=all"); a.Range.From != "2026-11-10" || *a.Recording.Since != "2026-11-10" {
		t.Fatalf("an earlier day held only in memory: %+v", a.Range)
	}
	empty := newTestEnv(t, nil)
	if a := empty.query(t, "period=all"); a.Range.From != today || a.Range.To != today || a.Recording.Since != nil {
		t.Fatalf("nothing recorded: %+v", a.Range)
	}
}

func TestEvery400(t *testing.T) {
	for raw, parameter := range map[string]string{
		"period=week": "period",
		"period=month&from=2026-11-01&to=2026-11-02": "period",
		"from=2026-11-01":                    "from, to",
		"to=2026-11-01":                      "from, to",
		"from=2026-1-01&to=2026-11-02":       "from",
		"from=2026-11-01&to=2026-02-30":      "to",
		"from=2026-11-02&to=2026-11-01":      "to",
		"from=2016-01-01&to=2026-11-01":      "to",
		"group=day,week":                     "group",
		"group=kind,kind":                    "group",
		"group=day,month":                    "group",
		"group=day,kind,provider,host,model": "group",
		"group=":                             "group",
		"group=kind&group=kind&group=kind&group=kind&group=kind&group=kind&group=kind&group=kind&group=kind": "group",
	} {
		values, _ := url.ParseQuery(raw)
		_, err := ParseRequest(values)
		if err == nil || !strings.HasPrefix(err.Error(), parameter+":") {
			t.Errorf("%s: error %v, want one naming %s", raw, err, parameter)
		}
	}
	if _, err := ParseRequest(url.Values{"from": {"2016-01-05"}, "to": {"2026-01-01"}}); err != nil {
		t.Fatalf("3648 days is allowed: %v", err)
	}
}

// sumOf adds up the rows of a breakdown as Metrics, with amounts summed then rounded.
func checkSums(t *testing.T, a *Response) {
	t.Helper()
	for _, b := range a.Breakdowns {
		var req, fail, in, cr, cw, cw5, cw1h, out, rsn, unc, priced, unpriced, floating, costReq int64
		var cost, without, saved, reported float64
		for _, r := range b.Rows {
			req += r.Requests
			fail += r.Failures
			in += r.Tokens.Input.Uncached
			cr += r.Tokens.Input.CacheRead
			cw += r.Tokens.Input.CacheWrite
			cw5 += r.Tokens.Input.CacheWrite5m
			cw1h += r.Tokens.Input.CacheWrite1h
			out += r.Tokens.Output.Total
			rsn += r.Tokens.Output.Reasoning
			unc += r.Tokens.Unclassified
			priced += r.Estimate.PricedRequests
			unpriced += r.Estimate.UnpricedRequests
			floating += r.Estimate.FloatingRequests
			costReq += r.ReportedCost.Requests
			cost += r.Estimate.CostUSD
			without += r.Estimate.CostWithoutCacheUSD
			saved += r.Estimate.SavedByCachingUSD
			reported += r.ReportedCost.USD
			if r.Estimate.PricedRequests+r.Estimate.UnpricedRequests != r.Requests {
				t.Errorf("%v: priced + unpriced != requests in a row", b.By)
			}
		}
		tt := a.Totals
		if req != tt.Requests || fail != tt.Failures || in != tt.Tokens.Input.Uncached || cr != tt.Tokens.Input.CacheRead ||
			cw != tt.Tokens.Input.CacheWrite || cw5 != tt.Tokens.Input.CacheWrite5m || cw1h != tt.Tokens.Input.CacheWrite1h ||
			out != tt.Tokens.Output.Total || rsn != tt.Tokens.Output.Reasoning || unc != tt.Tokens.Unclassified ||
			priced != tt.Estimate.PricedRequests || unpriced != tt.Estimate.UnpricedRequests ||
			floating != tt.Estimate.FloatingRequests || costReq != tt.ReportedCost.Requests {
			t.Errorf("%v: counters do not add up to the totals", b.By)
		}
		tolerance := 1e-6 * float64(len(b.Rows))
		if math.Abs(cost-tt.Estimate.CostUSD) > tolerance || math.Abs(without-tt.Estimate.CostWithoutCacheUSD) > tolerance ||
			math.Abs(saved-tt.Estimate.SavedByCachingUSD) > tolerance || math.Abs(reported-tt.ReportedCost.USD) > tolerance {
			t.Errorf("%v: amounts do not add up: %v %v %v %v", b.By, cost, without, saved, reported)
		}
	}
	if a.Totals.Estimate.PricedRequests+a.Totals.Estimate.UnpricedRequests != a.Totals.Requests {
		t.Error("totals: priced + unpriced != requests")
	}
}

func TestSumsAgree(t *testing.T) {
	store := openTestStore(t, "")
	putExampleDays(t, store)
	env := newTestEnv(t, store)
	env.prices.set(testList(t), testNow)
	env.record(claudeRecord("claude-sonnet-5-5", "client-key-zzzzzzzzzzzz", 100, 2000, 300, 40))
	checkSums(t, env.query(t, "period=all&group=day&group=month&group=kind&group=provider&group=host&group=model&group=key"))
}

func TestPendingIsIncluded(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.prices.set(testList(t), testNow)
	env.record(claudeRecord("claude-opus-5-5", "client-key-pending-0001", 100, 1000, 10, 50))
	env.round()
	env.record(claudeRecord("claude-opus-5-5", "client-key-pending-0001", 100, 1000, 10, 50))
	env.record(claudeRecord("claude-haiku-5-5", "client-key-pending-0002", 1, 2, 3, 4))
	const q = "period=today&group=kind&group=key&group=model"
	before := env.query(t, q)
	if before.Totals.Requests != 3 {
		t.Fatalf("a record not yet written is in the answer: %d", before.Totals.Requests)
	}
	// A row priced only by the read is floating: its price is not the day's until the saver writes it.
	// The opus record joins a row the store already priced; the haiku row is new.
	if before.Totals.Estimate.FloatingRequests != 1 {
		t.Fatalf("floating before the round: %d, want 1", before.Totals.Estimate.FloatingRequests)
	}
	env.round()
	after := env.query(t, q)
	if after.Totals.Estimate.FloatingRequests != 0 {
		t.Fatalf("floating after the round: %d", after.Totals.Estimate.FloatingRequests)
	}
	// Counters and amounts are the same; only the basis of the new row changed.
	b, a := generic(t, before.Totals, "estimate"), generic(t, after.Totals, "estimate")
	diffJSON(t, b, a)
	if before.Totals.Estimate.CostUSD != after.Totals.Estimate.CostUSD || before.Totals.Estimate.SavedByCachingUSD != after.Totals.Estimate.SavedByCachingUSD {
		t.Fatalf("amounts changed: %+v %+v", before.Totals.Estimate, after.Totals.Estimate)
	}
}

func TestOrder(t *testing.T) {
	store := openTestStore(t, "")
	putExampleDays(t, store)
	env := newTestEnv(t, store)
	const q = "period=month&group=day,kind&group=model,key&group=key"
	first, _ := json.Marshal(generic(t, env.query(t, q), "observed_at"))
	for i := 0; i < 5; i++ {
		again, _ := json.Marshal(generic(t, env.query(t, q), "observed_at"))
		if !bytes.Equal(first, again) {
			t.Fatal("the order changed between calls")
		}
	}
	rows := env.query(t, q).Breakdowns[0].Rows
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		if *a.Day > *b.Day || (*a.Day == *b.Day && a.Requests < b.Requests) {
			t.Fatalf("time first, then requests: %s/%d before %s/%d", *a.Day, a.Requests, *b.Day, b.Requests)
		}
	}
	if len(rows) != 6 || *rows[0].Day != "2026-11-18" || *rows[0].Kind != "oauth" {
		t.Fatalf("rows %d, first %s %s", len(rows), *rows[0].Day, *rows[0].Kind)
	}
}

func TestTruncation(t *testing.T) {
	env := newTestEnv(t, nil)
	for i := 0; i < 2001; i++ {
		rec := claudeRecord(fmt.Sprintf("model-%04d", i), "", 1, 0, 0, 1)
		if i%2 == 1 {
			rec.RequestedAt = testNow.AddDate(0, 0, -1) // two days, each under the row limit
		}
		env.record(rec)
		if i != 1000 {
			env.record(rec) // every model but one has 2 requests
		}
	}
	b := env.query(t, "period=7d&group=model").Breakdowns[0]
	if len(b.Rows) != 2000 || !b.Truncated {
		t.Fatalf("rows %d truncated %v", len(b.Rows), b.Truncated)
	}
	for _, r := range b.Rows {
		if *r.Model == "model-1000" {
			t.Fatal("the row with the fewest requests must be the one left out")
		}
	}
}

func TestFilters(t *testing.T) {
	store := openTestStore(t, "")
	putExampleDays(t, store)
	env := newTestEnv(t, store)
	count := func(q string) int64 { return env.query(t, "period=month&"+q).Totals.Requests }
	for q, want := range map[string]int64{
		"":                              2245,
		"kind=oauth":                    2006,
		"kind=apikey":                   239,
		"kind=":                         0,
		"provider=CLAUDE":               1402,
		"host=openrouter.ai":            213,
		"host=":                         2032,
		"model=swe-2":                   90,
		"key=3f9a1c2b7d4e5f60":          2006,
		"key=":                          26,
		"kind=apikey&provider=claude":   26,
		"kind=oauth&host=openrouter.ai": 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%q: %d, want %d", q, got, want)
		}
	}
	a := env.query(t, "period=month&kind=oauth")
	if len(a.ReportedCostHosts) != 0 || len(a.Models) != 3 {
		t.Fatalf("filters narrow the model list and the hosts: %v %d", a.ReportedCostHosts, len(a.Models))
	}
	if a.Recording.Since == nil || *a.Recording.Since != "2026-11-18" {
		t.Fatal("filters leave recording.since")
	}
}

func TestFoldedDaysFollowFilters(t *testing.T) {
	env := newTestEnv(t, nil)
	env.r.mu.Lock()
	env.r.deltas[today] = &deltaDay{rows: map[pendingKey]*counters{}, regular: maxRows} // as if the day were full
	env.r.mu.Unlock()
	env.record(claudeRecord("model-x", "", 1, 0, 0, 1))
	if a := env.query(t, "period=today"); a.Recording.FoldedDays != 1 {
		t.Fatalf("folded_days %d", a.Recording.FoldedDays)
	}
	if a := env.query(t, "period=today&provider=codex"); a.Recording.FoldedDays != 0 {
		t.Fatalf("a filter that leaves no row leaves no folded day: %d", a.Recording.FoldedDays)
	}
	if a := env.query(t, "period=today&group=model"); a.Models[0].Model != otherValue || a.Models[0].Price != nil {
		t.Fatalf("~other has no price: %+v", a.Models[0])
	}
}

func TestTheModelList(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	opus := map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}
	// Yesterday recorded without a price, today with one: mixed. A pair priced only now: current.
	yesterday := claudeRecord("claude-opus-5-5", "", 10, 0, 0, 1)
	yesterday.RequestedAt = testNow.AddDate(0, 0, -1)
	env.record(yesterday)
	env.record(claudeRecord("claude-sonnet-5-5", "", 10, 0, 0, 1))
	env.round()
	env.prices.set(opus, testNow)
	env.record(claudeRecord("claude-opus-5-5", "", 1000, 0, 0, 1))
	env.record(claudeRecord("swe-2", "", 1, 0, 0, 0))
	env.round()
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{
		"anthropic/claude-opus-5.5":   {Input: 4, Output: 20},
		"anthropic/claude-sonnet-5.5": {Input: 2, Output: 10},
	}, testNow.Add(time.Minute))
	env.refreshCache()
	a := env.query(t, "period=7d")
	got := map[string]string{}
	for _, m := range a.Models {
		basis := "null"
		if m.Price != nil {
			basis = m.Price.Basis
		}
		got[m.Model] = basis
	}
	if want := map[string]string{"claude-opus-5-5": "mixed", "claude-sonnet-5-5": "current", "swe-2": "null"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("basis %v, want %v", got, want)
	}
	if a.Models[0].Model != "claude-opus-5-5" {
		t.Fatal("most tokens first")
	}
	many := newTestEnv(t, nil)
	for i := 0; i < 501; i++ {
		many.record(claudeRecord(fmt.Sprintf("model-%03d", i), "", 1, 0, 0, 1))
	}
	if a = many.query(t, "period=today"); len(a.Models) != 500 || !a.ModelsTruncated {
		t.Fatalf("501 pairs: %d %v", len(a.Models), a.ModelsTruncated)
	}
}

// refreshCache makes the recorder take the current test list, as a saver round would.
func (e *testEnv) refreshCache() {
	e.r.mu.Lock()
	e.r.refreshPrices()
	e.r.mu.Unlock()
}

func TestFloating(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "", 1_000_000, 0, 0, 0))
	env.round()
	file := store.file(t)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}, testNow)
	env.refreshCache()
	a := env.query(t, "period=today")
	if a.Totals.Estimate.FloatingRequests != 1 || a.Totals.Estimate.CostUSD != 4 || a.Totals.Estimate.UnpricedRequests != 0 {
		t.Fatalf("a day recorded without prices is valued once a list exists: %+v", a.Totals.Estimate)
	}
	if !bytes.Equal(file, store.file(t)) {
		t.Fatal("the query wrote")
	}
}

// The answer takes the list and remote as the catalog keeps them, without a saver round and
// without asking for a download.
func TestQueryReadsTheKeptList(t *testing.T) {
	env := newTestEnv(t, nil)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}, testNow)
	env.record(claudeRecord("claude-opus-5-5", "", 1_000_000, 0, 0, 0))
	a := env.query(t, "period=today")
	if !a.Prices.OpenRouter.Remote || a.Prices.OpenRouter.Models != 1 || a.Prices.OpenRouter.FetchedAt == nil || a.Totals.Estimate.CostUSD != 4 {
		t.Fatalf("prices %+v cost %v", a.Prices.OpenRouter, a.Totals.Estimate.CostUSD)
	}
	if env.prices.calls.Load() != 0 {
		t.Fatal("a query asked for a download")
	}
}

func TestNoPrices(t *testing.T) {
	env := newTestEnv(t, openTestStore(t, ""))
	env.record(claudeRecord("claude-opus-5-5", "", 100, 1000, 10, 50))
	env.record(claudeRecord("gpt-6.2-sol", "", 100, 1000, 10, 50))
	a := env.query(t, "period=today&group=kind")
	e := a.Totals.Estimate
	if e.CostUSD != 0 || e.CostWithoutCacheUSD != 0 || e.SavedByCachingUSD != 0 || e.UnpricedRequests != 2 || a.Prices.OpenRouter.FetchedAt != nil || a.Prices.OpenRouter.Models != 0 {
		t.Fatalf("no prices: %+v %+v", e, a.Prices)
	}
}

func TestAKeysTail(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	key := "client-key-with-a-tail-9Zq1"
	old := claudeRecord("claude-opus-5-5", key, 1, 0, 0, 1)
	old.RequestedAt = testNow.AddDate(0, 0, -3)
	env.record(old)
	env.round()
	id := keyID(env.r.secret, key)
	// The older day says another tail: the latest day that has the id wins.
	d := storedDay(t, store, "2026-11-17")
	d.keys[id] = "OLD1"
	data, _ := d.encode("2026-11-17")
	_, _ = store.inner.KVSet(context.Background(), dayKey("2026-11-17"), data, kvstore.KVSetOptions{})
	env.record(claudeRecord("claude-opus-5-5", key, 1, 0, 0, 1))
	a := env.query(t, "period=7d&group=key")
	if k := a.Breakdowns[0].Rows[0].Key; k.ID != id || k.Tail == nil || *k.Tail != "9Zq1" || k.Name != nil {
		t.Fatalf("key %+v", k)
	}
}

func TestReadsDoNotWrite(t *testing.T) {
	store := openTestStore(t, "")
	putExampleDays(t, store)
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "client-key-unwritten-01", 1, 0, 0, 1))
	before := store.file(t)
	writes := store.writes.Load()
	for i := 0; i < 100; i++ {
		env.query(t, "period=all&group=day&group=key&group=kind,provider,host")
	}
	if store.writes.Load() != writes || !bytes.Equal(before, store.file(t)) {
		t.Fatal("queries changed the store's file")
	}
}

func TestQueryWhenTheStoreIsOffOrFailing(t *testing.T) {
	env := newTestEnv(t, nil)
	env.record(claudeRecord("claude-opus-5-5", "key-a", 10, 0, 0, 1))
	env.round()
	if a := env.query(t, "period=today"); a.Recording.Persistent || a.Totals.Requests != 1 {
		t.Fatalf("store off: persistent=%v requests=%d", a.Recording.Persistent, a.Totals.Requests)
	}

	store := openTestStore(t, "")
	failing := newTestEnv(t, store)
	failing.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	failing.round()
	store.mu.Lock()
	store.failSet = errDiskFull
	store.mu.Unlock()
	failing.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	failing.round()
	if a := failing.query(t, "period=today"); a.Recording.Persistent || a.Totals.Requests != 2 || a.Recording.SavedAt == nil {
		t.Fatalf("after a failed write: %+v %d", a.Recording, a.Totals.Requests)
	}
	store.mu.Lock()
	store.failSet = nil
	store.failGet = errDiskFull
	store.mu.Unlock()
	if _, err := failing.r.query(context.Background(), mustParse(t, "period=month")); err != ErrStoreUnavailable {
		t.Fatalf("a read error is ErrStoreUnavailable: %v", err)
	}
	store.mu.Lock()
	store.failGet = nil
	store.mu.Unlock()
	failing.round()
	if a := failing.query(t, "period=today"); !a.Recording.Persistent {
		t.Fatal("persistent again after a write succeeded")
	}
}

func TestQuerySkipsADayThatDoesNotDecode(t *testing.T) {
	store := openTestStore(t, "")
	if _, err := store.inner.KVSet(context.Background(), dayKey(today), []byte(`{"v":1,"rows":"x"`), kvstore.KVSetOptions{}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	restore := captureLogs(&logs)
	defer restore()
	env := newTestEnv(t, store)
	env.query(t, "period=today")
	env.record(claudeRecord("claude-opus-5-5", "", 1, 0, 0, 1))
	env.round()
	if a := env.query(t, "period=today"); a.Totals.Requests != 0 {
		t.Fatalf("reads skip the day: %d", a.Totals.Requests)
	}
	if n := strings.Count(logs.String(), dayKey(today)); n != 1 {
		t.Fatalf("logged %d times", n)
	}
}

func TestAQueryBeforeTheFirstRoundShowsTheStoredIDs(t *testing.T) {
	store := openTestStore(t, "")
	stored := bytes.Repeat([]byte{9}, 32)
	meta, _ := json.Marshal(metaValue{V: 1, CreatedAt: "2026-11-01T00:00:00Z", FirstDay: "2026-11-01", KeySecret: base64.StdEncoding.EncodeToString(stored)})
	_, _ = store.inner.KVSet(context.Background(), metaKey, meta, kvstore.KVSetOptions{})
	env := newTestEnv(t, store)
	key := "client-key-query-first-0001"
	env.record(claudeRecord("claude-opus-5-5", key, 1, 0, 0, 1))
	writes := store.writes.Load()
	a := env.query(t, "period=today&group=key")
	if store.writes.Load() != writes {
		t.Fatal("a query wrote")
	}
	if got := a.Breakdowns[0].Rows[0].Key.ID; got != keyID(stored, key) {
		t.Fatalf("query id %s, want the stored secret's", got)
	}
	env.round()
	if d := storedDay(t, store, today); d.keys[keyID(stored, key)] != "0001" {
		t.Fatalf("the round writes the same id: %+v", d.keys)
	}
}

func TestConcurrentWithQueries(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.prices.set(map[string]registry.ProvimuxOpenRouterPrice{"anthropic/claude-opus-5.5": {Input: 4, Output: 20}}, testNow)
	var wg sync.WaitGroup
	const workers, each = 8, 200
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				env.record(claudeRecord("claude-opus-5-5", fmt.Sprintf("client-key-%02d-xxxxxxxx", w), 1, 2, 3, 4))
				if i%40 == 0 {
					env.round()
					if _, err := env.r.query(context.Background(), mustParse(t, "period=today&group=key&group=model")); err != nil {
						t.Error(err)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	if a := env.query(t, "period=today"); a.Totals.Requests != workers*each || a.Totals.Tokens.Input.CacheRead != 2*workers*each {
		t.Fatalf("totals %d", a.Totals.Requests)
	}
	env.round()
	if got := sumRows(storedDay(t, store, today)).Req; got != workers*each {
		t.Fatalf("stored %d", got)
	}
}
