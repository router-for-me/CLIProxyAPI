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

func TestExtractIDCIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		startURL string
		expected string
	}{
		{
			name:     "Empty URL",
			startURL: "",
			expected: "",
		},
		{
			name:     "Standard IDC URL with d- prefix",
			startURL: "https://d-1234567890.awsapps.com/start",
			expected: "d-1234567890",
		},
		{
			name:     "IDC URL with company name",
			startURL: "https://my-company.awsapps.com/start",
			expected: "my-company",
		},
		{
			name:     "IDC URL with simple name",
			startURL: "https://acme-corp.awsapps.com/start",
			expected: "acme-corp",
		},
		{
			name:     "IDC URL without https",
			startURL: "http://d-9876543210.awsapps.com/start",
			expected: "d-9876543210",
		},
		{
			name:     "IDC URL with subdomain only",
			startURL: "https://test.awsapps.com/start",
			expected: "test",
		},
		{
			name:     "Builder ID URL",
			startURL: "https://view.awsapps.com/start",
			expected: "view",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractIDCIdentifier(tt.startURL)
			if result != tt.expected {
				t.Errorf("ExtractIDCIdentifier() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGenerateTokenFileName(t *testing.T) {
	tests := []struct {
		name      string
		tokenData *KiroTokenData
		expected  string
	}{
		{
			name: "IDC with email",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "user@example.com",
				StartURL:   "https://d-1234567890.awsapps.com/start",
			},
			expected: "kiro-idc-user-example-com.json",
		},
		{
			name: "IDC without email but with startUrl",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "",
				StartURL:   "https://d-1234567890.awsapps.com/start",
			},
			expected: "kiro-idc-d-1234567890.json",
		},
		{
			name: "IDC with company name in startUrl",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "",
				StartURL:   "https://my-company.awsapps.com/start",
			},
			expected: "kiro-idc-my-company.json",
		},
		{
			name: "IDC without email and without startUrl",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "",
				StartURL:   "",
			},
			expected: "kiro-idc.json",
		},
		{
			name: "Builder ID with email",
			tokenData: &KiroTokenData{
				AuthMethod: "builder-id",
				Email:      "user@gmail.com",
				StartURL:   "https://view.awsapps.com/start",
			},
			expected: "kiro-builder-id-user-gmail-com.json",
		},
		{
			name: "Builder ID without email",
			tokenData: &KiroTokenData{
				AuthMethod: "builder-id",
				Email:      "",
				StartURL:   "https://view.awsapps.com/start",
			},
			expected: "kiro-builder-id.json",
		},
		{
			name: "Social auth with email",
			tokenData: &KiroTokenData{
				AuthMethod: "google",
				Email:      "user@gmail.com",
			},
			expected: "kiro-google-user-gmail-com.json",
		},
		{
			name: "Empty auth method",
			tokenData: &KiroTokenData{
				AuthMethod: "",
				Email:      "",
			},
			expected: "kiro-unknown.json",
		},
		{
			name: "Email with special characters",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "user.name+tag@sub.example.com",
				StartURL:   "https://d-1234567890.awsapps.com/start",
			},
			expected: "kiro-idc-user-name+tag-sub-example-com.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateTokenFileName(tt.tokenData)
			if result != tt.expected {
				t.Errorf("GenerateTokenFileName() = %q, want %q", result, tt.expected)
			}
		})
	}
}
