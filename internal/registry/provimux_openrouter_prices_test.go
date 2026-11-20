package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

const provimuxPricedCatalog = `{"data":[
	{"id":"Anthropic/Claude-Opus-5.5","context_length":1000000,
	 "pricing":{"prompt":"0.000004","completion":"0.00002","input_cache_read":"0.0000002","input_cache_write":"0.000005","input_cache_write_1h":"0.000008",
	            "overrides":[{"min_prompt_tokens":200000,"prompt":"0.000008"}]}},
	{"id":"openai/gpt-6.2-sol","context_length":400000,"pricing":{"prompt":"0.000002","completion":"0.00001","input_cache_read":"0.0000001"}},
	{"id":"vendor/free:free","pricing":{"prompt":"0","completion":"0"}},
	{"id":"openrouter/auto","context_length":2000000,"pricing":{"prompt":"-1","completion":"-1"}},
	{"id":"vendor/half","pricing":{"prompt":"0.000001","completion":"-1"}},
	{"id":"vendor/bad-cache","pricing":{"prompt":"0.000001","completion":"0.000002","input_cache_read":"-0.1","input_cache_write":"x"}},
	{"id":"vendor/unpriced","context_length":1000},
	{"pricing":{"prompt":"1","completion":"1"}}]}`

func provimuxFloat(v float64) *float64 { return &v }

func provimuxCurrentPrices(t *testing.T) (map[string]ProvimuxOpenRouterPrice, time.Time) {
	t.Helper()
	provimuxOpenRouterPrices.mu.Lock()
	defer provimuxOpenRouterPrices.mu.Unlock()
	return provimuxOpenRouterPrices.prices, provimuxOpenRouterPrices.fetchedAt
}

// provimuxIsolate gives the test its own catalog and price state, restored afterwards.
func provimuxIsolate(t *testing.T, catalog *openRouterCatalog) {
	t.Helper()
	oldCatalog := openRouterModels
	if catalog != nil {
		openRouterModels = catalog
	}
	restore := ProvimuxSetOpenRouterPricesForTest(nil, time.Time{})
	t.Cleanup(func() {
		restore()
		openRouterModels = oldCatalog
	})
}

func TestProvimuxParsesTheFivePrices(t *testing.T) {
	got := provimuxParseOpenRouterPrices([]byte(provimuxPricedCatalog))
	opus := got["anthropic/claude-opus-5.5"]
	want := ProvimuxOpenRouterPrice{Input: 4, Output: 20, CacheRead: provimuxFloat(0.2), CacheWrite: provimuxFloat(5), CacheWrite1h: provimuxFloat(8)}
	if !reflect.DeepEqual(opus, want) {
		t.Fatalf("opus = %+v, want %+v (per million, overrides ignored)", opus, want)
	}
	sol := got["openai/gpt-6.2-sol"]
	if sol.Input != 2 || sol.Output != 10 || sol.CacheRead == nil || *sol.CacheRead != 0.1 || sol.CacheWrite != nil || sol.CacheWrite1h != nil {
		t.Fatalf("sol = %+v: a missing cache field must be nil", sol)
	}
}

func TestProvimuxKeepsWhatHasAPrice(t *testing.T) {
	got := provimuxParseOpenRouterPrices([]byte(provimuxPricedCatalog))
	if free, ok := got["vendor/free:free"]; !ok || free.Input != 0 || free.Output != 0 {
		t.Fatalf("zero is a price: %+v %v", free, ok)
	}
	for _, id := range []string{"openrouter/auto", "vendor/half", "vendor/unpriced"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s must be dropped (negative or missing prompt/completion)", id)
		}
	}
	bad := got["vendor/bad-cache"]
	if bad.CacheRead != nil || bad.CacheWrite != nil {
		t.Errorf("a negative or unparsable cache price is missing: %+v", bad)
	}
	// vendor/free:free has neither limits nor efforts: parseOpenRouterModels drops it, the prices keep it.
	models, err := parseOpenRouterModels([]byte(provimuxPricedCatalog))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := models["vendor/free:free"]; ok {
		t.Fatal("premise: parseOpenRouterModels drops a model without limits or efforts")
	}
	if len(got) != 4 {
		t.Fatalf("kept %d models, want 4: %+v", len(got), got)
	}
}

func TestProvimuxABadBodyChangesNothing(t *testing.T) {
	provimuxIsolate(t, nil)
	provimuxKeepOpenRouterPrices([]byte(provimuxPricedCatalog))
	before, beforeAt := provimuxCurrentPrices(t)
	if len(before) == 0 || beforeAt.IsZero() {
		t.Fatal("prices not kept")
	}
	for _, body := range []string{`not json`, `{"nope":[]}`, `{"data":[{"id":"a/b","pricing":{"prompt":"-1","completion":"1"}}]}`} {
		provimuxKeepOpenRouterPrices([]byte(body))
		after, afterAt := provimuxCurrentPrices(t)
		if !reflect.DeepEqual(after, before) || !afterAt.Equal(beforeAt) {
			t.Fatalf("body %q changed the prices", body)
		}
	}
}

func TestProvimuxTheDownloadKeepsPrices(t *testing.T) {
	provimuxIsolate(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(provimuxPricedCatalog))
	}))
	defer server.Close()
	oldURL := openRouterModelsURL
	openRouterModelsURL = server.URL
	defer func() { openRouterModelsURL = oldURL }()

	got, err := fetchOpenRouterModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := parseOpenRouterModels([]byte(provimuxPricedCatalog))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the added line changed fetchOpenRouterModels' result: %+v", got)
	}
	prices, at := provimuxCurrentPrices(t)
	if len(prices) != 4 || at.IsZero() {
		t.Fatalf("prices after the download: %d at %v", len(prices), at)
	}
}

func TestProvimuxAsksByTheCatalogsRules(t *testing.T) {
	t.Run("policy says no", func(t *testing.T) {
		var fetches atomic.Int32
		catalog, _, _ := testOpenRouterCatalog(func(context.Context) (map[string]OpenRouterModel, error) {
			fetches.Add(1)
			return nil, nil
		})
		catalog.remote = func() (context.Context, bool) { return nil, false }
		provimuxIsolate(t, catalog)
		if _, _, remote := ProvimuxOpenRouterPrices(); remote {
			t.Fatal("remote must be false under --local-model")
		}
		if _, _, remote := ProvimuxOpenRouterPricesKept(); remote {
			t.Fatal("Kept: remote must be false under --local-model")
		}
		if fetches.Load() != 0 || catalog.isFetching() {
			t.Fatal("no fetch may start when the policy says no")
		}
	})
	t.Run("fresh catalog", func(t *testing.T) {
		var fetches atomic.Int32
		catalog, now, _ := testOpenRouterCatalog(func(context.Context) (map[string]OpenRouterModel, error) {
			fetches.Add(1)
			return nil, nil
		})
		catalog.models = map[string]OpenRouterModel{"a/b": {ContextLength: 1}}
		catalog.fetchedAt = *now
		provimuxIsolate(t, catalog)
		if _, _, remote := ProvimuxOpenRouterPrices(); !remote {
			t.Fatal("remote must be true")
		}
		if fetches.Load() != 0 || catalog.isFetching() {
			t.Fatal("a fresh catalog is not fetched")
		}
	})
	t.Run("stale catalog", func(t *testing.T) {
		var fetches atomic.Int32
		release := make(chan struct{})
		catalog, now, refreshed := testOpenRouterCatalog(func(context.Context) (map[string]OpenRouterModel, error) {
			fetches.Add(1)
			<-release
			return map[string]OpenRouterModel{"a/b": {ContextLength: 2}}, nil
		})
		catalog.models = map[string]OpenRouterModel{"a/b": {ContextLength: 1}}
		catalog.fetchedAt = now.Add(-ModelsRefreshInterval - time.Minute)
		provimuxIsolate(t, catalog)
		if _, _, remote := ProvimuxOpenRouterPricesKept(); !remote || catalog.isFetching() {
			t.Fatal("Kept never asks for a refresh")
		}
		ProvimuxOpenRouterPrices() // returns while the fetch is blocked
		ProvimuxOpenRouterPrices()
		if !catalog.isFetching() {
			t.Fatal("a stale catalog must start a background fetch")
		}
		close(release)
		waitRefreshed(t, refreshed)
		if fetches.Load() != 1 {
			t.Fatalf("fetches = %d, want exactly 1", fetches.Load())
		}
	})
}

func TestProvimuxRegistersNoInterest(t *testing.T) {
	catalog, _, refreshed := testOpenRouterCatalog(func(context.Context) (map[string]OpenRouterModel, error) {
		return map[string]OpenRouterModel{"a/b": {ContextLength: 3}}, nil
	})
	provimuxIsolate(t, catalog)
	ProvimuxOpenRouterPrices()
	waitRefreshed(t, refreshed)
	catalog.mu.Lock()
	interested := len(catalog.interested)
	catalog.mu.Unlock()
	if interested != 0 {
		t.Fatalf("ProvimuxOpenRouterPrices registered %d providers' interest", interested)
	}
}

func TestProvimuxPricesDoNotReRegisterModels(t *testing.T) {
	a, err := parseOpenRouterModels([]byte(provimuxPricedCatalog))
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseOpenRouterModels([]byte(`{"data":[
	{"id":"Anthropic/Claude-Opus-5.5","context_length":1000000,"pricing":{"prompt":"0.000009","completion":"0.00009"}},
	{"id":"openai/gpt-6.2-sol","context_length":400000,"pricing":{"prompt":"0.000001","completion":"0.00001"}},
	{"id":"openrouter/auto","context_length":2000000,"pricing":{"prompt":"-1","completion":"-1"}},
	{"id":"vendor/unpriced","context_length":1000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !sameOpenRouterModels(a, b) {
		t.Fatal("catalogs that differ only in a price must be the same for sameOpenRouterModels")
	}
}
