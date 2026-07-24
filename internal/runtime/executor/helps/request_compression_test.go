package helps

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func testConfig(mode string) *config.Config {
	return &config.Config{SDKConfig: config.SDKConfig{RequestCompression: mode}}
}

func bigBody() []byte {
	return bytes.Repeat([]byte(`{"role":"user","content":"hello world"} `), 4096)
}

func TestMaybeCompressRequestBodyOff(t *testing.T) {
	body := bigBody()
	for _, mode := range []string{"", "off", "OFF"} {
		for _, provider := range []string{"claude", "codex", "kimi"} {
			got, enc := MaybeCompressRequestBody(testConfig(mode), provider, body)
			if enc != "" {
				t.Fatalf("mode %q provider %s: expected empty encoding, got %q", mode, provider, enc)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("mode %q provider %s: body was modified while disabled", mode, provider)
			}
		}
	}
}

func TestMaybeCompressRequestBodyAutoUsesProviderDefault(t *testing.T) {
	body := bigBody()
	cases := map[string]string{
		"claude": "gzip",
		"codex":  "zstd",
		"kimi":   "",
		"gemini": "",
	}
	for provider, wantEnc := range cases {
		_, enc := MaybeCompressRequestBody(testConfig("auto"), provider, body)
		if enc != wantEnc {
			t.Fatalf("auto provider %s: expected encoding %q, got %q", provider, wantEnc, enc)
		}
	}
}

func TestMaybeCompressRequestBodyInvalidModeFailsClosed(t *testing.T) {
	body := bigBody()
	for _, mode := range []string{"gzip", "zstd", "br"} {
		got, enc := MaybeCompressRequestBody(testConfig(mode), "claude", body)
		if enc != "" {
			t.Fatalf("mode %q: expected passthrough, got %q", mode, enc)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("mode %q: body was modified", mode)
		}
	}
}

func TestMaybeCompressRequestBodyThreshold(t *testing.T) {
	cfg := testConfig("auto")
	cfg.RequestCompressionMinSize = "16k"

	belowThreshold := bytes.Repeat([]byte("x"), config.DefaultRequestCompressionMinBytes-1)
	got, enc := MaybeCompressRequestBody(cfg, "claude", belowThreshold)
	if enc != "" || !bytes.Equal(got, belowThreshold) {
		t.Fatalf("expected passthrough below threshold, got encoding=%q len=%d", enc, len(got))
	}

	atThreshold := bytes.Repeat([]byte("x"), config.DefaultRequestCompressionMinBytes)
	_, enc = MaybeCompressRequestBody(cfg, "claude", atThreshold)
	if enc != "gzip" {
		t.Fatalf("expected compression at threshold, got %q", enc)
	}
}

func TestMaybeCompressRequestBodyCustomThreshold(t *testing.T) {
	cfg := testConfig("auto")
	cfg.RequestCompressionMinSize = "1024k"
	body := bytes.Repeat([]byte("y"), 64<<10)
	_, enc := MaybeCompressRequestBody(cfg, "claude", body)
	if enc != "" {
		t.Fatalf("expected passthrough below custom threshold, got %q", enc)
	}
}

func TestMaybeCompressRequestBodyRoundtrip(t *testing.T) {
	body := bigBody()

	gzipBody, enc := MaybeCompressRequestBody(testConfig("auto"), "claude", body)
	if enc != "gzip" {
		t.Fatalf("expected gzip, got %q", enc)
	}
	gzReader, err := gzip.NewReader(bytes.NewReader(gzipBody))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	roundtrip, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if err := gzReader.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if !bytes.Equal(roundtrip, body) {
		t.Fatalf("gzip roundtrip mismatch")
	}

	zstdBody, enc := MaybeCompressRequestBody(testConfig("auto"), "codex", body)
	if enc != "zstd" {
		t.Fatalf("expected zstd, got %q", enc)
	}
	zsReader, err := zstd.NewReader(bytes.NewReader(zstdBody))
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer zsReader.Close()
	roundtrip, err = io.ReadAll(zsReader)
	if err != nil {
		t.Fatalf("zstd read: %v", err)
	}
	if !bytes.Equal(roundtrip, body) {
		t.Fatalf("zstd roundtrip mismatch")
	}
}

func TestMaybeCompressRequestBodyNilConfig(t *testing.T) {
	body := []byte(`{}`)
	got, enc := MaybeCompressRequestBody(nil, "claude", body)
	if enc != "" || !bytes.Equal(got, body) {
		t.Fatalf("expected passthrough for nil config")
	}
}

func TestRequestCompressionCapabilityMatrix(t *testing.T) {
	if !RequestCompressionCapable("claude") || !RequestCompressionCapable("codex") {
		t.Fatalf("claude and codex must be compression capable")
	}
	for _, provider := range []string{"kimi", "kimi/anthropic", "gemini", "antigravity", ""} {
		if RequestCompressionCapable(provider) {
			t.Fatalf("provider %q must not be compression capable", provider)
		}
	}
	if got := RequestCompressionDefaultEncoding("claude"); got != "gzip" {
		t.Fatalf("claude default encoding: expected gzip, got %q", got)
	}
	if got := RequestCompressionDefaultEncoding("codex"); got != "zstd" {
		t.Fatalf("codex default encoding: expected zstd, got %q", got)
	}
	if got := RequestCompressionDefaultEncoding("kimi"); got != "" {
		t.Fatalf("kimi default encoding: expected empty, got %q", got)
	}
	if got := RequestCompressionDefaultEncoding("kimi/anthropic"); got != "" {
		t.Fatalf("kimi/anthropic default encoding: expected empty, got %q", got)
	}
}

func TestMaybeCompressRequestBodyContextProviderOverride(t *testing.T) {
	body := bigBody()
	ctx := WithRequestCompressionProvider(nil, "kimi")
	_, enc := MaybeCompressRequestBodyContext(ctx, testConfig("auto"), "claude", body)
	if enc != "" {
		t.Fatalf("override to incapable provider: expected passthrough, got %q", enc)
	}
	_, enc = MaybeCompressRequestBodyContext(nil, testConfig("auto"), "claude", body)
	if enc != "gzip" {
		t.Fatalf("no override: expected gzip, got %q", enc)
	}
}

func TestMaybeCompressRequestBodyGzipWriterReuse(t *testing.T) {
	cfg := testConfig("auto")
	bodies := [][]byte{
		bytes.Repeat([]byte("first payload "), 4096),
		bytes.Repeat([]byte("second payload "), 4096),
	}
	for _, body := range bodies {
		compressed, encoding := MaybeCompressRequestBody(cfg, "claude", body)
		if encoding != "gzip" {
			t.Fatalf("encoding: got %q, want gzip", encoding)
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		decompressed, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read gzip body: %v", err)
		}
		if err := reader.Close(); err != nil {
			t.Fatalf("close gzip reader: %v", err)
		}
		if !bytes.Equal(decompressed, body) {
			t.Fatalf("gzip writer reuse corrupted body")
		}
	}
}

func TestMaybeCompressRequestBodyConcurrent(t *testing.T) {
	cfg := testConfig("auto")
	type compressionRequest struct {
		provider string
		body     []byte
	}
	cases := make([]compressionRequest, 0, 32)
	for index := 0; index < cap(cases); index++ {
		provider := "claude"
		if index%2 == 1 {
			provider = "codex"
		}
		cases = append(cases, compressionRequest{
			provider: provider,
			body:     bytes.Repeat([]byte{byte(index)}, config.DefaultRequestCompressionMinBytes),
		})
	}

	var group sync.WaitGroup
	errs := make(chan error, len(cases))
	for _, request := range cases {
		group.Add(1)
		go func(request compressionRequest) {
			defer group.Done()
			compressed, encoding := MaybeCompressRequestBody(cfg, request.provider, request.body)
			var decompressed []byte
			var err error
			switch encoding {
			case "gzip":
				reader, errReader := gzip.NewReader(bytes.NewReader(compressed))
				if errReader != nil {
					errs <- errReader
					return
				}
				decompressed, err = io.ReadAll(reader)
				if errClose := reader.Close(); err == nil {
					err = errClose
				}
			case "zstd":
				reader, errReader := zstd.NewReader(bytes.NewReader(compressed))
				if errReader != nil {
					errs <- errReader
					return
				}
				decompressed, err = io.ReadAll(reader)
				reader.Close()
			default:
				errs <- fmt.Errorf("provider %s returned encoding %q", request.provider, encoding)
				return
			}
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(decompressed, request.body) {
				errs <- fmt.Errorf("provider %s returned corrupted body", request.provider)
			}
		}(request)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
