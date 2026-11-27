package auth

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The table of ProvimuxOverloadRefusal: the allow-list per executor, both sides of each line, and the limit codes that
// win. No word of a message decides anything.
func TestProvimuxOverloadRefusalTable(t *testing.T) {
	const (
		anthropicOverloaded = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
		anthropicCapacity   = `{"type":"error","error":{"type":"overloaded_error","message":"Insufficient server capacity. Try again shortly."}}`
		anthropicAPIError   = `{"type":"error","error":{"type":"api_error","message":"Overloaded"}}`
		anthropicRateLimit  = `{"type":"error","error":{"type":"rate_limit_error","message":"overloaded"}}`
		anthropicBilling    = `{"type":"error","error":{"type":"billing_error","message":"overloaded"}}`
		openAIOverloaded    = `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"The requested model is temporarily overloaded."}}`
		openAICodeOnly      = `{"error":{"code":"server_is_overloaded","message":"busy"}}`
		openAISpend         = `{"error":{"type":"service_unavailable_error","code":"organization_spend_limit_exceeded","message":"You reached the monthly spend limit."}}`
		openAIQuotaType     = `{"error":{"type":"insufficient_quota","code":"server_is_overloaded"}}`
		geminiUnavailable   = `{"error":{"code":503,"message":"The model is overloaded. Please try again later.","status":"UNAVAILABLE"}}`
		geminiExhausted     = `{"error":{"code":503,"message":"overloaded","status":"RESOURCE_EXHAUSTED"}}`
		kimiOverloaded      = `{"error":{"type":"engine_overloaded_error","message":"The engine is currently overloaded, please try again later"}}`
		plain               = `<html><body>502 Bad Gateway</body></html>`
		wordsOnly           = `{"error":{"message":"unknown model overloaded-model"}}`
	)
	cases := []struct {
		name     string
		provider string
		status   int
		body     string
		want     bool
	}{
		{"Anthropic 529 overloaded_error", "claude", 529, anthropicOverloaded, true},
		{"Anthropic 502 overloaded_error (the owner's case)", "claude", 502, anthropicOverloaded, true},
		{"Anthropic 503 overloaded_error", "claude", 503, anthropicOverloaded, true},
		{"Anthropic 529 overloaded_error, any message", "claude", 529, anthropicCapacity, true},
		{"Anthropic 500 overloaded_error: the error status carries it", "claude", 500, anthropicOverloaded, true},
		{"Anthropic 429 overloaded_error: never a 429", "claude", 429, anthropicOverloaded, false},
		{"Anthropic 200 overloaded_error: never a non-error status", "claude", 200, anthropicOverloaded, false},
		{"Anthropic api_error saying Overloaded", "claude", 529, anthropicAPIError, false},
		{"Anthropic rate_limit_error", "claude", 529, anthropicRateLimit, false},
		{"Anthropic billing_error", "claude", 503, anthropicBilling, false},
		{"Anthropic error without the envelope type", "claude", 529, `{"error":{"type":"overloaded_error"}}`, false},
		{"Anthropic plain 502", "claude", 502, plain, false},
		{"Anthropic plain 503", "claude", 503, "Service Unavailable", false},
		{"Anthropic words only", "claude", 400, wordsOnly, false},
		{"OpenAI 503 server_is_overloaded", "codex", 503, openAIOverloaded, true},
		{"OpenAI code without the documented type", "codex", 503, openAICodeOnly, false},
		{"OpenAI spend limit under 503", "codex", 503, openAISpend, false},
		{"OpenAI quota type with the overload code", "codex", 503, openAIQuotaType, false},
		{"OpenAI 429 server_is_overloaded", "codex", 429, openAIOverloaded, false},
		{"OpenAI plain 504", "codex", 504, plain, false},
		{"Anthropic body to a Codex account", "codex", 529, anthropicOverloaded, false},
		{"OpenAI body to a Claude account", "claude", 503, openAIOverloaded, false},
		{"Gemini 503 UNAVAILABLE: no entry", "gemini", 503, geminiUnavailable, false},
		{"Gemini 503 RESOURCE_EXHAUSTED", "gemini", 503, geminiExhausted, false},
		{"Vertex: no entry", "vertex", 503, geminiUnavailable, false},
		{"Kimi engine_overloaded_error: no entry", "kimi", 503, kimiOverloaded, false},
		{"OpenAI-compatible: no entry", "openai-compatible-openrouter", 503, openAIOverloaded, false},
		{"xAI: no entry", "xai", 503, openAIOverloaded, false},
		{"Meta: no entry", "meta", 503, openAIOverloaded, false},
		{"Devin: no entry", "devin", 503, anthropicOverloaded, false},
		{"Copilot: no entry", "github-copilot", 503, openAIOverloaded, false},
		{"Antigravity: no entry", "antigravity", 503, geminiUnavailable, false},
		{"truncated JSON", "claude", 529, anthropicOverloaded[:40], false},
	}
	for _, c := range cases {
		if got := ProvimuxOverloadRefusal(c.provider, c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProvimuxDecodeKeptBody(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	var gz, br, zs bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write(body)
	_ = gw.Close()
	bw := brotli.NewWriter(&br)
	_, _ = bw.Write(body)
	_ = bw.Close()
	zw, _ := zstd.NewWriter(&zs)
	_, _ = zw.Write(body)
	_ = zw.Close()
	for name, c := range map[string]struct {
		data     []byte
		encoding string
	}{
		"identity": {body, ""}, "gzip": {gz.Bytes(), "gzip"}, "gzip without the header": {gz.Bytes(), ""},
		"br": {br.Bytes(), "br"}, "zstd": {zs.Bytes(), "zstd"}, "zstd without the header": {zs.Bytes(), ""},
	} {
		got, ok := provimuxDecode(c.data, c.encoding)
		if !ok || !bytes.Equal(got, body) {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
	if _, ok := provimuxDecode(gz.Bytes()[:10], "gzip"); ok {
		t.Error("a cut gzip body decoded")
	}
	if _, ok := provimuxDecode(body, "compress"); ok {
		t.Error("an unknown encoding decoded")
	}
}

func TestProvimuxParseRetryAfter(t *testing.T) {
	now := time.Unix(1_000_000, 0) // date-safe: a fixed instant the parser is given; no date is compared with today's
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"2", 2 * time.Second, true},
		{"0", 0, true},
		{"0.5", 0, false},
		{"-3", 0, false},
		{"9999999999", 0, false},
		{now.Add(2 * time.Second).UTC().Format(http.TimeFormat), 2 * time.Second, true},
		{now.Add(-time.Minute).UTC().Format(http.TimeFormat), 0, true},
		{"soon", 0, false},
	}
	for _, c := range cases {
		got, ok := provimuxParseRetryAfter(c.raw, now)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

// What may be sent twice at all: not an image, video, speech or Gemini interaction job, nor a request forcing the
// image tool.
func TestProvimuxRepeatable(t *testing.T) {
	for format, want := range map[string]bool{"claude": true, "openai": true, "openai-response": true, "gemini": true,
		"openai-image": false, "openai-video": false, "openai-speech": false, "interactions": false} {
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(format)}
		if got := provimuxRepeatable(opts); got != want {
			t.Errorf("%s: %v, want %v", format, got, want)
		}
	}
	forced := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: []byte(`{"tool_choice":{"type":"image_generation"}}`)}
	if provimuxRepeatable(forced) {
		t.Error("a request forcing image_generation is repeatable")
	}
}

type provimuxTestKeys map[string]any

func (k provimuxTestKeys) Get(key string) (any, bool) { v, ok := k[key]; return v, ok }
func (k provimuxTestKeys) Set(key string, value any)  { k[key] = value }

// One second try per client call: on the client request's keys (gin), else on the call's own map, else none.
func TestProvimuxSecondTryOncePerCall(t *testing.T) {
	keys := provimuxTestKeys{}
	ctx := withRequestRefusedAuths(context.WithValue(context.Background(), "gin", keys)) //nolint:staticcheck // the handlers' key
	if !provimuxSecondTryAvailable(ctx) || !provimuxTakeSecondTry(ctx) || provimuxTakeSecondTry(ctx) || provimuxSecondTryAvailable(ctx) {
		t.Fatal("the client request's second try was not taken exactly once")
	}
	// Upstream's own retries of the same client request (another manager call, a new map) do not get another.
	if provimuxTakeSecondTry(withRequestRefusedAuths(context.WithValue(context.Background(), "gin", keys))) { //nolint:staticcheck
		t.Fatal("a second manager call of the same client request got a second try")
	}
	call := withRequestRefusedAuths(context.Background())
	if !provimuxTakeSecondTry(call) || provimuxTakeSecondTry(call) {
		t.Fatal("an SDK call's second try was not taken exactly once")
	}
	if provimuxSecondTryAvailable(context.Background()) {
		t.Fatal("a call with neither has a second try")
	}
}
