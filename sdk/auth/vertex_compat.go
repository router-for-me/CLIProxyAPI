package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// LoadVertexCompatCredentials loads Vertex AI-compatible API key credentials from config
// into the auth manager. Each entry becomes an in-memory auth entry with API key and
// custom headers stored in attributes.
func LoadVertexCompatCredentials(ctx context.Context, cfg *config.Config, mgr *coreauth.Manager) error {
	if cfg == nil || mgr == nil {
		return nil
	}
	if len(cfg.VertexCompatAPIKey) == 0 {
		return nil
	}

	for i, entry := range cfg.VertexCompatAPIKey {
		apiKey := strings.TrimSpace(entry.APIKey)
		baseURL := strings.TrimSpace(entry.BaseURL)

		if apiKey == "" || baseURL == "" {
			continue
		}

		// Create unique ID from index and base URL
		id := fmt.Sprintf("vertex-compat-%d-%s", i, sanitizeForID(baseURL))
		label := fmt.Sprintf("Vertex-Compat (%s)", extractDomain(baseURL))

		// Build attributes map
		attrs := make(map[string]string)
		attrs["api_key"] = apiKey
		attrs["base_url"] = baseURL

		// Add proxy URL if specified
		if entry.ProxyURL != "" {
			attrs["proxy_url"] = strings.TrimSpace(entry.ProxyURL)
		}

		// Copy custom headers to attributes with "header_" prefix
		for k, v := range entry.Headers {
			headerKey := "header_" + strings.ToLower(strings.TrimSpace(k))
			attrs[headerKey] = strings.TrimSpace(v)
		}

		auth := &coreauth.Auth{
			ID:         id,
			Provider:   "vertex-compat",
			Label:      label,
			Attributes: attrs,
			Metadata:   make(map[string]any),
		}

		if _, err := mgr.Register(ctx, auth); err != nil {
			return fmt.Errorf("failed to register vertex-compat credential %d: %w", i, err)
		}
	}

	return nil
}

// sanitizeForID creates a safe ID component from a URL.
func sanitizeForID(url string) string {
	// Remove https:// and http://
	clean := strings.TrimPrefix(url, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	// Replace non-alphanumeric with dash
	clean = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, clean)
	// Limit length
	if len(clean) > 30 {
		clean = clean[:30]
	}
	return strings.Trim(clean, "-")
}

// extractDomain extracts the domain from a URL for display purposes.
func extractDomain(url string) string {
	clean := strings.TrimPrefix(url, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	if idx := strings.Index(clean, "/"); idx > 0 {
		clean = clean[:idx]
	}
	return clean
}
