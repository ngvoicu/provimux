package cliproxy

// The second review's counterexamples and parity checks, under the rule of model-lists.md 13.1:
// for a model the catalog has, the vendor's list decides only whether the login offers it, and the
// cache never deletes a file.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// provimuxAnswerExecutor answers every list request with one fixed body; with no body, a request
// is an error (the type must not be asked).
type provimuxAnswerExecutor struct {
	coreauth.ProviderExecutor
	provider string
	answer   string
	calls    atomic.Int32
}

func (e *provimuxAnswerExecutor) Identifier() string { return e.provider }

func (e *provimuxAnswerExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *provimuxAnswerExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	if e.answer == "" {
		return nil, fmt.Errorf("unexpected model-list request")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(e.answer))}, nil
}

func provimuxOff() *config.Config {
	cfg := &config.Config{}
	cfg.Models.LiveLists = new(false)
	return cfg
}

// provimuxRegisteredModels registers a fresh login of the type under cfg, lets the fetcher run
// (when the type is asked and the option is on) and returns what the registry holds for it.
func provimuxRegisteredModels(t *testing.T, cfg *config.Config, provider, executorType, answer string) ([]*ModelInfo, int32) {
	t.Helper()
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	exec := &provimuxAnswerExecutor{provider: executorType, answer: answer}
	svc, auth := provimuxService(t, cfg, exec, &coreauth.Auth{ID: "rules-" + provider, Provider: provider,
		Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"},
		Metadata:   map[string]any{"account_id": "acct-rules", "access_token": "fake"}})
	svc.registerModelsForAuth(t.Context(), auth)
	provimuxRefresh(svc, exec, true)
	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	slices.SortFunc(models, func(a, b *ModelInfo) int { return strings.Compare(a.ID, b.ID) })
	registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	return models, exec.calls.Load()
}

// Parity with e5cf5a2d5, whose live path returned the catalog's entry untouched and whose Kimi and
// xAI logins were never asked: what a login registers with live lists on equals, field for field,
// what the same login registers with them off (the catalog through the same registration path).
func TestProvimuxParityWithTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		provider, executor, answer, target string
	}{
		{"kimi", "kimi", "", ""},
		{"kimi-ai", "kimi", "", ""},
		{"kimi.ai", "kimi", "", ""},
		{"kimi.com", "kimi", "", ""},
		{"xai", "xai", "", ""},
		// The review's answers: Claude without image support and with one effort level, Codex
		// without input modalities, Meta with ids only.
		{"claude", "claude", `{"data":[{"id":"claude-sonnet-5-5","capabilities":{"effort":{"low":{"supported":true}},"thinking":{"supported":true}}}]}`, "claude-sonnet-5-5"},
		{"codex", "codex", `{"models":[{"slug":"gpt-6.1-sol","visibility":"list"}]}`, "gpt-6.1-sol"},
		{"meta", "meta", `{"data":[{"id":"muse-spark-1.3"}]}`, "muse-spark-1.3"},
		// Answers that state other values for every field they have: the catalog's stay.
		{"claude", "claude", `{"data":[{"id":"claude-sonnet-5-5","display_name":"Other","created_at":"2020-01-01T00:00:00Z",
			"max_input_tokens":1000,"max_tokens":10,"capabilities":{"image_input":{"supported":false},
			"effort":{"low":{"supported":true}},"thinking":{"supported":true,"types":{"disabled":{"supported":false},"adaptive":{"supported":false}}}}}]}`, "claude-sonnet-5-5"},
		{"codex", "codex", `{"models":[{"slug":"gpt-6.1-sol","visibility":"list","display_name":"Other","context_window":1000,
			"input_modalities":["text"],"supported_reasoning_levels":[{"effort":"low"}]}]}`, "gpt-6.1-sol"},
	} {
		t.Run(tc.provider+"/"+tc.target, func(t *testing.T) {
			want, _ := provimuxRegisteredModels(t, provimuxOff(), tc.provider, tc.executor, "")
			got, calls := provimuxRegisteredModels(t, provimuxOn(), tc.provider, tc.executor, tc.answer)
			if len(want) == 0 {
				t.Fatal("the catalog registers no model")
			}
			if tc.answer == "" {
				if calls != 0 || !reflect.DeepEqual(got, want) {
					t.Fatalf("calls=%d; registered %v, the catalog %v", calls, liveIDs(got), liveIDs(want))
				}
				return
			}
			model, catalog := hasLiveID(got, tc.target), hasLiveID(want, tc.target)
			if calls != 1 || model == nil || catalog == nil {
				t.Fatalf("calls=%d registered=%v", calls, liveIDs(got))
			}
			if !reflect.DeepEqual(model, catalog) {
				t.Fatalf("fields differ from the catalog's:\n got %+v %+v\nwant %+v %+v", model, model.Thinking, catalog, catalog.Thinking)
			}
			if strings.Join(model.SupportedInputModalities, ",") != "text,image" {
				t.Fatalf("%s inputs = %v", model.ID, model.SupportedInputModalities)
			}
			if tc.provider == "claude" {
				config, err := thinking.ValidateConfig(thinking.ThinkingConfig{Mode: thinking.ModeNone}, model, "claude", "claude", true)
				if err != nil || config.Mode != thinking.ModeNone {
					t.Fatalf("thinking off must stay accepted: %+v %v", config, err)
				}
			}
		})
	}
}

// Claude's thinking-off: an explicit false, a null and a missing field, with and without effort
// levels, all give the catalog's thinking for a model the catalog has.
func TestProvimuxClaudeThinkingOffIsTheCatalogs(t *testing.T) {
	catalog := registry.GetClaudeModels()
	const id = "claude-sonnet-5-5"
	base := hasLiveID(catalog, id)
	if base == nil || base.Thinking == nil || !base.Thinking.ZeroAllowed {
		t.Fatal("the catalog's entry must allow thinking off")
	}
	effort := `,"effort":{"low":{"supported":true}}`
	for _, tc := range []struct{ name, thinking string }{
		{"false", `"thinking":{"types":{"disabled":{"supported":false}}}`},
		{"null", `"thinking":{"types":{"disabled":{"supported":null}}}`},
		{"missing", `"thinking":{"supported":true}`},
	} {
		for _, withEffort := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/effort=%v", tc.name, withEffort), func(t *testing.T) {
				caps := tc.thinking
				if withEffort {
					caps += effort
				}
				list, err := parseClaudeLiveModelList([]byte(`{"data":[{"id":"`+id+`","capabilities":{`+caps+`}}]}`), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				row := hasLiveID(filterByLiveModelList("claude", catalog, list), id)
				if row == nil || !reflect.DeepEqual(row.Thinking, base.Thinking) {
					t.Fatalf("thinking = %+v, the catalog's %+v", row.Thinking, base.Thinking)
				}
			})
		}
	}
}

// The status endpoint names where each field came from: the catalog for every field it states,
// the vendor only for a field the catalog leaves empty, never for a catalog model's levels.
func TestProvimuxFieldSourcesFollowTheRule(t *testing.T) {
	catalog := &ModelInfo{ID: "m", DisplayName: "Catalog", MaxCompletionTokens: 8000}
	vendor := &ModelInfo{ID: "m", DisplayName: "Vendor", ContextLength: 131072, MaxCompletionTokens: 4000,
		SupportedInputModalities: []string{"text"}, Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}}
	merged := fillLiveModel(catalog, vendor)
	sources := map[string]string{}
	provimuxFieldSources(sources, vendor, catalog, false, nil)
	want := map[string]string{"display_name": "built-in", "output": "built-in", "context": "live", "input_modalities": "live"}
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v, want %v", sources, want)
	}
	if merged.DisplayName != "Catalog" || merged.MaxCompletionTokens != 8000 || merged.ContextLength != 131072 ||
		strings.Join(merged.SupportedInputModalities, ",") != "text" || merged.Thinking != nil {
		t.Fatalf("merged = %+v", merged)
	}
	learned := map[string]string{}
	provimuxFieldSources(learned, vendor, nil, false, nil)
	if learned["reasoning_levels"] != "live" || learned["display_name"] != "live" {
		t.Fatalf("a learned model's fields are the vendor's: %v", learned)
	}
}

// Two active credentials of one account share one cache identity; when one of them moves to
// another account, the other keeps its list on a restart without network.
func TestProvimuxSharedIdentitySurvivesAnotherLoginsChange(t *testing.T) {
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	const id = "claude-fixture-live-only"
	exec := &provimuxAnswerExecutor{provider: "claude", answer: `{"data":[{"id":"` + id + `"}]}`}
	svc, a := provimuxService(t, provimuxOn(), exec, &coreauth.Auth{ID: "a.json", Provider: "claude",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "acct-shared", "access_token": "fake-a"}})
	b := a.Clone()
	b.ID = "b.json"
	b.Metadata["access_token"] = "fake-b"
	if _, err := svc.coreManager.Register(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(b.ID) })
	shared := liveModelListIdentity(b)
	if liveModelListIdentity(a) != shared {
		t.Fatal("the two credentials must share one identity")
	}
	provimuxRefresh(svc, exec, true)
	store := liveModelLists.cache.Load()
	a.Metadata["account_id"] = "acct-other"
	if _, err := svc.coreManager.Update(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	provimuxRefresh(svc, exec, false)
	if _, err := store.ReadLive("claude", liveModelListIdentity(a)); err != nil {
		t.Fatalf("the changed login's new file: %v", err)
	}
	if _, err := store.ReadLive("claude", shared); err != nil {
		t.Fatalf("the shared file must stay: %v", err)
	}
	// Restart without network.
	resetLiveModelLists()
	liveModelLists.cache.Store(nil)
	liveModelLists.mu.Lock()
	liveModelLists.cacheTried = make(map[string]string)
	liveModelLists.mu.Unlock()
	svc.registerModelsForAuth(t.Context(), b)
	if !slices.Contains(provimuxRegistered(b), id) {
		t.Fatalf("b.json lost its list on restart: %v", provimuxRegistered(b))
	}
}

// The 7-day limit is read from the list's own fetch time, never from the file's: a file last
// modified 8 days ago whose list was fetched an hour ago is used at start.
func TestProvimuxOldFileWithARecentListIsUsed(t *testing.T) {
	provimuxLiveReset(t)
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	exec := &provimuxAnswerExecutor{provider: "claude"}
	svc, auth := provimuxService(t, provimuxOn(), exec, &coreauth.Auth{ID: "active.json", Provider: "claude",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "acct-active"}})
	now := time.Now().UTC()
	liveModelListNow = func() time.Time { return now }
	store := modellists.NewStore(filepath.Join(dir, modellists.FolderName))
	identity := liveModelListIdentity(auth)
	const id = "claude-fixture-learned"
	if err := store.WriteLive("claude", identity, now.Add(-time.Hour), []string{id}, []*registry.ModelInfo{{ID: id, Type: "claude"}}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(store.Dir(), "live-"+modellists.IdentityHash(identity)+".json")
	old := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	svc.registerModelsForAuth(t.Context(), auth)
	if _, err := store.ReadLive("claude", identity); err != nil {
		t.Fatalf("the file must stay: %v", err)
	}
	if !slices.Contains(provimuxRegistered(auth), id) || exec.calls.Load() != 0 {
		t.Fatalf("registered %v, requests %d", provimuxRegistered(auth), exec.calls.Load())
	}
}

// A login that switches to an identity that already has a file reads it, and the next fetch
// replaces it; the previous identity's file stays (files are never deleted).
func TestProvimuxSwitchToAnIdentityWithAFile(t *testing.T) {
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	const id = "claude-fixture-live-only"
	exec := &provimuxAnswerExecutor{provider: "claude", answer: `{"data":[{"id":"` + id + `"}]}`}
	svc, a := provimuxService(t, provimuxOn(), exec, &coreauth.Auth{ID: "a.json", Provider: "claude",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "acct-old"}})
	previous := liveModelListIdentity(a)
	provimuxRefresh(svc, exec, true)
	store := liveModelLists.cache.Load()
	a.Metadata["account_id"] = "acct-cached"
	identity := liveModelListIdentity(a)
	if err := store.WriteLive("claude", identity, time.Now(), []string{"claude-cached-only"},
		[]*registry.ModelInfo{{ID: "claude-cached-only", Type: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.coreManager.Update(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	svc.registerModelsForAuth(t.Context(), a)
	if state, _ := (provimuxLiveListSource{}).ModelListLogin(a, true, false, false); !state.FromCache || state.Source != "live" {
		t.Fatalf("the login must read its identity's file: %+v", state)
	}
	if !slices.Contains(provimuxRegistered(a), "claude-cached-only") {
		t.Fatalf("registered %v", provimuxRegistered(a))
	}
	provimuxRefresh(svc, exec, false)
	state, _ := (provimuxLiveListSource{}).ModelListLogin(a, true, false, false)
	if state.FromCache || !state.Held || !slices.Contains(provimuxRegistered(a), id) || slices.Contains(provimuxRegistered(a), "claude-cached-only") {
		t.Fatalf("after the fetch: %+v %v", state, provimuxRegistered(a))
	}
	if file, err := store.ReadLive("claude", identity); err != nil || !slices.Contains(file.IDs, id) {
		t.Fatalf("the fetch must replace the identity's file: %+v %v", file, err)
	}
	if _, err := store.ReadLive("claude", previous); err != nil {
		t.Fatalf("the previous identity's file stays: %v", err)
	}
}

// Devin's real client: an answer whose body stalls ends at the deadline, the held list stays.
func TestProvimuxDevinBodyDeadline(t *testing.T) {
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	previous := liveModelListFetchTimeout
	liveModelListFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { liveModelListFetchTimeout = previous })
	var hang atomic.Bool
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if hang.Load() {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(provimuxDevinRecorded)
	}))
	t.Cleanup(server.Close)
	cfg := provimuxOn()
	exec := executor.NewDevinExecutor(cfg)
	svc, auth := provimuxService(t, cfg, exec, &coreauth.Auth{ID: "devin-deadline.json", Provider: "devin",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL},
		Metadata:   map[string]any{"session_token": "fake-devin-token", "user_id": "fake"}})
	provimuxRefresh(svc, exec, true)
	before := provimuxRegistered(auth)
	if !slices.Contains(before, "devin/gemini-4-neon") {
		t.Fatalf("initial list = %v", before)
	}
	hang.Store(true)
	provimuxRefresh(svc, exec, true)
	state := (provimuxLiveListSource{}).State(auth)
	if after := provimuxRegistered(auth); !slices.Equal(before, after) || requests.Load() != 2 || state.Fetching ||
		!state.HasList || state.Failure == nil || state.Failure.Category != provimuxLiveFailureNetwork {
		t.Fatalf("after=%v requests=%d state=%+v failure=%+v", after, requests.Load(), state, state.Failure)
	}
}

// A timeout while Meta mints its key is a network failure (model-lists.md 13.2), not a credential one.
func TestProvimuxCredentialTimeoutIsNetwork(t *testing.T) {
	provimuxLiveReset(t)
	t.Setenv("WRITABLE_PATH", t.TempDir())
	previous := liveModelListFetchTimeout
	liveModelListFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { liveModelListFetchTimeout = previous })
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	t.Setenv("META_MINT_URL", server.URL)
	cfg := provimuxOn()
	exec := executor.NewMetaExecutor(cfg)
	svc, auth := provimuxService(t, cfg, exec, &coreauth.Auth{ID: "meta-deadline.json", Provider: "meta",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"account_id": "acct-meta", "dca_token": "dca:fixture"}})
	provimuxRefresh(svc, exec, true)
	state := (provimuxLiveListSource{}).State(auth)
	if requests.Load() != 1 || state.Fetching || state.Failure == nil || state.Failure.Category != provimuxLiveFailureNetwork {
		t.Fatalf("requests=%d state=%+v failure=%+v", requests.Load(), state, state.Failure)
	}
	if category, _ := provimuxLiveListFailureOf(fmt.Errorf("prepare credentials: %w", errors.New("meta key exchange: HTTP 403"))); category != provimuxLiveFailureCredentials {
		t.Fatalf("a refused mint stays a credential failure: %s", category)
	}
}
