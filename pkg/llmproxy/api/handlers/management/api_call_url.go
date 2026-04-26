package management

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

func validateAPICallURL(parsedURL *url.URL) error {
	if parsedURL == nil {
		return fmt.Errorf("invalid url")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsedURL.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported url scheme")
	}
	if parsedURL.User != nil {
		return fmt.Errorf("target host is not allowed")
	}
	host := strings.TrimSpace(parsedURL.Hostname())
	if host == "" {
		return fmt.Errorf("invalid url host")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("target host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("target host is not allowed")
		}
	}
	return nil
}

func sanitizeAPICallURL(raw string) (string, *url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, fmt.Errorf("missing url")
	}
	parsedURL, errParseURL := url.Parse(trimmed)
	if errParseURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", nil, fmt.Errorf("invalid url")
	}
	if errValidateURL := validateAPICallURL(parsedURL); errValidateURL != nil {
		return "", nil, errValidateURL
	}
	// Reconstruct a clean URL from validated components to break taint propagation.
	// The scheme is validated to be http/https, host is validated against SSRF,
	// and path/query are preserved from the parsed (not raw) URL.
	reconstructed := &url.URL{
		Scheme:   parsedURL.Scheme,
		Host:     parsedURL.Host,
		Path:     parsedURL.Path,
		RawPath:  parsedURL.RawPath,
		RawQuery: parsedURL.RawQuery,
	}
	return reconstructed.String(), reconstructed, nil
}

func validateResolvedHostIPs(host string) error {
	_, err := resolveAllowedAPICallHostIPs(host)
	return err
}

func resolveAllowedAPICallHostIPs(host string) ([]net.IPAddr, error) {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return nil, fmt.Errorf("invalid url host")
	}
	resolved, errLookup := net.DefaultResolver.LookupIPAddr(context.Background(), trimmed)
	if errLookup != nil {
		return nil, fmt.Errorf("target host resolution failed")
	}
	allowed := make([]net.IPAddr, 0, len(resolved))
	for _, ip := range resolved {
		if ip.IP == nil {
			continue
		}
		if ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
			return nil, fmt.Errorf("target host is not allowed")
		}
		allowed = append(allowed, ip)
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("target host resolution failed")
	}
	return allowed, nil
}

func isAllowedHostOverride(parsedURL *url.URL, override string) bool {
	if parsedURL == nil {
		return false
	}
	trimmed := strings.TrimSpace(override)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, " \r\n\t") {
		return false
	}

	requestHost := strings.TrimSpace(parsedURL.Host)
	requestHostname := strings.TrimSpace(parsedURL.Hostname())
	if requestHost == "" {
		return false
	}
	if strings.EqualFold(trimmed, requestHost) {
		return true
	}
	if strings.EqualFold(trimmed, requestHostname) {
		return true
	}
	if len(trimmed) > 2 && trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']' {
		return false
	}
	return false
}

func copilotQuotaURLFromTokenURL(originalURL string) (string, error) {
	parsedURL, errParse := url.Parse(strings.TrimSpace(originalURL))
	if errParse != nil {
		return "", errParse
	}
	if parsedURL.User != nil {
		return "", fmt.Errorf("unsupported host %q", parsedURL.Hostname())
	}
	host := strings.ToLower(parsedURL.Hostname())
	if parsedURL.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", parsedURL.Scheme)
	}
	switch host {
	case "api.github.com":
		return "https://api.github.com/copilot_pkg/llmproxy/user", nil
	case "api.githubcopilot.com":
		return "https://api.githubcopilot.com/copilot_pkg/llmproxy/user", nil
	default:
		return "", fmt.Errorf("unsupported host %q", parsedURL.Hostname())
	}
}
