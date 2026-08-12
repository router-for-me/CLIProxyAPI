package configaccess

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

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
		newProvider(sdkaccess.DefaultAccessProviderName, keys, cfg.APIKeyMetadata),
	)
}

type clientKey struct {
	id    string
	alias string
}

type provider struct {
	name string
	keys map[string]clientKey
}

func newProvider(name string, keys []string, metadata map[string]sdkconfig.ClientAPIKeyMetadata) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultAccessProviderName
	}
	normalizedMetadata := normalizeMetadata(metadata)
	keySet := make(map[string]clientKey, len(keys))
	seenIDs := make(map[string]struct{}, len(keys))
	seenRawKeys := make(map[string]struct{}, len(keys))
	normalizedKeys := make([]string, 0, len(keys))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		if _, duplicate := seenRawKeys[key]; duplicate {
			continue
		}
		seenRawKeys[key] = struct{}{}
		normalizedKeys = append(normalizedKeys, key)
	}
	replacements := make([]string, 0, len(normalizedKeys)*2)
	for _, key := range normalizedKeys {
		replacements = append(replacements, key, "")
	}
	credentialRedactor := strings.NewReplacer(replacements...)
	for _, key := range normalizedKeys {
		entry := normalizedMetadata[key]
		clientKeyID := strings.TrimSpace(entry.ID)
		if clientKeyID != "" && credentialRedactor.Replace(clientKeyID) != clientKeyID {
			clientKeyID = ""
		}
		if clientKeyID == "" {
			clientKeyID = sdkaccess.FallbackClientKeyID(key)
		}
		if _, duplicate := seenIDs[clientKeyID]; duplicate {
			clientKeyID = sdkaccess.FallbackClientKeyID(key)
			if _, fallbackDuplicate := seenIDs[clientKeyID]; fallbackDuplicate {
				clientKeyID = uniqueClientKeyID(clientKeyID, seenIDs)
			}
		}
		seenIDs[clientKeyID] = struct{}{}
		if entry.Disabled {
			continue
		}
		alias := strings.TrimSpace(entry.Alias)
		if alias != "" && credentialRedactor.Replace(alias) != alias {
			alias = ""
		}
		alias = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, alias)
		aliasRunes := []rune(alias)
		if len(aliasRunes) > 128 {
			alias = string(aliasRunes[:128])
		}
		keySet[key] = clientKey{
			id:    clientKeyID,
			alias: alias,
		}
	}
	return &provider{name: providerName, keys: keySet}
}

func uniqueClientKeyID(base string, seen map[string]struct{}) string {
	for suffix := 2; ; suffix++ {
		candidate := base + "_" + strconv.Itoa(suffix)
		if _, exists := seen[candidate]; !exists {
			return candidate
		}
	}
}

func normalizeMetadata(metadata map[string]sdkconfig.ClientAPIKeyMetadata) map[string]sdkconfig.ClientAPIKeyMetadata {
	if len(metadata) == 0 {
		return nil
	}
	metadataKeys := make([]string, 0, len(metadata))
	for apiKey := range metadata {
		metadataKeys = append(metadataKeys, apiKey)
	}
	sort.Slice(metadataKeys, func(i, j int) bool {
		trimmedI := strings.TrimSpace(metadataKeys[i])
		trimmedJ := strings.TrimSpace(metadataKeys[j])
		if trimmedI != trimmedJ {
			return trimmedI < trimmedJ
		}
		exactI := metadataKeys[i] == trimmedI
		exactJ := metadataKeys[j] == trimmedJ
		if exactI != exactJ {
			return exactI
		}
		return metadataKeys[i] < metadataKeys[j]
	})

	normalized := make(map[string]sdkconfig.ClientAPIKeyMetadata, len(metadata))
	for _, rawKey := range metadataKeys {
		apiKey := strings.TrimSpace(rawKey)
		if apiKey == "" {
			continue
		}
		if _, exists := normalized[apiKey]; exists {
			continue
		}
		normalized[apiKey] = metadata[rawKey]
	}
	return normalized
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
		if key, ok := p.keys[candidate.value]; ok {
			metadata := map[string]string{
				"source":                      candidate.source,
				sdkaccess.MetadataClientKeyID: key.id,
			}
			if key.alias != "" {
				metadata[sdkaccess.MetadataClientKeyAlias] = key.alias
			}
			return &sdkaccess.Result{
				Provider:  p.Identifier(),
				Principal: candidate.value,
				Metadata:  metadata,
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

func normalizeKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if _, exists := seen[trimmedKey]; exists {
			continue
		}
		seen[trimmedKey] = struct{}{}
		normalized = append(normalized, trimmedKey)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
