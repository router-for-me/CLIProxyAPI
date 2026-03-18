// Bun v1.3.x (BoringSSL) TLS ClientHello Spec for utls
// Captured from real Claude Code 2.1.71 / Bun v1.3.8 via tls.peet.ws
//
// JA3 Hash: 50027c67d7d68e24c00d233bca146d88
// JA3: 771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49161-49171-49162-49172-156-157-47-53,0-65037-23-65281-10-11-35-16-5-13-18-51-45-43-21,29-23-24,0
// JA4: t13d1715h1_5b57614c22b0_7baf387fc6ff
//
// Key differences from Chrome:
//   - No GREASE (Chrome injects random GREASE values)
//   - ALPN: http/1.1 only (Chrome: h2 + http/1.1)
//   - Fewer extensions (no compress_certificate, no delegated_credentials)
//   - ECH extension 65037 present (BoringSSL-specific)

package claude

import (
	tls "github.com/refraction-networking/utls"
)

// BunBoringSSLSpec returns a utls ClientHelloSpec that exactly matches
// Bun v1.3.x's BoringSSL TLS fingerprint, as used by Claude Code CLI.
//
// This ensures TLS fingerprint consistency between API requests and
// token refresh, matching what Anthropic sees from real Claude Code users.
func BunBoringSSLSpec() *tls.ClientHelloSpec {
	return &tls.ClientHelloSpec{
		TLSVersMin: tls.VersionTLS12,
		TLSVersMax: tls.VersionTLS13,
		CipherSuites: []uint16{
			// TLS 1.3 suites
			tls.TLS_AES_128_GCM_SHA256,       // 0x1301
			tls.TLS_AES_256_GCM_SHA384,       // 0x1302
			tls.TLS_CHACHA20_POLY1305_SHA256,  // 0x1303
			// TLS 1.2 ECDHE suites
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,       // 0xC02B
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,         // 0xC02F
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,       // 0xC02C
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,         // 0xC030
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, // 0xCCA9
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,   // 0xCCA8
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,          // 0xC009
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,            // 0xC013
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,          // 0xC00A
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,            // 0xC014
			// TLS 1.2 RSA suites
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256, // 0x009C
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384, // 0x009D
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,    // 0x002F
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,    // 0x0035
		},
		Extensions: []tls.TLSExtension{
			// 0 - Server Name Indication
			&tls.SNIExtension{},

			// 65037 - Encrypted Client Hello (BoringSSL-specific)
			&tls.GenericExtension{Id: 65037},

			// 23 - Extended Master Secret
			&tls.ExtendedMasterSecretExtension{},

			// 65281 - Renegotiation Info
			&tls.RenegotiationInfoExtension{Renegotiation: tls.RenegotiateOnceAsClient},

			// 10 - Supported Groups
			&tls.SupportedCurvesExtension{
				Curves: []tls.CurveID{
					tls.X25519,    // 29
					tls.CurveP256, // 23
					tls.CurveP384, // 24
				},
			},

			// 11 - EC Point Formats
			&tls.SupportedPointsExtension{
				SupportedPoints: []byte{0x00}, // uncompressed
			},

			// 35 - Session Ticket
			&tls.SessionTicketExtension{},

			// 16 - ALPN (http/1.1 ONLY — Bun does not negotiate h2)
			&tls.ALPNExtension{
				AlpnProtocols: []string{"http/1.1"},
			},

			// 5 - Status Request (OCSP)
			&tls.StatusRequestExtension{},

			// 13 - Signature Algorithms
			&tls.SignatureAlgorithmsExtension{
				SupportedSignatureAlgorithms: []tls.SignatureScheme{
					tls.ECDSAWithP256AndSHA256, // 0x0403
					tls.PSSWithSHA256,          // 0x0804
					tls.PKCS1WithSHA256,        // 0x0401
					tls.ECDSAWithP384AndSHA384, // 0x0503
					tls.PSSWithSHA384,          // 0x0805
					tls.PKCS1WithSHA384,        // 0x0501
					tls.PSSWithSHA512,          // 0x0806
					tls.PKCS1WithSHA512,        // 0x0601
					tls.PKCS1WithSHA1,          // 0x0201
				},
			},

			// 18 - Signed Certificate Timestamp (SCT)
			&tls.SCTExtension{},

			// 51 - Key Share (X25519)
			&tls.KeyShareExtension{
				KeyShares: []tls.KeyShare{
					{Group: tls.X25519},
				},
			},

			// 45 - PSK Key Exchange Modes
			&tls.PSKKeyExchangeModesExtension{
				Modes: []uint8{tls.PskModeDHE}, // psk_dhe_ke (1)
			},

			// 43 - Supported Versions
			&tls.SupportedVersionsExtension{
				Versions: []uint16{
					tls.VersionTLS13, // 0x0304
					tls.VersionTLS12, // 0x0303
				},
			},

			// 21 - Padding
			&tls.UtlsPaddingExtension{GetPaddingLen: tls.BoringPaddingStyle},
		},
	}
}
