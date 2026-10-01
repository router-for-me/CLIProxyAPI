package misc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// GrokCLIFallbackVersion must stay above the minimum version accepted by
	// cli-chat-proxy. On 2026-10-01 that floor was 1.0.13 (#6249).
	GrokCLIFallbackVersion = "1.0.46"
	grokCLIVersionTTL      = 6 * time.Hour
	grokCLIFetchTimeout    = 10 * time.Second
)

var (
	grokCLIStableVersionURL   = "https://x.ai/cli/stable"
	grokCLIRegistryVersionURL = "https://registry.npmjs.org/@xai-official/grok/latest"
)

var (
	grokCLIVersionMu     sync.RWMutex
	cachedGrokCLIVersion = GrokCLIFallbackVersion
	grokCLIVersionExpiry time.Time
	grokCLIUpdaterOnce   sync.Once
)

// StartGrokCLIVersionUpdater starts a background goroutine that periodically refreshes
// the cached Grok CLI stable version used for cli-chat-proxy identity headers.
func StartGrokCLIVersionUpdater(ctx context.Context) {
	grokCLIUpdaterOnce.Do(func() {
		go runGrokCLIVersionUpdater(ctx)
	})
}

func runGrokCLIVersionUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	interval := grokCLIVersionTTL / 2
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Infof("periodic Grok CLI version refresh started (interval=%s)", interval)
	RefreshGrokCLIVersion(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			RefreshGrokCLIVersion(ctx)
		}
	}
}

// RefreshGrokCLIVersion fetches and caches the latest stable Grok CLI version.
// The cached fallback is retained when every version feed is unavailable.
func RefreshGrokCLIVersion(ctx context.Context) {
	version, errFetch := fetchGrokCLIVersion(ctx)
	if errFetch != nil {
		log.WithError(errFetch).Warn("failed to refresh Grok CLI stable version, keeping cached version")
		return
	}

	now := time.Now()
	grokCLIVersionMu.Lock()
	cachedGrokCLIVersion = version
	grokCLIVersionExpiry = now.Add(grokCLIVersionTTL)
	grokCLIVersionMu.Unlock()
	log.WithField("version", version).Info("fetched latest Grok CLI stable version")
}

// GrokCLILatestVersion returns the cached Grok CLI stable version. It falls back to
// the embedded release when the cache is empty or stale.
func GrokCLILatestVersion() string {
	grokCLIVersionMu.RLock()
	if cachedGrokCLIVersion != "" && time.Now().Before(grokCLIVersionExpiry) {
		version := cachedGrokCLIVersion
		grokCLIVersionMu.RUnlock()
		return version
	}
	grokCLIVersionMu.RUnlock()

	return GrokCLIFallbackVersion
}

func fetchGrokCLIVersion(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	client := &http.Client{Timeout: grokCLIFetchTimeout}
	primary, errPrimary := fetchGrokCLIVersionFromURL(ctx, client, grokCLIStableVersionURL)
	if errPrimary == nil {
		return primary, nil
	}

	registry, errRegistry := fetchGrokCLIVersionFromURL(ctx, client, grokCLIRegistryVersionURL)
	if errRegistry == nil {
		return registry, nil
	}

	return "", fmt.Errorf("primary feed: %v; registry feed: %v", errPrimary, errRegistry)
}

func fetchGrokCLIVersionFromURL(ctx context.Context, client *http.Client, url string) (string, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errReq != nil {
		return "", fmt.Errorf("build request: %w", errReq)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("fetch: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("Grok CLI version response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if errRead != nil {
		return "", fmt.Errorf("read body: %w", errRead)
	}

	version := strings.TrimSpace(gjson.GetBytes(data, "version").String())
	if version == "" {
		version = strings.TrimSpace(string(data))
	}
	if !IsValidGrokCLIVersion(version) {
		return "", errors.New("invalid or empty version")
	}
	return version, nil
}

// IsValidGrokCLIVersion accepts the X.Y.Z releases published by the Grok CLI feeds.
func IsValidGrokCLIVersion(version string) bool {
	if len(version) > 32 {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 8 {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}
