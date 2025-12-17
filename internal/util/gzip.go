package util

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

// ReadCloser wraps a reader and forwards Close to a separate closer.
// Used to restore peeked bytes while preserving upstream body Close behavior.
type ReadCloser struct {
	R io.Reader
	C io.Closer
}

func (rc *ReadCloser) Read(p []byte) (int, error) { return rc.R.Read(p) }
func (rc *ReadCloser) Close() error               { return rc.C.Close() }

// IsStreamingResponse detects if the response is streaming (SSE only).
// Note: We only treat text/event-stream as streaming. Chunked transfer encoding
// is a transport-level detail and doesn't mean we can't decompress the full response.
// Many JSON APIs use chunked encoding for normal responses.
func IsStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	return strings.Contains(contentType, "text/event-stream")
}

// DecompressGzipIfNeeded detects gzip magic bytes and decompresses the response body in place.
// It updates the Content-Length header after decompression.
// Returns nil if no decompression was needed or if decompression succeeded.
// The function:
//   - Only processes 2xx responses
//   - Skips responses with Content-Encoding header already set
//   - Skips streaming responses (SSE)
//   - Detects gzip via magic bytes (0x1f 0x8b), not headers
//   - Handles edge cases (empty body, truncated gzip, corrupted gzip)
func DecompressGzipIfNeeded(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	if resp.Header.Get("Content-Encoding") != "" {
		return nil
	}

	if IsStreamingResponse(resp) {
		return nil
	}

	originalBody := resp.Body

	header := make([]byte, 2)
	n, _ := io.ReadFull(originalBody, header)

	if n >= 2 && header[0] == 0x1f && header[1] == 0x8b {
		rest, err := io.ReadAll(originalBody)
		if err != nil {
			resp.Body = &ReadCloser{
				R: io.MultiReader(bytes.NewReader(header[:n]), originalBody),
				C: originalBody,
			}
			return nil
		}

		gzippedData := append(header[:n], rest...)

		gzipReader, err := gzip.NewReader(bytes.NewReader(gzippedData))
		if err != nil {
			log.Warnf("gzip header detected but decompress failed: %v", err)
			_ = originalBody.Close()
			resp.Body = io.NopCloser(bytes.NewReader(gzippedData))
			return nil
		}

		decompressed, err := io.ReadAll(gzipReader)
		_ = gzipReader.Close()
		if err != nil {
			log.Warnf("gzip decompress error: %v", err)
			_ = originalBody.Close()
			resp.Body = io.NopCloser(bytes.NewReader(gzippedData))
			return nil
		}

		_ = originalBody.Close()

		resp.Body = io.NopCloser(bytes.NewReader(decompressed))
		resp.ContentLength = int64(len(decompressed))

		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))

		log.Debugf("decompressed gzip response (%d -> %d bytes)", len(gzippedData), len(decompressed))
	} else {
		resp.Body = &ReadCloser{
			R: io.MultiReader(bytes.NewReader(header[:n]), originalBody),
			C: originalBody,
		}
	}

	return nil
}
