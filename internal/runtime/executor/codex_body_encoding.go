package executor

import (
	"strings"

	"github.com/klauspost/compress/zstd"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Codex request-body wire encoding.
//
// Real codex_cli_rs 0.142.5 compresses its POST /responses body with zstd and
// advertises `content-encoding: zstd` — live-captured on the HTTP fallback path
// (taken when the WebSocket upgrade fails, i.e. exactly when upstream detection is
// strictest). Stock CLIProxyAPI (and codex2api / sub2api) send the body as
// plaintext with no content-encoding, which is a one-glance tell: the body
// size/entropy AND the missing header both diverge from a genuine client.
//
// This mirrors the real behaviour, but ONLY on the OAuth (ChatGPT-backend) path.
// BYOK / API-key requests hit the user's own OpenAI-compatible endpoint, which is
// not guaranteed to accept a zstd-encoded request body (most would 400 it), so
// they intentionally stay plaintext — consistent with how beta-features and the
// Codex originator are applied OAuth-only.

// codexZstdEncoder is process-wide and safe for concurrent EncodeAll use (per the
// klauspost/compress docs). SpeedDefault ≈ zstd level 3, matching the Rust zstd
// crate (libzstd) default that the real codex_cli_rs ships with. WithEncoderCRC(false)
// drops klauspost's default 4-byte content-checksum trailer — libzstd's
// zstd::stream::encode_all(_, 3) does NOT emit one, so the frame descriptor
// (Content_Checksum_flag) and length match the real client's byte layout instead of
// a klauspost-flavored frame that a server-side reference encoder could distinguish.
var codexZstdEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderCRC(false))

// codexAuthIsAPIKey reports whether auth is a BYOK / API-key credential (its
// upstream is the user's own OpenAI-compatible endpoint rather than the official
// ChatGPT backend). OAuth credentials return false.
func codexAuthIsAPIKey(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["api_key"]) != ""
}

// codexShouldZstdBody reports whether the outbound codex request body should be
// zstd-compressed. It now always returns false: a local capture of the real codex
// 0.144.x client this session shows it sends a PLAIN request body (no zstd, no
// content-encoding) over BOTH HTTP and HTTPS. The 0.142.5-era zstd compression is
// therefore a fingerprint tell against a genuine 0.144 client — the body
// entropy/size AND the content-encoding header would all mismatch — so it is
// disabled. The signature is kept so the codex_executor.go call site still compiles.
func codexShouldZstdBody(auth *cliproxyauth.Auth) bool {
	return false
}

// codexZstdCompress returns the zstd-encoded form of src and true, or (nil, false)
// when there is nothing to compress or the encoder is unavailable — in which case
// the caller falls back to sending the plaintext body.
func codexZstdCompress(src []byte) ([]byte, bool) {
	if codexZstdEncoder == nil || len(src) == 0 {
		return nil, false
	}
	return codexZstdEncoder.EncodeAll(src, nil), true
}
