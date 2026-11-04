package kiro

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func createTestJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := base64.RawURLEncoding.EncodeToString([]byte("signature"))
	return header + "." + payload + "." + sig
}

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
			name:     "Invalid format",
			token:    "invalid.token",
			expected: "",
		},
		{
			name:     "Valid JWT with email",
			token:    createTestJWT(map[string]any{"email": "user@example.com"}),
			expected: "user@example.com",
		},
		{
			name:     "Valid JWT with preferred_username",
			token:    createTestJWT(map[string]any{"preferred_username": "user2@example.com"}),
			expected: "user2@example.com",
		},
		{
			name:     "Valid JWT with sub looking like email",
			token:    createTestJWT(map[string]any{"sub": "user3@example.com"}),
			expected: "user3@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractEmailFromJWT(tt.token)
			if got != tt.expected {
				t.Errorf("ExtractEmailFromJWT() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSanitizeEmailForFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"user@example.com", "user@example.com"},
		{"user/name@example.com", "user_name@example.com"},
		{"user\\name:test*@example.com", "user_name_test_@example.com"},
		{"../../../etc/passwd", "_.__.__._etc_passwd"},
		{"%2E%2E%2Fetc%2Fpasswd", "___etc_passwd"},
		{"%2f%2F%5c%5C", "____"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeEmailForFilename(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeEmailForFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractIDCIdentifier(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://d-1234567890.awsapps.com/start", "d-1234567890"},
		{"https://my-company.awsapps.com/start", "my-company"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := ExtractIDCIdentifier(tt.url)
			if got != tt.expected {
				t.Errorf("ExtractIDCIdentifier(%q) = %q, want %q", tt.url, got, tt.expected)
			}
		})
	}
}

func TestParseProfileARN(t *testing.T) {
	validARN := "arn:aws:codewhisperer:us-east-1:123456789012:profile/PROFILERESOURCE"
	parsed := ParseProfileARN(validARN)
	if parsed == nil {
		t.Fatalf("ParseProfileARN(%q) returned nil", validARN)
	}
	if parsed.Region != "us-east-1" {
		t.Errorf("parsed.Region = %q, want us-east-1", parsed.Region)
	}
	if parsed.ResourceType != "profile" {
		t.Errorf("parsed.ResourceType = %q, want profile", parsed.ResourceType)
	}
	if parsed.ResourceID != "PROFILERESOURCE" {
		t.Errorf("parsed.ResourceID = %q, want PROFILERESOURCE", parsed.ResourceID)
	}

	if ExtractRegionFromProfileArn(validARN) != "us-east-1" {
		t.Errorf("ExtractRegionFromProfileArn = %q, want us-east-1", ExtractRegionFromProfileArn(validARN))
	}

	if ParseProfileARN("invalid") != nil {
		t.Errorf("ParseProfileARN(invalid) want nil")
	}
}
