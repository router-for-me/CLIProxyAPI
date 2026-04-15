package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeOAuthQuotaGroups_InjectsDefaults(t *testing.T) {
	groups := NormalizeOAuthQuotaGroups(nil)
	if len(groups) != 3 {
		t.Fatalf("expected 3 default groups, got %d", len(groups))
	}
	if groups[0].ID != OAuthQuotaGroupClaude45 {
		t.Fatalf("expected highest priority group to be %q, got %q", OAuthQuotaGroupClaude45, groups[0].ID)
	}
}

func TestUpsertAndRemoveOAuthAccountQuotaGroupState(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	entries := UpsertOAuthAccountQuotaGroupState(nil, OAuthAccountQuotaGroupState{
		AuthID:             "auth-1",
		GroupID:            "Google-Claude",
		ManualSuspended:    true,
		ManualReason:       "maintenance",
		AutoSuspendedUntil: now,
	})
	if len(entries) != 1 {
		t.Fatalf("expected 1 state entry, got %d", len(entries))
	}
	if entries[0].GroupID != OAuthQuotaGroupClaude45 {
		t.Fatalf("expected normalized group id %q, got %q", OAuthQuotaGroupClaude45, entries[0].GroupID)
	}

	entries = RemoveOAuthAccountQuotaGroupState(entries, "auth-1", OAuthQuotaGroupClaude45)
	if len(entries) != 0 {
		t.Fatalf("expected state entry removal, got %d entries", len(entries))
	}
}

func TestNormalizeOAuthAccountQuotaGroupState_MigratesLegacyGeminiImageGroup(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 30, 0, 0, time.UTC)
	entries := NormalizeOAuthAccountQuotaGroupState([]OAuthAccountQuotaGroupState{
		{
			AuthID:             "auth-1",
			GroupID:            "google-gemini-image",
			ManualSuspended:    true,
			AutoSuspendedUntil: now,
		},
	})
	if len(entries) != 2 {
		t.Fatalf("expected legacy image group to expand into 2 groups, got %d", len(entries))
	}
	if entries[0].GroupID != OAuthQuotaGroupG3Flash {
		t.Fatalf("expected first migrated group to be %q, got %q", OAuthQuotaGroupG3Flash, entries[0].GroupID)
	}
	if entries[1].GroupID != OAuthQuotaGroupG3Pro {
		t.Fatalf("expected second migrated group to be %q, got %q", OAuthQuotaGroupG3Pro, entries[1].GroupID)
	}
}

func TestNormalizeOAuthQuotaGroups_DefaultsEnabledWhenOmitted(t *testing.T) {
	var entries []OAuthQuotaGroup
	if err := json.Unmarshal([]byte(`[
		{
			"id": "custom-group",
			"label": "Custom Group",
			"providers": ["antigravity"],
			"patterns": ["custom-*"],
			"priority": 50
		}
	]`), &entries); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	groups := NormalizeOAuthQuotaGroups(entries)
	found := false
	for _, group := range groups {
		if group.ID != "custom-group" {
			continue
		}
		found = true
		if !group.Enabled {
			t.Fatal("custom group Enabled = false, want true when field is omitted")
		}
	}
	if !found {
		t.Fatal("custom group missing after normalization")
	}
}

func TestNormalizeOAuthQuotaGroups_PreservesExplicitDisabledFlag(t *testing.T) {
	var entries []OAuthQuotaGroup
	if err := json.Unmarshal([]byte(`[
		{
			"id": "custom-group",
			"label": "Custom Group",
			"providers": ["antigravity"],
			"patterns": ["custom-*"],
			"priority": 50,
			"enabled": false
		}
	]`), &entries); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	groups := NormalizeOAuthQuotaGroups(entries)
	found := false
	for _, group := range groups {
		if group.ID != "custom-group" {
			continue
		}
		found = true
		if group.Enabled {
			t.Fatal("custom group Enabled = true, want explicit false to be preserved")
		}
	}
	if !found {
		t.Fatal("custom group missing after normalization")
	}
}

func TestNormalizeOAuthQuotaGroups_DropsLegacyGeminiImageDefinition(t *testing.T) {
	var entries []OAuthQuotaGroup
	if err := json.Unmarshal([]byte(`[
		{
			"id": "google-gemini-image",
			"label": "Google Gemini Image",
			"providers": ["antigravity"],
			"patterns": ["*image*"],
			"priority": 400,
			"enabled": true
		}
	]`), &entries); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	groups := NormalizeOAuthQuotaGroups(entries)
	for _, group := range groups {
		if group.ID == "google-gemini-image" {
			t.Fatal("legacy google-gemini-image definition should be dropped during normalization")
		}
	}
}
