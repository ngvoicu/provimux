// Package provimux holds PMX-06 checks: setting the Provimux version must not
// change anything AI providers receive. These tests live outside the
// provider-facing paths guarded by provimux-guard and drive the real executors.
package provimux

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	baseVersion     = "8.0.77"
	provimuxVersion = "8.0.77-pmx.1"
)

// withVersions sets buildinfo for the duration of fn and restores it afterwards.
func withVersions(t *testing.T, version, pmx string, fn func() http.Header) http.Header {
	t.Helper()
	prevVersion, prevProvimux := buildinfo.Version, buildinfo.ProvimuxVersion
	buildinfo.Version, buildinfo.ProvimuxVersion = version, pmx
	defer func() { buildinfo.Version, buildinfo.ProvimuxVersion = prevVersion, prevProvimux }()
	return fn()
}

// isolateHome keeps executors from reading the developer's real home directory
// (Kimi reads its device id from there).
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
}

// capture starts a server that records the headers of the first request and answers with body.
func capture(t *testing.T, body string) (*httptest.Server, func() http.Header) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if seen == nil {
			seen = r.Header.Clone()
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

func assertSameHeaders(t *testing.T, stock, pmx http.Header, mustEqual map[string]string) {
	t.Helper()
	if stock == nil || pmx == nil {
		t.Fatalf("no request captured (stock=%v, pmx=%v)", stock != nil, pmx != nil)
	}
	for name, want := range mustEqual {
		if got := pmx.Get(name); got != want {
			t.Errorf("%s with ProvimuxVersion set = %q, want %q", name, got, want)
		}
		if got := stock.Get(name); got != want {
			t.Errorf("%s with ProvimuxVersion unset = %q, want %q", name, got, want)
		}
	}
	if !reflect.DeepEqual(stock, pmx) {
		t.Errorf("headers differ when ProvimuxVersion is set:\n unset: %v\n   set: %v", stock, pmx)
	}
	for name, values := range pmx {
		for _, v := range values {
			if strings.Contains(v, "pmx") || strings.Contains(strings.ToLower(v), "provimux") {
				t.Errorf("header %s = %q leaks the Provimux version", name, v)
			}
		}
	}
}

const openAIChatResponse = `{"id":"c","object":"chat.completion","created":1,"model":"kimi-k2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// PMX-06a: Kimi chat requests send User-Agent and X-Msh-Version from the CPA base version only.
func TestProvimuxVersionDoesNotChangeKimiExecutorHeaders(t *testing.T) {
	isolateHome(t)
	run := func() http.Header {
		server, seen := capture(t, openAIChatResponse)
		auth := &cliproxyauth.Auth{ID: "kimi-headers", Provider: "kimi", Attributes: map[string]string{
			"api_key":  "kimi-test-key",
			"base_url": server.URL,
		}}
		_, err := runtimeexecutor.NewKimiExecutor(&config.Config{}).Execute(context.Background(), auth,
			cliproxyexecutor.Request{Model: "kimi-k2", Payload: []byte(`{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}]}`)},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
		if err != nil {
			t.Fatalf("Kimi Execute() error = %v", err)
		}
		return seen()
	}
	stock := withVersions(t, baseVersion, "dev", run)
	pmx := withVersions(t, baseVersion, provimuxVersion, run)
	assertSameHeaders(t, stock, pmx, map[string]string{
		"User-Agent":    "CLIProxyAPI/" + baseVersion,
		"X-Msh-Version": baseVersion,
	})
}

const claudeMessageResponse = `{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`

// PMX-06a: the Claude caller-owned fallback User-Agent uses the CPA base version only.
func TestProvimuxVersionDoesNotChangeClaudeFallbackUserAgent(t *testing.T) {
	isolateHome(t)
	run := func() http.Header {
		server, seen := capture(t, claudeMessageResponse)
		auth := &cliproxyauth.Auth{ID: "claude-fallback", Attributes: map[string]string{
			"api_key":  "claude-test-key",
			"base_url": server.URL,
		}}
		_, err := runtimeexecutor.NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth,
			cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		if err != nil {
			t.Fatalf("Claude Execute() error = %v", err)
		}
		return seen()
	}
	stock := withVersions(t, baseVersion, "dev", run)
	pmx := withVersions(t, baseVersion, provimuxVersion, run)
	assertSameHeaders(t, stock, pmx, map[string]string{
		"User-Agent": "CLIProxyAPI/" + baseVersion,
	})
}

type headerRecorder struct {
	mu   sync.Mutex
	seen http.Header
}

func (r *headerRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	if r.seen == nil {
		r.seen = req.Header.Clone()
	}
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"device_code":"d","user_code":"u","verification_uri":"https://example.invalid","verification_uri_complete":"https://example.invalid","expires_in":600,"interval":5}`)),
		Request:    req,
	}, nil
}

// PMX-06a: the Kimi device login sends X-Msh-Version from the CPA base version only.
func TestProvimuxVersionDoesNotChangeKimiDeviceLoginHeaders(t *testing.T) {
	isolateHome(t)
	run := func() http.Header {
		rec := &headerRecorder{}
		client := kimi.NewDeviceFlowClientWithDeviceID(&config.Config{}, "test-device")
		client.SetHTTPClient(&http.Client{Transport: rec})
		if _, err := client.RequestDeviceCode(context.Background()); err != nil {
			t.Fatalf("RequestDeviceCode() error = %v", err)
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.seen
	}
	stock := withVersions(t, baseVersion, "dev", run)
	pmx := withVersions(t, baseVersion, provimuxVersion, run)
	assertSameHeaders(t, stock, pmx, map[string]string{
		"X-Msh-Version": baseVersion,
	})
}
