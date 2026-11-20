package handlers

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

func gzipBody(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, errWrite := w.Write(plain); errWrite != nil {
		t.Fatalf("gzip write: %v", errWrite)
	}
	if errClose := w.Close(); errClose != nil {
		t.Fatalf("gzip close: %v", errClose)
	}
	return buf.Bytes()
}

func zstdBody(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, errNew := zstd.NewWriter(&buf)
	if errNew != nil {
		t.Fatalf("zstd writer: %v", errNew)
	}
	if _, errWrite := w.Write(plain); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := w.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}
	return buf.Bytes()
}

func newBodyRequest(t *testing.T, body []byte, encoding string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/v1/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return c, rec
}

func TestReadRequestBodyDecodesSupportedEncodings(t *testing.T) {
	plain := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	for _, tc := range []struct {
		name     string
		encoding string
		body     []byte
	}{
		{name: "plain", encoding: "", body: plain},
		{name: "identity", encoding: "identity", body: plain},
		{name: "zstd", encoding: "zstd", body: zstdBody(t, plain)},
		{name: "gzip", encoding: "gzip", body: gzipBody(t, plain)},
		{name: "x-gzip", encoding: "x-gzip", body: gzipBody(t, plain)},
		{name: "gzip_upper", encoding: "GZIP", body: gzipBody(t, plain)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newBodyRequest(t, tc.body, tc.encoding)
			decoded, err := ReadRequestBody(c)
			if err != nil {
				t.Fatalf("ReadRequestBody: %v", err)
			}
			if !bytes.Equal(decoded, plain) {
				t.Fatalf("decoded = %q, want %q", decoded, plain)
			}
		})
	}
}

func TestReadRequestBodyGzipOverCap(t *testing.T) {
	plain := bytes.Repeat([]byte("a"), 1024)
	c, _ := newBodyRequest(t, gzipBody(t, plain), "gzip")
	c.Set(MaxRequestBodyBytesContextKey, int64(64))

	if _, err := ReadRequestBody(c); err == nil {
		t.Fatalf("expected max bytes error for oversized gzip body")
	} else {
		var maxErr *http.MaxBytesError
		if !asMaxBytesErr(err, &maxErr) {
			t.Fatalf("error = %v (%T), want *http.MaxBytesError", err, err)
		}
	}
}

func TestReadRequestBodyMalformedGzip(t *testing.T) {
	// A gzip-looking body that is not valid JSON and cannot be gunzipped must fail
	// rather than fall through as compressed bytes.
	c, _ := newBodyRequest(t, []byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad}, "gzip")
	if _, err := ReadRequestBody(c); err == nil {
		t.Fatalf("expected decode error for malformed gzip body")
	}
}

func asMaxBytesErr(err error, target **http.MaxBytesError) bool {
	for err != nil {
		if mbe, ok := err.(*http.MaxBytesError); ok {
			*target = mbe
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
