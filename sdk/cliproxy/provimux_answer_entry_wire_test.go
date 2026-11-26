package cliproxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: every executor's answer names the entry of what the stand-in received (answered-by.md 8.4, "the one test
// that matters"). For each executor and each path it sends a model request on, the stand-in records the host it was
// called on and the body it received; the answer's X-Provimux-Model-Sent is the model the stand-in read, and
// X-Provimux-Entry is the map applied to what it received, or absent where the map has none. Each name the client asks
// for is an alias of another id, so what is asked and what is sent differ. Every key, login and id is invented.

// sentMatrixConfig: the key families, each on its executor's default host or the host named.
const sentMatrixConfig = `api-keys:
  claude:
    - name: provimux-test-claude
      models:
        - name: provimux-test-claude-sent
          alias: provimux-test-claude-key
      keys:
        - api-key: sk-provimux-test-claude-key
  codex:
    - name: provimux-test-codex
      base-url: https://api.openai.com/v1
      models:
        - name: provimux-test-codex-sent
          alias: provimux-test-codex-key
      keys:
        - api-key: sk-provimux-test-codex-key
  gemini:
    - name: provimux-test-gemini
      models:
        - name: provimux-test-gemini-sent
          alias: provimux-test-gemini-key
      keys:
        - api-key: provimux-test-gemini-key
  vertex:
    - name: provimux-test-vertex
      base-url: https://aiplatform.googleapis.com
      models:
        - name: provimux-test-vertex-sent
          alias: provimux-test-vertex-key
      keys:
        - api-key: provimux-test-vertex-key
  xai:
    - name: provimux-test-xai
      base-url: https://api.x.ai/v1
      models:
        - name: provimux-test-xai-sent
          alias: provimux-test-xai-key
      keys:
        - api-key: provimux-test-xai-key
  meta:
    - name: provimux-test-meta
      models:
        - name: provimux-test-meta-key-sent
          alias: provimux-test-meta-key
      keys:
        - api-key: provimux-test-meta-key
  openai-compatibility:
    - name: provimux-test-router
      base-url: https://openrouter.ai/api/v1
      models:
        - name: provimux-test-lab/provimux-test-routed:nitro
          alias: provimux-test-router
      keys:
        - api-key: sk-provimux-test-router
    - name: provimux-test-house
      base-url: https://llm.provimux-test.example/v1
      models:
        - name: provimux-test-house-sent
          alias: provimux-test-house
      keys:
        - api-key: sk-provimux-test-house
`

// sentFar is an expiry the test cannot reach: now plus a day.
func sentFar() string { return time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339) }

// sentLogins registers the logins of the matrix and returns them by name.
func sentLogins(t *testing.T, h *sentHarness) map[string]*coreauth.Auth {
	t.Helper()
	far := sentFar()
	login := func(provider, file string, metadata map[string]any) *coreauth.Auth {
		metadata["type"] = provider
		return &coreauth.Auth{ID: file, FileName: file, Provider: provider, Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/" + file, "source": "/provimux-test/auths/" + file},
			Metadata:   metadata}
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	serviceAccount := map[string]any{"type": "service_account", "client_email": "vertex@provimux-test.invalid", "project_id": "provimux-test-project",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), "token_uri": "https://oauth2.googleapis.com/token"}
	return map[string]*coreauth.Auth{
		"claude": h.register(login("claude", "provimux-test-claude-login.json", map[string]any{"access_token": "provimux-test-claude-token", "expired": far,
			"organization_uuid": "0f0e0d0c-0b0a-4909-8807-060504030201", "account_uuid": "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"}), "provimux-test-claude-login"),
		"codex": h.register(login("codex", "provimux-test-codex-login.json", map[string]any{"access_token": "provimux-test-codex-token", "account_id": "provimux-test-workspace", "expired": far}),
			"provimux-test-codex-login"),
		"antigravity": h.register(login("antigravity", "provimux-test-antigravity.json", map[string]any{"access_token": "provimux-test-ag-token", "refresh_token": "provimux-test-ag-refresh",
			"project_id": "provimux-test-project", "email": "ag@provimux-test.invalid", "expired": far, "expiry": far}), "provimux-test-antigravity"),
		"devin": h.register(login("devin", "provimux-test-devin.json", map[string]any{"api_key": "provimux-test-devin-key", "user_id": "provimux-test-user", "org_id": "provimux-test-org"}),
			"swe-2-1"),
		"meta": h.register(login("meta", "provimux-test-meta-login.json", map[string]any{"api_key": "provimux-test-meta-token", "user_email": "meta@provimux-test.invalid", "expired": far}),
			"provimux-test-meta-login"),
		// A login's token configured as a Claude key (a setup token: no profile to read).
		"claude-oat": h.register(&coreauth.Auth{ID: "provimux-test-claude-oat", Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "apikey", "api_key": "sk-ant-oat01-provimux-test-token", "source": "config:claude[provimux-test-oat]"},
			Metadata:   map[string]any{"skip_account_profile": true}}, "provimux-test-claude-oat"),
		"kimi": h.register(login("kimi", "provimux-test-kimi.json", map[string]any{"access_token": "provimux-test-kimi-token", "device_id": "provimux-test-device", "expired": far}),
			"provimux-test-kimi"),
		"vertex": h.register(login("vertex", "provimux-test-vertex-sa.json", map[string]any{"project_id": "provimux-test-project", "location": "us-central1", "service_account": serviceAccount}),
			"provimux-test-vertex-sa"),
	}
}

// sentMatrixCase is one executor and path: the name asked for, the path and the body, the host the stand-in must be
// called on, and the entry of what it received ("" none; {sent} stands for the model it received).
type sentMatrixCase struct {
	executor string // the executor that sends (sentExecutorCoverage)
	name     string
	login    string // a login of sentLogins, or "" for the config's key that serves model
	model    string
	path     string
	body     string
	host     string
	entry    string
}

func sentMatrix() []sentMatrixCase {
	chat := func(model string, stream bool) string { return chatBody(model, stream, "") }
	claude := func(model string, stream bool) string {
		return `{"model":"` + model + `","max_tokens":16,"stream":` + map[bool]string{true: "true", false: "false"}[stream] + `,"messages":[{"role":"user","content":"provimux-test question"}]}`
	}
	return []sentMatrixCase{
		{"claude", "a Claude key", "", "provimux-test-claude-key", "/v1/chat/completions", chat("provimux-test-claude-key", false), "api.anthropic.com", "anthropic/{sent}"},
		{"claude", "a Claude key, a stream", "", "provimux-test-claude-key", "/v1/chat/completions", chat("provimux-test-claude-key", true), "api.anthropic.com", "anthropic/{sent}"},
		{"claude", "a Claude key, Claude's own protocol", "", "provimux-test-claude-key", "/v1/messages", claude("provimux-test-claude-key", false), "api.anthropic.com", "anthropic/{sent}"},
		{"claude", "a Claude key, a token count the vendor answers", "", "provimux-test-claude-key", "/v1/messages/count_tokens", claude("provimux-test-claude-key", false), "api.anthropic.com", "anthropic/{sent}"},
		{"claude", "a login's token configured as a Claude key: sent as a login's", "claude-oat", "provimux-test-claude-oat", "/v1/chat/completions", chat("provimux-test-claude-oat", false), "api.anthropic.com", "claude-subscription/{sent}"},
		{"claude", "a Claude login", "claude", "provimux-test-claude-login", "/v1/messages", claude("provimux-test-claude-login", true), "api.anthropic.com", "claude-subscription/{sent}"},
		{"codex", "a Codex login", "codex", "provimux-test-codex-login", "/v1/chat/completions", chat("provimux-test-codex-login", false), "chatgpt.com", "openai-codex/{sent}"},
		{"codex", "a Codex login, the Responses API, a stream", "codex", "provimux-test-codex-login", "/v1/responses", `{"model":"provimux-test-codex-login","stream":true,"input":"provimux-test question"}`, "chatgpt.com", "openai-codex/{sent}"},
		{"codex", "a Codex key on api.openai.com", "", "provimux-test-codex-key", "/v1/chat/completions", chat("provimux-test-codex-key", false), "api.openai.com", "openai/{sent}"},
		{"gemini", "a Gemini key", "", "provimux-test-gemini-key", "/v1/chat/completions", chat("provimux-test-gemini-key", false), "generativelanguage.googleapis.com", "google/{sent}"},
		{"gemini", "a Gemini key, a stream", "", "provimux-test-gemini-key", "/v1/chat/completions", chat("provimux-test-gemini-key", true), "generativelanguage.googleapis.com", "google/{sent}"},
		{"gemini", "a Gemini key, Gemini's token count", "", "provimux-test-gemini-key", "/v1beta/models/provimux-test-gemini-key:countTokens", `{"contents":[{"role":"user","parts":[{"text":"provimux-test question"}]}]}`, "generativelanguage.googleapis.com", "google/{sent}"},
		{"vertex", "a Vertex key on Google's host", "", "provimux-test-vertex-key", "/v1/chat/completions", chat("provimux-test-vertex-key", false), "aiplatform.googleapis.com", "google-vertex/{sent}"},
		{"vertex", "a Vertex service account", "vertex", "provimux-test-vertex-sa", "/v1/chat/completions", chat("provimux-test-vertex-sa", false), "", "google-vertex/{sent}"},
		{"xai", "an xAI key", "", "provimux-test-xai-key", "/v1/chat/completions", chat("provimux-test-xai-key", false), "api.x.ai", "xai/{sent}"},
		{"meta", "a Meta login", "meta", "provimux-test-meta-login", "/v1/chat/completions", chat("provimux-test-meta-login", false), "api.meta.ai", "meta-subscription/{sent}"},
		{"meta", "a Meta key: none", "", "provimux-test-meta-key", "/v1/chat/completions", chat("provimux-test-meta-key", false), "api.meta.ai", ""},
		{"openai-compatibility", "OpenRouter, its routing suffix taken off", "", "provimux-test-router", "/v1/chat/completions", chat("provimux-test-router", false), "openrouter.ai", "openrouter/provimux-test-lab/provimux-test-routed"},
		{"openai-compatibility", "OpenRouter, a stream", "", "provimux-test-router", "/v1/chat/completions", chat("provimux-test-router", true), "openrouter.ai", "openrouter/provimux-test-lab/provimux-test-routed"},
		{"openai-compatibility", "a compatible key on a host the map lacks: none", "", "provimux-test-house", "/v1/chat/completions", chat("provimux-test-house", false), "llm.provimux-test.example", ""},
		{"copilot", "Copilot", "copilot", "copilot/provimux-test-copilot", "/v1/chat/completions", chat("copilot/provimux-test-copilot", false), "api.individual.githubcopilot.com", "github-copilot/{sent}"},
		{"antigravity", "Antigravity", "antigravity", "provimux-test-antigravity", "/v1/chat/completions", chat("provimux-test-antigravity", false), "daily-cloudcode-pa.googleapis.com", "antigravity/{sent}"},
		{"devin", "Devin: its request id, the model in Devin's catalog", "devin", "swe-2-1", "/v1/chat/completions", chat("swe-2-1", false), "server.codeium.com", "devin/swe-2-1"},
		{"devin", "Devin, an effort variant: the same model in Devin's catalog", "devin", "swe-2-1", "/v1/chat/completions", chatBody("swe-2-1", false, `,"reasoning_effort":"high"`), "server.codeium.com", "devin/swe-2-1"},
		{"kimi", "a Kimi login: none", "kimi", "provimux-test-kimi", "/v1/chat/completions", chat("provimux-test-kimi", false), "", ""},
	}
}

func TestProvimuxAnswerEntryEveryExecutorOnTheWire(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	logins := sentLogins(t, h)
	logins["copilot"] = registerCopilotRoutingTestAuth(t, h.service.coreManager, "", "provimux-test-copilot")
	copilotTestPublisher(t)(append(registry.GetStaticModelDefinitionsByChannel("copilot"), &registry.ModelInfo{ID: "provimux-test-copilot"}))
	h.service.registerExecutorForAuth(logins["copilot"], false)
	if !h.service.refreshModelRegistrationForAuth(logins["copilot"]) {
		t.Fatal("fixture: Copilot's models")
	}
	for _, tc := range sentMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			h.t = t
			h.stand.reset()
			account := logins[tc.login]
			if tc.login == "" {
				account = sentKeyFor(t, h, tc.model)
			}
			answer := h.post(tc.path, tc.body, nil)
			got := h.checkSent(answer, account, tc.host, strings.ReplaceAll(tc.entry, "{sent}", sentModelOf(h)))
			if got.Model == tc.model && tc.login == "" {
				t.Fatalf("fixture: the alias %s was sent as it was asked", tc.model)
			}
			if got.Scheme != "https" || got.Port != "" {
				t.Fatalf("fixture: sent to %s://%s:%s", got.Scheme, got.Host, got.Port)
			}
		})
	}
}

// sentModelOf is the model the stand-in last received in an answered model request.
func sentModelOf(h *sentHarness) string {
	answered := h.stand.answered()
	if len(answered) == 0 {
		return ""
	}
	return answered[len(answered)-1].Model
}

// sentKeyFor returns the config's key that serves model.
func sentKeyFor(t *testing.T, h *sentHarness, model string) *coreauth.Auth {
	t.Helper()
	var found *coreauth.Auth
	for _, auth := range h.service.coreManager.List() {
		for _, served := range namesServed(t, auth) {
			if served == model {
				if found != nil {
					t.Fatalf("fixture: %s is served by %s and %s", model, found.ID, auth.ID)
				}
				found = auth
			}
		}
	}
	if found == nil {
		t.Fatalf("fixture: no key serves %s", model)
	}
	return found
}

// sentHeader builds a header from name, value pairs.
func sentHeader(pairs ...string) http.Header {
	header := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		header.Set(pairs[i], pairs[i+1])
	}
	return header
}
