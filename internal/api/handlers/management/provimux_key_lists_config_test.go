package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// key-model-lists.md 6 and 6.1: fixed-models and an openai-compatibility entry's excluded-models
// pass load, PUT and PATCH, normalised as on the built-in key types.

func keyListsRequest(t *testing.T, h *Handler, handler func(*Handler, *gin.Context), method, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v0/management/openai-compatibility", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(h, ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, body %s", method, rec.Code, rec.Body.String())
	}
	return rec
}

func TestProvimuxKeyListFieldsLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    fixed-models: true
    excluded-models: [" X-AI/* ", "x-ai/*"]
    models: []
claude-api-key:
  - api-key: sk-ant
    fixed-models: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	compat := cfg.OpenAICompatibility[0]
	if !compat.FixedModels || !slices.Equal(compat.ExcludedModels, []string{"x-ai/*"}) || !cfg.ClaudeKey[0].FixedModels {
		t.Fatalf("loaded = %+v, claude fixed %v", compat, cfg.ClaudeKey[0].FixedModels)
	}
}

func TestProvimuxKeyListFieldsPutAndPatch(t *testing.T) {
	cfg := &config.Config{}
	configFile := writeTestConfigFile(t)
	h := &Handler{cfg: cfg, configFilePath: configFile}

	keyListsRequest(t, h, (*Handler).PutOpenAICompat, http.MethodPut,
		`[{"name":"acme","base-url":"https://acme.example/v1","fixed-models":true,"excluded-models":[" X-AI/* ","x-ai/*"],"models":[]}]`)
	if got := cfg.OpenAICompatibility[0]; !got.FixedModels || !slices.Equal(got.ExcludedModels, []string{"x-ai/*"}) {
		t.Fatalf("after PUT: %+v", got)
	}
	saved, _ := os.ReadFile(configFile)
	if !strings.Contains(string(saved), "fixed-models: true") || !strings.Contains(string(saved), "x-ai/*") {
		t.Fatalf("saved YAML:\n%s", saved)
	}

	// GET returns both fields.
	rec := keyListsRequest(t, h, (*Handler).GetOpenAICompat, http.MethodGet, "")
	var listed struct {
		Items []map[string]any `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("GET: %v %s", err, rec.Body.String())
	}
	if listed.Items[0]["fixed-models"] != true || len(listed.Items[0]["excluded-models"].([]any)) != 1 {
		t.Fatalf("GET item = %v", listed.Items[0])
	}

	// A PATCH that does not name the fields keeps them.
	keyListsRequest(t, h, (*Handler).PatchOpenAICompat, http.MethodPatch, `{"index":0,"value":{"priority":3}}`)
	if got := cfg.OpenAICompatibility[0]; !got.FixedModels || !slices.Equal(got.ExcludedModels, []string{"x-ai/*"}) || got.Priority != 3 {
		t.Fatalf("after an unrelated PATCH: %+v", got)
	}
	// A PATCH of excluded-models replaces them, normalised.
	keyListsRequest(t, h, (*Handler).PatchOpenAICompat, http.MethodPatch,
		`{"index":0,"value":{"excluded-models":[" Text-Embedding-* ","*-preview","*-PREVIEW"]}}`)
	if got := cfg.OpenAICompatibility[0].ExcludedModels; !slices.Equal(got, []string{"text-embedding-*", "*-preview"}) {
		t.Fatalf("after PATCH: %v", got)
	}
	keyListsRequest(t, h, (*Handler).PatchOpenAICompat, http.MethodPatch, `{"index":0,"value":{"excluded-models":[]}}`)
	if got := cfg.OpenAICompatibility[0].ExcludedModels; len(got) != 0 {
		t.Fatalf("an empty PATCH clears the list: %v", got)
	}
}
