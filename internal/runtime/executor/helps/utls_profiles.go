package helps

import (
	"strings"

	tls "github.com/refraction-networking/utls"
)

// tlsProfile describes how to build a uTLS ClientHello for an upstream host and
// which application protocol that ClientHello negotiates.
//
// The point of per-host profiles is to make the TLS fingerprint agree with the
// User-Agent we send. A generic Chrome ClientHello paired with a "Node/CLI"
// User-Agent is itself a detectable cross-layer tell; matching the real client's
// fingerprint removes it.
type tlsProfile struct {
	name string
	// http2 reports whether the profile negotiates HTTP/2 ("h2"). When false the
	// profile speaks HTTP/1.1, matching Node.js/undici clients such as Claude Code.
	http2 bool
	// helloID selects a built-in uTLS fingerprint; used when spec is nil.
	helloID tls.ClientHelloID
	// spec, when non-nil, is applied via HelloCustom for a hand-built fingerprint.
	spec func() *tls.ClientHelloSpec
}

// chromeH2Profile is uTLS's generic Chrome fingerprint over HTTP/2. This is the
// historical CLIProxyAPI behavior and remains the default for chatgpt.com.
var chromeH2Profile = tlsProfile{
	name:    "chrome-h2",
	http2:   true,
	helloID: tls.HelloChrome_Auto,
}

// nodeH1Profile mimics Claude Code (Node.js 24.x / undici) talking to
// api.anthropic.com over HTTP/1.1.
//
//	JA3: 44f88fca027f27bab4bb08d4af15f23e
//	JA4: t13d1714h1_5b57614c22b0_7baf387fc6ff
//
// Fingerprint values captured from a real Claude Code client; ordering is
// significant for JA3/JA4 matching.
var nodeH1Profile = tlsProfile{
	name:  "node-h1",
	http2: false,
	spec:  nodeClientHelloSpec,
}

// resolveTLSProfile returns the uTLS profile for a given upstream host and
// whether the host is uTLS-protected at all. When disableNode is true the
// Anthropic host reverts to the generic Chrome profile (escape hatch).
func resolveTLSProfile(host string, disableNode bool) (tlsProfile, bool) {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "api.anthropic.com":
		if disableNode {
			return chromeH2Profile, true
		}
		return nodeH1Profile, true
	case "chatgpt.com":
		return chromeH2Profile, true
	default:
		return tlsProfile{}, false
	}
}

// nodeCipherSuites are the 17 cipher suites offered by Node.js 24.x, in order.
var nodeCipherSuites = []uint16{
	// TLS 1.3
	0x1301, // TLS_AES_128_GCM_SHA256
	0x1302, // TLS_AES_256_GCM_SHA384
	0x1303, // TLS_CHACHA20_POLY1305_SHA256
	// ECDHE + AES-GCM
	0xc02b, // ECDHE_ECDSA_AES_128_GCM_SHA256
	0xc02f, // ECDHE_RSA_AES_128_GCM_SHA256
	0xc02c, // ECDHE_ECDSA_AES_256_GCM_SHA384
	0xc030, // ECDHE_RSA_AES_256_GCM_SHA384
	// ECDHE + ChaCha20-Poly1305
	0xcca9, // ECDHE_ECDSA_CHACHA20_POLY1305
	0xcca8, // ECDHE_RSA_CHACHA20_POLY1305
	// ECDHE + AES-CBC-SHA (legacy)
	0xc009, // ECDHE_ECDSA_AES_128_CBC_SHA
	0xc013, // ECDHE_RSA_AES_128_CBC_SHA
	0xc00a, // ECDHE_ECDSA_AES_256_CBC_SHA
	0xc014, // ECDHE_RSA_AES_256_CBC_SHA
	// RSA + AES-GCM (non-PFS)
	0x009c, // RSA_AES_128_GCM_SHA256
	0x009d, // RSA_AES_256_GCM_SHA384
	// RSA + AES-CBC-SHA (non-PFS, legacy)
	0x002f, // RSA_AES_128_CBC_SHA
	0x0035, // RSA_AES_256_CBC_SHA
}

// nodeSignatureAlgorithms are the 9 signature schemes offered by Node.js 24.x.
var nodeSignatureAlgorithms = []tls.SignatureScheme{
	0x0403, // ecdsa_secp256r1_sha256
	0x0804, // rsa_pss_rsae_sha256
	0x0401, // rsa_pkcs1_sha256
	0x0503, // ecdsa_secp384r1_sha384
	0x0805, // rsa_pss_rsae_sha384
	0x0501, // rsa_pkcs1_sha384
	0x0806, // rsa_pss_rsae_sha512
	0x0601, // rsa_pkcs1_sha512
	0x0201, // rsa_pkcs1_sha1
}

// nodeClientHelloSpec builds the Node.js 24.x / Claude Code ClientHello.
// Extension order matches the captured fingerprint and must not be reordered.
// No classic GREASE (0x0a0a cipher/curve/extension padding) is sent, matching
// JA3 44f88fca…. The GREASEEncryptedClientHelloExtension below is NOT classic
// GREASE — it is the genuine ECH-GREASE (ext 65037) that Node 24 / OpenSSL 3
// emits by default; do not remove it.
func nodeClientHelloSpec() *tls.ClientHelloSpec {
	curves := []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}
	return &tls.ClientHelloSpec{
		CipherSuites:       nodeCipherSuites,
		CompressionMethods: []uint8{0}, // null compression
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},                                                                      // 0  server_name
			&tls.GREASEEncryptedClientHelloExtension{},                                               // 65037 encrypted_client_hello (GREASE ECH)
			&tls.ExtendedMasterSecretExtension{},                                                     // 23 extended_master_secret
			&tls.RenegotiationInfoExtension{},                                                        // 65281 renegotiation_info
			&tls.SupportedCurvesExtension{Curves: curves},                                            // 10 supported_groups
			&tls.SupportedPointsExtension{SupportedPoints: []uint8{0}},                               // 11 ec_point_formats
			&tls.SessionTicketExtension{},                                                            // 35 session_ticket
			&tls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},                                  // 16 alpn
			&tls.StatusRequestExtension{},                                                            // 5  status_request
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: nodeSignatureAlgorithms}, // 13
			&tls.SCTExtension{},                                                                      // 18 signed_certificate_timestamp
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519}}},                   // 51 key_share
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},                        // 45 psk_key_exchange_modes
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},  // 43
		},
		TLSVersMax: tls.VersionTLS13,
		TLSVersMin: tls.VersionTLS10,
	}
}
