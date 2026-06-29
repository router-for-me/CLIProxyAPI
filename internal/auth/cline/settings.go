package cline

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	ProviderClinePass                = "cline-pass"
	APIBaseURL                       = "https://api.cline.bot/api/v1"
	CredentialSourceProviderSettings = "cline-provider-settings"
)

type ProviderSettingsFile struct {
	Providers map[string]ProviderEntry `json:"providers"`
}

type ProviderEntry struct {
	Settings ProviderSettings `json:"settings"`
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
	token   string
	modTime time.Time
}

var providerAccessTokenCache = struct {
	sync.RWMutex
	entries map[string]providerAccessTokenCacheEntry
}{
	entries: make(map[string]providerAccessTokenCacheEntry),
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

func ProviderAuth(settings *ProviderSettingsFile, providerID string) (ProviderSettings, bool) {
	if settings == nil {
		return ProviderSettings{}, false
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return ProviderSettings{}, false
	}
	entry, ok := settings.Providers[providerID]
	if !ok {
		return ProviderSettings{}, false
	}
	provider := strings.ToLower(strings.TrimSpace(entry.Settings.Provider))
	if provider != "" && provider != providerID {
		return ProviderSettings{}, false
	}
	if strings.TrimSpace(entry.Settings.Auth.AccessToken) == "" {
		return ProviderSettings{}, false
	}
	return entry.Settings, true
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
	if ok && cached.modTime.Equal(modTime) {
		return cached.token, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	settings, err := ParseProviderSettings(data)
	if err != nil {
		return "", err
	}
	provider, ok := ProviderAuth(settings, providerID)
	if !ok {
		return "", fmt.Errorf("cline provider settings: provider %q missing access token", providerID)
	}
	token := strings.TrimSpace(provider.Auth.AccessToken)
	providerAccessTokenCache.Lock()
	providerAccessTokenCache.entries[cacheKey] = providerAccessTokenCacheEntry{token: token, modTime: modTime}
	providerAccessTokenCache.Unlock()
	return token, nil
}
