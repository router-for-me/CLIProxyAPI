package helps

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

var codexTestEncodings = []string{CodexUpstreamRequestEncodingZstd, CodexUpstreamRequestEncodingGzip}

func newCompressionTestRequest(t *testing.T, rawURL string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// decodeCodexTestBody decodes raw with the given content coding.
func decodeCodexTestBody(t *testing.T, encoding string, raw []byte) []byte {
	t.Helper()
	switch encoding {
	case CodexUpstreamRequestEncodingZstd:
		dec, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("zstd reader: %v", err)
		}
		defer dec.Close()
		decoded, err := io.ReadAll(dec)
		if err != nil {
			t.Fatalf("zstd decode: %v", err)
		}
		return decoded
	case CodexUpstreamRequestEncodingGzip:
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		decoded, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("gzip decode: %v", err)
		}
		return decoded
	default:
		t.Fatalf("unknown encoding %q", encoding)
		return nil
	}
}

func TestNormalizeCodexUpstreamRequestCompression(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"off":       "",
		"none":      "",
		"false":     "",
		"identity":  "",
		"zstd":      "zstd",
		" ZSTD ":    "zstd",
		"gzip":      "gzip",
		"Gzip":      "gzip",
		"br":        "",
		"deflate":   "",
		"true":      "",
		"zstandard": "",
	}
	for in, want := range cases {
		if got := NormalizeCodexUpstreamRequestCompression(in); got != want {
			t.Fatalf("NormalizeCodexUpstreamRequestCompression(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexUpstreamRequestEncoding(t *testing.T) {
	base := "https://chatgpt.com/backend-api/codex"
	for _, encoding := range codexTestEncodings {
		on := &config.Config{Codex: config.CodexConfig{UpstreamRequestCompression: encoding}}
		cases := []struct {
			name string
			cfg  *config.Config
			url  string
			want string
		}{
			{name: "nil config", cfg: nil, url: base + "/responses", want: ""},
			{name: "disabled", cfg: &config.Config{}, url: base + "/responses", want: ""},
			{name: "off", cfg: &config.Config{Codex: config.CodexConfig{UpstreamRequestCompression: "off"}}, url: base + "/responses", want: ""},
			{name: "unsupported coding", cfg: &config.Config{Codex: config.CodexConfig{UpstreamRequestCompression: "br"}}, url: base + "/responses", want: ""},
			{name: "responses", cfg: on, url: base + "/responses", want: encoding},
			{name: "compact", cfg: on, url: base + "/responses/compact", want: encoding},
			{name: "query string", cfg: on, url: base + "/responses?x=1", want: encoding},
			{name: "trailing slash and query", cfg: on, url: base + "/responses/?x=1", want: encoding},
			{name: "fragment", cfg: on, url: base + "/responses#frag", want: encoding},
			{name: "images", cfg: on, url: base + "/images/generations", want: ""},
			{name: "unrelated suffix", cfg: on, url: base + "/responses_v2", want: ""},
			{name: "host case-insensitive", cfg: on, url: "https://ChatGPT.com/backend-api/codex/responses", want: encoding},
			{name: "custom base url host", cfg: on, url: "https://codex.example.com/v1/responses", want: ""},
			{name: "subdomain is not the backend", cfg: on, url: "https://api.chatgpt.com/backend-api/codex/responses", want: ""},
		}
		for _, tc := range cases {
			t.Run(encoding+"/"+tc.name, func(t *testing.T) {
				req := newCompressionTestRequest(t, tc.url, nil)
				if got := CodexUpstreamRequestEncoding(tc.cfg, req); got != tc.want {
					t.Fatalf("CodexUpstreamRequestEncoding(%q) = %q, want %q", tc.url, got, tc.want)
				}
			})
		}
		if got := CodexUpstreamRequestEncoding(on, nil); got != "" {
			t.Fatalf("nil request must not be compressed, got %q", got)
		}
	}
}

func TestApplyCodexUpstreamRequestCompressionSkipsSmallBodies(t *testing.T) {
	for _, encoding := range codexTestEncodings {
		for _, size := range []int{0, 1, CodexUpstreamRequestCompressionMinBytes - 1} {
			body := bytes.Repeat([]byte("a"), size)
			req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
			applied, err := ApplyCodexUpstreamRequestCompression(req, body, encoding)
			if err != nil {
				t.Fatal(err)
			}
			if applied || req.Header.Get("Content-Encoding") != "" {
				t.Fatalf("%s size %d: body must not be compressed (applied=%v, Content-Encoding=%q)", encoding, size, applied, req.Header.Get("Content-Encoding"))
			}
			got, _ := io.ReadAll(req.Body)
			if !bytes.Equal(got, body) {
				t.Fatalf("%s size %d: body changed", encoding, size)
			}
		}
		if applied, err := ApplyCodexUpstreamRequestCompression(nil, bytes.Repeat([]byte("a"), 4096), encoding); applied || err != nil {
			t.Fatalf("nil request: applied=%v err=%v", applied, err)
		}
	}
}

func TestApplyCodexUpstreamRequestCompressionCompressesAtThreshold(t *testing.T) {
	for _, encoding := range codexTestEncodings {
		body := bytes.Repeat([]byte("a"), CodexUpstreamRequestCompressionMinBytes)
		req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
		applied, err := ApplyCodexUpstreamRequestCompression(req, body, encoding)
		if err != nil {
			t.Fatal(err)
		}
		if !applied || req.Header.Get("Content-Encoding") != encoding {
			t.Fatalf("%s: threshold body must be compressed (applied=%v, Content-Encoding=%q)", encoding, applied, req.Header.Get("Content-Encoding"))
		}
	}
}

func TestApplyCodexUpstreamRequestCompressionRejectsUnknownEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
	req.Header.Set("Content-Encoding", "br")
	applied, err := ApplyCodexUpstreamRequestCompression(req, body, "br")
	if err == nil || applied {
		t.Fatalf("unknown coding must fail without replacing the body (applied=%v, err=%v)", applied, err)
	}
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("stale Content-Encoding %q must be removed even when compression fails", got)
	}
	raw, _ := io.ReadAll(req.Body)
	if !bytes.Equal(raw, body) {
		t.Fatal("identity body changed")
	}
}

func TestApplyCodexUpstreamRequestCompressionReplacesStaleEncodingHeader(t *testing.T) {
	for _, encoding := range codexTestEncodings {
		body := bytes.Repeat([]byte("a"), 4096)
		req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
		// A header override may have set an encoding that does not describe the plain body.
		req.Header.Set("Content-Encoding", "br")
		applied, err := ApplyCodexUpstreamRequestCompression(req, body, encoding)
		if err != nil {
			t.Fatal(err)
		}
		if !applied || req.Header.Get("Content-Encoding") != encoding {
			t.Fatalf("%s: stale header must be replaced (applied=%v, Content-Encoding=%q)", encoding, applied, req.Header.Get("Content-Encoding"))
		}
		if values := req.Header.Values("Content-Encoding"); len(values) != 1 {
			t.Fatalf("Content-Encoding values = %v, want exactly one", values)
		}
		raw, _ := io.ReadAll(req.Body)
		if decoded := decodeCodexTestBody(t, encoding, raw); !bytes.Equal(decoded, body) {
			t.Fatalf("%s: wire body is not the encoding of the identity body", encoding)
		}
	}
}

func TestApplyCodexUpstreamRequestCompressionDropsStaleEncodingOnSkip(t *testing.T) {
	incompressible := make([]byte, 2048)
	if _, err := rand.Read(incompressible); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		body []byte
	}{
		{name: "small body", body: bytes.Repeat([]byte("a"), 16)},
		{name: "incompressible body", body: incompressible},
	}
	for _, encoding := range codexTestEncodings {
		for _, tc := range cases {
			t.Run(encoding+"/"+tc.name, func(t *testing.T) {
				req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", tc.body)
				req.Header.Set("Content-Encoding", "gzip")
				applied, err := ApplyCodexUpstreamRequestCompression(req, tc.body, encoding)
				if err != nil {
					t.Fatal(err)
				}
				if applied {
					t.Fatal("body must not be compressed")
				}
				if got := req.Header.Get("Content-Encoding"); got != "" {
					t.Fatalf("stale Content-Encoding %q must be removed when the identity body is sent", got)
				}
				raw, _ := io.ReadAll(req.Body)
				if !bytes.Equal(raw, tc.body) {
					t.Fatal("identity body changed")
				}
			})
		}
	}
}

func TestApplyCodexUpstreamRequestCompressionSkipsIncompressibleBodies(t *testing.T) {
	body := make([]byte, 2048)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	for _, encoding := range codexTestEncodings {
		req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
		applied, err := ApplyCodexUpstreamRequestCompression(req, body, encoding)
		if err != nil {
			t.Fatal(err)
		}
		if applied || req.Header.Get("Content-Encoding") != "" {
			t.Fatalf("%s: incompressible body must be sent as-is (applied=%v, Content-Encoding=%q)", encoding, applied, req.Header.Get("Content-Encoding"))
		}
		got, _ := io.ReadAll(req.Body)
		if !bytes.Equal(got, body) {
			t.Fatal("incompressible body changed")
		}
	}
}

type trackingReadCloser struct {
	io.Reader
	closed int
}

func (c *trackingReadCloser) Close() error {
	c.closed++
	return nil
}

func TestApplyCodexUpstreamRequestCompressionClosesOriginalBodyOnce(t *testing.T) {
	for _, encoding := range codexTestEncodings {
		body := bytes.Repeat([]byte("a"), 4096)
		req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
		original := &trackingReadCloser{Reader: bytes.NewReader(body)}
		req.Body = original
		applied, err := ApplyCodexUpstreamRequestCompression(req, body, encoding)
		if err != nil {
			t.Fatal(err)
		}
		if !applied {
			t.Fatal("body should have been compressed")
		}
		if original.closed != 1 {
			t.Fatalf("%s: original body closed %d times, want 1", encoding, original.closed)
		}

		// When compression is skipped the caller's body stays in place and open.
		small := bytes.Repeat([]byte("a"), 16)
		req = newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", small)
		kept := &trackingReadCloser{Reader: bytes.NewReader(small)}
		req.Body = kept
		if _, err = ApplyCodexUpstreamRequestCompression(req, small, encoding); err != nil {
			t.Fatal(err)
		}
		if kept.closed != 0 || req.Body != kept {
			t.Fatalf("skipped body must be untouched (closed=%d, replaced=%v)", kept.closed, req.Body != kept)
		}
	}
}

func TestApplyCodexUpstreamRequestCompressionRoundTrip(t *testing.T) {
	body := []byte(`{"model":"gpt-5","instructions":"` + strings.Repeat("You are a helpful assistant. ", 200) + `","input":[]}`)
	for _, encoding := range codexTestEncodings {
		req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
		applied, err := ApplyCodexUpstreamRequestCompression(req, body, encoding)
		if err != nil {
			t.Fatal(err)
		}
		if !applied || req.Header.Get("Content-Encoding") != encoding {
			t.Fatalf("applied=%v Content-Encoding=%q, want %s", applied, req.Header.Get("Content-Encoding"), encoding)
		}
		raw, _ := io.ReadAll(req.Body)
		if int64(len(raw)) != req.ContentLength {
			t.Fatalf("ContentLength = %d, body = %d", req.ContentLength, len(raw))
		}
		if len(raw) >= len(body) {
			t.Fatalf("compressed body %d not smaller than %d", len(raw), len(body))
		}
		if decoded := decodeCodexTestBody(t, encoding, raw); !bytes.Equal(decoded, body) {
			t.Fatalf("%s round trip mismatch", encoding)
		}
		// GetBody must yield an identical copy so transports can replay the request.
		rc, err := req.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		again, _ := io.ReadAll(rc)
		if !bytes.Equal(again, raw) {
			t.Fatal("GetBody returned a different body")
		}
	}
}

// The shared zstd encoder is used from every executor goroutine at once.
func TestApplyCodexUpstreamRequestCompressionZstdConcurrent(t *testing.T) {
	body := []byte(`{"model":"gpt-5","instructions":"` + strings.Repeat("You are a helpful assistant. ", 200) + `","input":[]}`)
	const workers = 16
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			req := newCompressionTestRequest(t, "https://chatgpt.com/backend-api/codex/responses", body)
			applied, err := ApplyCodexUpstreamRequestCompression(req, body, CodexUpstreamRequestEncodingZstd)
			if err != nil || !applied {
				errs <- err
				return
			}
			raw, _ := io.ReadAll(req.Body)
			dec, err := zstd.NewReader(bytes.NewReader(raw))
			if err != nil {
				errs <- err
				return
			}
			decoded, err := io.ReadAll(dec)
			dec.Close()
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(decoded, body) {
				errs <- io.ErrUnexpectedEOF
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent zstd: %v", err)
		}
	}
}
