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

// AdoptGeminiAppRotationVersion updates a request-owned auth snapshot's result
// identity after a persisted rotation. It leaves the request's cookie merge base
// intact and never adopts an explicit re-import or another registration.
func (a *Auth) AdoptGeminiAppRotationVersion(saved *Auth) {
	if a == nil || saved == nil || a.ID != saved.ID ||
		a.RegistrationEpoch != saved.RegistrationEpoch || a.geminiAppCookieEpoch != saved.geminiAppCookieEpoch ||
		!strings.EqualFold(strings.TrimSpace(a.Provider), geminiapp.Provider) ||
		!strings.EqualFold(strings.TrimSpace(saved.Provider), geminiapp.Provider) {
		return
	}
	a.CredentialVersion = max(a.CredentialVersion, saved.CredentialVersion)
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
