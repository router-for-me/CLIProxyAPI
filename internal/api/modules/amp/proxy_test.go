package amp

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Helper: compress data with gzip
func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// Helper: create a mock http.Response
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

func TestCreateReverseProxy_ValidURL(t *testing.T) {
	proxy, err := createReverseProxy("http://example.com", NewStaticSecretSource("key"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if proxy == nil {
		t.Fatal("expected proxy to be created")
	}
}

func TestCreateReverseProxy_InvalidURL(t *testing.T) {
	_, err := createReverseProxy("://invalid", NewStaticSecretSource("key"))
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestModifyResponse_GzipScenarios(t *testing.T) {
	proxy, err := createReverseProxy("http://example.com", NewStaticSecretSource("k"))
	if err != nil {
		t.Fatal(err)
	}

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
			if err := proxy.ModifyResponse(resp); err != nil {
				t.Fatalf("ModifyResponse error: %v", err)
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

func TestModifyResponse_UpdatesContentLengthHeader(t *testing.T) {
	proxy, err := createReverseProxy("http://example.com", NewStaticSecretSource("k"))
	if err != nil {
		t.Fatal(err)
	}

	goodJSON := []byte(`{"message":"test response"}`)
	gzipped := gzipBytes(goodJSON)

	// Simulate upstream response with gzip body AND Content-Length header
	// (this is the scenario the bot flagged - stale Content-Length after decompression)
	resp := mkResp(200, http.Header{
		"Content-Length": []string{fmt.Sprintf("%d", len(gzipped))}, // Compressed size
	}, gzipped)

	if err := proxy.ModifyResponse(resp); err != nil {
		t.Fatalf("ModifyResponse error: %v", err)
	}

	// Verify body is decompressed
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, goodJSON) {
		t.Fatalf("body should be decompressed, got: %q, want: %q", got, goodJSON)
	}

	// Verify Content-Length header is updated to decompressed size
	wantCL := fmt.Sprintf("%d", len(goodJSON))
	gotCL := resp.Header.Get("Content-Length")
	if gotCL != wantCL {
		t.Fatalf("Content-Length header mismatch: want %q (decompressed), got %q", wantCL, gotCL)
	}

	// Verify struct field also matches
	if resp.ContentLength != int64(len(goodJSON)) {
		t.Fatalf("resp.ContentLength mismatch: want %d, got %d", len(goodJSON), resp.ContentLength)
	}
}

func TestModifyResponse_SkipsStreamingResponses(t *testing.T) {
	proxy, err := createReverseProxy("http://example.com", NewStaticSecretSource("k"))
	if err != nil {
		t.Fatal(err)
	}

	goodJSON := []byte(`{"ok":true}`)
	gzipped := gzipBytes(goodJSON)

	t.Run("sse_skips_decompression", func(t *testing.T) {
		resp := mkResp(200, http.Header{"Content-Type": []string{"text/event-stream"}}, gzipped)
		if err := proxy.ModifyResponse(resp); err != nil {
			t.Fatalf("ModifyResponse error: %v", err)
		}
		// SSE should NOT be decompressed
		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, gzipped) {
			t.Fatal("SSE response should not be decompressed")
		}
	})
}

func TestModifyResponse_DecompressesChunkedJSON(t *testing.T) {
	proxy, err := createReverseProxy("http://example.com", NewStaticSecretSource("k"))
	if err != nil {
		t.Fatal(err)
	}

	goodJSON := []byte(`{"ok":true}`)
	gzipped := gzipBytes(goodJSON)

	t.Run("chunked_json_decompresses", func(t *testing.T) {
		// Chunked JSON responses (like thread APIs) should be decompressed
		resp := mkResp(200, http.Header{"Transfer-Encoding": []string{"chunked"}}, gzipped)
		if err := proxy.ModifyResponse(resp); err != nil {
			t.Fatalf("ModifyResponse error: %v", err)
		}
		// Should decompress because it's not SSE
		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, goodJSON) {
			t.Fatalf("chunked JSON should be decompressed, got: %q, want: %q", got, goodJSON)
		}
	})
}

func TestReverseProxy_InjectsHeaders(t *testing.T) {
	gotHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders <- r.Header.Clone()
		w.WriteHeader(200)
		w.Write([]byte(`ok`))
	}))
	defer upstream.Close()

	proxy, err := createReverseProxy(upstream.URL, NewStaticSecretSource("secret"))
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/test")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	hdr := <-gotHeaders
	if hdr.Get("X-Api-Key") != "secret" {
		t.Fatalf("X-Api-Key missing or wrong, got: %q", hdr.Get("X-Api-Key"))
	}
	if hdr.Get("Authorization") != "Bearer secret" {
		t.Fatalf("Authorization missing or wrong, got: %q", hdr.Get("Authorization"))
	}
}

func TestReverseProxy_EmptySecret(t *testing.T) {
	gotHeaders := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders <- r.Header.Clone()
		w.WriteHeader(200)
		w.Write([]byte(`ok`))
	}))
	defer upstream.Close()

	proxy, err := createReverseProxy(upstream.URL, NewStaticSecretSource(""))
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/test")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	hdr := <-gotHeaders
	// Should NOT inject headers when secret is empty
	if hdr.Get("X-Api-Key") != "" {
		t.Fatalf("X-Api-Key should not be set, got: %q", hdr.Get("X-Api-Key"))
	}
	if authVal := hdr.Get("Authorization"); authVal != "" && authVal != "Bearer " {
		t.Fatalf("Authorization should not be set, got: %q", authVal)
	}
}

func TestReverseProxy_ErrorHandler(t *testing.T) {
	// Point proxy to a non-routable address to trigger error
	proxy, err := createReverseProxy("http://127.0.0.1:1", NewStaticSecretSource(""))
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/any")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", res.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"amp_upstream_proxy_error"`)) {
		t.Fatalf("unexpected body: %s", body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: want application/json, got %s", ct)
	}
}

func TestReverseProxy_FullRoundTrip_Gzip(t *testing.T) {
	// Upstream returns gzipped JSON without Content-Encoding header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(gzipBytes([]byte(`{"upstream":"ok"}`)))
	}))
	defer upstream.Close()

	proxy, err := createReverseProxy(upstream.URL, NewStaticSecretSource("key"))
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	expected := []byte(`{"upstream":"ok"}`)
	if !bytes.Equal(body, expected) {
		t.Fatalf("want decompressed JSON, got: %s", body)
	}
}

func TestReverseProxy_FullRoundTrip_PlainJSON(t *testing.T) {
	// Upstream returns plain JSON
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"plain":"json"}`))
	}))
	defer upstream.Close()

	proxy, err := createReverseProxy(upstream.URL, NewStaticSecretSource("key"))
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	expected := []byte(`{"plain":"json"}`)
	if !bytes.Equal(body, expected) {
		t.Fatalf("want plain JSON unchanged, got: %s", body)
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
			want:   false, // Chunked is transport-level, not streaming
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
			got := isStreamingResponse(resp)
			if got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestFilterBetaFeatures(t *testing.T) {
	tests := []struct {
		name            string
		header          string
		featureToRemove string
		expected        string
	}{
		{
			name:            "Remove context-1m from middle",
			header:          "fine-grained-tool-streaming-2025-05-14,context-1m-2025-08-07,oauth-2025-04-20",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "fine-grained-tool-streaming-2025-05-14,oauth-2025-04-20",
		},
		{
			name:            "Remove context-1m from start",
			header:          "context-1m-2025-08-07,fine-grained-tool-streaming-2025-05-14",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "fine-grained-tool-streaming-2025-05-14",
		},
		{
			name:            "Remove context-1m from end",
			header:          "fine-grained-tool-streaming-2025-05-14,context-1m-2025-08-07",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "fine-grained-tool-streaming-2025-05-14",
		},
		{
			name:            "Feature not present",
			header:          "fine-grained-tool-streaming-2025-05-14,oauth-2025-04-20",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "fine-grained-tool-streaming-2025-05-14,oauth-2025-04-20",
		},
		{
			name:            "Only feature to remove",
			header:          "context-1m-2025-08-07",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "",
		},
		{
			name:            "Empty header",
			header:          "",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "",
		},
		{
			name:            "Header with spaces",
			header:          "fine-grained-tool-streaming-2025-05-14, context-1m-2025-08-07 , oauth-2025-04-20",
			featureToRemove: "context-1m-2025-08-07",
			expected:        "fine-grained-tool-streaming-2025-05-14,oauth-2025-04-20",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterBetaFeatures(tt.header, tt.featureToRemove)
			if result != tt.expected {
				t.Errorf("filterBetaFeatures() = %q, want %q", result, tt.expected)
			}
		})
	}
}
