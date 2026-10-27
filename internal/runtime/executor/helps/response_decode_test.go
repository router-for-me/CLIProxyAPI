package helps

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

type responseDecodeTestBody struct {
	io.Reader
	closeCount int
	closeErr   error
}

func (b *responseDecodeTestBody) Close() error {
	b.closeCount++
	return b.closeErr
}

func encodeResponseDecodeTestBody(t *testing.T, encoding string, plain []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	var writer io.WriteCloser
	var errWriter error
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&output)
	case "deflate":
		writer, errWriter = flate.NewWriter(&output, flate.DefaultCompression)
	case "zlib":
		writer = zlib.NewWriter(&output)
	case "br":
		writer = brotli.NewWriter(&output)
	case "zstd":
		writer, errWriter = zstd.NewWriter(&output)
	}
	if errWriter != nil {
		t.Fatal(errWriter)
	}
	if _, errWrite := writer.Write(plain); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	return output.Bytes()
}

func TestDecodeResponseBody(t *testing.T) {
	plain := []byte("data: response.completed\n\n")
	for _, tt := range []struct {
		name      string
		encodings []string
		header    http.Header
	}{
		{name: "plain"},
		{name: "short-plain"},
		{name: "identity", header: http.Header{"Content-Encoding": {"identity"}}},
		{name: "gzip", encodings: []string{"gzip"}, header: http.Header{"Content-Encoding": {"GZIP"}}},
		{name: "gzip-sniff", encodings: []string{"gzip"}},
		{name: "zstd", encodings: []string{"zstd"}, header: http.Header{"Content-Encoding": {"zstd"}}},
		{name: "zstd-sniff", encodings: []string{"zstd"}},
		{name: "brotli", encodings: []string{"br"}, header: http.Header{"Content-Encoding": {"br"}}},
		{name: "deflate-raw", encodings: []string{"deflate"}, header: http.Header{"Content-Encoding": {"deflate"}}},
		{name: "deflate-zlib", encodings: []string{"zlib"}, header: http.Header{"Content-Encoding": {"deflate"}}},
		{name: "stacked", encodings: []string{"gzip", "br", "zstd"}, header: http.Header{"Content-Encoding": {"gzip, br", "zstd"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want := plain
			if tt.name == "short-plain" {
				want = []byte("a")
			}
			wire := want
			for _, encoding := range tt.encodings {
				wire = encodeResponseDecodeTestBody(t, encoding, wire)
			}
			body := &responseDecodeTestBody{Reader: bytes.NewReader(wire)}
			decoded, errDecode := DecodeResponseBody(body, tt.header)
			if errDecode != nil {
				t.Fatal(errDecode)
			}
			got, errRead := io.ReadAll(decoded)
			errClose := decoded.Close()
			if errRead != nil || errClose != nil || !bytes.Equal(got, want) {
				t.Fatalf("decode mismatch: read=%v, close=%v, got=%q", errRead, errClose, got)
			}
			if body.closeCount != 1 {
				t.Fatalf("original body closed %d times, want 1", body.closeCount)
			}
		})
	}
}

func TestDecodeResponseBodySetupErrorsCloseOriginal(t *testing.T) {
	for _, header := range []http.Header{
		{"Content-Encoding": {"unsupported"}},
		{"Content-Encoding": {"gzip"}},
		{"Content-Encoding": {"unsupported, gzip"}},
	} {
		body := &responseDecodeTestBody{Reader: bytes.NewReader([]byte("invalid gzip"))}
		if _, errDecode := DecodeResponseBody(body, header); errDecode == nil {
			t.Fatal("expected decoding error")
		}
		if body.closeCount != 1 {
			t.Fatalf("setup error closed body %d times, want 1", body.closeCount)
		}
	}
	if _, errDecode := DecodeResponseBody(nil, nil); errDecode == nil {
		t.Fatal("nil body must return an error")
	}
}

func TestDecodeResponseBodyCompositeClose(t *testing.T) {
	wantErr := errors.New("first close error")
	var order []int
	body := &compositeReadCloser{closers: []func() error{
		func() error { order = append(order, 1); return wantErr },
		nil,
		func() error { order = append(order, 2); return errors.New("second close error") },
	}}
	if errClose := body.Close(); !errors.Is(errClose, wantErr) || !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("close must attempt all closers and return first error: %v, order=%v", errClose, order)
	}
}
