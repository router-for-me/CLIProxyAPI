package helps

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// CodexUpstreamRequestCompressionMinBytes is the smallest request body worth compressing.
// Below this size the content-coding framing and CPU outweigh the saving.
const CodexUpstreamRequestCompressionMinBytes = 1024

// codexUpstreamRequestCompressionHost is the only upstream verified to accept compressed
// request bodies: the official ChatGPT Codex backend. Custom codex-api-key base URLs may
// reject request content codings, so they are never compressed.
const codexUpstreamRequestCompressionHost = "chatgpt.com"

// Content codings accepted by codex.upstream-request-compression.
const (
	CodexUpstreamRequestEncodingGzip = "gzip"
	// CodexUpstreamRequestEncodingZstd is what the official Codex CLI sends to the ChatGPT
	// backend (enable_request_compression), so it is the recommended value.
	CodexUpstreamRequestEncodingZstd = "zstd"
)

// codexZstdLevel mirrors the official Codex CLI, which encodes with zstd level 3.
const codexZstdLevel = 3

var (
	codexZstdEncoderOnce sync.Once
	codexZstdEncoder     *zstd.Encoder
	codexZstdEncoderErr  error
	codexUnknownEncoding sync.Map
)

func codexSharedZstdEncoder() (*zstd.Encoder, error) {
	codexZstdEncoderOnce.Do(func() {
		codexZstdEncoder, codexZstdEncoderErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(codexZstdLevel)),
			zstd.WithEncoderConcurrency(1),
		)
	})
	if codexZstdEncoderErr != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", codexZstdEncoderErr)
	}
	return codexZstdEncoder, nil
}

// NormalizeCodexUpstreamRequestCompression maps the configured value of
// codex.upstream-request-compression to a content coding, or "" when compression is off
// or the value is not recognised.
func NormalizeCodexUpstreamRequestCompression(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexUpstreamRequestEncodingZstd:
		return CodexUpstreamRequestEncodingZstd
	case CodexUpstreamRequestEncodingGzip:
		return CodexUpstreamRequestEncodingGzip
	case "", "off", "none", "false", "identity":
		return ""
	default:
		if _, seen := codexUnknownEncoding.LoadOrStore(value, struct{}{}); !seen {
			log.Warnf("codex.upstream-request-compression: unsupported value %q, sending identity bodies (use zstd, gzip or off)", value)
		}
		return ""
	}
}

// CodexUpstreamRequestEncoding returns the content coding the upstream request body of
// req should be sent with, or "" to send it as-is. Only the Responses endpoints of the
// official ChatGPT Codex backend are compressed: they carry the full conversation on
// every turn and are known to accept zstd (what the official Codex CLI sends) and gzip.
func CodexUpstreamRequestEncoding(cfg *config.Config, req *http.Request) string {
	if cfg == nil || req == nil || req.URL == nil {
		return ""
	}
	encoding := NormalizeCodexUpstreamRequestCompression(cfg.Codex.UpstreamRequestCompression)
	if encoding == "" {
		return ""
	}
	if !strings.EqualFold(req.URL.Hostname(), codexUpstreamRequestCompressionHost) {
		return ""
	}
	path := strings.TrimSuffix(req.URL.Path, "/")
	if strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact") {
		return encoding
	}
	return ""
}

func codexCompressBody(body []byte, encoding string) ([]byte, error) {
	switch encoding {
	case CodexUpstreamRequestEncodingZstd:
		enc, err := codexSharedZstdEncoder()
		if err != nil {
			return nil, err
		}
		return enc.EncodeAll(body, make([]byte, 0, len(body)/3)), nil
	case CodexUpstreamRequestEncodingGzip:
		var buf bytes.Buffer
		buf.Grow(len(body) / 3)
		zw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
		if err != nil {
			return nil, fmt.Errorf("create gzip writer: %w", err)
		}
		if _, err = zw.Write(body); err != nil {
			return nil, fmt.Errorf("gzip request body: %w", err)
		}
		if err = zw.Close(); err != nil {
			return nil, fmt.Errorf("close gzip writer: %w", err)
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported content coding %q", encoding)
	}
}

// ApplyCodexUpstreamRequestCompression replaces the request body with its encoding
// (zstd or gzip) and updates Content-Encoding, ContentLength and GetBody to match. body
// must be the identity-encoded bytes currently held by req.Body, so any Content-Encoding
// header set earlier (for example through a custom header override) cannot describe that
// body: it is always removed, and set only once the body has actually been replaced. It
// must run after all request headers are final, so that nothing can desync the header
// from the body. It returns false, leaving the identity body in place, when the body is
// smaller than CodexUpstreamRequestCompressionMinBytes or when compression would not
// make it smaller.
func ApplyCodexUpstreamRequestCompression(req *http.Request, body []byte, encoding string) (bool, error) {
	if req == nil {
		return false, nil
	}
	req.Header.Del("Content-Encoding")
	if len(body) < CodexUpstreamRequestCompressionMinBytes {
		return false, nil
	}
	compressed, err := codexCompressBody(body, encoding)
	if err != nil {
		return false, err
	}
	if len(compressed) >= len(body) {
		return false, nil
	}
	if req.Body != nil {
		if errClose := req.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("codex upstream request compression: close original request body")
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(compressed))
	req.ContentLength = int64(len(compressed))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressed)), nil
	}
	req.Header.Set("Content-Encoding", encoding)
	return true, nil
}
