package cliproxy

import (
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// The critical review of P3 (findings 1 to 6): providers.dev fills gaps only. Each test takes its case from
// the review: a listing row that differs from the catalog in every field it states must change no value the
// catalog states, in the catalog's readers (registrations, LookupModelInfo, the executors' lookups).

// provimuxReviewLoad makes providers.dev's held document the one served: the listings given, fetched by the
// real client from a test server.
func provimuxReviewLoad(t *testing.T, listings ...map[string]any) {
	t.Helper()
	pd := provimuxPD(t)
	pd.serve(http.StatusOK, `"review"`, provimuxPDPage("2026-11-24T04:20:11Z", listings...))
	cfg := &config.Config{}
	cfg.Models.PublishedLists = new(true)
	(&Service{cfg: cfg}).refreshPublishedLists(t.Context(), cfg)
	if state := provimuxPublishedStateOf(); state.State != "held" {
		t.Fatalf("document not held: %+v", state)
	}
}

// provimuxReviewRow is a row that states every field, each different from the catalog model's.
func provimuxReviewRow(raw *ModelInfo) map[string]any {
	window := provimuxFirstPositive(raw.ContextLength, raw.InputTokenLimit)*2 + 1000
	return map[string]any{"id": raw.ID, "name": raw.DisplayName + " (providers.dev)", "context_window": window,
		"max_output":       provimuxFirstPositive(raw.MaxCompletionTokens, raw.OutputTokenLimit)/2 + 1,
		"input_modalities": []string{"text", "pdf"}, "output_modalities": []string{"text", "image"},
		"reasoning": map[string]any{"supported": false, "levels": []string{"low", "medium", "high"}, "budget_min": 1, "budget_max": 999,
			"can_disable": true, "dynamic": false, "disable_with": "off"},
		"web_search": true, "aliases": []string{}}
}

// provimuxReviewUnchanged fails for every field the catalog states that a reader sees differently. The
// thinking block and web search are compared whole.
func provimuxReviewUnchanged(t *testing.T, label string, raw, seen *ModelInfo) {
	t.Helper()
	if seen == nil {
		t.Fatalf("%s: not found", label)
	}
	var diffs []string
	if raw.DisplayName != "" && raw.DisplayName != seen.DisplayName {
		diffs = append(diffs, "name "+raw.DisplayName+" -> "+seen.DisplayName)
	}
	if raw.ContextLength != 0 && raw.ContextLength != seen.ContextLength || raw.InputTokenLimit != 0 && raw.InputTokenLimit != seen.InputTokenLimit {
		diffs = append(diffs, "context")
	}
	if raw.MaxCompletionTokens != 0 && raw.MaxCompletionTokens != seen.MaxCompletionTokens || raw.OutputTokenLimit != 0 && raw.OutputTokenLimit != seen.OutputTokenLimit {
		diffs = append(diffs, "output")
	}
	if len(raw.SupportedInputModalities) > 0 && !reflect.DeepEqual(raw.SupportedInputModalities, seen.SupportedInputModalities) {
		diffs = append(diffs, "input modalities")
	}
	if len(raw.SupportedOutputModalities) > 0 && !reflect.DeepEqual(raw.SupportedOutputModalities, seen.SupportedOutputModalities) {
		diffs = append(diffs, "output modalities")
	}
	if !reflect.DeepEqual(raw.Thinking, seen.Thinking) {
		diffs = append(diffs, "thinking")
	}
	if !reflect.DeepEqual(raw.NativeCapabilities, seen.NativeCapabilities) {
		diffs = append(diffs, "web search")
	}
	if len(diffs) > 0 {
		t.Errorf("%s: providers.dev changed what the catalog states: %s", label, strings.Join(diffs, ", "))
	}
}

// provimuxReviewSection serves one row per catalog model of section (listing provider) and checks every
// model through the channel lookup, against what the lookup gave before the document.
func provimuxReviewSection(t *testing.T, section, channel, provider string, ids ...string) {
	t.Helper()
	var rows []map[string]any
	var raws, before []*ModelInfo
	catalog := registry.ProvimuxCatalogModels(section)
	if section == "aistudio" || section == "vertex" {
		catalog = registry.GetStaticModelDefinitionsByChannel(section)
	}
	for _, model := range catalog {
		if len(ids) > 0 && !containsFold(ids, model.ID) {
			continue
		}
		raw := registry.ProvimuxRawCatalogModel(section, model.ID)
		if raw == nil {
			continue // a model Provimux adds in code (xAI's image and video models), not a catalog entry
		}
		raws = append(raws, raw)
		before = append(before, registry.LookupStaticModelInfoByChannel(raw.ID, channel))
		rows = append(rows, provimuxReviewRow(raw))
	}
	if len(raws) == 0 {
		t.Fatalf("%s: no catalog model of %v", section, ids)
	}
	provimuxReviewLoad(t, provimuxPDListing(provider, rows...))
	for i, raw := range raws {
		// What a reader saw without providers.dev (the catalog, or the definition Provimux keeps in code for
		// the same id) must be what it sees with it.
		provimuxReviewUnchanged(t, section+"/"+raw.ID, before[i], registry.LookupStaticModelInfoByChannel(raw.ID, channel))
	}
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

// Findings 1 and 2: Gemini's thinking budgets stay the catalog's (no levels added, no budget changed, no
// "can be off" from providers.dev), for Gemini keys, AI Studio and Vertex alike.
func TestProvimuxReviewGeminiThinkingUnchanged(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxReviewSection(t, "gemini", "gemini", "google")
	flash := registry.ProvimuxRawCatalogModel("aistudio", "gemini-flash-latest")
	if flash == nil || flash.Thinking == nil || len(flash.Thinking.Levels) != 0 {
		t.Fatalf("the review's case: gemini-flash-latest is a budget model in the catalog: %+v", flash)
	}
	provimuxReviewSection(t, "aistudio", "aistudio", "google")
	provimuxReviewSection(t, "vertex", "vertex", "google-vertex")
	if seen := registry.LookupStaticModelInfoByChannel("gemini-flash-latest", "aistudio"); seen == nil || len(seen.Thinking.Levels) != 0 ||
		seen.Thinking.Max != flash.Thinking.Max {
		t.Fatalf("gemini-flash-latest thinking = %+v", seen.Thinking)
	}
}

// Finding 3: gemini-4-argon's output limit (65536 in the catalog; providers.dev said 32768) stays.
func TestProvimuxReviewGeminiArgonOutputUnchanged(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	raw := registry.ProvimuxRawCatalogModel("aistudio", "gemini-4-argon")
	if raw == nil {
		t.Fatal("gemini-4-argon must be in the catalog")
	}
	output := 32768
	row := provimuxReviewRow(raw)
	row["max_output"] = output
	provimuxReviewLoad(t, provimuxPDListing("google", row))
	for _, channel := range []string{"aistudio", "gemini"} {
		if seen := registry.LookupStaticModelInfoByChannel("gemini-4-argon", channel); seen != nil &&
			provimuxFirstPositive(seen.MaxCompletionTokens, seen.OutputTokenLimit) != provimuxFirstPositive(raw.MaxCompletionTokens, raw.OutputTokenLimit) {
			t.Fatalf("%s gemini-4-argon output = %d/%d, catalog %d/%d", channel, seen.MaxCompletionTokens, seen.OutputTokenLimit,
				raw.MaxCompletionTokens, raw.OutputTokenLimit)
		}
	}
	provimuxReviewUnchanged(t, "aistudio/gemini-4-argon", raw, registry.LookupStaticModelInfoByChannel("gemini-4-argon", "aistudio"))
}

// Finding 4: Codex 6.1's window (272000 for gpt-6.1-sol in the catalog; providers.dev said 1050000) stays,
// in every plan's section.
func TestProvimuxReviewCodex61WindowUnchanged(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	raw := registry.ProvimuxRawCatalogModel("codex", "gpt-6.1-sol")
	if raw == nil || raw.ContextLength != 272000 {
		t.Fatalf("the review's case: gpt-6.1-sol = %+v", raw)
	}
	window := 1050000
	row := provimuxReviewRow(raw)
	row["context_window"] = window
	provimuxReviewLoad(t, provimuxPDListing("openai-codex", row))
	for name, models := range map[string][]*ModelInfo{"pro": registry.GetCodexProModels(), "plus": registry.GetCodexPlusModels(), "team": registry.GetCodexTeamModels()} {
		for _, model := range models {
			if model.ID == "gpt-6.1-sol" {
				provimuxReviewUnchanged(t, "codex-"+name+"/gpt-6.1-sol", raw, model)
			}
		}
	}
	provimuxReviewUnchanged(t, "codex lookup", raw, registry.LookupStaticModelInfoByChannel("gpt-6.1-sol", "codex"))
}

// Finding 5: xAI's and Copilot's catalog models keep every value the catalog states (the review's Copilot
// case: claude-haiku-4.5's window and output limit; Copilot's resolver reads this catalog).
func TestProvimuxReviewXAIAndCopilotUnchanged(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxReviewSection(t, "xai", "xai", "xai")
	provimuxReviewSection(t, "copilot", "copilot", "github-copilot")
	if raw := registry.ProvimuxRawCatalogModel("copilot", "claude-haiku-4.5"); raw == nil {
		t.Fatal("the review's case: claude-haiku-4.5 must be in Copilot's catalog")
	}
}

// providers.dev still fills a gap both leave: a catalog model without a window or output limit (Vertex's
// Imagen models) takes the row's, with a smaller max_input as the window; its name stays the catalog's.
func TestProvimuxReviewGapsAreFilled(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	raw := registry.ProvimuxRawCatalogModel("vertex", "imagen-4.0-ultra-generate-001")
	if raw == nil || provimuxFirstPositive(raw.ContextLength, raw.InputTokenLimit) != 0 || provimuxFirstPositive(raw.MaxCompletionTokens, raw.OutputTokenLimit) != 0 {
		t.Fatalf("imagen-4.0-ultra-generate-001 = %+v", raw)
	}
	provimuxReviewLoad(t, provimuxPDListing("google-vertex", map[string]any{"id": "imagen-4.0-ultra-generate-001", "context_window": 8192,
		"max_input": 4096, "max_output": 2048, "name": "other", "aliases": []string{}}))
	seen := registry.LookupStaticModelInfoByChannel("imagen-4.0-ultra-generate-001", "vertex")
	if seen == nil || provimuxFirstPositive(seen.ContextLength, seen.InputTokenLimit) != 4096 ||
		provimuxFirstPositive(seen.MaxCompletionTokens, seen.OutputTokenLimit) != 2048 || seen.DisplayName != raw.DisplayName || seen.Thinking != nil {
		t.Fatalf("gap fill: %+v", seen)
	}
}

// Learned models: providers.dev never supplies the thinking block (the vendor's list's, or none); a
// max_output not below the window is refused; a smaller max_input is the window.
func TestProvimuxReviewLearnedModelGuards(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxReviewLoad(t, provimuxPDListing("devin",
		map[string]any{"id": "no-window", "context_window": 1000000, "max_input": 800000, "max_output": 900000, "aliases": []string{},
			"reasoning": map[string]any{"supported": true, "levels": []string{"low", "high"}, "budget_max": 4096, "can_disable": true, "dynamic": true}},
		map[string]any{"id": "has-thinking", "max_output": 1000, "aliases": []string{},
			"reasoning": map[string]any{"supported": false, "levels": []string{"low", "high"}, "budget_max": 4096, "can_disable": true}}))

	got := provimuxComposeLearned("devin", &ModelInfo{ID: "no-window", Type: "devin"})
	if got.ContextLength != 800000 {
		t.Errorf("a smaller max_input is the window: %d", got.ContextLength)
	}
	if got.MaxCompletionTokens != 0 {
		t.Errorf("a max_output not below the window is refused: %d", got.MaxCompletionTokens)
	}
	if got.Thinking != nil {
		t.Errorf("no thinking block from providers.dev: %+v", got.Thinking)
	}

	vendor := &registry.ThinkingSupport{Levels: []string{"medium"}, ZeroAllowed: false}
	got = provimuxComposeLearned("devin", &ModelInfo{ID: "has-thinking", Type: "devin", ContextLength: 200000, Thinking: vendor})
	if !reflect.DeepEqual(got.Thinking, vendor) {
		t.Errorf("the vendor's block, whole: %+v", got.Thinking)
	}
	if got.MaxCompletionTokens != 1000 {
		t.Errorf("output below the vendor's window: %d", got.MaxCompletionTokens)
	}
	got = provimuxComposeLearned("devin", &ModelInfo{ID: "has-thinking", Type: "devin", ContextLength: 200000})
	if got.Thinking != nil || got.NativeCapabilities != nil {
		t.Errorf("no thinking and no web search from providers.dev: %+v %+v", got.Thinking, got.NativeCapabilities)
	}
}

// The six learned Gemini rows of today's data that providers.dev gives levels: a Gemini key's learned model
// gets none from it (the review's levels-on-a-budget-model case, for models the catalog lacks).
func TestProvimuxReviewLearnedGeminiGetsNoLevels(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	ids := []string{"gemini-flash-latest", "gemini-flash-lite-latest", "gemini-3.1-pro-preview-customtools", "gemini-3-pro-image",
		"gemini-nano-banana-2.1", "gemini-robotics-er-2-preview"}
	var rows []map[string]any
	for _, id := range ids {
		rows = append(rows, map[string]any{"id": id, "context_window": 1048576, "max_output": 65536, "input_modalities": []string{"text", "image"},
			"output_modalities": []string{"text"}, "web_search": true, "aliases": []string{},
			"reasoning": map[string]any{"supported": true, "levels": []string{"low", "medium", "high"}, "budget_max": 24576, "dynamic": true}})
	}
	provimuxReviewLoad(t, provimuxPDListing("google", rows...))
	for _, id := range ids {
		got := provimuxComposeLearned("gemini", &ModelInfo{ID: id, Type: "gemini", DisplayName: id, InputTokenLimit: 1048576, ContextLength: 1048576})
		if got.Thinking != nil || got.NativeCapabilities != nil {
			t.Errorf("%s: thinking %+v web search %+v from providers.dev", id, got.Thinking, got.NativeCapabilities)
		}
		if got.MaxCompletionTokens != 65536 || strings.Join(got.SupportedInputModalities, ",") != "text,image" {
			t.Errorf("%s: output and modalities are still filled: %+v", id, got)
		}
	}
}

// Finding 6: a real start. The option's switch is still off and the disk copy unread when the first login
// registers; that registration must already see the document on disk (here a Vertex login, whose models
// the catalog decides: Imagen has no window in the catalog, the disk copy states one).
func TestProvimuxReviewColdStartFirstLogin(t *testing.T) {
	provimuxLiveReset(t)
	provimuxPublishedReset(t)
	provimuxPublished.mu.Lock()
	provimuxPublished.cacheRead = false
	provimuxPublished.mu.Unlock()
	provimuxPublished.on.Store(false) // a fresh process: nothing has set it yet
	registry.ProvimuxCatalogOverlayChanged()
	raw := registry.ProvimuxRawCatalogModel("vertex", "imagen-4.0-generate-001")
	if raw == nil || raw.ContextLength != 0 || raw.InputTokenLimit != 0 {
		t.Fatalf("imagen-4.0-generate-001 = %+v", raw)
	}
	dir := t.TempDir()
	window, output := 4096, 1024
	seed := modellists.NewStore(filepath.Join(dir, modellists.FolderName))
	if err := seed.WritePublished(&modellists.Document{Schema: modellists.PublishedSchema, ETag: `"v1"`, DatasetVersion: "ds-cache",
		GeneratedAt: time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC), FetchedAt: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC),
		Listings: map[string]modellists.Listing{"google-vertex": {Provider: "google-vertex", Models: []modellists.ListingModel{
			{ID: "imagen-4.0-generate-001", ContextWindow: &window, MaxOutput: &output}}}}}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Models.PublishedLists = new(true)
	svc := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil), configPath: filepath.Join(dir, "config.yaml")}
	auth, err := svc.coreManager.Register(t.Context(), &coreauth.Auth{ID: "vertex-cold", Provider: "vertex",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"project_id": "p", "email": "office@providers.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	got := provimuxRegisteredModel(auth, "imagen-4.0-generate-001")
	if got == nil {
		t.Fatalf("not registered: %v", provimuxRegistered(auth))
	}
	if provimuxFirstPositive(got.ContextLength, got.InputTokenLimit) != window || provimuxFirstPositive(got.MaxCompletionTokens, got.OutputTokenLimit) != output {
		t.Fatalf("the first registration of a start missed the disk copy: %+v (on=%v)", got, provimuxPublished.on.Load())
	}
}
