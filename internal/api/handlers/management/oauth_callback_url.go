package management

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

var defaultOAuthCallbackProviderPaths = map[string]string{
	"anthropic":   "/oauth/callback/anthropic",
	"codex":       "/oauth/callback/codex",
	"gemini":      "/oauth/callback/gemini",
	"iflow":       "/oauth/callback/iflow",
	"antigravity": "/oauth/callback/antigravity",
}

func BuildOAuthRedirectURI(cfg *config.Config, provider, fallback string) (string, error) {
	if cfg == nil || !cfg.OAuthCallback.Enabled {
		return fallback, nil
	}

	base := strings.TrimSpace(cfg.OAuthCallback.ExternalBaseURL)
	if base == "" {
		return fallback, nil
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid oauth callback external base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid oauth callback external base url scheme: %s", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid oauth callback external base url host")
	}

	canonicalProvider, err := NormalizeOAuthProvider(provider)
	if err != nil {
		return "", err
	}

	path := ""
	if cfg.OAuthCallback.ProviderPaths != nil {
		path = strings.TrimSpace(cfg.OAuthCallback.ProviderPaths[canonicalProvider])
	}
	if path == "" {
		path = defaultOAuthCallbackProviderPaths[canonicalProvider]
	}
	if path == "" {
		return "", fmt.Errorf("unsupported oauth provider: %s", canonicalProvider)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	full := strings.TrimRight(base, "/") + path
	return full, nil
}
