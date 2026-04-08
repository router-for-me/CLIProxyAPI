package configaccess

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
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
	keys map[string]*keyState
}

type keyState struct {
	requestsPerSecond int
	limiter           *tokenBucketLimiter
}

type tokenBucketLimiter struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	lastSeen time.Time
}

func newTokenBucketLimiter(requestsPerSecond int) *tokenBucketLimiter {
	rate := float64(requestsPerSecond)
	now := time.Now()
	return &tokenBucketLimiter{
		rate:     rate,
		burst:    rate,
		tokens:   rate,
		lastSeen: now,
	}
}

func (l *tokenBucketLimiter) Allow(now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.IsZero() {
		now = time.Now()
	}
	if l.lastSeen.IsZero() {
		l.lastSeen = now
	}

	elapsed := now.Sub(l.lastSeen).Seconds()
	if elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.lastSeen = now
	}

	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}

	missing := 1 - l.tokens
	retryAfter := time.Duration((missing / l.rate) * float64(time.Second))
	if retryAfter < time.Millisecond {
		retryAfter = time.Millisecond
	}
	return false, retryAfter
}

func newProvider(name string, keys []sdkconfig.APIKeyEntry) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultAccessProviderName
	}

	keySet := make(map[string]*keyState, len(keys))
	for _, key := range keys {
		keySet[key.APIKey] = &keyState{
			requestsPerSecond: key.RequestsPerSecond,
			limiter:           newTokenBucketLimiter(key.RequestsPerSecond),
		}
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

	now := time.Now()
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		state, ok := p.keys[candidate.value]
		if !ok {
			continue
		}
		if allowed, retryAfter := state.limiter.Allow(now); !allowed {
			message := fmt.Sprintf(
				"API key rate limit exceeded: %d requests per second allowed, retry in %s",
				state.requestsPerSecond,
				retryAfter.Round(time.Millisecond),
			)
			return nil, sdkaccess.NewRateLimitedAuthError(message)
		}
		return &sdkaccess.Result{
			Provider:  p.Identifier(),
			Principal: candidate.value,
			Metadata: map[string]string{
				"source":              candidate.source,
				"requests_per_second": fmt.Sprintf("%d", state.requestsPerSecond),
			},
		}, nil
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

func normalizeKeys(keys []sdkconfig.APIKeyEntry) []sdkconfig.APIKeyEntry {
	return sdkconfig.NormalizeAPIKeyEntries(keys)
}
