package amp

import (
	"bytes"
	"compress/gzip"
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
