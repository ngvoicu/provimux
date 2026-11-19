package managementasset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	customAssetURL       = "https://github.com/someone/my-panel/releases/download/v1/management.html"
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

// Every spelling of the original Management Center, and of provimux-ui, downloads provimux-ui
// and never contacts router-for-me.
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
		"ssh://git@github.com/router-for-me/Cli-Proxy-API-Management-Center.git",
		"https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest",
		"https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/Releases/Latest",
		"https://api.github.com:443/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest///",
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

// upstreamResolveReleaseURL is resolveReleaseURL exactly as at CLIProxyAPI v8.0.78, with
// its default (the original Management Center) passed in.
func upstreamResolveReleaseURL(repo string, defaultURL string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return defaultURL
	}

	parsed, err := url.Parse(repo)
	if err != nil || parsed.Host == "" {
		return defaultURL
	}

	host := strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	if host == "api.github.com" {
		if !strings.HasSuffix(strings.ToLower(parsed.Path), "/releases/latest") {
			parsed.Path = parsed.Path + "/releases/latest"
		}
		return parsed.String()
	}

	if host == "github.com" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			repoName := strings.TrimSuffix(parts[1], ".git")
			return fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", parts[0], repoName)
		}
	}

	return defaultURL
}

// Any repository other than the original and provimux-ui resolves exactly as on v8.0.78
// (where v8.0.78 picks its default, Provimux picks provimux-ui). d5f8ba05 fails the ssh://,
// git+ssh://, github.com:443 and www.github.com forms.
func TestCustomRepositoryResolvesAsUpstream(t *testing.T) {
	for _, repo := range []string{
		"ssh://git@github.com/someone/my-panel.git",
		"ssh://git@github.com/someone/my-panel",
		"git+ssh://git@github.com/someone/my-panel.git",
		"git@github.com:someone/my-panel.git",
		"https://github.com/someone/my-panel",
		"https://github.com/someone/my-panel.git/",
		"https://github.com/someone/my-panel/releases",
		"https://github.com:443/someone/my-panel",
		"http://github.com/someone/my-panel",
		"https://www.github.com/someone/my-panel",
		"https://api.github.com/repos/someone/my-panel",
		"https://api.github.com/repos/someone/my-panel/releases/latest",
		"https://api.github.com/repositories/123456/releases/latest",
		"https://api.github.com/repos/router-for-me/other-repo",
		"https://gitlab.com/someone/my-panel",
		"github.com/someone/my-panel",
		"someone/my-panel",
		"not a url",
	} {
		t.Run(repo, func(t *testing.T) {
			want := upstreamResolveReleaseURL(repo, defaultManagementReleaseURL)
			if got := resolveReleaseURL(repo); got != want {
				t.Fatalf("resolveReleaseURL(%q) = %q, v8.0.78 gives %q", repo, got, want)
			}
		})
	}
}

// A saved custom repository is the one downloaded, end to end, including over ssh://.
// d5f8ba05 fails both ssh:// forms (it installed provimux-ui's page over the custom one).
func TestEnsureCustomRepositoryKept(t *testing.T) {
	for _, repo := range []string{
		"ssh://git@github.com/someone/my-panel.git",
		"ssh://git@github.com/someone/my-panel",
		"https://github.com/someone/my-panel",
		"https://github.com/someone/my-panel.git/",
		"https://api.github.com/repos/someone/my-panel",
	} {
		t.Run(repo, func(t *testing.T) {
			routes := map[string]fakeResponse{
				customReleaseURL:     {http.StatusOK, fmt.Sprintf(`{"assets":[{"name":"management.html","browser_download_url":%q,"digest":%q,"size":%d}]}`, customAssetURL, "sha256:"+sha256Hex(nextPage), len(nextPage))},
				customAssetURL:       {http.StatusOK, nextPage},
				provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex("provimux-ui\n"), len("provimux-ui\n"), false)},
				fixtureAssetURL:      {http.StatusOK, "provimux-ui\n"},
			}
			exists, data, requested := runEnsure(t, repo, priorPage, routes)
			if !exists || data != nextPage || !slices.Equal(requested, []string{customReleaseURL, customAssetURL}) {
				t.Fatalf("file = %t %q, requests %v", exists, data, requested)
			}
		})
	}
}

// An existing file is replaced only by a download that is HTTP 200, has exactly the size
// the API reports (never 0) and matches the asset's sha256 API digest. A release's
// checksums.txt is never read on this path, so a swapped manifest cannot vouch for a page.
// d5f8ba05 fails "checksums.txt is not a digest" (it installed by the manifest), "size 0"
// and "size absent" (it installed a 16-byte page).
func TestEnsureNeverReplacesWithoutVerification(t *testing.T) {
	digest := "sha256:" + sha256Hex(nextPage)
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
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", len(nextPage), false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"malformed digest", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:abc", len(nextPage), false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"digest of another algorithm", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha512:"+sha256Hex(nextPage), len(nextPage), false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"checksums.txt is not a digest", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", len(nextPage), true)},
			fixtureSumsURL:       {http.StatusOK, sha256Hex(nextPage) + "  management.html\n"},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"206 full body", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON(digest, len(nextPage), false)},
			fixtureAssetURL:      {http.StatusPartialContent, nextPage},
		}, priorPage},
		{"truncated body", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON(digest, len(nextPage), false)},
			fixtureAssetURL:      {http.StatusOK, nextPage[:4]},
		}, priorPage},
		{"size mismatch", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON(digest, len(nextPage)+1, false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"size 0", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON(digest, 0, false)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"size absent", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, fmt.Sprintf(`{"assets":[{"name":"management.html","browser_download_url":%q,"digest":%q}]}`, fixtureAssetURL, digest)},
			fixtureAssetURL:      {http.StatusOK, nextPage},
		}, priorPage},
		{"download fails", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON(digest, len(nextPage), false)},
			fixtureAssetURL:      {http.StatusServiceUnavailable, ""},
		}, priorPage},
		{"verified by digest", verifiedRelease(nextPage), nextPage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exists, data, requested := runEnsure(t, "", priorPage, tc.routes)
			if !exists || data != tc.want {
				t.Fatalf("file = %t %q, want %q (requests %v)", exists, data, tc.want, requested)
			}
			if slices.Contains(requested, fixtureSumsURL) || slices.Contains(requested, fallbackSumsURL) {
				t.Fatalf("read a checksums.txt on the normal path: %v", requested)
			}
		})
	}
}

// A fresh install never gets an unverified page, and an API lookup that succeeded never
// falls back. d5f8ba05 fails "download fails" (it fell back) and "checksums.txt only"
// (it installed by the manifest).
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
		"checksums.txt only": {
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("", len(nextPage), true)},
			fixtureSumsURL:       {http.StatusOK, sha256Hex(nextPage) + "  management.html\n"},
			fixtureAssetURL:      {http.StatusOK, nextPage},
			fallbackSumsURL:      {http.StatusOK, sha256Hex(nextPage) + "  management.html\n"},
			fallbackPageURL:      {http.StatusOK, nextPage},
		},
		"download fails": {
			provimuxUIReleaseURL: {http.StatusOK, releaseJSON("sha256:"+sha256Hex(nextPage), len(nextPage), false)},
			fixtureAssetURL:      {http.StatusServiceUnavailable, ""},
			fallbackSumsURL:      {http.StatusOK, sha256Hex(nextPage) + "  management.html\n"},
			fallbackPageURL:      {http.StatusOK, nextPage},
		},
	} {
		t.Run(name, func(t *testing.T) {
			exists, data, requested := runEnsure(t, "", "", routes)
			if exists {
				t.Fatalf("installed %q (requests %v)", data, requested)
			}
			if slices.Contains(requested, fallbackSumsURL) || slices.Contains(requested, fallbackPageURL) || slices.Contains(requested, fixtureSumsURL) {
				t.Fatalf("read a checksums.txt or fell back after a successful API lookup: %v", requested)
			}
		})
	}
}

// When the API lookup fails on a fresh install of provimux-ui, the fallback installs the
// latest release's management.html on a checksums.txt match. Nothing else falls back.
// d5f8ba05 fails "custom ssh repository has none" (it fell back to provimux-ui).
func TestEnsureFallback(t *testing.T) {
	sums := fakeResponse{http.StatusOK, "0000000000000000000000000000000000000000000000000000000000000000  other.txt\r\n" + strings.ToUpper(sha256Hex(nextPage)) + " *management.html\r\n"}
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
		{"fallback checksum mismatch", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      {http.StatusOK, sha256Hex(priorPage) + "  management.html\n"}, fallbackPageURL: page,
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"fallback 206", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      sums, fallbackPageURL: {http.StatusPartialContent, nextPage},
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL, fallbackPageURL}},
		{"fallback checksums 206", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      {http.StatusPartialContent, sums.body}, fallbackPageURL: page,
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL}},
		{"fallback without a sum", "", "", map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      {http.StatusOK, "xyz  management.html\n"}, fallbackPageURL: page,
		}, "", false, []string{provimuxUIReleaseURL, fallbackSumsURL}},
		{"existing file kept", "", priorPage, map[string]fakeResponse{
			provimuxUIReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:      sums, fallbackPageURL: page,
		}, priorPage, true, []string{provimuxUIReleaseURL}},
		{"custom repository has none", "https://github.com/someone/my-panel", "", map[string]fakeResponse{
			customReleaseURL: {http.StatusForbidden, ""},
			fallbackSumsURL:  sums, fallbackPageURL: page,
		}, "", false, []string{customReleaseURL}},
		{"custom ssh repository has none", "ssh://git@github.com/someone/my-panel.git", "", map[string]fakeResponse{
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
