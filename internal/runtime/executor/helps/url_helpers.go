package helps

import "strings"

// NormalizeBaseURL trims whitespace and trailing slashes from an upstream base URL.
func NormalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// JoinBaseURL joins an upstream base URL and endpoint with exactly one slash.
func JoinBaseURL(baseURL, endpoint string) string {
	baseURL = NormalizeBaseURL(baseURL)
	endpoint = strings.TrimLeft(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return baseURL
	}
	if baseURL == "" {
		return "/" + endpoint
	}
	return baseURL + "/" + endpoint
}
