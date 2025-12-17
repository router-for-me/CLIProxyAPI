package util

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func mkResp(status int, hdr http.Header, body []byte) *http.Response {
	if hdr == nil {
		hdr = http.Header{}
	}
	return &http.Response{
		StatusCode:    status,
		Header:        hdr,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func TestIsStreamingResponse(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{
			name:   "sse",
			header: http.Header{"Content-Type": []string{"text/event-stream"}},
			want:   true,
		},
		{
			name:   "chunked_not_streaming",
			header: http.Header{"Transfer-Encoding": []string{"chunked"}},
			want:   false,
		},
		{
			name:   "normal_json",
			header: http.Header{"Content-Type": []string{"application/json"}},
			want:   false,
		},
		{
			name:   "empty",
			header: http.Header{},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: tc.header}
			got := IsStreamingResponse(resp)
			if got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestDecompressGzipIfNeeded_Scenarios(t *testing.T) {
	goodJSON := []byte(`{"ok":true}`)
	good := gzipBytes(goodJSON)
	truncated := good[:10]
	corrupted := append([]byte{0x1f, 0x8b}, []byte("notgzip")...)

	cases := []struct {
		name     string
		header   http.Header
		body     []byte
		status   int
		wantBody []byte
		wantCE   string
	}{
		{
			name:     "decompresses_valid_gzip_no_header",
			header:   http.Header{},
			body:     good,
			status:   200,
			wantBody: goodJSON,
			wantCE:   "",
		},
		{
			name:     "skips_when_ce_present",
			header:   http.Header{"Content-Encoding": []string{"gzip"}},
			body:     good,
			status:   200,
			wantBody: good,
			wantCE:   "gzip",
		},
		{
			name:     "passes_truncated_unchanged",
			header:   http.Header{},
			body:     truncated,
			status:   200,
			wantBody: truncated,
			wantCE:   "",
		},
		{
			name:     "passes_corrupted_unchanged",
			header:   http.Header{},
			body:     corrupted,
			status:   200,
			wantBody: corrupted,
			wantCE:   "",
		},
		{
			name:     "non_gzip_unchanged",
			header:   http.Header{},
			body:     []byte("plain"),
			status:   200,
			wantBody: []byte("plain"),
			wantCE:   "",
		},
		{
			name:     "empty_body",
			header:   http.Header{},
			body:     []byte{},
			status:   200,
			wantBody: []byte{},
			wantCE:   "",
		},
		{
			name:     "single_byte_body",
			header:   http.Header{},
			body:     []byte{0x1f},
			status:   200,
			wantBody: []byte{0x1f},
			wantCE:   "",
		},
		{
			name:     "skips_non_2xx_status",
			header:   http.Header{},
			body:     good,
			status:   404,
			wantBody: good,
			wantCE:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := mkResp(tc.status, tc.header, tc.body)
			if err := DecompressGzipIfNeeded(resp); err != nil {
				t.Fatalf("DecompressGzipIfNeeded error: %v", err)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll error: %v", err)
			}
			if !bytes.Equal(got, tc.wantBody) {
				t.Fatalf("body mismatch:\nwant: %q\ngot:  %q", tc.wantBody, got)
			}
			if ce := resp.Header.Get("Content-Encoding"); ce != tc.wantCE {
				t.Fatalf("Content-Encoding: want %q, got %q", tc.wantCE, ce)
			}
		})
	}
}

func TestDecompressGzipIfNeeded_UpdatesContentLengthHeader(t *testing.T) {
	goodJSON := []byte(`{"message":"test response"}`)
	gzipped := gzipBytes(goodJSON)

	resp := mkResp(200, http.Header{
		"Content-Length": []string{fmt.Sprintf("%d", len(gzipped))},
	}, gzipped)

	if err := DecompressGzipIfNeeded(resp); err != nil {
		t.Fatalf("DecompressGzipIfNeeded error: %v", err)
	}

	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, goodJSON) {
		t.Fatalf("body should be decompressed, got: %q, want: %q", got, goodJSON)
	}

	wantCL := fmt.Sprintf("%d", len(goodJSON))
	gotCL := resp.Header.Get("Content-Length")
	if gotCL != wantCL {
		t.Fatalf("Content-Length header mismatch: want %q (decompressed), got %q", wantCL, gotCL)
	}

	if resp.ContentLength != int64(len(goodJSON)) {
		t.Fatalf("resp.ContentLength mismatch: want %d, got %d", len(goodJSON), resp.ContentLength)
	}
}

func TestDecompressGzipIfNeeded_SkipsStreamingResponses(t *testing.T) {
	goodJSON := []byte(`{"ok":true}`)
	gzipped := gzipBytes(goodJSON)

	t.Run("sse_skips_decompression", func(t *testing.T) {
		resp := mkResp(200, http.Header{"Content-Type": []string{"text/event-stream"}}, gzipped)
		if err := DecompressGzipIfNeeded(resp); err != nil {
			t.Fatalf("DecompressGzipIfNeeded error: %v", err)
		}
		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, gzipped) {
			t.Fatal("SSE response should not be decompressed")
		}
	})
}

func TestDecompressGzipIfNeeded_DecompressesChunkedJSON(t *testing.T) {
	goodJSON := []byte(`{"ok":true}`)
	gzipped := gzipBytes(goodJSON)

	t.Run("chunked_json_decompresses", func(t *testing.T) {
		resp := mkResp(200, http.Header{"Transfer-Encoding": []string{"chunked"}}, gzipped)
		if err := DecompressGzipIfNeeded(resp); err != nil {
			t.Fatalf("DecompressGzipIfNeeded error: %v", err)
		}
		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, goodJSON) {
			t.Fatalf("chunked JSON should be decompressed, got: %q, want: %q", got, goodJSON)
		}
	})
}
