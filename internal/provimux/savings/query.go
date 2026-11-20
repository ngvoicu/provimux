package savings

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Limits of the endpoint (openapi.yaml).
const (
	maxRangeDays     = 3660
	maxGroups        = 8
	maxDimensions    = 4
	maxBreakdownRows = 2000
	maxModels        = 500
)

var periods = []string{"today", "yesterday", "7d", "30d", "month", "last_month", "year", "all"}

var dimensions = []string{"day", "month", "kind", "provider", "host", "model", "key"}

// Request is a parsed query: a period or a from/to pair, the group values, the filters.
// A nil filter is absent; a pointer to "" is present and empty.
type Request struct {
	Period                           string
	From, To                         string
	Groups                           [][]string
	Kind, Provider, Host, Model, Key *string
}

// ParseRequest reads the endpoint's query. Its error text is the 400 answer's message.
func ParseRequest(values url.Values) (Request, error) {
	var req Request
	_, hasPeriod := values["period"]
	_, hasFrom := values["from"]
	_, hasTo := values["to"]
	switch {
	case hasPeriod && (hasFrom || hasTo):
		return Request{}, errors.New("period: not allowed together with from and to")
	case hasFrom != hasTo:
		return Request{}, errors.New("from, to: both or neither")
	case hasFrom:
		from, errFrom := parseDay(values.Get("from"))
		if errFrom != nil {
			return Request{}, fmt.Errorf("from: %w", errFrom)
		}
		to, errTo := parseDay(values.Get("to"))
		if errTo != nil {
			return Request{}, fmt.Errorf("to: %w", errTo)
		}
		if to.Before(from) {
			return Request{}, errors.New("to: before from")
		}
		if days := daysBetween(from, to); days > maxRangeDays {
			return Request{}, fmt.Errorf("to: more than %d days after from", maxRangeDays)
		}
		req.From, req.To = values.Get("from"), values.Get("to")
	case hasPeriod:
		req.Period = values.Get("period")
		if !slices.Contains(periods, req.Period) {
			return Request{}, fmt.Errorf("period: unknown value %q", req.Period)
		}
	default:
		req.Period = "month"
	}

	groups := values["group"]
	if len(groups) > maxGroups {
		return Request{}, fmt.Errorf("group: more than %d values", maxGroups)
	}
	for _, value := range groups {
		by := strings.Split(value, ",")
		if len(by) > maxDimensions {
			return Request{}, fmt.Errorf("group: more than %d dimensions in %q", maxDimensions, value)
		}
		for i, dimension := range by {
			if !slices.Contains(dimensions, dimension) {
				return Request{}, fmt.Errorf("group: unknown dimension %q", dimension)
			}
			if slices.Contains(by[:i], dimension) {
				return Request{}, fmt.Errorf("group: dimension %q repeated in %q", dimension, value)
			}
		}
		if slices.Contains(by, "day") && slices.Contains(by, "month") {
			return Request{}, fmt.Errorf("group: day and month together in %q", value)
		}
		req.Groups = append(req.Groups, by)
	}

	filter := func(name string, lower bool) *string {
		if _, ok := values[name]; !ok {
			return nil
		}
		v := values.Get(name)
		if lower {
			v = strings.ToLower(v)
		}
		return &v
	}
	req.Kind = filter("kind", false)
	req.Provider = filter("provider", true)
	req.Host = filter("host", true)
	req.Model = filter("model", true)
	req.Key = filter("key", false)
	return req, nil
}

// civil is a calendar date, computed at noon UTC so that day arithmetic never meets a 23 or 25 hour day.
func civil(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

func parseDay(value string) (time.Time, error) {
	if len(value) != 10 {
		return time.Time{}, fmt.Errorf("%q is not YYYY-MM-DD", value)
	}
	t, errParse := time.Parse(time.DateOnly, value)
	if errParse != nil {
		return time.Time{}, fmt.Errorf("%q is not a calendar day", value)
	}
	return civil(t.Year(), t.Month(), t.Day()), nil
}

func daysBetween(from, to time.Time) int { return int(to.Sub(from).Hours()/24 + 0.5) }

// Response is the endpoint's answer: the properties of SavingsResponse in openapi.yaml, with those
// names as JSON tags. Slices are never nil ("breakdowns": [], not null).
type Response struct {
	ObservedAt        string       `json:"observed_at"`
	Timezone          string       `json:"timezone"`
	UTCOffsetSeconds  int          `json:"utc_offset_seconds"`
	Today             string       `json:"today"`
	Range             Range        `json:"range"`
	Recording         Recording    `json:"recording"`
	Prices            Prices       `json:"prices"`
	Totals            Metrics      `json:"totals"`
	Breakdowns        []Breakdown  `json:"breakdowns"`
	Models            []ModelPrice `json:"models"`
	ModelsTruncated   bool         `json:"models_truncated"`
	ReportedCostHosts []string     `json:"reported_cost_hosts"`
}

// Range is the resolved range; Period is null when from and to were given.
type Range struct {
	Period *string `json:"period"`
	From   string  `json:"from"`
	To     string  `json:"to"`
}

// Recording says what the numbers cover.
type Recording struct {
	Since      *string `json:"since"`
	Persistent bool    `json:"persistent"`
	SavedAt    *string `json:"saved_at"`
	FoldedDays int     `json:"folded_days"`
}

// Prices says where the prices come from.
type Prices struct {
	Currency   string           `json:"currency"`
	OpenRouter OpenRouterSource `json:"openrouter"`
	Provimux   ProvimuxSource   `json:"provimux"`
}

// OpenRouterSource is OpenRouter's public list.
type OpenRouterSource struct {
	URL       string  `json:"url"`
	FetchedAt *string `json:"fetched_at"`
	Models    int     `json:"models"`
	Remote    bool    `json:"remote"`
}

// ProvimuxSource is Provimux's own table.
type ProvimuxSource struct {
	Updated string `json:"updated"`
	Models  int    `json:"models"`
}

// Metrics are the totals of a set of rows.
type Metrics struct {
	Requests     int64        `json:"requests"`
	Failures     int64        `json:"failures"`
	Tokens       Tokens       `json:"tokens"`
	CacheHitRate *float64     `json:"cache_hit_rate"`
	Estimate     Estimate     `json:"estimate"`
	ReportedCost ReportedCost `json:"reported_cost"`
}

// Tokens are the token buckets.
type Tokens struct {
	Input        InputTokens  `json:"input"`
	Output       OutputTokens `json:"output"`
	Unclassified int64        `json:"unclassified"`
}

// InputTokens are mutually exclusive buckets; Total is their sum.
type InputTokens struct {
	Uncached     int64 `json:"uncached"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite   int64 `json:"cache_write"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
	Total        int64 `json:"total"`
}

// OutputTokens include reasoning.
type OutputTokens struct {
	Total     int64 `json:"total"`
	Reasoning int64 `json:"reasoning"`
}

// Estimate is valued at list prices, over the rows that have a price.
type Estimate struct {
	CostUSD             float64 `json:"cost_usd"`
	CostWithoutCacheUSD float64 `json:"cost_without_cache_usd"`
	SavedByCachingUSD   float64 `json:"saved_by_caching_usd"`
	PricedRequests      int64   `json:"priced_requests"`
	UnpricedRequests    int64   `json:"unpriced_requests"`
	UnpricedTokens      int64   `json:"unpriced_tokens"`
	FloatingRequests    int64   `json:"floating_requests"`
}

// ReportedCost is what upstreams reported having charged.
type ReportedCost struct {
	USD             float64 `json:"usd"`
	BYOKUpstreamUSD float64 `json:"byok_upstream_usd"`
	Requests        int64   `json:"requests"`
}

// Breakdown is one group value's rows.
type Breakdown struct {
	By        []string       `json:"by"`
	Rows      []BreakdownRow `json:"rows"`
	Truncated bool           `json:"truncated"`
}

// BreakdownRow is the dimensions named in by, each as a property, and the metrics of the rows they
// select.
type BreakdownRow struct {
	Day      *string    `json:"day,omitempty"`
	Month    *string    `json:"month,omitempty"`
	Kind     *string    `json:"kind,omitempty"`
	Provider *string    `json:"provider,omitempty"`
	Host     *string    `json:"host,omitempty"`
	Model    *string    `json:"model,omitempty"`
	Key      *ClientKey `json:"key,omitempty"`
	Metrics
}

// ClientKey is a client API key, never the key itself.
type ClientKey struct {
	ID   string  `json:"id"`
	Tail *string `json:"tail"`
	Name *string `json:"name"`
}

// ModelPrice is a provider and model of the range with the price it is valued at.
type ModelPrice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
	Price    *Price `json:"price"`
}

// Price is the price a model is valued at.
type Price struct {
	Source              string   `json:"source"`
	ID                  string   `json:"id"`
	Match               string   `json:"match"`
	Basis               string   `json:"basis"`
	InputPerMTok        float64  `json:"input_per_mtok"`
	OutputPerMTok       float64  `json:"output_per_mtok"`
	CacheReadPerMTok    *float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok   *float64 `json:"cache_write_per_mtok"`
	CacheWrite1hPerMTok *float64 `json:"cache_write_1h_per_mtok"`
}

// Query answers a request from memory and the store. It writes nothing and starts no download.
func Query(ctx context.Context, request Request) (*Response, error) {
	return defaultRecorder.query(ctx, request)
}

// queryDay is one day of the range as the answer sees it: what the saver would write next.
type queryDay struct {
	day  string
	rows *dayRows
}

// memoryDay is a copy of a day's in-memory state taken under the mutex.
type memoryDay struct {
	rows    *dayRows
	loaded  bool
	pending map[pendingKey]*counters
	broken  bool
}

func (r *recorder) query(ctx context.Context, req Request) (*Response, error) {
	now := r.opts.now()
	loc := r.location()
	local := now.In(loc)
	todayDate := civil(local.Year(), local.Month(), local.Day())
	_, offset := local.Zone()

	store, errStore := r.opts.store()
	if errStore != nil {
		return nil, ErrStoreUnavailable
	}

	// What the process knows, under the mutex. No store call under it.
	r.mu.Lock()
	memEarliest := ""
	for day := range r.days {
		if memEarliest == "" || day < memEarliest {
			memEarliest = day
		}
	}
	metaKnown, meta, metaOK, secret := r.metaKnown, r.meta, r.metaOK, slices.Clone(r.secret)
	savedKnown := r.savedKnown
	book := r.bookLocked()
	wroteOnce, lastWriteOK, lastSavedAt := r.wroteOnce, r.lastWriteOK, r.lastSavedAt
	r.mu.Unlock()

	// The list as the catalog keeps it now, and whether it may be downloaded: read without asking
	// for a download, so a list downloaded after the saver's last round is used too.
	if list, fetchedAt, remote := r.opts.openRouterKept(); list != nil || remote != book.remote {
		next := *book
		next.remote = remote
		if list != nil && (next.openRouter == nil || fetchedAt.After(next.fetchedAt)) {
			next.openRouter, next.fetchedAt = list, fetchedAt
		}
		book = &next
	}

	// Before the saver reached the store, read meta and the saved list itself; never write them.
	if store != nil && (!metaKnown || !savedKnown) {
		values, found, errGet := store.KVMGet(ctx, metaKey, openRouterListKey)
		if errGet != nil || len(values) != 2 || len(found) != 2 {
			return nil, ErrStoreUnavailable
		}
		if !metaKnown && found[0] {
			if decoded, storedSecret, errMeta := decodeMeta(values[0]); errMeta == nil {
				meta, metaOK, secret = decoded, true, storedSecret
			}
		}
		if !savedKnown && found[1] {
			if list, at, errList := decodeListCopy(values[1]); errList == nil &&
				(book.openRouter == nil || at.After(book.fetchedAt)) {
				book = &priceBook{openRouter: list, fetchedAt: at, remote: book.remote, table: book.table}
			}
		}
	}
	since := memEarliest
	if metaOK && meta.FirstDay != "" && (since == "" || meta.FirstDay < since) {
		since = meta.FirstDay
	}

	from, to := resolveRange(req, todayDate, since)
	var days []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format(time.DateOnly))
	}

	r.mu.Lock()
	memory := make(map[string]memoryDay)
	for _, day := range days {
		st, ok := r.days[day]
		if !ok {
			continue
		}
		m := memoryDay{loaded: st.loaded, broken: st.broken, pending: make(map[pendingKey]*counters, len(st.pending))}
		if st.loaded {
			m.rows = st.rows.clone()
		} else {
			m.rows = newDayRows(st.rows.tz, st.rows.offset)
		}
		for k, c := range st.pending {
			copied := *c
			m.pending[k] = &copied
		}
		memory[day] = m
	}
	r.mu.Unlock()

	// One read of the days in range that memory does not hold as stored.
	var toRead []string
	for _, day := range days {
		if m, ok := memory[day]; !ok || (!m.loaded && !m.broken) {
			toRead = append(toRead, day)
		}
	}
	fromStore := make(map[string]*dayRows)
	if store != nil && len(toRead) > 0 {
		keys := make([]string, len(toRead))
		for i, day := range toRead {
			keys[i] = dayKey(day)
		}
		values, found, errGet := store.KVMGet(ctx, keys...)
		if errGet != nil || len(values) != len(keys) || len(found) != len(keys) {
			return nil, ErrStoreUnavailable
		}
		for i, day := range toRead {
			if !found[i] {
				continue
			}
			decoded, errDecode := decodeDay(values[i])
			if errDecode != nil {
				r.logOnce(dayKey(day), "savings: %s does not decode; it is kept as it is and skipped", dayKey(day))
				fromStore[day] = nil
				continue
			}
			fromStore[day] = decoded
		}
	}

	var assembled []queryDay
	for _, day := range days {
		m, inMemory := memory[day]
		stored, read := fromStore[day]
		if (inMemory && m.broken) || (read && stored == nil) {
			continue // a value that does not decode: reads skip the day
		}
		var rows *dayRows
		switch {
		case inMemory && m.loaded:
			rows = m.rows
		case read:
			rows = stored
		case inMemory:
			rows = m.rows
		default:
			continue
		}
		if inMemory && len(m.pending) > 0 {
			rows.mergePending(m.pending, secret)
			rows.assignPrices(book)
		}
		if len(rows.rows) > 0 {
			assembled = append(assembled, queryDay{day: day, rows: rows})
		}
	}

	answer := build(req, assembled, book)
	answer.ObservedAt = formatTime(now)
	answer.Timezone = loc.String()
	answer.UTCOffsetSeconds = offset
	answer.Today = todayDate.Format(time.DateOnly)
	answer.Range = Range{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)}
	if req.Period != "" {
		period := req.Period
		answer.Range.Period = &period
	}
	if since != "" {
		answer.Recording.Since = &since
	}
	answer.Recording.Persistent = store != nil && (!wroteOnce || lastWriteOK)
	if !lastSavedAt.IsZero() {
		saved := formatTime(lastSavedAt)
		answer.Recording.SavedAt = &saved
	}
	answer.Prices = Prices{
		Currency:   "USD",
		OpenRouter: OpenRouterSource{URL: openRouterModelsURL, Models: len(book.openRouter), Remote: book.remote},
		Provimux:   ProvimuxSource{Updated: book.table.Updated, Models: len(book.table.Models)},
	}
	if !book.fetchedAt.IsZero() && book.openRouter != nil {
		fetched := formatTime(book.fetchedAt)
		answer.Prices.OpenRouter.FetchedAt = &fetched
	}
	return answer, nil
}

// resolveRange is 03-endpoint.md section 2, on calendar dates of the server's zone.
func resolveRange(req Request, today time.Time, since string) (time.Time, time.Time) {
	if req.Period == "" {
		from, _ := parseDay(req.From)
		to, _ := parseDay(req.To)
		return from, to
	}
	y, m, d := today.Date()
	switch req.Period {
	case "today":
		return today, today
	case "yesterday":
		yesterday := civil(y, m, d-1)
		return yesterday, yesterday
	case "7d":
		return civil(y, m, d-6), today
	case "30d":
		return civil(y, m, d-29), today
	case "last_month":
		return civil(y, m-1, 1), civil(y, m, 0)
	case "year":
		return civil(y, time.January, 1), today
	case "all":
		if first, errParse := parseDay(since); errParse == nil && first.Before(today) {
			return first, today
		}
		return today, today
	default: // month
		return civil(y, m, 1), today
	}
}

// accumulator sums counters and amounts over a set of rows.
type accumulator struct {
	n                          counters
	cost, without, saved       float64
	priced, floating, unpriced int64
	unpricedTokens             int64
}

func (a *accumulator) add(n counters, valued, floating bool, cost, without float64) {
	a.n.add(n)
	if !valued {
		a.unpriced += n.Req
		a.unpricedTokens += n.tokens()
		return
	}
	a.priced += n.Req
	if floating {
		a.floating += n.Req
	}
	a.cost += cost
	a.without += without
	a.saved += without - cost
}

func (a *accumulator) metrics() Metrics {
	n := a.n
	m := Metrics{
		Requests: n.Req,
		Failures: n.Fail,
		Tokens: Tokens{
			Input:        InputTokens{Uncached: n.In, CacheRead: n.Cr, CacheWrite: n.Cw, CacheWrite5m: n.Cw5, CacheWrite1h: n.Cw1h, Total: n.In + n.Cr + n.Cw},
			Output:       OutputTokens{Total: n.Out, Reasoning: n.Rsn},
			Unclassified: n.Unc,
		},
		Estimate: Estimate{
			CostUSD:             roundMoney(a.cost),
			CostWithoutCacheUSD: roundMoney(a.without),
			SavedByCachingUSD:   roundMoney(a.saved),
			PricedRequests:      a.priced,
			UnpricedRequests:    a.unpriced,
			UnpricedTokens:      a.unpricedTokens,
			FloatingRequests:    a.floating,
		},
		ReportedCost: ReportedCost{
			USD:             roundMoney(float64(n.Cost+n.Ucost) / 1e9),
			BYOKUpstreamUSD: roundMoney(float64(n.Ucost) / 1e9),
			Requests:        n.Costn,
		},
	}
	if input := n.In + n.Cr + n.Cw; input > 0 {
		rate := roundTo(float64(n.Cr)/float64(input), 1e4)
		m.CacheHitRate = &rate
	}
	return m
}

// bucket is one row of a breakdown being built.
type bucket struct {
	values []string // the dimension values, in by order
	acc    accumulator
}

// modelAcc is one provider and model of the model list being built.
type modelAcc struct {
	provider, model   string
	requests, tokens  int64
	priceDay          string
	price             *Price
	recorded, current int
}

func dimensionValue(dimension, day string, k rowKey) string {
	switch dimension {
	case "day":
		return day
	case "month":
		return day[:7]
	case "kind":
		return k.k
	case "provider":
		return k.p
	case "host":
		return k.h
	case "model":
		return k.m
	default: // key
		return k.c
	}
}

func (req Request) passes(k rowKey) bool {
	match := func(filter *string, value string) bool { return filter == nil || *filter == value }
	return match(req.Kind, k.k) && match(req.Provider, k.p) && match(req.Host, k.h) && match(req.Model, k.m) && match(req.Key, k.c)
}

// build sums the rows of the range into the totals, the breakdowns and the model list
// (03-endpoint.md section 3, steps 4 and 5).
func build(req Request, days []queryDay, book *priceBook) *Response {
	answer := &Response{Breakdowns: []Breakdown{}, Models: []ModelPrice{}, ReportedCostHosts: []string{}}
	var totals accumulator
	groups := make([]map[string]*bucket, len(req.Groups))
	for i := range groups {
		groups[i] = map[string]*bucket{}
	}
	models := map[string]*modelAcc{}
	hosts := map[string]bool{}
	tails := map[string]string{} // key id -> tail of the latest day that has the id

	for _, qd := range days {
		matched := false
		for id, tail := range qd.rows.keys {
			tails[id] = tail // days come oldest first, so the latest day wins
		}
		for _, k := range qd.rows.sortedKeys() {
			if !req.passes(k) {
				continue
			}
			matched = true
			r := qd.rows.rows[k]
			var (
				p              price
				ref            priceRef
				valued, recent bool
			)
			if dayPrice, ok := qd.rows.prices[r.pm]; r.pm != "" && ok && dayPrice.usable() {
				p, valued = dayPrice, true
				ref = priceRef{ID: r.pm, Source: dayPrice.Src, Match: r.pmm}
			} else if !k.folded() {
				ref, p, valued = book.resolvePrice(k.p, k.m)
				recent = valued
			}
			var cost, without float64
			if valued {
				cost, without = amounts(r.n, p)
			}
			totals.add(r.n, valued, recent, cost, without)
			for i, by := range req.Groups {
				values := make([]string, len(by))
				for j, dimension := range by {
					values[j] = dimensionValue(dimension, qd.day, k)
				}
				id := strings.Join(values, "\x00")
				b := groups[i][id]
				if b == nil {
					b = &bucket{values: values}
					groups[i][id] = b
				}
				b.acc.add(r.n, valued, recent, cost, without)
			}
			pair := k.p + "\x00" + k.m
			ma := models[pair]
			if ma == nil {
				ma = &modelAcc{provider: k.p, model: k.m}
				models[pair] = ma
			}
			ma.requests += r.n.Req
			ma.tokens += r.n.tokens()
			if valued && !k.folded() {
				if recent {
					ma.current++
				} else {
					ma.recorded++
				}
				if qd.day >= ma.priceDay {
					ma.priceDay = qd.day
					ma.price = &Price{Source: ref.Source, ID: ref.ID, Match: ref.Match, InputPerMTok: p.In, OutputPerMTok: p.Out,
						CacheReadPerMTok: p.Cr, CacheWritePerMTok: p.Cw, CacheWrite1hPerMTok: p.Cw1h}
				}
			}
			if r.n.Costn > 0 {
				hosts[k.h] = true
			}
		}
		if matched && qd.rows.folded {
			answer.Recording.FoldedDays++
		}
	}

	answer.Totals = totals.metrics()
	for i, by := range req.Groups {
		answer.Breakdowns = append(answer.Breakdowns, breakdownOf(by, groups[i], tails))
	}
	answer.Models, answer.ModelsTruncated = modelList(models)
	answer.ReportedCostHosts = slices.Sorted(maps.Keys(hosts))
	if answer.ReportedCostHosts == nil {
		answer.ReportedCostHosts = []string{}
	}
	return answer
}

func breakdownOf(by []string, buckets map[string]*bucket, tails map[string]string) Breakdown {
	all := slices.Collect(maps.Values(buckets))
	byValues := func(a, b *bucket) int {
		for i := range a.values {
			if c := cmp.Compare(a.values[i], b.values[i]); c != 0 {
				return c
			}
		}
		return 0
	}
	mostRequests := func(a, b *bucket) int {
		return cmp.Or(cmp.Compare(b.acc.n.Req, a.acc.n.Req), byValues(a, b))
	}
	out := Breakdown{By: slices.Clone(by), Rows: []BreakdownRow{}}
	if len(all) > maxBreakdownRows {
		slices.SortFunc(all, mostRequests)
		all, out.Truncated = all[:maxBreakdownRows], true
	}
	timeIndex := slices.IndexFunc(by, func(d string) bool { return d == "day" || d == "month" })
	slices.SortFunc(all, func(a, b *bucket) int {
		if timeIndex >= 0 {
			if c := cmp.Compare(a.values[timeIndex], b.values[timeIndex]); c != 0 {
				return c
			}
		}
		return mostRequests(a, b)
	})
	for _, b := range all {
		row := BreakdownRow{Metrics: b.acc.metrics()}
		for i, dimension := range by {
			value := b.values[i]
			switch dimension {
			case "day":
				row.Day = &value
			case "month":
				row.Month = &value
			case "kind":
				row.Kind = &value
			case "provider":
				row.Provider = &value
			case "host":
				row.Host = &value
			case "model":
				row.Model = &value
			case "key":
				key := ClientKey{ID: value}
				if tail := tails[value]; tail != "" {
					key.Tail = &tail
				}
				row.Key = &key
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func modelList(models map[string]*modelAcc) ([]ModelPrice, bool) {
	all := slices.Collect(maps.Values(models))
	slices.SortFunc(all, func(a, b *modelAcc) int {
		return cmp.Or(cmp.Compare(b.tokens, a.tokens), cmp.Compare(b.requests, a.requests), cmp.Compare(a.provider, b.provider), cmp.Compare(a.model, b.model))
	})
	truncated := len(all) > maxModels
	if truncated {
		all = all[:maxModels]
	}
	out := make([]ModelPrice, 0, len(all))
	for _, m := range all {
		entry := ModelPrice{Provider: m.provider, Model: m.model, Requests: m.requests, Tokens: m.tokens}
		if m.price != nil && m.model != otherValue {
			p := *m.price
			switch {
			case m.current == 0:
				p.Basis = "recorded"
			case m.recorded == 0:
				p.Basis = "current"
			default:
				p.Basis = "mixed"
			}
			entry.Price = &p
		}
		out = append(out, entry)
	}
	return out, truncated
}
