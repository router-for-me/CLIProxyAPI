package notifications

import (
	"net/url"
	"strings"
	"sync"
)

type serviceURLState struct {
	mu    sync.RWMutex
	value string
}

var globalServiceURL serviceURLState

// ConfigureServiceURL sets the externally reachable service URL used by notification actions.
func ConfigureServiceURL(rawURL string) {
	serviceURL := normalizeServiceURL(rawURL)
	globalServiceURL.mu.Lock()
	globalServiceURL.value = serviceURL
	globalServiceURL.mu.Unlock()
}

// CurrentServiceURL returns the configured externally reachable service URL.
func CurrentServiceURL() string {
	globalServiceURL.mu.RLock()
	defer globalServiceURL.mu.RUnlock()
	return globalServiceURL.value
}

func normalizeServiceURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func resetServiceURLForTest() {
	ConfigureServiceURL("")
}
