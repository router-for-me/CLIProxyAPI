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
	_, _, usageInfo, _, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  22,
	}, 0, false, false, true)
	if !(estCR || estCW) {
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
	// Sonnet MSRP: in=3, out=15, cw=3.75, cr=0.30; 1 credit = $0.12.
	// uncached=1000, output=500, total=21000 → cached_total=19500
	// known_USD = (1000×3 + 500×15)/1M = 0.0105
	// target_USD = 0.3 × 0.12 = 0.036
	// remaining_USD = 0.0255 → cache_value = 25500
	// CW = (25500 - 0.30×19500) / (3.75 - 0.30) = (25500 - 5850) / 3.45 ≈ 5695.65
	// CR = 19500 - 5696 = 13804
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  21000,
	}, 0.3, false, false, true)
	if !(estCR || estCW) {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 5696 {
		t.Fatalf("CacheCreationInputTokens = %d, want 5696", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 13804 {
		t.Fatalf("CacheReadInputTokens = %d, want 13804", detail.CacheReadInputTokens)
	}
	if detail.CachedTokens != 19500 {
		t.Fatalf("CachedTokens = %d, want 19500", detail.CachedTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_Opus(t *testing.T) {
	// Opus MSRP: in=15, out=75, cw=18.75, cr=1.50; 1 credit = $0.12.
	// uncached=100, output=200, total=10300 → cached_total=10000
	// known_USD = (100×15 + 200×75)/1M = 0.0165
	// target_USD = 0.35 × 0.12 = 0.042 → remaining_USD = 0.0255 → cache_value = 25500
	// CW = (25500 - 1.50×10000) / (18.75 - 1.50) = (25500 - 15000) / 17.25 ≈ 608.70 → 609
	// CR = 10000 - 609 = 9391
	detail, estCR, estCW := estimateKiroCacheUsage("claude-opus-4.5", usage.Detail{
		InputTokens:  100,
		OutputTokens: 200,
		TotalTokens:  10300,
	}, 0.35, false, false, true)
	if !(estCR || estCW) {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 609 {
		t.Fatalf("CacheCreationInputTokens = %d, want 609", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 9391 {
		t.Fatalf("CacheReadInputTokens = %d, want 9391", detail.CacheReadInputTokens)
	}
}

func TestEstimateKiroCacheUsage_BranchA_Haiku(t *testing.T) {
	// Haiku MSRP: in=1.0, out=5.0, cw=1.25, cr=0.10; 1 credit = $0.12.
	// uncached=2000, output=1000, total=22000 → cached_total=19000
	// known_USD = (2000×1 + 1000×5)/1M = 0.007
	// credits=0.05 → target_USD = 0.006 → remaining_USD = -0.001
	// remaining_USD ≤ 0 with cached_total > 0 → fall to Branch C: CR=cached_total, CW=0
	detail, estCR, estCW := estimateKiroCacheUsage("claude-haiku-4.5", usage.Detail{
		InputTokens:  2000,
		OutputTokens: 1000,
		TotalTokens:  22000,
	}, 0.05, false, false, true)
	if !(estCR || estCW) {
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
	// known_USD = (10×3 + 10×15)/1M = 0.000180
	// credits=10 → target_USD = 1.2 → remaining ≈ 1.19982 → cache_value ≈ 1199820
	// solve CW = (1199820 - 0.30×990)/(3.75-0.30) ≈ 347688 → > cached_total, clamp to 990, CR=0.
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  10,
		OutputTokens: 10,
		TotalTokens:  1010,
	}, 10, false, false, true)
	if !(estCR || estCW) {
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
	// known_USD = (100×3 + 100×15)/1M = 0.0018
	// credits=0.03 → target_USD = 0.0036 → remaining = 0.0018 → cache_value = 1800
	// solve CW = (1800 - 0.30×9900)/(3.75-0.30) = (1800 - 2970)/3.45 ≈ -339 → clamp to 0, CR = 9900.
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  100,
		OutputTokens: 100,
		TotalTokens:  10100,
	}, 0.03, false, false, true)
	if !(estCR || estCW) {
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
	// known_USD = (1000×3 + 500×15)/1M = 0.0105
	// credits=0.10 → target_USD = 0.012 → remaining = 0.0015 → cache_value = 1500
	// CR = 1500 / 0.30 = 5000
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 0.10, false, false, true)
	if !(estCR || estCW) {
		t.Fatalf("estimated = false, want true")
	}
	if detail.CacheCreationInputTokens != 0 {
		t.Fatalf("CacheCreationInputTokens = %d, want 0 (Branch B)", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 5000 {
		t.Fatalf("CacheReadInputTokens = %d, want 5000", detail.CacheReadInputTokens)
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
	out, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, true, true, true)
	if estCR || estCW {
		t.Fatalf("estimated = true, want false (both cache fields official should skip)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

// Kiro reports cacheReadInputTokens but never cacheWriteInputTokens. The
// estimator must keep the official CR and back-derive CW from credits.
func TestEstimateKiroCacheUsage_OnlyCROfficial_SolveCW(t *testing.T) {
	// Sonnet, uncached=0, output=247, official CR=9666, no total
	// known_USD = (0×3 + 247×15 + 9666×0.30)/1M = 0.0066048
	// credits=0.2595 → target = 0.03114 → remaining = 0.024535 → cache_value = 24535
	// CW = 24535 / 3.75 ≈ 6542.7 → 6543
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:          0,
		OutputTokens:         247,
		CacheReadInputTokens: 9666,
		CachedTokens:         9666,
	}, 0.2595, true, false, true)
	if estCR {
		t.Fatalf("estCR = true, want false (CR was officially reported, must not be flagged as estimated)")
	}
	if !estCW {
		t.Fatalf("estCW = false, want true (CW must be derived from credits)")
	}
	if detail.CacheReadInputTokens != 9666 {
		t.Fatalf("CacheReadInputTokens = %d, want 9666 (official preserved)", detail.CacheReadInputTokens)
	}
	if detail.CacheCreationInputTokens != 6543 {
		t.Fatalf("CacheCreationInputTokens = %d, want 6543", detail.CacheCreationInputTokens)
	}
	if detail.CachedTokens != 9666+6543 {
		t.Fatalf("CachedTokens = %d, want %d", detail.CachedTokens, 9666+6543)
	}
}

// When CR is officially given but the credit charge implies no extra cache
// (or the pricing is off), preserve the official CR untouched and skip CW.
func TestEstimateKiroCacheUsage_OnlyCROfficial_RemainingNonPositive(t *testing.T) {
	in := usage.Detail{
		InputTokens:          5000,
		OutputTokens:         500,
		CacheReadInputTokens: 1000,
		CachedTokens:         1000,
	}
	// known_USD = (5000×3 + 500×15 + 1000×0.30)/1M = 0.022800
	// credits=0.10 → target = 0.012 → remaining = -0.0108 → preserve as-is.
	out, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.10, true, false, true)
	if estCR || estCW {
		t.Fatalf("estimated = true, want false (no positive room for CW)")
	}
	if out != in {
		t.Fatalf("detail mutated: got %+v, want %+v", out, in)
	}
}

// Symmetric branch: CW officially given but CR unknown. Kiro doesn't actually
// produce this shape today, but the estimator supports it for completeness.
func TestEstimateKiroCacheUsage_OnlyCWOfficial_SolveCR(t *testing.T) {
	// Sonnet, uncached=0, output=247, official CW=1000, no total
	// known_USD = (0×3 + 247×15 + 1000×3.75)/1M = 0.007455
	// credits=0.20 → target = 0.024 → remaining = 0.016545 → cache_value = 16545
	// CR = 16545 / 0.30 = 55150
	detail, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", usage.Detail{
		InputTokens:              0,
		OutputTokens:             247,
		CacheCreationInputTokens: 1000,
		CachedTokens:             1000,
	}, 0.20, false, true, true)
	if !estCR {
		t.Fatalf("estCR = false, want true (CR must be derived)")
	}
	if estCW {
		t.Fatalf("estCW = true, want false (CW was officially reported)")
	}
	if detail.CacheCreationInputTokens != 1000 {
		t.Fatalf("CacheCreationInputTokens = %d, want 1000 (official preserved)", detail.CacheCreationInputTokens)
	}
	if detail.CacheReadInputTokens != 55150 {
		t.Fatalf("CacheReadInputTokens = %d, want 55150", detail.CacheReadInputTokens)
	}
}

func TestEstimateKiroCacheUsage_NoUncachedSkipped(t *testing.T) {
	in := usage.Detail{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  21000,
	}
	out, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, false, false, false)
	if estCR || estCW {
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
	out, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, false, false, true)
	if estCR || estCW {
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
	out, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0.85, false, false, true)
	if estCR || estCW {
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
	out, estCR, estCW := estimateKiroCacheUsage("claude-sonnet-4.5", in, 0, false, false, true)
	if estCR || estCW {
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
	stripped := usageDetailForInternalStats(in, true, true)
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
	out := usageDetailForInternalStats(in, false, false)
	if out != in {
		t.Fatalf("detail mutated when estimated=false: got %+v, want %+v", out, in)
	}
}

// Only-CW-estimated case: an officially reported CR must NOT be folded back
// into InputTokens — only the estimated CW is stripped.
func TestUsageDetailForInternalStats_PreservesOfficialCacheRead(t *testing.T) {
	in := usage.Detail{
		InputTokens:              1000,
		OutputTokens:             500,
		TotalTokens:              21000,
		CacheReadInputTokens:     16000, // official
		CacheCreationInputTokens: 3500,  // estimated
		CachedTokens:             19500,
	}
	out := usageDetailForInternalStats(in, false, true)
	if out.InputTokens != 1000+3500 {
		t.Fatalf("InputTokens = %d, want 4500 (uncached + estimated CW only)", out.InputTokens)
	}
	if out.CacheReadInputTokens != 16000 {
		t.Fatalf("CacheReadInputTokens = %d, want 16000 (official preserved)", out.CacheReadInputTokens)
	}
	if out.CacheCreationInputTokens != 0 {
		t.Fatalf("CacheCreationInputTokens = %d, want 0 (estimated stripped)", out.CacheCreationInputTokens)
	}
	if out.CachedTokens != 16000 {
		t.Fatalf("CachedTokens = %d, want 16000 (only CR remains)", out.CachedTokens)
	}
}

// Symmetric: only CR estimated, official CW preserved.
func TestUsageDetailForInternalStats_PreservesOfficialCacheWrite(t *testing.T) {
	in := usage.Detail{
		InputTokens:              1000,
		OutputTokens:             500,
		CacheReadInputTokens:     16000, // estimated
		CacheCreationInputTokens: 3500,  // official
		CachedTokens:             19500,
	}
	out := usageDetailForInternalStats(in, true, false)
	if out.InputTokens != 1000+16000 {
		t.Fatalf("InputTokens = %d, want 17000 (uncached + estimated CR only)", out.InputTokens)
	}
	if out.CacheReadInputTokens != 0 {
		t.Fatalf("CacheReadInputTokens = %d, want 0 (estimated stripped)", out.CacheReadInputTokens)
	}
	if out.CacheCreationInputTokens != 3500 {
		t.Fatalf("CacheCreationInputTokens = %d, want 3500 (official preserved)", out.CacheCreationInputTokens)
	}
	if out.CachedTokens != 3500 {
		t.Fatalf("CachedTokens = %d, want 3500 (only CW remains)", out.CachedTokens)
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
	_, _, usageInfo, _, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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
			"usage": 0.3
		}
	}`))

	executor := &KiroExecutor{}
	_, _, usageInfo, _, estCR, estCW, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	if !estCR || !estCW {
		t.Fatalf("estimatedCR=%t estimatedCW=%t, want both true (credits + total + no official cache)", estCR, estCW)
	}
	// Math (sonnet MSRP, 1 credit = $0.12):
	//   target_USD = 0.036
	//   known_USD = (1000×3 + 500×15)/1M = 0.0105
	//   cache_value = 25500; cached_total = 19500
	//   CW = (25500 - 0.30×19500) / (3.75 - 0.30) ≈ 5696
	//   CR = 19500 - 5696 = 13804
	if usageInfo.InputTokens != 1000 {
		t.Fatalf("InputTokens = %d, want 1000 (uncached preserved)", usageInfo.InputTokens)
	}
	if usageInfo.CacheCreationInputTokens != 5696 {
		t.Fatalf("CacheCreationInputTokens = %d, want 5696", usageInfo.CacheCreationInputTokens)
	}
	if usageInfo.CacheReadInputTokens != 13804 {
		t.Fatalf("CacheReadInputTokens = %d, want 13804", usageInfo.CacheReadInputTokens)
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
	_, _, usageInfo, _, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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
	_, _, usageInfo, _, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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
	_, _, usageInfo, _, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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
	_, _, usageInfo, _, _, _, err := executor.parseEventStream(bytes.NewReader(stream.Bytes()), "claude-sonnet-4.5")
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
		"meteringEvent": {"unit": "credit", "usage": 0.10}
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
	// Sonnet MSRP, 1 credit=$0.12: target=0.012, known=(1000×3+500×15)/1M=0.0105,
	// remaining=0.0015, cache_value=1500, CR=1500/0.30=5000, CW=0.
	if got := gjson.Get(messageDelta, "usage.input_tokens").Int(); got != 1000 {
		t.Fatalf("usage.input_tokens = %d, want uncached 1000", got)
	}
	if got := gjson.Get(messageDelta, "usage.cache_read_input_tokens").Int(); got != 5000 {
		t.Fatalf("usage.cache_read_input_tokens = %d, want 5000", got)
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
