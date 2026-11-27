package cliproxy

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the other paths of answered-by.md 8.4 on the wire, and the guard that a new
// executor upstream fails a test instead of naming an entry nobody read. Every key, login and id is invented.

// sentExecutorCoverage names every executor constructor of internal/runtime/executor: the executor key of
// sentMatrix's cases that put it on the wire, or why its answers name no entry.
var sentExecutorCoverage = map[string]string{
	"NewAIStudioExecutor":           "none: a JSON message on the socket a browser opened (internal/wsrelay); no http.RoundTripper sees it",
	"NewAntigravityExecutor":        "antigravity",
	"NewClaudeExecutor":             "claude",
	"NewCodexExecutor":              "codex",
	"NewCodexAutoExecutor":          "codex",
	"NewCodexWebsocketsExecutor":    "none: the upstream WebSocket, a websocket.Dialer no decorator sees; TestProvimuxAnswerEntryOnTheResponsesWebsocket",
	"NewCopilotExecutor":            "copilot",
	"NewDevinExecutor":              "devin",
	"NewGeminiExecutor":             "gemini",
	"NewGeminiInteractionsExecutor": "gemini-interactions",
	"NewGeminiVertexExecutor":       "vertex",
	"NewKimiExecutor":               "kimi",
	"NewMetaExecutor":               "meta",
	"NewOpenAICompatExecutor":       "openai-compatibility",
	"NewXAIExecutor":                "xai",
	"NewXAIAutoExecutor":            "xai",
	"NewXAIWebsocketsExecutor":      "none: the upstream WebSocket, a websocket.Dialer no decorator sees",
}

// Every executor upstream has is on the wire in the matrix or named as one whose answers have no entry. An executor
// upstream adds fails here, so a sync sees it: until a reader or a row is written for it, its answers name no entry.
func TestProvimuxAnswerEntryEveryExecutorIsCovered(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "runtime", "executor")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no executor sources under %s: %v", dir, err)
	}
	inMatrix := map[string]bool{}
	for _, tc := range append(sentMatrix(), sentInteractionsCase) {
		inMatrix[tc.executor] = true
	}
	found := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, errParse := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
		if errParse != nil {
			t.Fatal(errParse)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "New") || !strings.HasSuffix(fn.Name.Name, "Executor") {
				continue
			}
			found[fn.Name.Name] = true
			covered, known := sentExecutorCoverage[fn.Name.Name]
			switch {
			case !known:
				t.Errorf("%s (%s): an executor nobody wrote a reader or a row for; put it on the wire in sentMatrix or name why it has none", fn.Name.Name, filepath.Base(file))
			case !strings.HasPrefix(covered, "none:") && !inMatrix[covered]:
				t.Errorf("%s: no case of sentMatrix sends through %q", fn.Name.Name, covered)
			}
		}
	}
	var gone []string
	for name := range sentExecutorCoverage {
		if !found[name] {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	if len(gone) > 0 {
		t.Errorf("upstream no longer has %v: take them out of sentExecutorCoverage", gone)
	}
}

// The Gemini Interactions executor: a key's direct Interactions request, read from its body; the map has no row.
var sentInteractionsCase = sentMatrixCase{"gemini-interactions", "a Gemini Interactions key: none", "", "provimux-test-interactions",
	"/v1beta/interactions", `{"model":"provimux-test-interactions","input":"provimux-test question"}`, "generativelanguage.googleapis.com", ""}

func TestProvimuxAnswerEntryGeminiInteractions(t *testing.T) {
	h := newSentHarness(t, `api-keys:
  interactions:
    - name: provimux-test-interactions
      models:
        - name: provimux-test-interactions-sent
          alias: provimux-test-interactions
      keys:
        - api-key: provimux-test-interactions-key
`)
	tc := sentInteractionsCase
	h.stand.reset()
	sent := h.checkSent(h.post(tc.path, tc.body, nil), sentKeyFor(t, h, tc.model), tc.host, tc.entry)
	if sent.Model == tc.model {
		t.Fatalf("fixture: the alias was sent as asked")
	}
}

// Paths whose host or credential the request decides: a base URL in a login's file and in its metadata, Codex's second
// sign-in for a Codex client and for another, a redirect to another host, a zstd body.
func TestProvimuxAnswerEntryTheRequestDecides(t *testing.T) {
	h := newSentHarness(t, `upstream:
  codex:
    upstream-request-compression: true
    upstream-request-compression-min-bytes: 1
`)
	far := sentFar()
	login := func(provider, file string, attributes map[string]string, metadata map[string]any, model string) *coreauth.Auth {
		metadata["type"] = provider
		attrs := map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/" + file}
		for k, v := range attributes {
			attrs[k] = v
		}
		return h.register(&coreauth.Auth{ID: file, FileName: file, Provider: provider, Status: coreauth.StatusActive, Attributes: attrs, Metadata: metadata}, model)
	}
	metaRelay := login("meta", "provimux-test-meta-relay.json", nil, map[string]any{"api_key": "provimux-test-meta-token", "base_url": "https://provimux-test-relay.invalid/v1"}, "provimux-test-meta-relay")
	codexOpenAI := login("codex", "provimux-test-codex-openai.json", map[string]string{"base_url": "https://api.openai.com/v1"},
		map[string]any{"access_token": "provimux-test-first", "account_id": "provimux-test-ws", "expired": far}, "provimux-test-codex-openai")
	codexBound := login("codex", "provimux-test-codex-bound.json", nil, map[string]any{"access_token": "provimux-test-first", "account_id": "provimux-test-ws", "expired": far,
		"use_siwc": true, "siwc": map[string]any{"access_token": "provimux-test-second", "expired": far}}, "provimux-test-codex-bound")
	codexPlain := login("codex", "provimux-test-codex-plain.json", nil, map[string]any{"access_token": "provimux-test-plain", "account_id": "provimux-test-ws", "expired": far}, "provimux-test-codex-plain")
	xaiKey := h.register(&coreauth.Auth{ID: "provimux-test-xai-redirect", FileName: "provimux-test-xai-redirect.json", Provider: "xai", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "apikey", "api_key": "provimux-test-xai-key", "base_url": "https://api.x.ai/v1"}, Metadata: map[string]any{"using_api": true}}, "provimux-test-xai-redirect")

	for _, tc := range []struct {
		name      string
		account   *coreauth.Auth
		model     string
		userAgent string
		host      string
		entry     string
		status    func(*http.Request, int) (int, http.Header)
		check     func(t *testing.T, got sentReceived)
	}{
		{name: "a Meta login whose metadata names a relay", account: metaRelay, model: "provimux-test-meta-relay", host: "provimux-test-relay.invalid"},
		{name: "a Codex login whose base URL is api.openai.com", account: codexOpenAI, model: "provimux-test-codex-openai", host: "api.openai.com"},
		{name: "a Codex login with the second sign-in, another client", account: codexBound, model: "provimux-test-codex-bound", userAgent: "provimux-test-agent/1.0", host: "api.openai.com"},
		{name: "a Codex login with the second sign-in, a Codex client", account: codexBound, model: "provimux-test-codex-bound", userAgent: "codex_cli_rs/0.0.0", host: "chatgpt.com",
			entry: "openai-codex/provimux-test-codex-bound"},
		{name: "a zstd body to ChatGPT's backend", account: codexPlain, model: "provimux-test-codex-plain", host: "chatgpt.com", entry: "openai-codex/provimux-test-codex-plain"},
		{name: "a redirect to another host: that host's row, here none", account: xaiKey, model: "provimux-test-xai-redirect", host: "provimux-test-relay.invalid",
			status: func(r *http.Request, n int) (int, http.Header) {
				if r.URL.Hostname() == "api.x.ai" {
					return http.StatusTemporaryRedirect, http.Header{"Location": {"https://provimux-test-relay.invalid" + r.URL.Path}}
				}
				return 0, nil
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.t = t
			h.stand.reset()
			h.stand.status = tc.status
			var header http.Header
			if tc.userAgent != "" {
				header = sentHeader("User-Agent", tc.userAgent)
			}
			got := h.checkSent(h.post("/v1/responses", `{"model":"`+tc.model+`","input":"provimux-test question","stream":false}`, header), tc.account, tc.host, tc.entry)
			if tc.status != nil {
				all := h.stand.all()
				if len(all) < 2 || all[0].Status != http.StatusTemporaryRedirect || all[0].Host != "api.x.ai" {
					t.Fatalf("fixture: no redirect from api.x.ai first: %+v", all)
				}
			}
			t.Logf("sent to %s: %s", got.Host, got.Model)
		})
	}
	// The zstd case is one only where the body went compressed.
	h.t = t
	h.stand.reset()
	compressed := false
	h.stand.status = func(r *http.Request, _ int) (int, http.Header) {
		compressed = compressed || strings.EqualFold(r.Header.Get("Content-Encoding"), "zstd")
		return 0, nil
	}
	h.checkSent(h.post("/v1/responses", `{"model":"provimux-test-codex-plain","input":"provimux-test question","stream":false}`, nil), codexPlain, "chatgpt.com", "openai-codex/provimux-test-codex-plain")
	if !compressed {
		t.Fatal("fixture: the request to ChatGPT's backend was not zstd")
	}
}

// A client on the Responses WebSocket, with two Codex logins. One without websockets (the default): the gateway sends
// the turn to the vendor over HTTP, and response.created carries the account, the model and the entry. One with
// websockets: true: the gateway sends the turn upstream over a WebSocket, which no transport sees, and response.created
// carries the account and no entry.
func TestProvimuxAnswerEntryOnTheResponsesWebsocket(t *testing.T) {
	// The gateway's logs away from the harness's directory: a WebSocket session being torn down after the test may
	// still capture a request body there, which would race the directory's removal. Removed last, whatever is left.
	logs, errLogs := os.MkdirTemp("", "provimux-test-ws-logs-")
	if errLogs != nil {
		t.Fatal(errLogs)
	}
	t.Cleanup(func() { _ = os.RemoveAll(logs) })
	t.Setenv("WRITABLE_PATH", logs)
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock()) // a record of its own: another test may have stopped the gateway's
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var upstreamMu sync.Mutex
	var upstreamModels []string
	received := func() []string {
		upstreamMu.Lock()
		defer upstreamMu.Unlock()
		return append([]string(nil), upstreamModels...)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, message, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			model := ojson.GetBytes(message, "model").String()
			upstreamMu.Lock()
			upstreamModels = append(upstreamModels, model)
			upstreamMu.Unlock()
			for _, event := range []string{
				fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_provimux_ws","object":"response","created_at":1,"status":"in_progress","model":%q,"output":[]}}`, model),
				fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_provimux_ws","object":"response","created_at":1,"status":"completed","model":%q,"output":[{"id":"msg_ws","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"provimux-test-answer","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, model),
			} {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(upstream.Close)
	far := sentFar()
	overSocket := h.register(&coreauth.Auth{ID: "provimux-test-codex-ws.json", FileName: "provimux-test-codex-ws.json", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "websockets": "true", "base_url": upstream.URL + "/backend-api/codex", "path": "/provimux-test/auths/provimux-test-codex-ws.json"},
		Metadata:   map[string]any{"type": "codex", "access_token": "provimux-test-ws-token", "account_id": "provimux-test-ws", "expired": far}}, "provimux-test-codex-ws")
	overHTTP := h.register(&coreauth.Auth{ID: "provimux-test-codex-http.json", FileName: "provimux-test-codex-http.json", Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/provimux-test-codex-http.json"},
		Metadata:   map[string]any{"type": "codex", "access_token": "provimux-test-http-token", "account_id": "provimux-test-http", "expired": far}}, "provimux-test-codex-http")

	turn := func(model string) map[string]string {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + sentClientKey}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"`+model+`","input":[{"type":"message","role":"user","content":"provimux-test question"}]}`)); errWrite != nil {
			t.Fatal(errWrite)
		}
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		for {
			_, message, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("turn %s: %v (stand-in received %+v)", model, errRead, h.stand.all())
			}
			if ojson.GetBytes(message, "type").String() == "response.created" {
				fields := map[string]string{}
				ojson.ParseBytes(message).ForEach(func(key, value ojson.Result) bool {
					if strings.HasPrefix(key.String(), "provimux_") || strings.HasPrefix(key.String(), "cpa_auth_") {
						fields[key.String()] = value.String()
					}
					return true
				})
				return fields
			}
			if ojson.GetBytes(message, "type").String() == "error" {
				t.Fatalf("turn %s: %s", model, message)
			}
		}
	}

	h.stand.reset()
	fields := turn("provimux-test-codex-ws")
	if got := received(); len(got) != 1 || got[0] != "provimux-test-codex-ws" {
		t.Fatalf("fixture: the upstream WebSocket received %v", got)
	}
	if fields[logging.ProvimuxAccountField] != overSocket.ProvimuxAccountMarker() || fields["cpa_auth_kind"] != "oauth" {
		t.Fatalf("the turn over the upstream WebSocket names %v, want the account %s", fields, overSocket.ProvimuxAccountMarker())
	}
	for _, name := range logging.ProvimuxFieldNames()[1:] {
		if value, ok := fields[name.Field]; ok {
			t.Fatalf("the turn over the upstream WebSocket carries %s=%q (stand-in received %+v)", name.Field, value, h.stand.all())
		}
	}

	h.stand.reset()
	fields = turn("provimux-test-codex-http")
	answered := h.stand.answered()
	if len(answered) != 1 || answered[0].Host != "chatgpt.com" {
		t.Fatalf("fixture: the HTTP turn reached %+v", h.stand.all())
	}
	want := map[string]string{logging.ProvimuxAccountField: overHTTP.ProvimuxAccountMarker(), logging.ProvimuxModelField: answered[0].Model,
		logging.ProvimuxModelBasisField: "not-confirmed", logging.ProvimuxModelSentField: answered[0].Model, logging.ProvimuxEntryField: "openai-codex/" + answered[0].Model}
	for name, value := range want {
		if fields[name] != value {
			t.Fatalf("the turn over HTTP: %s=%q, want %q (all %v)", name, fields[name], value, fields)
		}
	}
	if _, ok := fields[logging.ProvimuxEffortField]; ok {
		t.Fatalf("the turn over HTTP carries an effort: %v", fields)
	}
	// A WebSocket turn reaches the record with the name it asked for: the upstream WebSocket's answer as one that
	// named no entry, the HTTP one with its entry.
	// The record is written after the frame is: wait for the note, never for a fixed time.
	overSocketItems := func() []coreauth.ProvimuxAnsweredItem {
		return coreauth.ProvimuxAnswered(coreauth.ProvimuxClientRef(sentClientKey), overSocket.ID, overSocket.ProvimuxAccountMarker(), "provimux-test-codex-ws")
	}
	overHTTPItems := func() []coreauth.ProvimuxAnsweredItem {
		return coreauth.ProvimuxAnswered(coreauth.ProvimuxClientRef(sentClientKey), overHTTP.ID, overHTTP.ProvimuxAccountMarker(), "provimux-test-codex-http")
	}
	waitAnswered(t, func() bool { return len(overSocketItems()) > 0 && len(overHTTPItems()) > 0 })
	if items := overSocketItems(); len(items) == 0 || items[0].Entry != "" {
		t.Fatalf("the record of the turn over the upstream WebSocket: %+v", items)
	}
	if items := overHTTPItems(); len(items) == 0 || items[0].Entry != "openai-codex/"+answered[0].Model {
		t.Fatalf("the record of the turn over HTTP: %+v", items)
	}
}

// The entry of one sent request, by the credential: made or changed by a plugin, an xAI key marked as not the paid
// API, a URL that is not https on its default port.
func TestProvimuxEntryOfAnswerVouchesForTheCredential(t *testing.T) {
	sent := cliproxyexecutor.ProvimuxSentRequest{Scheme: "https", Host: "api.x.ai", Model: "provimux-test-grok", Signer: "authorization", Status: 200}
	key := func(attributes map[string]string, metadata map[string]any) *coreauth.Auth {
		attrs := map[string]string{"auth_kind": "apikey", "api_key": "provimux-test-key", "source": "config:xai[provimux-test]"}
		for k, v := range attributes {
			attrs[k] = v
		}
		return &coreauth.Auth{ID: "provimux-test-xai", Provider: "xai", Attributes: attrs, Metadata: metadata}
	}
	for _, tc := range []struct {
		name string
		auth *coreauth.Auth
		sent cliproxyexecutor.ProvimuxSentRequest
		want string
	}{
		{"a key the gateway holds", key(nil, nil), sent, "xai/provimux-test-grok"},
		{"using_api true in its metadata", key(nil, map[string]any{"using_api": true}), sent, "xai/provimux-test-grok"},
		{"using_api false in its metadata", key(nil, map[string]any{"using_api": false}), sent, ""},
		{"using_api false in its attributes", key(map[string]string{"using_api": "false"}, nil), sent, ""},
		{"using_api not a boolean", key(nil, map[string]any{"using_api": "provimux-test"}), sent, ""},
		{"a plugin's virtual credential", key(map[string]string{coreauth.AttributePluginVirtual: "true"}, nil), sent, ""},
		{"marked as a plugin's change", key(nil, map[string]any{"provimux_plugin_credential": true}), sent, ""},
		{"http", key(nil, nil), cliproxyexecutor.ProvimuxSentRequest{Scheme: "http", Host: "api.x.ai", Model: "provimux-test-grok", Status: 200}, ""},
		{"another port", key(nil, nil), cliproxyexecutor.ProvimuxSentRequest{Scheme: "https", Host: "api.x.ai", Port: "8443", Model: "provimux-test-grok", Status: 200}, ""},
		{"no model", key(nil, nil), cliproxyexecutor.ProvimuxSentRequest{Scheme: "https", Host: "api.x.ai", Status: 200}, ""},
		{"no credential", nil, sent, ""},
	} {
		if got := provimuxEntryOfAnswer(tc.auth, tc.sent); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The decorator changes no request: the matrix's model requests reach the stand-in with the same bytes with
// models.answered-by off and on, for every case whose bytes are the same from one request to the next.
func TestProvimuxAnswerEntryRequestsAreTheSameOnAndOff(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	sentLogins(t, h)
	var compared, skipped []string
	for _, tc := range sentMatrix() {
		if tc.login == "copilot" {
			continue
		}
		capture := func(on bool) []string {
			logging.SetProvimuxAnsweredBy(on)
			h.stand.reset()
			var bodies []string
			// The model requests only: a token exchange is made when a cached token ran out, not per request.
			h.stand.status = func(r *http.Request, _ int) (int, http.Header) {
				if request := standInBytes(r); standInModel(t, r, standInBody(r)) != "" {
					bodies = append(bodies, request)
				}
				return 0, nil
			}
			if answer := h.post(tc.path, tc.body, nil); answer.status != http.StatusOK {
				t.Fatalf("%s: %d %s", tc.name, answer.status, answer.body)
			}
			return bodies
		}
		off1, off2, on := capture(false), capture(false), capture(true)
		if strings.Join(off1, "\n") != strings.Join(off2, "\n") {
			skipped = append(skipped, tc.name)
			continue
		}
		if strings.Join(off1, "\n") != strings.Join(on, "\n") {
			t.Errorf("%s: the requests differ with the decorator on:\noff %s\non  %s", tc.name, off1, on)
		}
		compared = append(compared, tc.name)
	}
	logging.SetProvimuxAnsweredBy(true)
	t.Logf("compared byte for byte: %d cases; not the same from one request to the next with the switch off, so not compared: %v", len(compared), skipped)
	if len(compared) < len(sentMatrix())/2 {
		t.Fatalf("fixture: only %d cases compared", len(compared))
	}
}

// standInBody is a request's body, read through GetBody.
func standInBody(r *http.Request) []byte {
	if r.GetBody == nil {
		return nil
	}
	body, err := r.GetBody()
	if err != nil {
		return nil
	}
	defer func() { _ = body.Close() }()
	raw, _ := io.ReadAll(body)
	return raw
}

// standInBytes is a request as bytes the stand-in can compare: method, URL, headers in order, body (read through
// GetBody, the transport's own body left to the stand-in).
func standInBytes(r *http.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", r.Method, r.URL.String())
	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(r.Header[name], ", "))
	}
	if r.GetBody != nil {
		if body, err := r.GetBody(); err == nil {
			raw, _ := io.ReadAll(body)
			_ = body.Close()
			b.Write(raw)
		}
	}
	return b.String()
}

// Devin: every request id the executor makes for a model of Devin's catalog and an effort resolves to that model's
// entry, or to none; never to another model's (answered-by.md 11).
func TestProvimuxAnswerEntryDevinRequestIDsOverTheCatalog(t *testing.T) {
	models := registry.GetDevinModels()
	if len(models) == 0 {
		t.Fatal("fixture: Devin's catalog is empty")
	}
	var unresolved []string
	checked := 0
	for _, model := range models {
		id := strings.TrimPrefix(strings.ToLower(model.ID), "devin/")
		levels := []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max", "auto"}
		if model.Thinking != nil {
			levels = append(levels, model.Thinking.Levels...)
		}
		for _, level := range levels {
			uid := helps.ResolveDevinChatModelUID(model.ID, level, 0)
			checked++
			switch got := modellists.EntryModel("devin", uid); {
			case got == "":
				unresolved = append(unresolved, model.ID+"@"+level+"="+uid)
			case !strings.EqualFold(got, id):
				t.Errorf("%s at effort %q is sent as %s, whose entry is devin/%s: another model's", model.ID, level, uid, got)
			}
		}
	}
	t.Logf("%d request ids over %d models; %d have no entry: %v", checked, len(models), len(unresolved), unresolved)
}

// The second try on the same account after an overload refusal (routing.retry.same-account-retry): the refused first
// try answered no 2xx, so it is no candidate, and the answer of the second names its account and its entry.
func TestProvimuxAnswerEntryAfterASecondTry(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			h := newSentHarness(t, answeredClaudeConfig+"routing:\n  retry:\n    same-account-retry: true\n")
			t.Cleanup(coreauth.ProvimuxSetRetryClockForTest(func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, nil))
			key := sentKeyFor(t, h, "provimux-test-claude-key")
			h.stand.mu.Lock()
			h.stand.refusal = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
			h.stand.status = func(_ *http.Request, n int) (int, http.Header) {
				if n == 0 {
					return 529, nil
				}
				return 0, nil
			}
			h.stand.mu.Unlock()
			answer := h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", stream, ""), nil)
			if received := h.stand.all(); len(received) != 2 || received[0].Status != 529 || received[1].Status != http.StatusOK {
				t.Fatalf("fixture: the stand-in received %+v", received)
			}
			h.checkSent(answer, key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
		})
	}
}
