package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// CommandCodeTier represents the normalized subscription tier.
type CommandCodeTier string

const (
	CommandCodeTierUnknown CommandCodeTier = "unknown"
	CommandCodeTierGo      CommandCodeTier = "go"
	CommandCodeTierGoat    CommandCodeTier = "goat" // includes goat, pro, max, team, provider
)

var (
	commandCodeTierMu    sync.RWMutex
	commandCodeTierCache = make(map[string]commandCodeTierEntry)
)

type commandCodeTierEntry struct {
	tier      CommandCodeTier
	updatedAt time.Time
}

// CommandCodeTierRefreshInterval defines how often subscription tiers are silently refreshed.
var CommandCodeTierRefreshInterval = 1 * time.Hour

// SubscriptionsAPIURL is the commandcode billing subscriptions endpoint.
var SubscriptionsAPIURL = "https://api.commandcode.ai/alpha/billing/subscriptions"

type commandCodeSubResponse struct {
	Data struct {
		PlanID string `json:"planId"`
		Status string `json:"status"`
	} `json:"data"`
}

// NormalizeCommandCodePlan converts planId to CommandCodeTier.
func NormalizeCommandCodePlan(planID string) CommandCodeTier {
	p := strings.ToLower(strings.TrimSpace(planID))
	if strings.Contains(p, "goat") || strings.Contains(p, "pro") || strings.Contains(p, "max") || strings.Contains(p, "team") || strings.Contains(p, "provider") {
		return CommandCodeTierGoat
	}
	if strings.Contains(p, "go") {
		return CommandCodeTierGo
	}
	return CommandCodeTierGoat // Default unknown non-empty to goat to allow native attempts
}

// GetCommandCodeTier returns the cached tier for an apiKey. If not cached, returns CommandCodeTierUnknown.
func GetCommandCodeTier(apiKey string) CommandCodeTier {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return CommandCodeTierUnknown
	}
	commandCodeTierMu.RLock()
	defer commandCodeTierMu.RUnlock()
	if entry, ok := commandCodeTierCache[key]; ok {
		return entry.tier
	}
	return CommandCodeTierUnknown
}

// SetCommandCodeTier caches the tier for an apiKey.
func SetCommandCodeTier(apiKey string, tier CommandCodeTier) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return
	}
	commandCodeTierMu.Lock()
	defer commandCodeTierMu.Unlock()
	commandCodeTierCache[key] = commandCodeTierEntry{
		tier:      tier,
		updatedAt: time.Now(),
	}
}

// ClearCommandCodeTierCache clears the cache (for tests).
func ClearCommandCodeTierCache() {
	commandCodeTierMu.Lock()
	defer commandCodeTierMu.Unlock()
	commandCodeTierCache = make(map[string]commandCodeTierEntry)
}

// FetchCommandCodeTierHTTP fetches the tier from the billing API without generating errors.
func FetchCommandCodeTierHTTP(ctx context.Context, apiKey string, client *http.Client) (CommandCodeTier, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return CommandCodeTierUnknown, fmt.Errorf("empty api key")
	}
	if client == nil {
		client = http.DefaultClient
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, errNew := http.NewRequestWithContext(reqCtx, http.MethodGet, SubscriptionsAPIURL, nil)
	if errNew != nil {
		return CommandCodeTierUnknown, errNew
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-command-code-version", "1.36.0")
	req.Header.Set("x-cli-environment", "production")

	resp, errDo := client.Do(req)
	if errDo != nil {
		return CommandCodeTierUnknown, errDo
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CommandCodeTierUnknown, fmt.Errorf("billing endpoint status %d", resp.StatusCode)
	}

	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if errRead != nil {
		return CommandCodeTierUnknown, errRead
	}

	var parsed commandCodeSubResponse
	if errJSON := json.Unmarshal(data, &parsed); errJSON != nil {
		return CommandCodeTierUnknown, errJSON
	}

	tier := NormalizeCommandCodePlan(parsed.Data.PlanID)
	SetCommandCodeTier(key, tier)
	log.Debugf("commandcode tier silently synced for key ending in ...%s: %s (planId=%s)",
		tail4(key), tier, parsed.Data.PlanID)
	return tier, nil
}

// BackgroundSyncCommandCodeTier ensures a background sync is started or triggered for a key.
func BackgroundSyncCommandCodeTier(ctx context.Context, apiKey string, client *http.Client) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return
	}
	commandCodeTierMu.RLock()
	entry, ok := commandCodeTierCache[key]
	commandCodeTierMu.RUnlock()

	// If already cached within refresh interval, skip
	if ok && time.Since(entry.updatedAt) < CommandCodeTierRefreshInterval {
		return
	}

	go func() {
		_, err := FetchCommandCodeTierHTTP(ctx, key, client)
		if err != nil {
			log.Debugf("commandcode background tier sync failed for key ending in ...%s: %v", tail4(key), err)
		}
	}()
}

func tail4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}
