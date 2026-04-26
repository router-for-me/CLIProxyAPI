package management

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"

	coreauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) apiCallTransport(auth *coreauth.Auth) http.RoundTripper {
	hasAuthProxy := false
	var proxyCandidates []string
	if auth != nil {
		if proxyStr := strings.TrimSpace(auth.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
			hasAuthProxy = true
		}
	}
	if h != nil && h.cfg != nil {
		if proxyStr := strings.TrimSpace(h.cfg.ProxyURL); proxyStr != "" {
			proxyCandidates = append(proxyCandidates, proxyStr)
		}
	}

	for _, proxyStr := range proxyCandidates {
		transport, errBuild := buildProxyTransportWithError(proxyStr)
		if transport != nil {
			return transport
		}
		if hasAuthProxy {
			return &transportFailureRoundTripper{err: fmt.Errorf("authentication proxy misconfigured: %v", errBuild)}
		}
		log.Debugf("failed to setup API call proxy from URL: %s, trying next candidate", proxyStr)
	}

	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || transport == nil {
		return &http.Transport{Proxy: nil}
	}
	clone := transport.Clone()
	clone.Proxy = nil
	clone.DialContext = guardedAPICallDialContext
	return clone
}

func (h *Handler) apiCallHTTPClient(auth *coreauth.Auth) *http.Client {
	return &http.Client{
		Timeout:   defaultAPICallTimeout,
		Transport: apiCallGuardedRoundTripper{base: h.apiCallTransport(auth)},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return validateAPICallRequestURL(req.URL)
		},
	}
}

func buildProxyTransportWithError(proxyStr string) (*http.Transport, error) {
	proxyStr = strings.TrimSpace(proxyStr)
	if proxyStr == "" {
		return nil, fmt.Errorf("proxy URL is empty")
	}

	proxyURL, errParse := url.Parse(proxyStr)
	if errParse != nil {
		log.WithError(errParse).Debug("parse proxy URL failed")
		return nil, fmt.Errorf("parse proxy URL failed: %w", errParse)
	}
	if proxyURL.Scheme == "" || proxyURL.Host == "" {
		log.Debug("proxy URL missing scheme/host")
		return nil, fmt.Errorf("missing proxy scheme or host: %s", proxyStr)
	}

	if proxyURL.Scheme == "socks5" {
		var proxyAuth *proxy.Auth
		if proxyURL.User != nil {
			username := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.WithError(errSOCKS5).Debug("create SOCKS5 dialer failed")
			return nil, fmt.Errorf("create SOCKS5 dialer failed: %w", errSOCKS5)
		}
		return &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}, nil
	}

	if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
		return &http.Transport{Proxy: http.ProxyURL(proxyURL)}, nil
	}

	log.Debugf("unsupported proxy scheme: %s", proxyURL.Scheme)
	return nil, fmt.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
}

type transportFailureRoundTripper struct {
	err error
}

func (t *transportFailureRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
