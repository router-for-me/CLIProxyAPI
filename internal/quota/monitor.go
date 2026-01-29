// Package quota provides quota monitoring functionality for Antigravity accounts.
// It automatically disables models when their quota usage reaches a threshold.
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultCheckInterval is the default interval for checking quota.
	DefaultCheckInterval = 1 * time.Minute

	// DefaultDisableThresholdPercent is the default threshold in percent (80 = 80% used).
	DefaultDisableThresholdPercent = 80

	// DefaultDisableThreshold is the default threshold (0.2 = 80% used).
	// When remainingFraction <= 0.2, the model is disabled.
	DefaultDisableThreshold = 0.2

	// fetchModelsPath is the API path for fetching available models with quota info.
	fetchModelsPath = "/v1internal:fetchAvailableModels"
)

// AntigravityQuotaGroup defines a group of models that share quota.
type AntigravityQuotaGroup struct {
	Label  string
	Models []string
}

// Default quota groups based on the management panel configuration.
var DefaultQuotaGroups = []AntigravityQuotaGroup{
	{Label: "Claude/GPT", Models: []string{"claude-sonnet-4-5-thinking", "claude-opus-4-5-thinking", "claude-sonnet-4-5", "gpt-oss-120b-medium"}},
	{Label: "Gemini 3 Pro", Models: []string{"gemini-3-pro-high", "gemini-3-pro-low"}},
	{Label: "Gemini 2.5 Flash", Models: []string{"gemini-2.5-flash", "gemini-2.5-flash-thinking"}},
	{Label: "Gemini 2.5 Flash Lite", Models: []string{"gemini-2.5-flash-lite"}},
	{Label: "Gemini 3 Flash", Models: []string{"gemini-3-flash"}},
	{Label: "Gemini 3 Pro Image", Models: []string{"gemini-3-pro-image"}},
}

// QuotaInfo represents quota information for a model.
type QuotaInfo struct {
	RemainingFraction float64 `json:"remainingFraction"`
}

// ModelQuotaEntry represents a model entry in the API response.
type ModelQuotaEntry struct {
	Name      string     `json:"name"`
	QuotaInfo *QuotaInfo `json:"quotaInfo"`
}

// FetchModelsResponse represents the response from fetchAvailableModels API.
type FetchModelsResponse struct {
	Models []ModelQuotaEntry `json:"models"`
}

// Monitor watches Antigravity account quotas and disables models when threshold is reached.
type Monitor struct {
	cfg            *config.Config
	authManager    *coreauth.Manager
	checkInterval  time.Duration
	threshold      float64
	httpClient     *http.Client
	stopCh         chan struct{}
	wg             sync.WaitGroup
	mu             sync.Mutex
	running        bool
	quotaGroups    []AntigravityQuotaGroup
	onModelDisable func(authID, modelName, groupLabel string, usedPercent float64)
}

// NewMonitor creates a new quota monitor.
func NewMonitor(cfg *config.Config, authManager *coreauth.Manager) *Monitor {
	checkInterval := DefaultCheckInterval
	thresholdPercent := DefaultDisableThresholdPercent

	if cfg != nil {
		// Apply config overrides
		if cfg.QuotaMonitor.CheckIntervalSeconds > 0 {
			checkInterval = time.Duration(cfg.QuotaMonitor.CheckIntervalSeconds) * time.Second
		}
		if cfg.QuotaMonitor.DisableThresholdPercent > 0 && cfg.QuotaMonitor.DisableThresholdPercent <= 100 {
			thresholdPercent = cfg.QuotaMonitor.DisableThresholdPercent
		}
	}

	// Convert percent to remaining fraction threshold
	// e.g., 80% used means 20% remaining, so threshold = 0.2
	threshold := float64(100-thresholdPercent) / 100.0

	return &Monitor{
		cfg:           cfg,
		authManager:   authManager,
		checkInterval: checkInterval,
		threshold:     threshold,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		quotaGroups: DefaultQuotaGroups,
	}
}

// SetCheckInterval sets the interval between quota checks.
func (m *Monitor) SetCheckInterval(interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if interval > 0 {
		m.checkInterval = interval
	}
}

// SetThreshold sets the disable threshold (0.0 to 1.0).
// For example, 0.2 means disable when 80% quota is used.
func (m *Monitor) SetThreshold(threshold float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if threshold > 0 && threshold <= 1 {
		m.threshold = threshold
	}
}

// SetOnModelDisable sets a callback that is called when a model is disabled.
func (m *Monitor) SetOnModelDisable(fn func(authID, modelName, groupLabel string, usedPercent float64)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onModelDisable = fn
}

// Start begins the quota monitoring loop.
func (m *Monitor) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	interval := m.checkInterval
	threshold := m.threshold
	m.mu.Unlock()

	m.wg.Add(1)
	go m.monitorLoop(ctx)

	thresholdPercent := int((1 - threshold) * 100)
	log.WithFields(log.Fields{
		"interval":          interval.String(),
		"threshold_percent": thresholdPercent,
	}).Info("quota monitor started")
}

// Stop stops the quota monitoring loop.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stopCh)
	m.mu.Unlock()

	m.wg.Wait()
	log.Info("quota monitor stopped")
}

func (m *Monitor) monitorLoop(ctx context.Context) {
	defer m.wg.Done()

	// Run immediately on start
	m.checkAllAntigravityAccounts(ctx)

	m.mu.Lock()
	interval := m.checkInterval
	m.mu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkAllAntigravityAccounts(ctx)

			// Check if interval changed and reset ticker if needed
			m.mu.Lock()
			newInterval := m.checkInterval
			m.mu.Unlock()

			if newInterval != interval {
				ticker.Reset(newInterval)
				interval = newInterval
				log.Infof("quota monitor check interval updated to %s", interval)
			}
		}
	}
}

func (m *Monitor) checkAllAntigravityAccounts(ctx context.Context) {
	if m.authManager == nil {
		return
	}

	auths := m.authManager.List()
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		default:
		}

		if err := m.checkAccountQuota(ctx, auth); err != nil {
			log.WithError(err).WithField("auth_id", auth.ID).Debug("failed to check antigravity quota")
		}
	}
}

func (m *Monitor) checkAccountQuota(ctx context.Context, auth *coreauth.Auth) error {
	accessToken, err := m.ensureAccessToken(ctx, auth)
	if err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}

	models, err := m.fetchAvailableModels(ctx, accessToken, auth)
	if err != nil {
		return fmt.Errorf("fetch models: %w", err)
	}

	m.mu.Lock()
	threshold := m.threshold
	groups := m.quotaGroups
	callback := m.onModelDisable
	m.mu.Unlock()

	modelQuotaMap := make(map[string]float64)
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" || model.QuotaInfo == nil {
			continue
		}
		modelQuotaMap[name] = model.QuotaInfo.RemainingFraction
	}

	for _, group := range groups {
		for _, modelName := range group.Models {
			remaining, exists := modelQuotaMap[modelName]
			if !exists {
				continue
			}

			if remaining <= threshold {
				usedPercent := (1 - remaining) * 100
				m.disableModelForAuth(ctx, auth, modelName, group.Label, usedPercent)
				if callback != nil {
					callback(auth.ID, modelName, group.Label, usedPercent)
				}
			}
		}
	}

	return nil
}

func (m *Monitor) disableModelForAuth(ctx context.Context, auth *coreauth.Auth, modelName, groupLabel string, usedPercent float64) {
	if auth == nil || m.authManager == nil {
		return
	}

	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*coreauth.ModelState)
	}

	state, exists := auth.ModelStates[modelName]
	if !exists {
		state = &coreauth.ModelState{}
		auth.ModelStates[modelName] = state
	}

	if state.Unavailable {
		return
	}

	state.Unavailable = true
	state.Status = coreauth.StatusDisabled
	state.StatusMessage = fmt.Sprintf("auto-disabled: quota usage %.1f%% (group: %s)", usedPercent, groupLabel)
	state.UpdatedAt = time.Now()

	auth.UpdatedAt = time.Now()
	if _, err := m.authManager.Update(ctx, auth); err != nil {
		log.WithError(err).WithField("auth_id", auth.ID).WithField("model", modelName).Error("failed to update auth after disabling model")
		return
	}

	log.WithFields(log.Fields{
		"auth_id":      auth.ID,
		"model":        modelName,
		"group":        groupLabel,
		"used_percent": fmt.Sprintf("%.1f%%", usedPercent),
	}).Info("model auto-disabled due to quota threshold")
}

func (m *Monitor) ensureAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if auth == nil || auth.Metadata == nil {
		return "", fmt.Errorf("auth metadata is nil")
	}

	current := m.tokenFromMetadata(auth.Metadata)
	if current != "" && !m.tokenNeedsRefresh(auth.Metadata) {
		return current, nil
	}

	refreshToken, ok := auth.Metadata["refresh_token"].(string)
	if !ok || strings.TrimSpace(refreshToken) == "" {
		return "", fmt.Errorf("refresh token not found")
	}

	form := url.Values{}
	form.Set("client_id", antigravity.ClientID)
	form.Set("client_secret", antigravity.ClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravity.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token refresh failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}

	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", fmt.Errorf("empty access token in response")
	}

	now := time.Now()
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	if tokenResp.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = tokenResp.ExpiresIn
		auth.Metadata["timestamp"] = now.UnixMilli()
		auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	auth.LastRefreshedAt = now
	auth.UpdatedAt = now
	if m.authManager != nil {
		if _, err := m.authManager.Update(ctx, auth); err != nil {
			log.WithError(err).WithField("auth_id", auth.ID).Warn("failed to persist refreshed token")
		}
	}

	return tokenResp.AccessToken, nil
}

func (m *Monitor) fetchAvailableModels(ctx context.Context, accessToken string, auth *coreauth.Auth) ([]ModelQuotaEntry, error) {
	apiURL := antigravity.APIEndpoint + fetchModelsPath

	reqBody := `{}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravity.APIUserAgent)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch models failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var response FetchModelsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return response.Models, nil
}

func (m *Monitor) tokenFromMetadata(metadata map[string]any) string {
	if v, ok := metadata["access_token"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (m *Monitor) tokenNeedsRefresh(metadata map[string]any) bool {
	const skew = 30 * time.Second

	if expStr, ok := metadata["expired"].(string); ok {
		if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(expStr)); err == nil {
			return !ts.After(time.Now().Add(skew))
		}
	}

	expiresIn := int64Value(metadata["expires_in"])
	timestampMs := int64Value(metadata["timestamp"])
	if expiresIn > 0 && timestampMs > 0 {
		exp := time.UnixMilli(timestampMs).Add(time.Duration(expiresIn) * time.Second)
		return !exp.After(time.Now().Add(skew))
	}

	return true
}

func int64Value(raw any) int64 {
	switch v := raw.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float32:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
	}
	return 0
}
