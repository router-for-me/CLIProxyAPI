package auth

import (
	"maps"
	"reflect"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/geminiapp"
)

func geminiAppCookieMap(auth *Auth) map[string]any {
	if auth == nil {
		return nil
	}
	switch cookies := auth.Metadata["cookies"].(type) {
	case map[string]any:
		return cookies
	case map[string]string:
		values := make(map[string]any, len(cookies))
		for key, value := range cookies {
			values[key] = value
		}
		return values
	default:
		return nil
	}
}

func geminiAppCookiesChanged(existing, incoming *Auth) bool {
	if existing == nil || incoming == nil || !strings.EqualFold(strings.TrimSpace(existing.Provider), geminiapp.Provider) {
		return false
	}
	before, after := geminiAppCookieMap(existing), geminiAppCookieMap(incoming)
	return !reflect.DeepEqual(before[geminiapp.CookiePSID], after[geminiapp.CookiePSID]) ||
		!reflect.DeepEqual(before[geminiapp.CookiePSIDTS], after[geminiapp.CookiePSIDTS])
}

func mergeGeminiAppPreparedCookies(base, current, updated, merged *Auth) {
	if base == nil || current == nil || updated == nil || merged == nil ||
		!strings.EqualFold(strings.TrimSpace(current.Provider), geminiapp.Provider) ||
		base.RegistrationEpoch != current.RegistrationEpoch {
		return
	}
	// A replacement invalidates the whole old session, even if it changes only
	// one slot. Upstream rotations retain this epoch and can merge independently.
	if base.geminiAppCookieEpoch != current.geminiAppCookieEpoch {
		if _, ok := current.Metadata["cookies"]; ok {
			merged.Metadata["cookies"] = cloneMetadataMap(current.Metadata)["cookies"]
		} else {
			delete(merged.Metadata, "cookies")
		}
		return
	}
	before, live, after := geminiAppCookieMap(base), geminiAppCookieMap(current), geminiAppCookieMap(updated)
	cookies := maps.Clone(live)
	if cookies == nil {
		cookies = make(map[string]any)
	}
	for _, name := range []string{geminiapp.CookiePSID, geminiapp.CookiePSIDTS} {
		if reflect.DeepEqual(before[name], after[name]) || !reflect.DeepEqual(before[name], live[name]) {
			continue
		}
		if value, exists := after[name]; exists {
			cookies[name] = value
		} else {
			delete(cookies, name)
		}
	}
	merged.Metadata["cookies"] = cookies
}
