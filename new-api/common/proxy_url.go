package common

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseProxyURLStrict validates and normalizes a proxy URL for persistence.
func ParseProxyURLStrict(rawProxyURL string) (*url.URL, error) {
	parsedURL, _, err := parseProxyURL(rawProxyURL, false)
	return parsedURL, err
}

// ParseProxyURLRuntime validates and normalizes a proxy URL for runtime use.
// The boolean result reports whether a legacy path, query, or fragment was removed.
func ParseProxyURLRuntime(rawProxyURL string) (*url.URL, bool, error) {
	return parseProxyURL(rawProxyURL, true)
}

func parseProxyURL(rawProxyURL string, allowLegacySuffix bool) (*url.URL, bool, error) {
	trimmedProxyURL := strings.TrimSpace(rawProxyURL)
	if trimmedProxyURL == "" {
		return nil, false, nil
	}

	parsedURL, err := url.Parse(trimmedProxyURL)
	if err != nil {
		return nil, false, fmt.Errorf("invalid proxy URL")
	}
	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	switch parsedURL.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, false, fmt.Errorf("proxy URL must use http, https, socks5, or socks5h")
	}
	if parsedURL.Hostname() == "" {
		return nil, false, fmt.Errorf("proxy URL must include a host")
	}
	if portText := parsedURL.Port(); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, false, fmt.Errorf("proxy URL must include a valid port")
		}
	}

	hasQuery := parsedURL.RawQuery != "" || parsedURL.ForceQuery
	hasFragment := strings.Contains(trimmedProxyURL, "#")
	escapedPath := parsedURL.EscapedPath()
	hasNonRootPath := escapedPath != "" && escapedPath != "/"
	legacySuffixStripped := hasQuery || hasFragment || hasNonRootPath
	if !allowLegacySuffix {
		switch {
		case hasQuery:
			return nil, false, fmt.Errorf("proxy URL must not include a query")
		case hasFragment:
			return nil, false, fmt.Errorf("proxy URL must not include a fragment")
		case hasNonRootPath:
			return nil, false, fmt.Errorf("proxy URL must not include a path")
		}
	}

	parsedURL.Path = ""
	parsedURL.RawPath = ""
	parsedURL.RawQuery = ""
	parsedURL.ForceQuery = false
	parsedURL.Fragment = ""
	parsedURL.RawFragment = ""

	if (parsedURL.Scheme == "socks5" || parsedURL.Scheme == "socks5h") && parsedURL.Port() == "" {
		parsedURL.Host = net.JoinHostPort(parsedURL.Hostname(), "1080")
	}

	return parsedURL, legacySuffixStripped, nil
}
