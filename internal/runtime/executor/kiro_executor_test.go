package executor

import (
	"fmt"
	"testing"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestBuildKiroEndpointConfigs(t *testing.T) {
	tests := []struct {
		name           string
		region         string
		expectedURL    string
		expectedOrigin string
		expectedName   string
	}{
		{
			name:           "Empty region - defaults to us-east-1",
			region:         "",
			expectedURL:    "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
			expectedOrigin: "AI_EDITOR",
			expectedName:   "AmazonQ",
		},
		{
			name:           "us-east-1",
			region:         "us-east-1",
			expectedURL:    "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
			expectedOrigin: "AI_EDITOR",
			expectedName:   "AmazonQ",
		},
		{
			name:           "ap-southeast-1",
			region:         "ap-southeast-1",
			expectedURL:    "https://q.ap-southeast-1.amazonaws.com/generateAssistantResponse",
			expectedOrigin: "AI_EDITOR",
			expectedName:   "AmazonQ",
		},
		{
			name:           "eu-west-1",
			region:         "eu-west-1",
			expectedURL:    "https://q.eu-west-1.amazonaws.com/generateAssistantResponse",
			expectedOrigin: "AI_EDITOR",
			expectedName:   "AmazonQ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configs := buildKiroEndpointConfigs(tt.region)

			if len(configs) != 2 {
				t.Fatalf("expected 2 endpoint configs, got %d", len(configs))
			}

			// Check primary endpoint (AmazonQ)
			primary := configs[0]
			if primary.URL != tt.expectedURL {
				t.Errorf("primary URL = %q, want %q", primary.URL, tt.expectedURL)
			}
			if primary.Origin != tt.expectedOrigin {
				t.Errorf("primary Origin = %q, want %q", primary.Origin, tt.expectedOrigin)
			}
			if primary.Name != tt.expectedName {
				t.Errorf("primary Name = %q, want %q", primary.Name, tt.expectedName)
			}
			if primary.AmzTarget != "" {
				t.Errorf("primary AmzTarget should be empty, got %q", primary.AmzTarget)
			}

			// Check fallback endpoint (CodeWhisperer)
			fallback := configs[1]
			if fallback.Name != "CodeWhisperer" {
				t.Errorf("fallback Name = %q, want %q", fallback.Name, "CodeWhisperer")
			}
			// CodeWhisperer fallback uses the same region as Q endpoint
			expectedRegion := tt.region
			if expectedRegion == "" {
				expectedRegion = kiroDefaultRegion
			}
			expectedFallbackURL := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", expectedRegion)
			if fallback.URL != expectedFallbackURL {
				t.Errorf("fallback URL = %q, want %q", fallback.URL, expectedFallbackURL)
			}
			if fallback.AmzTarget == "" {
				t.Error("fallback AmzTarget should NOT be empty")
			}
		})
	}
}

func TestGetKiroEndpointConfigs_NilAuth(t *testing.T) {
	configs := getKiroEndpointConfigs(nil)

	if len(configs) != 2 {
		t.Fatalf("expected 2 endpoint configs, got %d", len(configs))
	}

	// Should return default us-east-1 configs
	if configs[0].Name != "AmazonQ" {
		t.Errorf("first config Name = %q, want %q", configs[0].Name, "AmazonQ")
	}
	expectedURL := "https://q.us-east-1.amazonaws.com/generateAssistantResponse"
	if configs[0].URL != expectedURL {
		t.Errorf("first config URL = %q, want %q", configs[0].URL, expectedURL)
	}
}

func TestGetKiroEndpointConfigs_WithRegionFromProfileArn(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"profile_arn": "arn:aws:codewhisperer:ap-southeast-1:123456789012:profile/ABC",
		},
	}

	configs := getKiroEndpointConfigs(auth)

	if len(configs) != 2 {
		t.Fatalf("expected 2 endpoint configs, got %d", len(configs))
	}

	expectedURL := "https://q.ap-southeast-1.amazonaws.com/generateAssistantResponse"
	if configs[0].URL != expectedURL {
		t.Errorf("primary URL = %q, want %q", configs[0].URL, expectedURL)
	}
}

func TestGetKiroEndpointConfigs_WithApiRegionOverride(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"api_region":  "eu-central-1",
			"profile_arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
		},
	}

	configs := getKiroEndpointConfigs(auth)

	// api_region should take precedence over profile_arn
	expectedURL := "https://q.eu-central-1.amazonaws.com/generateAssistantResponse"
	if configs[0].URL != expectedURL {
		t.Errorf("primary URL = %q, want %q", configs[0].URL, expectedURL)
	}
}

func TestGetKiroEndpointConfigs_PreferredEndpoint(t *testing.T) {
	tests := []struct {
		name              string
		preference        string
		expectedFirstName string
	}{
		{
			name:              "Prefer codewhisperer",
			preference:        "codewhisperer",
			expectedFirstName: "CodeWhisperer",
		},
		{
			name:              "Prefer ide (alias for codewhisperer)",
			preference:        "ide",
			expectedFirstName: "CodeWhisperer",
		},
		{
			name:              "Prefer amazonq",
			preference:        "amazonq",
			expectedFirstName: "AmazonQ",
		},
		{
			name:              "Prefer q (alias for amazonq)",
			preference:        "q",
			expectedFirstName: "AmazonQ",
		},
		{
			name:              "Prefer cli (alias for amazonq)",
			preference:        "cli",
			expectedFirstName: "AmazonQ",
		},
		{
			name:              "Unknown preference - no reordering",
			preference:        "unknown",
			expectedFirstName: "AmazonQ",
		},
		{
			name:              "Empty preference - no reordering",
			preference:        "",
			expectedFirstName: "AmazonQ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{
				Metadata: map[string]any{
					"preferred_endpoint": tt.preference,
				},
			}

			configs := getKiroEndpointConfigs(auth)

			if configs[0].Name != tt.expectedFirstName {
				t.Errorf("first endpoint Name = %q, want %q", configs[0].Name, tt.expectedFirstName)
			}
		})
	}
}

func TestGetKiroEndpointConfigs_PreferredEndpointFromAttributes(t *testing.T) {
	// Test that preferred_endpoint can also come from Attributes
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{},
		Attributes: map[string]string{"preferred_endpoint": "codewhisperer"},
	}

	configs := getKiroEndpointConfigs(auth)

	if configs[0].Name != "CodeWhisperer" {
		t.Errorf("first endpoint Name = %q, want %q", configs[0].Name, "CodeWhisperer")
	}
}

func TestGetKiroEndpointConfigs_MetadataTakesPrecedenceOverAttributes(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata:   map[string]any{"preferred_endpoint": "amazonq"},
		Attributes: map[string]string{"preferred_endpoint": "codewhisperer"},
	}

	configs := getKiroEndpointConfigs(auth)

	// Metadata should take precedence
	if configs[0].Name != "AmazonQ" {
		t.Errorf("first endpoint Name = %q, want %q", configs[0].Name, "AmazonQ")
	}
}

func TestGetAuthValue(t *testing.T) {
	tests := []struct {
		name     string
		auth     *cliproxyauth.Auth
		key      string
		expected string
	}{
		{
			name: "From metadata",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"test_key": "metadata_value"},
			},
			key:      "test_key",
			expected: "metadata_value",
		},
		{
			name: "From attributes (fallback)",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
		{
			name: "Metadata takes precedence",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"test_key": "metadata_value"},
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "metadata_value",
		},
		{
			name: "Key not found",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"other_key": "value"},
				Attributes: map[string]string{"another_key": "value"},
			},
			key:      "test_key",
			expected: "",
		},
		{
			name: "Nil metadata",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
		{
			name:     "Both nil",
			auth:     &cliproxyauth.Auth{},
			key:      "test_key",
			expected: "",
		},
		{
			name: "Value is trimmed and lowercased",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{"test_key": "  UPPER_VALUE  "},
			},
			key:      "test_key",
			expected: "upper_value",
		},
		{
			name: "Empty string value in metadata - falls back to attributes",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"test_key": ""},
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
		{
			name: "Non-string value in metadata - falls back to attributes",
			auth: &cliproxyauth.Auth{
				Metadata:   map[string]any{"test_key": 123},
				Attributes: map[string]string{"test_key": "attribute_value"},
			},
			key:      "test_key",
			expected: "attribute_value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getAuthValue(tt.auth, tt.key)
			if result != tt.expected {
				t.Errorf("getAuthValue() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGetAccountKey(t *testing.T) {
	tests := []struct {
		name    string
		auth    *cliproxyauth.Auth
		checkFn func(t *testing.T, result string)
	}{
		{
			name: "From client_id",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"client_id":     "test-client-id-123",
					"refresh_token": "test-refresh-token-456",
				},
			},
			checkFn: func(t *testing.T, result string) {
				expected := kiroauth.GetAccountKey("test-client-id-123", "test-refresh-token-456")
				if result != expected {
					t.Errorf("expected %s, got %s", expected, result)
				}
			},
		},
		{
			name: "From refresh_token only",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{
					"refresh_token": "test-refresh-token-789",
				},
			},
			checkFn: func(t *testing.T, result string) {
				expected := kiroauth.GetAccountKey("", "test-refresh-token-789")
				if result != expected {
					t.Errorf("expected %s, got %s", expected, result)
				}
			},
		},
		{
			name: "Nil auth",
			auth: nil,
			checkFn: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
			},
		},
		{
			name: "Nil metadata",
			auth: &cliproxyauth.Auth{},
			checkFn: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
			},
		},
		{
			name: "Empty metadata",
			auth: &cliproxyauth.Auth{
				Metadata: map[string]any{},
			},
			checkFn: func(t *testing.T, result string) {
				if len(result) != 16 {
					t.Errorf("expected 16 char key, got %d chars", len(result))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getAccountKey(tt.auth)
			tt.checkFn(t, result)
		})
	}
}

func TestEndpointAliases(t *testing.T) {
	// Verify all expected aliases are defined
	expectedAliases := map[string]string{
		"codewhisperer": "codewhisperer",
		"ide":           "codewhisperer",
		"amazonq":       "amazonq",
		"q":             "amazonq",
		"cli":           "amazonq",
	}

	for alias, target := range expectedAliases {
		if actual, ok := endpointAliases[alias]; !ok {
			t.Errorf("missing alias %q", alias)
		} else if actual != target {
			t.Errorf("alias %q = %q, want %q", alias, actual, target)
		}
	}

	// Verify no unexpected aliases
	if len(endpointAliases) != len(expectedAliases) {
		t.Errorf("unexpected number of aliases: got %d, want %d", len(endpointAliases), len(expectedAliases))
	}
}

func TestMapModelToKiro_MapsClaudeOpus47Variants(t *testing.T) {
	executor := &KiroExecutor{}
	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{
			name:     "kiro alias",
			model:    "kiro-claude-opus-4-7",
			expected: "claude-opus-4.7",
		},
		{
			name:     "kiro agentic alias",
			model:    "kiro-claude-opus-4-7-agentic",
			expected: "claude-opus-4.7",
		},
		{
			name:     "native hyphen alias",
			model:    "claude-opus-4-7",
			expected: "claude-opus-4.7",
		},
		{
			name:     "native dotted alias",
			model:    "claude-opus-4.7",
			expected: "claude-opus-4.7",
		},
		{
			name:     "native agentic alias",
			model:    "claude-opus-4.7-agentic",
			expected: "claude-opus-4.7",
		},
		{
			name:     "dated alias collapses to canonical version",
			model:    "claude-sonnet-4-5-20250929",
			expected: "claude-sonnet-4.5",
		},
		{
			name:     "amazonq prefix",
			model:    "amazonq-claude-sonnet-4-5",
			expected: "claude-sonnet-4.5",
		},
		{
			name:     "claude opus 4.5 hyphenated alias",
			model:    "kiro-claude-opus-4-5",
			expected: "claude-opus-4.5",
		},
		{
			name:     "claude haiku 4.5 hyphenated alias",
			model:    "kiro-claude-haiku-4-5",
			expected: "claude-haiku-4.5",
		},
		{
			name:     "claude sonnet 4.6 hyphenated alias",
			model:    "kiro-claude-sonnet-4-6",
			expected: "claude-sonnet-4.6",
		},
		{
			name:     "minimax m2.1 hyphenated alias",
			model:    "kiro-minimax-m2-1",
			expected: "minimax-m2.1",
		},
		{
			name:     "non-Claude model passes through",
			model:    "kiro-glm-5",
			expected: "glm-5",
		},
		{
			name:     "non-Claude minimax versioned",
			model:    "kiro-minimax-m2-5",
			expected: "minimax-m2.5",
		},
		{
			name:     "gpt version with trailing codename (terra)",
			model:    "kiro-gpt-5-6-terra",
			expected: "gpt-5.6-terra",
		},
		{
			name:     "canonical gpt version remains unchanged",
			model:    "gpt-5.6-terra",
			expected: "gpt-5.6-terra",
		},
		{
			name:     "gpt version with trailing codename (sol)",
			model:    "kiro-gpt-5-6-sol",
			expected: "gpt-5.6-sol",
		},
		{
			name:     "gpt version with trailing codename (luna)",
			model:    "kiro-gpt-5-6-luna",
			expected: "gpt-5.6-luna",
		},
		{
			name:     "kimi version with trailing codename",
			model:    "kiro-kimi-k2-7-code",
			expected: "kimi-k2.7-code",
		},
		{
			name:     "canonical kimi version remains unchanged",
			model:    "kimi-k2.7-code",
			expected: "kimi-k2.7-code",
		},
		{
			name:     "deepseek versioned",
			model:    "kiro-deepseek-3-2",
			expected: "deepseek-3.2",
		},
		{
			name:     "canonical deepseek version remains unchanged",
			model:    "deepseek-3.2",
			expected: "deepseek-3.2",
		},
		{
			name:     "grok version keeps trailing build segment",
			model:    "kiro-grok-4-20-0309-reasoning",
			expected: "grok-4.20-0309-reasoning",
		},
		{
			name:     "canonical grok version remains unchanged",
			model:    "grok-4.20-0309-reasoning",
			expected: "grok-4.20-0309-reasoning",
		},
		{
			name:     "single digit segment left alone",
			model:    "kiro-claude-sonnet-5",
			expected: "claude-sonnet-5",
		},
		{
			name:     "identifier without trailing version unchanged",
			model:    "kiro-qwen3-coder-next",
			expected: "qwen3-coder-next",
		},
		{
			name:     "unknown model passes through unchanged",
			model:    "kiro-future-model-9",
			expected: "future-model-9",
		},
		{
			name:     "unknown date-like numeric ID passes through unchanged",
			model:    "kiro-model-2024-05-preview",
			expected: "model-2024-05-preview",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executor.mapModelToKiro(tt.model)
			if got != tt.expected {
				t.Fatalf("mapModelToKiro(%q) = %q, want %q", tt.model, got, tt.expected)
			}
			if normalizedAgain := executor.mapModelToKiro(got); normalizedAgain != got {
				t.Fatalf("mapModelToKiro() is not idempotent: first = %q, second = %q", got, normalizedAgain)
			}
		})
	}
}

func TestApplyKiroTokenUsagePreservesCacheTokenBreakdown(t *testing.T) {
	detail := usage.Detail{}
	ok := applyKiroTokenUsage(&detail, map[string]interface{}{
		"uncachedInputTokens":   float64(290),
		"outputTokens":          float64(1),
		"totalTokens":           float64(4130),
		"cacheReadInputTokens":  float64(3822),
		"cacheWriteInputTokens": float64(17),
	})
	if !ok {
		t.Fatal("applyKiroTokenUsage() = false, want true")
	}
	if detail.InputTokens != 290 {
		t.Fatalf("InputTokens = %d, want 290", detail.InputTokens)
	}
	if detail.OutputTokens != 1 {
		t.Fatalf("OutputTokens = %d, want 1", detail.OutputTokens)
	}
	if detail.TotalTokens != 4130 {
		t.Fatalf("TotalTokens = %d, want 4130", detail.TotalTokens)
	}
	if detail.CacheReadTokens != 3822 {
		t.Fatalf("CacheReadTokens = %d, want 3822", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 17 {
		t.Fatalf("CacheCreationTokens = %d, want 17", detail.CacheCreationTokens)
	}
	if detail.CachedTokens != 3822 {
		t.Fatalf("CachedTokens = %d, want 3822", detail.CachedTokens)
	}
}

func TestFinalizeKiroUsageTotalIncludesCacheTokensWhenUpstreamTotalMissing(t *testing.T) {
	detail := usage.Detail{
		InputTokens:         290,
		OutputTokens:        1,
		CacheReadTokens:     3822,
		CacheCreationTokens: 17,
	}
	finalizeKiroUsageTotal(&detail)
	if detail.TotalTokens != 4130 {
		t.Fatalf("TotalTokens = %d, want 4130", detail.TotalTokens)
	}
}

func TestKiroContextUsageFallbackDoesNotOverwritePreciseTokenUsage(t *testing.T) {
	detail := usage.Detail{}
	hasPreciseTokenUsage := applyKiroTokenUsage(&detail, map[string]interface{}{
		"uncachedInputTokens":    float64(290),
		"outputTokens":           float64(1),
		"totalTokens":            float64(4130),
		"cacheReadInputTokens":   float64(3822),
		"cacheWriteInputTokens":  float64(17),
		"contextUsagePercentage": float64(50),
	})
	if !hasPreciseTokenUsage {
		t.Fatal("applyKiroTokenUsage() = false, want true")
	}
	if _, applied := applyKiroContextUsageFallback(&detail, 50, hasPreciseTokenUsage); applied {
		t.Fatal("applyKiroContextUsageFallback() applied despite precise token usage")
	}
	if detail.InputTokens != 290 {
		t.Fatalf("InputTokens = %d, want precise uncached value 290", detail.InputTokens)
	}
	if detail.TotalTokens != 4130 {
		t.Fatalf("TotalTokens = %d, want upstream total 4130", detail.TotalTokens)
	}
}

func TestKiroContextUsageFallbackAppliesWhenPreciseTokenUsageMissing(t *testing.T) {
	detail := usage.Detail{OutputTokens: 3}
	calculated, applied := applyKiroContextUsageFallback(&detail, 50, false)
	if !applied {
		t.Fatal("applyKiroContextUsageFallback() applied = false, want true")
	}
	if calculated != 100000 {
		t.Fatalf("calculated input tokens = %d, want 100000", calculated)
	}
	if detail.InputTokens != 100000 {
		t.Fatalf("InputTokens = %d, want 100000", detail.InputTokens)
	}
	if detail.TotalTokens != 100003 {
		t.Fatalf("TotalTokens = %d, want 100003", detail.TotalTokens)
	}
}
