package management

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

func guardedAPICallDialContext(ctx context.Context, network string, addr string) (net.Conn, error) {
	host, port, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		return nil, fmt.Errorf("invalid dial address: %w", errSplit)
	}
	resolved, errResolve := resolveAllowedAPICallHostIPs(host)
	if errResolve != nil {
		return nil, errResolve
	}
	if len(resolved) == 0 {
		return nil, fmt.Errorf("target host resolution failed")
	}
	dialer := &net.Dialer{}
	return dialer.DialContext(ctx, network, net.JoinHostPort(resolved[0].IP.String(), port))
}

type apiCallGuardedRoundTripper struct {
	base http.RoundTripper
}

func (t apiCallGuardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if errValidate := validateAPICallRequestURL(req.URL); errValidate != nil {
		return nil, errValidate
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func validateAPICallRequestURL(reqURL *url.URL) error {
	if errValidate := validateAPICallURL(reqURL); errValidate != nil {
		return errValidate
	}
	return validateResolvedHostIPs(reqURL.Hostname())
}
