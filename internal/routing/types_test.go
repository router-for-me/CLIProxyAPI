package routing

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultRoutingConfig(t *testing.T) {
	cfg := DefaultRoutingConfig()

	if cfg.Version != 1 {
		t.Errorf("DefaultRoutingConfig().Version = %d, want 1", cfg.Version)
	}

	if cfg.ActiveProfileID != nil {
		t.Error("DefaultRoutingConfig().ActiveProfileID should be nil")
	}

	if len(cfg.Profiles) != 0 {
		t.Errorf("DefaultRoutingConfig().Profiles should be empty, got %d", len(cfg.Profiles))
	}

	if len(cfg.ModelFamilies.Chat) == 0 {
		t.Error("DefaultRoutingConfig().ModelFamilies.Chat should have defaults")
	}
}

func TestRoutingConfig_GetActiveProfile(t *testing.T) {
	profileID := "test-profile-id"
	cfg := &RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
		Profiles: []Profile{
			{ID: "other-id", Name: "Other"},
			{ID: profileID, Name: "Test Profile"},
		},
	}

	profile := cfg.GetActiveProfile()
	if profile == nil {
		t.Fatal("GetActiveProfile() returned nil")
	}
	if profile.ID != profileID {
		t.Errorf("GetActiveProfile().ID = %q, want %q", profile.ID, profileID)
	}

	cfg.ActiveProfileID = nil
	if cfg.GetActiveProfile() != nil {
		t.Error("GetActiveProfile() should return nil when no active profile")
	}

	var nilCfg *RoutingConfig
	if nilCfg.GetActiveProfile() != nil {
		t.Error("nil config GetActiveProfile() should return nil")
	}
}

func TestRoutingConfig_GetProviderGroup(t *testing.T) {
	groupID := "test-group-id"
	cfg := &RoutingConfig{
		ProviderGroups: []ProviderGroup{
			{ID: "other-id", Name: "Other"},
			{ID: groupID, Name: "Test Group"},
		},
	}

	group := cfg.GetProviderGroup(groupID)
	if group == nil {
		t.Fatal("GetProviderGroup() returned nil")
	}
	if group.ID != groupID {
		t.Errorf("GetProviderGroup().ID = %q, want %q", group.ID, groupID)
	}

	if cfg.GetProviderGroup("nonexistent") != nil {
		t.Error("GetProviderGroup() should return nil for unknown ID")
	}

	if cfg.GetProviderGroup("") != nil {
		t.Error("GetProviderGroup() should return nil for empty ID")
	}
}

func TestRoutingConfig_ResolveProviderGroup(t *testing.T) {
	chatGroupID := "chat-group"
	defaultGroupID := "default-group"
	profileID := "profile-id"

	cfg := &RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
		Profiles: []Profile{
			{
				ID:   profileID,
				Name: "Test Profile",
				RoutingRules: RoutingRules{
					Chat:       &chatGroupID,
					Completion: nil,
					Embedding:  nil,
					Other:      nil,
				},
				DefaultProviderGroup: &defaultGroupID,
			},
		},
		ProviderGroups: []ProviderGroup{
			{ID: chatGroupID, Name: "Chat Group"},
			{ID: defaultGroupID, Name: "Default Group"},
		},
	}

	chatGroup := cfg.ResolveProviderGroup(RequestTypeChat)
	if chatGroup == nil || chatGroup.ID != chatGroupID {
		t.Errorf("ResolveProviderGroup(chat) = %v, want group with ID %q", chatGroup, chatGroupID)
	}

	completionGroup := cfg.ResolveProviderGroup(RequestTypeCompletion)
	if completionGroup == nil || completionGroup.ID != defaultGroupID {
		t.Errorf("ResolveProviderGroup(completion) should fall back to default group")
	}
}

func TestRoutingRules_Get(t *testing.T) {
	chatID := "chat-id"
	completionID := "completion-id"

	rules := &RoutingRules{
		Chat:       &chatID,
		Completion: &completionID,
		Embedding:  nil,
		Other:      nil,
	}

	if got := rules.Get(RequestTypeChat); got == nil || *got != chatID {
		t.Errorf("RoutingRules.Get(chat) = %v, want %q", got, chatID)
	}

	if got := rules.Get(RequestTypeCompletion); got == nil || *got != completionID {
		t.Errorf("RoutingRules.Get(completion) = %v, want %q", got, completionID)
	}

	if rules.Get(RequestTypeEmbedding) != nil {
		t.Error("RoutingRules.Get(embedding) should return nil")
	}

	var nilRules *RoutingRules
	if nilRules.Get(RequestTypeChat) != nil {
		t.Error("nil RoutingRules.Get() should return nil")
	}
}

func TestSelectionStrategy_IsValid(t *testing.T) {
	tests := []struct {
		ss    SelectionStrategy
		valid bool
	}{
		{SelectionRoundRobin, true},
		{SelectionRandom, true},
		{SelectionPriority, true},
		{SelectionStrategy("invalid"), false},
		{SelectionStrategy(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.ss), func(t *testing.T) {
			if got := tt.ss.IsValid(); got != tt.valid {
				t.Errorf("SelectionStrategy(%q).IsValid() = %v, want %v", tt.ss, got, tt.valid)
			}
		})
	}
}

func TestProfile_JSONMarshal(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	profile := Profile{
		ID:        "test-id",
		Name:      "Test Profile",
		Color:     "#FF5733",
		CreatedAt: now,
		UpdatedAt: now,
	}

	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded Profile
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.ID != profile.ID {
		t.Errorf("decoded.ID = %q, want %q", decoded.ID, profile.ID)
	}
	if !decoded.CreatedAt.Equal(profile.CreatedAt) {
		t.Errorf("decoded.CreatedAt = %v, want %v", decoded.CreatedAt, profile.CreatedAt)
	}
}

func TestRoutingConfig_Validate(t *testing.T) {
	cfg := &RoutingConfig{Version: 0}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Validate() should set Version to 1, got %d", cfg.Version)
	}

	var nilCfg *RoutingConfig
	if err := nilCfg.Validate(); err != nil {
		t.Errorf("nil Validate() error = %v", err)
	}
}
