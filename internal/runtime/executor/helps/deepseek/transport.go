package deepseek

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	utls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

const DefaultBaseURL = "https://chat.deepseek.com"

type Clients struct {
	Regular        *http.Client
	Stream         *http.Client
	Fallback       *http.Client
	FallbackStream *http.Client
}

func NewClients(cfg *config.Config, auth *cliproxyauth.Auth) Clients {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	return Clients{
		Regular:        newFingerprintClient(60*time.Second, proxyURL),
		Stream:         newFingerprintClient(0, proxyURL),
		Fallback:       newFallbackClient(60*time.Second, proxyURL),
		FallbackStream: newFallbackClient(0, proxyURL),
	}
}

func newFingerprintClient(timeout time.Duration, proxyURL string) *http.Client {
	dialContext := proxyDialContext(proxyURL)
	transport := &http.Transport{
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DialContext:         dialContext,
		DialTLSContext:      safariTLSDialer(dialContext),
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if strings.TrimSpace(proxyURL) == "" {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func newFallbackClient(timeout time.Duration, proxyURL string) *http.Client {
	client := &http.Client{Timeout: timeout}
	if proxyURL == "" {
		return client
	}
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("deepseek: failed to configure proxy transport: %v", errBuild)
		return client
	}
	if transport != nil {
		transport.ForceAttemptHTTP2 = false
		client.Transport = transport
	}
	return client
}

func proxyDialContext(proxyURL string) func(context.Context, string, string) (net.Conn, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		return dialer.DialContext
	}
	dialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
	if errBuild != nil {
		log.Errorf("deepseek: failed to configure proxy dialer for %s: %v", proxyutil.Redact(proxyURL), errBuild)
		d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext
	}
	if mode == proxyutil.ModeInherit || dialer == nil {
		d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, network, addr)
		}
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			conn, err := dialer.Dial(network, addr)
			ch <- result{conn: conn, err: err}
		}()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-ch:
			return res.conn, res.err
		}
	}
}

func safariTLSDialer(dialContext func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		dialContext = dialer.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		plainConn, err := dialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(addr)
		uConn := utls.UClient(plainConn, &utls.Config{ServerName: host}, utls.HelloSafari_Auto)
		if err := forceHTTP11ALPN(uConn); err != nil {
			if errClose := plainConn.Close(); errClose != nil {
				return nil, fmt.Errorf("deepseek: build TLS handshake failed: %w; close failed: %v", err, errClose)
			}
			return nil, err
		}
		if err := uConn.HandshakeContext(ctx); err != nil {
			if errClose := plainConn.Close(); errClose != nil {
				return nil, fmt.Errorf("deepseek: TLS handshake failed: %w; close failed: %v", err, errClose)
			}
			return nil, err
		}
		if negotiated := uConn.ConnectionState().NegotiatedProtocol; negotiated != "" && negotiated != "http/1.1" {
			if errClose := uConn.Close(); errClose != nil {
				return nil, fmt.Errorf("deepseek: unexpected ALPN protocol negotiated: %s; close failed: %v", negotiated, errClose)
			}
			return nil, fmt.Errorf("deepseek: unexpected ALPN protocol negotiated: %s", negotiated)
		}
		return uConn, nil
	}
}

func forceHTTP11ALPN(uConn *utls.UConn) error {
	if err := uConn.BuildHandshakeState(); err != nil {
		return err
	}
	for _, ext := range uConn.Extensions {
		alpnExt, ok := ext.(*utls.ALPNExtension)
		if !ok {
			continue
		}
		alpnExt.AlpnProtocols = []string{"http/1.1"}
		return nil
	}
	return nil
}

func ReadResponseBody(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var reader io.Reader = resp.Body
	switch encoding {
	case "gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer func() {
			if errClose := gz.Close(); errClose != nil {
				log.Errorf("deepseek: close gzip reader error: %v", errClose)
			}
		}()
		reader = gz
	case "br":
		reader = brotli.NewReader(resp.Body)
	}
	return io.ReadAll(reader)
}
