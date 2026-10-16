// Package httpcompression implements HTTP content codings without changing transport fingerprints.
package httpcompression

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

type readCloser struct {
	io.Reader
	close func() error
}

func (r *readCloser) Close() error { return r.close() }

// Decode returns a streaming decoder and closes every decoder and the original body.
func Decode(body io.ReadCloser, encoding string) (io.ReadCloser, error) {
	var reader io.Reader = body
	closers := []func() error{body.Close}
	closeAll := func() error {
		var first error
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i](); first == nil {
				first = err
			}
		}
		return first
	}
	parts := strings.Split(encoding, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		switch enc := strings.ToLower(strings.TrimSpace(parts[i])); enc {
		case "", "identity":
		case "gzip":
			d, e := gzip.NewReader(reader)
			if e != nil {
				_ = closeAll()
				return nil, e
			}
			reader = d
			closers = append(closers, d.Close)
		case "br":
			reader = brotli.NewReader(reader)
		case "zstd":
			d, e := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1))
			if e != nil {
				_ = closeAll()
				return nil, e
			}
			reader = d
			closers = append(closers, func() error { d.Close(); return nil })
		default:
			_ = closeAll()
			return nil, fmt.Errorf("unsupported content encoding: %s", enc)
		}
	}
	return &readCloser{Reader: reader, close: closeAll}, nil
}

// Writer is a streaming content encoder. Flush must be called at each SSE boundary.
type Writer interface {
	io.WriteCloser
	Flush() error
}

func Encode(w io.Writer, encoding string) (Writer, error) {
	switch encoding {
	case "gzip":
		return gzip.NewWriter(w), nil
	case "br":
		return brotli.NewWriter(w), nil
	case "zstd":
		return zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
	}
	return nil, fmt.Errorf("unsupported content encoding: %s", encoding)
}

// Transport decodes explicitly encoded upstream responses while preserving the selected transport.
// It deliberately does not advertise compressed request support or override Accept-Encoding.
type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	res, err := base.RoundTrip(req)
	if err != nil || res == nil || res.Body == nil || req.Method == http.MethodHead || res.StatusCode == 101 {
		return res, err
	}
	enc := strings.Join(res.Header.Values("Content-Encoding"), ",")
	if enc == "" || strings.EqualFold(enc, "identity") {
		return res, nil
	}
	// Leave unknown codings to provider-specific decoders (for example deflate).
	for _, part := range strings.Split(enc, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "gzip", "br", "zstd", "identity", "":
		default:
			return res, nil
		}
	}
	decoded, err := Decode(res.Body, enc)
	if err != nil {
		// A content decoding failure is a body error, not a connection failure.
		// Preserve status and coding metadata for provider-specific error handling.
		res.Body = &failedBody{err: err}
		return res, nil
	}
	res.Body = decoded
	res.Header.Del("Content-Encoding")
	res.Header.Del("Content-Length")
	res.ContentLength = -1
	res.Uncompressed = true
	return res, nil
}

func (t Transport) CloseIdleConnections() {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if closer, ok := base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type failedBody struct{ err error }

func (b *failedBody) Read([]byte) (int, error) { return 0, b.err }
func (b *failedBody) Close() error             { return nil }
