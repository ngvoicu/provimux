package provimux

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the fixture of answered-by (answered-by.md 8.4). This file and answered_by_today_test.go use only what
// the commit before answered-by has, so that they build there: the golden files under testdata/answered-by are
// what that commit writes for the fixture (go test ./test/provimux -run TestProvimuxAnsweredByOffIsToday
// -provimux-answered-golden, with answered_by_switch_test.go replaced by a stub whose switch does nothing). With
// models.answered-by off, this build must write the same bytes. Every key, login and id here is invented.

var provimuxAnsweredGolden = flag.Bool("provimux-answered-golden", false, "write the answered-by golden files from this build")

const (
	answeredSharedModel = "provimux-answered-shared"
	answeredKeyModel    = "provimux-answered-key-only"
)

// answeredByListAuths are the accounts of the list fixture: finding 8's two Claude keys without a base URL (the same
// provider, kind and no host), one Claude login held in two files, a Codex login, and an OpenAI-compatible key.
func answeredByListAuths() []*coreauth.Auth {
	claudeLogin := func(file string) *coreauth.Auth {
		return &coreauth.Auth{ID: file, FileName: file, Provider: "claude", Label: "ada@fixture.example.test", Status: coreauth.StatusActive,
			Attributes: map[string]string{"path": "/fixture/auths/" + file, "source": "/fixture/auths/" + file, "priority": "5"},
			Metadata: map[string]any{"type": "claude", "access_token": "fixture-token-" + file, "email": "ada@fixture.example.test",
				"organization_uuid": "0f0e0d0c-0b0a-4909-8807-060504030201", "account_uuid": "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"}}
	}
	return []*coreauth.Auth{
		{ID: "claude:apikey:dear", Provider: "claude", Status: coreauth.StatusActive, Label: "claude-apikey",
			Attributes: map[string]string{"source": "config:claude[dear]", "api_key": "sk-fixture-dear", "priority": "10"}},
		{ID: "claude:apikey:cheap", Provider: "claude", Status: coreauth.StatusActive, Label: "claude-apikey",
			Attributes: map[string]string{"source": "config:claude[cheap]", "api_key": "sk-fixture-cheap", "priority": "10"}},
		claudeLogin("claude-ada.json"),
		claudeLogin("claude-ada-again.json"),
		{ID: "codex-bo.json", FileName: "codex-bo.json", Provider: "codex", Label: "bo@fixture.example.test", Status: coreauth.StatusActive,
			Attributes: map[string]string{"path": "/fixture/auths/codex-bo.json", "source": "/fixture/auths/codex-bo.json", "priority": "1"},
			Metadata:   map[string]any{"type": "codex", "access_token": "fixture-token-codex", "account_id": "ws-fixture-1", "email": "bo@fixture.example.test"}},
		{ID: "openai-compatibility:lab:k", Provider: "openai-compatible-lab", Status: coreauth.StatusActive, Label: "lab",
			Attributes: map[string]string{"source": "config:lab[k]", "api_key": "sk-fixture-lab", "base_url": "https://llm.lab.example.test/v1", "compat_name": "lab", "provider_key": "openai-compatible-lab", "priority": "0"}},
	}
}

// registerAnsweredByLists registers the list fixture with a new manager and the global registry.
func registerAnsweredByLists(t *testing.T) (*coreauth.Manager, []*coreauth.Auth) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	auths := answeredByListAuths()
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		models := []*registry.ModelInfo{{ID: answeredSharedModel, Object: "model", OwnedBy: "provimux-test", Created: 1700000000}}
		if auth.Attributes["api_key"] != "" {
			models = append(models, &registry.ModelInfo{ID: answeredKeyModel, Object: "model", OwnedBy: "provimux-test", Created: 1700000001})
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return manager, manager.List()
}

// answeredByModels answers GET /v1/models with the query given. The models are listed in the registry's map order,
// so the answer is written out as its envelope, then each model's bytes as sent, in sorted order.
func answeredByModels(t *testing.T, manager *coreauth.Manager, query string) []byte {
	t.Helper()
	body := answeredByModelsRaw(t, manager, query)
	data := ojson.GetBytes(body, "data")
	if !data.IsArray() {
		t.Fatalf("/v1/models%s: no data: %s", query, body)
	}
	models := make([]string, 0, len(data.Array()))
	for _, model := range data.Array() {
		models = append(models, model.Raw)
	}
	sort.Strings(models)
	return []byte(strings.Replace(string(body), data.Raw, "[…]", 1) + "\n" + strings.Join(models, "\n"))
}

// answeredByModelsRaw answers GET /v1/models with the query given, as sent.
func answeredByModelsRaw(t *testing.T, manager *coreauth.Manager, query string) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models"+query, nil)
	openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(nil, manager)).OpenAIModels(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("/v1/models%s: %d %s", query, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

var answeredObservedAt = regexp.MustCompile(`"observed_at":"[^"]*"`)

// answeredByRoutes answers GET /v8/management/provimux/model-routes, its clock written as a constant.
func answeredByRoutes(t *testing.T, manager *coreauth.Manager) []byte {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/model-routes", nil)
	management.NewHandlerWithoutConfigFilePath(&config.Config{}, manager).ProvimuxModelRoutes(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("model-routes: %d %s", recorder.Code, recorder.Body.String())
	}
	return answeredObservedAt.ReplaceAll(recorder.Body.Bytes(), []byte(`"observed_at":"(clock)"`))
}

// checkAnsweredGolden compares got with the golden file name, or writes it under -provimux-answered-golden.
func checkAnsweredGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "answered-by", name)
	if *provimuxAnsweredGolden {
		if errMkdir := os.MkdirAll(filepath.Dir(path), 0o755); errMkdir != nil {
			t.Fatal(errMkdir)
		}
		if errWrite := os.WriteFile(path, got, 0o644); errWrite != nil {
			t.Fatal(errWrite)
		}
		return
	}
	want, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("golden %s: %v", name, errRead)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from what the commit before answered-by writes:\n got %s\nwant %s", name, got, want)
	}
}
