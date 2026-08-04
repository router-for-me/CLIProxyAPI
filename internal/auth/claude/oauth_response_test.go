package claude

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestReadClaudeOAuthResponseBodyDecodesStackedRepeatedHeaders(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"account":{"uuid":"test"}}`)
	var gzipOutput bytes.Buffer
	gzipWriter := gzip.NewWriter(&gzipOutput)
	if _, errWrite := gzipWriter.Write(payload); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := gzipWriter.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	var brotliOutput bytes.Buffer
	brotliWriter := brotli.NewWriter(&brotliOutput)
	if _, errWrite := brotliWriter.Write(gzipOutput.Bytes()); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := brotliWriter.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	header := make(http.Header)
	header.Add("Content-Encoding", "gzip")
	header.Add("Content-Encoding", "br")
	resp := &http.Response{
		Header: header,
		Body:   io.NopCloser(bytes.NewReader(brotliOutput.Bytes())),
	}
	got, errRead := readClaudeOAuthResponseBody(resp)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decoded body = %q, want %q", got, payload)
	}
}

func TestReadClaudeOAuthResponseBodyDecodesAdvertisedEncodings(t *testing.T) {
	t.Parallel()

	const payload = `{"account":{"uuid":"test"}}`
	tests := []struct {
		name     string
		encoding string
		encode   func(testing.TB, []byte) []byte
	}{
		{
			name:     "gzip",
			encoding: "gzip",
			encode: func(tb testing.TB, input []byte) []byte {
				tb.Helper()
				var output bytes.Buffer
				writer := gzip.NewWriter(&output)
				if _, errWrite := writer.Write(input); errWrite != nil {
					tb.Fatal(errWrite)
				}
				if errClose := writer.Close(); errClose != nil {
					tb.Fatal(errClose)
				}
				return output.Bytes()
			},
		},
		{
			name:     "brotli",
			encoding: "br",
			encode: func(tb testing.TB, input []byte) []byte {
				tb.Helper()
				var output bytes.Buffer
				writer := brotli.NewWriter(&output)
				if _, errWrite := writer.Write(input); errWrite != nil {
					tb.Fatal(errWrite)
				}
				if errClose := writer.Close(); errClose != nil {
					tb.Fatal(errClose)
				}
				return output.Bytes()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resp := &http.Response{
				Header: http.Header{"Content-Encoding": []string{test.encoding}},
				Body:   io.NopCloser(bytes.NewReader(test.encode(t, []byte(payload)))),
			}
			got, errRead := readClaudeOAuthResponseBody(resp)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if string(got) != payload {
				t.Fatalf("decoded body = %q, want %q", got, payload)
			}
		})
	}
}
