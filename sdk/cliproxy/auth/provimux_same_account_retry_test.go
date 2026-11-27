package auth

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type provimuxRequestScoped struct{ error }

func (provimuxRequestScoped) IsRequestScoped() bool { return true }

// The table of ProvimuxPassingFault, both sides of each line.
func TestProvimuxPassingFaultTable(t *testing.T) {
	status := func(code int, body string) error { return &Error{Message: body, HTTPStatus: code} }
	const (
		overloaded = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
		apiError   = `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`
		rateLimit  = `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`
		quota      = `{"error":{"code":503,"message":"You exceeded your current quota","status":"UNAVAILABLE"}}`
		gemini     = `{"error":{"code":503,"message":"The model is overloaded. Please try again later.","status":"UNAVAILABLE"}}`
		codex      = `{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are busy"}}`
		credit     = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`
		plain      = `<html>bad gateway</html>`
	)
	cases := []struct {
		name string
		err  error
		kind string // "" = never
	}{
		{"529 overloaded_error", status(529, overloaded), provimuxFaultOverloaded},
		{"529 without a body", status(529, ""), provimuxFaultOverloaded},
		{"502 overloaded_error (the report)", status(502, overloaded), provimuxFaultOverloaded},
		{"500 overloaded_error: the body decides", status(500, overloaded), provimuxFaultOverloaded},
		{"429 overloaded_error: the body decides", status(429, overloaded), provimuxFaultOverloaded},
		{"503 Gemini overloaded", status(503, gemini), provimuxFaultOverloaded},
		{"503 Codex server_is_overloaded", status(503, codex), provimuxFaultOverloaded},
		{"overloaded in plain text", status(502, "claude executor: upstream returned error event: Overloaded"), provimuxFaultOverloaded},
		{"502 plain", status(502, plain), provimuxFaultUnavailable},
		{"503", status(503, apiError), provimuxFaultUnavailable},
		{"504", status(504, plain), provimuxFaultUnavailable},
		{"connection refused", &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, provimuxFaultConnection},
		{"connection reset", &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}, provimuxFaultConnection},
		{"closed before any byte (EOF)", &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: io.EOF}, provimuxFaultConnection},
		{"dial timeout", &url.Error{Op: "Post", URL: "http://127.0.0.1:1", Err: &net.OpError{Op: "dial", Net: "tcp", Err: provimuxTimeoutErr{}}}, provimuxFaultConnection},

		{"500 api_error", status(500, apiError), ""},
		{"408", status(408, plain), ""},
		{"429 rate limit", status(429, rateLimit), ""},
		{"429 without a body", status(429, ""), ""},
		{"503 stating a quota", status(503, quota), ""},
		{"502 overloaded but stating a rate limit", status(502, `{"error":{"type":"overloaded_error","message":"rate limit"}}`), ""},
		{"400", status(400, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: field required"}}`), ""},
		{"400 credit", status(400, credit), ""},
		{"401", status(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`), ""},
		{"402", status(402, `{"error":{"message":"payment required"}}`), ""},
		{"403", status(403, `{"type":"error","error":{"type":"permission_error","message":"forbidden"}}`), ""},
		{"404", status(404, `{"type":"error","error":{"type":"not_found_error","message":"model"}}`), ""},
		{"content refusal (400)", status(400, `{"error":{"message":"Output blocked by content filtering policy"}}`), ""},
		{"client went away", context.Canceled, ""},
		{"client deadline", fmt.Errorf("post: %w", context.DeadlineExceeded), ""},
		{"request-scoped 503", provimuxRequestScoped{status(503, apiError)}, ""},
		{"lost upstream WebSocket turn", &cliproxyexecutor.UpstreamWebsocketTurnError{Cause: io.EOF}, ""},
		{"whole-credential quota", &Error{Code: credentialQuotaErrorCode, Message: "quota", HTTPStatus: 429}, ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		kind, ok := ProvimuxPassingFault(c.err)
		if kind != c.kind || ok != (c.kind != "") {
			t.Errorf("%s: (%q, %v), want %q", c.name, kind, ok, c.kind)
		}
	}
}

type provimuxTimeoutErr struct{}

func (provimuxTimeoutErr) Error() string   { return "i/o timeout" }
func (provimuxTimeoutErr) Timeout() bool   { return true }
func (provimuxTimeoutErr) Temporary() bool { return true }

func TestProvimuxParseRetryAfter(t *testing.T) {
	now := time.Unix(1_000_000, 0) // date-safe: a fixed instant the parser is given; no date is compared with today's
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", 0, false},
		{"2", 2 * time.Second, true},
		{"0.5", 500 * time.Millisecond, true},
		{"0", 0, true},
		{"-3", 0, true},
		{now.Add(2 * time.Second).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), 2 * time.Second, true},
		{"soon", 0, false},
	}
	for _, c := range cases {
		got, ok := provimuxParseRetryAfter(c.raw, now)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

// What may be sent twice: not an image, video, speech or Gemini interaction job, nor a request forcing the image
// tool.
func TestProvimuxRepeatable(t *testing.T) {
	for format, want := range map[string]bool{"claude": true, "openai": true, "openai-response": true, "gemini": true,
		"codex-alpha-search": true, "openai-image": false, "openai-video": false, "openai-speech": false, "interactions": false} {
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(format)}
		if got := provimuxRepeatable(opts); got != want {
			t.Errorf("%s: %v, want %v", format, got, want)
		}
	}
	forced := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: []byte(`{"tool_choice":{"type":"image_generation"}}`)}
	if provimuxRepeatable(forced) {
		t.Error("a request forcing image_generation is repeatable")
	}
	chat := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)}
	if !provimuxRepeatable(chat) {
		t.Error("a conversation that merely offers the image tool is not repeatable")
	}
}
