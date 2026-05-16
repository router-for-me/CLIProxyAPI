package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func TestDecodeZstdRequestBody(t *testing.T) {
	want := []byte(`{"model":"gpt-5.5","stream":true}`)
	encoded := encodeZstdForTest(t, want)

	got, decoded, err := DecodeZstdRequestBody(encoded, "zstd")
	if err != nil {
		t.Fatalf("DecodeZstdRequestBody() error = %v", err)
	}
	if !decoded {
		t.Fatal("DecodeZstdRequestBody() decoded = false, want true")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DecodeZstdRequestBody() = %q, want %q", got, want)
	}
}

func TestReadRawZstdRequestBodyDecodesAndStripsEncodingHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	want := []byte(`{"model":"gpt-5.5","input":"hello"}`)
	encoded := encodeZstdForTest(t, want)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(encoded))
	req.Header.Set("Content-Encoding", "zstd")
	req.Header.Set("Content-Length", "999")
	c.Request = req

	got, err := ReadRawZstdRequestBody(c)
	if err != nil {
		t.Fatalf("ReadRawZstdRequestBody() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ReadRawZstdRequestBody() = %q, want %q", got, want)
	}
	if gotEncoding := c.Request.Header.Get("Content-Encoding"); gotEncoding != "" {
		t.Fatalf("Content-Encoding header = %q, want empty", gotEncoding)
	}
	if gotLength := c.Request.Header.Get("Content-Length"); gotLength != "" {
		t.Fatalf("Content-Length header = %q, want empty", gotLength)
	}
	if c.Request.ContentLength != int64(len(want)) {
		t.Fatalf("ContentLength = %d, want %d", c.Request.ContentLength, len(want))
	}
}

func TestDecodeZstdRequestBodyUnsupportedEncoding(t *testing.T) {
	_, _, err := DecodeZstdRequestBody([]byte("body"), "deflate")
	if err == nil {
		t.Fatal("DecodeZstdRequestBody() error = nil, want unsupported encoding error")
	}
}

func encodeZstdForTest(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter() error = %v", err)
	}
	if _, err = writer.Write(body); err != nil {
		t.Fatalf("zstd writer Write() error = %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("zstd writer Close() error = %v", err)
	}
	return buf.Bytes()
}
