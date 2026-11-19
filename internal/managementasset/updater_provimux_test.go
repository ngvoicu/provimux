package managementasset

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const provimuxUIReleaseURL = "https://api.github.com/repos/ngvoicu/provimux-ui/releases/latest"

func TestResolveReleaseURLProvimuxUI(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		repo string
		want string
	}{
		{"default config", cfg.RemoteManagement.PanelGitHubRepository, provimuxUIReleaseURL},
		{"empty", "", provimuxUIReleaseURL},
		{"original", "https://github.com/router-for-me/Cli-Proxy-API-Management-Center", provimuxUIReleaseURL},
		{"original trailing slash", "https://github.com/router-for-me/Cli-Proxy-API-Management-Center/", provimuxUIReleaseURL},
		{"original .git", "https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git", provimuxUIReleaseURL},
		{"original case", "HTTPS://GitHub.COM/Router-For-Me/cli-proxy-api-management-center", provimuxUIReleaseURL},
		{"original api", "https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest", provimuxUIReleaseURL},
		{"original api no suffix", "https://API.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/", provimuxUIReleaseURL},
		{"another repo", "https://github.com/someone/my-panel", "https://api.github.com/repos/someone/my-panel/releases/latest"},
		{"another repo .git", "https://github.com/someone/my-panel.git/", "https://api.github.com/repos/someone/my-panel/releases/latest"},
		{"another api", "https://api.github.com/repos/someone/my-panel", "https://api.github.com/repos/someone/my-panel/releases/latest"},
		{"original-like fork", "https://github.com/someone/Cli-Proxy-API-Management-Center", "https://api.github.com/repos/someone/Cli-Proxy-API-Management-Center/releases/latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveReleaseURL(tt.repo); got != tt.want {
				t.Fatalf("resolveReleaseURL(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestOriginalPanelRepositoryInConfigGetsProvimuxUI(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("remote-management:\n  panel-github-repository: \"https://github.com/router-for-me/Cli-Proxy-API-Management-Center\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveReleaseURL(cfg.RemoteManagement.PanelGitHubRepository); got != provimuxUIReleaseURL {
		t.Fatalf("release URL = %q, want %q", got, provimuxUIReleaseURL)
	}
}

func TestDefaultPanelRepositoryIsProvimuxUI(t *testing.T) {
	if config.DefaultPanelGitHubRepository != "https://github.com/ngvoicu/provimux-ui" {
		t.Fatalf("DefaultPanelGitHubRepository = %q", config.DefaultPanelGitHubRepository)
	}
	if defaultManagementReleaseURL != provimuxUIReleaseURL {
		t.Fatalf("defaultManagementReleaseURL = %q", defaultManagementReleaseURL)
	}
}

// fallbackClient serves the fallback assets and records every requested URL.
func fallbackClient(t *testing.T, sums string, page string) (*http.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var urls []string
	client := &http.Client{Transport: githubAssetTransport(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		urls = append(urls, req.URL.String())
		mu.Unlock()
		body, status := "", http.StatusNotFound
		switch req.URL.String() {
		case defaultManagementFallbackBaseURL + "checksums.txt":
			body, status = sums, http.StatusOK
		case defaultManagementFallbackBaseURL + "management.html":
			body, status = page, http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	return client, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), urls...) }
}

func assertNoOriginal(t *testing.T, urls []string) {
	t.Helper()
	for _, u := range urls {
		lower := strings.ToLower(u)
		if strings.Contains(lower, "router-for-me") || strings.Contains(lower, "router-for.me") {
			t.Fatalf("fallback fetched the original console: %s", u)
		}
		if !strings.HasPrefix(u, "https://github.com/ngvoicu/provimux-ui/") {
			t.Fatalf("fallback fetched an unexpected URL: %s", u)
		}
	}
}

func TestFallbackInstallsVerifiedProvimuxUI(t *testing.T) {
	page := "<html>provimux-ui</html>"
	sum := sha256.Sum256([]byte(page))
	client, urls := fallbackClient(t, hex.EncodeToString(sum[:])+"  management.html\n", page)
	localPath := filepath.Join(t.TempDir(), managementAssetName)

	if !ensureFallbackManagementHTML(t.Context(), client, localPath, defaultManagementReleaseURL) {
		t.Fatal("fallback did not install the page")
	}
	got, err := os.ReadFile(localPath)
	if err != nil || string(got) != page {
		t.Fatalf("installed page = %q, %v", got, err)
	}
	if len(urls()) != 2 {
		t.Fatalf("requests = %v", urls())
	}
	assertNoOriginal(t, urls())
}

func TestFallbackRefusesChecksumMismatch(t *testing.T) {
	client, urls := fallbackClient(t, strings.Repeat("0", 64)+"  management.html\n", "<html>tampered</html>")
	localPath := filepath.Join(t.TempDir(), managementAssetName)

	if ensureFallbackManagementHTML(t.Context(), client, localPath, defaultManagementReleaseURL) {
		t.Fatal("fallback installed a page that does not match checksums.txt")
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("page written despite mismatch: %v", err)
	}
	assertNoOriginal(t, urls())
}

func TestFallbackRefusesMissingChecksum(t *testing.T) {
	client, urls := fallbackClient(t, strings.Repeat("a", 64)+"  other.html\n", "<html>x</html>")
	localPath := filepath.Join(t.TempDir(), managementAssetName)

	if ensureFallbackManagementHTML(t.Context(), client, localPath, defaultManagementReleaseURL) {
		t.Fatal("fallback installed a page without a checksum")
	}
	assertNoOriginal(t, urls())
}

func TestFallbackSkippedForCustomRepository(t *testing.T) {
	client, urls := fallbackClient(t, "", "")
	localPath := filepath.Join(t.TempDir(), managementAssetName)

	if ensureFallbackManagementHTML(t.Context(), client, localPath, "https://api.github.com/repos/someone/my-panel/releases/latest") {
		t.Fatal("fallback installed a page for a custom repository")
	}
	if n := len(urls()); n != 0 {
		t.Fatalf("fallback made %d requests for a custom repository", n)
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
