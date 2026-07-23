package executor

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var codexPinnedClientProfileHeaders = []string{
	"X-Codex-Beta-Features",
	"Version",
	"User-Agent",
	"Originator",
	"x-responsesapi-include-timing-metrics",
}

type codexClientProfile struct {
	headers http.Header
}

type codexClientProfileKey struct {
	id   string
	auth *cliproxyauth.Auth
}

var (
	codexClientProfilesMu sync.RWMutex
	codexClientProfiles   = make(map[codexClientProfileKey]codexClientProfile)
)

func codexPinClientProfileFromFirstRequest(_ context.Context, auth *cliproxyauth.Auth, target http.Header, source http.Header, cfg *config.Config) {
	key, ok := codexClientProfileKeyForAuth(auth)
	if !ok || (target == nil && source == nil) {
		return
	}

	codexClientProfilesMu.RLock()
	_, pinned := codexClientProfiles[key]
	codexClientProfilesMu.RUnlock()
	if pinned {
		return
	}

	codexClientProfilesMu.Lock()
	defer codexClientProfilesMu.Unlock()
	if _, pinned = codexClientProfiles[key]; pinned {
		return
	}

	pinnedHeaders := make(http.Header)
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
		if strings.EqualFold(headerName, "Version") && (value == "" || !codexVersionAtLeast(value, codexDefaultVersion)) {
			if value != "" || !codexAuthUsesAPIKey(auth) {
				value = codexDefaultVersion
			}
		}
		if value == "" {
			continue
		}
		pinnedHeaders.Set(headerName, value)
	}
	codexClientProfiles[key] = codexClientProfile{headers: pinnedHeaders}
}

func codexClientProfilePinned(auth *cliproxyauth.Auth) bool {
	key, ok := codexClientProfileKeyForAuth(auth)
	if !ok {
		return false
	}
	codexClientProfilesMu.RLock()
	_, pinned := codexClientProfiles[key]
	codexClientProfilesMu.RUnlock()
	return pinned
}

func codexClientProfileSourceHeaders(auth *cliproxyauth.Auth, source http.Header) http.Header {
	if codexClientProfilePinned(auth) {
		return nil
	}
	return source
}

func codexPreparePinnedClientProfileHeaders(headers http.Header, auth *cliproxyauth.Auth) {
	if headers == nil {
		return
	}
	key, ok := codexClientProfileKeyForAuth(auth)
	if !ok {
		return
	}

	codexClientProfilesMu.RLock()
	profile, pinned := codexClientProfiles[key]
	codexClientProfilesMu.RUnlock()
	if !pinned {
		return
	}

	for _, headerName := range codexPinnedClientProfileHeaders {
		if codexAuthHeaderFixed(auth, headerName) {
			continue
		}
		if value := profile.headers.Get(headerName); value != "" {
			setHeaderCasePreserved(headers, headerName, value)
		} else {
			deleteHeaderCaseInsensitive(headers, headerName)
		}
	}
}

func codexClientProfileKeyForAuth(auth *cliproxyauth.Auth) (codexClientProfileKey, bool) {
	if auth == nil {
		return codexClientProfileKey{}, false
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return codexClientProfileKey{id: id}, true
	}
	return codexClientProfileKey{auth: auth}, true
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

func firstNonEmptyHeaderValue(primary http.Header, secondary http.Header, key string) string {
	if value := headerValueCaseInsensitive(primary, key); value != "" {
		return value
	}
	return headerValueCaseInsensitive(secondary, key)
}

func codexEnsureVersionHeader(target http.Header, source http.Header, useDefault bool) {
	if target == nil {
		return
	}
	version := firstNonEmptyHeaderValue(target, source, "Version")
	if version == "" && useDefault {
		version = codexDefaultVersion
	}
	if version == "" {
		return
	}
	if !codexVersionAtLeast(version, codexDefaultVersion) {
		version = codexDefaultVersion
	}
	setHeaderCasePreserved(target, "Version", version)
}

func codexVersionAtLeast(version string, minimum string) bool {
	currentParts, okCurrent := codexParseVersionParts(version)
	minimumParts, okMinimum := codexParseVersionParts(minimum)
	if !okCurrent || !okMinimum {
		return true
	}
	for i := 0; i < len(currentParts); i++ {
		if currentParts[i] > minimumParts[i] {
			return true
		}
		if currentParts[i] < minimumParts[i] {
			return false
		}
	}
	return true
}

func codexParseVersionParts(version string) ([3]int, bool) {
	version = strings.TrimSpace(strings.TrimPrefix(version, "v"))
	if version == "" {
		return [3]int{}, false
	}
	fields := strings.FieldsFunc(version, func(r rune) bool {
		return r == '.' || r == '-'
	})
	if len(fields) < 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for i := 0; i < len(parts); i++ {
		value, err := strconv.Atoi(fields[i])
		if err != nil {
			return [3]int{}, false
		}
		parts[i] = value
	}
	return parts, true
}
