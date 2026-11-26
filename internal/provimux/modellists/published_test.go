package modellists

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

// Every test talks to a test server; none reaches providers.dev.

func testListing(provider string, models ...map[string]any) map[string]any {
	if models == nil {
		models = []map[string]any{}
	}
	// date-safe: a fixture field; the package reads no clock: every time is an argument or a stored value.
	return map[string]any{"provider": provider, "basis": "listed", "listed_at": "2026-11-22", "client": "provimux",
		"evidence": []any{}, "model_count": len(models), "models": models}
}

func testPage(generated string, listings ...map[string]any) []byte {
	data, _ := json.Marshal(map[string]any{"meta": map[string]any{"dataset_version": "ds-" + generated, "generated_at": generated,
		"upstream_fetched_at": generated}, "total": len(listings), "limit": FullListingLimit, "offset": 0, "next_offset": nil, "data": listings})
	return data
}

func testModel(id string, maxOutput int) map[string]any {
	return map[string]any{"id": id, "name": strings.ToUpper(id), "context_window": nil, "max_output": maxOutput,
		"input_modalities": []string{"text"}, "output_modalities": []string{"text"}, "reasoning": nil, "aliases": []string{}}
}

func TestPublishedRequestURLIsTheSameForEveryUser(t *testing.T) {
	got := PublishedRequestURL("https://example.test/api/v1/listings")
	want := "https://example.test/api/v1/listings?limit=40&provider=antigravity%2Cclaude-subscription%2Cdevin%2Cgithub-copilot%2Cgoogle%2Cgoogle-vertex%2Ckimi-code-plan-global%2Cmeta-subscription%2Copenai-codex%2Cxai&view=full"
	if got != want {
		t.Fatalf("url = %s", got)
	}
	if len(ListingProviders()) > FullListingLimit {
		t.Fatal("the filter must fit one page in full")
	}
}

func TestFetchStatusesAndShapes(t *testing.T) {
	var status int
	var body []byte
	var etag string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		if status == http.StatusNotModified && r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	// date-safe: Fetch's time argument; the package reads no clock: every time is an argument or a stored value.
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	fetch := func(held string) (FetchResult, error) {
		return Fetch(context.Background(), server.Client(), server.URL, held, "provimux/test", now)
	}

	status, etag = http.StatusOK, `"v1"`
	// date-safe: a published page's generated_at, compared only with other documents' (never with a clock).
	body = testPage("2026-11-22T04:20:11Z", testListing("devin", testModel("kimi-k9", 32768)), testListing("openai-codex", testModel("gpt-9", 1)))
	result, err := fetch("")
	if err != nil || result.NotModified || result.Document == nil {
		t.Fatalf("200: %+v %v", result, err)
	}
	if result.Document.ETag != `"v1"` || result.Document.DatasetVersion != "ds-2026-11-22T04:20:11Z" || !result.Document.FetchedAt.Equal(now) {
		t.Fatalf("document = %+v", result.Document)
	}
	if got := *result.Document.Listings["devin"].Models[0].MaxOutput; got != 32768 {
		t.Fatalf("max_output = %d", got)
	}
	if len(result.Bad) != len(ListingProviders())-2 || strings.Contains(strings.Join(result.Bad, ","), "devin") {
		t.Fatalf("bad = %v", result.Bad)
	}

	status = http.StatusNotModified
	if result, err = fetch(`"v1"`); err != nil || !result.NotModified || result.Document != nil {
		t.Fatalf("304: %+v %v", result, err)
	}

	status, body = http.StatusInternalServerError, []byte("oops")
	_, err = fetch(`"v1"`)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Category != FailureHTTP || fetchErr.Status != 500 || strings.Contains(err.Error(), "oops") {
		t.Fatalf("500: %v", err)
	}

	for name, bad := range map[string][]byte{
		// date-safe: pages refused before their date matters; the package reads no clock: every time is an argument or a stored value.
		"cut off":    testPage("2026-11-22T04:20:11Z", testListing("devin", testModel("a", 1)))[:40],
		"not json":   []byte("<html>"),
		"no meta":    []byte(`{"total":0,"data":[]}`),
		"incomplete": []byte(`{"meta":{"dataset_version":"x","generated_at":"2026-11-22T04:20:11Z"},"total":5,"next_offset":40,"data":[]}`),
		"bad date":   []byte(`{"meta":{"dataset_version":"x","generated_at":"yesterday"},"total":0,"next_offset":null,"data":[]}`),
	} {
		status, body = http.StatusOK, bad
		if _, err = fetch(""); !errors.As(err, &fetchErr) || fetchErr.Category != FailureResponse {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestFetchBodyCutOffInTransfer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte(`{"meta":{"dataset_version":"x"`))
	}))
	defer server.Close()
	_, err := Fetch(context.Background(), server.Client(), server.URL, "", "provimux/test", time.Now())
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Category != FailureNetwork {
		t.Fatalf("cut-off body: %v", err)
	}
}

func TestFetchTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(" ", maxPublishedBytes+1)))
	}))
	defer server.Close()
	_, err := Fetch(context.Background(), server.Client(), server.URL, "", "provimux/test", time.Now())
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || fetchErr.Category != FailureResponse {
		t.Fatalf("too large: %v", err)
	}
}

func TestBadListingsAreCheckedOneByOne(t *testing.T) {
	// date-safe: a published page's generated_at, compared only with other documents' (never with a clock).
	page := []byte(`{"meta":{"dataset_version":"x","generated_at":"2026-11-22T04:20:11Z"},"total":4,"next_offset":null,"data":[
		{"provider":"devin","basis":"listed","models":[]},
		{"provider":"openai-codex","basis":"providers.dev","models":"not a list"},
		{"provider":"xai","basis":"models.dev","models":[{"id":""}]},
		{"provider":"google","basis":"models.dev","models":[{"id":"gemini-9","max_output":8192}]}]}`)
	doc, bad, err := parseListingsPage(page)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Listings) != 1 || doc.Listings["google"].Models[0].ID != "gemini-9" {
		t.Fatalf("listings = %+v", doc.Listings)
	}
	for _, provider := range []string{"devin", "openai-codex", "xai", "claude-subscription"} {
		found := false
		for _, name := range bad {
			found = found || name == provider
		}
		if !found {
			t.Fatalf("%s must be bad: %v", provider, bad)
		}
	}
}

func TestAcceptKeepsOnlyTheBadListingsHeldOne(t *testing.T) {
	// date-safe: a published page's generated_at, compared only with other documents' (never with a clock).
	day := func(d int) time.Time { return time.Date(2026, 11, d, 4, 0, 0, 0, time.UTC) }
	held := &Document{GeneratedAt: day(21), Listings: map[string]Listing{
		"devin":        {Provider: "devin", Models: []ListingModel{{ID: "kimi-k9"}}},
		"openai-codex": {Provider: "openai-codex", Models: []ListingModel{{ID: "gpt-9"}}},
	}}
	fetched := &Document{GeneratedAt: day(22), ETag: `"v2"`, Listings: map[string]Listing{
		"openai-codex": {Provider: "openai-codex", Models: []ListingModel{{ID: "gpt-9"}, {ID: "gpt-10"}}},
	}}
	merged, changed, err := Accept(held, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Listings["devin"].Models[0].ID != "kimi-k9" || len(merged.Listings["openai-codex"].Models) != 2 || merged.ETag != `"v2"` {
		t.Fatalf("merged = %+v", merged)
	}
	if strings.Join(changed, ",") != "openai-codex" {
		t.Fatalf("changed = %v", changed)
	}
	if _, _, err = Accept(merged, &Document{GeneratedAt: day(20)}); !errors.Is(err, ErrOlder) {
		t.Fatalf("older: %v", err)
	}
	if _, changed, err = Accept(merged, &Document{GeneratedAt: day(22), Listings: merged.Listings}); err != nil || len(changed) != 0 {
		t.Fatalf("same document: %v %v", changed, err)
	}
}

func TestPublishedCacheRoundTripAndDamage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), FolderName)
	store := NewStore(dir)
	if _, err := store.ReadPublished(); !errors.Is(err, ErrNotCached) {
		t.Fatalf("empty: %v", err)
	}
	maxOutput := 32768
	// date-safe: times stored and read back; the package reads no clock: every time is an argument or a stored value.
	doc := &Document{Schema: PublishedSchema, ETag: `"v1"`, DatasetVersion: "ds", GeneratedAt: time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC),
		FetchedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC),
		Listings:  map[string]Listing{"devin": {Provider: "devin", Models: []ListingModel{{ID: "kimi-k9", MaxOutput: &maxOutput}}}}}
	if err := store.WritePublished(doc); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, PublishedFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	read, err := store.ReadPublished()
	if err != nil || read.ETag != `"v1"` || *read.Listings["devin"].Models[0].MaxOutput != 32768 {
		t.Fatalf("read: %+v %v", read, err)
	}
	for name, content := range map[string]string{
		"damaged": `{"schema":1,`,
		"foreign": `{"schema":9}`,
		"no date": `{"schema":1,"listings":{}}`,
		// date-safe: a damaged file's times; the package reads no clock: every time is an argument or a stored value.
		"invalid": `{"schema":1,"generated_at":"2026-11-22T04:00:00Z","fetched_at":"2026-11-22T04:00:00Z","listings":{"devin":{"provider":"xai","models":[{"id":"a"}]}}}`,
	} {
		if err = os.WriteFile(filepath.Join(dir, PublishedFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = store.ReadPublished(); err == nil || errors.Is(err, ErrNotCached) {
			t.Fatalf("%s file must be ignored: %v", name, err)
		}
	}
}
