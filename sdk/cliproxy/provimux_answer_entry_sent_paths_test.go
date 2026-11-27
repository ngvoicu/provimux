package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProvimuxEntryMediaAndCompaction(t *testing.T) {
	cfg := strings.Replace(sentMatrixConfig, "        - name: provimux-test-codex-sent", "        - name: deepseek-v4.1-flash\n          is-compat: true\n        - name: provimux-test-codex-sent", 1)
	h := newSentHarness(t, cfg)
	useAnsweredBook(t, newSentClock())
	_ = sentLogins(t, h)
	h.register(&ca.Auth{ID: "media-xai", FileName: "media-xai.json", Provider: "xai", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "base_url": "https://api.x.ai/v1", "api_key": "media-key"}}, "grok-imagine-image", "grok-imagine-video", "grok-tts", "grok-voice-tts-1.0")
	h.register(&ca.Auth{ID: "media-codex", FileName: "media-codex.json", Provider: "codex", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "path": "/fixture/media-codex.json"}, Metadata: map[string]any{"type": "codex", "account_id": "fixture-codex", "access_token": "fixture-codex", "expired": sentFar()}}, "gpt-image-2", "gpt-image-1.5")
	type seen struct{ url, path, model, content string }
	var got []seen
	var captures []reviewFrozenRequest
	reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
		var b []byte
		if r.Body != nil {
			b, _ = io.ReadAll(r.Body)
			r.Body.Close()
		}
		captures = append(captures, reviewFrozenRequest{r.Clone(context.Background()), bytes.Clone(b)})
		m := ojson.GetBytes(b, "model").String()
		got = append(got, seen{r.URL.Host, r.URL.Path, m, string(b)})
		switch {
		case strings.Contains(r.URL.Path, "/images/") || strings.Contains(r.URL.Path, "/imagegen/"):
			return reviewOK(r, `{"created":1,"data":[{"b64_json":"AA==","url":"https://fixture.invalid/img"}]}`, "application/json"), nil
		case strings.Contains(r.URL.Path, "/videos/"):
			return reviewOK(r, `{"request_id":"vid-fixture","status":"pending"}`, "application/json"), nil
		case strings.HasSuffix(r.URL.Path, "/tts"):
			return reviewOK(r, "fixture audio", "audio/mpeg"), nil
		case strings.Contains(string(b), `"image_generation"`) && strings.Contains(r.URL.Path, "/responses"):
			return reviewOK(r, "event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"resp_fixture","created_at":1,"status":"completed","output":[{"type":"image_generation_call","id":"ig_fixture","status":"completed","result":"AA=="}]}}`+"\n\n", "text/event-stream"), nil
		}
		return standInAnswer(r, b, m), nil
	}))
	for _, tc := range []struct{ name, path, model, extra, provider string }{
		{"xai-image", "/v1/images/generations", "grok-imagine-image", `"prompt":"fixture"`, "xai"},
		{"xai-video", "/v1/videos/generations", "grok-imagine-video", `"prompt":"fixture"`, "xai"},
		{"xai-speech", "/v1/audio/speech", "grok-tts", `"input":"fixture"`, "xai"},
		{"codex-image", "/v1/images/generations", "gpt-image-2", `"prompt":"fixture"`, "openai-codex"},
		{"codex-direct", "/v1/images/generations", "gpt-image-1.5", `"prompt":"fixture"`, "openai-codex"},
		{"kimi-claude", "/v1/messages", "provimux-test-kimi", `"max_tokens":20,"messages":[{"role":"user","content":"fixture"}]`, ""},
		{"codex-reentry", "/v1/responses", "deepseek-v4.1-flash", `"input":[{"type":"message","role":"user","content":"fixture history"},{"type":"compaction_trigger"}]`, "openai"},
		{"compat-compact", "/v1/responses/compact", "provimux-test-router", `"input":[{"type":"message","role":"user","content":"fixture history"}]`, "openrouter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			captures = nil
			a := h.post(tc.path, fmt.Sprintf(`{"model":%q,%s}`, tc.model, tc.extra), nil)
			if a.status != 200 || len(got) == 0 {
				t.Fatalf("fixture %d %s requests %+v", a.status, a.body, got)
			}
			for _, request := range captures {
				request.checkSwitch(t)
			}
			last := got[len(got)-1]
			want := ""
			if last.model != "" && tc.provider != "" {
				want = tc.provider + "/" + strings.TrimSuffix(last.model, ":nitro")
			}
			if tc.name == "codex-direct" && last.url == "api.openai.com" {
				want = ""
			}
			if tc.name == "codex-reentry" && (strings.Contains(last.content, `"compaction_trigger"`) || !strings.Contains(a.body, "compaction")) {
				t.Fatalf("summary reentry fixture: final body %s, response %s", last.content, a.body)
			}
			t.Logf("HTTP %d, vendor %s%s model=%q; answer model=%q entry=%q", a.status, last.url, last.path, last.model, a.get(logging.ProvimuxModelSentHeader), a.get(logging.ProvimuxEntryHeader))
			if a.get(logging.ProvimuxAccountHeader) == "" || a.get(logging.ProvimuxEntryHeader) != want || a.get(logging.ProvimuxModelSentHeader) != last.model {
				t.Fatalf("want named account, sent %q, entry %q; headers %v", last.model, want, a.header)
			}
		})
	}
}
func TestProvimuxEntryFailover(t *testing.T) {
	h := newSentHarness(t, `api-keys:
  claude:
    - name: first-claude
      models:
        - name: first-model
          alias: review-public
      keys:
        - api-key: fixture-first
    - name: second-claude
      models:
        - name: second-model
          alias: review-public
      keys:
        - api-key: fixture-second
`)
	useAnsweredBook(t, newSentClock())
	h.service.coreManager.SetRetryConfig(0, 0, 3)
	reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
		res, e := h.stand.RoundTrip(r)
		if e == nil && res.StatusCode == 503 {
			res.Body.Close()
			res.Body = io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"fixture upstream unavailable"}}`))
		}
		return res, e
	}))
	h.stand.status = func(r *http.Request, n int) (int, http.Header) {
		if n == 0 {
			return 503, nil
		}
		return 0, nil
	}
	a := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	r := h.stand.all()
	if a.status != 200 || len(r) != 2 || r[0].Model == r[1].Model || r[0].Status != 503 || r[1].Status != 200 {
		t.Fatalf("fixture %d %s %#v", a.status, a.body, r)
	}
	dest := map[string]string{"api.anthropic.com": "anthropic"}
	want := dest[r[1].Host] + "/" + r[1].Model
	t.Logf("two accounts: %v, entry=%q", r, a.get(logging.ProvimuxEntryHeader))
	if a.get(logging.ProvimuxEntryHeader) != want {
		t.Fatal("wrong failover destination")
	}
	for _, acc := range h.service.coreManager.List() {
		cl, mg, _ := h.listsRow(sentClientKey, "review-public", acc.ProvimuxAccountMarker())
		if cl == nil || mg == nil {
			t.Fatal("missing account row")
		}
		if acc.ProvimuxAccountMarker() != a.get(logging.ProvimuxAccountHeader) && (len(cl.items()) != 0 || len(mg.items()) != 0) {
			t.Error("the refused account got a note")
		}
	}
}
func TestProvimuxEntryRefresh(t *testing.T) {
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock())
	a := h.register(&ca.Auth{ID: "refresh-fixture", FileName: "refresh-fixture.json", Provider: "meta", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "path": filepath.Join(t.TempDir(), "refresh-fixture.json")}, Metadata: map[string]any{"type": "meta", "access_token": "before", "api_key": "fixture-key", "refresh_token": "fixture-refresh", "email": "refresh@fixture.invalid", "expired": sentFar()}}, "review-refresh")
	orig, _ := h.service.coreManager.Executor(a.Provider)
	var seen []string
	rt := reviewRT(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		m := ojson.GetBytes(b, "model").String()
		seen = append(seen, m)
		res := reviewOK(r, reviewChat, "application/json")
		if m == "refused" {
			res.StatusCode = 401
		}
		return res, nil
	})
	h.service.coreManager.RegisterExecutor(reviewExec{ProviderExecutor: orig, run: func(c context.Context, a *ca.Auth, _ ex.Request, _ ex.Options) (ex.Response, error) {
		m := "refused"
		if a.Metadata["access_token"] == "after" {
			m = "actual"
		}
		res, e := reviewSend(c, rt, "https://api.meta.ai/v1/chat/completions", m)
		if e != nil {
			return ex.Response{}, e
		}
		res.Body.Close()
		ex.MarkUpstreamAttempt(c)
		if res.StatusCode == 401 {
			return ex.Response{}, &ca.Error{HTTPStatus: 401, Message: "fixture unauthorized"}
		}
		return ex.Response{Payload: []byte(reviewChat)}, nil
	}, refresh: func(c context.Context, a *ca.Auth) (*ca.Auth, error) {
		res, e := reviewSend(c, rt, "https://api.meta.ai/auth/token", "refresh-side")
		if e != nil {
			return nil, e
		}
		res.Body.Close()
		b := a.Clone()
		b.Metadata["access_token"] = "after"
		return b, nil
	}})
	ans := h.post("/v1/chat/completions", chatBody("review-refresh", false, ""), nil)
	t.Logf("refresh receipts %v; status %d entry=%q", seen, ans.status, ans.get(logging.ProvimuxEntryHeader))
	if ans.status != 200 || strings.Join(seen, " ") != "refused refresh-side actual" {
		t.Fatalf("fixture %s", ans.body)
	}
	// This executor is registered through the SDK, so its answer
	// names no entry at all; that a refresh's requests stay off the answering attempt is pinned with a built-in
	// executor in the subtest below.
	if ans.get(logging.ProvimuxEntryHeader) != "" {
		t.Fatal("an executor registered through the SDK named an entry")
	}

	t.Run("native-antigravity-token-near-expiry", func(t *testing.T) {
		h := newSentHarness(t, "")
		useAnsweredBook(t, newSentClock())
		const id = "review-antigravity-refresh"
		nearExpiry := time.Now().UTC().Add(4 * time.Minute).Format(time.RFC3339)
		h.register(&ca.Auth{ID: "review-expired-ag", FileName: "review-expired-ag.json", Provider: "antigravity", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "path": filepath.Join(t.TempDir(), "review-expired-ag.json")}, Metadata: map[string]any{
			"type": "antigravity", "access_token": "expired-fixture", "refresh_token": "renew-fixture", "project_id": "fixture-project", "email": "fixture@fixture.invalid", "expiry": nearExpiry, "expired": nearExpiry,
		}}, id)
		h.stand.reset()
		answer := h.post("/v1/chat/completions", chatBody(id, false, ""), nil)
		receipts := h.stand.all()
		if answer.status != 200 {
			t.Fatalf("native near-expiry fixture HTTP %d receipts=%v, body=%s", answer.status, receipts, answer.body)
		}
		exchanged := false
		var model string
		for _, r := range receipts {
			if r.Host == "oauth2.googleapis.com" && r.Path == "/token" && r.Status == 200 {
				exchanged = true
			}
			if exchanged && strings.Contains(r.Path, ":generateContent") && r.Status == 200 {
				model = r.Model
			}
		}
		t.Logf("native in-execution refresh receipts=%v entry=%q", receipts, answer.get(logging.ProvimuxEntryHeader))
		if !exchanged || model == "" {
			t.Fatal("missing token and subsequent model receipt")
		}
		if answer.get(logging.ProvimuxEntryHeader) != "antigravity/"+model {
			t.Error("inline refresh supplied the entry")
		}
	})
}
func TestProvimuxEntryZstdRetry(t *testing.T) {
	h := newSentHarness(t, `upstream:
  codex:
    upstream-request-compression: true
    upstream-request-compression-min-bytes: 1
`)
	useAnsweredBook(t, newSentClock())
	h.register(&ca.Auth{ID: "retry-codex", FileName: "retry-codex.json", Provider: "codex", Status: ca.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "path": "/fixture/retry-codex.json"}, Metadata: map[string]any{"type": "codex", "access_token": "fixture-access", "account_id": "fixture-account", "expired": sentFar()}}, "review-zstd")
	var enc, models []string
	reviewSet(h, reviewRT(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		m := standInModel(t, r, b)
		enc = append(enc, r.Header.Get("Content-Encoding"))
		models = append(models, m)
		if len(enc) == 1 {
			res := reviewOK(r, `{"error":{"message":"unsupported content encoding: zstd"}}`, "application/json")
			res.StatusCode = 415
			return res, nil
		}
		return standInAnswer(r, b, m), nil
	}))
	a := h.post("/v1/responses", `{"model":"review-zstd","input":"fixture"}`, nil)
	t.Logf("status %d, encodings=%q models=%q entry=%q", a.status, enc, models, a.get(logging.ProvimuxEntryHeader))
	if a.status != 200 || len(enc) != 2 || enc[0] != "zstd" || enc[1] != "" || a.get(logging.ProvimuxEntryHeader) != "openai-codex/"+models[1] {
		t.Fatalf("fixture or attribution: %s", a.body)
	}
}
func TestProvimuxEntryClaudeSigner(t *testing.T) {
	// The bytes reported by the receiving HTTP transport are decisive, regardless of kind.
	for _, both := range []bool{false, true} {
		cfg := answeredClaudeConfig
		if both {
			cfg = strings.Replace(cfg, "      models:", "      headers:\n        Authorization: Bearer second-fixture-signer\n      models:", 1)
		}
		h := newSentHarness(t, cfg)
		useAnsweredBook(t, newSentClock())
		r := h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil)
		seen := h.stand.answered()
		if r.status != 200 || len(seen) != 1 {
			t.Fatal(r.status, seen)
		}
		want := "anthropic/" + seen[0].Model
		if both {
			if seen[0].Signer != "both" {
				t.Fatal("fixture signer", seen)
			}
			want = ""
		}
		if r.get(logging.ProvimuxEntryHeader) != want {
			t.Error("signer mismatch", seen, r.header)
		}
	}
}

var _ = bytes.Clone

// reviewFrozenRequest is a request as an executor built it, sent once with models.answered-by off and once on.
type reviewFrozenRequest struct {
	request *http.Request
	body    []byte
}

func (s reviewFrozenRequest) checkSwitch(t *testing.T) {
	t.Helper()
	saved := logging.ProvimuxAnsweredBy()
	defer logging.SetProvimuxAnsweredBy(saved)
	var encoded [2][]byte
	for i, enable := range []bool{false, true} {
		logging.SetProvimuxAnsweredBy(enable)
		c := ex.WithUpstreamAttemptTracker(context.Background())
		r := s.request.Clone(c)
		open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(s.body)), nil }
		r.Body, _ = open()
		r.GetBody = open
		rec := reviewRT(func(req *http.Request) (*http.Response, error) {
			var b bytes.Buffer
			if err := req.Write(&b); err != nil {
				return nil, err
			}
			encoded[i] = b.Bytes()
			return reviewOK(req, "{}", "application/json"), nil
		})
		resp, err := httptransport.Decorate(c, &http.Client{Transport: rec}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if len(encoded[0]) == 0 || !bytes.Equal(encoded[0], encoded[1]) {
		t.Fatal("outgoing request differs at the receiving transport")
	}
}
