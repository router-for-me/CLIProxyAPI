// Package autoupdate implements binary self-update from GitHub releases.
package autoupdate

import (
	"archive/tar"
	"archive/zip"
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
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	releaseURL      = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"
	httpUA          = "CLIProxyAPI-self-updater"
	binaryName      = "cli-proxy-api"
	projectName     = "CLIProxyAPI"
	maxDownloadSize = 256 << 20 // 256 MB limit for downloads
)

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

// CheckAndUpdate checks GitHub for a newer release and replaces the running binary if one is found.
// It returns true if an update was applied and the caller should restart.
func CheckAndUpdate(ctx context.Context, proxyURL string) bool {
	if buildinfo.Version == "dev" || buildinfo.Version == "" {
		log.Debug("auto-update skipped: development build")
		return false
	}

	client := newHTTPClient(proxyURL)

	latest, err := fetchLatestVersion(ctx, client)
	if err != nil {
		log.Warnf("auto-update: failed to check latest version: %v", err)
		return false
	}

	latest = strings.TrimPrefix(latest, "v")
	current := strings.TrimPrefix(buildinfo.Version, "v")

	if !isNewer(current, latest) {
		log.Debugf("auto-update: already up to date (current=%s, latest=%s)", current, latest)
		return false
	}

	log.Infof("auto-update: new version available: %s -> %s", current, latest)

	if err := downloadAndReplace(ctx, client, latest); err != nil {
		log.Warnf("auto-update: failed to update: %v", err)
		return false
	}

	log.Infof("auto-update: successfully updated to %s, restarting...", latest)
	return true
}

func newHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{Timeout: 60 * time.Second}
	sdkCfg := &sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}
	util.SetProxy(sdkCfg, client)
	return client
}

func fetchLatestVersion(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", httpUA)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if info.TagName == "" {
		return "", fmt.Errorf("empty tag_name in release response")
	}

	return info.TagName, nil
}

// isNewer returns true if latest is a higher semver than current.
func isNewer(current, latest string) bool {
	cp := parseSemver(current)
	lp := parseSemver(latest)
	if cp == nil || lp == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if lp[i] > cp[i] {
			return true
		}
		if lp[i] < cp[i] {
			return false
		}
	}
	return false
}

// parseSemver extracts major.minor.patch from a version string.
// Returns nil if parsing fails.
func parseSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	// Strip pre-release suffix (e.g. "-beta1")
	if idx := strings.Index(v, "-"); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return nil
	}
	result := make([]int, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return nil
		}
		result[i] = n
	}
	return result
}

// archiveName returns the expected GitHub release asset filename for the current platform.
func archiveName(version string) string {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	if osName == "windows" {
		return fmt.Sprintf("%s_%s_%s_%s.zip", projectName, version, osName, arch)
	}
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", projectName, version, osName, arch)
}

func downloadAndReplace(ctx context.Context, client *http.Client, version string) error {
	tag := "v" + version
	archive := archiveName(version)

	// Download checksums (mandatory — abort if unavailable)
	checksumsURL := fmt.Sprintf("https://github.com/router-for-me/CLIProxyAPI/releases/download/%s/checksums.txt", tag)
	expectedHash, err := fetchChecksumFor(ctx, client, checksumsURL, archive)
	if err != nil {
		return fmt.Errorf("fetch checksums (aborting update for safety): %w", err)
	}

	// Download archive to temp file
	archiveURL := fmt.Sprintf("https://github.com/router-for-me/CLIProxyAPI/releases/download/%s/%s", tag, archive)
	archivePath, err := downloadToTempFile(ctx, client, archiveURL)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	defer os.Remove(archivePath)

	// Verify SHA256
	actualHash, err := hashFile(archivePath)
	if err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	if !strings.EqualFold(expectedHash, actualHash) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	log.Debug("auto-update: checksum verified")

	// Read archive and extract binary
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}

	binaryData, err := extractBinary(data, runtime.GOOS == "windows")
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	// Replace the running binary
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	return replaceBinary(execPath, binaryData)
}

func fetchChecksumFor(ctx context.Context, client *http.Client, url, filename string) (string, error) {
	data, err := downloadSmallFile(ctx, client, url)
	if err != nil {
		return "", err
	}
	return parseChecksums(string(data), filename)
}

// parseChecksums extracts the SHA256 hash for filename from a checksums.txt content.
func parseChecksums(content, filename string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<hash>  <filename>" (two spaces)
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[len(parts)-1] == filename {
			return strings.ToLower(parts[0]), nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", filename)
}

// downloadSmallFile downloads a small file (like checksums.txt) into memory with a 1 MB limit.
func downloadSmallFile(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpUA)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
}

// downloadToTempFile streams a download to a temporary file on disk, respecting maxDownloadSize.
func downloadToTempFile(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", httpUA)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	tmpFile, err := os.CreateTemp("", "cli-proxy-api-dl-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	_, err = io.Copy(tmpFile, io.LimitReader(resp.Body, maxDownloadSize))
	if closeErr := tmpFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("write temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// hashFile computes the SHA256 hex digest of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary extracts the cli-proxy-api binary from the downloaded archive.
func extractBinary(data []byte, isZip bool) ([]byte, error) {
	if isZip {
		return extractFromZip(data)
	}
	return extractFromTarGz(data)
}

func extractFromZip(data []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	target := binaryName + ".exe"
	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if strings.EqualFold(name, target) {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("%s not found in zip archive", target)
}

func extractFromTarGz(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}
		name := filepath.Base(hdr.Name)
		if name == binaryName {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("%s not found in tar.gz archive", binaryName)
}

// replaceBinary atomically replaces the binary at execPath with newData.
// On Windows, the running exe cannot be deleted but can be renamed.
func replaceBinary(execPath string, newData []byte) error {
	dir := filepath.Dir(execPath)

	// Write new binary to temp file
	tmpFile, err := os.CreateTemp(dir, "cli-proxy-api-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // cleanup on failure

	if _, err = tmpFile.Write(newData); err == nil {
		err = tmpFile.Chmod(0o755)
	}
	if closeErr := tmpFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("prepare temp file: %w", err)
	}

	if runtime.GOOS == "windows" {
		// On Windows, rename running exe to .old, then rename new to original name
		oldPath := execPath + ".old"
		os.Remove(oldPath) // remove previous .old if exists
		if err := os.Rename(execPath, oldPath); err != nil {
			return fmt.Errorf("rename current binary to .old: %w", err)
		}
		if err := os.Rename(tmpPath, execPath); err != nil {
			// Try to restore original binary
			if restoreErr := os.Rename(oldPath, execPath); restoreErr != nil {
				return fmt.Errorf("rename new binary into place: %v; failed to restore original: %w", err, restoreErr)
			}
			return fmt.Errorf("rename new binary into place (original restored): %w", err)
		}
		// .old will be cleaned up on next update or manually
	} else {
		// On Unix, atomic rename
		if err := os.Rename(tmpPath, execPath); err != nil {
			return fmt.Errorf("rename new binary into place: %w", err)
		}
	}

	return nil
}

// Restart re-launches the updated binary.
// It first attempts to spawn a replacement process (for unsupervised environments like
// direct CLI invocation or Docker without a restart policy). If spawning fails, it falls
// back to a simple exit, relying on a process supervisor (systemd, Docker restart policy, etc.)
// to bring the service back up.
func Restart() {
	execPath, err := os.Executable()
	if err != nil {
		log.Warnf("auto-update: cannot determine executable path, falling back to exit: %v", err)
		os.Exit(0)
	}

	args := os.Args
	log.Infof("auto-update: restarting %s %v", execPath, args[1:])

	proc, err := os.StartProcess(execPath, args, &os.ProcAttr{
		Dir:   "",
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		log.Warnf("auto-update: could not spawn new process (%v), exiting for supervisor restart", err)
		os.Exit(0)
	}

	// Release the new process so it outlives the current one
	if err := proc.Release(); err != nil {
		log.Warnf("auto-update: failed to release new process: %v", err)
	}

	os.Exit(0)
}
