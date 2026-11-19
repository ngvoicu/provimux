package managementasset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	provimuxUIReleaseURL = "https://api.github.com/repos/ngvoicu/provimux-ui/releases/latest"
	customReleaseURL     = "https://api.github.com/repos/someone/my-panel/releases/latest"
	fixtureAssetURL      = "https://github.com/fixture/releases/download/v1/management.html"
	fixtureSumsURL       = "https://github.com/fixture/releases/download/v1/checksums.txt"
	fallbackSumsURL      = "https://github.com/ngvoicu/provimux-ui/releases/latest/download/checksums.txt"
	fallbackPageURL      = "https://github.com/ngvoicu/provimux-ui/releases/latest/download/management.html"
	priorPage            = "previous console\n"
	nextPage             = "current console\n"
)

func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

type fakeResponse struct {
	status int
	body   string
}

// releaseJSON is a releases/latest body with management.html (and checksums.txt when sumsAsset).
func releaseJSON(digest string, size int, sumsAsset bool) string {
	assets := fmt.Sprintf(`{"name":"management.html","browser_download_url":%q,"digest":%q,"size":%d}`, fixtureAssetURL, digest, size)
	if sumsAsset {
		assets += fmt.Sprintf(`,{"name":"checksums.txt","browser_download_url":%q}`, fixtureSumsURL)
	}
	return `{"assets":[` + assets + `]}`
}

// runEnsure runs EnsureLatestManagementHTML against a fake GitHub (every other URL is 404)
// and returns whether the file exists, its contents and the requested URLs.
func runEnsure(t *testing.T, repo string, seed string, routes map[string]fakeResponse) (bool, string, []string) {
	t.Helper()
	for _, key := range []string{"GITHUB_TOKEN", "github_token", "GITSTORE_GIT_TOKEN", "GITSTORE_GIT_URL", "MANAGEMENT_STATIC_PATH"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	localPath := filepath.Join(dir, managementAssetName)
	if seed != "" {
		if err := os.WriteFile(localPath, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var requested []string
	oldTransport := http.DefaultTransport
	lastUpdateCheckMu.Lock()
	lastUpdateCheckTime = time.Time{}
	lastUpdateCheckMu.Unlock()
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		lastUpdateCheckMu.Lock()
		lastUpdateCheckTime = time.Time{}
		lastUpdateCheckMu.Unlock()
	})
	http.DefaultTransport = githubAssetTransport(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		requested = append(requested, req.URL.String())
		mu.Unlock()
		resp, ok := routes[req.URL.String()]
		if !ok {
			resp = fakeResponse{status: http.StatusNotFound}
		}
		return &http.Response{StatusCode: resp.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(resp.body)), Request: req}, nil
	})

	exists := EnsureLatestManagementHTML(t.Context(), dir, "", repo)
	data, err := os.ReadFile(localPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if exists != (err == nil) {
		t.Fatalf("EnsureLatestManagementHTML() = %t, file exists = %t", exists, err == nil)
	}
	mu.Lock()
	defer mu.Unlock()
	return exists, string(data), slices.Clone(requested)
}

func assertNeverOriginal(t *testing.T, requested []string) {
	t.Helper()
	for _, u := range requested {
		lower := strings.ToLower(u)
		if strings.Contains(lower, "router-for-me") || strings.Contains(lower, "router-for.me") || strings.Contains(lower, "1051566067") {
			t.Fatalf("requested the original console: %s (all: %v)", u, requested)
		}
	}
}

func verifiedRelease(page string) map[string]fakeResponse {
	return map[string]fakeResponse{
		provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(page), len(page), false)},
		fixtureAssetURL:      {http.StatusOK, page},
	}
}

// Every spelling of the original Management Center, and of provimux-ui, downloads provimux-ui.
// Old code (ba2b4f4a) fails "original by numeric id" (it requested
// /repositories/1051566067/releases/latest and installed that release), ".GIT",
// "/Releases/Latest", trailing "///", and "provimux-ui mixed case".
func TestEnsureOriginalRepositoryGetsProvimuxUI(t *testing.T) {
	for _, repo := range []string{
		"", // default config
		config.DefaultPanelGitHubRepository,
		"https://github.com/router-for-me/Cli-Proxy-API-Management-Center",
		"https://github.com/router-for-me/Cli-Proxy-API-Management-Center/",
		"https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git",
		"https://github.com/router-for-me/Cli-Proxy-API-Management-Center.GIT/",
		"HTTPS://GitHub.COM:443/Router-For-Me/cli-proxy-api-management-center",
		"http://github.com/router-for-me/Cli-Proxy-API-Management-Center/releases",
		"https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest",
		"https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/Releases/Latest",
		"https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest///",
		"https://api.github.com/repositories/1051566067/releases/latest",
		"https://API.github.com/repositories/1051566067",
		"https://github.com/NGVOICU/Provimux-UI.git",
		"https://api.github.com/repositories/1463889407/releases/latest",
	} {
		t.Run(repo, func(t *testing.T) {
			exists, data, requested := runEnsure(t, repo, "", verifiedRelease(nextPage))
			if !exists || data != nextPage {
				t.Fatalf("file = %t %q, want provimux-ui's page (requests %v)", exists, data, requested)
			}
			if requested[0] != provimuxUIReleaseURL {
				t.Fatalf("first request %s, want %s", requested[0], provimuxUIReleaseURL)
			}
			assertNeverOriginal(t, requested)
		})
	}
}

// Another repository is respected in every equivalent spelling. Old code fails ":443"
// (it resolved to the default).
func TestEnsureCustomRepositoryKept(t *testing.T) {
	for _, repo := range []string{
		"https://github.com/someone/my-panel",
		"https://github.com:443/someone/my-panel",
		"https://github.com/someone/my-panel.git/",
		"https://api.github.com/repos/someone/my-panel",
		"https://api.github.com/repos/someone/my-panel/releases/latest//",
	} {
		t.Run(repo, func(t *testing.T) {
			routes := map[string]fakeResponse{
				customReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(nextPage), len(nextPage), false)},
				fixtureAssetURL:  {http.StatusOK, nextPage},
			}
			exists, data, requested := runEnsure(t, repo, "", routes)
			if !exists || data != nextPage || requested[0] != customReleaseURL {
				t.Fatalf("file = %t %q, requests %v", exists, data, requested)
			}
		})
	}
}

// An existing file is never replaced without a valid sha256 that the download matches.
// Old code fails "no digest, 204" (replaced the file with 0 bytes), "no digest, 200"
// (installed unverified), "bad checksums.txt" (installed) and "206 full body" (installed).
func TestEnsureNeverReplacesWithoutVerification(t *testing.T) {
	cases := []struct {
		name   string
		routes map[string]fakeResponse
		want   string
	}{
		{"no digest, 204", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", 0, false)},
			fixtureAssetURL:      {http.StatusNoContent, ""},
		}, priorPage},
		{"no digest, 200", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", 0, false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"malformed digest", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:abc", 0, false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"bad checksums.txt", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", 0, true)},
			fixtureSumsURL:       {http.StatusOK, sha256Hex("other") + "  management.html\n"},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"206 full body", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(nextPage), len(nextPage), false)},
			fixtureAssetURL:      {http.StatusPartialContent, nextPage},
		}, priorPage},
		{"truncated body", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(nextPage), len(nextPage), false)},
			fixtureAssetURL:      {http.StatusOK, nextPage[:4]},
		}, priorPage},
		{"size mismatch", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(nextPage), len(nextPage)+1, false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"verified by digest", verifiedRelease(nextPage), nextPage},
		{"verified by the release's checksums.txt", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", 0, true)},
			fixtureSumsURL:       {http.StatusOK, sha256Hex(nextPage) + "  management.html\n"},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, nextPage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exists, data, requested := runEnsure(t, "", priorPage, tc.routes)
			if !exists || data != tc.want {
				t.Fatalf("file = %t %q, want %q (requests %v)", exists, data, tc.want, requested)
			}
		})
	}
}

// A fresh install never gets an unverified or partial page. Old code fails both
// (it installed 4 bytes and 0 bytes).
func TestEnsureFreshInstallNeedsVerification(t *testing.T) {
	for name, routes := range map[string]map[string]fakeResponse{
		"no digest, 206 partial": {
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", 0, false)},
			fixtureAssetURL:      {http.StatusPartialContent, nextPage[:4]},
		},
		"no digest, 204": {
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", 0, false)},
			fixtureAssetURL:      {http.StatusNoContent, ""},
		},
	} {
		t.Run(name, func(t *testing.T) {
			exists, data, requested := runEnsure(t, "", "", routes)
			if exists {
				t.Fatalf("installed %q (requests %v)", data, requested)
			}
			if slices.Contains(requested, fallbackPageURL) {
				t.Fatalf("an unverifiable release must not fall back: %v", requested)
			}
		})
	}
}

// When the API lookup or the download fails on a fresh install of the default console,
// the verified fallback installs provimux-ui. Old code fails "mixed-case provimux-ui"
// (no fallback); the suite fails on any code without the fallback calls.
func TestEnsureFallback(t *testing.T) {
	sums := fakeResponse{http.StatusOK, sha256Hex(nextPage) + "  management.html\n"}
	page := fakeResponse{http.StatusOK, nextPage}
	cases := []struct {
		name       string
		repo       string
		seed       string
		routes     map[string]fakeResponse
		want       string
		wantExists bool
		wantURLs   []string
	}{
		{"rate limited", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
			fallbackSumsURL:      sums, fallbackPageURL: page,
		}, nextPage, true, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"rate limited, original by id", "https://api.github.com/repositories/1051566067/releases/latest", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusTooManyRequests, ""},
			fallbackSumsURL:      sums, fallbackPageURL: page,
		}, nextPage, true, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"rate limited, mixed-case provimux-ui", "https://github.com/NGVOICU/Provimux-UI", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      sums, fallbackPageURL: page,
		}, nextPage, true, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"download fails", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(nextPage), len(nextPage), false)},
			fixtureAssetURL:      {http.StatusServiceUnavailable, ""},
			fallbackSumsURL:      sums, fallbackPageURL: page,
		}, nextPage, true, []string{provimuxUIReleaseURL, fixtureAssetURL, fallbackSumsURL, fallbackPageURL}},
		{"fallback checksum mismatch", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      {http.StatusOK, sha256Hex(priorPage) + "  management.html\n"}, fallbackPageURL: page,
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"fallback 206", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      sums, fallbackPageURL: {http.StatusPartialContent, nextPage},
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"fallback without a sum", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      {http.StatusOK, ""}, fallbackPageURL: page,
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL}},
		{"existing file kept", "", priorPage, map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      sums, fallbackPageURL: page,
		}, priorPage, true, []string{provimuxUIReleaseURL}},
		{"custom repository has none", "https://github.com/someone/my-panel", "", map[string]fakeResponse{
			customReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:  sums, fallbackPageURL: page,
		}, "", false, []string{customReleaseURL}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exists, data, requested := runEnsure(t, tc.repo, tc.seed, tc.routes)
			if exists != tc.wantExists || data != tc.want {
				t.Fatalf("file = %t %q, want %t %q (requests %v)", exists, data, tc.wantExists, tc.want, requested)
			}
			if !slices.Equal(requested, tc.wantURLs) {
				t.Fatalf("requests %v, want %v", requested, tc.wantURLs)
			}
			assertNeverOriginal(t, requested)
		})
	}
}

func TestDefaultPanelRepositoryIsProvimuxUI(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RemoteManagement.PanelGitHubRepository != "https://github.com/ngvoicu/provimux-ui" {
		t.Fatalf("default panel-github-repository = %q", cfg.RemoteManagement.PanelGitHubRepository)
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("Ab", 32)
	if got := checksumFor([]byte(sum+" *management.html\r\n"), "management.html"); got != strings.ToLower(sum) {
		t.Fatalf("checksumFor = %q", got)
	}
	if got := checksumFor([]byte("xyz  management.html\n"), "management.html"); got != "" {
		t.Fatalf("checksumFor accepted a malformed sum: %q", got)
	}
}
