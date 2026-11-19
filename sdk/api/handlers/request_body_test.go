package handlers

import (
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func gzipForTest(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeRequestBodyGzipCap(t *testing.T) {
	plain := []byte(strings.Repeat("a", 4096))
	packed := gzipForTest(t, plain)

	for _, enc := range []string{"gzip", "GZIP", "x-gzip", " identity , gzip "} {
		for _, tc := range []struct {
			name     string
			maxBytes int64
			tooLarge bool
		}{
			{"no cap", 0, false},
			{"exactly at cap", int64(len(plain)), false},
			{"one over cap", int64(len(plain)) - 1, true},
		} {
			t.Run(enc+"/"+tc.name, func(t *testing.T) {
				got, err := decodeRequestBody(packed, enc, tc.maxBytes)
				if tc.tooLarge {
					var maxErr *http.MaxBytesError
					if !errors.As(err, &maxErr) {
						t.Fatalf("err = %v, want *http.MaxBytesError", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if !bytes.Equal(got, plain) {
					t.Fatalf("decoded %d bytes, want %d", len(got), len(plain))
				}
			})
		}
	}
}

func TestDecodeRequestBodyGzipMalformed(t *testing.T) {
	if _, err := decodeRequestBody([]byte("not gzip"), "gzip", 0); err == nil {
		t.Fatal("malformed gzip: want error")
	}
}
