package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
)

// GET /v8/management/provimux/figures (prices-and-cache.md 6.3, step F2) against a stand-in source.

func provimuxFiguresServe(t *testing.T, cfg *config.Config, query string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(cfg, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/figures"+query, nil)
	h.ProvimuxFigures(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func provimuxFiguresSourceFor(t *testing.T, state ProvimuxFiguresState) {
	t.Helper()
	SetProvimuxFiguresSource(func() ProvimuxFiguresState { return state })
	t.Cleanup(func() { SetProvimuxFiguresSource(nil) })
}

func TestProvimuxFiguresHandler(t *testing.T) {
	provimuxTestClock(t)
	// date-safe: the held document's dates, shown as they are; no clock compares them.
	doc := &modellists.FiguresDocument{Schema: modellists.FiguresSchema, DatasetVersion: "c37885999c67cfaa",
		GeneratedAt: time.Date(2026, 11, 26, 11, 22, 48, 384000000, time.UTC), FetchedAt: time.Date(2026, 11, 26, 12, 0, 3, 0, time.UTC),
		Entries: map[string]modellists.Figures{
			"devin/kimi-k3":   {Provider: "devin", Model: "kimi-k3", Access: "subscription"},
			"anthropic/x-1-0": {Provider: "anthropic", Model: "x-1-0", Access: "api"},
		}}
	// date-safe: a failure's time, shown as it is.
	failure := &ProvimuxLiveListFailure{At: time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC), Category: "http_status", HTTPStatus: 503}
	on := &config.Config{}
	on.Models.PublishedLists = new(true)

	provimuxFiguresSourceFor(t, ProvimuxFiguresState{State: "held", Document: doc, FromCache: true, Failure: failure})
	all := provimuxFiguresServe(t, on, "?all=true")
	figures := all["figures"].(map[string]any)
	// date-safe: the held document's dates as the answer writes them.
	if figures["state"] != "held" || figures["contract"] != float64(1) || figures["source"] != "https://providers.dev" ||
		figures["dataset_version"] != "c37885999c67cfaa" || figures["generated_at"] != "2026-11-26T11:22:48.384Z" ||
		figures["fetched_at"] != "2026-11-26T12:00:03Z" || len(figures["entries"].(map[string]any)) != 2 ||
		all["held_entries"] != float64(2) || all["from_cache"] != true || all["options"].(map[string]any)["published_lists"] != true {
		t.Fatalf("held, all: %v", all)
	}
	last := all["last_failure"].(map[string]any)
	// date-safe: the failure's time as the answer writes it.
	if last["category"] != "http_status" || last["http_status"] != float64(503) || last["at"] != "2026-11-26T18:00:00Z" {
		t.Fatalf("last_failure: %v", last)
	}
	// Without all=true: the entries the routes' answered items name. No route names one yet (W6b).
	selected := provimuxFiguresServe(t, on, "")
	if entries := selected["figures"].(map[string]any)["entries"].(map[string]any); len(entries) != 0 || selected["held_entries"] != float64(2) {
		t.Fatalf("selected: %v", selected)
	}
	if notBool := provimuxFiguresServe(t, on, "?all=yes"); len(notBool["figures"].(map[string]any)["entries"].(map[string]any)) != 0 {
		t.Fatal("all takes a boolean")
	}

	// Off: the switch or Home mode. No figure, whatever is held.
	off := &config.Config{}
	off.Models.PublishedLists = new(false)
	home := &config.Config{}
	home.Models.PublishedLists = new(true)
	home.Home.Enabled = true
	for name, cfg := range map[string]*config.Config{"off": off, "home": home, "unset in a test binary": {}} {
		answer := provimuxFiguresServe(t, cfg, "?all=true")
		f := answer["figures"].(map[string]any)
		if f["state"] != "off" || len(f["entries"].(map[string]any)) != 0 || f["dataset_version"] != nil || f["fetched_at"] != nil ||
			answer["held_entries"] != float64(0) || answer["from_cache"] != false || answer["last_failure"] != nil ||
			answer["options"].(map[string]any)["published_lists"] != false {
			t.Fatalf("%s: %v", name, answer)
		}
	}

	// None: nothing held, the failure shown; a failure without an HTTP status has null.
	failure = &ProvimuxLiveListFailure{At: failure.At, Category: "network"}
	provimuxFiguresSourceFor(t, ProvimuxFiguresState{State: "none", Failure: failure})
	none := provimuxFiguresServe(t, on, "?all=true")
	f := none["figures"].(map[string]any)
	if f["state"] != "none" || len(f["entries"].(map[string]any)) != 0 || f["dataset_version"] != nil || none["held_entries"] != float64(0) ||
		none["last_failure"].(map[string]any)["http_status"] != nil || none["last_failure"].(map[string]any)["category"] != "network" {
		t.Fatalf("none: %v", none)
	}
	// No source registered (a binary without sdk/cliproxy): nothing is held.
	SetProvimuxFiguresSource(nil)
	if answer := provimuxFiguresServe(t, on, ""); answer["figures"].(map[string]any)["state"] != "none" {
		t.Fatalf("no source: %v", answer)
	}
}
