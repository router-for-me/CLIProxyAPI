package executor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func resolveOAuthBaseURL(cfg *config.Config, channel, defaultBaseURL string, auth *cliproxyauth.Auth) string {
	return resolveOAuthBaseURLWithOverride(cfg, channel, defaultBaseURL, authBaseURL(auth))
}

func resolveOAuthBaseURLWithOverride(cfg *config.Config, channel, defaultBaseURL, authBaseURLOverride string) string {
	if custom := strings.TrimSpace(authBaseURLOverride); custom != "" {
		return strings.TrimRight(custom, "/")
	}
	if cfg != nil {
		if custom := strings.TrimSpace(cfg.OAuthUpstreamURL(channel)); custom != "" {
			return strings.TrimRight(custom, "/")
		}
	}
	return strings.TrimRight(strings.TrimSpace(defaultBaseURL), "/")
}

func authBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return v
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
