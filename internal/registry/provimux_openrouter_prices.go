package registry

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// Provimux layer: OpenRouter's list prices for the Savings page (.github/provimux/design/savings/02-prices.md
// section 2). They are parsed from the bytes fetchOpenRouterModels already downloads; OpenRouterModel,
// parseOpenRouterModels and sameOpenRouterModels do not change, so a price change registers no model again.

// ProvimuxOpenRouterPrice is one model's list price at OpenRouter, in US dollars per million tokens.
// CacheRead, CacheWrite and CacheWrite1h are nil when the list publishes none.
type ProvimuxOpenRouterPrice struct {
	Input, Output                       float64
	CacheRead, CacheWrite, CacheWrite1h *float64
}

// provimuxPricesNow is the clock of fetched_at; tests replace it.
var provimuxPricesNow = time.Now

var provimuxOpenRouterPrices struct {
	mu        sync.Mutex
	prices    map[string]ProvimuxOpenRouterPrice
	fetchedAt time.Time
}

// provimuxKeepOpenRouterPrices keeps the prices of a downloaded catalog and the time. Called by
// fetchOpenRouterModels with the bytes it read. A body that is not the catalog, or that prices no
// model, leaves the previous prices in place.
func provimuxKeepOpenRouterPrices(data []byte) {
	prices := provimuxParseOpenRouterPrices(data)
	if len(prices) == 0 {
		return
	}
	now := provimuxPricesNow()
	provimuxOpenRouterPrices.mu.Lock()
	provimuxOpenRouterPrices.prices = prices
	provimuxOpenRouterPrices.fetchedAt = now
	provimuxOpenRouterPrices.mu.Unlock()
}

// provimuxParseOpenRouterPrices reads data[].pricing. A model is kept when prompt and completion both
// parse and neither is negative; each cache price is kept when it parses and is not negative.
// pricing.overrides is not read.
func provimuxParseOpenRouterPrices(data []byte) map[string]ProvimuxOpenRouterPrice {
	if !ojson.ValidBytes(data) {
		return nil
	}
	models := ojson.GetBytes(data, "data")
	if !models.IsArray() {
		return nil
	}
	out := make(map[string]ProvimuxOpenRouterPrice)
	for _, model := range models.Array() {
		id := strings.ToLower(strings.TrimSpace(model.Get("id").String()))
		if id == "" {
			continue
		}
		pricing := model.Get("pricing")
		input, okInput := provimuxPerMillion(pricing.Get("prompt"))
		output, okOutput := provimuxPerMillion(pricing.Get("completion"))
		if !okInput || !okOutput {
			continue
		}
		price := ProvimuxOpenRouterPrice{Input: input, Output: output}
		if v, ok := provimuxPerMillion(pricing.Get("input_cache_read")); ok {
			price.CacheRead = &v
		}
		if v, ok := provimuxPerMillion(pricing.Get("input_cache_write")); ok {
			price.CacheWrite = &v
		}
		if v, ok := provimuxPerMillion(pricing.Get("input_cache_write_1h")); ok {
			price.CacheWrite1h = &v
		}
		out[id] = price
	}
	return out
}

// provimuxPerMillion turns OpenRouter's dollars per token (a decimal string, or a number) into dollars
// per million tokens. Missing, unparsable or negative values are not a price.
func provimuxPerMillion(value ojson.Result) (float64, bool) {
	if !value.Exists() {
		return 0, false
	}
	var perToken float64
	switch value.Type {
	case ojson.Number:
		perToken = value.Float()
	case ojson.String:
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
		if errParse != nil {
			return 0, false
		}
		perToken = parsed
	default:
		return 0, false
	}
	if perToken < 0 || perToken != perToken {
		return 0, false
	}
	// Round away float noise of the multiplication (0.000003 * 1e6 = 2.9999999999999996).
	perMillion, errRound := strconv.ParseFloat(strconv.FormatFloat(perToken*1_000_000, 'f', 9, 64), 64)
	if errRound != nil {
		return 0, false
	}
	return perMillion, true
}

// ProvimuxOpenRouterPrices returns the prices of the last downloaded catalog, keyed by lower-case
// model id, and when it was downloaded; nil and the zero time when none was in this process.
// remote is false when the catalog may not be downloaded (--local-model, home mode).
// It asks the catalog for a refresh under the catalog's own rules: in the background, when its
// copy is older than ModelsRefreshInterval, never when remote is false. It never blocks.
func ProvimuxOpenRouterPrices() (prices map[string]ProvimuxOpenRouterPrice, fetchedAt time.Time, remote bool) {
	c := openRouterModels
	if c.remote != nil {
		_, remote = c.remote()
	}
	// An empty provider registers no interest and an empty id finds nothing: this only starts the
	// catalog's own background refresh when it is due.
	c.lookup("", "")
	provimuxOpenRouterPrices.mu.Lock()
	defer provimuxOpenRouterPrices.mu.Unlock()
	return provimuxOpenRouterPrices.prices, provimuxOpenRouterPrices.fetchedAt, remote
}

// ProvimuxOpenRouterPricesKept returns what ProvimuxOpenRouterPrices returns without asking for a
// refresh: for readers that must start no download (the Savings endpoint).
func ProvimuxOpenRouterPricesKept() (prices map[string]ProvimuxOpenRouterPrice, fetchedAt time.Time, remote bool) {
	if c := openRouterModels; c.remote != nil {
		_, remote = c.remote()
	}
	provimuxOpenRouterPrices.mu.Lock()
	defer provimuxOpenRouterPrices.mu.Unlock()
	return provimuxOpenRouterPrices.prices, provimuxOpenRouterPrices.fetchedAt, remote
}

// ProvimuxSetOpenRouterPricesForTest installs prices for a test and returns the function that
// restores the previous ones. It starts no download.
func ProvimuxSetOpenRouterPricesForTest(prices map[string]ProvimuxOpenRouterPrice, fetchedAt time.Time) func() {
	var installed map[string]ProvimuxOpenRouterPrice
	if prices != nil {
		installed = make(map[string]ProvimuxOpenRouterPrice, len(prices))
		for id, price := range prices {
			installed[strings.ToLower(strings.TrimSpace(id))] = price
		}
	}
	provimuxOpenRouterPrices.mu.Lock()
	oldPrices, oldFetchedAt := provimuxOpenRouterPrices.prices, provimuxOpenRouterPrices.fetchedAt
	provimuxOpenRouterPrices.prices, provimuxOpenRouterPrices.fetchedAt = installed, fetchedAt
	provimuxOpenRouterPrices.mu.Unlock()
	return func() {
		provimuxOpenRouterPrices.mu.Lock()
		provimuxOpenRouterPrices.prices, provimuxOpenRouterPrices.fetchedAt = oldPrices, oldFetchedAt
		provimuxOpenRouterPrices.mu.Unlock()
	}
}
