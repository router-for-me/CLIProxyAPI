package codex

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func closeTestConn(t *testing.T, c io.Closer) {
	t.Helper()
	if errClose := c.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
		t.Logf("close test connection: %v", errClose)
	}
}

func newRedirectedTransport(t *testing.T, server *httptest.Server, dialOverride func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	t.Helper()
	parsed, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	clientTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok || clientTransport == nil {
		t.Fatal("expected *http.Transport from TLS test server")
	}
	cloned := clientTransport.Clone()
	if cloned.TLSClientConfig == nil {
		cloned.TLSClientConfig = &tls.Config{}
	} else {
		cloned.TLSClientConfig = cloned.TLSClientConfig.Clone()
	}
	cloned.TLSClientConfig.ServerName = "127.0.0.1"
	cloned.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if dialOverride != nil {
			return dialOverride(ctx, network, parsed.Host)
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, parsed.Host)
	}
	t.Cleanup(cloned.CloseIdleConnections)
	return cloned
}

func TestRefreshTokensReleasesClaimAfterPreSendFailures(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	newTokenServer := func(t *testing.T, calls *atomic.Int32) *httptest.Server {
		t.Helper()
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			calls.Add(1)
			if errParse := req.ParseForm(); errParse != nil {
				t.Errorf("parse form: %v", errParse)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"recovered-access","refresh_token":"recovered-refresh","expires_in":3600}`)
		}))
		t.Cleanup(server.Close)
		return server
	}

	t.Run("dns_failure", func(t *testing.T) {
		resetCodexRefreshGroupForTest()
		var serverCalls atomic.Int32
		server := newTokenServer(t, &serverCalls)
		cfg := &config.Config{AuthDir: t.TempDir()}

		var failDNS atomic.Bool
		failDNS.Store(true)
		transport := newRedirectedTransport(t, server, func(ctx context.Context, network, targetAddr string) (net.Conn, error) {
			if failDNS.Load() {
				return nil, &net.DNSError{Err: "no such host", Name: "auth.openai.com", IsNotFound: true}
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, targetAddr)
		})

		firstAuth := NewCodexAuth(cfg)
		firstAuth.httpClient.Transport = transport
		_, errFirst := firstAuth.RefreshTokens(context.Background(), "wake-dns-grant")
		if errFirst == nil {
			t.Fatal("expected pre-send DNS failure")
		}
		if IsRefreshOutcomeUnknown(errFirst) {
			t.Fatalf("pre-send DNS failure was classified as unknown outcome: %v", errFirst)
		}
		if serverCalls.Load() != 0 {
			t.Fatalf("server calls during DNS failure = %d, want 0", serverCalls.Load())
		}

		failDNS.Store(false)
		secondAuth := NewCodexAuth(cfg)
		secondAuth.httpClient.Transport = transport
		tokens, errSecond := secondAuth.RefreshTokens(context.Background(), "wake-dns-grant")
		if errSecond != nil || tokens == nil || tokens.AccessToken != "recovered-access" || tokens.RefreshToken != "recovered-refresh" {
			t.Fatalf("retry after pre-send DNS failure failed: tokens=%+v, err=%v", tokens, errSecond)
		}
		if serverCalls.Load() != 1 {
			t.Fatalf("server calls after recovery = %d, want 1", serverCalls.Load())
		}
	})

	t.Run("dial_connection_refused", func(t *testing.T) {
		resetCodexRefreshGroupForTest()
		var serverCalls atomic.Int32
		server := newTokenServer(t, &serverCalls)
		cfg := &config.Config{AuthDir: t.TempDir()}

		closedListener, errListen := net.Listen("tcp", "127.0.0.1:0")
		if errListen != nil {
			t.Fatal(errListen)
		}
		closedAddr := closedListener.Addr().String()
		if errClose := closedListener.Close(); errClose != nil {
			t.Fatal(errClose)
		}

		var useClosedPort atomic.Bool
		useClosedPort.Store(true)
		transport := newRedirectedTransport(t, server, func(ctx context.Context, network, targetAddr string) (net.Conn, error) {
			var dialer net.Dialer
			if useClosedPort.Load() {
				return dialer.DialContext(ctx, network, closedAddr)
			}
			return dialer.DialContext(ctx, network, targetAddr)
		})

		firstAuth := NewCodexAuth(cfg)
		firstAuth.httpClient.Transport = transport
		_, errFirst := firstAuth.RefreshTokens(context.Background(), "wake-dial-grant")
		if errFirst == nil {
			t.Fatal("expected connection refused error")
		}
		if IsRefreshOutcomeUnknown(errFirst) {
			t.Fatalf("pre-send dial failure was classified as unknown outcome: %v", errFirst)
		}

		useClosedPort.Store(false)
		secondAuth := NewCodexAuth(cfg)
		secondAuth.httpClient.Transport = transport
		tokens, errSecond := secondAuth.RefreshTokens(context.Background(), "wake-dial-grant")
		if errSecond != nil || tokens == nil || tokens.AccessToken != "recovered-access" {
			t.Fatalf("retry after dial failure failed: tokens=%+v, err=%v", tokens, errSecond)
		}
		if serverCalls.Load() != 1 {
			t.Fatalf("server calls = %d, want 1", serverCalls.Load())
		}
	})

	t.Run("tls_handshake_failure", func(t *testing.T) {
		resetCodexRefreshGroupForTest()
		var serverCalls atomic.Int32
		server := newTokenServer(t, &serverCalls)
		cfg := &config.Config{AuthDir: t.TempDir()}

		dropTLSListener, errListen := net.Listen("tcp", "127.0.0.1:0")
		if errListen != nil {
			t.Fatal(errListen)
		}
		t.Cleanup(func() { closeTestConn(t, dropTLSListener) })
		go func() {
			for {
				conn, errAccept := dropTLSListener.Accept()
				if errAccept != nil {
					return
				}
				closeTestConn(t, conn)
			}
		}()

		var dropTLS atomic.Bool
		dropTLS.Store(true)
		transport := newRedirectedTransport(t, server, func(ctx context.Context, network, targetAddr string) (net.Conn, error) {
			var dialer net.Dialer
			if dropTLS.Load() {
				return dialer.DialContext(ctx, network, dropTLSListener.Addr().String())
			}
			return dialer.DialContext(ctx, network, targetAddr)
		})

		firstAuth := NewCodexAuth(cfg)
		firstAuth.httpClient.Transport = transport
		_, errFirst := firstAuth.RefreshTokens(context.Background(), "wake-tls-grant")
		if errFirst == nil {
			t.Fatal("expected TLS handshake error")
		}
		if IsRefreshOutcomeUnknown(errFirst) {
			t.Fatalf("pre-send TLS handshake failure was classified as unknown outcome: %v", errFirst)
		}

		dropTLS.Store(false)
		secondAuth := NewCodexAuth(cfg)
		secondAuth.httpClient.Transport = transport
		tokens, errSecond := secondAuth.RefreshTokens(context.Background(), "wake-tls-grant")
		if errSecond != nil || tokens == nil || tokens.AccessToken != "recovered-access" {
			t.Fatalf("retry after TLS handshake failure failed: tokens=%+v, err=%v", tokens, errSecond)
		}
		if serverCalls.Load() != 1 {
			t.Fatalf("server calls = %d, want 1", serverCalls.Load())
		}
	})

	t.Run("proxy_connect_401_is_retryable_and_releases_claim", func(t *testing.T) {
		resetCodexRefreshGroupForTest()
		var serverCalls atomic.Int32
		server := newTokenServer(t, &serverCalls)
		cfg := &config.Config{AuthDir: t.TempDir()}

		proxyListener, errListen := net.Listen("tcp", "127.0.0.1:0")
		if errListen != nil {
			t.Fatal(errListen)
		}
		t.Cleanup(func() { closeTestConn(t, proxyListener) })
		var proxyAttempts atomic.Int32
		go func() {
			for {
				conn, errAccept := proxyListener.Accept()
				if errAccept != nil {
					return
				}
				go func(c net.Conn) {
					defer func() { closeTestConn(t, c) }()
					reader := bufio.NewReader(c)
					if _, errReadReq := http.ReadRequest(reader); errReadReq != nil {
						return
					}
					attempt := proxyAttempts.Add(1)
					if attempt == 1 {
						_, _ = io.WriteString(c, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n")
						return
					}
					parsedTarget, errParse := url.Parse(server.URL)
					if errParse != nil {
						return
					}
					upstream, errDial := net.Dial("tcp", parsedTarget.Host)
					if errDial != nil {
						return
					}
					defer func() { closeTestConn(t, upstream) }()
					_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
					done := make(chan struct{}, 2)
					go func() { _, _ = io.Copy(upstream, reader); done <- struct{}{} }()
					go func() { _, _ = io.Copy(c, upstream); done <- struct{}{} }()
					<-done
				}(conn)
			}
		}()

		proxyURL, errProxyURL := url.Parse("http://" + proxyListener.Addr().String())
		if errProxyURL != nil {
			t.Fatal(errProxyURL)
		}
		clientTransport := server.Client().Transport.(*http.Transport).Clone()
		clientTransport.TLSClientConfig = clientTransport.TLSClientConfig.Clone()
		clientTransport.TLSClientConfig.ServerName = "127.0.0.1"
		clientTransport.Proxy = http.ProxyURL(proxyURL)
		t.Cleanup(clientTransport.CloseIdleConnections)

		auth := NewCodexAuth(cfg)
		auth.httpClient.Transport = clientTransport
		tokens, errRefresh := auth.RefreshTokensWithRetry(context.Background(), "wake-proxy-grant", 2)
		if errRefresh != nil || tokens == nil || tokens.AccessToken != "recovered-access" {
			t.Fatalf("proxy CONNECT 401 aborted refresh or locked grant: tokens=%+v, err=%v", tokens, errRefresh)
		}
		if proxyAttempts.Load() != 2 || serverCalls.Load() != 1 {
			t.Fatalf("proxyAttempts=%d, serverCalls=%d, want 2 and 1", proxyAttempts.Load(), serverCalls.Load())
		}
	})

	t.Run("post_gotconn_disconnect_stays_unknown_outcome", func(t *testing.T) {
		resetCodexRefreshGroupForTest()
		var serverCalls atomic.Int32
		server := newTokenServer(t, &serverCalls)
		cfg := &config.Config{AuthDir: t.TempDir()}

		tlsCert := server.TLS.Certificates[0]
		postHandshakeListener, errListen := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{tlsCert}})
		if errListen != nil {
			t.Fatal(errListen)
		}
		t.Cleanup(func() { closeTestConn(t, postHandshakeListener) })
		var accepted atomic.Int32
		go func() {
			for {
				conn, errAccept := postHandshakeListener.Accept()
				if errAccept != nil {
					return
				}
				go func(c net.Conn) {
					defer func() { closeTestConn(t, c) }()
					tlsConn := c.(*tls.Conn)
					if errHS := tlsConn.Handshake(); errHS != nil {
						return
					}
					accepted.Add(1)
					var buf [64]byte
					_, _ = tlsConn.Read(buf[:])
				}(conn)
			}
		}()

		transport := newRedirectedTransport(t, server, func(ctx context.Context, network, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, postHandshakeListener.Addr().String())
		})
		auth := NewCodexAuth(cfg)
		auth.httpClient.Transport = transport
		_, errFirst := auth.RefreshTokensWithRetry(context.Background(), "post-send-grant", 3)
		if !IsRefreshOutcomeUnknown(errFirst) {
			t.Fatalf("post-handshake disconnect must remain unknown outcome, got: %v", errFirst)
		}
		if accepted.Load() != 1 {
			t.Fatalf("post-handshake transport calls = %d, want 1", accepted.Load())
		}
	})
}

func TestSIWCRefreshReleasesClaimAfterPreSendFailure(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)

	var serverCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		serverCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"siwc-recovered","refresh_token":"siwc-next","expires_in":3600,"scope":"openid chatgpt.tokens.use.direct"}`)
	}))
	t.Cleanup(server.Close)

	previousEndpoint := siwcTokenEndpointURL
	siwcTokenEndpointURL = strings.TrimRight(server.URL, "/") + "/api/accounts/oauth/token"
	t.Cleanup(func() { siwcTokenEndpointURL = previousEndpoint })

	cfg := &config.Config{AuthDir: t.TempDir()}
	var failDNS atomic.Bool
	failDNS.Store(true)
	transport := newRedirectedTransport(t, server, func(ctx context.Context, network, targetAddr string) (net.Conn, error) {
		if failDNS.Load() {
			return nil, &net.DNSError{Err: "temporary failure in name resolution", Name: "auth.openai.com", IsTemporary: true}
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, targetAddr)
	})

	auth := NewCodexAuth(cfg)
	auth.httpClient.Transport = transport
	_, errFirst := auth.RefreshSIWCTokens(context.Background(), "siwc-wake-grant", "oaiapp_wake", "550e8400-e29b-41d4-a716-446655440000")
	if errFirst == nil || IsRefreshOutcomeUnknown(errFirst) {
		t.Fatalf("expected retryable pre-send SIWC error, got: %v", errFirst)
	}

	failDNS.Store(false)
	tokens, errSecond := auth.RefreshSIWCTokens(context.Background(), "siwc-wake-grant", "oaiapp_wake", "550e8400-e29b-41d4-a716-446655440000")
	if errSecond != nil || tokens == nil || tokens.AccessToken != "siwc-recovered" {
		t.Fatalf("SIWC retry after pre-send DNS failure failed: tokens=%+v, err=%v", tokens, errSecond)
	}
	if serverCalls.Load() != 1 {
		t.Fatalf("SIWC server calls = %d, want 1", serverCalls.Load())
	}
}

type peerReleasedClaimJournal struct {
	memoryRefreshJournal
	releasedBeforeRead bool
}

func (j *peerReleasedClaimJournal) RefreshClaim(ctx context.Context, key string) (bool, error) {
	if !j.releasedBeforeRead {
		j.releasedBeforeRead = true
		return false, nil
	}
	return j.memoryRefreshJournal.RefreshClaim(ctx, key)
}

func TestJournalRefreshTreatsPeerReleasedClaimAsPreSendRetryable(t *testing.T) {
	journal := &peerReleasedClaimJournal{}
	_, errFirst := journalRefresh(context.Background(), journal, ClientID, "peer-released-grant", func() (string, error) {
		t.Fatal("run must not execute when RefreshClaim returned false")
		return "", nil
	}, func(v ojson.Result) (string, error) { return v.String(), nil })
	if !IsRefreshPreSendError(errFirst) || IsRefreshOutcomeUnknown(errFirst) {
		t.Fatalf("peer-released claim returned %v, want retryable RefreshPreSendError", errFirst)
	}
	got, errSecond := journalRefresh(context.Background(), journal, ClientID, "peer-released-grant", func() (string, error) {
		return "rotated", nil
	}, func(v ojson.Result) (string, error) { return v.String(), nil })
	if errSecond != nil || got != "rotated" {
		t.Fatalf("second attempt after peer release: got=%q, err=%v", got, errSecond)
	}
}
