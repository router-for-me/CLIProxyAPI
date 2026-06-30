package cline

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ProviderCline                    = "cline"
	ProviderClinePass                = "cline-pass"
	APIBaseURL                       = "https://api.cline.bot/api/v1"
	CredentialSourceProviderSettings = "cline-provider-settings"
)

func IsProviderSettingsClinePassAttributes(attrs map[string]string) bool {
	if attrs == nil {
		return false
	}
	if strings.TrimSpace(attrs["credential_source"]) != CredentialSourceProviderSettings {
		return false
	}
	providerID := strings.TrimSpace(attrs["cline_provider"])
	if providerID == "" {
		providerID = strings.TrimSpace(attrs["compat_name"])
	}
	if !strings.EqualFold(providerID, ProviderClinePass) {
		return false
	}
	baseURL := strings.TrimRight(strings.TrimSpace(attrs["base_url"]), "/")
	return strings.EqualFold(baseURL, strings.TrimRight(APIBaseURL, "/"))
}

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
	AccessToken string `json:"accessToken"`
	ExpiresAt   any    `json:"expiresAt"`
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

type providerMatch struct {
	provider ProviderSettings
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
				return providerMatch{provider: provider}, true
			}
		}
		for _, key := range sortedProviderKeys(settings.Providers) {
			if key == expectedProviderID {
				continue
			}
			provider := providerSettingsForEntry(key, settings.Providers[key])
			if providerIDForEntry(key, provider) == expectedProviderID {
				return providerMatch{provider: provider}, true
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
			matches = append(matches, providerMatch{provider: provider})
		}
	}
	for _, key := range sortedProviderKeys(settings.Providers) {
		if key == expectedProviderID {
			continue
		}
		provider := providerSettingsForEntry(key, settings.Providers[key])
		if providerIDForEntry(key, provider) == expectedProviderID {
			matches = append(matches, providerMatch{provider: provider})
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

	entry, err := readProviderAccessTokenCacheEntry(path, providerID, modTime)
	if err != nil {
		return "", err
	}
	if tokenExpiresSoon(entry.expiresAt, time.Now()) {
		return "", fmt.Errorf("cline provider settings: provider %q access token expired", providerID)
	}
	cacheProviderAccessToken(cacheKey, entry)
	return entry.token, nil
}

func readProviderAccessTokenCacheEntry(path string, providerID string, modTime time.Time) (providerAccessTokenCacheEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return providerAccessTokenCacheEntry{}, err
	}
	settings, err := ParseProviderSettings(data)
	if err != nil {
		return providerAccessTokenCacheEntry{}, err
	}
	providerIDs := []string{providerID}
	if providerID == ProviderClinePass {
		providerIDs = []string{ProviderCline, ProviderClinePass}
	}
	match, ok := findProviderAuth(settings, providerIDs...)
	if !ok {
		return providerAccessTokenCacheEntry{}, fmt.Errorf("cline provider settings: provider %q missing access token", providerID)
	}
	token := strings.TrimSpace(match.provider.Auth.AccessToken)
	return providerAccessTokenCacheEntry{
		token:     token,
		modTime:   modTime,
		expiresAt: authExpiryTime(match.provider.Auth, token),
	}, nil
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
