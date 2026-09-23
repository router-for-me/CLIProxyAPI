package zstdreq

import (
	"bytes"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestDecodeRequestBodyRoundTrip(t *testing.T) {
	const plaintext = `{"messages":[{"role":"user","content":"hi"}]}`
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

	got, err := DecodeRequestBody(buf.Bytes())
	if err != nil {
		t.Fatalf("DecodeRequestBody: %v", err)
	}
	if string(got) != plaintext {
		t.Fatalf("DecodeRequestBody = %q, want %q", got, plaintext)
	}
}

func TestDecodeRequestBodyRejectsPlaintext(t *testing.T) {
	_, err := DecodeRequestBody([]byte(`{"ok":true}`))
	if err == nil {
		t.Fatal("DecodeRequestBody(plaintext) error = nil, want decode error")
	}
}
