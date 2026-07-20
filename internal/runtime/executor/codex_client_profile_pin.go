package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const codexClientProfilePinnedMetadataKey = "codex_client_profile_pinned"

var codexPinnedClientProfileHeaders = []string{
	"X-Codex-Beta-Features",
	"Version",
	"User-Agent",
	"Originator",
	"x-responsesapi-include-timing-metrics",
}

func codexPinClientProfileFromFirstRequest(_ context.Context, auth *cliproxyauth.Auth, target http.Header, source http.Header, cfg *config.Config) {
	if auth == nil || (target == nil && source == nil) || codexClientProfilePinned(auth) {
		return
	}

	codexEnsureAuthMetadata(auth)
	auth.Metadata[codexClientProfilePinnedMetadataKey] = true

	for _, headerName := range codexPinnedClientProfileHeaders {
		if codexAuthHeaderFixed(auth, headerName) {
			continue
		}
		value := firstNonEmptyHeaderValue(target, source, headerName)
		if strings.EqualFold(headerName, "User-Agent") {
			if cfgUserAgent, _ := codexHeaderDefaults(cfg, auth); headerValueCaseInsensitive(target, headerName) == "" && cfgUserAgent != "" {
				value = cfgUserAgent
			}
		}
		if value == "" {
			continue
		}
		codexSetAuthMetadataHeader(auth, headerName, value)
		codexSetAuthAttribute(auth, "header:"+headerName, value)
	}
}

func codexClientProfilePinned(auth *cliproxyauth.Auth) bool {
	if auth == nil || len(auth.Metadata) == 0 {
		return false
	}
	pinned, _ := auth.Metadata[codexClientProfilePinnedMetadataKey].(bool)
	return pinned
}

func codexClientProfileSourceHeaders(auth *cliproxyauth.Auth, source http.Header) http.Header {
	if codexClientProfilePinned(auth) {
		return nil
	}
	return source
}

func codexPreparePinnedClientProfileHeaders(headers http.Header, auth *cliproxyauth.Auth) {
	if headers == nil || !codexClientProfilePinned(auth) {
		return
	}
	for _, headerName := range codexPinnedClientProfileHeaders {
		if !codexAuthHeaderFixed(auth, headerName) {
			headers.Del(headerName)
		}
	}
}

func codexEnsureAuthMetadata(auth *cliproxyauth.Auth) {
	if auth != nil && auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
}

func codexSetAuthAttribute(auth *cliproxyauth.Auth, key string, value string) {
	if auth == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[key] = value
}

func codexAuthHeaderFixed(auth *cliproxyauth.Auth, name string) bool {
	name = strings.TrimSpace(name)
	if auth == nil || name == "" {
		return false
	}
	if len(auth.Attributes) > 0 {
		for key, value := range auth.Attributes {
			headerName, ok := strings.CutPrefix(key, "header:")
			if !ok {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(headerName), name) && strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	return codexMetadataHeaderValue(auth.Metadata, name) != ""
}

func codexMetadataHeaderValue(metadata map[string]any, name string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata["headers"]
	if !ok || raw == nil {
		return ""
	}
	switch headers := raw.(type) {
	case map[string]any:
		for key, value := range headers {
			if !strings.EqualFold(strings.TrimSpace(key), name) {
				continue
			}
			if typed, ok := value.(string); ok {
				return strings.TrimSpace(typed)
			}
		}
	case map[string]string:
		for key, value := range headers {
			if strings.EqualFold(strings.TrimSpace(key), name) {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func codexSetAuthMetadataHeader(auth *cliproxyauth.Auth, name string, value string) {
	if auth == nil {
		return
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" || value == "" {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	headers, ok := auth.Metadata["headers"].(map[string]any)
	if !ok || headers == nil {
		headers = make(map[string]any)
		if existing, okExisting := auth.Metadata["headers"].(map[string]string); okExisting {
			for key, existingValue := range existing {
				if strings.TrimSpace(key) != "" && strings.TrimSpace(existingValue) != "" {
					headers[key] = strings.TrimSpace(existingValue)
				}
			}
		}
		auth.Metadata["headers"] = headers
	}
	for key := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			delete(headers, key)
		}
	}
	headers[name] = value
}

func firstNonEmptyHeaderValue(primary http.Header, secondary http.Header, key string) string {
	if value := headerValueCaseInsensitive(primary, key); value != "" {
		return value
	}
	return headerValueCaseInsensitive(secondary, key)
}
