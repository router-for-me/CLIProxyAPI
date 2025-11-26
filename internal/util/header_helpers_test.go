package util

import (
	"testing"
)

func TestExtractCustomQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		attrs    map[string]string
		expected map[string]string
	}{
		{
			name:     "nil attrs",
			attrs:    nil,
			expected: nil,
		},
		{
			name:     "empty attrs",
			attrs:    map[string]string{},
			expected: nil,
		},
		{
			name: "no query params",
			attrs: map[string]string{
				"header:X-Custom": "value",
				"base_url":        "https://example.com",
			},
			expected: nil,
		},
		{
			name: "single query param",
			attrs: map[string]string{
				"query:api-version": "2024-02-01",
			},
			expected: map[string]string{
				"api-version": "2024-02-01",
			},
		},
		{
			name: "multiple query params",
			attrs: map[string]string{
				"query:api-version": "2024-02-01",
				"query:deployment":  "gpt-4",
				"header:X-Custom":   "ignored",
			},
			expected: map[string]string{
				"api-version": "2024-02-01",
				"deployment":  "gpt-4",
			},
		},
		{
			name: "empty value ignored",
			attrs: map[string]string{
				"query:api-version": "2024-02-01",
				"query:empty":       "",
			},
			expected: map[string]string{
				"api-version": "2024-02-01",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractCustomQueryParams(tt.attrs)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d params, got %d", len(tt.expected), len(result))
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("expected %s=%s, got %s=%s", k, v, k, result[k])
				}
			}
		})
	}
}

func TestAppendQueryParams(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		params   map[string]string
		expected string
	}{
		{
			name:     "no params",
			baseURL:  "https://example.com/api",
			params:   nil,
			expected: "https://example.com/api",
		},
		{
			name:    "single param",
			baseURL: "https://example.com/api",
			params: map[string]string{
				"api-version": "2024-02-01",
			},
			expected: "https://example.com/api?api-version=2024-02-01",
		},
		{
			name:    "multiple params sorted",
			baseURL: "https://example.com/api",
			params: map[string]string{
				"z-param": "last",
				"a-param": "first",
			},
			expected: "https://example.com/api?a-param=first&z-param=last",
		},
		{
			name:    "existing query params",
			baseURL: "https://example.com/api?existing=true",
			params: map[string]string{
				"new": "param",
			},
			expected: "https://example.com/api?existing=true&new=param",
		},
		{
			name:    "url encoding",
			baseURL: "https://example.com/api",
			params: map[string]string{
				"key": "value with spaces",
			},
			expected: "https://example.com/api?key=value+with+spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendQueryParams(tt.baseURL, tt.params)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestApplyCustomQueryParamsFromAttrs(t *testing.T) {
	attrs := map[string]string{
		"query:api-version": "2024-02-01",
		"header:X-Custom":   "ignored",
	}

	result := ApplyCustomQueryParamsFromAttrs("https://example.com/api", attrs)
	expected := "https://example.com/api?api-version=2024-02-01"

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}

	// Test empty URL
	result = ApplyCustomQueryParamsFromAttrs("", attrs)
	if result != "" {
		t.Errorf("expected empty string, got %s", result)
	}
}
