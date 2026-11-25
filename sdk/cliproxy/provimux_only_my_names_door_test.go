package cliproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// D1-D4 (only-my-names.md section 9): a full server (the real Service, watcher, API server and
// handlers) over one stand-in vendor that counts every request it receives. Every key family points
// at it. The server listens on a local test port only for the websocket route.

const namesClientKey = "names-client-key"

// namesVendor is the stand-in: it answers every request and records its path and model.
type namesVendor struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
}

func newNamesVendor(t *testing.T) *namesVendor {
	t.Helper()
	vendor := &namesVendor{}
	vendor.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		vendor.mu.Lock()
		vendor.requests = append(vendor.requests, r.URL.Path+" "+payload.Model)
		vendor.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"c","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, payload.Model)
	}))
	t.Cleanup(vendor.Close)
	return vendor
}

func (v *namesVendor) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.requests...)
}

func namesDoorConfig(vendor string, on bool) string {
	return strings.ReplaceAll(strings.ReplaceAll(`access:
  api-keys:
    - names-client-key
api-keys:
  openai-compatibility:
    - name: standin
      base-url: VENDOR/v1
      models:
        - name: up-mine
          alias: mine
        - name: up-pool-a
          alias: pool
        - name: held-compat
      keys:
        - api-key: sk-standin
    - name: standin-b
      base-url: VENDOR/b/v1
      models:
        - name: up-pool-b
          alias: pool
      keys:
        - api-key: sk-standin-b
    - name: teamed
      base-url: VENDOR/t/v1
      prefix: team-a
      models:
        - name: up-t
          alias: mine-t
      keys:
        - api-key: sk-teamed
  claude:
    - name: claude-a
      base-url: VENDOR
      models:
        - name: held-claude
      keys:
        - api-key: sk-claude
  gemini:
    - name: gemini-a
      base-url: VENDOR
      models:
        - name: held-gemini
      keys:
        - api-key: sk-gemini
  interactions:
    - name: interactions-a
      base-url: VENDOR
      models:
        - name: held-interactions
      keys:
        - api-key: sk-interactions
  codex:
    - name: codex-a
      base-url: VENDOR
      models:
        - name: held-codex
      keys:
        - api-key: sk-codex
  xai:
    - name: xai-a
      base-url: VENDOR
      models:
        - name: grok-imagine-image
        - name: grok-imagine-video
        - name: grok-tts
      keys:
        - api-key: sk-xai
models:
  only-my-names:
    enabled: SWITCH
`, "VENDOR", vendor), "SWITCH", fmt.Sprint(on))
}

var namesHeld = []string{"held-compat", "held-claude", "held-gemini", "held-interactions", "held-codex", "grok-imagine-image", "grok-imagine-video", "grok-tts"}

var namesServed = []string{"mine", "mine-t", "pool", "team-a/mine-t"}

type namesDoor struct {
	*namesHarness
	vendor *namesVendor
	server *httptest.Server
}

func newNamesDoor(t *testing.T, on bool) *namesDoor {
	t.Helper()
	vendor := newNamesVendor(t)
	h := newNamesHarness(t, namesDoorConfig(vendor.URL, on))
	server := httptest.NewServer(h.handler)
	t.Cleanup(server.Close)
	door := &namesDoor{namesHarness: h, vendor: vendor, server: server}
	if !on {
		deadline := time.Now().Add(10 * time.Second)
		for _, name := range namesHeld {
			for registry.GetGlobalRegistry().GetModelCount(name) == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("off: %s is not registered", name)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		return door
	}
	// Every held-back name is offered by an account (served with the switch off), and none is registered.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ids := map[string]bool{}
		for _, other := range namesOthersOfAll(h) {
			ids[other] = true
		}
		missing := []string{}
		for _, name := range namesHeld {
			if !ids[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("names not offered by any account: %v (others %v)", missing, ids)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, name := range namesHeld {
		if count := registry.GetGlobalRegistry().GetModelCount(name); count != 0 {
			t.Fatalf("%s is registered on %d accounts", name, count)
		}
	}
	return door
}

func namesOthersOfAll(h *namesHarness) []string {
	var ids []string
	for _, auth := range h.service.coreManager.List() {
		ids = append(ids, auth.ID)
	}
	var names []string
	for _, other := range registry.ProvimuxNamesState(ids).Others {
		names = append(names, other.Name)
	}
	return names
}

func (d *namesDoor) do(method, path string, header http.Header, body string) (int, string) {
	d.t.Helper()
	request, err := http.NewRequest(method, d.server.URL+path, strings.NewReader(body))
	if err != nil {
		d.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+namesClientKey)
	request.Header.Set("Content-Type", "application/json")
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := d.server.Client().Do(request)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(data)
}

const namesNotServed = "is not served: this Provimux serves only the names its owner set (models.only-my-names)"

// namesRoute is a route of server_routes.go that takes a model, asked for a held-back name.
type namesRoute struct {
	method, path, body string
	header             http.Header
	// message: the answer carries the setting's message (the route reaches the unknown-model
	// refusal); false: the account-selection gate refuses it ("no auth available", decision 6).
	message bool
}

func namesChat(model string, stream bool) string {
	return fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, model, stream)
}

func namesDoorRoutes() []namesRoute {
	chat := namesChat
	claudeHeader := http.Header{"Anthropic-Version": {"2023-06-01"}}
	return []namesRoute{
		{"POST", "/v1/chat/completions", chat("held-compat", false), nil, true},
		{"POST", "/v1/chat/completions", chat("held-compat", true), nil, true},
		{"POST", "/v1/chat/completions", chat("held-compat(high)", false), nil, true},
		{"POST", "/v1/completions", `{"model":"held-compat","prompt":"hi"}`, nil, true},
		{"POST", "/v1/completions", `{"model":"held-compat","prompt":"hi","stream":true}`, nil, true},
		{"POST", "/v1/images/generations", `{"model":"grok-imagine-image","prompt":"x"}`, nil, true},
		{"POST", "/v1/images/edits", `{"model":"grok-imagine-image","prompt":"x","image":"data:image/png;base64,AA=="}`, nil, true},
		{"POST", "/v1/videos", `{"model":"grok-imagine-video","prompt":"x"}`, nil, true},
		{"POST", "/v1/videos/generations", `{"model":"grok-imagine-video","prompt":"x"}`, nil, true},
		{"POST", "/v1/videos/edits", `{"model":"grok-imagine-video","prompt":"x","video_url":"https://example.invalid/v.mp4"}`, nil, true},
		{"POST", "/v1/videos/extensions", `{"model":"grok-imagine-video","prompt":"x","video_url":"https://example.invalid/v.mp4"}`, nil, true},
		{"POST", "/v1/audio/speech", `{"model":"grok-tts","input":"hi","voice":"eve"}`, nil, true},
		{"POST", "/v1/tts", `{"model":"grok-tts","text":"hi","voice_id":"eve"}`, nil, true},
		{"POST", "/v1/messages", `{"model":"held-claude","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, claudeHeader, true},
		{"POST", "/v1/messages", `{"model":"held-claude","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, claudeHeader, true},
		{"POST", "/v1/messages/count_tokens", `{"model":"held-claude","messages":[{"role":"user","content":"hi"}]}`, claudeHeader, true},
		{"POST", "/v1/responses", `{"model":"held-codex","input":"hi"}`, nil, true},
		{"POST", "/v1/responses", `{"model":"held-codex","input":"hi","stream":true}`, nil, true},
		{"POST", "/v1/responses/compact", `{"model":"held-codex","input":"hi"}`, nil, true},
		{"POST", "/v1/alpha/search", `{"model":"held-codex","id":"s"}`, nil, false},
		{"POST", "/openai/v1/videos", `{"model":"sora-2","prompt":"x"}`, nil, true},
		{"POST", "/backend-api/codex/responses", `{"model":"held-codex","input":"hi"}`, nil, true},
		{"POST", "/backend-api/codex/responses/compact", `{"model":"held-codex","input":"hi"}`, nil, true},
		{"POST", "/backend-api/codex/alpha/search", `{"model":"held-codex","id":"s"}`, nil, false},
		{"POST", "/v1beta/interactions", `{"model":"held-interactions","input":"hi"}`, nil, true},
		{"POST", "/v1beta/models/held-gemini:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, nil, true},
		{"POST", "/v1beta/models/held-gemini:streamGenerateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, nil, true},
		{"POST", "/v1beta/models/held-gemini:countTokens", `{"contents":[{"parts":[{"text":"hi"}]}]}`, nil, true},
	}
}

// D2 and D3: every route of server_routes.go that takes a model, asked for a held-back name, is
// refused, and the stand-in vendor receives nothing. The refusal is HTTP 400, model_not_found, with
// the setting's message in each client format's wrapping; a name no account offers keeps upstream's.
func TestProvimuxOnlyMyNamesDoorRefusesEveryRoute(t *testing.T) {
	d := newNamesDoor(t, true)
	routes := namesDoorRoutes()
	claudeHeader := http.Header{"Anthropic-Version": {"2023-06-01"}}
	chat := namesChat
	var report []string
	for _, r := range routes {
		status, body := d.do(r.method, r.path, r.header, r.body)
		if r.message {
			report = append(report, fmt.Sprintf("%s %s -> %d", r.method, r.path, status))
		} else {
			report = append(report, fmt.Sprintf("%s %s -> %d %s", r.method, r.path, status, strings.TrimSpace(body)))
		}
		if status < 400 && !strings.Contains(body, "error") {
			t.Errorf("%s %s (%s): %d %s", r.method, r.path, r.body, status, body)
		}
		if r.message && !strings.Contains(body, namesNotServed) {
			t.Errorf("%s %s: no setting message: %d %s", r.method, r.path, status, body)
		}
		if r.message && status != http.StatusBadRequest && !strings.Contains(r.body, `"stream":true`) && !strings.Contains(r.path, "stream") {
			t.Errorf("%s %s: status %d, want 400", r.method, r.path, status)
		}
		if seen := d.vendor.seen(); len(seen) != 0 {
			t.Fatalf("%s %s reached the stand-in vendor: %v", r.method, r.path, seen)
		}
	}

	// Routes that name no model but execute with a default one (video retrieval by id uses
	// grok-imagine-video): refused while that name is held back.
	for _, path := range []string{"/v1/videos/vid_names", "/openai/v1/videos/vid_names", "/openai/v1/videos/vid_names/content"} {
		status, body := d.do("GET", path, nil, "")
		report = append(report, fmt.Sprintf("GET %s (no model named) -> %d %s", path, status, strings.TrimSpace(body)))
		if status < 400 {
			t.Errorf("GET %s: %d %s", path, status, body)
		}
		if seen := d.vendor.seen(); len(seen) != 0 {
			t.Fatalf("GET %s reached the stand-in vendor: %v", path, seen)
		}
	}

	// The Responses websocket executes through the same path. In this setup upstream answers a name
	// it cannot route, held back or unknown alike, by closing the socket without an error event; the
	// test requires that nothing reaches the vendor, and the setting's message if an event comes.
	for _, model := range []string{"held-codex", "nobody-offers-this"} {
		wsURL := "ws" + strings.TrimPrefix(d.server.URL, "http") + "/v1/responses"
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + namesClientKey}})
		if err != nil {
			t.Fatalf("websocket: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"`+model+`","input":[{"type":"message","role":"user","content":"hi"}]}`)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, message, errRead := conn.ReadMessage()
		_ = conn.Close()
		switch {
		case errRead == nil && model == "held-codex" && !strings.Contains(string(message), namesNotServed):
			t.Fatalf("websocket event for %s: %s", model, message)
		case errRead == nil && !strings.Contains(string(message), "error"):
			t.Fatalf("websocket answered %s: %s", model, message)
		}
		report = append(report, fmt.Sprintf("GET /v1/responses (websocket, %s) -> %v %s", model, errRead, message))
		if seen := d.vendor.seen(); len(seen) != 0 {
			t.Fatalf("the websocket reached the stand-in vendor: %v", seen)
		}
	}

	// D3: the wrapping of each client format, and a name no account offers.
	status, body := d.do("POST", "/v1/chat/completions", nil, chat("held-compat", false))
	var openAIError struct {
		Error struct{ Message, Type, Code, Param string }
	}
	if err := json.Unmarshal([]byte(body), &openAIError); err != nil || status != 400 ||
		openAIError.Error.Message != "model held-compat "+namesNotServed+". Keep it on the console's Models page, or ask for a served name (GET /v1/models)." ||
		openAIError.Error.Type != "invalid_request_error" || openAIError.Error.Code != "model_not_found" || openAIError.Error.Param != "model" {
		t.Fatalf("OpenAI refusal: %d %s", status, body)
	}
	if status, body := d.do("POST", "/v1/chat/completions", nil, chat("nobody-offers-this", false)); status != 400 || !strings.Contains(body, "unknown provider for model nobody-offers-this") {
		t.Fatalf("an unknown name: %d %s", status, body)
	}
	if status, body := d.do("POST", "/v1/messages", claudeHeader, `{"model":"held-claude","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`); status != 400 || !strings.Contains(body, "model held-claude "+namesNotServed) {
		t.Fatalf("Claude refusal: %d %s", status, body)
	}
	if status, body := d.do("POST", "/v1beta/models/held-gemini:generateContent", nil, `{"contents":[{"parts":[{"text":"hi"}]}]}`); status != 400 || !strings.Contains(body, "model held-gemini "+namesNotServed) {
		t.Fatalf("Gemini refusal: %d %s", status, body)
	}

	// The refusals were counted per name, as the client sent it without a thinking suffix.
	snapshot := registry.ProvimuxNamesState(nil)
	counted := map[string]int{}
	for _, name := range snapshot.Refused.Names {
		counted[name.Name] = name.Requests
	}
	if counted["held-compat"] < 4 || counted["held-claude"] < 3 || counted["nobody-offers-this"] != 0 {
		t.Fatalf("refused counts %v", counted)
	}
	sort.Strings(report)
	t.Logf("routes walked:\n%s", strings.Join(report, "\n"))
}

// The control: the same routes, with the switch off, reach the stand-in vendor, or an account for
// the name (auth_unavailable: the stand-in's one canned answer puts an account in cooldown for the
// routes that follow), so each refusal above is the rule's. Alpha search needs a Codex login, which
// this setup does not have: it is refused by account selection either way.
func TestProvimuxOnlyMyNamesDoorControlSwitchOff(t *testing.T) {
	d := newNamesDoor(t, false)
	var report []string
	for _, r := range namesDoorRoutes() {
		before := len(d.vendor.seen())
		status, body := d.do(r.method, r.path, r.header, r.body)
		reached := len(d.vendor.seen()) > before
		report = append(report, fmt.Sprintf("%s %s -> %d, vendor reached: %v", r.method, r.path, status, reached))
		if strings.Contains(body, namesNotServed) {
			t.Errorf("off: %s %s carries the setting's message", r.method, r.path)
		}
		routed := reached || strings.Contains(body, "auth_unavailable")
		report[len(report)-1] += fmt.Sprintf(", an account was chosen or tried: %v", routed)
		if !routed && r.message {
			t.Errorf("off: %s %s did not reach the vendor: %d %s", r.method, r.path, status, body)
		}
	}
	t.Logf("switch off:\n%s", strings.Join(report, "\n"))
}

// D4: his names work as before: an alias, a pool across two entries, a prefixed name, a name with
// a thinking suffix, and auto.
func TestProvimuxOnlyMyNamesDoorServesHisNames(t *testing.T) {
	d := newNamesDoor(t, true)
	for _, tc := range []struct{ model, want string }{
		{"mine", "/v1/chat/completions up-mine"},
		{"mine(high)", "/v1/chat/completions up-mine"},
		{"team-a/mine-t", "/t/v1/chat/completions up-t"},
		{"mine-t", "/t/v1/chat/completions up-t"},
		{"pool", ""},
		{"auto", ""},
	} {
		before := len(d.vendor.seen())
		status, body := d.do("POST", "/v1/chat/completions", nil, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, tc.model))
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.model, status, body)
		}
		seen := d.vendor.seen()
		if len(seen) != before+1 {
			t.Fatalf("%s: the vendor saw %v", tc.model, seen[before:])
		}
		if tc.want != "" && !strings.HasPrefix(seen[before], tc.want) {
			t.Fatalf("%s: the vendor saw %q, want %q", tc.model, seen[before], tc.want)
		}
	}
	// Both members of the pool stay registered, in the same order of providers.
	if count := registry.GetGlobalRegistry().GetModelCount("pool"); count != 2 {
		t.Fatalf("pool accounts: %d", count)
	}
}

// D1: every list lists exactly his names: /v1/models in the OpenAI and Claude shapes,
// ?include=accounts, /v1/models/<id>, /v1beta/models and /v1beta/models/<id>, the Codex client
// catalog, Grok's, /muse-code/models. No held-back name appears in any of them.
func TestProvimuxOnlyMyNamesDoorListsOnlyHisNames(t *testing.T) {
	d := newNamesDoor(t, true)
	ids := func(body string, field string) []string {
		var list struct {
			Data   []map[string]any `json:"data"`
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("decode %s", body)
		}
		var out []string
		for _, entry := range append(list.Data, list.Models...) {
			if id, ok := entry[field].(string); ok {
				out = append(out, strings.TrimPrefix(id, "models/"))
			}
		}
		sort.Strings(out)
		return out
	}
	noHeld := func(what, body string) {
		t.Helper()
		for _, name := range namesHeld {
			if strings.Contains(body, name) {
				t.Errorf("%s lists %s", what, name)
			}
		}
	}
	status, body := d.do("GET", "/v1/models", nil, "")
	if got := ids(body, "id"); status != 200 || !slices.Equal(got, namesServed) {
		t.Fatalf("/v1/models: %d %v", status, got)
	}
	// The Claude shape writes Claude-like ids of upstream's own; the names are the display names.
	status, body = d.do("GET", "/v1/models", http.Header{"Anthropic-Version": {"2023-06-01"}}, "")
	noHeld("/v1/models (Claude)", body)
	if got := slices.Compact(ids(body, "display_name")); status != 200 || !slices.Equal(got, []string{"mine", "mine-t", "pool"}) {
		t.Fatalf("/v1/models (Claude): %d %v %s", status, got, body)
	}
	status, body = d.do("GET", "/v1/models?include=accounts", nil, "")
	if got := ids(body, "id"); status != 200 || !slices.Equal(got, namesServed) {
		t.Fatalf("/v1/models?include=accounts: %d %v", status, got)
	}
	if status, _ := d.do("GET", "/v1/models/held-compat", nil, ""); status != http.StatusNotFound {
		t.Fatalf("/v1/models/held-compat: %d", status)
	}
	if status, _ := d.do("GET", "/v1/models/mine", nil, ""); status != http.StatusOK {
		t.Fatalf("/v1/models/mine: %d", status)
	}
	status, body = d.do("GET", "/v1beta/models", nil, "")
	noHeld("/v1beta/models", body)
	if status != 200 || !strings.Contains(body, "mine") {
		t.Fatalf("/v1beta/models: %d %s", status, body)
	}
	if status, _ := d.do("GET", "/v1beta/models/held-gemini", nil, ""); status != http.StatusNotFound {
		t.Fatalf("/v1beta/models/held-gemini: %d", status)
	}
	for _, probe := range []struct {
		what, path string
		header     http.Header
	}{
		{"Codex client catalog", "/v1/models?client_version=0.130.0", nil},
		{"Grok's list", "/v1/models", http.Header{"User-Agent": {"grok-shell/1.0"}}},
		{"Muse Code's list", "/muse-code/models", nil},
	} {
		status, body := d.do("GET", probe.path, probe.header, "")
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", probe.what, status, body)
		}
		noHeld(probe.what, body)
	}
	if len(d.vendor.seen()) != 0 {
		t.Fatalf("listing reached the vendor: %v", d.vendor.seen())
	}
}
