package cline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	ProviderCline                    = "cline"
	ProviderClinePass                = "cline-pass"
	APIBaseURL                       = "https://api.cline.bot/api/v1"
	CredentialSourceProviderSettings = "cline-provider-settings"
)

type ProviderSettingsFile struct {
	Providers map[string]ProviderEntry `json:"providers"`
}

type ProviderEntry struct {
	Settings ProviderSettings `json:"settings"`
	Auth     AuthSettings     `json:"auth"`
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
}

type ProviderSettings struct {
	Provider string       `json:"provider"`
	Model    string       `json:"model"`
	Auth     AuthSettings `json:"auth"`
}

type AuthSettings struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    any    `json:"expiresAt"`
	AccountID    string `json:"accountId"`
}

type providerAccessTokenCacheEntry struct {
	token     string
	modTime   time.Time
	expiresAt time.Time
}

var providerAccessTokenCache = struct {
	sync.RWMutex
	entries map[string]providerAccessTokenCacheEntry
}{
	entries: make(map[string]providerAccessTokenCacheEntry),
}

var (
	clineAPIBaseURL                 = APIBaseURL
	providerAccessTokenRefreshGroup singleflight.Group
)

type providerMatch struct {
	key      string
	provider ProviderSettings
}

type providerAccessTokenSnapshot struct {
	providerAccessTokenCacheEntry
	resolvedPath string
	matchKey     string
	auth         AuthSettings
}

type refreshResponse struct {
	Success *bool `json:"success"`
	Data    struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    any    `json:"expiresAt"`
		AccountID    string `json:"accountId"`
		UserInfo     struct {
			ClineUserID string `json:"clineUserId"`
			AccountID   string `json:"accountId"`
		} `json:"userInfo"`
	} `json:"data"`
	Code      string `json:"code"`
	ErrorCode string `json:"errorCode"`
	Error     string `json:"error"`
	Message   string `json:"message"`
}

func ParseProviderSettings(data []byte) (*ProviderSettingsFile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cline provider settings: empty data")
	}
	var settings ProviderSettingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	if len(settings.Providers) == 0 {
		return nil, fmt.Errorf("cline provider settings: no providers")
	}
	normalized := make(map[string]ProviderEntry, len(settings.Providers))
	for rawKey, entry := range settings.Providers {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if key == "" {
			return nil, fmt.Errorf("cline provider settings: empty provider key")
		}
		if _, exists := normalized[key]; exists {
			return nil, fmt.Errorf("cline provider settings: duplicate provider key %q", key)
		}
		normalized[key] = entry
	}
	settings.Providers = normalized
	return &settings, nil
}

func ProviderAuth(settings *ProviderSettingsFile, providerIDs ...string) (ProviderSettings, bool) {
	match, ok := findProviderAuth(settings, providerIDs...)
	if !ok {
		return ProviderSettings{}, false
	}
	return match.provider, true
}

func FindProvider(settings *ProviderSettingsFile, providerIDs ...string) (ProviderSettings, bool) {
	match, ok := findProvider(settings, providerIDs...)
	if !ok {
		return ProviderSettings{}, false
	}
	return match.provider, true
}

func findProvider(settings *ProviderSettingsFile, providerIDs ...string) (providerMatch, bool) {
	if settings == nil {
		return providerMatch{}, false
	}
	normalizedIDs := normalizeProviderIDs(providerIDs...)
	if len(normalizedIDs) == 0 {
		return providerMatch{}, false
	}
	for _, expectedProviderID := range normalizedIDs {
		if entry, ok := settings.Providers[expectedProviderID]; ok {
			provider := providerSettingsForEntry(expectedProviderID, entry)
			if providerIDForEntry(expectedProviderID, provider) == expectedProviderID {
				return providerMatch{key: expectedProviderID, provider: provider}, true
			}
		}
		for _, key := range sortedProviderKeys(settings.Providers) {
			if key == expectedProviderID {
				continue
			}
			provider := providerSettingsForEntry(key, settings.Providers[key])
			if providerIDForEntry(key, provider) == expectedProviderID {
				return providerMatch{key: key, provider: provider}, true
			}
		}
	}
	return providerMatch{}, false
}

func findProviderAuth(settings *ProviderSettingsFile, providerIDs ...string) (providerMatch, bool) {
	if settings == nil {
		return providerMatch{}, false
	}
	normalizedIDs := normalizeProviderIDs(providerIDs...)
	if len(normalizedIDs) == 0 {
		return providerMatch{}, false
	}
	for _, expectedProviderID := range normalizedIDs {
		for _, match := range providerMatches(settings, expectedProviderID) {
			if strings.TrimSpace(match.provider.Auth.AccessToken) != "" {
				return match, true
			}
		}
	}
	return providerMatch{}, false
}

func providerMatches(settings *ProviderSettingsFile, expectedProviderID string) []providerMatch {
	if settings == nil || strings.TrimSpace(expectedProviderID) == "" {
		return nil
	}
	matches := make([]providerMatch, 0, 1)
	if entry, ok := settings.Providers[expectedProviderID]; ok {
		provider := providerSettingsForEntry(expectedProviderID, entry)
		if providerIDForEntry(expectedProviderID, provider) == expectedProviderID {
			matches = append(matches, providerMatch{key: expectedProviderID, provider: provider})
		}
	}
	for _, key := range sortedProviderKeys(settings.Providers) {
		if key == expectedProviderID {
			continue
		}
		provider := providerSettingsForEntry(key, settings.Providers[key])
		if providerIDForEntry(key, provider) == expectedProviderID {
			matches = append(matches, providerMatch{key: key, provider: provider})
		}
	}
	return matches
}

func normalizeProviderIDs(providerIDs ...string) []string {
	normalized := make([]string, 0, len(providerIDs))
	seen := make(map[string]struct{}, len(providerIDs))
	for _, providerID := range providerIDs {
		providerID = strings.ToLower(strings.TrimSpace(providerID))
		if providerID == "" {
			continue
		}
		if _, exists := seen[providerID]; exists {
			continue
		}
		seen[providerID] = struct{}{}
		normalized = append(normalized, providerID)
	}
	return normalized
}

func providerSettingsForEntry(key string, entry ProviderEntry) ProviderSettings {
	provider := entry.Settings
	if strings.TrimSpace(provider.Provider) == "" && strings.TrimSpace(entry.Provider) != "" {
		provider.Provider = entry.Provider
	}
	if strings.TrimSpace(provider.Model) == "" && strings.TrimSpace(entry.Model) != "" {
		provider.Model = entry.Model
	}
	if strings.TrimSpace(provider.Provider) == "" {
		provider.Provider = strings.ToLower(strings.TrimSpace(key))
	}
	if strings.TrimSpace(provider.Auth.AccessToken) == "" && strings.TrimSpace(entry.Auth.AccessToken) != "" {
		provider.Auth = entry.Auth
	}
	return provider
}

func providerIDForEntry(key string, provider ProviderSettings) string {
	providerID := strings.ToLower(strings.TrimSpace(provider.Provider))
	if providerID == "" {
		providerID = strings.ToLower(strings.TrimSpace(key))
	}
	return providerID
}

func sortedProviderKeys(providers map[string]ProviderEntry) []string {
	keys := make([]string, 0, len(providers))
	for key := range providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ReadProviderAccessToken(path string, providerID string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("cline provider settings: missing path")
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return "", fmt.Errorf("cline provider settings: missing provider ID")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	cacheKey := path + "|" + providerID
	modTime := info.ModTime()
	providerAccessTokenCache.RLock()
	cached, ok := providerAccessTokenCache.entries[cacheKey]
	providerAccessTokenCache.RUnlock()
	if ok && cached.modTime.Equal(modTime) && !tokenExpiresSoon(cached.expiresAt, time.Now()) {
		return cached.token, nil
	}

	snapshot, err := readProviderAccessTokenSnapshot(path, providerID)
	if err != nil {
		return "", err
	}
	if tokenExpiresSoon(snapshot.expiresAt, time.Now()) && strings.TrimSpace(snapshot.auth.RefreshToken) != "" {
		value, errRefresh, _ := providerAccessTokenRefreshGroup.Do(cacheKey, func() (any, error) {
			return refreshProviderAccessToken(path, providerID)
		})
		if errRefresh != nil {
			return "", errRefresh
		}
		refreshed, ok := value.(providerAccessTokenCacheEntry)
		if !ok {
			return "", fmt.Errorf("cline provider settings: invalid refresh result")
		}
		return refreshed.token, nil
	}
	cacheProviderAccessToken(cacheKey, snapshot.providerAccessTokenCacheEntry)
	return snapshot.token, nil
}

func readProviderAccessTokenSnapshot(path string, providerID string) (providerAccessTokenSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return providerAccessTokenSnapshot{}, err
	}
	modTime := info.ModTime()
	resolvedPath := path
	if evaluated, errEval := filepath.EvalSymlinks(path); errEval == nil && strings.TrimSpace(evaluated) != "" {
		resolvedPath = evaluated
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return providerAccessTokenSnapshot{}, err
	}
	settings, err := ParseProviderSettings(data)
	if err != nil {
		return providerAccessTokenSnapshot{}, err
	}
	providerIDs := []string{providerID}
	if providerID == ProviderClinePass {
		providerIDs = []string{ProviderCline, ProviderClinePass}
	}
	match, ok := findProviderAuth(settings, providerIDs...)
	if !ok {
		return providerAccessTokenSnapshot{}, fmt.Errorf("cline provider settings: provider %q missing access token", providerID)
	}
	provider := match.provider
	token := strings.TrimSpace(provider.Auth.AccessToken)
	expiresAt := authExpiryTime(provider.Auth, token)
	return providerAccessTokenSnapshot{
		providerAccessTokenCacheEntry: providerAccessTokenCacheEntry{
			token:     token,
			modTime:   modTime,
			expiresAt: expiresAt,
		},
		resolvedPath: resolvedPath,
		matchKey:     match.key,
		auth:         provider.Auth,
	}, nil
}

func refreshProviderAccessToken(path string, providerID string) (providerAccessTokenCacheEntry, error) {
	snapshot, err := readProviderAccessTokenSnapshot(path, providerID)
	if err != nil {
		return providerAccessTokenCacheEntry{}, err
	}
	if !tokenExpiresSoon(snapshot.expiresAt, time.Now()) || strings.TrimSpace(snapshot.auth.RefreshToken) == "" {
		cacheProviderAccessToken(path+"|"+providerID, snapshot.providerAccessTokenCacheEntry)
		return snapshot.providerAccessTokenCacheEntry, nil
	}
	refreshed, err := refreshProviderAuth(snapshot.auth.RefreshToken)
	if err != nil {
		return providerAccessTokenCacheEntry{}, err
	}
	updated, err := updateStoredProviderAuth(snapshot.resolvedPath, snapshot.matchKey, snapshot.auth.RefreshToken, refreshed)
	if err != nil {
		return providerAccessTokenCacheEntry{}, err
	}
	if !updated {
		current, errCurrent := readProviderAccessTokenSnapshot(path, providerID)
		if errCurrent != nil {
			return providerAccessTokenCacheEntry{}, errCurrent
		}
		if tokenExpiresSoon(current.expiresAt, time.Now()) {
			return providerAccessTokenCacheEntry{}, fmt.Errorf("cline provider settings: provider auth changed during refresh")
		}
		cacheProviderAccessToken(path+"|"+providerID, current.providerAccessTokenCacheEntry)
		return current.providerAccessTokenCacheEntry, nil
	}
	token := strings.TrimSpace(refreshed.AccessToken)
	expiresAt := authExpiryTime(refreshed, token)
	modTime := snapshot.modTime
	if refreshedInfo, errStat := os.Stat(path); errStat == nil {
		modTime = refreshedInfo.ModTime()
	}
	result := providerAccessTokenCacheEntry{token: token, modTime: modTime, expiresAt: expiresAt}
	cacheProviderAccessToken(path+"|"+providerID, result)
	return result, nil
}

func cacheProviderAccessToken(cacheKey string, entry providerAccessTokenCacheEntry) {
	providerAccessTokenCache.Lock()
	providerAccessTokenCache.entries[cacheKey] = entry
	providerAccessTokenCache.Unlock()
}

func tokenExpiresSoon(expiresAt time.Time, now time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return !expiresAt.After(now.Add(time.Minute))
}

func authExpiryTime(auth AuthSettings, token string) time.Time {
	if expiresAt, ok := parseExpiryTime(auth.ExpiresAt); ok {
		return expiresAt
	}
	if expiresAt, ok := parseJWTExpiry(stripClineAccountAccessTokenPrefix(token)); ok {
		return expiresAt
	}
	return time.Time{}
}

func parseExpiryTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case nil:
		return time.Time{}, false
	case float64:
		return timeFromNumericExpiry(v)
	case int64:
		return timeFromNumericExpiry(float64(v))
	case int:
		return timeFromNumericExpiry(float64(v))
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return time.Time{}, false
		}
		return timeFromNumericExpiry(n)
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return time.Time{}, false
		}
		if n, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return timeFromNumericExpiry(n)
		}
		parsed, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	default:
		return time.Time{}, false
	}
}

func timeFromNumericExpiry(value float64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value > 10000000000 {
		return time.UnixMilli(int64(value)), true
	}
	return time.Unix(int64(value), 0), true
}

func parseJWTExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || strings.TrimSpace(parts[1]) == "" {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp any `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	return parseExpiryTime(claims.Exp)
}

func stripClineAccountAccessTokenPrefix(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "workos:") {
		return token[len("workos:"):]
	}
	return token
}

func formatClineAccountAccessToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" || strings.HasPrefix(strings.ToLower(token), "workos:") {
		return token
	}
	return "workos:" + token
}

func refreshProviderAuth(refreshToken string) (AuthSettings, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return AuthSettings{}, fmt.Errorf("cline provider settings: missing refresh token")
	}
	body, err := json.Marshal(map[string]string{
		"grantType":    "refresh_token",
		"refreshToken": refreshToken,
	})
	if err != nil {
		return AuthSettings{}, err
	}
	url := strings.TrimRight(clineAPIBaseURL, "/") + "/auth/refresh"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return AuthSettings{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return AuthSettings{}, fmt.Errorf("cline provider settings: refresh token request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload refreshResponse
	_ = json.Unmarshal(respBody, &payload)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || (payload.Success != nil && !*payload.Success) {
		return AuthSettings{}, fmt.Errorf("cline provider settings: refresh token request failed: %s", safeRefreshErrorDetail(resp.StatusCode, payload))
	}
	accessToken := strings.TrimSpace(payload.Data.AccessToken)
	if accessToken == "" {
		return AuthSettings{}, fmt.Errorf("cline provider settings: refresh token response missing access token")
	}
	accountID := strings.TrimSpace(payload.Data.AccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(payload.Data.UserInfo.ClineUserID)
	}
	if accountID == "" {
		accountID = strings.TrimSpace(payload.Data.UserInfo.AccountID)
	}
	refreshed := AuthSettings{
		AccessToken:  formatClineAccountAccessToken(accessToken),
		RefreshToken: strings.TrimSpace(payload.Data.RefreshToken),
		ExpiresAt:    payload.Data.ExpiresAt,
		AccountID:    accountID,
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = refreshToken
	}
	return refreshed, nil
}

func safeRefreshErrorDetail(status int, payload refreshResponse) string {
	for _, value := range []string{payload.Code, payload.ErrorCode, payload.Error, payload.Message} {
		value = strings.TrimSpace(value)
		if isSafeErrorCode(value) {
			return value
		}
	}
	return fmt.Sprintf("HTTP %d", status)
}

func isSafeErrorCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func updateStoredProviderAuth(path string, providerKey string, expectedRefreshToken string, refreshed AuthSettings) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return false, err
	}
	providers, ok := root["providers"].(map[string]any)
	if !ok {
		return false, fmt.Errorf("cline provider settings: providers object missing")
	}
	entry, ok := providers[providerKey].(map[string]any)
	if !ok {
		return false, fmt.Errorf("cline provider settings: provider %q missing during refresh", providerKey)
	}
	authTarget := providerAuthTarget(entry)
	if authTarget == nil {
		settings, _ := entry["settings"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
			entry["settings"] = settings
		}
		authTarget = map[string]any{}
		settings["auth"] = authTarget
	}
	if currentRefresh, _ := authTarget["refreshToken"].(string); strings.TrimSpace(currentRefresh) != "" && strings.TrimSpace(currentRefresh) != expectedRefreshToken {
		return false, nil
	}
	authTarget["accessToken"] = refreshed.AccessToken
	authTarget["refreshToken"] = refreshed.RefreshToken
	authTarget["expiresAt"] = refreshed.ExpiresAt
	if strings.TrimSpace(refreshed.AccountID) != "" {
		authTarget["accountId"] = refreshed.AccountID
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	updated = append(updated, '\n')
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	tmpPath := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, updated, info.Mode().Perm()); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	return true, nil
}

func providerAuthTarget(entry map[string]any) map[string]any {
	if settings, _ := entry["settings"].(map[string]any); settings != nil {
		if auth, _ := settings["auth"].(map[string]any); auth != nil {
			return auth
		}
	}
	if auth, _ := entry["auth"].(map[string]any); auth != nil {
		return auth
	}
	return nil
}
