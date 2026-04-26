package executor

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/klauspost/compress/zstd"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestMaybeEnableCodexRequestCompression_EnabledForOAuth(t *testing.T) {
	t.Setenv(codexCompressionEnv, "1")

	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compress me"}]}]}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"account_id": "acct_123"},
	}

	if err := maybeEnableCodexRequestCompression(req, auth); err != nil {
		t.Fatalf("maybeEnableCodexRequestCompression() error = %v", err)
	}
	if got := req.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding = %q, want zstd", got)
	}

	compressed, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll(req.Body) error = %v", err)
	}
	decoder, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("zstd.NewReader() error = %v", err)
	}
	defer decoder.Close()

	decompressed, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatalf("ReadAll(decoder) error = %v", err)
	}
	if string(decompressed) != string(body) {
		t.Fatalf("decompressed body = %q, want %q", string(decompressed), string(body))
	}
}

func TestMaybeEnableCodexRequestCompression_SkipsAPIKeyAuth(t *testing.T) {
	t.Setenv(codexCompressionEnv, "1")

	body := []byte(`{"model":"gpt-5-codex","input":[]}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}

	if err := maybeEnableCodexRequestCompression(req, auth); err != nil {
		t.Fatalf("maybeEnableCodexRequestCompression() error = %v", err)
	}
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty", got)
	}
	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll(req.Body) error = %v", err)
	}
	if string(gotBody) != string(body) {
		t.Fatalf("request body = %q, want %q", string(gotBody), string(body))
	}
}

func TestMaybeEnableCodexRequestCompressionWithBody_UsesProvidedBody(t *testing.T) {
	t.Setenv(codexCompressionEnv, "1")

	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compress the prepared payload"}]}]}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", bytes.NewReader([]byte(`{"stale":true}`)))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"account_id": "acct_123"},
	}

	if err := maybeEnableCodexRequestCompressionWithBody(req, auth, body); err != nil {
		t.Fatalf("maybeEnableCodexRequestCompressionWithBody() error = %v", err)
	}
	if got := req.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding = %q, want zstd", got)
	}

	compressed, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll(req.Body) error = %v", err)
	}
	decoder, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("zstd.NewReader() error = %v", err)
	}
	defer decoder.Close()

	decompressed, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatalf("ReadAll(decoder) error = %v", err)
	}
	if string(decompressed) != string(body) {
		t.Fatalf("decompressed body = %q, want %q", string(decompressed), string(body))
	}
}

func BenchmarkCompressCodexRequestBody(b *testing.B) {
	body := append([]byte(`{"model":"gpt-5-codex","input":"`), bytes.Repeat([]byte("large codex request payload "), 4096)...)
	body = append(body, []byte(`"}`)...)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		compressed, err := compressCodexRequestBody(body)
		if err != nil {
			b.Fatalf("compressCodexRequestBody() error = %v", err)
		}
		if len(compressed) == 0 {
			b.Fatal("compressed body is empty")
		}
	}
}
