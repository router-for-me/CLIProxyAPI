package configaccess

import (
	"context"
	"net/http"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// Register ensures the config-access provider is available to the access manager.
func Register(cfg *sdkconfig.SDKConfig) {
	if cfg == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	keys := normalizeKeys(cfg.APIKeys)
	if len(keys) == 0 {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	sdkaccess.RegisterProvider(
		sdkaccess.AccessProviderTypeConfigAPIKey,
		newProvider(sdkaccess.DefaultAccessProviderName, keys),
	)
}

type provider struct {
	name string
	// keys maps a raw API key to its display name; an empty name means the key
	// itself is used as the principal.
	keys map[string]string
}

func newProvider(name string, keys []sdkaccess.APIKeyEntry) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultAccessProviderName
	}
	keySet := make(map[string]string, len(keys))
	for _, entry := range keys {
		keySet[entry.Key] = entry.Name
	}
	return &provider{name: providerName, keys: keySet}
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return sdkaccess.DefaultAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if len(p.keys) == 0 {
		return nil, sdkaccess.NewNotHandledError()
	}
	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}

	apiKey := extractBearerToken(authHeader)

	candidates := []struct {
		value  string
		source string
	}{
		{apiKey, "authorization"},
		{authHeaderGoogle, "x-goog-api-key"},
		{authHeaderAnthropic, "x-api-key"},
		{queryKey, "query-key"},
		{queryAuthToken, "query-auth-token"},
	}

	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		if name, ok := p.keys[candidate.value]; ok {
			principal := name
			if principal == "" {
				principal = candidate.value
			}
			return &sdkaccess.Result{
				Provider:  p.Identifier(),
				Principal: principal,
				Metadata: map[string]string{
					"source": candidate.source,
				},
			}, nil
		}
	}

	return nil, sdkaccess.NewInvalidCredentialError()
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}

// normalizeKeys trims entries, drops empty keys and deduplicates by key while
// keeping the first occurrence. When the first occurrence carries no name, a
// later duplicate may still supply one.
func normalizeKeys(keys []sdkaccess.APIKeyEntry) []sdkaccess.APIKeyEntry {
	if len(keys) == 0 {
		return nil
	}
	normalized := make([]sdkaccess.APIKeyEntry, 0, len(keys))
	seen := make(map[string]int, len(keys))
	for _, entry := range keys {
		trimmedKey := strings.TrimSpace(entry.Key)
		if trimmedKey == "" {
			continue
		}
		trimmedName := strings.TrimSpace(entry.Name)
		if index, exists := seen[trimmedKey]; exists {
			if normalized[index].Name == "" && trimmedName != "" {
				normalized[index].Name = trimmedName
			}
			continue
		}
		seen[trimmedKey] = len(normalized)
		normalized = append(normalized, sdkaccess.APIKeyEntry{Key: trimmedKey, Name: trimmedName})
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
