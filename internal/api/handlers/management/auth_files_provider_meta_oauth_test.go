package management

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildMetaAuthRecord_PreservesSubscriptionMetadata_Issue6117(t *testing.T) {
	bundle := &metaauth.MetaAuthBundle{
		Email: "user@example.com",
		Name:  "Test User",
		TokenData: &metaauth.TokenData{
			AccessToken: "dca:access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
		MintedKey: &metaauth.MintedKeyResponse{
			APIKey:           "LLM|minted-key",
			BaseURL:          "https://api.meta.ai/v1",
			UserEmail:        "user@example.com",
			UserFullName:     "Test User",
			SubsTierName:     "High Usage",
			SubsTierID:       "high_usage",
			IsSubsActive:     true,
			HasPaymentMethod: true,
		},
	}
	tokenStorage := metaauth.NewMetaAuth(nil).CreateTokenStorage(bundle)
	if tokenStorage == nil {
		t.Fatal("CreateTokenStorage returned nil")
	}

	record := buildMetaAuthRecord(bundle, tokenStorage)
	if record == nil {
		t.Fatal("buildMetaAuthRecord returned nil")
	}

	expectedMetadata := map[string]any{
		"subs_tier_name":     "High Usage",
		"subs_tier_id":       "high_usage",
		"is_subs_active":     true,
		"has_payment_method": true,
	}
	for key, want := range expectedMetadata {
		got, ok := record.Metadata[key]
		if !ok || got != want {
			t.Errorf("record.Metadata[%q] = %v (present=%t), want %v", key, got, ok, want)
		}
	}
}

func TestBuildMetaAuthRecord_SavesToDiskWithSubscriptionMetadata_Issue6117(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandler(&config.Config{AuthDir: authDir}, "", nil)

	bundle := &metaauth.MetaAuthBundle{
		Email: "user@example.com",
		Name:  "Test User",
		TokenData: &metaauth.TokenData{
			AccessToken: "dca:access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
		MintedKey: &metaauth.MintedKeyResponse{
			APIKey:           "LLM|minted-key",
			BaseURL:          "https://api.meta.ai/v1",
			UserEmail:        "user@example.com",
			UserFullName:     "Test User",
			SubsTierName:     "High Usage",
			SubsTierID:       "high_usage",
			IsSubsActive:     true,
			HasPaymentMethod: true,
		},
	}
	tokenStorage := metaauth.NewMetaAuth(nil).CreateTokenStorage(bundle)
	record := buildMetaAuthRecord(bundle, tokenStorage)

	savedPath, errSave := h.saveTokenRecord(context.Background(), record)
	if errSave != nil {
		t.Fatalf("saveTokenRecord error: %v", errSave)
	}

	data, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatalf("failed to read saved auth file: %v", errRead)
	}

	var parsed map[string]any
	if errJSON := json.Unmarshal(data, &parsed); errJSON != nil {
		t.Fatalf("failed to parse saved auth file: %v", errJSON)
	}

	for _, key := range []string{"subs_tier_name", "subs_tier_id", "is_subs_active", "has_payment_method"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("saved auth file missing expected metadata key %q", key)
		}
	}
}
