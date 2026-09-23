// Package zstdreq exports a byte-level zstd request decoder.
// It lives under sdk so other modules can import it; internal is not importable.
package zstdreq

import (
	"bytes"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// DecodeRequestBody decompresses one complete zstd-encoded request body.
// raw must contain the full frame; a truncated body returns an error.
func DecodeRequestBody(raw []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", err)
	}
	defer decoder.Close()

	decoded, err := io.ReadAll(decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", err)
	}
	return decoded, nil
}
