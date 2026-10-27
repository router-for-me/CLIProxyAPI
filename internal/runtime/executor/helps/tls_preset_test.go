package helps

import (
	"fmt"
	"slices"
	"testing"

	tls "github.com/refraction-networking/utls"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	testChromeUA  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	testFirefoxUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:120.0) Gecko/20100101 Firefox/120.0"
	testSafariUA  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15"
	testIOSUA     = "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1"
	testCodexUA   = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64)"
)

func TestResolveAccountTLSPreset(t *testing.T) {
	tests := []struct {
		name      string
		preset    string
		authID    string
		userAgent string
		want      string
	}{
		{name: "empty keeps legacy default", preset: "", userAgent: testChromeUA, want: ""},
		{name: "empty skips UA guard for zero config", preset: "", userAgent: testFirefoxUA, want: ""},
		{name: "chrome 120", preset: TLSPresetChrome120, want: TLSPresetChrome120},
		{name: "chrome auto", preset: TLSPresetChromeAuto, want: TLSPresetChromeAuto},
		{name: "firefox 120", preset: TLSPresetFirefox120, want: TLSPresetFirefox120},
		{name: "safari 16", preset: TLSPresetSafari16, want: TLSPresetSafari16},
		{name: "ios 16", preset: TLSPresetIOS16, want: TLSPresetIOS16},
		{name: "concrete chrome 131", preset: tlsPresetChrome131, want: tlsPresetChrome131},
		{name: "concrete chrome 133", preset: tlsPresetChrome133, want: tlsPresetChrome133},
		{name: "case and whitespace normalized", preset: "  ChRoMe_120 \t", want: TLSPresetChrome120},
		{name: "unknown falls back to chrome auto", preset: "netscape_4", want: TLSPresetChromeAuto},
		{name: "chrome preset with chrome UA", preset: TLSPresetChrome120, userAgent: testChromeUA, want: TLSPresetChrome120},
		{name: "firefox preset with firefox UA", preset: TLSPresetFirefox120, userAgent: testFirefoxUA, want: TLSPresetFirefox120},
		{name: "safari preset with safari UA", preset: TLSPresetSafari16, userAgent: testSafariUA, want: TLSPresetSafari16},
		{name: "ios preset with ios UA", preset: TLSPresetIOS16, userAgent: testIOSUA, want: TLSPresetIOS16},
		{name: "chrome preset conflicts with firefox UA", preset: TLSPresetChrome120, userAgent: testFirefoxUA, want: TLSPresetFirefox120},
		{name: "chrome auto conflicts with firefox UA", preset: TLSPresetChromeAuto, userAgent: testFirefoxUA, want: TLSPresetFirefox120},
		{name: "firefox preset conflicts with chrome UA", preset: TLSPresetFirefox120, userAgent: testChromeUA, want: TLSPresetChromeAuto},
		{name: "firefox preset conflicts with safari UA", preset: TLSPresetFirefox120, userAgent: testSafariUA, want: TLSPresetSafari16},
		{name: "safari preset conflicts with chrome UA", preset: TLSPresetSafari16, userAgent: testChromeUA, want: TLSPresetChromeAuto},
		{name: "chrome preset conflicts with ios UA", preset: TLSPresetChrome120, userAgent: testIOSUA, want: TLSPresetIOS16},
		{name: "unknown preset falls back then honors firefox UA", preset: "netscape_4", userAgent: testFirefoxUA, want: TLSPresetFirefox120},
		{name: "non browser UA skips guard", preset: TLSPresetChrome120, userAgent: testCodexUA, want: TLSPresetChrome120},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ResolveAccountTLSPreset(testCase.preset, testCase.authID, testCase.userAgent); got != testCase.want {
				t.Fatalf("ResolveAccountTLSPreset(%q, %q, %q) = %q, want %q", testCase.preset, testCase.authID, testCase.userAgent, got, testCase.want)
			}
		})
	}
}

func TestResolveAccountTLSPresetRandomChrome(t *testing.T) {
	const authID = "auth-random-chrome-account"
	first := ResolveAccountTLSPreset(TLSPresetRandomChrome, authID, "")
	if !slices.Contains(randomChromePresets, first) {
		t.Fatalf("random_chrome resolved to %q, want one of %v", first, randomChromePresets)
	}
	if family := tlsFamilyForPreset(first); family != tlsFamilyChrome {
		t.Fatalf("random_chrome resolved to %q with family %s, want chrome", first, family)
	}
	for attempt := 0; attempt < 8; attempt++ {
		if got := ResolveAccountTLSPreset(TLSPresetRandomChrome, authID, testChromeUA); got != first {
			t.Fatalf("random_chrome is not stable for %q: got %q on attempt %d, want %q", authID, got, attempt, first)
		}
	}

	seen := make(map[string]struct{}, len(randomChromePresets))
	for index := 0; index < 64; index++ {
		seen[ResolveAccountTLSPreset(TLSPresetRandomChrome, fmt.Sprintf("auth-%d", index), "")] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("random_chrome mapped 64 distinct auth IDs to a single preset %v", seen)
	}
}

func TestTLSClientHelloIDForPreset(t *testing.T) {
	tests := []struct {
		preset string
		want   tls.ClientHelloID
	}{
		{preset: "", want: tls.HelloChrome_Auto},
		{preset: TLSPresetChromeAuto, want: tls.HelloChrome_Auto},
		{preset: TLSPresetChrome120, want: tls.HelloChrome_120},
		{preset: tlsPresetChrome131, want: tls.HelloChrome_131},
		{preset: tlsPresetChrome133, want: tls.HelloChrome_133},
		{preset: TLSPresetFirefox120, want: tls.HelloFirefox_120},
		{preset: TLSPresetSafari16, want: tls.HelloSafari_16_0},
		{preset: TLSPresetIOS16, want: tls.HelloIOS_Auto},
		{preset: TLSPresetRandomChrome, want: tls.HelloChrome_Auto},
		{preset: "not_a_preset", want: tls.HelloChrome_Auto},
		{preset: "  FIREFOX_120 ", want: tls.HelloFirefox_120},
	}
	for _, testCase := range tests {
		t.Run(testCase.preset, func(t *testing.T) {
			if got := TLSClientHelloIDForPreset(testCase.preset); got != testCase.want {
				t.Fatalf("TLSClientHelloIDForPreset(%q) = %+v, want %+v", testCase.preset, got, testCase.want)
			}
		})
	}
}

func TestTLSUserAgentFamily(t *testing.T) {
	tests := []struct {
		name      string
		userAgent string
		want      tlsPresetFamily
	}{
		{name: "empty", userAgent: "", want: tlsFamilyUnknown},
		{name: "chrome", userAgent: testChromeUA, want: tlsFamilyChrome},
		{name: "chromium", userAgent: "Mozilla/5.0 Chromium/120.0", want: tlsFamilyChrome},
		{name: "edge", userAgent: "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/120.0 Safari/537.36 Edg/120.0", want: tlsFamilyChrome},
		{name: "firefox", userAgent: testFirefoxUA, want: tlsFamilyFirefox},
		{name: "safari", userAgent: testSafariUA, want: tlsFamilySafari},
		{name: "ios", userAgent: testIOSUA, want: tlsFamilyIOS},
		{name: "non browser client", userAgent: testCodexUA, want: tlsFamilyUnknown},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := tlsFamilyForUserAgent(testCase.userAgent); got != testCase.want {
				t.Fatalf("tlsFamilyForUserAgent(%q) = %s, want %s", testCase.userAgent, got, testCase.want)
			}
		})
	}
}

func TestAuthTLSPresetAndUserAgentSources(t *testing.T) {
	authWithAttributes := &cliproxyauth.Auth{ID: "auth-attributes"}
	authWithAttributes.Attributes = map[string]string{
		AttributeTLSPreset: " " + TLSPresetFirefox120 + " ",
		"user_agent":       testFirefoxUA,
	}
	if got := authTLSPreset(authWithAttributes); got != TLSPresetFirefox120 {
		t.Fatalf("authTLSPreset = %q, want %q", got, TLSPresetFirefox120)
	}
	if got := authUserAgent(authWithAttributes); got != testFirefoxUA {
		t.Fatalf("authUserAgent from attributes = %q, want %q", got, testFirefoxUA)
	}

	authWithHeaderOverride := &cliproxyauth.Auth{Attributes: map[string]string{
		"user_agent":        testSafariUA,
		"header:user-agent": testFirefoxUA,
	}}
	if got := authUserAgent(authWithHeaderOverride); got != testFirefoxUA {
		t.Fatalf("authUserAgent with header override = %q, want %q", got, testFirefoxUA)
	}

	authWithMetadata := &cliproxyauth.Auth{Metadata: map[string]any{"user_agent": testSafariUA}}
	if got := authUserAgent(authWithMetadata); got != testSafariUA {
		t.Fatalf("authUserAgent from metadata = %q, want %q", got, testSafariUA)
	}
	if got := authTLSPreset(authWithMetadata); got != "" {
		t.Fatalf("authTLSPreset with no attribute = %q, want empty", got)
	}

	if got := authUserAgent(nil); got != "" {
		t.Fatalf("authUserAgent(nil) = %q, want empty", got)
	}
	if got := authTLSPreset(nil); got != "" {
		t.Fatalf("authTLSPreset(nil) = %q, want empty", got)
	}
}
