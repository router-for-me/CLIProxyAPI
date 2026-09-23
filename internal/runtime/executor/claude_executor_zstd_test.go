package executor

import (
	"bytes"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/zstdreq"
)

func TestDecodeResponseBodyMagicByteZstdMatchesExportedDecoder(t *testing.T) {
	const plaintext = `{"type":"error","error":{"message":"zstd magic"}}`
	var buf bytes.Buffer
	encoder, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err = encoder.Write([]byte(plaintext)); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err = encoder.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	compressed := append([]byte(nil), buf.Bytes()...)

	want, err := zstdreq.DecodeRequestBody(compressed)
	if err != nil {
		t.Fatalf("DecodeRequestBody: %v", err)
	}

	decoded, err := decodeResponseBody(io.NopCloser(bytes.NewReader(compressed)), "")
	if err != nil {
		t.Fatalf("decodeResponseBody: %v", err)
	}
	defer decoded.Close()
	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("magic-byte decode = %q, DecodeRequestBody = %q", got, want)
	}
	if string(got) != plaintext {
		t.Fatalf("decoded = %q, want %q", got, plaintext)
	}
}
