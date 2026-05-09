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
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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

func TestEstimateKiroCacheUsage_NoCredits(t *testing.T) {
	// Branch C: total only, no credits. All inferred cache → CR.
	detail, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  22,
	}, 0, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheReadInputTokens != 10 {
		t.Fatalf("CacheReadInputTokens = %d, want 10", detail.CacheReadInputTokens)
	}
	if detail.CacheCreationInputTokens != 0 {
		t.Fatalf("CacheCreationInputTokens = %d, want 0", detail.CacheCreationInputTokens)
	}
	if detail.CachedTokens != 10 {
		t.Fatalf("CachedTokens = %d, want 10", detail.CachedTokens)
	}
	if detail.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want original uncached input preserved", detail.InputTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_Sonnet(t *testing.T) {
	// uncached=1000, output=500, total=21000 → cached_total=19500
	// known_USD = (1000×3.9 + 500×19.5)/1M = 0.013650
	// remaining_USD = 0.034 - 0.013650 = 0.020350
	// cache_value = 20350
	// CW = (20350 - 0.39×19500) / (4.875 - 0.39) = (20350 - 7605) / 4.485 ≈ 2842.36
	// CR = 19500 - 2842 = 16658
	detail, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  21000,
	}, 0.85, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 2842 {
		t.Fatalf("CacheCreationInputTokens = %d, want 2842", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 16658 {
		t.Fatalf("CacheReadInputTokens = %d, want 16658", detail.CacheReadInputTokens)
	}
	if detail.CachedTokens != 19500 {
		t.Fatalf("CachedTokens = %d, want 19500", detail.CachedTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_Opus(t *testing.T) {
	// Opus prices: in=33, out=165, cw=41.25, cr=3.30
	// uncached=100, output=200, total=10300 → cached_total=10000
	// known_USD = (100×33 + 200×165)/1M = 0.036300
	// Pick credits=2.0 → target_USD = 0.08, remaining_USD = 0.0437, cache_value = 43700
	// CW = (43700 - 3.30×10000) / (41.25 - 3.30) = (43700 - 33000) / 37.95 ≈ 281.95 → 282
	// CR = 10000 - 282 = 9718
	detail, estimated := estimateKiroCacheUsage("claude-opus-4.5", usage.Detail{
		InputTokens:  100,
		OutputTokens: 200,
		TotalTokens:  10300,
	}, 2.0, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 282 {
		t.Fatalf("CacheCreationInputTokens = %d, want 282", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 9718 {
		t.Fatalf("CacheReadInputTokens = %d, want 9718", detail.CacheReadInputTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_Haiku(t *testing.T) {
	// Haiku prices: in=0.4, out=2.0, cw=0.5, cr=0.04
	// uncached=2000, output=1000, total=22000 → cached_total=19000
	// known_USD = (2000×0.4 + 1000×2.0)/1M = 0.0028
	// credits=0.05 → target_USD = 0.002, remaining_USD = -0.0008
	// remaining_USD ≤ 0 with cached_total > 0 → fall to Branch C: CR=cached_total, CW=0
	detail, estimated := estimateKiroCacheUsage("claude-haiku-4.5", usage.Detail{
		InputTokens:  2000,
		OutputTokens: 1000,
		TotalTokens:  22000,
	}, 0.05, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheReadInputTokens != 19000 {
		t.Fatalf("CacheReadInputTokens = %d, want 19000 (Branch C fallback)", detail.CacheReadInputTokens)
	}
	if detail.CacheCreationInputTokens != 0 {
		t.Fatalf("CacheCreationInputTokens = %d, want 0", detail.CacheCreationInputTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_ClampCWHigh(t *testing.T) {
	// Force a scenario where the linear solve yields CW > cached_total.
	// Sonnet, uncached=10, output=10, total=1010 → cached_total=990
	// known_USD = (10×3.9 + 10×19.5)/1M = 0.000234
	// credits=10 → target_USD = 0.4 → remaining_USD ≈ 0.3998 → cache_value ≈ 399766
	// solve CW = (399766 - 0.39×990)/(4.875-0.39) ≈ 89034 → > cached_total, clamp to 990, CR=0.
	detail, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  10,
		OutputTokens: 10,
		TotalTokens:  1010,
	}, 10, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 990 {
		t.Fatalf("CacheCreationInputTokens = %d, want clamp to cached_total 990", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 0 {
		t.Fatalf("CacheReadInputTokens = %d, want 0", detail.CacheReadInputTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_ClampCWLow(t *testing.T) {
	// Force a scenario where the linear solve yields CW < 0.
	// Sonnet, uncached=100, output=100, total=10100 → cached_total=9900
	// known_USD = (100×3.9 + 100×19.5)/1M = 0.00234
	// credits=0.2 → target_USD = 0.008 → remaining_USD ≈ 0.00566 → cache_value ≈ 5660
	// solve CW = (5660 - 0.39×9900)/(4.875-0.39) = (5660 - 3861)/4.485 ≈ 401.1 → positive.
	// Need a smaller cache_value: drop credits so remaining is barely above 0.39×cached_total/1M? No.
	// Use credits=0.15 → target_USD = 0.006 → remaining = 0.00366 → cache_value = 3660
	// solve CW = (3660 - 3861)/4.485 ≈ -44.8 → clamp to 0, CR = 9900.
	detail, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  100,
		OutputTokens: 100,
		TotalTokens:  10100,
	}, 0.15, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 0 {
		t.Fatalf("CacheCreationInputTokens = %d, want clamp to 0", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 9900 {
		t.Fatalf("CacheReadInputTokens = %d, want 9900", detail.CacheReadInputTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchB_NoTotal(t *testing.T) {
	// uncached + credits, no total. Assume CW=0, back-derive CR from credits.
	// Sonnet, uncached=1000, output=500, no total
	// known_USD = (1000×3.9 + 500×19.5)/1M = 0.013650
	// credits=0.5 → target_USD = 0.02 → remaining_USD = 0.00635 → cache_value = 6350
	// CR = 6350 / 0.39 ≈ 16282
	detail, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 0.5, false, true)
	if !estimated {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 0 {
		t.Fatalf("CacheCreationInputTokens = %d, want 0 (Branch B)", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens < 16280 || detail.CacheReadInputTokens > 16285 {
		t.Fatalf("CacheReadInputTokens = %d, want ≈16282", detail.CacheReadInputTokens)
	}
	if detail.TotalTokens != detail.InputTokens+detail.OutputTokens+detail.CachedTokens {
		t.Fatalf("TotalTokens = %d, want backfilled to input+output+cached", detail.TotalTokens)
	}
}

func TestEstimateKiroCacheUsage_OfficialCacheSkipped(t *testing.T) {
	in := usage.Detail{
		InputTokens:              1000,
		OutputTokens:             500,
		TotalTokens:              21000,
		CacheReadInputTokens:     12000,
		CacheCreationInputTokens: 7500,
		CachedTokens:             19500,
	}
	out, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, true, true)
	if estimated {
		t.Fatalf("estimated = true, want false (hasOfficialCache should skip)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

func TestEstimateKiroCacheUsage_NoUncachedSkipped(t *testing.T) {
	in := usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  21000,
	}
	out, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, false, false)
	if estimated {
		t.Fatalf("estimated = true, want false (hasUncached=false should skip)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

func TestEstimateKiroCacheUsage_TotalProvidedNoCacheRoom(t *testing.T) {
	// Upstream total proves there is no cache (total == uncached + output).
	// Even with positive credits, the estimator must not fabricate cache reads.
	in := usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
	}
	out, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, false, true)
	if estimated {
		t.Fatalf("estimated = true, want false (total proves no cache room)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

func TestEstimateKiroCacheUsage_TotalContradictory(t *testing.T) {
	// Upstream total < uncached + output (contradictory). Trust the total
	// rather than letting credits fabricate phantom cache.
	in := usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1200,
	}
	out, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, false, true)
	if estimated {
		t.Fatalf("estimated = true, want false (contradictory total)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

func TestEstimateKiroCacheUsage_NoTotalNoCredits(t *testing.T) {
	in := usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	out, estimated := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0, false, true)
	if estimated {
		t.Fatalf("estimated = true, want false (no signal)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

func TestUsageDetailForInternalStats_StripsEstimated(t *testing.T) {
	in := usage.Detail{
		InputTokens:              1000,
		OutputTokens:             500,
		TotalTokens:              21000,
		CacheReadInputTokens:     16000,
		CacheCreationInputTokens: 3500,
		CachedTokens:             19500,
	}
	stripped := usageDetailForInternalStats(in, true)
	if stripped.InputTokens != 1000+16000+3500 {
		t.Fatalf("InputTokens = %d, want 20500 (uncached + cache_read + cache_write)", stripped.InputTokens)
	}
	if stripped.CacheReadInputTokens != 0 || stripped.CacheCreationInputTokens != 0 || stripped.CachedTokens != 0 {
		t.Fatalf("cache fields not zeroed: %+v", stripped)
	}
	if stripped.OutputTokens != 500 || stripped.TotalTokens != 21000 {
		t.Fatalf("Output/Total mutated unexpectedly: %+v", stripped)
	}
}

func TestUsageDetailForInternalStats_NoOpWhenNotEstimated(t *testing.T) {
	in := usage.Detail{
		InputTokens:              1000,
		OutputTokens:             500,
		TotalTokens:              21000,
		CacheReadInputTokens:     16000,
		CacheCreationInputTokens: 3500,
		CachedTokens:             19500,
	}
	out := usageDetailForInternalStats(in, false)
	if out != in {
		t.Fatalf("detail mutated when estimated=false: got %+v, want %+v", out, in)
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
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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

// End-to-end: tokenUsage gives uncached + total but no cache breakdown, plus a
// meteringEvent reports credit usage. The estimator must split the cached
// portion into both cache_read and cache_creation using the credit equation.
func TestParseEventStream_EstimatesCacheFromCredits(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 500,
				"totalTokens": 21000,
				"uncachedInputTokens": 1000
			}
		}
	}`))
	writeKiroTestEvent(t, &stream, "meteringEvent", []byte(`{
		"meteringEvent": {
			"unit": "credit",
			"usage": 0.85
		}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, estimated, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if !estimated {
		t.Fatalf("estimated = false, want true (credits + total + no official cache)")
	}
	// Math (sonnet @ 1.3 multiplier, 1 credit = $0.04):
	//   target_USD = 0.034
	//   known_USD = (1000×3.9 + 500×19.5)/1M = 0.013650
	//   cache_value = 20350; cached_total = 19500
	//   CW = (20350 - 0.39×19500) / (4.875 - 0.39) ≈ 2842
	//   CR = 19500 - 2842 = 16658
	if usageInfo.InputTokens != 1000 {
		t.Fatalf("InputTokens = %d, want 1000 (uncached preserved)", usageInfo.InputTokens)
	}
	if usageInfo.CacheCreationInputTokens != 2842 {
		t.Fatalf("CacheCreationInputTokens = %d, want 2842", usageInfo.CacheCreationInputTokens)
	}
	if usageInfo.CacheReadInputTokens != 16658 {
		t.Fatalf("CacheReadInputTokens = %d, want 16658", usageInfo.CacheReadInputTokens)
	}
	if usageInfo.CachedTokens != 19500 {
		t.Fatalf("CachedTokens = %d, want 19500", usageInfo.CachedTokens)
	}
}

// Regression: when messageMetadataEvent (with uncachedInputTokens) arrives
// before a usageEvent that carries the *total* inputTokens, the second event
// must not overwrite the uncached value or downstream cache_read inference
// will silently fail.
func TestParseEventStream_UsageEventDoesNotOverwriteUncachedInput(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"totalTokens": 22,
				"uncachedInputTokens": 2,
				"cacheReadInputTokens": 18
			}
		}
	}`))
	writeKiroTestEvent(t, &stream, "usageEvent", []byte(`{
		"inputTokens": 20,
		"outputTokens": 2,
		"totalTokens": 22
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.InputTokens != 2 {
		t.Fatalf("InputTokens = %d, want uncached 2 (usageEvent must not overwrite)", usageInfo.InputTokens)
	}
	if usageInfo.CacheReadInputTokens != 18 {
		t.Fatalf("CacheReadInputTokens = %d, want 18", usageInfo.CacheReadInputTokens)
	}
}

// Regression: when upstream returns uncachedInputTokens + cacheReadInputTokens
// but no totalTokens, the fallback must include CachedTokens so the reported
// total matches the input the request actually consumed.
func TestParseEventStream_TotalTokensFallbackIncludesCache(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"uncachedInputTokens": 5,
				"cacheReadInputTokens": 10,
				"cacheWriteInputTokens": 3
			}
		}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	// In executeWithRetry the TotalTokens fallback adds CachedTokens; replicate
	// that step here since parseEventStream itself leaves TotalTokens at 0 when
	// upstream did not provide totalTokens.
	if usageInfo.TotalTokens == 0 {
		usageInfo.TotalTokens = usageInfo.InputTokens + usageInfo.OutputTokens + usageInfo.CachedTokens
	}
	if usageInfo.TotalTokens != 20 {
		t.Fatalf("TotalTokens = %d, want 5 (uncached) + 2 (output) + 13 (cache) = 20", usageInfo.TotalTokens)
	}
}

// Regression: a supplementaryWebLinksEvent that arrives after
// uncachedInputTokens carries the *total* inputTokens and must not overwrite
// the uncached value.
func TestParseEventStream_SupplementaryWebLinksDoesNotOverwriteUncachedInput(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"totalTokens": 22,
				"uncachedInputTokens": 2,
				"cacheReadInputTokens": 18
			}
		}
	}`))
	writeKiroTestEvent(t, &stream, "supplementaryWebLinksEvent", []byte(`{
		"inputTokens": 20,
		"outputTokens": 2
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.InputTokens != 2 {
		t.Fatalf("InputTokens = %d, want uncached 2 (supplementaryWebLinksEvent must not overwrite)", usageInfo.InputTokens)
	}
}

// Regression: when upstream reports the all-cached scenario
// (uncachedInputTokens=0, cacheReadInputTokens=N), a later legacy-format
// metadata event with `inputTokens` (total) must not overwrite the zero
// uncached value via the `InputTokens == 0` fallback.
func TestParseEventStream_LegacyMetadataDoesNotOverwriteAllCached(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 2,
				"totalTokens": 22,
				"uncachedInputTokens": 0,
				"cacheReadInputTokens": 20
			}
		}
	}`))
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"inputTokens": 20,
			"outputTokens": 2,
			"totalTokens": 22
		}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if usageInfo.InputTokens != 0 {
		t.Fatalf("InputTokens = %d, want 0 (all-cached scenario, legacy fallback must not overwrite)", usageInfo.InputTokens)
	}
	if usageInfo.CacheReadInputTokens != 20 {
		t.Fatalf("CacheReadInputTokens = %d, want 20", usageInfo.CacheReadInputTokens)
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

// End-to-end Branch B (credits, no upstream total): tokenUsage gives
// uncachedInputTokens + outputTokens but no totalTokens, plus a credit
// meteringEvent. Estimator must populate cache_read from credits and
// streamToChannel must surface that to the SSE message_delta. Regresses the
// "TotalTokens backfill before estimator" bug — pre-fix, streamToChannel
// would synthesize TotalTokens=Input+Output before estimation, lying to the
// estimator that total was authoritative with cachedTotal=0 and routing this
// case into the linear-solve path with no cache.
func TestStreamToChannel_EstimatesCacheFromCreditsBranchB(t *testing.T) {
	var stream bytes.Buffer
	writeKiroTestEvent(t, &stream, "assistantResponseEvent", []byte(`{
		"assistantResponseEvent": {"content": "hello"}
	}`))
	writeKiroTestEvent(t, &stream, "messageMetadataEvent", []byte(`{
		"messageMetadataEvent": {
			"tokenUsage": {
				"outputTokens": 500,
				"uncachedInputTokens": 1000
			}
		}
	}`))
	writeKiroTestEvent(t, &stream, "meteringEvent", []byte(`{
		"meteringEvent": {"unit": "credit", "usage": 0.5}
	}`))

	out := make(chan cliproxyexecutor.StreamChunk, 16)
	executor := &KiroExecutor{}
	executor.streamToChannel(
		context.Background(),
		bytes.NewReader(stream.Bytes()),
		out,
		sdktranslator.FromString("claude"),
		"claude-sonnet-4.5",
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
	// Sonnet @1.3, 1 credit=$0.04: target=0.02, known=(1000×3.9+500×19.5)/1M=0.013650,
	// remaining=0.00635, cache_value=6350, CR=6350/0.39≈16282, CW=0.
	if got := gjson.Get(messageDelta, "usage.input_tokens").Int(); got != 1000 {
		t.Fatalf("usage.input_tokens = %d, want uncached 1000", got)
	}
	if got := gjson.Get(messageDelta, "usage.cache_read_input_tokens").Int(); got < 16280 || got > 16285 {
		t.Fatalf("usage.cache_read_input_tokens = %d, want ≈16282", got)
	}
	if got := gjson.Get(messageDelta, "usage.cache_creation_input_tokens").Int(); got != 0 {
		t.Fatalf("usage.cache_creation_input_tokens = %d, want 0 (Branch B)", got)
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
