package jsplugin

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateRequestURL prevents plugins from directing a channel credential to
// hosts other than the configured base URL or an administrator-approved host.
func ValidateRequestURL(requestURL, baseURL string, allowedHosts []string) error {
	request, err := url.Parse(requestURL)
	if err != nil || request.Scheme == "" || request.Host == "" {
		return fmt.Errorf("plugin request URL must be absolute")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		return fmt.Errorf("channel base URL is invalid")
	}
	requestHost := canonicalHost(request)
	if requestHost == canonicalHost(base) {
		return nil
	}
	for _, allowed := range allowedHosts {
		// Parse with the request scheme so "host:443" matches an https request
		// the same way an explicit default port on the request URL does.
		allowedURL, parseErr := url.Parse(request.Scheme + "://" + strings.TrimSpace(allowed))
		if parseErr == nil && requestHost == canonicalHost(allowedURL) {
			return nil
		}
	}
	return fmt.Errorf("plugin request host %q is not allowed", request.Host)
}

func canonicalHost(value *url.URL) string {
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if port == "" || port == "80" && value.Scheme == "http" || port == "443" && value.Scheme == "https" {
		return host
	}
	return net.JoinHostPort(host, port)
}
