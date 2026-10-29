package proxyutil

import (
	"os"
	"strings"
)

// EnvironmentProxyURL returns the first non-empty HTTPS_PROXY, https_proxy,
// HTTP_PROXY, or http_proxy value from the process environment.
func EnvironmentProxyURL() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
