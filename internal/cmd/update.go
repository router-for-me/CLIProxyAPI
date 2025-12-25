package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	githubReleaseURL = "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest"
	userAgent        = "CLIProxyAPI-updater"
)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// DoUpdate checks for updates and installs the latest version if available.
func DoUpdate(cfg *config.Config) {
	log.Infof("Checking for updates... Current version: %s", buildinfo.Version)

	ctx := context.Background()
	client := newUpdateHTTPClient(cfg.ProxyURL)

	release, err := fetchLatestRelease(ctx, client)
	if err != nil {
		log.Errorf("Failed to fetch latest release: %v", err)
		return
	}

	latestVersion := release.TagName
	if strings.HasPrefix(latestVersion, "v") {
		latestVersion = latestVersion[1:]
	}

	currentVersion := buildinfo.Version
	if strings.HasPrefix(currentVersion, "v") {
		currentVersion = currentVersion[1:]
	}

	if currentVersion != "dev" && !isNewerVersion(currentVersion, latestVersion) {
		log.Info("CLIProxyAPI is already up to date.")
		return
	}

	log.Infof("New version available: %s", release.TagName)

	assetURL, assetName := findCompatibleAsset(release)
	if assetURL == "" {
		log.Errorf("No compatible asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
		return
	}

	log.Infof("Downloading %s...", assetName)
	data, err := downloadAssetData(ctx, client, assetURL)
	if err != nil {
		log.Errorf("Failed to download asset: %v", err)
		return
	}

	log.Info("Updating binary...")
	err = applyUpdate(data, assetName)
	if err != nil {
		log.Errorf("Failed to apply update: %v", err)
		return
	}

	log.Infof("Successfully updated to %s! Please restart CLIProxyAPI.", release.TagName)
}

func newUpdateHTTPClient(proxyURL string) *http.Client {
	client := &http.Client{}
	sdkCfg := &sdkconfig.SDKConfig{ProxyURL: strings.TrimSpace(proxyURL)}
	util.SetProxy(sdkCfg, client)
	return client
}

func fetchLatestRelease(ctx context.Context, client *http.Client) (*githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubReleaseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

func isNewerVersion(current, latest string) bool {
	// Clean potentially prefixed 'v'
	current = strings.TrimPrefix(current, "v")
	latest = strings.TrimPrefix(latest, "v")

	currParts := strings.Split(current, ".")
	lateParts := strings.Split(latest, ".")

	for i := 0; i < len(currParts) && i < len(lateParts); i++ {
		currNum, err1 := strconv.Atoi(currParts[i])
		lateNum, err2 := strconv.Atoi(lateParts[i])
		if err1 != nil || err2 != nil {
			// If we can't parse as integer, fall back to string comparison for robustness
			if lateParts[i] > currParts[i] {
				return true
			}
			if currParts[i] > lateParts[i] {
				return false
			}
			continue
		}

		if lateNum > currNum {
			return true
		}
		if currNum > lateNum {
			return false
		}
	}

	return len(lateParts) > len(currParts)
}

func findCompatibleAsset(release *githubRelease) (string, string) {
	osName := runtime.GOOS
	archName := runtime.GOARCH

	// Map GOARCH to what goreleaser might use if different (usually they match for these)
	for _, asset := range release.Assets {
		nameLower := strings.ToLower(asset.Name)
		if strings.Contains(nameLower, osName) && strings.Contains(nameLower, archName) {
			// Ensure it's not a checksum file
			if !strings.HasSuffix(nameLower, ".txt") && !strings.HasSuffix(nameLower, ".md") {
				return asset.BrowserDownloadURL, asset.Name
			}
		}
	}
	return "", ""
}

func downloadAssetData(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func applyUpdate(data []byte, assetName string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not get executable path: %w", err)
	}

	// Extract binary from archive
	var binaryData []byte
	if strings.HasSuffix(assetName, ".zip") {
		binaryData, err = extractFromZip(data)
	} else if strings.HasSuffix(assetName, ".tar.gz") {
		binaryData, err = extractFromTarGz(data)
	} else {
		return fmt.Errorf("unsupported asset format: %s", assetName)
	}

	if err != nil {
		return fmt.Errorf("failed to extract binary: %w", err)
	}

	// Atomic replace (as much as possible)
	oldPath := exePath + ".old"
	_ = os.Remove(oldPath)

	err = os.Rename(exePath, oldPath)
	if err != nil {
		return fmt.Errorf("failed to move current binary: %w", err)
	}

	err = os.WriteFile(exePath, binaryData, 0755)
	if err != nil {
		// Try to rollback
		if errRollback := os.Rename(oldPath, exePath); errRollback != nil {
			log.Errorf("CRITICAL: Failed to restore original binary after update failure. Please manually rename %s to %s. Rollback error: %v", oldPath, exePath, errRollback)
		}
		return fmt.Errorf("failed to write new binary: %w", err)
	}

	// Optional: remove old binary
	_ = os.Remove(oldPath)

	return nil
}

func extractFromZip(data []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}

	// Identify what we are looking for based on current executable name
	exePath, _ := os.Executable()
	baseName := strings.ToLower(filepath.Base(exePath))
	// Also check without extension if it has one
	baseNameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	for _, file := range reader.File {
		name := strings.ToLower(filepath.Base(file.Name))
		if name == baseName || name == baseNameNoExt || name == "cli-proxy-api" || name == "cli-proxy-api.exe" {
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			binaryData, readErr := io.ReadAll(rc)
			rc.Close() // Explicit close instead of defer in loop
			if readErr != nil {
				return nil, readErr
			}
			return binaryData, nil
		}
	}
	return nil, fmt.Errorf("binary not found in zip archive")
}

func extractFromTarGz(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	// Identify what we are looking for based on current executable name
	exePath, _ := os.Executable()
	baseName := strings.ToLower(filepath.Base(exePath))
	baseNameNoExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		name := strings.ToLower(filepath.Base(header.Name))
		if name == baseName || name == baseNameNoExt || name == "cli-proxy-api" {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary not found in tar.gz archive")
}
