package handlers

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// ReadRawZstdRequestBody reads the inbound request body and decodes zstd
// Content-Encoding values so JSON handlers always receive JSON bytes.
func ReadRawZstdRequestBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, fmt.Errorf("request is nil")
	}
	body, err := c.GetRawData()
	if err != nil {
		return nil, err
	}
	decoded, decodedEncoding, err := DecodeZstdRequestBody(body, c.GetHeader("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	if decodedEncoding {
		c.Request.ContentLength = int64(len(decoded))
		c.Request.Header.Del("Content-Encoding")
		c.Request.Header.Del("Content-Length")
	}
	return decoded, nil
}

// DecodeZstdRequestBody decodes zstd-encoded HTTP request bodies.
func DecodeZstdRequestBody(body []byte, contentEncoding string) ([]byte, bool, error) {
	encodings := parseContentEncodings(contentEncoding)
	if len(encodings) == 0 {
		return body, false, nil
	}

	decoded := body
	decodedEncoding := false
	for i := len(encodings) - 1; i >= 0; i-- {
		encoding := encodings[i]
		switch encoding {
		case "", "identity":
			continue
		case "zstd", "zstandard":
			out, err := decodeZstd(decoded)
			if err != nil {
				return nil, false, fmt.Errorf("decode zstd request body: %w", err)
			}
			decoded = out
			decodedEncoding = true
		default:
			return nil, false, fmt.Errorf("unsupported content encoding %q", encoding)
		}
	}
	return decoded, decodedEncoding, nil
}

func parseContentEncodings(contentEncoding string) []string {
	if strings.TrimSpace(contentEncoding) == "" {
		return nil
	}
	parts := strings.Split(contentEncoding, ",")
	encodings := make([]string, 0, len(parts))
	for _, part := range parts {
		encoding := strings.ToLower(strings.TrimSpace(part))
		if encoding == "" {
			continue
		}
		encodings = append(encodings, encoding)
	}
	return encodings
}

func decodeZstd(body []byte) ([]byte, error) {
	reader, err := zstd.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
