package usage

import (
	"testing"
)

func TestParseAntigravityQuotaSnapshot_Empty(t *testing.T) {
	if snap := ParseAntigravityQuotaSnapshot(nil); snap != nil {
		t.Fatalf("expected nil snapshot, got %#v", snap)
	}
	if snap := ParseAntigravityQuotaSnapshot([]byte(`{}`)); snap != nil {
		t.Fatalf("expected nil snapshot, got %#v", snap)
	}
}

func TestParseAntigravityQuotaSnapshot_ParsesRelevantModels(t *testing.T) {
	body := []byte(`{
		"models": {
			"gemini-3-pro-high": {"quotaInfo": {"remainingFraction": 0.73, "resetTime": "2025-01-01T00:00:00Z"}},
			"claude-sonnet-4-5": {"quotaInfo": {"remainingFraction": 0.12, "resetTime": "2025-01-02T00:00:00Z"}},
			"other-model": {"quotaInfo": {"remainingFraction": 0.99, "resetTime": "2025-01-03T00:00:00Z"}}
		}
	}`)

	snap := ParseAntigravityQuotaSnapshot(body)
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if len(snap.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %#v", len(snap.Models), snap.Models)
	}
	if snap.Models[0].Name != "claude-sonnet-4-5" {
		t.Fatalf("expected sorted models, got %#v", snap.Models)
	}
	if snap.Models[1].Name != "gemini-3-pro-high" {
		t.Fatalf("expected sorted models, got %#v", snap.Models)
	}
	if snap.Models[0].RemainingPercent == nil || *snap.Models[0].RemainingPercent != 12 {
		t.Fatalf("expected claude remaining=12, got %#v", snap.Models[0].RemainingPercent)
	}
	if snap.Models[1].RemainingPercent == nil || *snap.Models[1].RemainingPercent != 73 {
		t.Fatalf("expected gemini remaining=73, got %#v", snap.Models[1].RemainingPercent)
	}
}

func TestAntigravityQuotaSnapshotStore_RoundTrip(t *testing.T) {
	UpdateAntigravityQuotaSnapshot("auth-1", &AntigravityQuotaSnapshot{
		Models: []AntigravityModelQuota{{Name: "gemini-3-pro-high"}},
	})
	snap := GetAntigravityQuotaSnapshot("auth-1")
	if snap == nil {
		t.Fatal("expected snapshot")
	}
	if len(snap.Models) != 1 || snap.Models[0].Name != "gemini-3-pro-high" {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
}

