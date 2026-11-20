package savings

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Price sources, as the stored day's prices[].src and the answer's price.source say them.
const (
	sourceOpenRouter = "openrouter"
	sourceProvimux   = "provimux"
)

// Kinds of match, as rows' pmm and the answer's price.match say them (02-prices.md section 4).
const (
	matchTable      = "table"
	matchExact      = "exact"
	matchVariant    = "variant"
	matchNormalized = "normalized"
)

// openRouterModelsURL is the list the answer names (prices.openrouter.url).
const openRouterModelsURL = "https://openrouter.ai/api/v1/models"

// price is one model's list price in US dollars per million tokens, as a day stores it
// (schemas/day.schema.json, $defs/price). Cr, Cw and Cw1h are nil when the source publishes none.
type price struct {
	Src  string   `json:"src"`
	In   float64  `json:"in"`
	Out  float64  `json:"out"`
	Cr   *float64 `json:"cr,omitempty"`
	Cw   *float64 `json:"cw,omitempty"`
	Cw1h *float64 `json:"cw1h,omitempty"`
}

// usable is false for a negative price, which is never used.
func (p price) usable() bool {
	if p.In < 0 || p.Out < 0 {
		return false
	}
	for _, v := range []*float64{p.Cr, p.Cw, p.Cw1h} {
		if v != nil && *v < 0 {
			return false
		}
	}
	return true
}

// cleanOptional drops a negative cache price, which then counts as not published.
func cleanOptional(v *float64) *float64 {
	if v == nil || *v < 0 {
		return nil
	}
	out := *v
	return &out
}

func priceFromOpenRouter(p registry.ProvimuxOpenRouterPrice) price {
	return price{
		Src:  sourceOpenRouter,
		In:   p.Input,
		Out:  p.Output,
		Cr:   cleanOptional(p.CacheRead),
		Cw:   cleanOptional(p.CacheWrite),
		Cw1h: cleanOptional(p.CacheWrite1h),
	}
}

// priceRef says where a price came from: the price id (a row's pm), the source and the match.
type priceRef struct {
	ID     string
	Source string
	Match  string
}

// tablePrice is a price of Provimux's table (schemas/prices-table.schema.json, $defs/price).
type tablePrice struct {
	In   *float64 `json:"in"`
	Out  *float64 `json:"out"`
	Cr   *float64 `json:"cr,omitempty"`
	Cw   *float64 `json:"cw,omitempty"`
	Cw1h *float64 `json:"cw1h,omitempty"`
}

// tableEntry is one entry of Provimux's table.
type tableEntry struct {
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Price    *tablePrice `json:"price,omitempty"`
	SameAs   string      `json:"same_as,omitempty"`
	Source   string      `json:"source"`
	Note     string      `json:"note,omitempty"`
}

// priceTable is Provimux's own table, internal/provimux/savings/prices.json (02-prices.md section 3).
type priceTable struct {
	V       int          `json:"v"`
	Updated string       `json:"updated"`
	Models  []tableEntry `json:"models"`

	byPair map[string]tableEntry
}

//go:embed prices.json
var embeddedPriceTable []byte

// defaultTable is the embedded table. A table that does not parse is a build error caught by the
// tests; at run time it is then empty, so every model it would have priced is unpriced.
var defaultTable = mustParseTable(embeddedPriceTable)

func mustParseTable(data []byte) *priceTable {
	table, errParse := parseTable(data)
	if errParse != nil {
		return &priceTable{V: 1, byPair: map[string]tableEntry{}}
	}
	return table
}

func parseTable(data []byte) (*priceTable, error) {
	var table priceTable
	if errDecode := json.Unmarshal(data, &table); errDecode != nil {
		return nil, fmt.Errorf("prices.json: %w", errDecode)
	}
	table.byPair = make(map[string]tableEntry, len(table.Models))
	for _, entry := range table.Models {
		table.byPair[pairKey(entry.Provider, entry.Model)] = entry
	}
	return &table, nil
}

func pairKey(provider, model string) string { return provider + "\x00" + model }

// tableID is the price id of a table entry with a price of its own. OpenRouter ids never start
// with "provimux:", so a day's prices map holds both without a clash.
func tableID(entry tableEntry) string {
	return sourceProvimux + ":" + entry.Provider + "/" + entry.Model
}

func (t *priceTable) entry(provider, model string) (tableEntry, bool) {
	if t == nil {
		return tableEntry{}, false
	}
	if entry, ok := t.byPair[pairKey(provider, model)]; ok {
		return entry, true
	}
	entry, ok := t.byPair[pairKey("*", model)]
	return entry, ok
}

// priceBook is the prices in use: OpenRouter's list (downloaded or the saved copy, whichever was
// fetched later) and Provimux's table.
type priceBook struct {
	openRouter map[string]registry.ProvimuxOpenRouterPrice
	fetchedAt  time.Time
	remote     bool
	table      *priceTable
}

// openRouterPrice is the list's price for id, when it has a usable one.
func (b *priceBook) openRouterPrice(id string) (price, bool) {
	if b == nil || b.openRouter == nil {
		return price{}, false
	}
	listed, ok := b.openRouter[id]
	if !ok {
		return price{}, false
	}
	p := priceFromOpenRouter(listed)
	if !p.usable() {
		return price{}, false
	}
	return p, true
}

// resolvePrice is 02-prices.md section 4: the first step that answers ends the search.
func (b *priceBook) resolvePrice(provider, model string) (ref priceRef, p price, ok bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || model == otherValue {
		return priceRef{}, price{}, false
	}
	// 1. Provimux's table: a price, or an OpenRouter id to take the price of.
	if entry, found := b.table.entry(provider, model); found {
		if entry.Price != nil {
			if entry.Price.In == nil || entry.Price.Out == nil {
				return priceRef{}, price{}, false
			}
			p = price{Src: sourceProvimux, In: *entry.Price.In, Out: *entry.Price.Out,
				Cr: cleanOptional(entry.Price.Cr), Cw: cleanOptional(entry.Price.Cw), Cw1h: cleanOptional(entry.Price.Cw1h)}
			if !p.usable() {
				return priceRef{}, price{}, false
			}
			return priceRef{ID: tableID(entry), Source: sourceProvimux, Match: matchTable}, p, true
		}
		id := strings.ToLower(strings.TrimSpace(entry.SameAs))
		if p, ok = b.openRouterPrice(id); ok {
			return priceRef{ID: id, Source: sourceOpenRouter, Match: matchTable}, p, true
		}
		return priceRef{}, price{}, false // no later step is tried
	}
	// 2. The list has the model as an id.
	if p, ok = b.openRouterPrice(model); ok {
		return priceRef{ID: model, Source: sourceOpenRouter, Match: matchExact}, p, true
	}
	// 3. A routing suffix after the last ":".
	if i := strings.LastIndexByte(model, ':'); i > 0 {
		id := model[:i]
		if p, ok = b.openRouterPrice(id); ok {
			return priceRef{ID: id, Source: sourceOpenRouter, Match: matchVariant}, p, true
		}
	}
	// 4. The documented rewriting.
	for _, id := range normalizedCandidates(model) {
		if p, ok = b.openRouterPrice(id); ok {
			return priceRef{ID: id, Source: sourceOpenRouter, Match: matchNormalized}, p, true
		}
	}
	return priceRef{}, price{}, false
}

// otherValue is the model and key of rows folded at the row limit.
const otherValue = "~other"
