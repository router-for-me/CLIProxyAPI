package util

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestGetProviderNameAndModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()

	// Setup registry with models
	modelID := "real-model"
	aliasID := "alias-to-real"

	reg.RegisterClient("client1", "claude", []*registry.ModelInfo{
		{ID: modelID},
	})
	reg.RegisterClient("client2", "gemini", []*registry.ModelInfo{
		{ID: aliasID},
	})
	defer reg.UnregisterClient("client1")
	defer reg.UnregisterClient("client2")

	// Setup aliases
	aliases := map[string][]string{
		aliasID: {modelID},
	}
	SetAliases(aliases)
	defer SetAliases(nil)

	tests := []struct {
		name             string
		input            string
		wantProviders    []string
		wantResolvedName string
	}{
		{
			name:             "Union of direct and aliased providers",
			input:            aliasID,
			wantProviders:    []string{"claude", "gemini"}, // Order might vary but both should be there
			wantResolvedName: aliasID,
		},
		{
			name:             "Direct model only",
			input:            modelID,
			wantProviders:    []string{"claude"},
			wantResolvedName: modelID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, resolvedName := GetProviderNameAndModel(tt.input)
			if len(providers) != len(tt.wantProviders) {
				t.Errorf("GetProviderNameAndModel() got %d providers, want %d", len(providers), len(tt.wantProviders))
			}
			for _, want := range tt.wantProviders {
				found := false
				for _, got := range providers {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("GetProviderNameAndModel() missing provider %s", want)
				}
			}
			if resolvedName != tt.wantResolvedName {
				t.Errorf("GetProviderNameAndModel() resolvedName = %v, want %v", resolvedName, tt.wantResolvedName)
			}
		})
	}
}
