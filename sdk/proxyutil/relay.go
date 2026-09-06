package proxyutil

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// relayTransport forwards each request to a relay base URL using the
// x-relay-target / x-relay-path header contract (the wire protocol of the
// relay workers deployed to Cloudflare/Vercel/Deno). The target's
// scheme://host moves into x-relay-target, the path+query into x-relay-path,
// and the request is sent to the relay with all other headers preserved.
type relayTransport struct {
	base  *url.URL
	inner http.RoundTripper
}

// NewRelayTransport builds a relay RoundTripper. The base URL must be https —
// relay workers are publicly deployed endpoints and relayed requests carry
// upstream credentials.
func NewRelayTransport(base string) (http.RoundTripper, error) {
	return newRelayTransport(base, http.DefaultTransport, true)
}

// NewRelayTransportRelaxed skips the https-base enforcement for tests, which
// run against local httptest servers.
func NewRelayTransportRelaxed(base string, inner http.RoundTripper) (http.RoundTripper, error) {
	return newRelayTransport(base, inner, false)
}

func newRelayTransport(base string, inner http.RoundTripper, enforceHTTPS bool) (http.RoundTripper, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(base))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid relay base URL")
	}
	if enforceHTTPS && parsed.Scheme != "https" {
		return nil, fmt.Errorf("relay base URL must be https, got %s", parsed.Scheme)
	}
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &relayTransport{base: parsed, inner: inner}, nil
}

func (t *relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || req.URL.Host == "" {
		return nil, errors.New("relay transport: nil or hostless request")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("relay transport: target must be http(s), got %s", req.URL.Scheme)
	}
	clone := req.Clone(req.Context())
	target := fmt.Sprintf("%s://%s", req.URL.Scheme, req.URL.Host)
	relayPath := req.URL.Path
	if relayPath == "" {
		relayPath = "/"
	}
	if req.URL.RawQuery != "" {
		relayPath += "?" + req.URL.RawQuery
	}
	// The relay contract headers replace any caller-supplied values so a
	// spoofed target cannot survive the rewrite.
	clone.Header.Set("x-relay-target", target)
	clone.Header.Set("x-relay-path", relayPath)
	clone.Host = t.base.Host
	clone.URL = &url.URL{Scheme: t.base.Scheme, Host: t.base.Host, Path: "/"}
	return t.inner.RoundTrip(clone)
}
