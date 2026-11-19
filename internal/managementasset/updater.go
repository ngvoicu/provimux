package managementasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpfetch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	defaultManagementReleaseURL = "https://api.github.com/repos/ngvoicu/provimux-ui/releases/latest"
	// defaultManagementFallbackBaseURL serves the latest provimux-ui release assets without
	// the GitHub API. It is used only for the default repository when the API lookup fails
	// and no console file exists yet; management.html must match checksums.txt.
	defaultManagementFallbackBaseURL = "https://github.com/ngvoicu/provimux-ui/releases/latest/download/"
	managementAssetName              = "management.html"
	managementChecksumsName          = "checksums.txt"
	httpUserAgent                    = "CLIProxyAPI-management-updater"
	managementSyncMinInterval        = 30 * time.Second
	updateCheckInterval              = 3 * time.Hour
	maxAssetDownloadSize             = 50 << 20 // 10 MB safety limit for management asset downloads
)

// Repository identities (see panelRepositoryIdentity) that select the default console.
// The original Management Center is named in configs copied from CLIProxyAPI's
// config.example.yaml; Provimux serves provimux-ui in its place.
var defaultPanelRepositoryIdentities = map[string]bool{
	"ngvoicu/provimux-ui": true,
	"#1463889407":         true, // ngvoicu/provimux-ui
	"router-for-me/cli-proxy-api-management-center": true,
	"#1051566067": true, // router-for-me/Cli-Proxy-API-Management-Center
}

// ManagementFileName exposes the control panel asset filename.
const ManagementFileName = managementAssetName

var (
	lastUpdateCheckMu   sync.Mutex
	lastUpdateCheckTime time.Time
	currentConfigPtr    atomic.Pointer[config.Config]
	schedulerOnce       sync.Once
	schedulerConfigPath atomic.Value
	sfGroup             singleflight.Group
)

// SetCurrentConfig stores the latest configuration snapshot for management asset decisions.
func SetCurrentConfig(cfg *config.Config) {
	if cfg == nil {
		currentConfigPtr.Store(nil)
		return
	}
	currentConfigPtr.Store(cfg)
}

// StartAutoUpdater launches a background goroutine that periodically ensures the management asset is up to date.
// It respects the disable-control-panel flag on every iteration and supports hot-reloaded configurations.
func StartAutoUpdater(ctx context.Context, configFilePath string) {
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		log.Debug("management asset auto-updater skipped: empty config path")
		return
	}

	schedulerConfigPath.Store(configFilePath)

	schedulerOnce.Do(func() {
		go runAutoUpdater(ctx)
	})
}

func runAutoUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(updateCheckInterval)
	defer ticker.Stop()

	runOnce := func() {
		cfg := currentConfigPtr.Load()
		if reason, skip := autoUpdateSkipReason(cfg); skip {
			log.Debugf("management asset auto-updater skipped: %s", reason)
			return
		}

		configPath, _ := schedulerConfigPath.Load().(string)
		staticDir := StaticDir(configPath)
		EnsureLatestManagementHTML(ctx, staticDir, cfg.ProxyURL, cfg.RemoteManagement.PanelGitHubRepository)
	}

	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func autoUpdateSkipReason(cfg *config.Config) (string, bool) {
	if cfg == nil {
		return "config not yet available", true
	}
	if cfg.Home.Enabled {
		return "cluster mode enabled", true
	}
	if cfg.RemoteManagement.DisableControlPanel {
		return "control panel disabled", true
	}
	if cfg.RemoteManagement.DisableAutoUpdatePanel {
		return "disable-auto-update-panel is enabled", true
	}
	return "", false
}

func newHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{Timeout: 15 * time.Second}

	sdkCfg := &sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}
	util.SetProxy(sdkCfg, client)

	return client
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type releaseResponse struct {
	Assets []releaseAsset `json:"assets"`
}

// StaticDir resolves the directory that stores the management control panel asset.
func StaticDir(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return filepath.Dir(cleaned)
		}
		return cleaned
	}

	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "static")
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	fileInfo, err := os.Stat(configFilePath)
	if err == nil {
		if fileInfo.IsDir() {
			base = configFilePath
		}
	}

	return filepath.Join(base, "static")
}

// FilePath resolves the absolute path to the management control panel asset.
func FilePath(configFilePath string) string {
	if override := strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")); override != "" {
		cleaned := filepath.Clean(override)
		if strings.EqualFold(filepath.Base(cleaned), managementAssetName) {
			return cleaned
		}
		return filepath.Join(cleaned, ManagementFileName)
	}

	dir := StaticDir(configFilePath)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ManagementFileName)
}

// EnsureLatestManagementHTML checks the latest management.html asset and updates the local copy when needed.
// It coalesces concurrent sync attempts and returns whether the asset exists after the sync attempt.
func EnsureLatestManagementHTML(ctx context.Context, staticDir string, proxyURL string, panelRepository string) bool {
	if ctx == nil {
		ctx = context.Background()
	}

	staticDir = strings.TrimSpace(staticDir)
	if staticDir == "" {
		log.Debug("management asset sync skipped: empty static directory")
		return false
	}
	localPath := filepath.Join(staticDir, managementAssetName)

	_, _, _ = sfGroup.Do(localPath, func() (interface{}, error) {
		lastUpdateCheckMu.Lock()
		now := time.Now()
		timeSinceLastAttempt := now.Sub(lastUpdateCheckTime)
		if !lastUpdateCheckTime.IsZero() && timeSinceLastAttempt < managementSyncMinInterval {
			lastUpdateCheckMu.Unlock()
			log.Debugf(
				"management asset sync skipped by throttle: last attempt %v ago (interval %v)",
				timeSinceLastAttempt.Round(time.Second),
				managementSyncMinInterval,
			)
			return nil, nil
		}
		lastUpdateCheckTime = now
		lastUpdateCheckMu.Unlock()

		localFileMissing := false
		if _, errStat := os.Stat(localPath); errStat != nil {
			if errors.Is(errStat, os.ErrNotExist) {
				localFileMissing = true
			} else {
				log.WithError(errStat).Debug("failed to stat local management asset")
			}
		}

		if errMkdirAll := os.MkdirAll(staticDir, 0o755); errMkdirAll != nil {
			log.WithError(errMkdirAll).Warn("failed to prepare static directory for management asset")
			return nil, nil
		}

		releaseURL, isDefaultRepository := resolvePanelRelease(panelRepository)
		client := newHTTPClient(proxyURL)

		localHash, err := fileSHA256(localPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.WithError(err).Debug("failed to read local management asset hash")
			}
			localHash = ""
		}

		release, err := fetchLatestRelease(ctx, client, releaseURL)
		if err != nil {
			if localFileMissing {
				log.WithError(err).Warn("failed to fetch latest management release information, trying verified fallback")
				ensureFallbackManagementHTML(ctx, client, localPath, isDefaultRepository)
				return nil, nil
			}
			log.WithError(err).Warn("failed to fetch latest management release information")
			return nil, nil
		}

		// Every install or replacement needs a sha256 from this release.
		asset, remoteHash, err := verifiedReleaseAsset(ctx, client, release)
		if err != nil {
			log.WithError(err).Error("management asset not verifiable — not installing it")
			return nil, nil
		}

		if localHash != "" && strings.EqualFold(remoteHash, localHash) {
			log.Debug("management asset is already up to date")
			return nil, nil
		}

		data, downloadedHash, err := downloadAsset(ctx, client, asset.BrowserDownloadURL)
		if err != nil {
			if localFileMissing {
				log.WithError(err).Warn("failed to download management asset, trying verified fallback")
				ensureFallbackManagementHTML(ctx, client, localPath, isDefaultRepository)
				return nil, nil
			}
			log.WithError(err).Warn("failed to download management asset")
			return nil, nil
		}

		if asset.Size > 0 && int64(len(data)) != asset.Size {
			log.Errorf("management asset size mismatch: expected %d got %d — aborting update for safety", asset.Size, len(data))
			return nil, nil
		}
		if !strings.EqualFold(remoteHash, downloadedHash) {
			log.Errorf("management asset digest mismatch: expected %s got %s — aborting update for safety", remoteHash, downloadedHash)
			return nil, nil
		}

		if err = atomicWriteFile(localPath, data); err != nil {
			log.WithError(err).Warn("failed to update management asset on disk")
			return nil, nil
		}

		log.Infof("management asset updated successfully (hash=%s)", downloadedHash)
		return nil, nil
	})

	_, err := os.Stat(localPath)
	return err == nil
}

func ensureFallbackManagementHTML(ctx context.Context, client *http.Client, localPath string, isDefaultRepository bool) bool {
	if !isDefaultRepository {
		log.Warn("no fallback for a custom panel-github-repository; management control panel page not installed")
		return false
	}

	sums, _, err := downloadAsset(ctx, client, defaultManagementFallbackBaseURL+managementChecksumsName)
	if err != nil {
		log.WithError(err).Warn("failed to download fallback management checksums")
		return false
	}
	expectedHash := checksumFor(sums, managementAssetName)
	if expectedHash == "" {
		log.Warnf("fallback %s has no sha256 for %s", managementChecksumsName, managementAssetName)
		return false
	}

	data, downloadedHash, err := downloadAsset(ctx, client, defaultManagementFallbackBaseURL+managementAssetName)
	if err != nil {
		log.WithError(err).Warn("failed to download fallback management control panel page")
		return false
	}
	if !strings.EqualFold(expectedHash, downloadedHash) {
		log.Errorf("fallback management asset checksum mismatch: expected %s got %s — not installing it", expectedHash, downloadedHash)
		return false
	}

	if err = atomicWriteFile(localPath, data); err != nil {
		log.WithError(err).Warn("failed to persist fallback management control panel page")
		return false
	}

	log.Infof("management asset installed from fallback release download (hash=%s)", downloadedHash)
	return true
}

// checksumFor returns the lowercase sha256 for name from a sha256sum-style listing.
func checksumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return validSHA256(fields[0])
		}
	}
	return ""
}

// resolveReleaseURL maps panel-github-repository to a releases API URL.
func resolveReleaseURL(repo string) string {
	releaseURL, _ := resolvePanelRelease(repo)
	return releaseURL
}

// resolvePanelRelease returns the releases API URL for panel-github-repository and
// whether it is the default console. Empty or unrecognised values, provimux-ui and the
// original Management Center (in any spelling, by name or numeric id) select the
// default, provimux-ui; any other GitHub repository is kept.
func resolvePanelRelease(repo string) (string, bool) {
	identity, releaseURL, ok := panelRepositoryIdentity(repo)
	if !ok || defaultPanelRepositoryIdentities[identity] {
		return defaultManagementReleaseURL, true
	}
	return releaseURL, false
}

// panelRepositoryIdentity normalises a GitHub repository URL or releases API URL to one
// identity ("owner/repo" lowercased, or "#<numeric id>") and its HTTPS releases/latest
// API URL. Scheme, host case, the default port, ".git" (any case), extra slashes and
// trailing path parts do not change the identity.
func panelRepositoryIdentity(repo string) (identity string, releaseURL string, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(repo))
	if err != nil {
		return "", "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", "", false
	}
	if port := parsed.Port(); port != "" && !(scheme == "https" && port == "443") && !(scheme == "http" && port == "80") {
		return "", "", false
	}

	var segments []string
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}

	var owner, name string
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "www.github.com":
		if len(segments) < 2 {
			return "", "", false
		}
		owner, name = segments[0], segments[1]
	case "api.github.com":
		switch {
		case len(segments) >= 3 && strings.EqualFold(segments[0], "repos"):
			owner, name = segments[1], segments[2]
		case len(segments) >= 2 && strings.EqualFold(segments[0], "repositories"):
			id, errID := strconv.ParseUint(segments[1], 10, 64)
			if errID != nil || id == 0 {
				return "", "", false
			}
			idText := strconv.FormatUint(id, 10)
			return "#" + idText, "https://api.github.com/repositories/" + idText + "/releases/latest", true
		default:
			return "", "", false
		}
	default:
		return "", "", false
	}

	if len(name) > len(".git") && strings.EqualFold(name[len(name)-len(".git"):], ".git") {
		name = name[:len(name)-len(".git")]
	}
	if owner == "" || name == "" {
		return "", "", false
	}
	identity = strings.ToLower(owner) + "/" + strings.ToLower(name)
	releaseURL = fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", url.PathEscape(owner), url.PathEscape(name))
	return identity, releaseURL, true
}

func fetchLatestAsset(ctx context.Context, client *http.Client, releaseURL string) (*releaseAsset, string, error) {
	release, err := fetchLatestRelease(ctx, client, releaseURL)
	if err != nil {
		return nil, "", err
	}
	asset := findReleaseAsset(release, managementAssetName)
	if asset == nil {
		return nil, "", fmt.Errorf("management asset %s not found in latest release", managementAssetName)
	}
	return asset, parseDigest(asset.Digest), nil
}

func fetchLatestRelease(ctx context.Context, client *http.Client, releaseURL string) (*releaseResponse, error) {
	if strings.TrimSpace(releaseURL) == "" {
		releaseURL = defaultManagementReleaseURL
	}

	headers := map[string]string{
		"Accept":     "application/vnd.github+json",
		"User-Agent": httpUserAgent,
	}
	if token := githubauth.TokenForURL(releaseURL); token != "" {
		headers["Authorization"] = "Bearer " + token
	}

	data, err := httpfetch.GetBytes(ctx, client, releaseURL, headers, 0)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}

	var release releaseResponse
	if err = json.Unmarshal(data, &release); err != nil {
		return nil, fmt.Errorf("decode release response: %w", err)
	}
	return &release, nil
}

func findReleaseAsset(release *releaseResponse, name string) *releaseAsset {
	for i := range release.Assets {
		if strings.EqualFold(release.Assets[i].Name, name) {
			return &release.Assets[i]
		}
	}
	return nil
}

// verifiedReleaseAsset returns the release's management.html and its sha256: the asset's
// API digest when it is a valid sha256, otherwise the line in the same release's
// checksums.txt. A release that offers neither is refused.
func verifiedReleaseAsset(ctx context.Context, client *http.Client, release *releaseResponse) (*releaseAsset, string, error) {
	asset := findReleaseAsset(release, managementAssetName)
	if asset == nil {
		return nil, "", fmt.Errorf("management asset %s not found in latest release", managementAssetName)
	}
	if hash := sha256FromDigest(asset.Digest); hash != "" {
		return asset, hash, nil
	}
	sumsAsset := findReleaseAsset(release, managementChecksumsName)
	if sumsAsset == nil {
		return nil, "", fmt.Errorf("release has no sha256 digest and no %s for %s", managementChecksumsName, managementAssetName)
	}
	sums, _, err := downloadAsset(ctx, client, sumsAsset.BrowserDownloadURL)
	if err != nil {
		return nil, "", fmt.Errorf("download %s: %w", managementChecksumsName, err)
	}
	hash := checksumFor(sums, managementAssetName)
	if hash == "" {
		return nil, "", fmt.Errorf("release %s has no sha256 for %s", managementChecksumsName, managementAssetName)
	}
	return asset, hash, nil
}

// sha256FromDigest returns the hex sha256 of a GitHub asset digest ("sha256:<hex>"), or "".
func sha256FromDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	if idx := strings.Index(digest, ":"); idx >= 0 && !strings.EqualFold(digest[:idx], "sha256") {
		return ""
	}
	return validSHA256(parseDigest(digest))
}

// validSHA256 returns the lowercase hex sum when it is a well-formed sha256, otherwise "".
func validSHA256(sum string) string {
	sum = strings.ToLower(strings.TrimSpace(sum))
	if len(sum) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return ""
	}
	return sum
}

// downloadAsset fetches an asset; only HTTP 200 with a body within the size limit is accepted.
func downloadAsset(ctx context.Context, client *http.Client, downloadURL string) ([]byte, string, error) {
	if strings.TrimSpace(downloadURL) == "" {
		return nil, "", fmt.Errorf("empty download url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("download asset: create request: %w", err)
	}
	req.Header.Set("User-Agent", httpUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download asset: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close management asset response body")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download asset: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetDownloadSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("download asset: read response: %w", err)
	}
	if int64(len(data)) > maxAssetDownloadSize {
		return nil, "", fmt.Errorf("download asset: response exceeds maximum allowed size of %d bytes", maxAssetDownloadSize)
	}

	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()

	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func atomicWriteFile(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "management-*.html")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err = tmpFile.Write(data); err != nil {
		return err
	}

	if err = tmpFile.Chmod(0o644); err != nil {
		return err
	}

	if err = tmpFile.Close(); err != nil {
		return err
	}

	if err = os.Rename(tmpName, path); err != nil {
		return err
	}

	return nil
}

func parseDigest(digest string) string {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return ""
	}

	if idx := strings.Index(digest, ":"); idx >= 0 {
		digest = digest[idx+1:]
	}

	return strings.ToLower(strings.TrimSpace(digest))
}
