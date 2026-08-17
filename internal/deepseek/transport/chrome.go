package transport

import (
	fhttp2 "github.com/bogdanfinn/fhttp/http2"
	utls "github.com/refraction-networking/utls"
)

// ChromeMajorVersion is the Chrome generation advertised at the HTTP layer
// (User-Agent and sec-ch-ua, both built from it in the protocol package).
//
// It is set from a real capture of chat.deepseek.com's web client. Keep it
// current: Chrome ships roughly every four weeks and the overwhelming majority
// of real users sit within a few versions of latest, so a stale User-Agent is
// itself an anomaly — and it is the cheapest thing in the world for a server to
// check.
const ChromeMajorVersion = "150"

// TLSChromeVersion is the Chrome generation the uTLS ClientHello reproduces.
//
// This deliberately lags ChromeMajorVersion because uTLS only ships
// fingerprints it has actually modelled, and HelloChrome_133 is the newest one
// available (checked against refraction-networking/utls v1.8.2, its latest
// release). Chrome's ClientHello changes far more slowly than its version
// number, so reproducing a slightly older handshake costs much less than
// advertising a User-Agent nobody runs any more.
//
// When uTLS gains a newer Chrome, bump both constants together and drop the
// gap. TestChromeProfileSelfConsistent guards the invariant.
const TLSChromeVersion = "133"

// clientHelloID is the browser TLS fingerprint impersonated on every upstream
// request. Pinned explicitly rather than using utls.HelloChrome_Auto: Auto
// silently tracks whatever the utls release considers current, which would
// desynchronise it from TLSChromeVersion on every dependency bump.
var clientHelloID = utls.HelloChrome_133

// Chrome's HTTP/2 connection preface. Reproducing these exactly is what keeps
// the "Akamai" HTTP/2 fingerprint (SETTINGS | WINDOW_UPDATE | PRIORITY |
// pseudo-header order) from reading as Go's net/http2 instead of Chrome's.
//
// Go's stock net/http2 emits 2:0;4:4194304;5:1048576;6:10485760 with a
// 1073741824 WINDOW_UPDATE and a,m,p,s pseudo-header order, which is a
// well-known bot signature.
const chromeConnectionFlow = 15663105

var chromeSettingsOrder = []fhttp2.SettingID{
	fhttp2.SettingHeaderTableSize,
	fhttp2.SettingEnablePush,
	fhttp2.SettingInitialWindowSize,
	fhttp2.SettingMaxHeaderListSize,
}

var chromeSettings = map[fhttp2.SettingID]uint32{
	fhttp2.SettingHeaderTableSize:   65536,
	fhttp2.SettingEnablePush:        0,
	fhttp2.SettingInitialWindowSize: 6291456,
	fhttp2.SettingMaxHeaderListSize: 262144,
}

// chromePseudoHeaderOrder is Chrome's :method,:authority,:scheme,:path.
// Go's net/http2 hardcodes :authority,:method,:path,:scheme instead.
var chromePseudoHeaderOrder = []string{":method", ":authority", ":scheme", ":path"}

// chromeHeaderOrder pins the request header order. Go's map iteration
// randomises header order on every request, which is more anomalous than any
// fixed-but-wrong order: a real browser's order is stable per request shape.
//
// Names must be lowercase — fhttp lowercases keys before looking them up in
// the order map, and headers absent from this list are appended afterwards in
// lexicographic order (still deterministic).
//
// This mirrors commonly observed Chrome fetch/XHR ordering. It is worth
// re-validating against a live capture of chat.deepseek.com if the upstream
// web client changes which headers it sets.
var chromeHeaderOrder = []string{
	"content-length",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"authorization",
	"x-client-bundle-id",
	"x-client-locale",
	"x-client-platform",
	"x-client-timezone-offset",
	"x-client-version",
	"x-ds-pow-response",
	"user-agent",
	"content-type",
	"accept",
	"origin",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-dest",
	"referer",
	"accept-encoding",
	"accept-language",
	"cookie",
	"priority",
}

// ChromeHeaderOrder returns a copy of the pinned request header order, for
// tooling that needs to reproduce or diff the exact on-wire ordering.
func ChromeHeaderOrder() []string {
	return append([]string(nil), chromeHeaderOrder...)
}

// ChromePseudoHeaderOrder returns a copy of the pinned pseudo-header order.
func ChromePseudoHeaderOrder() []string {
	return append([]string(nil), chromePseudoHeaderOrder...)
}

func newChromeH2Transport() *fhttp2.Transport {
	return &fhttp2.Transport{
		Settings:          chromeSettings,
		SettingsOrder:     chromeSettingsOrder,
		ConnectionFlow:    chromeConnectionFlow,
		PseudoHeaderOrder: chromePseudoHeaderOrder,
		// Chrome does not send legacy PRIORITY frames, so Priorities stays
		// empty to keep the priority component of the fingerprint at 0.
	}
}
