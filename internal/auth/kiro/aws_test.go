package kiro

import (
	"encoding/base64"
	"encoding/json"
	"strings"
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
		exact     string // exact match (for cases with email)
		prefix    string // prefix match (for cases without email, where sequence is appended)
	}{
		{
			name: "IDC with email",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "user@example.com",
				StartURL:   "https://d-1234567890.awsapps.com/start",
			},
			exact: "kiro-idc-user-example-com.json",
		},
		{
			name: "IDC without email but with startUrl",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "",
				StartURL:   "https://d-1234567890.awsapps.com/start",
			},
			prefix: "kiro-idc-d-1234567890-",
		},
		{
			name: "IDC with company name in startUrl",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "",
				StartURL:   "https://my-company.awsapps.com/start",
			},
			prefix: "kiro-idc-my-company-",
		},
		{
			name: "IDC without email and without startUrl",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "",
				StartURL:   "",
			},
			prefix: "kiro-idc-",
		},
		{
			name: "Builder ID with email",
			tokenData: &KiroTokenData{
				AuthMethod: "builder-id",
				Email:      "user@gmail.com",
				StartURL:   "https://view.awsapps.com/start",
			},
			exact: "kiro-builder-id-user-gmail-com.json",
		},
		{
			name: "Builder ID without email",
			tokenData: &KiroTokenData{
				AuthMethod: "builder-id",
				Email:      "",
				StartURL:   "https://view.awsapps.com/start",
			},
			prefix: "kiro-builder-id-",
		},
		{
			name: "Social auth with email",
			tokenData: &KiroTokenData{
				AuthMethod: "google",
				Email:      "user@gmail.com",
			},
			exact: "kiro-google-user-gmail-com.json",
		},
		{
			name: "Empty auth method",
			tokenData: &KiroTokenData{
				AuthMethod: "",
				Email:      "",
			},
			prefix: "kiro-unknown-",
		},
		{
			name: "Email with special characters",
			tokenData: &KiroTokenData{
				AuthMethod: "idc",
				Email:      "user.name+tag@sub.example.com",
				StartURL:   "https://d-1234567890.awsapps.com/start",
			},
			exact: "kiro-idc-user-name+tag-sub-example-com.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateTokenFileName(tt.tokenData)
			if tt.exact != "" {
				if result != tt.exact {
					t.Errorf("GenerateTokenFileName() = %q, want %q", result, tt.exact)
				}
			} else if tt.prefix != "" {
				if !strings.HasPrefix(result, tt.prefix) || !strings.HasSuffix(result, ".json") {
					t.Errorf("GenerateTokenFileName() = %q, want prefix %q with .json suffix", result, tt.prefix)
				}
			}
		})
	}
}

func TestParseProfileARN(t *testing.T) {
	tests := []struct {
		name     string
		arn      string
		expected *ProfileARN
	}{
		{
			name:     "Empty ARN",
			arn:      "",
			expected: nil,
		},
		{
			name:     "Invalid format - too few parts",
			arn:      "arn:aws:codewhisperer",
			expected: nil,
		},
		{
			name:     "Invalid prefix - not arn",
			arn:      "notarn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
			expected: nil,
		},
		{
			name:     "Invalid service - not codewhisperer",
			arn:      "arn:aws:s3:us-east-1:123456789012:bucket/mybucket",
			expected: nil,
		},
		{
			name:     "Invalid region - no hyphen",
			arn:      "arn:aws:codewhisperer:useast1:123456789012:profile/ABC",
			expected: nil,
		},
		{
			name:     "Empty partition",
			arn:      "arn::codewhisperer:us-east-1:123456789012:profile/ABC",
			expected: nil,
		},
		{
			name:     "Empty region",
			arn:      "arn:aws:codewhisperer::123456789012:profile/ABC",
			expected: nil,
		},
		{
			name: "Valid ARN - us-east-1",
			arn:  "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL",
			expected: &ProfileARN{
				Raw:          "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEFGHIJKL",
				Partition:    "aws",
				Service:      "codewhisperer",
				Region:       "us-east-1",
				AccountID:    "123456789012",
				ResourceType: "profile",
				ResourceID:   "ABCDEFGHIJKL",
			},
		},
		{
			name: "Valid ARN - ap-southeast-1",
			arn:  "arn:aws:codewhisperer:ap-southeast-1:987654321098:profile/ZYXWVUTSRQ",
			expected: &ProfileARN{
				Raw:          "arn:aws:codewhisperer:ap-southeast-1:987654321098:profile/ZYXWVUTSRQ",
				Partition:    "aws",
				Service:      "codewhisperer",
				Region:       "ap-southeast-1",
				AccountID:    "987654321098",
				ResourceType: "profile",
				ResourceID:   "ZYXWVUTSRQ",
			},
		},
		{
			name: "Valid ARN - eu-west-1",
			arn:  "arn:aws:codewhisperer:eu-west-1:111222333444:profile/PROFILE123",
			expected: &ProfileARN{
				Raw:          "arn:aws:codewhisperer:eu-west-1:111222333444:profile/PROFILE123",
				Partition:    "aws",
				Service:      "codewhisperer",
				Region:       "eu-west-1",
				AccountID:    "111222333444",
				ResourceType: "profile",
				ResourceID:   "PROFILE123",
			},
		},
		{
			name: "Valid ARN - aws-cn partition",
			arn:  "arn:aws-cn:codewhisperer:cn-north-1:123456789012:profile/CHINAID",
			expected: &ProfileARN{
				Raw:          "arn:aws-cn:codewhisperer:cn-north-1:123456789012:profile/CHINAID",
				Partition:    "aws-cn",
				Service:      "codewhisperer",
				Region:       "cn-north-1",
				AccountID:    "123456789012",
				ResourceType: "profile",
				ResourceID:   "CHINAID",
			},
		},
		{
			name: "Valid ARN - resource without slash",
			arn:  "arn:aws:codewhisperer:us-west-2:123456789012:profile",
			expected: &ProfileARN{
				Raw:          "arn:aws:codewhisperer:us-west-2:123456789012:profile",
				Partition:    "aws",
				Service:      "codewhisperer",
				Region:       "us-west-2",
				AccountID:    "123456789012",
				ResourceType: "profile",
				ResourceID:   "",
			},
		},
		{
			name: "Valid ARN - resource with colon",
			arn:  "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC:extra",
			expected: &ProfileARN{
				Raw:          "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC:extra",
				Partition:    "aws",
				Service:      "codewhisperer",
				Region:       "us-east-1",
				AccountID:    "123456789012",
				ResourceType: "profile",
				ResourceID:   "ABC:extra",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseProfileARN(tt.arn)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("ParseProfileARN(%q) = %+v, want nil", tt.arn, result)
				}
				return
			}
			if result == nil {
				t.Errorf("ParseProfileARN(%q) = nil, want %+v", tt.arn, tt.expected)
				return
			}
			if result.Raw != tt.expected.Raw {
				t.Errorf("Raw = %q, want %q", result.Raw, tt.expected.Raw)
			}
			if result.Partition != tt.expected.Partition {
				t.Errorf("Partition = %q, want %q", result.Partition, tt.expected.Partition)
			}
			if result.Service != tt.expected.Service {
				t.Errorf("Service = %q, want %q", result.Service, tt.expected.Service)
			}
			if result.Region != tt.expected.Region {
				t.Errorf("Region = %q, want %q", result.Region, tt.expected.Region)
			}
			if result.AccountID != tt.expected.AccountID {
				t.Errorf("AccountID = %q, want %q", result.AccountID, tt.expected.AccountID)
			}
			if result.ResourceType != tt.expected.ResourceType {
				t.Errorf("ResourceType = %q, want %q", result.ResourceType, tt.expected.ResourceType)
			}
			if result.ResourceID != tt.expected.ResourceID {
				t.Errorf("ResourceID = %q, want %q", result.ResourceID, tt.expected.ResourceID)
			}
		})
	}
}

func TestExtractRegionFromProfileArn(t *testing.T) {
	tests := []struct {
		name       string
		profileArn string
		expected   string
	}{
		{
			name:       "Empty ARN",
			profileArn: "",
			expected:   "",
		},
		{
			name:       "Invalid ARN",
			profileArn: "invalid-arn",
			expected:   "",
		},
		{
			name:       "Valid ARN - us-east-1",
			profileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
			expected:   "us-east-1",
		},
		{
			name:       "Valid ARN - ap-southeast-1",
			profileArn: "arn:aws:codewhisperer:ap-southeast-1:123456789012:profile/ABC",
			expected:   "ap-southeast-1",
		},
		{
			name:       "Valid ARN - eu-central-1",
			profileArn: "arn:aws:codewhisperer:eu-central-1:123456789012:profile/ABC",
			expected:   "eu-central-1",
		},
		{
			name:       "Non-codewhisperer ARN",
			profileArn: "arn:aws:s3:us-east-1:123456789012:bucket/mybucket",
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractRegionFromProfileArn(tt.profileArn)
			if result != tt.expected {
				t.Errorf("ExtractRegionFromProfileArn(%q) = %q, want %q", tt.profileArn, result, tt.expected)
			}
		})
	}
}

func TestGetKiroAPIEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "Empty region - defaults to us-east-1",
			region:   "",
			expected: "https://q.us-east-1.amazonaws.com",
		},
		{
			name:     "us-east-1",
			region:   "us-east-1",
			expected: "https://q.us-east-1.amazonaws.com",
		},
		{
			name:     "us-west-2",
			region:   "us-west-2",
			expected: "https://q.us-west-2.amazonaws.com",
		},
		{
			name:     "ap-southeast-1",
			region:   "ap-southeast-1",
			expected: "https://q.ap-southeast-1.amazonaws.com",
		},
		{
			name:     "eu-west-1",
			region:   "eu-west-1",
			expected: "https://q.eu-west-1.amazonaws.com",
		},
		{
			name:     "cn-north-1",
			region:   "cn-north-1",
			expected: "https://q.cn-north-1.amazonaws.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetKiroAPIEndpoint(tt.region)
			if result != tt.expected {
				t.Errorf("GetKiroAPIEndpoint(%q) = %q, want %q", tt.region, result, tt.expected)
			}
		})
	}
}

func TestGetKiroAPIEndpointFromProfileArn(t *testing.T) {
	tests := []struct {
		name       string
		profileArn string
		expected   string
	}{
		{
			name:       "Empty ARN - defaults to us-east-1",
			profileArn: "",
			expected:   "https://q.us-east-1.amazonaws.com",
		},
		{
			name:       "Invalid ARN - defaults to us-east-1",
			profileArn: "invalid-arn",
			expected:   "https://q.us-east-1.amazonaws.com",
		},
		{
			name:       "Valid ARN - us-east-1",
			profileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
			expected:   "https://q.us-east-1.amazonaws.com",
		},
		{
			name:       "Valid ARN - ap-southeast-1",
			profileArn: "arn:aws:codewhisperer:ap-southeast-1:123456789012:profile/ABC",
			expected:   "https://q.ap-southeast-1.amazonaws.com",
		},
		{
			name:       "Valid ARN - eu-central-1",
			profileArn: "arn:aws:codewhisperer:eu-central-1:123456789012:profile/ABC",
			expected:   "https://q.eu-central-1.amazonaws.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetKiroAPIEndpointFromProfileArn(tt.profileArn)
			if result != tt.expected {
				t.Errorf("GetKiroAPIEndpointFromProfileArn(%q) = %q, want %q", tt.profileArn, result, tt.expected)
			}
		})
	}
}

func TestGetCodeWhispererLegacyEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "Empty region - defaults to us-east-1",
			region:   "",
			expected: "https://codewhisperer.us-east-1.amazonaws.com",
		},
		{
			name:     "us-east-1",
			region:   "us-east-1",
			expected: "https://codewhisperer.us-east-1.amazonaws.com",
		},
		{
			name:     "us-west-2",
			region:   "us-west-2",
			expected: "https://codewhisperer.us-west-2.amazonaws.com",
		},
		{
			name:     "ap-northeast-1",
			region:   "ap-northeast-1",
			expected: "https://codewhisperer.ap-northeast-1.amazonaws.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetCodeWhispererLegacyEndpoint(tt.region)
			if result != tt.expected {
				t.Errorf("GetCodeWhispererLegacyEndpoint(%q) = %q, want %q", tt.region, result, tt.expected)
			}
		})
	}
}

func TestExtractRegionFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]interface{}
		expected string
	}{
		{
			name:     "Nil metadata - defaults to us-east-1",
			metadata: nil,
			expected: "us-east-1",
		},
		{
			name:     "Empty metadata - defaults to us-east-1",
			metadata: map[string]interface{}{},
			expected: "us-east-1",
		},
		{
			name: "Priority 1: api_region override",
			metadata: map[string]interface{}{
				"api_region":  "eu-west-1",
				"profile_arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
			},
			expected: "eu-west-1",
		},
		{
			name: "Priority 2: profile_arn when api_region is empty",
			metadata: map[string]interface{}{
				"api_region":  "",
				"profile_arn": "arn:aws:codewhisperer:ap-southeast-1:123456789012:profile/ABC",
			},
			expected: "ap-southeast-1",
		},
		{
			name: "Priority 2: profile_arn when api_region is missing",
			metadata: map[string]interface{}{
				"profile_arn": "arn:aws:codewhisperer:eu-central-1:123456789012:profile/ABC",
			},
			expected: "eu-central-1",
		},
		{
			name: "Fallback: default when profile_arn is invalid",
			metadata: map[string]interface{}{
				"profile_arn": "invalid-arn",
			},
			expected: "us-east-1",
		},
		{
			name: "Fallback: default when profile_arn is empty",
			metadata: map[string]interface{}{
				"profile_arn": "",
			},
			expected: "us-east-1",
		},
		{
			name: "OIDC region is NOT used for API region",
			metadata: map[string]interface{}{
				"region": "ap-northeast-2", // OIDC region - should be ignored
			},
			expected: "us-east-1",
		},
		{
			name: "api_region takes precedence over OIDC region",
			metadata: map[string]interface{}{
				"api_region": "us-west-2",
				"region":     "ap-northeast-2", // OIDC region - should be ignored
			},
			expected: "us-west-2",
		},
		{
			name: "Non-string api_region is ignored",
			metadata: map[string]interface{}{
				"api_region":  123, // wrong type
				"profile_arn": "arn:aws:codewhisperer:ap-south-1:123456789012:profile/ABC",
			},
			expected: "ap-south-1",
		},
		{
			name: "Non-string profile_arn is ignored",
			metadata: map[string]interface{}{
				"profile_arn": 123, // wrong type
			},
			expected: "us-east-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractRegionFromMetadata(tt.metadata)
			if result != tt.expected {
				t.Errorf("ExtractRegionFromMetadata(%v) = %q, want %q", tt.metadata, result, tt.expected)
			}
		})
	}
}

