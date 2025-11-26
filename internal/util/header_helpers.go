package util

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ApplyCustomHeadersFromAttrs applies user-defined headers stored in the provided attributes map.
// Custom headers override built-in defaults when conflicts occur.
func ApplyCustomHeadersFromAttrs(r *http.Request, attrs map[string]string) {
	if r == nil {
		return
	}
	applyCustomHeaders(r, extractCustomHeaders(attrs))
}

func extractCustomHeaders(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	headers := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "header:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		headers[name] = val
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func applyCustomHeaders(r *http.Request, headers map[string]string) {
	if r == nil || len(headers) == 0 {
		return
	}
	for k, v := range headers {
		if k == "" || v == "" {
			continue
		}
		r.Header.Set(k, v)
	}
}

// ApplyCustomQueryParamsFromAttrs extracts query params from attributes and appends them to the URL.
// Returns the URL with query params appended.
func ApplyCustomQueryParamsFromAttrs(baseURL string, attrs map[string]string) string {
	if baseURL == "" {
		return baseURL
	}
	params := extractCustomQueryParams(attrs)
	if len(params) == 0 {
		return baseURL
	}
	return appendQueryParams(baseURL, params)
}

func extractCustomQueryParams(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	params := make(map[string]string)
	for k, v := range attrs {
		if !strings.HasPrefix(k, "query:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(k, "query:"))
		if name == "" {
			continue
		}
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		params[name] = val
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

func appendQueryParams(baseURL string, params map[string]string) string {
	if len(params) == 0 {
		return baseURL
	}

	// Check if URL already has query params
	separator := "?"
	if strings.Contains(baseURL, "?") {
		separator = "&"
	}

	// Build query string (sort keys for deterministic output)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(params[k]))
	}

	return baseURL + separator + strings.Join(parts, "&")
}
