package helps

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

type codexTransportError struct{ err error }

func (t codexTransportError) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, t.err
}

// newCodexHTTP1RoundTripper avoids the custom HTTP/2 connection entirely.
// No automatic fallback or connection reuse: an inference POST is sent once.
// Empty proxy settings remain direct, matching the existing ChatGPT transport.
func newCodexHTTP1RoundTripper(proxyURL string) http.RoundTripper {
	dialer, _, err := proxyutil.BuildDialer(proxyURL)
	if err != nil {
		return codexTransportError{errors.New("codex HTTP/1.1: invalid proxy setting")}
	}
	if dialer == nil {
		dialer = proxy.Direct
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return codexTransportError{errors.New("codex HTTP/1.1: proxy must support context cancellation")}
	}
	return &http.Transport{
		Proxy:               nil,
		DialContext:         contextDialer.DialContext,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        make(map[string]func(string, *tls.Conn) http.RoundTripper),
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
}
