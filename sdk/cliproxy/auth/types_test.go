package auth

import (
	"strings"
	"testing"
	"time"
)

func TestToolPrefixDisabled(t *testing.T) {
	var a *Auth
	if a.ToolPrefixDisabled() {
		t.Error("nil auth should return false")
	}

	a = &Auth{}
	if a.ToolPrefixDisabled() {
		t.Error("empty auth should return false")
	}

	a = &Auth{Metadata: map[string]any{"tool_prefix_disabled": true}}
	if !a.ToolPrefixDisabled() {
		t.Error("should return true when set to true")
	}

	a = &Auth{Metadata: map[string]any{"tool_prefix_disabled": "true"}}
	if !a.ToolPrefixDisabled() {
		t.Error("should return true when set to string 'true'")
	}

	a = &Auth{Metadata: map[string]any{"tool-prefix-disabled": true}}
	if !a.ToolPrefixDisabled() {
		t.Error("should return true with kebab-case key")
	}

	a = &Auth{Metadata: map[string]any{"tool_prefix_disabled": false}}
	if a.ToolPrefixDisabled() {
		t.Error("should return false when set to false")
	}
}

func TestEnsureIndexUsesCredentialIdentity(t *testing.T) {
	t.Parallel()

	geminiAuth := &Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
			"source":  "config:gemini[abc123]",
		},
	}
	compatAuth := &Auth{
		Provider: "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
			"source":       "config:bohe[def456]",
		},
	}
	geminiAltBase := &Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "shared-key",
			"base_url": "https://alt.example.com",
			"source":   "config:gemini[ghi789]",
		},
	}
	geminiDuplicate := &Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
			"source":  "config:gemini[abc123-1]",
		},
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	altBaseIndex := geminiAltBase.EnsureIndex()
	duplicateIndex := geminiDuplicate.EnsureIndex()

	if geminiIndex == "" {
		t.Fatal("gemini index should not be empty")
	}
	if compatIndex == "" {
		t.Fatal("compat index should not be empty")
	}
	if altBaseIndex == "" {
		t.Fatal("alt base index should not be empty")
	}
	if duplicateIndex == "" {
		t.Fatal("duplicate index should not be empty")
	}
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}
	if geminiIndex == altBaseIndex {
		t.Fatalf("same provider/key with different base_url produced duplicate auth_index %q", geminiIndex)
	}
	if geminiIndex == duplicateIndex {
		t.Fatalf("duplicate config entries should be separated by source-derived seed, got %q", geminiIndex)
	}
}

func TestRecentRequestsSnapshotEmptyReturnsTwentyBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).In(time.Local)
	a := &Auth{}

	got := a.RecentRequestsSnapshot(now)
	if len(got) != recentRequestBucketCount {
		t.Fatalf("len = %d, want %d", len(got), recentRequestBucketCount)
	}

	currentBucketID := now.Unix() / recentRequestBucketSeconds
	baseBucketID := currentBucketID - int64(recentRequestBucketCount-1)
	for i, bucket := range got {
		if bucket.Success != 0 || bucket.Failed != 0 {
			t.Fatalf("bucket[%d] counts = %d/%d, want 0/0", i, bucket.Success, bucket.Failed)
		}
		if strings.TrimSpace(bucket.Time) == "" {
			t.Fatalf("bucket[%d] time label is empty", i)
		}
		expectedBucketID := baseBucketID + int64(i)
		start := time.Unix(expectedBucketID*recentRequestBucketSeconds, 0).In(time.Local)
		end := start.Add(10 * time.Minute)
		expected := start.Format("15:04") + "-" + end.Format("15:04")
		if bucket.Time != expected {
			t.Fatalf("bucket[%d] time = %q, want %q", i, bucket.Time, expected)
		}
	}
}

func TestRecentRequestsSnapshotIncludesCounts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).In(time.Local)
	a := &Auth{}

	a.recordRecentRequest(now, true)
	a.recordRecentRequest(now, false)

	got := a.RecentRequestsSnapshot(now)
	if len(got) != recentRequestBucketCount {
		t.Fatalf("len = %d, want %d", len(got), recentRequestBucketCount)
	}

	newest := got[len(got)-1]
	if newest.Success != 1 || newest.Failed != 1 {
		t.Fatalf("newest bucket = success=%d failed=%d, want 1/1", newest.Success, newest.Failed)
	}
}

func TestRecentRequestsSnapshotBucketAdvanceMovesCounts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).In(time.Local)
	next := now.Add(10 * time.Minute)
	a := &Auth{}

	a.recordRecentRequest(now, true)
	a.recordRecentRequest(next, false)

	got := a.RecentRequestsSnapshot(next)
	if len(got) != recentRequestBucketCount {
		t.Fatalf("len = %d, want %d", len(got), recentRequestBucketCount)
	}

	secondNewest := got[len(got)-2]
	newest := got[len(got)-1]
	if secondNewest.Success != 1 || secondNewest.Failed != 0 {
		t.Fatalf("second newest bucket = success=%d failed=%d, want 1/0", secondNewest.Success, secondNewest.Failed)
	}
	if newest.Success != 0 || newest.Failed != 1 {
		t.Fatalf("newest bucket = success=%d failed=%d, want 0/1", newest.Success, newest.Failed)
	}
}
