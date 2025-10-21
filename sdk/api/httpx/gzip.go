// Package httpx provides HTTP transport utilities for SDK clients,
// including automatic gzip decompression for misconfigured upstreams.
package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
)

// DecodePossibleGzip inspects the raw response body and transparently
// decompresses it when the payload is gzip compressed. Some upstream
// providers return gzip data without a Content-Encoding header, which
// confuses clients expecting JSON. This helper restores the original
// JSON bytes while leaving plain responses untouched.
//
// This function is preserved for backward compatibility but new code
// should use GzipFixupTransport instead.
func DecodePossibleGzip(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		decompressed, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
		return decompressed, nil
	}
	return raw, nil
}
