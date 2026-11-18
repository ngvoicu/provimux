package management

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func setProvimuxBuildInfo(t *testing.T, version, provimuxVersion string) {
	t.Helper()
	prevVersion, prevProvimux := buildinfo.Version, buildinfo.ProvimuxVersion
	buildinfo.Version, buildinfo.ProvimuxVersion = version, provimuxVersion
	t.Cleanup(func() { buildinfo.Version, buildinfo.ProvimuxVersion = prevVersion, prevProvimux })
}

func managementVersionHeader(t *testing.T) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	router := gin.New()
	router.GET("/v8/management/config", h.Middleware(), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer test-secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Header().Get("X-CPA-VERSION")
}

// PMX-06b: X-CPA-VERSION carries the Provimux version when one is baked in.
func TestProvimuxManagementVersionHeaderCarriesProvimuxVersion(t *testing.T) {
	setProvimuxBuildInfo(t, "8.0.77", "8.0.77-pmx.1")
	if got := managementVersionHeader(t); got != "8.0.77-pmx.1" {
		t.Fatalf("X-CPA-VERSION = %q, want %q", got, "8.0.77-pmx.1")
	}
}

// PMX-06b: without a Provimux version, X-CPA-VERSION keeps upstream's value.
func TestProvimuxManagementVersionHeaderFallsBackToUpstream(t *testing.T) {
	for _, provimuxVersion := range []string{"dev", "", "  "} {
		setProvimuxBuildInfo(t, "8.0.77", provimuxVersion)
		if got := managementVersionHeader(t); got != "8.0.77" {
			t.Fatalf("ProvimuxVersion=%q: X-CPA-VERSION = %q, want upstream %q", provimuxVersion, got, "8.0.77")
		}
	}
}

type recordingRoundTripper struct {
	mu       sync.Mutex
	requests []*http.Request
	body     string
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.requests = append(rt.requests, req.Clone(req.Context()))
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Request:    req,
	}, nil
}

// PMX-06c: the latest-version check asks the Provimux repository and reports its answer.
func TestProvimuxLatestVersionQueriesProvimuxRepo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := &recordingRoundTripper{body: `{"tag_name":"v8.0.77-pmx.2","name":"v8.0.77-pmx.2"}`}
	prevTransport := http.DefaultTransport
	http.DefaultTransport = rt
	t.Cleanup(func() { http.DefaultTransport = prevTransport })

	h := &Handler{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/latest-version", nil)
	h.GetLatestVersion(c)

	if len(rt.requests) != 1 {
		t.Fatalf("outbound requests = %d, want 1", len(rt.requests))
	}
	got := rt.requests[0]
	const wantURL = "https://api.github.com/repos/ngvoicu/provimux/releases/latest"
	if got.URL.String() != wantURL {
		t.Fatalf("release check URL = %q, want %q", got.URL.String(), wantURL)
	}
	if ua := got.Header.Get("User-Agent"); ua != "CLIProxyAPI" {
		t.Fatalf("release check User-Agent = %q, want upstream's %q", ua, "CLIProxyAPI")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if payload["latest-version"] != "v8.0.77-pmx.2" {
		t.Fatalf("latest-version = %q, want %q", payload["latest-version"], "v8.0.77-pmx.2")
	}
}
