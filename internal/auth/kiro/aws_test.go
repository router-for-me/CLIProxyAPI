package kiro

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestExtractEmailFromJWT(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected string
	}{
		{
			name:     "Empty token",
			token:    "",
			expected: "",
		},
		{
			name:     "Invalid token format",
			token:    "not.a.valid.jwt",
			expected: "",
		},
		{
			name:     "Invalid token - not base64",
			token:    "xxx.yyy.zzz",
			expected: "",
		},
		{
			name:     "Valid JWT with email",
			token:    createTestJWT(map[string]any{"email": "test@example.com", "sub": "user123"}),
			expected: "test@example.com",
		},
		{
			name:     "JWT without email but with preferred_username",
			token:    createTestJWT(map[string]any{"preferred_username": "user@domain.com", "sub": "user123"}),
			expected: "user@domain.com",
		},
		{
			name:     "JWT with email-like sub",
			token:    createTestJWT(map[string]any{"sub": "another@test.com"}),
			expected: "another@test.com",
		},
		{
			name:     "JWT without any email fields",
			token:    createTestJWT(map[string]any{"sub": "user123", "name": "Test User"}),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractEmailFromJWT(tt.token)
			if result != tt.expected {
				t.Errorf("ExtractEmailFromJWT() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSanitizeEmailForFilename(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		expected string
	}{
		{
			name:     "Empty email",
			email:    "",
			expected: "",
		},
		{
			name:     "Simple email",
			email:    "user@example.com",
			expected: "user@example.com",
		},
		{
			name:     "Email with space",
			email:    "user name@example.com",
			expected: "user_name@example.com",
		},
		{
			name:     "Email with special chars",
			email:    "user:name@example.com",
			expected: "user_name@example.com",
		},
		{
			name:     "Email with multiple special chars",
			email:    "user/name:test@example.com",
			expected: "user_name_test@example.com",
		},
		{
			name:     "Path traversal attempt",
			email:    "../../../etc/passwd",
			expected: "_.__.__._etc_passwd",
		},
		{
			name:     "Path traversal with backslash",
			email:    `..\..\..\..\windows\system32`,
			expected: "_.__.__.__._windows_system32",
		},
		{
			name:     "Null byte injection attempt",
			email:    "user\x00@evil.com",
			expected: "user_@evil.com",
		},
		// URL-encoded path traversal tests
		{
			name:     "URL-encoded slash",
			email:    "user%2Fpath@example.com",
			expected: "user_path@example.com",
		},
		{
			name:     "URL-encoded backslash",
			email:    "user%5Cpath@example.com",
			expected: "user_path@example.com",
		},
		{
			name:     "URL-encoded dot",
			email:    "%2E%2E%2Fetc%2Fpasswd",
			expected: "___etc_passwd",
		},
		{
			name:     "URL-encoded null",
			email:    "user%00@evil.com",
			expected: "user_@evil.com",
		},
		{
			name:     "Double URL-encoding attack",
			email:    "%252F%252E%252E",
			expected: "_252F_252E_252E", // % replaced with _, remaining chars preserved (safe)
		},
		{
			name:     "Mixed case URL-encoding",
			email:    "%2f%2F%5c%5C",
			expected: "____",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeEmailForFilename(tt.email)
			if result != tt.expected {
				t.Errorf("SanitizeEmailForFilename() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// createTestJWT creates a test JWT token with the given claims
func createTestJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	
	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))
	
	return header + "." + payload + "." + signature
}
