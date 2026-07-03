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

func TestCodexShouldZstdBody(t *testing.T) {
	oauth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}
	apiKey := &cliproxyauth.Auth{ID: "auth-2", Provider: "codex", Attributes: map[string]string{"api_key": "sk-test"}}

	cases := []struct {
		name string
		auth *cliproxyauth.Auth
		want bool
	}{
		{"oauth compresses", oauth, true},
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

func TestCacheHelper_OAuthBodyZstdCompressed(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := context.Background()
	url := "https://chatgpt.com/backend-api/codex/responses"
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true,"input":[{"role":"user","content":"compress me compress me"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex"}`)}

	httpReq, upstreamBody, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, oauthCodexAuth(), req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	wireBody, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	if !bytes.HasPrefix(wireBody, zstdMagic) {
		t.Fatalf("OAuth wire body is not zstd-compressed: % x", wireBody[:min(4, len(wireBody))])
	}
	if got := httpReq.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding = %q, want %q", got, "zstd")
	}
	// ContentLength must reflect the compressed body so h2 sends the right frame.
	if httpReq.ContentLength != int64(len(wireBody)) {
		t.Fatalf("ContentLength = %d, want %d (compressed length)", httpReq.ContentLength, len(wireBody))
	}
	// The compressed wire body must decode to valid JSON identical to the plaintext
	// upstreamBody returned for logging.
	decoded := zstdDecodeAll(t, wireBody)
	if !gjson.ValidBytes(decoded) {
		t.Fatalf("decoded wire body is not valid JSON: %s", decoded)
	}
	if !bytes.Equal(decoded, upstreamBody) {
		t.Fatalf("decoded wire body != plaintext upstreamBody:\n decoded=%s\n upstream=%s", decoded, upstreamBody)
	}
	if got := gjson.GetBytes(decoded, "model").String(); got != "gpt-5-codex" {
		t.Fatalf("decoded model = %q, want gpt-5-codex", got)
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

// TestCacheHelper_OAuthWireBodyReachesServerAsZstd is the end-to-end wire check:
// it drives the real cacheHelper output through a real http.Client.Do over a real
// TCP connection to a local server, then asserts the SERVER received a zstd body
// advertised via content-encoding: zstd that decodes back to the plaintext. This
// proves the transport puts the compressed body on the socket verbatim (the
// segment the pure unit tests can't reach) — stronger evidence than sniffing an
// encrypted capture of the chatgpt.com connection, which would only show ciphertext.
func TestCacheHelper_OAuthWireBodyReachesServerAsZstd(t *testing.T) {
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
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":false,"input":[{"role":"user","content":"end to end zstd wire check"}]}`)
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

	if gotEncoding != "zstd" {
		t.Fatalf("server saw Content-Encoding = %q, want zstd", gotEncoding)
	}
	if !bytes.HasPrefix(gotBody, zstdMagic) {
		t.Fatalf("server received non-zstd body: % x", gotBody[:min(4, len(gotBody))])
	}
	decoded := zstdDecodeAll(t, gotBody)
	if !bytes.Equal(decoded, upstreamBody) {
		t.Fatalf("server body decoded != plaintext upstreamBody:\n decoded=%s\n upstream=%s", decoded, upstreamBody)
	}
	if got := gjson.GetBytes(decoded, "model").String(); got != "gpt-5-codex" {
		t.Fatalf("server decoded model = %q, want gpt-5-codex", got)
	}
}
