package helps

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// RequestCompressionCapable reports whether the given provider path is
// business-verified to accept a compressed upstream request body. The
// capability matrix lives in code (not config) because support differs per
// provider and per path, and was established by live probing:
//   - claude: accepts Content-Encoding: gzip (zstd is rejected with a 400).
//   - codex: accepts Content-Encoding: zstd (gzip is rejected with a 400);
//     matches the official Codex CLI enable_request_compression behavior.
//   - kimi: NOT capable. Live probing contradicts an early assumption: BOTH
//     Kimi endpoints reject compressed request bodies with a 400 — the native
//     /v1/chat/completions path AND the Anthropic-compatible /v1/messages
//     path served by the ClaudeExecutor embedded in KimiExecutor (gzip, both
//     streaming and non-streaming). Kimi therefore stays uncompressed.
func RequestCompressionCapable(provider string) bool {
	return RequestCompressionDefaultEncoding(provider) != ""
}

// RequestCompressionDefaultEncoding returns the encoding verified to work for
// the given provider, or "" when the provider has no verified compression
// support.
func RequestCompressionDefaultEncoding(provider string) string {
	switch provider {
	case "claude":
		return "gzip"
	case "codex":
		return "zstd"
	default:
		return ""
	}
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		writer, err := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		if err != nil {
			return gzip.NewWriter(io.Discard)
		}
		return writer
	},
}

var (
	zstdEncoderOnce sync.Once
	zstdEncoder     *zstd.Encoder
	zstdEncoderErr  error
)

type requestCompressionProviderOverrideKey struct{}

// WithRequestCompressionProvider returns a context that makes
// MaybeCompressRequestBody use providerOverride instead of the executing
// provider's own identity.
func WithRequestCompressionProvider(ctx context.Context, providerOverride string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestCompressionProviderOverrideKey{}, providerOverride)
}

// requestCompressionProvider resolves the effective provider identity for
// compression decisions: the context override when set, else the executing
// provider.
func requestCompressionProvider(ctx context.Context, provider string) string {
	if ctx != nil {
		if override, ok := ctx.Value(requestCompressionProviderOverrideKey{}).(string); ok && override != "" {
			return override
		}
	}
	return provider
}

// requestCompressionEncodingForProvider resolves the effective Content-Encoding
// for a provider from the configured mode:
//   - "off" -> "" (never compress)
//   - "auto" -> the provider's verified default encoding
func requestCompressionEncodingForProvider(cfg *config.Config, provider string) (encoding, mode string) {
	if cfg == nil {
		return "", config.RequestCompressionOff
	}

	mode = cfg.EffectiveRequestCompressionMode()
	if mode != config.RequestCompressionAuto {
		return "", mode
	}
	return RequestCompressionDefaultEncoding(provider), mode
}

// MaybeCompressRequestBody compresses an upstream request body according to
// cfg.RequestCompression and the provider's verified capability (see
// RequestCompressionCapable). Bodies below the configured threshold are
// returned unchanged. The returned encoding is the Content-Encoding value to
// set on the request, or "" when the body was not modified. Invalid direct
// configuration values disable compression and log a warning.
func MaybeCompressRequestBody(cfg *config.Config, provider string, body []byte) ([]byte, string) {
	return MaybeCompressRequestBodyContext(nil, cfg, provider, body)
}

// MaybeCompressRequestBodyContext is MaybeCompressRequestBody with a context
// that may carry a provider identity override set via
// WithRequestCompressionProvider.
func MaybeCompressRequestBodyContext(ctx context.Context, cfg *config.Config, provider string, body []byte) ([]byte, string) {
	provider = requestCompressionProvider(ctx, provider)
	if cfg == nil || len(body) == 0 {
		return body, ""
	}

	encoding, mode := requestCompressionEncodingForProvider(cfg, provider)
	if encoding == "" {
		rawMode := strings.ToLower(strings.TrimSpace(cfg.RequestCompression))
		if rawMode != "" && rawMode != config.RequestCompressionOff && rawMode != config.RequestCompressionAuto {
			log.Warnf("request compression: unsupported mode %q for provider %s, sending uncompressed", cfg.RequestCompression, provider)
		}
		return body, ""
	}

	minBytes := cfg.EffectiveRequestCompressionMinBytes()
	if len(body) < minBytes {
		return body, ""
	}

	var (
		compressed []byte
		err        error
	)
	switch encoding {
	case "gzip":
		compressed, err = compressGzip(body)
	case "zstd":
		compressed, err = compressZstd(body)
	default:
		err = fmt.Errorf("unsupported encoding %q", encoding)
	}
	if err != nil {
		log.Warnf("request compression: failed to compress %d bytes for provider %s with %s: %v; sending uncompressed", len(body), provider, encoding, err)
		return body, ""
	}

	ratio := float64(len(compressed)) / float64(len(body))
	log.Debugf("request compression: provider=%s mode=%s encoding=%s original_bytes=%d compressed_bytes=%d ratio=%.3f", provider, mode, encoding, len(body), len(compressed), ratio)
	return compressed, encoding
}

func compressGzip(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(body) / 2)

	writer, ok := gzipWriterPool.Get().(*gzip.Writer)
	if !ok || writer == nil {
		writer = gzip.NewWriter(io.Discard)
	}
	defer func() {
		writer.Reset(io.Discard)
		gzipWriterPool.Put(writer)
	}()

	writer.Reset(&buf)
	if _, err := writer.Write(body); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

func compressZstd(body []byte) (compressed []byte, err error) {
	encoder, err := requestCompressionZstdEncoder()
	if err != nil {
		return nil, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			compressed = nil
			err = fmt.Errorf("zstd encode: %v", recovered)
		}
	}()
	return encoder.EncodeAll(body, nil), nil
}

func requestCompressionZstdEncoder() (*zstd.Encoder, error) {
	zstdEncoderOnce.Do(func() {
		zstdEncoder, zstdEncoderErr = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1),
		)
	})
	if zstdEncoderErr != nil {
		return nil, fmt.Errorf("create zstd encoder: %w", zstdEncoderErr)
	}
	return zstdEncoder, nil
}
