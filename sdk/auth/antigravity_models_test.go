package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestNormalizeAntigravityModelID(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"gemini-3-flash", "gemini-3-flash"},
		{"  GEMINI-3-FLASH  ", "gemini-3-flash"},
		{"", ""},
		{"   ", ""},
		{strings.Repeat("a", 256), strings.Repeat("a", 256)},
		{strings.Repeat("a", 257), ""},
	}
	for _, c := range cases {
		got := NormalizeAntigravityModelID(c.input)
		if got != c.expected {
			t.Errorf("NormalizeAntigravityModelID(%q) = %q, want %q", c.input, got, c.expected)
		}
	}
}

func TestParseAntigravityModelCapabilityHints(t *testing.T) {
	t.Run("valid payload with models and web search", func(t *testing.T) {
		body := []byte(`{"models":{"gemini-3-flash":{"displayName":"Gemini 3 Flash"},"claude-sonnet-5-5-medium":{"displayName":"Claude Sonnet 5.5"}},"webSearchModelIds":["gemini-3-flash"]}`)
		hints, ok := ParseAntigravityModelCapabilityHints(body)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if hints.ModelIDs == nil {
			t.Fatal("expected non-nil ModelIDs")
		}
		if len(hints.ModelIDs) != 2 {
			t.Errorf("got %d models, want 2", len(hints.ModelIDs))
		}
		if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
			t.Errorf("missing gemini-3-flash in ModelIDs")
		}
		if len(hints.WebSearchModelIDs) != 1 {
			t.Errorf("got %d webSearchModelIds, want 1", len(hints.WebSearchModelIDs))
		}
		if _, exists := hints.WebSearchModelIDs["gemini-3-flash"]; !exists {
			t.Errorf("missing gemini-3-flash in WebSearchModelIDs")
		}
	})

	t.Run("empty catalog is authoritative empty", func(t *testing.T) {
		body := []byte(`{"models":{}}`)
		hints, ok := ParseAntigravityModelCapabilityHints(body)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if hints.ModelIDs == nil {
			t.Fatal("expected non-nil empty ModelIDs")
		}
		if len(hints.ModelIDs) != 0 {
			t.Errorf("expected 0 models, got %d", len(hints.ModelIDs))
		}
	})

	t.Run("missing models field leaves ModelIDs nil", func(t *testing.T) {
		body := []byte(`{"webSearchModelIds":["gemini-3.5-flash-lite"]}`)
		hints, ok := ParseAntigravityModelCapabilityHints(body)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if hints.ModelIDs != nil {
			t.Fatal("expected nil ModelIDs for unknown catalog")
		}
		if len(hints.WebSearchModelIDs) != 1 {
			t.Errorf("got %d webSearchModelIds, want 1", len(hints.WebSearchModelIDs))
		}
	})

	t.Run("invalid json returns false", func(t *testing.T) {
		_, ok := ParseAntigravityModelCapabilityHints([]byte(`{not valid json`))
		if ok {
			t.Fatal("expected false for invalid JSON")
		}
	})

	t.Run("clones model IDs out of caller buffer", func(t *testing.T) {
		buf := []byte(`{"models":{"gemini-3-flash":{}},"webSearchModelIds":["gemini-3-flash"]}`)
		hints, ok := ParseAntigravityModelCapabilityHints(buf)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		for i := range buf {
			buf[i] = 'x'
		}
		if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
			t.Fatal("expected gemini-3-flash in ModelIDs after buffer mutation")
		}
		if _, exists := hints.WebSearchModelIDs["gemini-3-flash"]; !exists {
			t.Fatal("expected gemini-3-flash in WebSearchModelIDs after buffer mutation")
		}
	})
}

func TestPersistedAntigravityCapabilityHintsRoundTrip(t *testing.T) {
	hints := AntigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{
			"gemini-3-flash": {},
			"gemini-4-argon": {},
		},
		WebSearchModelIDs: map[string]struct{}{
			"gemini-3-flash": {},
		},
	}
	project := "aicode-consumers"
	endpointWithUser := "https://user:password@cloudcode-pa.googleapis.com"
	expectedEndpoint := "https://cloudcode-pa.googleapis.com"

	serialized := BuildPersistedAntigravityCapabilityHints(project, endpointWithUser, hints)
	if ep, ok := serialized["endpoint"].(string); !ok || ep != expectedEndpoint {
		t.Errorf("expected endpoint %q, got %q", expectedEndpoint, ep)
	}

	metadata := map[string]any{
		AntigravityPersistedHintsMetaKey: serialized,
	}

	parsed, ok := ParsePersistedAntigravityCapabilityHints(metadata, project, expectedEndpoint)
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if len(parsed.ModelIDs) != 2 {
		t.Errorf("got %d models, want 2", len(parsed.ModelIDs))
	}
	if len(parsed.WebSearchModelIDs) != 1 {
		t.Errorf("got %d web search models, want 1", len(parsed.WebSearchModelIDs))
	}

	// Mismatched project
	if _, okMismatch := ParsePersistedAntigravityCapabilityHints(metadata, "other-project", expectedEndpoint); okMismatch {
		t.Error("expected mismatch for different project")
	}

	// Mismatched endpoint
	if _, okMismatch := ParsePersistedAntigravityCapabilityHints(metadata, project, "https://other.com"); okMismatch {
		t.Error("expected mismatch for different endpoint")
	}

	// Nil ModelIDs (unknown catalog) must not serialize as authoritative empty catalog
	if gotNil := BuildPersistedAntigravityCapabilityHints(project, expectedEndpoint, AntigravityModelCapabilityHints{}); gotNil != nil {
		t.Errorf("expected nil persisted hints for unknown catalog, got %v", gotNil)
	}
}

func TestFetchAntigravityAvailableModels(t *testing.T) {
	t.Run("successful probe", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("unexpected authorization header: %s", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}},"webSearchModelIds":["gemini-3-flash"]}`))
		}))
		defer server.Close()

		hints, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{server.URL},
			AccessToken: "test-token",
			ProjectID:   "aicode-consumers",
		})
		if status != AntigravityProbeStatusSuccess {
			t.Fatalf("expected success, got %v", status)
		}
		if len(hints.ModelIDs) != 1 {
			t.Errorf("got %d models, want 1", len(hints.ModelIDs))
		}
	})

	t.Run("auth error 401", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{server.URL},
			AccessToken: "bad-token",
		})
		if status != AntigravityProbeStatusAuthError {
			t.Fatalf("expected auth error, got %v", status)
		}
	})

	t.Run("forbidden 403 distinct from 401 auth error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{server.URL},
			AccessToken: "forbidden-token",
		})
		if status != AntigravityProbeStatusForbidden {
			t.Fatalf("expected forbidden status, got %v", status)
		}
	})

	t.Run("empty base urls returns transient error", func(t *testing.T) {
		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    nil,
			AccessToken: "token",
		})
		if status != AntigravityProbeStatusTransientError {
			t.Fatalf("expected transient error, got %v", status)
		}
	})

	t.Run("empty access token returns auth error", func(t *testing.T) {
		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{"https://example.com"},
			AccessToken: "",
		})
		if status != AntigravityProbeStatusAuthError {
			t.Fatalf("expected auth error, got %v", status)
		}
	})

	t.Run("redirect is not followed", func(t *testing.T) {
		redirectTargetVisited := false
		targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectTargetVisited = true
			w.WriteHeader(http.StatusOK)
		}))
		defer targetServer.Close()

		sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, targetServer.URL, http.StatusFound)
		}))
		defer sourceServer.Close()

		_, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{sourceServer.URL},
			AccessToken: "token",
		})
		if redirectTargetVisited {
			t.Fatal("redirect target should not have been visited")
		}
		if status != AntigravityProbeStatusTransientError {
			t.Fatalf("expected transient error for 302 without redirect, got %v", status)
		}
	})
}

func TestFetchAntigravityAvailableModels_EnforcesHTTP11WithoutALPN(t *testing.T) {
	var mu sync.Mutex
	var clientHelloProtos []string
	var requestProto string
	var tlsVersion uint16
	var negotiatedProtocol string

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestProto = r.Proto
		if r.TLS != nil {
			tlsVersion = r.TLS.Version
			negotiatedProtocol = r.TLS.NegotiatedProtocol
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			clientHelloProtos = append([]string(nil), hello.SupportedProtos...)
			mu.Unlock()
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	var proxyConnCount int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusMethodNotAllowed)
			return
		}
		targetConn, errDial := net.Dial("tcp", r.Host)
		if errDial != nil {
			http.Error(w, errDial.Error(), http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			if errClose := targetConn.Close(); errClose != nil {
				t.Logf("close target conn error: %v", errClose)
			}
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		clientConn, bufRW, errHijack := hijacker.Hijack()
		if errHijack != nil {
			if errClose := targetConn.Close(); errClose != nil {
				t.Logf("close target conn error: %v", errClose)
			}
			return
		}
		mu.Lock()
		proxyConnCount++
		mu.Unlock()
		if _, errWrite := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			if errClose := clientConn.Close(); errClose != nil {
				t.Logf("close client conn error: %v", errClose)
			}
			if errClose := targetConn.Close(); errClose != nil {
				t.Logf("close target conn error: %v", errClose)
			}
			return
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(targetConn, bufRW)
			if tcpConn, okTCP := targetConn.(*net.TCPConn); okTCP {
				_ = tcpConn.CloseWrite()
			}
		}()
		go func() {
			defer wg.Done()
			_, _ = io.Copy(clientConn, targetConn)
			if tcpConn, okTCP := clientConn.(*net.TCPConn); okTCP {
				_ = tcpConn.CloseWrite()
			}
		}()
		wg.Wait()
		if errClose := targetConn.Close(); errClose != nil {
			t.Logf("close target conn error: %v", errClose)
		}
		if errClose := clientConn.Close(); errClose != nil {
			t.Logf("close client conn error: %v", errClose)
		}
	}))
	defer proxyServer.Close()

	certPool := x509.NewCertPool()
	certPool.AddCert(server.Certificate())

	origDefault := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = origDefault
	})

	baseDefault, ok := origDefault.(*http.Transport)
	if !ok || baseDefault == nil {
		t.Fatal("expected http.DefaultTransport to be *http.Transport")
	}
	baseTransport := baseDefault.Clone()
	baseTransport.TLSClientConfig = &tls.Config{
		RootCAs:    certPool,
		NextProtos: []string{"h2", "http/1.1"},
	}
	http.DefaultTransport = baseTransport

	cases := []struct {
		name      string
		proxyURL  string
		wantProxy int
	}{
		{name: "empty proxy inherits default transport", proxyURL: "", wantProxy: 0},
		{name: "direct proxy", proxyURL: "direct", wantProxy: 0},
		{name: "http connect proxy", proxyURL: proxyServer.URL, wantProxy: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			clientHelloProtos = nil
			requestProto = ""
			tlsVersion = 0
			negotiatedProtocol = ""
			proxyConnCount = 0
			mu.Unlock()

			hints, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
				BaseURLs:    []string{server.URL},
				ProxyURL:    tc.proxyURL,
				AccessToken: "test-token",
				ProjectID:   "aicode-consumers",
			})
			if status != AntigravityProbeStatusSuccess {
				t.Fatalf("status = %v, want %v", status, AntigravityProbeStatusSuccess)
			}
			if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
				t.Fatalf("missing gemini-3-flash in hints: %+v", hints)
			}

			mu.Lock()
			gotHelloProtos := slices.Clone(clientHelloProtos)
			gotProto := requestProto
			gotTLSVersion := tlsVersion
			gotNegotiated := negotiatedProtocol
			gotProxyCount := proxyConnCount
			mu.Unlock()

			if len(gotHelloProtos) != 0 {
				t.Fatalf("ClientHello ALPN = %v, want no ALPN extension", gotHelloProtos)
			}
			if gotProto != "HTTP/1.1" {
				t.Fatalf("request protocol = %q, want HTTP/1.1", gotProto)
			}
			if gotTLSVersion != tls.VersionTLS13 {
				t.Fatalf("TLS version = %#x, want TLS 1.3", gotTLSVersion)
			}
			if gotNegotiated != "" {
				t.Fatalf("negotiated ALPN = %q, want empty", gotNegotiated)
			}
			if gotProxyCount != tc.wantProxy {
				t.Fatalf("proxy CONNECT count = %d, want %d", gotProxyCount, tc.wantProxy)
			}
			if !slices.Equal(baseTransport.TLSClientConfig.NextProtos, []string{"h2", "http/1.1"}) {
				t.Fatalf("default transport TLSClientConfig.NextProtos mutated: %v", baseTransport.TLSClientConfig.NextProtos)
			}
		})
	}
}

type stubRoundTripper func(*http.Request) (*http.Response, error)

func (f stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFetchAntigravityAvailableModels_PreservesCustomDefaultRoundTripper(t *testing.T) {
	origDefault := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = origDefault
	})

	called := false
	http.DefaultTransport = stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"models":{"gemini-3-flash":{}}}`)),
			Request:    r,
		}, nil
	})

	hints, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
		BaseURLs:    []string{"https://custom-roundtripper.invalid"},
		AccessToken: "test-token",
	})
	if !called {
		t.Fatal("expected custom http.DefaultTransport RoundTripper to be called")
	}
	if status != AntigravityProbeStatusSuccess {
		t.Fatalf("status = %v, want %v", status, AntigravityProbeStatusSuccess)
	}
	if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
		t.Fatalf("missing gemini-3-flash in hints: %+v", hints)
	}
}

func TestNewAntigravityProbeTransport_HTTP11AndNoALPN(t *testing.T) {
	origDefault := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = origDefault
	})

	t.Run("handles typed-nil DefaultTransport", func(t *testing.T) {
		var typedNil *http.Transport
		http.DefaultTransport = typedNil

		tr, errTr := newAntigravityProbeTransport("")
		if errTr != nil {
			t.Fatalf("unexpected error: %v", errTr)
		}
		if tr == nil {
			t.Fatal("expected non-nil fallback *http.Transport")
		}
		if tr.ForceAttemptHTTP2 {
			t.Fatal("ForceAttemptHTTP2 = true, want false")
		}
		if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
			t.Fatalf("TLSNextProto = %#v, want non-nil empty map", tr.TLSNextProto)
		}
		if tr.TLSClientConfig == nil || len(tr.TLSClientConfig.NextProtos) != 0 {
			t.Fatalf("TLSClientConfig = %#v, want non-nil config with empty NextProtos", tr.TLSClientConfig)
		}
	})

	t.Run("configures all proxy modes", func(t *testing.T) {
		http.DefaultTransport = origDefault
		for _, proxyURL := range []string{
			"",
			"direct",
			"http://127.0.0.1:8080",
			"https://127.0.0.1:8443",
			"socks5://127.0.0.1:1080",
			"socks5h://127.0.0.1:1080",
		} {
			tr, errTr := newAntigravityProbeTransport(proxyURL)
			if errTr != nil {
				t.Fatalf("proxy %q unexpected error: %v", proxyURL, errTr)
			}
			if tr == nil {
				t.Fatalf("proxy %q returned nil transport", proxyURL)
			}
			if tr.Protocols != nil {
				t.Fatalf("proxy %q Protocols = %v, want nil", proxyURL, tr.Protocols)
			}
			if tr.ForceAttemptHTTP2 {
				t.Fatalf("proxy %q ForceAttemptHTTP2 = true, want false", proxyURL)
			}
			if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
				t.Fatalf("proxy %q TLSNextProto = %#v, want non-nil empty map", proxyURL, tr.TLSNextProto)
			}
			if tr.TLSClientConfig == nil || len(tr.TLSClientConfig.NextProtos) != 0 {
				t.Fatalf("proxy %q TLSClientConfig = %#v, want non-nil config with empty NextProtos", proxyURL, tr.TLSClientConfig)
			}
			tr.CloseIdleConnections()
		}
	})

	t.Run("strips explicit DefaultTransport Protocols HTTP/2 over real TLS", func(t *testing.T) {
		var mu sync.Mutex
		var clientHelloProtos []string
		var requestProto string
		var negotiatedProtocol string

		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requestProto = r.Proto
			if r.TLS != nil {
				negotiatedProtocol = r.TLS.NegotiatedProtocol
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
		}))
		server.EnableHTTP2 = true
		server.TLS = &tls.Config{
			MinVersion: tls.VersionTLS13,
			MaxVersion: tls.VersionTLS13,
			GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				mu.Lock()
				clientHelloProtos = append([]string(nil), hello.SupportedProtos...)
				mu.Unlock()
				return nil, nil
			},
		}
		server.StartTLS()
		defer server.Close()

		certPool := x509.NewCertPool()
		certPool.AddCert(server.Certificate())

		baseDefault, ok := origDefault.(*http.Transport)
		if !ok || baseDefault == nil {
			t.Fatal("expected http.DefaultTransport to be *http.Transport")
		}
		explicitProtocols := &http.Protocols{}
		explicitProtocols.SetHTTP1(true)
		explicitProtocols.SetHTTP2(true)

		baseTransport := baseDefault.Clone()
		baseTransport.Protocols = explicitProtocols
		baseTransport.TLSClientConfig = &tls.Config{
			RootCAs: certPool,
		}
		http.DefaultTransport = baseTransport

		hints, status := FetchAntigravityAvailableModels(context.Background(), AntigravityModelProbeRequest{
			BaseURLs:    []string{server.URL},
			AccessToken: "test-token",
			ProjectID:   "aicode-consumers",
		})
		if status != AntigravityProbeStatusSuccess {
			t.Fatalf("status = %v, want %v", status, AntigravityProbeStatusSuccess)
		}
		if _, exists := hints.ModelIDs["gemini-3-flash"]; !exists {
			t.Fatalf("missing gemini-3-flash in hints: %+v", hints)
		}

		mu.Lock()
		gotHelloProtos := slices.Clone(clientHelloProtos)
		gotProto := requestProto
		gotNegotiated := negotiatedProtocol
		mu.Unlock()

		if len(gotHelloProtos) != 0 {
			t.Fatalf("ClientHello ALPN = %v, want no ALPN extension", gotHelloProtos)
		}
		if gotProto != "HTTP/1.1" {
			t.Fatalf("request protocol = %q, want HTTP/1.1", gotProto)
		}
		if gotNegotiated != "" {
			t.Fatalf("negotiated ALPN = %q, want empty", gotNegotiated)
		}
		if baseTransport.Protocols == nil || !baseTransport.Protocols.HTTP2() {
			t.Fatalf("default transport Protocols mutated: %v", baseTransport.Protocols)
		}
	})
}
