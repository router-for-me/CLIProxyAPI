package configaccess

import (
	"context"
	"net/http"
	"strings"
	"sync"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

var registerOnce sync.Once

// Register ensures the config-access provider is available to the access manager.
func Register() {
	registerOnce.Do(func() {
		sdkaccess.RegisterProvider(sdkconfig.AccessProviderTypeConfigAPIKey, newProvider)
	})
}

type provider struct {
	name string
	keys map[string]struct{}
}

func newProvider(cfg *sdkconfig.AccessProvider, _ *sdkconfig.SDKConfig) (sdkaccess.Provider, error) {
	name := cfg.Name
	if name == "" {
		name = sdkconfig.DefaultAccessProviderName
	}
	keys := make(map[string]struct{}, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		if key == "" {
			continue
		}
		keys[key] = struct{}{}
	}
	return &provider{name: name, keys: keys}, nil
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return sdkconfig.DefaultAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, error) {
	if p == nil {
		return nil, sdkaccess.ErrNotHandled
	}
	if len(p.keys) == 0 {
		return nil, sdkaccess.ErrNotHandled
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
		return nil, sdkaccess.ErrNoCredentials
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
		if _, ok := p.keys[candidate.value]; ok {
			return &sdkaccess.Result{
				Provider:  p.Identifier(),
				Principal: candidate.value,
				Metadata: map[string]string{
					"source": candidate.source,
				},
			}, nil
		}
	}

	return nil, sdkaccess.ErrInvalidCredential
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
