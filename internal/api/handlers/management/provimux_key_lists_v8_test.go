package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// key-model-lists.md 14: fixed-models and an openai-compatibility entry's excluded-models through the
// console's own save path (GET /config, PUT /config/api-keys/<family>), and a Vertex key's
// fixed-models (accepted, no effect) never breaking a later save.

func keyV8Router(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v8/management/config", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	return router
}

func keyV8Do(t *testing.T, router *gin.Engine, method, url, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func keyV8Load(t *testing.T, text string) (*Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return &Handler{cfg: cfg, configFilePath: path}, path
}

const keyV8Every = `openai-compatibility:
  - name: openrouter
    base-url: https://openrouter.ai/api/v1
    fixed-models: true
    excluded-models: ["x-ai/*"]
    api-key-entries:
      - api-key: sk-or
    models:
      - name: a/b
        alias: ab
claude-api-key:
  - api-key: sk-ant
    fixed-models: true
    models:
      - name: claude-x
codex-api-key:
  - api-key: sk-cx
    base-url: https://cx.example/v1
    fixed-models: true
    models:
      - name: gpt-x
xai-api-key:
  - api-key: sk-xai
    base-url: https://api.x.ai/v1
    fixed-models: true
    models:
      - name: grok-x
meta-api-key:
  - api-key: sk-meta
    fixed-models: true
    models:
      - name: muse-x
gemini-api-key:
  - api-key: AIza
    fixed-models: true
    models:
      - name: gem-x
interactions-api-key:
  - api-key: AIza-i
    fixed-models: true
    models:
      - name: gem-i
vertex-api-key:
  - api-key: vk
    base-url: https://v.example
    fixed-models: true
    models:
      - name: vx
        alias: vx
`

func keyV8AllFixed(cfg *config.Config) bool {
	return len(cfg.OpenAICompatibility) == 1 && cfg.OpenAICompatibility[0].FixedModels &&
		strings.Join(cfg.OpenAICompatibility[0].ExcludedModels, ",") == "x-ai/*" &&
		cfg.ClaudeKey[0].FixedModels && cfg.CodexKey[0].FixedModels && cfg.XAIKey[0].FixedModels && cfg.MetaKey[0].FixedModels &&
		cfg.GeminiKey[0].FixedModels && cfg.InteractionsKey[0].FixedModels && cfg.VertexCompatAPIKey[0].FixedModels
}

// The console reads GET /config and writes each family back with PUT /config/api-keys/<family>: both
// fields survive a read and an unchanged write, for every key family, and the file loads again.
func TestProvimuxKeyListFieldsConsoleSavePath(t *testing.T) {
	h, path := keyV8Load(t, keyV8Every)
	if !keyV8AllFixed(h.cfg) {
		t.Fatalf("loaded: %+v", h.cfg)
	}
	router := keyV8Router(h)
	code, body := keyV8Do(t, router, http.MethodGet, "/v8/management/config", "")
	if code != http.StatusOK {
		t.Fatalf("GET: %d %s", code, body)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	groups, _ := raw["api-keys"].(map[string]any)
	families := []string{"openai-compatibility", "claude", "codex", "xai", "meta", "gemini", "interactions", "vertex"}
	for _, family := range families {
		out, _ := json.Marshal(groups[family])
		if !strings.Contains(string(out), `"fixed-models":true`) {
			t.Fatalf("GET /config: api-keys.%s lacks fixed-models: %s", family, out)
		}
	}
	if out, _ := json.Marshal(groups["openai-compatibility"]); !strings.Contains(string(out), `"excluded-models":["x-ai/*"]`) {
		t.Fatalf("GET /config: openai-compatibility lacks excluded-models: %s", out)
	}
	for _, family := range families {
		out, _ := json.Marshal(groups[family])
		if code, body := keyV8Do(t, router, http.MethodPut, "/v8/management/config/api-keys/"+family, string(out)); code != http.StatusOK {
			t.Fatalf("PUT api-keys/%s: %d %s", family, code, body)
		}
	}
	saved, _ := os.ReadFile(path)
	again, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v\n%s", err, saved)
	}
	if !keyV8AllFixed(again) || !keyV8AllFixed(h.cfg) {
		t.Fatalf("after the round trip:\n%s", saved)
	}
}

// fixed-models on a Vertex key or group loads, and an unrelated v8 write afterwards still succeeds.
func TestProvimuxKeyListFieldsVertexNeverBreaksASave(t *testing.T) {
	for name, text := range map[string]string{
		"legacy key": "vertex-api-key:\n  - api-key: k\n    base-url: https://v.example\n    fixed-models: true\n    models:\n      - name: m\n        alias: m\n",
		"v8 group":   "config-version: 8\napi-keys:\n  vertex:\n    - name: v1\n      base-url: https://v.example\n      fixed-models: true\n      models:\n        - name: m\n          alias: m\n      keys:\n        - api-key: k\n",
		"v8 key":     "config-version: 8\napi-keys:\n  vertex:\n    - name: v1\n      base-url: https://v.example\n      models:\n        - name: m\n          alias: m\n      keys:\n        - api-key: k\n          fixed-models: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			h, path := keyV8Load(t, text)
			if len(h.cfg.VertexCompatAPIKey) != 1 || !h.cfg.VertexCompatAPIKey[0].FixedModels {
				t.Fatalf("loaded: %+v", h.cfg.VertexCompatAPIKey)
			}
			router := keyV8Router(h)
			if code, body := keyV8Do(t, router, http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":2}}}`); code != http.StatusOK {
				t.Fatalf("unrelated PATCH /config: %d %s", code, body)
			}
			if code, body := keyV8Do(t, router, http.MethodPut, "/v8/management/config/api-keys/claude", `[{"name":"c","keys":[{"api-key":"k2"}]}]`); code != http.StatusOK {
				t.Fatalf("PUT api-keys/claude: %d %s", code, body)
			}
			saved, _ := os.ReadFile(path)
			again, err := config.LoadConfig(path)
			if err != nil || !again.VertexCompatAPIKey[0].FixedModels || len(again.ClaudeKey) != 1 {
				t.Fatalf("reload: %v\n%s", err, saved)
			}
		})
	}
}

// The legacy PATCH handlers name their fields one by one and do not take fixed-models: a PATCH
// naming it answers 200 and leaves the field as it was (key-model-lists.md 14). PUT and the v8
// endpoints write it.
func TestProvimuxKeyListFieldsLegacyPatchIgnoresFixedModels(t *testing.T) {
	h, _ := keyV8Load(t, keyV8Every)
	for _, tc := range []struct {
		patch func(*Handler, *gin.Context)
		fixed func() bool
	}{
		{(*Handler).PatchOpenAICompat, func() bool { return h.cfg.OpenAICompatibility[0].FixedModels }},
		{(*Handler).PatchClaudeKey, func() bool { return h.cfg.ClaudeKey[0].FixedModels }},
	} {
		keyListsRequest(t, h, tc.patch, http.MethodPatch, `{"index":0,"value":{"fixed-models":false}}`)
		if !tc.fixed() {
			t.Fatal("a legacy PATCH changed fixed-models; update the design's as-built notes")
		}
	}
}
