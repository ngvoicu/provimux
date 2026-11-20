package savings

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/MoeForge/moego/json"
)

// maxRows is the row limit of a day (01-recording.md section 5, "The row limit"). Rows with model and
// key ~other come on top of it, one per kind, provider and host.
const maxRows = 2000

// Store keys (01-recording.md section 5, "Keys").
const (
	keyPrefix         = "provimux:savings:v1:"
	metaKey           = keyPrefix + "meta"
	openRouterListKey = keyPrefix + "prices:openrouter"
)

func dayKey(day string) string { return keyPrefix + "day:" + day }

// rowKey is what makes a row unique in a day: kind, provider, host, model and client key id.
type rowKey struct{ k, p, h, m, c string }

func (k rowKey) folded() bool { return k.m == otherValue }

// row is one row of a day: its counters and the price it was first valued with.
type row struct {
	pm, pmm string
	n       counters
}

// dayValue is the stored value of one day: schemas/day.schema.json.
type dayValue struct {
	V         int               `json:"v"`
	Day       string            `json:"day"`
	TZ        string            `json:"tz"`
	UTCOffset int               `json:"utc_offset"`
	Folded    bool              `json:"folded,omitempty"`
	Keys      map[string]string `json:"keys"`
	Prices    map[string]price  `json:"prices"`
	Rows      []storedRow       `json:"rows"`
}

type storedRow struct {
	K   string   `json:"k"`
	P   string   `json:"p"`
	H   string   `json:"h,omitempty"`
	M   string   `json:"m"`
	C   string   `json:"c,omitempty"`
	Pm  string   `json:"pm,omitempty"`
	Pmm string   `json:"pmm,omitempty"`
	N   counters `json:"n"`
}

// dayRows is a day's content as the store holds it (or will, once written).
type dayRows struct {
	tz      string
	offset  int
	folded  bool
	keys    map[string]string
	prices  map[string]price
	rows    map[rowKey]*row
	regular int // rows that are not folded
}

func newDayRows(tz string, offset int) *dayRows {
	return &dayRows{tz: tz, offset: offset, keys: map[string]string{}, prices: map[string]price{}, rows: map[rowKey]*row{}}
}

func (d *dayRows) clone() *dayRows {
	out := &dayRows{tz: d.tz, offset: d.offset, folded: d.folded, keys: maps.Clone(d.keys), prices: maps.Clone(d.prices),
		rows: make(map[rowKey]*row, len(d.rows)), regular: d.regular}
	for k, r := range d.rows {
		copied := *r
		out.rows[k] = &copied
	}
	return out
}

// sortedKeys orders rows by kind, provider, host, model and key, so a value encodes the same bytes
// for the same totals.
func (d *dayRows) sortedKeys() []rowKey {
	keys := slices.Collect(maps.Keys(d.rows))
	slices.SortFunc(keys, compareRowKeys)
	return keys
}

func compareRowKeys(a, b rowKey) int {
	return cmp.Or(cmp.Compare(a.k, b.k), cmp.Compare(a.p, b.p), cmp.Compare(a.h, b.h), cmp.Compare(a.m, b.m), cmp.Compare(a.c, b.c))
}

func (d *dayRows) encode(day string) ([]byte, error) {
	value := dayValue{V: 1, Day: day, TZ: d.tz, UTCOffset: d.offset, Folded: d.folded, Keys: d.keys, Prices: d.prices,
		Rows: make([]storedRow, 0, len(d.rows))}
	if value.Keys == nil {
		value.Keys = map[string]string{}
	}
	if value.Prices == nil {
		value.Prices = map[string]price{}
	}
	for _, k := range d.sortedKeys() {
		r := d.rows[k]
		value.Rows = append(value.Rows, storedRow{K: k.k, P: k.p, H: k.h, M: k.m, C: k.c, Pm: r.pm, Pmm: r.pmm, N: r.n})
	}
	return json.Marshal(value)
}

// decodeDay reads a stored day. Properties it does not know are ignored; an absent counter is 0.
func decodeDay(data []byte) (*dayRows, error) {
	var value dayValue
	if errDecode := json.Unmarshal(data, &value); errDecode != nil {
		return nil, errDecode
	}
	if value.V != 1 {
		return nil, fmt.Errorf("unknown version %d", value.V)
	}
	d := newDayRows(value.TZ, value.UTCOffset)
	d.folded = value.Folded
	maps.Copy(d.keys, value.Keys)
	maps.Copy(d.prices, value.Prices)
	for _, stored := range value.Rows {
		k := rowKey{k: stored.K, p: stored.P, h: stored.H, m: stored.M, c: stored.C}
		if existing, ok := d.rows[k]; ok {
			existing.n.add(stored.N)
			continue
		}
		d.rows[k] = &row{pm: stored.Pm, pmm: stored.Pmm, n: stored.N}
		if !k.folded() {
			d.regular++
		}
	}
	return d, nil
}

// pendingKey is a combination recorded since the last write: kind, provider, host, model and the
// client key itself, because its id needs the stored secret (01-recording.md section 7).
type pendingKey struct {
	k, p, h, m, key string
	folded          bool // over the row limit: goes to model and key ~other
}

func comparePendingKeys(a, b pendingKey) int {
	return cmp.Or(cmp.Compare(a.k, b.k), cmp.Compare(a.p, b.p), cmp.Compare(a.h, b.h), cmp.Compare(a.m, b.m),
		cmp.Compare(a.key, b.key), boolCompare(a.folded, b.folded))
}

func boolCompare(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// mergePending adds pending to the rows by key id (step 2 of 01-recording.md section 5). A new
// combination beyond the row limit goes to the ~other row of its kind, provider and host.
func (d *dayRows) mergePending(pending map[pendingKey]*counters, secret []byte) {
	keys := slices.Collect(maps.Keys(pending))
	slices.SortFunc(keys, comparePendingKeys)
	for _, pk := range keys {
		k := rowKey{k: pk.k, p: pk.p, h: pk.h, m: pk.m}
		if pk.folded {
			k.m, k.c = otherValue, otherValue
		} else if pk.key != "" {
			k.c = keyID(secret, pk.key)
			d.keys[k.c] = keyTail(pk.key)
		}
		if _, ok := d.rows[k]; !ok && !k.folded() && d.regular >= maxRows {
			k.m, k.c = otherValue, otherValue
		}
		if k.folded() {
			d.folded = true
		}
		r, ok := d.rows[k]
		if !ok {
			r = &row{}
			d.rows[k] = r
			if !k.folded() {
				d.regular++
			}
		}
		r.n.add(*pending[pk])
	}
}

// assignPrices gives every row without a price the one in use now, and the day that price unless it
// already has one for that id (02-prices.md section 5). Once set, neither changes.
func (d *dayRows) assignPrices(book *priceBook) {
	for k, r := range d.rows {
		if r.pm != "" || k.folded() {
			continue
		}
		ref, p, ok := book.resolvePrice(k.p, k.m)
		if !ok {
			continue
		}
		r.pm, r.pmm = ref.ID, ref.Match
		if _, has := d.prices[ref.ID]; !has {
			d.prices[ref.ID] = p
		}
	}
}
