package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// zstdMagic is the 4-byte zstd frame magic (little-endian 0xFD2FB528).
var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

func zstdDecodeAll(t *testing.T, src []byte) []byte {
	t.Helper()
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer dec.Close()
	out, err := dec.DecodeAll(src, nil)
	if err != nil {
		t.Fatalf("zstd DecodeAll: %v", err)
	}
	return out
}

func TestCodexZstdCompress_RoundTrip(t *testing.T) {
	src := []byte(`{"model":"gpt-5-codex","stream":true,"input":[{"role":"user","content":"hello hello hello"}]}`)
	compressed, ok := codexZstdCompress(src)
	if !ok {
		t.Fatal("codexZstdCompress returned ok=false for non-empty input")
	}
	if !bytes.HasPrefix(compressed, zstdMagic) {
		t.Fatalf("compressed output missing zstd magic: % x", compressed[:min(4, len(compressed))])
	}
	if bytes.Equal(compressed, src) {
		t.Fatal("compressed output equals input (not actually encoded)")
	}
	if got := zstdDecodeAll(t, compressed); !bytes.Equal(got, src) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, src)
	}

	if _, ok := codexZstdCompress(nil); ok {
		t.Fatal("codexZstdCompress(nil) should return ok=false")
	}
	if _, ok := codexZstdCompress([]byte{}); ok {
		t.Fatal("codexZstdCompress(empty) should return ok=false")
	}
}

// TestCodexZstdCompress_NoContentChecksum is a frame-level regression guard for the
// WithEncoderCRC(false) encoder option: it asserts the produced zstd frame carries
// NO content-checksum trailer, i.e. the Frame_Header_Descriptor's Content_Checksum_flag
// (bit 2, per RFC 8878 §3.1.1.1.1) is 0. Real codex_cli_rs uses libzstd's
// zstd::stream::encode_all(_, 3), which emits no checksum; klauspost defaults crc=true
// (a 4-byte trailer + the flag set). Without WithEncoderCRC(false) a server-side
// reference encoder could flag "this frame has a klauspost checksum" as a non-codex
// tell. If someone drops that option, this test fails loudly.
func TestCodexZstdCompress_NoContentChecksum(t *testing.T) {
	const contentChecksumFlag = 0x04 // FHD bit 2
	src := []byte(`{"model":"gpt-5-codex","stream":true,"input":[{"role":"user","content":"frame descriptor check frame descriptor check"}]}`)
	compressed, ok := codexZstdCompress(src)
	if !ok {
		t.Fatal("codexZstdCompress returned ok=false for non-empty input")
	}
	if len(compressed) < 5 || !bytes.HasPrefix(compressed, zstdMagic) {
		t.Fatalf("not a valid zstd frame: % x", compressed[:min(8, len(compressed))])
	}
	// Byte 4 (immediately after the 4-byte magic) is the Frame_Header_Descriptor.
	if fhd := compressed[4]; fhd&contentChecksumFlag != 0 {
		t.Fatalf("Content_Checksum_flag SET (frame descriptor = 0x%02x): WithEncoderCRC(false) not in effect — frame carries a klauspost checksum that libzstd/real codex never emits", fhd)
	}
	// Sanity: it must still decode back to the original.
	if got := zstdDecodeAll(t, compressed); !bytes.Equal(got, src) {
		t.Fatalf("round-trip mismatch after CRC-off: got %q want %q", got, src)
	}
}

func TestCodexShouldZstdBody(t *testing.T) {
	oauth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}
	apiKey := &cliproxyauth.Auth{ID: "auth-2", Provider: "codex", Attributes: map[string]string{"api_key": "sk-test"}}

	cases := []struct {
		name string
		auth *cliproxyauth.Auth
		want bool
	}{
		// Real codex 0.144.x sends a PLAIN body over HTTP and HTTPS (captured this
		// session), so compression is now disabled for every credential type.
		{"oauth stays plaintext", oauth, false},
		{"api-key stays plaintext", apiKey, false},
		{"nil stays plaintext", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexShouldZstdBody(tc.auth); got != tc.want {
				t.Fatalf("codexShouldZstdBody = %v, want %v", got, tc.want)
			}
		})
	}
}

// oauthCodexAuth is a genuine (non-API-key) codex OAuth credential.
func oauthCodexAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "auth-oauth", Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}
}

// Real codex 0.144.x sends a PLAIN body with no content-encoding on the OAuth path
// (captured this session); this asserts the wire body stays plaintext and matches
// the plaintext upstreamBody verbatim.
func TestCacheHelper_OAuthBodyPlaintext(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := context.Background()
	url := "https://chatgpt.com/backend-api/codex/responses"
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true,"input":[{"role":"user","content":"plain body plain body"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex"}`)}

	httpReq, upstreamBody, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, oauthCodexAuth(), req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	wireBody, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	if bytes.HasPrefix(wireBody, zstdMagic) {
		t.Fatalf("OAuth wire body must NOT be zstd-compressed (real 0.144.x sends plaintext): % x", wireBody[:min(4, len(wireBody))])
	}
	if got := httpReq.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty (real 0.144.x sends no content-encoding)", got)
	}
	// The plaintext wire body must be valid JSON identical to the upstreamBody used
	// for logging.
	if !gjson.ValidBytes(wireBody) {
		t.Fatalf("wire body is not valid JSON: %s", wireBody)
	}
	if !bytes.Equal(wireBody, upstreamBody) {
		t.Fatalf("wire body != plaintext upstreamBody:\n wire=%s\n upstream=%s", wireBody, upstreamBody)
	}
	if got := gjson.GetBytes(wireBody, "model").String(); got != "gpt-5-codex" {
		t.Fatalf("wire body model = %q, want gpt-5-codex", got)
	}
}

func TestCacheHelper_APIKeyBodyPlaintext(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := context.Background()
	url := "https://api.example.com/v1/responses"
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex"}`)}
	apiKeyAuth := &cliproxyauth.Auth{ID: "auth-byok", Provider: "codex", Attributes: map[string]string{"api_key": "sk-user"}}

	httpReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, apiKeyAuth, req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	wireBody, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	if bytes.HasPrefix(wireBody, zstdMagic) {
		t.Fatal("API-key wire body must NOT be zstd-compressed (endpoint may reject it)")
	}
	if got := httpReq.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for API-key path", got)
	}
	if got := gjson.GetBytes(wireBody, "model").String(); got != "gpt-5-codex" {
		t.Fatalf("plaintext model = %q, want gpt-5-codex", got)
	}
}

func TestCacheHelper_NilAuthBodyPlaintext(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := context.Background()
	url := "https://chatgpt.com/backend-api/codex/responses"
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex"}`)}

	httpReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, nil, req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	wireBody, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	if bytes.HasPrefix(wireBody, zstdMagic) {
		t.Fatal("nil-auth wire body must NOT be zstd-compressed (no authenticated identity)")
	}
	if got := httpReq.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty for nil-auth path", got)
	}
}

// TestCacheHelper_OAuthWireBodyReachesServerAsPlaintext is the end-to-end wire check:
// it drives the real cacheHelper output through a real http.Client.Do over a real
// TCP connection to a local server, then asserts the SERVER received a PLAIN body
// with no content-encoding — matching what the real codex 0.144.x client sends
// (captured this session over HTTP and HTTPS). This proves the transport puts the
// plaintext body on the socket verbatim (the segment the pure unit tests can't reach).
func TestCacheHelper_OAuthWireBodyReachesServerAsPlaintext(t *testing.T) {
	var (
		gotEncoding string
		gotBody     []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Content-Encoding")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	executor := &CodexExecutor{}
	ctx := context.Background()
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":false,"input":[{"role":"user","content":"end to end plaintext wire check"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex"}`)}

	httpReq, upstreamBody, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), srv.URL+"/responses", oauthCodexAuth(), req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if gotEncoding != "" {
		t.Fatalf("server saw Content-Encoding = %q, want empty (real 0.144.x sends plaintext)", gotEncoding)
	}
	if bytes.HasPrefix(gotBody, zstdMagic) {
		t.Fatalf("server received zstd body, want plaintext: % x", gotBody[:min(4, len(gotBody))])
	}
	if !bytes.Equal(gotBody, upstreamBody) {
		t.Fatalf("server body != plaintext upstreamBody:\n server=%s\n upstream=%s", gotBody, upstreamBody)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-5-codex" {
		t.Fatalf("server body model = %q, want gpt-5-codex", got)
	}
}
