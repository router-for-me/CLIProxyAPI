package helps

import (
	"context"
	"fmt"
	"net"
	"net/http"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// newChromeHTTP1RoundTripper keeps the Chrome uTLS ClientHello while limiting
// ALPN to HTTP/1.1. It is used only when codex.force-http1 is enabled.
func newChromeHTTP1RoundTripper(proxyURL string) http.RoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls http1: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	return &http.Transport{
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialChromeHTTP1(ctx, dialer, network, addr)
		},
	}
}

func dialChromeHTTP1(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("utls http1: dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, network, addr)
	if errDial != nil {
		return nil, fmt.Errorf("utls http1: dial upstream: %w", errDial)
	}

	host, _, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls http1: split upstream address: %w; close connection: %v", errSplit, errClose)
		}
		return nil, fmt.Errorf("utls http1: split upstream address: %w", errSplit)
	}

	clientHello, errSpec := chromeHTTP1ClientHelloSpec()
	if errSpec != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls http1: build Chrome ClientHello: %w; close connection: %v", errSpec, errClose)
		}
		return nil, fmt.Errorf("utls http1: build Chrome ClientHello: %w", errSpec)
	}

	tlsConn := tls.UClient(conn, &tls.Config{ServerName: host}, tls.HelloCustom)
	if errPreset := tlsConn.ApplyPreset(clientHello); errPreset != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls http1: apply Chrome ClientHello: %w; close connection: %v", errPreset, errClose)
		}
		return nil, fmt.Errorf("utls http1: apply Chrome ClientHello: %w", errPreset)
	}
	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls http1: TLS handshake: %w; close connection: %v", errHandshake, errClose)
		}
		return nil, fmt.Errorf("utls http1: TLS handshake: %w", errHandshake)
	}
	if negotiated := tlsConn.ConnectionState().NegotiatedProtocol; negotiated != "http/1.1" {
		if errClose := tlsConn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls http1: negotiated protocol %q; close connection: %v", negotiated, errClose)
		}
		return nil, fmt.Errorf("utls http1: negotiated protocol %q", negotiated)
	}
	return tlsConn, nil
}

func chromeHTTP1ClientHelloSpec() (*tls.ClientHelloSpec, error) {
	spec, errSpec := tls.UTLSIdToSpec(tls.HelloChrome_Auto)
	if errSpec != nil {
		return nil, errSpec
	}

	foundALPN := false
	extensions := spec.Extensions[:0]
	for _, extension := range spec.Extensions {
		switch typed := extension.(type) {
		case *tls.ALPNExtension:
			typed.AlpnProtocols = []string{"http/1.1"}
			foundALPN = true
			extensions = append(extensions, extension)
		case *tls.ApplicationSettingsExtension, *tls.ApplicationSettingsExtensionNew:
			// Chrome advertises ALPS for h2. Drop it when h2 is not offered.
		default:
			extensions = append(extensions, extension)
		}
	}
	if !foundALPN {
		return nil, fmt.Errorf("Chrome ClientHello does not contain ALPN")
	}
	spec.Extensions = extensions
	return &spec, nil
}
