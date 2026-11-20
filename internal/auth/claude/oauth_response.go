package claude

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/lzw"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

func readClaudeOAuthResponseBody(resp *http.Response) ([]byte, error) {
	return readClaudeOAuthResponseBodyLimited(resp, 0)
}

// readClaudeOAuthResponseBodyLimited reads and decodes the body, refusing
// more than limit bytes both on the wire and after decompression (a
// zero limit reads unbounded). The bound applies per layer so a
// compression bomb is cut off at the decoder, not after inflating.
func readClaudeOAuthResponseBodyLimited(resp *http.Response, limit int64) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("read Claude OAuth response: body is nil")
	}
	encoded, errRead := readAllLimited(resp.Body, limit)
	if errRead != nil {
		return nil, errRead
	}
	encodings := strings.Split(strings.Join(resp.Header.Values("Content-Encoding"), ","), ",")
	for index := len(encodings) - 1; index >= 0; index-- {
		encoding := strings.ToLower(strings.TrimSpace(encodings[index]))
		if encoding == "" || encoding == "identity" {
			continue
		}
		var errDecode error
		encoded, errDecode = decodeClaudeOAuthEncoding(encoded, encoding, limit)
		if errDecode != nil {
			return nil, errDecode
		}
	}
	return encoded, nil
}

// readAllLimited is io.ReadAll with a size cap: limit <= 0 is unbounded,
// otherwise a body longer than limit bytes is an error rather than a
// truncated success.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	data, errRead := io.ReadAll(io.LimitReader(r, limit+1))
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("read Claude OAuth response: body exceeds %d bytes", limit)
	}
	return data, nil
}

func decodeClaudeOAuthEncoding(encoded []byte, encoding string, limit int64) ([]byte, error) {
	var reader io.ReadCloser
	switch encoding {
	case "gzip":
		gzipReader, errGzip := gzip.NewReader(bytes.NewReader(encoded))
		if errGzip != nil {
			return nil, fmt.Errorf("decode Claude OAuth gzip response: %w", errGzip)
		}
		reader = gzipReader
	case "deflate":
		zlibReader, errZlib := zlib.NewReader(bytes.NewReader(encoded))
		if errZlib == nil {
			reader = zlibReader
		} else {
			reader = flate.NewReader(bytes.NewReader(encoded))
		}
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(encoded)))
	case "compress":
		reader = lzw.NewReader(bytes.NewReader(encoded), lzw.MSB, 8)
	default:
		return nil, fmt.Errorf("decode Claude OAuth response: unsupported content encoding %q", encoding)
	}
	decoded, errDecoded := readAllLimited(reader, limit)
	if errDecoded != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("decode Claude OAuth %s response: %w", encoding, errDecoded)
	}
	if errClose := reader.Close(); errClose != nil {
		return nil, fmt.Errorf("close Claude OAuth %s decoder: %w", encoding, errClose)
	}
	return decoded, nil
}
