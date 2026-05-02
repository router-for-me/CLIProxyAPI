package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
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

func TestParseEventStream_KiroCacheUsageDoesNotDoubleCountInput(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"totalTokens": 22,
				"uncachedInputTokens": 10,
				"cacheReadInputTokens": 7,
				"cacheWriteInputTokens": 3,
				"contextUsagePercentage": 50
			}
		}
	}`))
	// A repeated metadata event should update values without accumulating cached tokens twice.
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"totalTokens": 22,
				"uncachedInputTokens": 10,
				"cacheReadInputTokens": 7,
				"cacheWriteInputTokens": 3,
				"contextUsagePercentage": 50
			}
		}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "kiro-claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want uncached input 10", usageInfo.InputTokens)
	}
	if usageInfo.CacheReadInputTokens != 7 {
		t.Fatalf("CacheReadInputTokens = %d, want 7", usageInfo.CacheReadInputTokens)
	}
	if usageInfo.CacheCreationInputTokens != 3 {
		t.Fatalf("CacheCreationInputTokens = %d, want 3", usageInfo.CacheCreationInputTokens)
	}
	if usageInfo.CachedTokens != 10 {
		t.Fatalf("CachedTokens = %d, want read+write 10", usageInfo.CachedTokens)
	}
	if usageInfo.TotalTokens != 22 {
		t.Fatalf("TotalTokens = %d, want upstream total 22", usageInfo.TotalTokens)
	}
}

func TestEstimateKiroCacheUsageFromCredits(t *testing.T) {
	detail, estimated := estimateKiroCacheUsageFromCredits("kiro-claude-sonnet-4-5", usage.Detail{
		InputTokens:  10000,
		OutputTokens: 1000,
		TotalTokens:  11000,
	}, 0.7875)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}

	if detail.CacheReadInputTokens != 5000 {
		t.Fatalf("CacheReadInputTokens = %d, want estimated read tokens 5000", detail.CacheReadInputTokens)
	}
	if detail.InputTokens != 5000 {
		t.Fatalf("InputTokens = %d, want estimated uncached input 5000", detail.InputTokens)
	}
	if detail.CachedTokens != 5000 {
		t.Fatalf("CachedTokens = %d, want 5000", detail.CachedTokens)
	}
	if detail.TotalTokens != 11000 {
		t.Fatalf("TotalTokens = %d, want original total 11000", detail.TotalTokens)
	}

	internalDetail := usageDetailForInternalStats(detail, true)
	if internalDetail.InputTokens != 10000 {
		t.Fatalf("internal InputTokens = %d, want total input restored 10000", internalDetail.InputTokens)
	}
	if internalDetail.CachedTokens != 0 || internalDetail.CacheReadInputTokens != 0 {
		t.Fatalf("internal cache fields = cached:%d read:%d, want cleared", internalDetail.CachedTokens, internalDetail.CacheReadInputTokens)
	}
}

func TestEstimateKiroCacheUsageFromCredits_AllInputCached(t *testing.T) {
	detail, estimated := estimateKiroCacheUsageFromCredits("kiro-claude-sonnet-4-5", usage.Detail{
		InputTokens:  10000,
		OutputTokens: 1000,
		TotalTokens:  11000,
	}, 0.4125)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.InputTokens != 0 {
		t.Fatalf("InputTokens = %d, want zero uncached input", detail.InputTokens)
	}
	if detail.CacheReadInputTokens != 10000 {
		t.Fatalf("CacheReadInputTokens = %d, want all input cached", detail.CacheReadInputTokens)
	}
	if !hasKiroPromptCacheUsage(detail) {
		t.Fatalf("hasKiroPromptCacheUsage = false, want true")
	}

	internalDetail := usageDetailForInternalStats(detail, true)
	if internalDetail.InputTokens != 10000 {
		t.Fatalf("internal InputTokens = %d, want restored total input", internalDetail.InputTokens)
	}
}

func TestEstimateKiroCacheUsageFromCredits_PreservesOfficialCacheUsage(t *testing.T) {
	detail, estimated := estimateKiroCacheUsageFromCredits("kiro-claude-sonnet-4-5", usage.Detail{
		InputTokens:          10000,
		OutputTokens:         1000,
		CacheReadInputTokens: 123,
		CachedTokens:         123,
		TotalTokens:          11123,
	}, 0.7875)
	if estimated {
		t.Fatalf("estimated = true, want false for official cache usage")
	}

	if detail.CacheReadInputTokens != 123 {
		t.Fatalf("CacheReadInputTokens = %d, want official value 123", detail.CacheReadInputTokens)
	}
	if detail.InputTokens != 10000 {
		t.Fatalf("InputTokens = %d, want official uncached input preserved", detail.InputTokens)
	}
}

func TestInferKiroCacheUsageFromTotal(t *testing.T) {
	detail, estimated := inferKiroCacheUsageFromTotal(usage.Detail{
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  22,
	})
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}

	if detail.CacheReadInputTokens != 10 {
		t.Fatalf("CacheReadInputTokens = %d, want inferred read tokens 10", detail.CacheReadInputTokens)
	}
	if detail.CachedTokens != 10 {
		t.Fatalf("CachedTokens = %d, want 10", detail.CachedTokens)
	}
	if detail.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want original uncached input preserved", detail.InputTokens)
	}
}

func TestInferKiroCacheUsageFromTotal_FillsReadTokensFromCachedTokens(t *testing.T) {
	detail, estimated := inferKiroCacheUsageFromTotal(usage.Detail{
		InputTokens:  10,
		OutputTokens: 2,
		CachedTokens: 7,
		TotalTokens:  19,
	})
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}

	if detail.CacheReadInputTokens != 7 {
		t.Fatalf("CacheReadInputTokens = %d, want cached token fallback 7", detail.CacheReadInputTokens)
	}
}

func TestParseEventStream_PartialTokenUsageStillEstimatesFromCredits(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 1000,
				"totalTokens": 11000
			}
		}
	}`))
	writeKiroTestEvent(t, &stream, "usageEvent", []byte(`{
		"inputTokens": 10000,
		"outputTokens": 1000,
		"totalTokens": 11000
	}`))
	writeKiroTestEvent(t, &stream, "meteringEvent", []byte(`{
		"meteringEvent": {"unit": "credit", "unitPlural": "credits", "usage": 0.7875}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, estimatedCacheUsage, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "kiro-claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.CachedTokens != 5000 {
		t.Fatalf("CachedTokens = %d, want credits-estimated cache 5000", usageInfo.CachedTokens)
	}
	if !estimatedCacheUsage {
		t.Fatalf("estimatedCacheUsage = false, want true")
	}
	if usageInfo.CacheReadInputTokens != 5000 {
		t.Fatalf("CacheReadInputTokens = %d, want estimated read tokens 5000", usageInfo.CacheReadInputTokens)
	}
}

func TestParseEventStream_AccumulatesMeteringEvents(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "usageEvent", []byte(`{
		"inputTokens": 10000,
		"outputTokens": 1000,
		"totalTokens": 11000
	}`))
	writeKiroTestEvent(t, &stream, "meteringEvent", []byte(`{
		"meteringEvent": {"unit": "credit", "unitPlural": "credits", "usage": 0.3}
	}`))
	writeKiroTestEvent(t, &stream, "meteringEvent", []byte(`{
		"meteringEvent": {"unit": "credit", "unitPlural": "credits", "usage": 0.4875}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "kiro-claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.CachedTokens != 5000 {
		t.Fatalf("CachedTokens = %d, want cache estimate based on accumulated credits", usageInfo.CachedTokens)
	}
}

func TestParseEventStream_DoesNotInferCacheFromGenericTotalTokens(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"inputTokens": 10,
			"outputTokens": 2,
			"totalTokens": 22
		}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "kiro-claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.CacheReadInputTokens != 0 {
		t.Fatalf("CacheReadInputTokens = %d, want no inference from generic totalTokens", usageInfo.CacheReadInputTokens)
	}
	if usageInfo.CachedTokens != 0 {
		t.Fatalf("CachedTokens = %d, want no inference from generic totalTokens", usageInfo.CachedTokens)
	}
}

func TestStreamToChannel_KiroCacheUsagePreservesOfficialTokenUsage(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "assistantResponseEvent", []byte(`{
		"assistantResponseEvent": {"content": "hello"}
	}`))
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"totalTokens": 22,
				"uncachedInputTokens": 10,
				"cacheReadInputTokens": 7,
				"cacheWriteInputTokens": 3,
				"contextUsagePercentage": 50
			}
		}
	}`))

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	executor := &KiroExecutor{}
	executor.streamToChannel(
		context.Background(),
		bytes.NewReader(stream.Bytes()),
		out,
		sdktranslator.FromString("claude"),
		"claude-sonnet-4",
		nil,
		[]byte(`{"messages":[]}`),
		nil,
		false,
	)
	close(out)

	var messageDelta string
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload := string(chunk.Payload)
		if strings.HasPrefix(payload, "event: message_delta\ndata: ") {
			messageDelta = strings.TrimPrefix(payload, "event: message_delta\ndata: ")
			messageDelta = strings.TrimSpace(messageDelta)
		}
	}
	if messageDelta == "" {
		t.Fatalf("expected message_delta event")
	}
	if got := gjson.Get(messageDelta, "usage.input_tokens").Int(); got != 10 {
		t.Fatalf("usage.input_tokens = %d, want uncached input 10", got)
	}
	if got := gjson.Get(messageDelta, "usage.cache_read_input_tokens").Int(); got != 7 {
		t.Fatalf("usage.cache_read_input_tokens = %d, want 7", got)
	}
	if got := gjson.Get(messageDelta, "usage.cache_creation_input_tokens").Int(); got != 3 {
		t.Fatalf("usage.cache_creation_input_tokens = %d, want 3", got)
	}
}

func TestStreamToChannel_IgnoresNonCreditMeteringEvents(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "assistantResponseEvent", []byte(`{
		"assistantResponseEvent": {"content": "hello"}
	}`))
	writeKiroTestEvent(t, &stream, "usageEvent", []byte(`{
		"inputTokens": 10000,
		"outputTokens": 1000,
		"totalTokens": 11000
	}`))
	writeKiroTestEvent(t, &stream, "meteringEvent", []byte(`{
		"meteringEvent": {"unit": "request", "usage": 0.7875}
	}`))

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	executor := &KiroExecutor{}
	executor.streamToChannel(
		context.Background(),
		bytes.NewReader(stream.Bytes()),
		out,
		sdktranslator.FromString("claude"),
		"claude-sonnet-4",
		nil,
		[]byte(`{"messages":[]}`),
		nil,
		false,
	)
	close(out)

	var messageDelta string
	for chunk := range out {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload := string(chunk.Payload)
		if strings.HasPrefix(payload, "event: message_delta\ndata: ") {
			messageDelta = strings.TrimSpace(strings.TrimPrefix(payload, "event: message_delta\ndata: "))
		}
	}
	if messageDelta == "" {
		t.Fatalf("expected message_delta event")
	}
	if got := gjson.Get(messageDelta, "usage.cache_read_input_tokens").Int(); got != 0 {
		t.Fatalf("usage.cache_read_input_tokens = %d, want no estimate from non-credit usage", got)
	}
}

func writeKiroTestEvent(t *testing.T, dst *bytes.Buffer, eventType string, payload []byte) {
	t.Helper()

	var headers bytes.Buffer
	headers.WriteByte(byte(len(":event-type")))
	headers.WriteString(":event-type")
	headers.WriteByte(7) // string
	if err := binary.Write(&headers, binary.BigEndian, uint16(len(eventType))); err != nil {
		t.Fatalf("write header length: %v", err)
	}
	headers.WriteString(eventType)

	headersBytes := headers.Bytes()
	totalLength := uint32(12 + len(headersBytes) + len(payload) + 4)

	if err := binary.Write(dst, binary.BigEndian, totalLength); err != nil {
		t.Fatalf("write total length: %v", err)
	}
	if err := binary.Write(dst, binary.BigEndian, uint32(len(headersBytes))); err != nil {
		t.Fatalf("write headers length: %v", err)
	}
	if err := binary.Write(dst, binary.BigEndian, uint32(0)); err != nil {
		t.Fatalf("write prelude crc: %v", err)
	}
	if _, err := dst.Write(headersBytes); err != nil {
		t.Fatalf("write headers: %v", err)
	}
	if _, err := dst.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := binary.Write(dst, binary.BigEndian, uint32(0)); err != nil {
		t.Fatalf("write message crc: %v", err)
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
