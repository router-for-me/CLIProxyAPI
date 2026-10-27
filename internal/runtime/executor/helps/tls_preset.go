package helps

import (
	"hash/fnv"
	"strings"
	"sync"

	tls "github.com/refraction-networking/utls"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// AttributeTLSPreset is the Auth.Attributes key that selects the account TLS
// fingerprint preset. Attributes carry immutable executor configuration, so the
// value follows the Auth snapshot without a new storage backend.
const AttributeTLSPreset = "tls_preset"

// Supported account TLS presets. An empty or missing preset keeps the
// historical HelloChrome_Auto behavior for zero-config deployments.
const (
	TLSPresetChrome120    = "chrome_120"
	TLSPresetChromeAuto   = "chrome_auto"
	TLSPresetFirefox120   = "firefox_120"
	TLSPresetSafari16     = "safari_16"
	TLSPresetIOS16        = "ios_16"
	TLSPresetRandomChrome = "random_chrome"
)

// Concrete Chrome presets that random_chrome chooses from. They deliberately
// stay inside the Chrome UA family so random selection never violates the UA
// consistency guard.
const (
	tlsPresetChrome131 = "chrome_131"
	tlsPresetChrome133 = "chrome_133"
)

var randomChromePresets = []string{
	TLSPresetChrome120,
	tlsPresetChrome131,
	tlsPresetChrome133,
}

// tlsPresetFamily groups presets by the browser family whose User-Agent they
// must accompany.
type tlsPresetFamily int

const (
	tlsFamilyUnknown tlsPresetFamily = iota
	tlsFamilyChrome
	tlsFamilyFirefox
	tlsFamilySafari
	tlsFamilyIOS
)

func (f tlsPresetFamily) String() string {
	switch f {
	case tlsFamilyChrome:
		return "chrome"
	case tlsFamilyFirefox:
		return "firefox"
	case tlsFamilySafari:
		return "safari"
	case tlsFamilyIOS:
		return "ios"
	default:
		return "unknown"
	}
}

// ResolveAccountTLSPreset returns the concrete preset to use for one account.
//
// An empty preset preserves the historical HelloChrome_Auto default and skips
// the User-Agent guard, keeping zero-config deployments byte-identical to the
// previous behavior. random_chrome is expanded deterministically from authID so
// an account keeps the same Chrome profile across requests and restarts.
// Unknown values fall back to chrome_auto. When a non-empty preset conflicts
// with the User-Agent family, the preset falls back to that family's default
// and a warning is logged; the guard never fails or rewrites a request.
func ResolveAccountTLSPreset(preset, authID, userAgent string) string {
	raw := strings.ToLower(strings.TrimSpace(preset))
	if raw == "" {
		return ""
	}

	resolved := raw
	switch raw {
	case TLSPresetRandomChrome:
		resolved = randomChromePreset(authID)
	case TLSPresetChrome120, TLSPresetChromeAuto, tlsPresetChrome131, tlsPresetChrome133,
		TLSPresetFirefox120, TLSPresetSafari16, TLSPresetIOS16:
		// Supported preset, keep it as configured.
	default:
		warnTLSPresetOnce("preset:"+raw, "utls: unknown tls_preset %q; falling back to %q", raw, TLSPresetChromeAuto)
		resolved = TLSPresetChromeAuto
	}

	presetFamily := tlsFamilyForPreset(resolved)
	uaFamily := tlsFamilyForUserAgent(userAgent)
	if presetFamily == tlsFamilyUnknown || uaFamily == tlsFamilyUnknown || presetFamily == uaFamily {
		return resolved
	}

	fallback := defaultPresetForFamily(uaFamily)
	warnTLSPresetOnce(
		"preset-conflict:"+raw+":"+uaFamily.String(),
		"utls: tls_preset %q conflicts with %s user-agent; falling back to %q",
		raw, uaFamily.String(), fallback,
	)
	return fallback
}

// TLSClientHelloIDForPreset maps a resolved preset to a uTLS ClientHelloID.
// Empty input returns HelloChrome_Auto so unset accounts keep the previous
// fingerprint. Unknown input is warned about and downgraded to the same default.
func TLSClientHelloIDForPreset(preset string) tls.ClientHelloID {
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case "", TLSPresetChromeAuto:
		return tls.HelloChrome_Auto
	case TLSPresetChrome120:
		return tls.HelloChrome_120
	case tlsPresetChrome131:
		return tls.HelloChrome_131
	case tlsPresetChrome133:
		return tls.HelloChrome_133
	case TLSPresetFirefox120:
		return tls.HelloFirefox_120
	case TLSPresetSafari16:
		return tls.HelloSafari_16_0
	case TLSPresetIOS16:
		// uTLS v1.8.2 ships iOS 14 as its newest iOS profile. Use it to keep
		// the ClientHello inside the iOS UA family rather than crossing to a
		// desktop browser fingerprint.
		warnTLSPresetOnce(
			"preset:"+TLSPresetIOS16+":approximation",
			"utls: tls_preset %q has no exact uTLS v1.8.2 profile; using iOS 14",
			TLSPresetIOS16,
		)
		return tls.HelloIOS_Auto
	case TLSPresetRandomChrome:
		// random_chrome must be expanded with an Auth ID first.
		warnTLSPresetOnce(
			"preset:"+TLSPresetRandomChrome+":unresolved",
			"utls: tls_preset %q requires an auth ID; using chrome_auto",
			TLSPresetRandomChrome,
		)
		return tls.HelloChrome_Auto
	default:
		warnTLSPresetOnce(
			"preset:"+strings.TrimSpace(preset)+":unknown",
			"utls: unknown tls_preset %q; using chrome_auto",
			strings.TrimSpace(preset),
		)
		return tls.HelloChrome_Auto
	}
}

func tlsFamilyForPreset(preset string) tlsPresetFamily {
	switch preset {
	case TLSPresetChrome120, TLSPresetChromeAuto, tlsPresetChrome131, tlsPresetChrome133, TLSPresetRandomChrome:
		return tlsFamilyChrome
	case TLSPresetFirefox120:
		return tlsFamilyFirefox
	case TLSPresetSafari16:
		return tlsFamilySafari
	case TLSPresetIOS16:
		return tlsFamilyIOS
	default:
		return tlsFamilyUnknown
	}
}

func tlsFamilyForUserAgent(userAgent string) tlsPresetFamily {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if ua == "" {
		return tlsFamilyUnknown
	}
	switch {
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ipod"):
		return tlsFamilyIOS
	case strings.Contains(ua, "firefox/"):
		return tlsFamilyFirefox
	case strings.Contains(ua, "chrome/"), strings.Contains(ua, "chromium/"), strings.Contains(ua, "edg/"):
		return tlsFamilyChrome
	case strings.Contains(ua, "safari/") && strings.Contains(ua, "version/"):
		return tlsFamilySafari
	default:
		return tlsFamilyUnknown
	}
}

func defaultPresetForFamily(family tlsPresetFamily) string {
	switch family {
	case tlsFamilyFirefox:
		return TLSPresetFirefox120
	case tlsFamilySafari:
		return TLSPresetSafari16
	case tlsFamilyIOS:
		return TLSPresetIOS16
	default:
		return TLSPresetChromeAuto
	}
}

// randomChromePreset selects a Chrome profile with FNV-1a over the Auth ID.
// The selection is stable for an account and spreads distinct IDs across the
// available Chrome versions without any runtime randomness.
func randomChromePreset(authID string) string {
	hasher := fnv.New64a()
	if _, errWrite := hasher.Write([]byte(strings.TrimSpace(authID))); errWrite != nil {
		return TLSPresetChromeAuto
	}
	return randomChromePresets[hasher.Sum64()%uint64(len(randomChromePresets))]
}

func authTLSPreset(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[AttributeTLSPreset])
}

func authIDForTLSPreset(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.ID)
}

// authUserAgent reads the account User-Agent that the UA consistency guard
// compares against. An explicit header override wins because executors apply
// it last on the wire; otherwise the antigravity user_agent attribute and
// mutable Metadata are used.
func authUserAgent(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		for key, value := range auth.Attributes {
			if strings.EqualFold(strings.TrimSpace(key), "header:User-Agent") {
				if ua := strings.TrimSpace(value); ua != "" {
					return ua
				}
			}
		}
		if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
			return ua
		}
	}
	if auth.Metadata != nil {
		if ua, ok := auth.Metadata["user_agent"].(string); ok {
			return strings.TrimSpace(ua)
		}
	}
	return ""
}

// tlsPresetWarnings deduplicates configuration warnings that would otherwise
// repeat on every request because NewUtlsHTTPClient is built per request.
var tlsPresetWarnings sync.Map

func warnTLSPresetOnce(key, format string, args ...any) {
	if _, loaded := tlsPresetWarnings.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Warnf(format, args...)
}
