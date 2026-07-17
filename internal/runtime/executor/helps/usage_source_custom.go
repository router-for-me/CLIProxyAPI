package helps

import (
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// resolveUsageSource returns a safe display dimension for dashboards.
// Prefer the upstream provider base URL and never return raw API keys.
func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {
	_ = ctxAPIKey
	if auth == nil {
		return ""
	}

	provider := strings.TrimSpace(auth.Provider)
	if base := authBaseURL(auth); base != "" {
		return sanitizeUsageSource(base)
	}

	if strings.EqualFold(provider, "vertex") && auth.Metadata != nil {
		if projectID, ok := auth.Metadata["project_id"].(string); ok {
			if trimmed := strings.TrimSpace(projectID); trimmed != "" {
				return sanitizeUsageSource(trimmed)
			}
		}
		if project, ok := auth.Metadata["project"].(string); ok {
			if trimmed := strings.TrimSpace(project); trimmed != "" {
				return sanitizeUsageSource(trimmed)
			}
		}
	}

	if kind, value := auth.AccountInfo(); kind == cliproxyauth.AuthKindOAuth {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return sanitizeUsageSource(trimmed)
		}
	}
	if auth.Metadata != nil {
		if email, ok := auth.Metadata["email"].(string); ok {
			if trimmed := strings.TrimSpace(email); trimmed != "" {
				return sanitizeUsageSource(trimmed)
			}
		}
	}

	if defaultURL := defaultProviderBaseURL(provider); defaultURL != "" {
		return defaultURL
	}
	if provider != "" {
		return sanitizeUsageSource(provider)
	}
	return ""
}

func authBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		for _, key := range []string{"base_url", "baseURL", "endpoint", "api_base"} {
			if base := strings.TrimSpace(auth.Attributes[key]); base != "" {
				return base
			}
		}
	}
	type baseURLGetter interface {
		GetBaseURL() string
	}
	if getter, ok := auth.Runtime.(baseURLGetter); ok && getter != nil {
		if base := strings.TrimSpace(getter.GetBaseURL()); base != "" {
			return base
		}
	}
	return ""
}

func defaultProviderBaseURL(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return "https://chatgpt.com/backend-api/codex"
	case "claude", "anthropic":
		return "https://api.anthropic.com"
	case "gemini", "aistudio":
		return "https://generativelanguage.googleapis.com"
	case "vertex":
		return "https://aiplatform.googleapis.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "xai":
		return "https://api.x.ai/v1"
	default:
		return ""
	}
}

func sanitizeUsageSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "sk-") ||
		strings.HasPrefix(lower, "sk_") ||
		strings.HasPrefix(lower, "rk-") ||
		strings.HasPrefix(lower, "xai-") ||
		strings.HasPrefix(lower, "bearer ") {
		return ""
	}
	if !strings.Contains(value, "://") && !strings.Contains(value, ".") && len(value) >= 32 {
		return ""
	}
	if !strings.Contains(value, "://") && !strings.Contains(value, "@") && !strings.Contains(value, ".") && len(value) >= 24 {
		secretish := true
		for _, r := range value {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				continue
			}
			secretish = false
			break
		}
		if secretish {
			return ""
		}
	}
	return value
}
