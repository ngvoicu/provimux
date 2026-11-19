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
	defaultManagementReleaseURL  = "https://api.github.com/repos/ngvoicu/provimux-ui/releases/latest"
	defaultManagementFallbackURL = "https://github.com/ngvoicu/provimux-ui/releases/latest/download/"
	managementAssetName          = "management.html"
	httpUserAgent                = "CLIProxyAPI-management-updater"
	managementSyncMinInterval    = 30 * time.Second
	updateCheckInterval          = 3 * time.Hour
	maxAssetDownloadSize         = 50 << 20 // 10 MB safety limit for management asset downloads
)

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

		releaseURL := resolveReleaseURL(panelRepository)
		client := newHTTPClient(proxyURL)

		localHash, err := fileSHA256(localPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.WithError(err).Debug("failed to read local management asset hash")
			}
			localHash = ""
		}

		asset, remoteHash, err := fetchLatestAsset(ctx, client, releaseURL)
		if err != nil {
			if localFileMissing && releaseURL == defaultManagementReleaseURL {
				log.WithError(err).Warn("failed to fetch latest management release information, trying fallback page")
				if ensureFallbackManagementHTML(ctx, client, localPath) {
					return nil, nil
				}
				return nil, nil
			}
			log.WithError(err).Warn("failed to fetch latest management release information")
			return nil, nil
		}

		// Provimux: install only what the release's API digest and size vouch for.
		if !isSHA256Hex(remoteHash) || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.Digest)), "sha256:") || asset.Size <= 0 {
			log.Errorf("management asset has no sha256 digest or size (digest %q, size %d) — not installing it", asset.Digest, asset.Size)
			return nil, nil
		}

		if remoteHash != "" && localHash != "" && strings.EqualFold(remoteHash, localHash) {
			log.Debug("management asset is already up to date")
			return nil, nil
		}

		data, downloadedHash, err := downloadAsset(ctx, client, asset.BrowserDownloadURL)
		if err != nil {
			log.WithError(err).Warn("failed to download management asset")
			return nil, nil
		}

		if int64(len(data)) != asset.Size {
			log.Errorf("management asset size mismatch: expected %d got %d — aborting update for safety", asset.Size, len(data))
			return nil, nil
		}
		if remoteHash != "" && !strings.EqualFold(remoteHash, downloadedHash) {
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

// ensureFallbackManagementHTML installs provimux-ui's latest management.html from the release
// download URLs when it matches the sha256 listed in the same release's checksums.txt.
func ensureFallbackManagementHTML(ctx context.Context, client *http.Client, localPath string) bool {
	sums, _, err := downloadAsset(ctx, client, defaultManagementFallbackURL+"checksums.txt")
	if err != nil {
		log.WithError(err).Warn("failed to download fallback management checksums")
		return false
	}
	expectedHash := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == managementAssetName {
			expectedHash = strings.ToLower(fields[0])
		}
	}
	if !isSHA256Hex(expectedHash) {
		log.Warn("fallback checksums.txt has no sha256 for management.html")
		return false
	}

	data, downloadedHash, err := downloadAsset(ctx, client, defaultManagementFallbackURL+managementAssetName)
	if err != nil {
		log.WithError(err).Warn("failed to download fallback management control panel page")
		return false
	}
	if downloadedHash != expectedHash {
		log.Errorf("fallback management asset checksum mismatch: expected %s got %s — not installing it", expectedHash, downloadedHash)
		return false
	}

	if err = atomicWriteFile(localPath, data); err != nil {
		log.WithError(err).Warn("failed to persist fallback management control panel page")
		return false
	}

	log.Infof("management asset updated from fallback page successfully (hash=%s)", downloadedHash)
	return true
}

func resolveReleaseURL(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" || isDefaultPanelRepository(repo) {
		return defaultManagementReleaseURL
	}

	parsed, err := url.Parse(repo)
	if err != nil || parsed.Host == "" {
		return defaultManagementReleaseURL
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

	return defaultManagementReleaseURL
}

func fetchLatestAsset(ctx context.Context, client *http.Client, releaseURL string) (*releaseAsset, string, error) {
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
		return nil, "", fmt.Errorf("fetch release: %w", err)
	}

	var release releaseResponse
	if err = json.Unmarshal(data, &release); err != nil {
		return nil, "", fmt.Errorf("decode release response: %w", err)
	}

	for i := range release.Assets {
		asset := &release.Assets[i]
		if strings.EqualFold(asset.Name, managementAssetName) {
			remoteHash := parseDigest(asset.Digest)
			return asset, remoteHash, nil
		}
	}

	return nil, "", fmt.Errorf("management asset %s not found in latest release", managementAssetName)
}

func downloadAsset(ctx context.Context, client *http.Client, downloadURL string) ([]byte, string, error) {
	if strings.TrimSpace(downloadURL) == "" {
		return nil, "", fmt.Errorf("empty download url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("download asset: %w", err)
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
	if resp.StatusCode != http.StatusOK { // Provimux: a 206 or 204 is never the asset
		return nil, "", fmt.Errorf("download asset: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetDownloadSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("download asset: %w", err)
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

// isDefaultPanelRepository reports whether repo names provimux-ui or the original
// Management Center (by owner/name in any case, with or without ".git", or by numeric id
// on api.github.com), which Provimux serves as provimux-ui. Any other value is left to
// resolveReleaseURL's own handling.
func isDefaultPanelRepository(repo string) bool {
	parsed, err := url.Parse(repo)
	if err != nil {
		return false
	}
	var segments []string
	for _, segment := range strings.Split(strings.ToLower(parsed.Path), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "api.github.com" && len(segments) >= 2 && segments[0] == "repositories" {
		id, errID := strconv.ParseUint(segments[1], 10, 64)
		return errID == nil && (id == 1051566067 || id == 1463889407)
	}
	if host == "api.github.com" && len(segments) >= 1 && segments[0] == "repos" {
		segments = segments[1:]
	} else if host != "github.com" && host != "www.github.com" {
		return false
	}
	if len(segments) < 2 {
		return false
	}
	name := segments[0] + "/" + strings.TrimSuffix(segments[1], ".git")
	return name == "router-for-me/cli-proxy-api-management-center" || name == "ngvoicu/provimux-ui"
}

func isSHA256Hex(sum string) bool {
	if len(sum) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(sum)
	return err == nil
}
