package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Step P3: providers.dev's lists. Every test serves providers.dev from a test server; none reaches the
// real site.

// provimuxPDServer is a test providers.dev: it serves page with etag and answers If-None-Match with 304.
type provimuxPDServer struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	page     []byte
	etag     string
	requests []*http.Request
	hang     chan struct{} // when set, the handler waits on it
}

func provimuxPD(t *testing.T) *provimuxPDServer {
	t.Helper()
	pd := &provimuxPDServer{status: http.StatusOK}
	pd.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pd.mu.Lock()
		pd.requests = append(pd.requests, r.Clone(context.Background()))
		status, page, etag, hang := pd.status, pd.page, pd.etag, pd.hang
		pd.mu.Unlock()
		if hang != nil {
			select {
			case <-hang:
			case <-r.Context().Done():
			}
			return
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(page)
	}))
	t.Cleanup(pd.Close)
	previous := provimuxPublishedBase
	provimuxPublishedBase = pd.URL + "/api/v1/listings"
	t.Cleanup(func() { provimuxPublishedBase = previous })
	return pd
}

func (pd *provimuxPDServer) serve(status int, etag string, page []byte) {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	pd.status, pd.etag, pd.page = status, etag, page
}

func (pd *provimuxPDServer) count() int {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return len(pd.requests)
}

// provimuxPublishedReset gives the test an empty published store, on, and empties it again afterwards.
func provimuxPublishedReset(t *testing.T) {
	t.Helper()
	reset := func(on bool) {
		st := provimuxPublished
		st.mu.Lock()
		st.setDocLocked(nil, false)
		st.cacheRead, st.failure, st.failures, st.next, st.lastLogged = false, nil, 0, time.Time{}, ""
		st.mu.Unlock()
		st.on.Store(!on)
		provimuxSetPublishedOn(on)
	}
	reset(true)
	previousDefault := registry.SetProvimuxPublishedListsDefault(true)
	t.Cleanup(func() {
		reset(false)
		registry.SetProvimuxPublishedListsDefault(previousDefault)
	})
}

func provimuxPDPage(generated string, listings ...map[string]any) []byte {
	data, _ := json.Marshal(map[string]any{
		"meta":  map[string]any{"dataset_version": "ds-" + generated, "generated_at": generated, "upstream_fetched_at": generated},
		"total": len(listings), "limit": modellists.FullListingLimit, "offset": 0, "next_offset": nil, "data": listings})
	return data
}

func provimuxPDListing(provider string, models ...map[string]any) map[string]any {
	if models == nil {
		models = []map[string]any{}
	}
	return map[string]any{"provider": provider, "basis": "listed", "listed_at": "2026-11-23", "client": "provimux",
		"evidence": []any{}, "model_count": len(models), "models": models}
}

// provimuxPDKimiK9 is providers.dev's row for Devin's kimi-k9: every field stated, so the test shows
// which ones the vendor's list keeps and which ones providers.dev fills.
func provimuxPDKimiK9(maxOutput int) map[string]any {
	return map[string]any{"id": "kimi-k9", "name": "Kimi K9 (providers.dev)", "context_window": 131072, "max_input": nil,
		"max_output": maxOutput, "input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"},
		"reasoning": map[string]any{"supported": true, "levels": []string{"low", "high"}, "budget_min": nil, "budget_max": nil,
			"can_disable": true, "dynamic": nil, "disable_with": nil},
		"web_search": nil, "aliases": []string{}, "variants": []string{"kimi-k9-low", "kimi-k9-high"}, "plans": []string{}, "hidden": false}
}

// provimuxDevinLogin registers a Devin login whose vendor's list is provimuxDevinRecorded (gemini-4-neon
// and kimi-k9 are learned: the catalog lacks both; kimi-k9's answer states no output limit and no levels).
func provimuxDevinLogin(t *testing.T) (*Service, *coreauth.Auth) {
	t.Helper()
	return provimuxDevinLoginAt(t, "")
}

// provimuxDevinLoginAt is provimuxDevinLogin with the service's config file at configPath, so the disk
// cache beside it is opened at the first registration, as at a real start.
func provimuxDevinLoginAt(t *testing.T, configPath string) (*Service, *coreauth.Auth) {
	t.Helper()
	previous := devinLiveModelListDo
	devinLiveModelListDo = func(_ context.Context, _ *config.Config, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		_, _ = recorder.Write(provimuxDevinRecorded)
		return recorder.Result(), nil
	}
	t.Cleanup(func() { devinLiveModelListDo = previous })
	executor := &liveListExecutor{provider: "devin"}
	cfg := provimuxOn()
	cfg.Models.PublishedLists = new(true)
	svc, auth := provimuxService(t, cfg, executor, &coreauth.Auth{ID: "devin-published-test", Provider: "devin",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"api_key": "devin-session", "email": "office@providers.dev"}})
	svc.configPath = configPath
	svc.registerModelsForAuth(t.Context(), auth)
	provimuxRefresh(svc, executor, false)
	return svc, auth
}

func provimuxRegisteredModel(auth *coreauth.Auth, id string) *ModelInfo {
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if model.ID == id {
			return model
		}
	}
	return nil
}

func provimuxStatusRow(t *testing.T, auth *coreauth.Auth, id string) (row struct {
	Context, Output int
	Levels          []string
	Name            string
	Sources         map[string]string
}) {
	t.Helper()
	state, _ := provimuxLiveListSource{}.ModelListLogin(auth, true, true, false)
	for _, model := range state.Models {
		if model.ID == id {
			row.Context, row.Output, row.Levels, row.Name, row.Sources = model.Context, model.Output, model.Levels, model.Name, model.Sources
			return row
		}
	}
	t.Fatalf("no status row %s", id)
	return row
}

// A learned model gets its limits from providers.dev: every field the vendor's list does not state, and
// only those. A model the catalog has takes providers.dev's value in every field the listing states (on
// top of the catalog), and the executors' lookups see the same values.
func TestProvimuxPublishedFillsALearnedModel(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	pd := provimuxPD(t)
	svc, auth := provimuxDevinLogin(t)

	before := provimuxRegisteredModel(auth, "devin/kimi-k9")
	if before == nil || before.MaxCompletionTokens != 0 || before.Thinking != nil || before.ContextLength != 262144 {
		t.Fatalf("before: %+v", before)
	}
	beforeRow := provimuxStatusRow(t, auth, "devin/kimi-k9")
	t.Logf("before: context=%d (%s) output=%d (%s) levels=%v (%s) name=%q (%s)", beforeRow.Context, beforeRow.Sources["context"],
		beforeRow.Output, beforeRow.Sources["output"], beforeRow.Levels, beforeRow.Sources["reasoning_levels"], beforeRow.Name, beforeRow.Sources["display_name"])
	if beforeRow.Sources["output"] != "" || beforeRow.Sources["reasoning_levels"] != "" {
		t.Fatalf("before: no source states the output limit or levels: %v", beforeRow.Sources)
	}
	catalogSWE := registry.LookupDevinModel("swe-2")
	if catalogSWE == nil {
		t.Fatal("swe-2 must be in the catalog")
	}

	pd.serve(http.StatusOK, `"v1"`, provimuxPDPage("2026-11-23T04:20:11Z", provimuxPDListing("devin",
		provimuxPDKimiK9(32768),
		map[string]any{"id": "gemini-4-neon", "name": "other", "context_window": 1, "max_output": 2, "input_modalities": []string{"audio"},
			"output_modalities": []string{"image"}, "reasoning": map[string]any{"levels": []string{"max"}}, "aliases": []string{}},
		map[string]any{"id": "swe-2", "name": nil, "context_window": nil, "max_output": catalogSWE.MaxCompletionTokens + 1000, "aliases": []string{}})))
	svc.refreshPublishedLists(t.Context(), svc.cfg)

	after := provimuxRegisteredModel(auth, "devin/kimi-k9")
	afterRow := provimuxStatusRow(t, auth, "devin/kimi-k9")
	t.Logf("after:  context=%d (%s) output=%d (%s) levels=%v (%s) name=%q (%s)", afterRow.Context, afterRow.Sources["context"],
		afterRow.Output, afterRow.Sources["output"], afterRow.Levels, afterRow.Sources["reasoning_levels"], afterRow.Name, afterRow.Sources["display_name"])
	if after == nil || after.MaxCompletionTokens != 32768 || after.ContextLength != 262144 || after.DisplayName != "Kimi K9" ||
		after.Thinking == nil || strings.Join(after.Thinking.Levels, ",") != "low,high" || !after.Thinking.ZeroAllowed ||
		strings.Join(after.SupportedInputModalities, ",") != "text" {
		t.Fatalf("after: %+v thinking=%+v", after, after.Thinking)
	}
	if afterRow.Sources["output"] != "published" || afterRow.Sources["reasoning_levels"] != "published" ||
		afterRow.Sources["context"] != "live" || afterRow.Sources["display_name"] != "live" || afterRow.Sources["input_modalities"] != "live" {
		t.Fatalf("after: sources %v", afterRow.Sources)
	}
	// The executor's lookup sees the same values.
	if model := registry.LookupDevinModel("kimi-k9-high"); model == nil || model.MaxCompletionTokens != 32768 {
		t.Fatalf("lookup: %+v", model)
	}
	// A learned model whose vendor's list states every field keeps the vendor's values.
	neon := provimuxRegisteredModel(auth, "devin/gemini-4-neon")
	if neon == nil || neon.ContextLength != 1048576 || neon.MaxCompletionTokens != 65536 || neon.DisplayName != "Gemini 4 Neon" ||
		strings.Join(neon.Thinking.Levels, ",") != "low,medium,high" || strings.Join(neon.SupportedInputModalities, ",") != "text,image" {
		t.Fatalf("neon: %+v", neon)
	}
	// A model the catalog has: providers.dev's output limit on top of the catalog's; the fields its row
	// leaves out stay the catalog's.
	swe := provimuxRegisteredModel(auth, "devin/swe-2")
	t.Logf("catalog model devin/swe-2: catalog output=%d, providers.dev output=%d, registered output=%d, context=%d (catalog %d)",
		catalogSWE.MaxCompletionTokens, catalogSWE.MaxCompletionTokens+1000, swe.MaxCompletionTokens, swe.ContextLength, catalogSWE.ContextLength)
	if swe == nil || swe.MaxCompletionTokens != catalogSWE.MaxCompletionTokens+1000 || swe.ContextLength != catalogSWE.ContextLength ||
		swe.DisplayName != catalogSWE.DisplayName {
		t.Fatalf("catalog model: %+v, catalog %+v", swe, catalogSWE)
	}
	if row := provimuxStatusRow(t, auth, "devin/swe-2"); row.Sources["output"] != "published" || row.Sources["context"] != "built-in" ||
		row.Output != catalogSWE.MaxCompletionTokens+1000 {
		t.Fatalf("a catalog model's sources: %v", row.Sources)
	}
	if model := registry.LookupDevinModel("swe-2"); model == nil || model.MaxCompletionTokens != catalogSWE.MaxCompletionTokens+1000 {
		t.Fatalf("the executor's lookup: %+v", model)
	}
	if model := registry.LookupModelInfo("devin/swe-2", "devin"); model == nil || model.MaxCompletionTokens != catalogSWE.MaxCompletionTokens+1000 {
		t.Fatalf("LookupModelInfo: %+v", model)
	}
	if raw := registry.ProvimuxRawCatalogModel("devin", "swe-2"); raw == nil || raw.MaxCompletionTokens != catalogSWE.MaxCompletionTokens {
		t.Fatalf("the catalog itself is unchanged: %+v", raw)
	}

	// Option off: the vendor's and the catalog's values alone again.
	svc.cfg.Models.PublishedLists = new(false)
	provimuxSetPublishedOn(false)
	svc.provimuxPublishedChanged([]string{"devin"})
	if off := provimuxRegisteredModel(auth, "devin/kimi-k9"); off == nil || off.MaxCompletionTokens != 0 || off.Thinking != nil {
		t.Fatalf("option off: %+v", off)
	}
	if off := provimuxRegisteredModel(auth, "devin/swe-2"); off == nil || off.MaxCompletionTokens != catalogSWE.MaxCompletionTokens {
		t.Fatalf("option off, catalog model: %+v", off)
	}
}

// The request: one, the same for every user, with view, limit and the fixed provider filter, and no
// header beyond User-Agent and If-None-Match (Go's transport adds Accept-Encoding). A 304 changes nothing.
func TestProvimuxPublished304ChangesNothing(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	dir := t.TempDir()
	liveModelLists.cache.Store(modellists.NewStore(dir))
	pd := provimuxPD(t)
	svc, auth := provimuxDevinLogin(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	liveModelListNow = func() time.Time { return now }

	pd.serve(http.StatusOK, `"v1"`, provimuxPDPage("2026-11-23T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(32768))))
	svc.refreshPublishedLists(t.Context(), svc.cfg)
	file := filepath.Join(dir, modellists.PublishedFileName)
	stored, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore := provimuxPublishedStateOf()
	if stateBefore.State != "held" || stateBefore.DatasetVersion != "ds-2026-11-23T04:20:11Z" || stateBefore.Failure != nil {
		t.Fatalf("state = %+v", stateBefore)
	}

	// The next fetch is due in 6 hours, not before.
	if provimuxPublished.due(now.Add(5*time.Hour)) || !provimuxPublished.due(now.Add(6*time.Hour)) {
		t.Fatal("one request per 6 hours")
	}
	now = now.Add(6 * time.Hour)
	svc.refreshPublishedLists(t.Context(), svc.cfg)
	if pd.count() != 2 {
		t.Fatalf("requests = %d", pd.count())
	}
	for i, req := range pd.requests {
		query := req.URL.Query()
		if len(query) != 3 || query.Get("view") != "full" || query.Get("limit") != "40" ||
			query.Get("provider") != strings.Join(modellists.ListingProviders(), ",") {
			t.Fatalf("request %d query = %v", i, query)
		}
		for name := range req.Header {
			if name != "User-Agent" && name != "If-None-Match" && name != "Accept-Encoding" {
				t.Fatalf("request %d sends header %s", i, name)
			}
		}
		if !strings.HasPrefix(req.Header.Get("User-Agent"), "provimux/") {
			t.Fatalf("user agent = %q", req.Header.Get("User-Agent"))
		}
	}
	if pd.requests[0].Header.Get("If-None-Match") != "" || pd.requests[1].Header.Get("If-None-Match") != `"v1"` {
		t.Fatal("the second request is conditional on the held ETag")
	}
	stateAfter := provimuxPublishedStateOf()
	if stateAfter != stateBefore {
		t.Fatalf("304 changed the state: %+v -> %+v", stateBefore, stateAfter)
	}
	if again, _ := os.ReadFile(file); string(again) != string(stored) {
		t.Fatal("304 rewrote the disk copy")
	}
	if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 32768 {
		t.Fatalf("304 changed the model: %+v", model)
	}
	if provimuxPublished.due(now.Add(5 * time.Hour)) {
		t.Fatal("a 304 also counts as the 6-hour fetch")
	}
}

// One bad listing keeps only that login type's held listing; the others take the new document.
func TestProvimuxPublishedBadListingKeepsOnlyItsType(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	pd := provimuxPD(t)
	svc, auth := provimuxDevinLogin(t)
	codex := map[string]any{"id": "gpt-9", "max_output": 1000, "aliases": []string{}}
	pd.serve(http.StatusOK, `"v1"`, provimuxPDPage("2026-11-23T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(32768)),
		provimuxPDListing("openai-codex", codex)))
	svc.refreshPublishedLists(t.Context(), svc.cfg)

	for name, devin := range map[string]map[string]any{
		"empty":     provimuxPDListing("devin"),
		"malformed": {"provider": "devin", "models": "not a list"},
		"no id":     provimuxPDListing("devin", map[string]any{"id": "", "max_output": 1}),
	} {
		codex["max_output"] = codex["max_output"].(int) + 1
		pd.serve(http.StatusOK, `"v-`+name+`"`, provimuxPDPage("2026-11-24T04:20:11Z", devin, provimuxPDListing("openai-codex", codex)))
		svc.refreshPublishedLists(t.Context(), svc.cfg)
		if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 32768 {
			t.Fatalf("%s devin listing: the held one stays: %+v", name, model)
		}
		if row := provimuxPublished.lookup("codex", "gpt-9"); row == nil || *row.MaxOutput != codex["max_output"].(int) {
			t.Fatalf("%s devin listing: codex takes the new one: %+v", name, row)
		}
		if state := provimuxPublishedStateOf(); state.Failure != nil || state.DatasetVersion != "ds-2026-11-24T04:20:11Z" {
			t.Fatalf("%s: a bad listing is not a failed fetch: %+v", name, state)
		}
	}
	// A missing listing too.
	pd.serve(http.StatusOK, `"v-missing"`, provimuxPDPage("2026-11-24T05:00:00Z", provimuxPDListing("openai-codex", codex)))
	svc.refreshPublishedLists(t.Context(), svc.cfg)
	if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 32768 {
		t.Fatalf("missing devin listing: %+v", model)
	}
}

// 500, a cut-off body, an incomplete page and an older document: the held document stays, the failure is
// a code, and the retry follows 10 minutes, 1 hour, then 6 hours.
func TestProvimuxPublishedFailuresKeepTheHeldDocument(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	pd := provimuxPD(t)
	svc, auth := provimuxDevinLogin(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	liveModelListNow = func() time.Time { return now }
	pd.serve(http.StatusOK, `"v1"`, provimuxPDPage("2026-11-23T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(32768))))
	svc.refreshPublishedLists(t.Context(), svc.cfg)

	cases := []struct {
		name     string
		status   int
		page     []byte
		category string
		code     int
		wait     time.Duration
	}{
		{"500", http.StatusInternalServerError, []byte("internal"), modellists.FailureHTTP, 500, 10 * time.Minute},
		{"cut-off", http.StatusOK, provimuxPDPage("2026-11-24T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(1)))[:60],
			modellists.FailureResponse, 0, time.Hour},
		{"incomplete", http.StatusOK, []byte(`{"meta":{"dataset_version":"x","generated_at":"2026-11-24T04:20:11Z"},"total":11,"next_offset":40,"data":[]}`),
			modellists.FailureResponse, 0, 6 * time.Hour},
		{"older", http.StatusOK, provimuxPDPage("2026-11-22T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(1))),
			modellists.FailureResponse, 0, 6 * time.Hour},
	}
	for _, tc := range cases {
		pd.serve(tc.status, `"v-`+tc.name+`"`, tc.page)
		svc.refreshPublishedLists(t.Context(), svc.cfg)
		state := provimuxPublishedStateOf()
		if state.State != "held" || state.DatasetVersion != "ds-2026-11-23T04:20:11Z" || state.Failure == nil ||
			state.Failure.Category != tc.category || state.Failure.HTTPStatus != tc.code {
			t.Fatalf("%s: state %+v failure %+v", tc.name, state, state.Failure)
		}
		if provimuxPublished.due(now.Add(tc.wait-time.Second)) || !provimuxPublished.due(now.Add(tc.wait)) {
			t.Fatalf("%s: retry not after %s", tc.name, tc.wait)
		}
		if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 32768 {
			t.Fatalf("%s: the model changed: %+v", tc.name, model)
		}
	}
	// A success clears the failure and the backoff.
	pd.serve(http.StatusOK, `"v2"`, provimuxPDPage("2026-11-24T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(40000))))
	svc.refreshPublishedLists(t.Context(), svc.cfg)
	if state := provimuxPublishedStateOf(); state.Failure != nil || provimuxPublished.failures != 0 {
		t.Fatalf("after success: %+v", state)
	}
	if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 40000 {
		t.Fatalf("after success: %+v", model)
	}
}

// providers.dev unreachable at start: the start never waits for it, no list is emptied, and the disk copy
// of the last run fills the learned model before any request.
func TestProvimuxPublishedUnreachableAtStart(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	dir := t.TempDir()
	maxOutput := 32768
	seed := modellists.NewStore(filepath.Join(dir, modellists.FolderName))
	if err := seed.WritePublished(&modellists.Document{Schema: modellists.PublishedSchema, ETag: `"v1"`, DatasetVersion: "ds-cache",
		GeneratedAt: time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC), FetchedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC),
		Listings: map[string]modellists.Listing{"devin": {Provider: "devin", Models: []modellists.ListingModel{{ID: "kimi-k9", MaxOutput: &maxOutput}}}}}); err != nil {
		t.Fatal(err)
	}

	// providers.dev hangs: it accepts the connection and never answers.
	pd := provimuxPD(t)
	release := make(chan struct{})
	pd.mu.Lock()
	pd.hang = release
	pd.mu.Unlock()
	t.Cleanup(func() { close(release) })
	previousTimeout := provimuxPublishedFetchTimeout
	provimuxPublishedFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { provimuxPublishedFetchTimeout = previousTimeout })

	// The start: the disk copy is read at the first registration, before any request; the live list
	// arrives; then the background fetchers start, and providers.dev never answers.
	started := time.Now()
	svc, auth := provimuxDevinLoginAt(t, filepath.Join(dir, "config.yaml"))
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("registration waited: %s", elapsed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		svc.runLiveModelListRefresh(ctx) // starts the published fetch in the background, as Run does
		close(done)
	}()
	svc.registerModelsForAuth(t.Context(), auth)
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("registration waited for providers.dev: %s", elapsed)
	}
	if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 32768 {
		t.Fatalf("the disk copy fills the learned model before any answer: %+v", model)
	}
	if state := provimuxPublishedStateOf(); state.State != "held" || !state.FromCache {
		t.Fatalf("state from the cache: %+v", state)
	}
	// The hanging fetch times out: a network failure; nothing is emptied.
	deadline := time.Now().Add(5 * time.Second)
	for provimuxPublishedStateOf().Failure == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if state := provimuxPublishedStateOf(); state.Failure == nil || state.Failure.Category != modellists.FailureNetwork || state.State != "held" {
		t.Fatalf("after the timeout: %+v", state)
	}
	if ids := provimuxRegistered(auth); len(ids) != 4 {
		t.Fatalf("the login's list was emptied: %v", ids)
	}
	cancel()
	<-done
}

// Unreachable with nothing on disk: the vendor's values alone, state none, and the start goes on.
func TestProvimuxPublishedUnreachableWithoutCache(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	previous := provimuxPublishedBase
	provimuxPublishedBase = closed.URL + "/api/v1/listings"
	t.Cleanup(func() { provimuxPublishedBase = previous })
	svc, auth := provimuxDevinLogin(t)
	svc.refreshPublishedLists(t.Context(), svc.cfg)
	state := provimuxPublishedStateOf()
	if state.State != "none" || state.Failure == nil || state.Failure.Category != modellists.FailureNetwork {
		t.Fatalf("state = %+v", state)
	}
	if ids := provimuxRegistered(auth); len(ids) != 4 {
		t.Fatalf("registered = %v", ids)
	}
	if model := provimuxRegisteredModel(auth, "devin/kimi-k9"); model == nil || model.MaxCompletionTokens != 0 || model.ContextLength != 262144 {
		t.Fatalf("kimi-k9 = %+v", model)
	}
}

// POST /v8/management/models/refresh reaches the published fetcher through the registry's hook.
func TestProvimuxPublishedRefreshHook(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	pd := provimuxPD(t)
	pd.serve(http.StatusOK, `"v1"`, provimuxPDPage("2026-11-23T04:20:11Z", provimuxPDListing("devin", provimuxPDKimiK9(32768))))
	cfg := provimuxOn()
	cfg.Models.PublishedLists = new(true)
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { svc.runLiveModelListRefresh(ctx); close(done) }()
	waitFor := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for pd.count() < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if pd.count() < n {
			t.Fatalf("requests = %d, want %d", pd.count(), n)
		}
	}
	waitFor(1) // at start
	time.Sleep(50 * time.Millisecond)
	registry.RequestLiveModelListRefresh()
	waitFor(2) // on refresh
	cancel()
	<-done
	if pd.count() != 2 {
		t.Fatalf("requests = %d", pd.count())
	}
}

// Where the catalog leaves a field empty, the vendor's list fills it as built and wins over providers.dev;
// where the catalog states it, providers.dev's value (on top) stays, whatever the vendor says. The thinking
// block is never the vendor's.
func TestProvimuxVendorBeforePublishedOnlyWhereTheCatalogIsEmpty(t *testing.T) {
	raw := &ModelInfo{ID: "m", DisplayName: "Catalog", MaxCompletionTokens: 8000}
	overlaid := &ModelInfo{ID: "m", DisplayName: "Catalog", MaxCompletionTokens: 9000, ContextLength: 200000,
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}}
	vendor := &ModelInfo{ID: "m", DisplayName: "Vendor", MaxCompletionTokens: 4000, ContextLength: 131072,
		Thinking: &registry.ThinkingSupport{Levels: []string{"medium"}}}
	merged := fillLiveModelOver(raw, overlaid, vendor)
	if merged.MaxCompletionTokens != 9000 || merged.ContextLength != 131072 || merged.DisplayName != "Catalog" ||
		strings.Join(merged.Thinking.Levels, ",") != "low,high" {
		t.Fatalf("merged = %+v thinking %+v", merged, merged.Thinking)
	}
	maxOutput, window := 9000, 200000
	row := &modellists.ListingModel{ID: "m", MaxOutput: &maxOutput, ContextWindow: &window}
	sources := map[string]string{}
	provimuxFieldSources(sources, vendor, raw, false, row)
	if sources["output"] != "published" || sources["context"] != "live" || sources["display_name"] != "built-in" {
		t.Fatalf("sources = %v", sources)
	}
}

// A login without a live list (live lists off): the catalog decides, so providers.dev's values go on top
// of every catalog model its listing has a row for; the models offered stay the catalog's.
func TestProvimuxPublishedOnTopOfTheCatalogWithoutALiveList(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	pd := provimuxPD(t)
	catalogSWE := registry.LookupDevinModel("swe-2")
	cfg := &config.Config{}
	cfg.Models.LiveLists = new(false)
	cfg.Models.PublishedLists = new(true)
	executor := &liveListExecutor{provider: "devin"}
	svc, auth := provimuxService(t, cfg, executor, &coreauth.Auth{ID: "devin-no-list", Provider: "devin",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"api_key": "devin-session", "email": "office@providers.dev"}})
	svc.registerModelsForAuth(t.Context(), auth)
	before := provimuxRegistered(auth)
	pd.serve(http.StatusOK, `"v1"`, provimuxPDPage("2026-11-23T04:20:11Z", provimuxPDListing("devin",
		map[string]any{"id": "swe-2", "max_output": catalogSWE.MaxCompletionTokens / 2, "aliases": []string{}},
		provimuxPDKimiK9(32768))))
	svc.refreshPublishedLists(t.Context(), svc.cfg)
	if after := provimuxRegistered(auth); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("the listing decided which models the login offers: %v -> %v", before, after)
	}
	if swe := provimuxRegisteredModel(auth, "devin/swe-2"); swe == nil || swe.MaxCompletionTokens != catalogSWE.MaxCompletionTokens/2 {
		t.Fatalf("swe-2 = %+v", swe)
	}
	if row := provimuxStatusRow(t, auth, "devin/swe-2"); row.Sources["output"] != "published" || row.Sources["context"] != "built-in" {
		t.Fatalf("sources = %v", row.Sources)
	}
}
