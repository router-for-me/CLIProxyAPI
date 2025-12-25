package registry

import (
	"sync"
	"testing"
)

func TestModelRegistry_Aliases(t *testing.T) {
	r := &ModelRegistry{
		models:           make(map[string]*ModelRegistration),
		clientModels:     make(map[string][]string),
		clientModelInfos: make(map[string]map[string]*ModelInfo),
		clientProviders:  make(map[string]string),
		aliases:          make(map[string][]string),
		mutex:            &sync.RWMutex{},
	}

	// 1. Setup: Register a client with a real model
	realModelID := "gpt-4-real"
	r.RegisterClient("client1", "openai", []*ModelInfo{
		{ID: realModelID, OwnedBy: "openai"},
	})

	// 2. Setup: Register another client with a model that will be an alias target
	targetModelID := "claude-3-target"
	r.RegisterClient("client2", "anthropic", []*ModelInfo{
		{ID: targetModelID, OwnedBy: "anthropic"},
	})

	// 3. Setup: Define aliases
	aliases := map[string][]string{
		"gpt-4-alias":   {realModelID},   // Alias to a model that exists
		"claude-alias":  {targetModelID}, // Alias to another model that exists
		"missing-alias": {"non-existent"},
		"multi-alias":   {realModelID, targetModelID}, // Alias to multiple models
	}
	r.SetAliases(aliases)

	// Test GetModelProviders (Union behavior)
	t.Run("GetModelProviders_Union", func(t *testing.T) {
		// Direct model
		providers := r.GetModelProviders(realModelID)
		if len(providers) != 1 || providers[0] != "openai" {
			t.Errorf("Expected [openai], got %v", providers)
		}

		// Alias model
		providers = r.GetModelProviders("gpt-4-alias")
		if len(providers) != 1 || providers[0] != "openai" {
			t.Errorf("Expected [openai] for alias, got %v", providers)
		}

		// Multi alias
		providers = r.GetModelProviders("multi-alias")
		if len(providers) != 2 {
			t.Errorf("Expected 2 providers for multi-alias, got %v", providers)
		}
	})

	// Test GetModelCount (Union behavior)
	t.Run("GetModelCount_Union", func(t *testing.T) {
		count := r.GetModelCount(realModelID)
		if count != 1 {
			t.Errorf("Expected count 1 for real model, got %d", count)
		}

		count = r.GetModelCount("gpt-4-alias")
		if count != 1 {
			t.Errorf("Expected count 1 for alias, got %d", count)
		}

		count = r.GetModelCount("multi-alias")
		if count != 2 {
			t.Errorf("Expected count 2 for multi-alias, got %d", count)
		}
	})

	// Test ClientSupportsModel
	t.Run("ClientSupportsModel", func(t *testing.T) {
		if !r.ClientSupportsModel("client1", realModelID) {
			t.Error("client1 should support real model")
		}
		if !r.ClientSupportsModel("client1", "gpt-4-alias") {
			t.Error("client1 should support alias model")
		}
		if !r.ClientSupportsModel("client1", "multi-alias") {
			t.Error("client1 should support multi-alias")
		}
		if !r.ClientSupportsModel("client2", "multi-alias") {
			t.Error("client2 should support multi-alias")
		}
		if r.ClientSupportsModel("client2", realModelID) {
			t.Error("client2 should NOT support real model")
		}
	})

	// Test ResolveModelForClient
	t.Run("ResolveModelForClient", func(t *testing.T) {
		// Direct match
		resolved := r.ResolveModelForClient("client1", realModelID)
		if resolved != realModelID {
			t.Errorf("Expected %s, got %s", realModelID, resolved)
		}

		// Alias match
		resolved = r.ResolveModelForClient("client1", "gpt-4-alias")
		if resolved != realModelID {
			t.Errorf("Expected %s for alias, got %s", realModelID, resolved)
		}

		// Multi alias match (client1 supports realModelID)
		resolved = r.ResolveModelForClient("client1", "multi-alias")
		if resolved != realModelID {
			t.Errorf("Expected %s for multi-alias, got %s", realModelID, resolved)
		}

		// Multi alias match (client2 supports targetModelID)
		resolved = r.ResolveModelForClient("client2", "multi-alias")
		if resolved != targetModelID {
			t.Errorf("Expected %s for multi-alias, got %s", targetModelID, resolved)
		}

		// No match
		resolved = r.ResolveModelForClient("client2", "gpt-4-alias")
		if resolved != "gpt-4-alias" {
			t.Errorf("Expected gpt-4-alias (no resolution), got %s", resolved)
		}
	})

	// Test GetAvailableModels
	t.Run("GetAvailableModels", func(t *testing.T) {
		models := r.GetAvailableModels("openai")
		foundReal := false
		foundAlias := false
		foundMulti := false
		for _, m := range models {
			if m["id"] == realModelID {
				foundReal = true
			}
			if m["id"] == "gpt-4-alias" {
				foundAlias = true
			}
			if m["id"] == "multi-alias" {
				foundMulti = true
			}
		}
		if !foundReal {
			t.Error("real model not found in available models")
		}
		if !foundAlias {
			t.Error("alias model not found in available models")
		}
		if !foundMulti {
			t.Error("multi-alias not found in available models")
		}
	})

	// Test GetModelInfo
	t.Run("GetModelInfo", func(t *testing.T) {
		info := r.GetModelInfo(realModelID)
		if info == nil || info.ID != realModelID {
			t.Errorf("Expected info for %s", realModelID)
		}

		info = r.GetModelInfo("gpt-4-alias")
		if info == nil || info.ID != "gpt-4-alias" {
			t.Errorf("Expected info for alias gpt-4-alias, got %v", info)
		}

		info = r.GetModelInfo("multi-alias")
		if info == nil || info.ID != "multi-alias" {
			t.Errorf("Expected info for multi-alias, got %v", info)
		}
	})

	// 4. Test Conflict: Model is both real and an alias
	t.Run("Conflict_RealAndAlias", func(t *testing.T) {
		conflictID := "conflict-model"
		// Register conflict-model as a real model on client3
		r.RegisterClient("client3", "google", []*ModelInfo{
			{ID: conflictID, OwnedBy: "google"},
		})

		// Set conflict-model as an alias to realModelID (gpt-4-real)
		newAliases := make(map[string][]string)
		for k, v := range r.aliases {
			newAliases[k] = v
		}
		newAliases[conflictID] = []string{realModelID}
		r.SetAliases(newAliases)

		// Now conflict-model should have providers from BOTH client3 (google) and client1 (openai)
		providers := r.GetModelProviders(conflictID)
		if len(providers) != 2 {
			t.Errorf("Expected 2 providers for conflict model, got %v", providers)
		}

		// Check if both are present
		hasGoogle := false
		hasOpenAI := false
		for _, p := range providers {
			if p == "google" {
				hasGoogle = true
			}
			if p == "openai" {
				hasOpenAI = true
			}
		}
		if !hasGoogle || !hasOpenAI {
			t.Errorf("Expected both google and openai providers, got %v", providers)
		}

		// Count should be 2
		count := r.GetModelCount(conflictID)
		if count != 2 {
			t.Errorf("Expected count 2 for conflict model, got %d", count)
		}
	})
}
