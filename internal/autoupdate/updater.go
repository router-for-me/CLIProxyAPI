// Package autoupdate provides binary self-update functionality for CLIProxyAPI.
// When enabled via config, it periodically checks GitHub releases for newer versions,
// downloads the matching platform binary, verifies its SHA256 checksum, and performs
// an atomic replacement followed by a graceful restart.
package autoupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	releaseURL    = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"
	checkInterval = 3 * time.Hour
	binaryName    = "cli-proxy-api"
	userAgent     = "CLIProxyAPI-self-updater"

	// usageBackupFile is the filename used to persist usage statistics across restarts.
	usageBackupFile = ".usage-backup.json"
)

var (
	configPtr atomic.Pointer[config.Config]
	once      sync.Once
)

// SetConfig stores the latest configuration snapshot for the auto-updater.
func SetConfig(cfg *config.Config) {
	if cfg != nil {
		configPtr.Store(cfg)
	}
}

// Start launches the background auto-update goroutine. It is safe to call
// multiple times; only the first call starts the updater.
func Start(ctx context.Context) {
	once.Do(func() {
		go run(ctx)
	})
}

func run(ctx context.Context) {
	// Initial check shortly after startup (give the server time to initialize).
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	check := func() {
		cfg := configPtr.Load()
		if cfg == nil || !cfg.AutoUpdate {
			return
		}
		if buildinfo.Version == "dev" || buildinfo.Version == "" {
			log.Debug("auto-update skipped: development build")
			return
		}
		if err := checkAndUpdate(ctx, cfg); err != nil {
			log.WithError(err).Warn("auto-update check failed")
		}
	}

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		check()
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

// releaseInfo represents the GitHub release API response.
type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func checkAndUpdate(ctx context.Context, cfg *config.Config) error {
	client := newHTTPClient(cfg.ProxyURL)

	// 1. Fetch latest release info.
	release, err := fetchLatestRelease(ctx, client)
	if err != nil {
		return fmt.Errorf("fetch release: %w", err)
	}

	latestVersion := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if latestVersion == "" {
		latestVersion = strings.TrimPrefix(strings.TrimSpace(release.Name), "v")
	}
	if latestVersion == "" {
		return fmt.Errorf("release has no version tag")
	}

	currentVersion := strings.TrimPrefix(buildinfo.Version, "v")
	if !isNewer(latestVersion, currentVersion) {
		log.Debugf("auto-update: already up to date (%s)", currentVersion)
		return nil
	}

	log.Infof("auto-update: new version available %s -> %s", currentVersion, latestVersion)

	// 2. Find the matching platform asset.
	assetName := platformAssetName(latestVersion)
	asset, err := findAsset(release.Assets, assetName)
	if err != nil {
		return err
	}

	// 3. Download checksums.txt for verification.
	checksums, err := downloadChecksums(ctx, client, release.Assets)
	if err != nil {
		log.WithError(err).Warn("auto-update: checksums not available, proceeding without verification")
		checksums = nil
	}

	// 4. Download the archive.
	archiveData, err := downloadFile(ctx, client, asset.BrowserDownloadURL)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}

	// 5. Verify checksum.
	if checksums != nil {
		if err := verifyChecksum(archiveData, assetName, checksums); err != nil {
			return fmt.Errorf("checksum verification: %w", err)
		}
		log.Info("auto-update: checksum verified")
	}

	// 6. Extract binary from archive.
	binaryData, err := extractBinaryFromTarGz(archiveData)
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	// 7. Back up usage statistics before restart.
	backupUsageStatistics(cfg)

	// 8. Replace the running binary.
	if err := replaceBinary(binaryData); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	log.Infof("auto-update: binary updated to %s, triggering restart", latestVersion)

	// 9. Trigger graceful restart.
	triggerRestart()
	return nil
}

func newHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{Timeout: 60 * time.Second}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		sdkCfg := &sdkconfig.SDKConfig{ProxyURL: proxyURL}
		util.SetProxy(sdkCfg, client)
	}
	return client
}

func fetchLatestRelease(ctx context.Context, client *http.Client) (*releaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var release releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &release, nil
}

// isNewer returns true when latest is a higher semantic version than current.
func isNewer(latest, current string) bool {
	lp := strings.Split(latest, ".")
	cp := strings.Split(current, ".")

	maxLen := len(lp)
	if len(cp) > maxLen {
		maxLen = len(cp)
	}

	for i := 0; i < maxLen; i++ {
		var l, c int
		if i < len(lp) {
			l, _ = strconv.Atoi(lp[i])
		}
		if i < len(cp) {
			c, _ = strconv.Atoi(cp[i])
		}
		if l > c {
			return true
		}
		if l < c {
			return false
		}
	}
	return false
}

// platformAssetName constructs the expected archive filename for the current platform.
func platformAssetName(version string) string {
	return fmt.Sprintf("CLIProxyAPI_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func findAsset(assets []releaseAsset, name string) (*releaseAsset, error) {
	for i := range assets {
		if strings.EqualFold(assets[i].Name, name) {
			return &assets[i], nil
		}
	}
	return nil, fmt.Errorf("asset %s not found in release", name)
}

func downloadChecksums(ctx context.Context, client *http.Client, assets []releaseAsset) (map[string]string, error) {
	asset, err := findAsset(assets, "checksums.txt")
	if err != nil {
		return nil, err
	}

	data, err := downloadFile(ctx, client, asset.BrowserDownloadURL)
	if err != nil {
		return nil, err
	}

	return parseChecksums(data), nil
}

// parseChecksums parses a checksums.txt file (sha256sum format: "hash  filename").
func parseChecksums(data []byte) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			result[parts[1]] = strings.ToLower(parts[0])
		}
	}
	return result
}

func verifyChecksum(data []byte, assetName string, checksums map[string]string) error {
	expected, ok := checksums[assetName]
	if !ok {
		return fmt.Errorf("no checksum for %s", assetName)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("expected %s, got %s", expected, actual)
	}
	return nil
}

func downloadFile(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// extractBinaryFromTarGz extracts the cli-proxy-api binary from a tar.gz archive.
func extractBinaryFromTarGz(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, errNext := tr.Next()
		if errNext == io.EOF {
			break
		}
		if errNext != nil {
			return nil, fmt.Errorf("read tar: %w", errNext)
		}
		if filepath.Base(hdr.Name) == binaryName {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %s not found in archive", binaryName)
}

// replaceBinary atomically replaces the current executable with new binary data.
func replaceBinary(data []byte) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, "cliproxyapi-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // Clean up on failure.

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, execPath); err != nil {
		return fmt.Errorf("rename binary: %w", err)
	}
	return nil
}

// backupUsageStatistics saves current usage statistics to a file so they can
// be restored after restart. Uses the management API via localhost.
func backupUsageStatistics(cfg *config.Config) {
	port := cfg.Port
	if port == 0 {
		port = 8317
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/v0/management/usage/export", port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		log.WithError(err).Warn("auto-update: failed to create usage backup request")
		return
	}

	// Use management secret if configured.
	if cfg.RemoteManagement.SecretKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.RemoteManagement.SecretKey)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Warn("auto-update: failed to export usage statistics")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warnf("auto-update: usage export returned %d", resp.StatusCode)
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.WithError(err).Warn("auto-update: failed to read usage export")
		return
	}

	backupPath := usageBackupPath(cfg)
	if err := os.WriteFile(backupPath, data, 0o644); err != nil {
		log.WithError(err).Warn("auto-update: failed to save usage backup")
		return
	}
	log.Infof("auto-update: usage statistics backed up to %s", backupPath)
}

// RestoreUsageStatistics imports previously backed-up usage statistics.
// Call this after the server has started listening.
func RestoreUsageStatistics(cfg *config.Config) {
	backupPath := usageBackupPath(cfg)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("auto-update: failed to read usage backup")
		}
		return
	}

	port := cfg.Port
	if port == 0 {
		port = 8317
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/v0/management/usage/import", port)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		log.WithError(err).Warn("auto-update: failed to create usage restore request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.RemoteManagement.SecretKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.RemoteManagement.SecretKey)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.WithError(err).Warn("auto-update: failed to restore usage statistics")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		os.Remove(backupPath)
		log.Info("auto-update: usage statistics restored from backup")
	} else {
		log.Warnf("auto-update: usage restore returned %d", resp.StatusCode)
	}
}

func usageBackupPath(cfg *config.Config) string {
	dir := strings.TrimSpace(cfg.AuthDir)
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(filepath.Dir(dir), usageBackupFile)
}

func triggerRestart() {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		log.WithError(err).Error("auto-update: failed to find own process")
		return
	}
	_ = p.Signal(syscall.SIGTERM)
}
