package amp

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"gopkg.in/yaml.v3"
)

func TestModelMapper_MapModel_Basic(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "claude-opus-4.5", To: config.StringOrSlice{"claude-sonnet-4"}},
		{From: "gemini-ultra-2", To: config.StringOrSlice{"gemini-2.5-pro"}},
	}

	mapper := NewModelMapper(mappings)

	// Without a registered provider for the target, mapping should return empty
	result := mapper.MapModel("claude-opus-4.5")
	if result != "" {
		t.Errorf("Expected empty result when target has no provider, got %s", result)
	}
}

func TestModelMapper_MapModel_WithProvider(t *testing.T) {
	// Register a mock provider for the target model
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-client")

	mappings := []config.AmpModelMapping{
		{From: "claude-opus-4.5", To: config.StringOrSlice{"claude-sonnet-4"}},
	}

	mapper := NewModelMapper(mappings)

	// With a registered provider, mapping should work
	result := mapper.MapModel("claude-opus-4.5")
	if result != "claude-sonnet-4" {
		t.Errorf("Expected claude-sonnet-4, got %s", result)
	}
}

func TestModelMapper_MapModel_Recursive(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-rec", "claude", []*registry.ModelInfo{
		{ID: "real-model", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-client-rec")

	mappings := []config.AmpModelMapping{
		{From: "alias-1", To: config.StringOrSlice{"alias-2"}},
		{From: "alias-2", To: config.StringOrSlice{"real-model"}},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("alias-1")
	if result != "real-model" {
		t.Errorf("Expected real-model via recursion, got %q", result)
	}
}

func TestModelMapper_MapModel_CycleDetection(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "cycle-a", To: config.StringOrSlice{"cycle-b"}},
		{From: "cycle-b", To: config.StringOrSlice{"cycle-a"}},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("cycle-a")
	if result != "" {
		t.Errorf("Expected empty result for cycle, got %q", result)
	}
}

func TestModelMapper_MapModel_ComplexFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-fallback", "claude", []*registry.ModelInfo{
		{ID: "available-model", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-client-fallback")

	mappings := []config.AmpModelMapping{
		// alias-1 -> [missing-1, alias-2]
		{From: "alias-1", To: config.StringOrSlice{"missing-1", "alias-2"}},
		// alias-2 -> [missing-2, available-model]
		{From: "alias-2", To: config.StringOrSlice{"missing-2", "available-model"}},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("alias-1")
	if result != "available-model" {
		t.Errorf("Expected available-model via complex fallback, got %q", result)
	}
}

func TestModelMapper_MapModel_CaseInsensitive(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client2", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-client2")

	mappings := []config.AmpModelMapping{
		{From: "Claude-Opus-4.5", To: config.StringOrSlice{"claude-sonnet-4"}},
	}

	mapper := NewModelMapper(mappings)

	// Should match case-insensitively
	result := mapper.MapModel("claude-opus-4.5")
	if result != "claude-sonnet-4" {
		t.Errorf("Expected claude-sonnet-4, got %s", result)
	}
}

func TestModelMapper_MapModel_NotFound(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "claude-opus-4.5", To: config.StringOrSlice{"claude-sonnet-4"}},
	}

	mapper := NewModelMapper(mappings)

	// Unknown model should return empty
	result := mapper.MapModel("unknown-model")
	if result != "" {
		t.Errorf("Expected empty for unknown model, got %s", result)
	}
}

func TestModelMapper_UpdateMappings(t *testing.T) {
	mapper := NewModelMapper(nil)

	// Initially empty
	if len(mapper.GetMappings()) != 0 {
		t.Error("Expected 0 initial mappings")
	}

	// Update with new mappings
	mapper.UpdateMappings([]config.AmpModelMapping{
		{From: "model-a", To: config.StringOrSlice{"model-b"}},
		{From: "model-c", To: config.StringOrSlice{"model-d"}},
	})

	result := mapper.GetMappings()
	if len(result) != 2 {
		t.Errorf("Expected 2 mappings after update, got %d", len(result))
	}

	// Update again should replace, not append
	mapper.UpdateMappings([]config.AmpModelMapping{
		{From: "model-x", To: config.StringOrSlice{"model-y"}},
	})

	result = mapper.GetMappings()
	if len(result) != 1 {
		t.Errorf("Expected 1 mapping after second update, got %d", len(result))
	}
}

func TestModelMapper_UpdateMappings_SkipsInvalid(t *testing.T) {
	mapper := NewModelMapper(nil)

	mapper.UpdateMappings([]config.AmpModelMapping{
		{From: "", To: config.StringOrSlice{"model-b"}},        // Invalid: empty from
		{From: "model-a", To: config.StringOrSlice{}},          // Invalid: empty to
		{From: "  ", To: config.StringOrSlice{"model-b"}},      // Invalid: whitespace from
		{From: "model-c", To: config.StringOrSlice{"model-d"}}, // Valid
	})

	result := mapper.GetMappings()
	if len(result) != 1 {
		t.Errorf("Expected 1 valid mapping, got %d", len(result))
	}
}

func TestModelMapper_GetFallbacks(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{
			From: "claude-4-5-opus",
			To:   config.StringOrSlice{"model-1", "model-2", "model-3"},
		},
	}

	mapper := NewModelMapper(mappings)

	// Get all fallbacks
	fallbacks := mapper.GetFallbacks("claude-4-5-opus")
	if len(fallbacks) != 3 {
		t.Errorf("Expected 3 fallbacks, got %d", len(fallbacks))
	}
}

func TestStringOrSlice_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		expected []string
	}{
		{
			name:     "single string",
			yaml:     `to: "target-model"`,
			expected: []string{"target-model"},
		},
		{
			name:     "list of strings",
			yaml:     `to: ["t1", "t2"]`,
			expected: []string{"t1", "t2"},
		},
		{
			name:     "single string no quotes",
			yaml:     `to: target-model`,
			expected: []string{"target-model"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m config.AmpModelMapping
			if err := yaml.Unmarshal([]byte(tt.yaml), &m); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if len(m.To) != len(tt.expected) {
				t.Fatalf("expected length %d, got %d", len(tt.expected), len(m.To))
			}
			for i, v := range m.To {
				if v != tt.expected[i] {
					t.Errorf("expected %q, got %q", tt.expected[i], v)
				}
			}
		})
	}
}
