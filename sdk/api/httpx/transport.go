package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

// GzipFixupTransport wraps an http.RoundTripper to auto-decode gzip responses
// that don't properly set Content-Encoding header.
//
// Some upstream providers (especially when proxied) return gzip-compressed
// responses without setting the Content-Encoding: gzip header, which causes
// Go's http client to pass the compressed bytes directly to the application.
//
// This transport detects gzip magic bytes and transparently decompresses
// the response while preserving streaming behavior for SSE and chunked responses.
type GzipFixupTransport struct {
	// Base is the underlying transport. If nil, http.DefaultTransport is used.
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper
func (t *GzipFixupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}

	// Skip if Go already decompressed it
	if resp.Uncompressed {
		return resp, nil
	}

	// Skip if Content-Encoding is already set (properly configured upstream)
	if resp.Header.Get("Content-Encoding") != "" {
		return resp, nil
	}

	// Skip streaming responses - they need different handling
	if isStreamingResponse(resp) {
		// For streaming responses, wrap with a streaming gzip detector
		// that can handle chunked gzip data
		resp.Body = &streamingGzipDetector{
			inner: resp.Body,
		}
		return resp, nil
	}

	// For non-streaming responses, peek and decompress if needed
	resp.Body = &gzipDetectingReader{
		inner: resp.Body,
	}

	return resp, nil
}

// isStreamingResponse checks if response is SSE or chunked
func isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")

	// Check for Server-Sent Events
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}

	// Check for chunked transfer encoding
	if strings.Contains(strings.ToLower(resp.Header.Get("Transfer-Encoding")), "chunked") {
		return true
	}

	return false
}

// gzipDetectingReader is an io.ReadCloser that detects gzip magic bytes
// on first read and switches to gzip decompression if detected.
// This is used for non-streaming responses.
type gzipDetectingReader struct {
	inner  io.ReadCloser
	reader io.Reader
	once   bool
}

func (g *gzipDetectingReader) Read(p []byte) (int, error) {
	if !g.once {
		g.once = true

		// Peek at first 2 bytes to detect gzip magic bytes
		buf := make([]byte, 2)
		n, err := io.ReadFull(g.inner, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			// Can't peek, use original reader
			g.reader = io.MultiReader(bytes.NewReader(buf[:n]), g.inner)
			return g.reader.Read(p)
		}

		if n >= 2 && buf[0] == 0x1f && buf[1] == 0x8b {
			// It's gzipped, create gzip reader
			multiReader := io.MultiReader(bytes.NewReader(buf[:n]), g.inner)
			gzipReader, err := gzip.NewReader(multiReader)
			if err != nil {
				log.Warnf("gzip header detected but reader creation failed: %v", err)
				g.reader = multiReader
			} else {
				g.reader = gzipReader
			}
		} else {
			// Not gzipped, combine peeked bytes with rest
			g.reader = io.MultiReader(bytes.NewReader(buf[:n]), g.inner)
		}
	}

	return g.reader.Read(p)
}

func (g *gzipDetectingReader) Close() error {
	if closer, ok := g.reader.(io.Closer); ok {
		_ = closer.Close()
	}
	return g.inner.Close()
}

// streamingGzipDetector is similar to gzipDetectingReader but designed for
// streaming responses. It doesn't buffer; it wraps with a streaming gzip reader.
type streamingGzipDetector struct {
	inner  io.ReadCloser
	reader io.Reader
	once   bool
}

func (s *streamingGzipDetector) Read(p []byte) (int, error) {
	if !s.once {
		s.once = true

		// Peek at first 2 bytes
		buf := make([]byte, 2)
		n, err := io.ReadFull(s.inner, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			s.reader = io.MultiReader(bytes.NewReader(buf[:n]), s.inner)
			return s.reader.Read(p)
		}

		if n >= 2 && buf[0] == 0x1f && buf[1] == 0x8b {
			// It's gzipped - wrap with streaming gzip reader
			multiReader := io.MultiReader(bytes.NewReader(buf[:n]), s.inner)
			gzipReader, err := gzip.NewReader(multiReader)
			if err != nil {
				log.Warnf("streaming gzip header detected but reader creation failed: %v", err)
				s.reader = multiReader
			} else {
				s.reader = gzipReader
				log.Debug("streaming gzip decompression enabled")
			}
		} else {
			// Not gzipped
			s.reader = io.MultiReader(bytes.NewReader(buf[:n]), s.inner)
		}
	}

	return s.reader.Read(p)
}

func (s *streamingGzipDetector) Close() error {
	if closer, ok := s.reader.(io.Closer); ok {
		_ = closer.Close()
	}
	return s.inner.Close()
}
