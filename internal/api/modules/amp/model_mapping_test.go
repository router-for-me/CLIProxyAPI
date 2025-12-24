package amp

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestNewModelMapper(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "claude-opus-4.5", To: "claude-sonnet-4"},
		{From: "gpt-5", To: "gemini-2.5-pro"},
	}

	mapper := NewModelMapper(mappings)
	if mapper == nil {
		t.Fatal("Expected non-nil mapper")
	}

	result := mapper.GetMappings()
	if len(result) != 2 {
		t.Errorf("Expected 2 mappings, got %d", len(result))
	}
}

func TestNewModelMapper_Empty(t *testing.T) {
	mapper := NewModelMapper(nil)
	if mapper == nil {
		t.Fatal("Expected non-nil mapper")
	}

	result := mapper.GetMappings()
	if len(result) != 0 {
		t.Errorf("Expected 0 mappings, got %d", len(result))
	}
}

func TestModelMapper_MapModel_NoProvider(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "claude-opus-4.5", To: "claude-sonnet-4"},
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
		{From: "claude-opus-4.5", To: "claude-sonnet-4"},
	}

	mapper := NewModelMapper(mappings)

	// With a registered provider, mapping should work
	result := mapper.MapModel("claude-opus-4.5")
	if result != "claude-sonnet-4" {
		t.Errorf("Expected claude-sonnet-4, got %s", result)
	}
}

func TestModelMapper_MapModel_TargetWithThinkingSuffix(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-thinking", "codex", []*registry.ModelInfo{
		{ID: "gpt-5.2", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("test-client-thinking")

	mappings := []config.AmpModelMapping{
		{From: "gpt-5.2-alias", To: "gpt-5.2(xhigh)"},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("gpt-5.2-alias")
	if result != "gpt-5.2(xhigh)" {
		t.Errorf("Expected gpt-5.2(xhigh), got %s", result)
	}
}

func TestModelMapper_MapModel_CaseInsensitive(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client2", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-client2")

	mappings := []config.AmpModelMapping{
		{From: "Claude-Opus-4.5", To: "claude-sonnet-4"},
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
		{From: "claude-opus-4.5", To: "claude-sonnet-4"},
	}

	mapper := NewModelMapper(mappings)

	// Unknown model should return empty
	result := mapper.MapModel("unknown-model")
	if result != "" {
		t.Errorf("Expected empty for unknown model, got %s", result)
	}
}

func TestModelMapper_MapModel_EmptyInput(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "claude-opus-4.5", To: "claude-sonnet-4"},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("")
	if result != "" {
		t.Errorf("Expected empty for empty input, got %s", result)
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
		{From: "model-a", To: "model-b"},
		{From: "model-c", To: "model-d"},
	})

	result := mapper.GetMappings()
	if len(result) != 2 {
		t.Errorf("Expected 2 mappings after update, got %d", len(result))
	}

	// Update again should replace, not append
	mapper.UpdateMappings([]config.AmpModelMapping{
		{From: "model-x", To: "model-y"},
	})

	result = mapper.GetMappings()
	if len(result) != 1 {
		t.Errorf("Expected 1 mapping after second update, got %d", len(result))
	}
}

func TestModelMapper_UpdateMappings_SkipsInvalid(t *testing.T) {
	mapper := NewModelMapper(nil)

	mapper.UpdateMappings([]config.AmpModelMapping{
		{From: "", To: "model-b"},        // Invalid: empty from
		{From: "model-a", To: ""},        // Invalid: empty to
		{From: "  ", To: "model-b"},      // Invalid: whitespace from
		{From: "model-c", To: "model-d"}, // Valid
	})

	result := mapper.GetMappings()
	if len(result) != 1 {
		t.Errorf("Expected 1 valid mapping, got %d", len(result))
	}
}

func TestModelMapper_GetMappings_ReturnsCopy(t *testing.T) {
	mappings := []config.AmpModelMapping{
		{From: "model-a", To: "model-b"},
	}

	mapper := NewModelMapper(mappings)

	// Get mappings and modify the returned map
	result := mapper.GetMappings()
	result["new-key"] = "new-value"

	// Original should be unchanged
	original := mapper.GetMappings()
	if len(original) != 1 {
		t.Errorf("Expected original to have 1 mapping, got %d", len(original))
	}
	if _, exists := original["new-key"]; exists {
		t.Error("Original map was modified")
	}
}

func TestModelMapper_Regex_MatchBaseWithoutParens(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-regex-1", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", OwnedBy: "google", Type: "gemini"},
	})
	defer reg.UnregisterClient("test-client-regex-1")

	mappings := []config.AmpModelMapping{
		{From: "^gpt-5$", To: "gemini-2.5-pro", Regex: true},
	}

	mapper := NewModelMapper(mappings)

	// Incoming model has reasoning suffix but should match base via regex
	result := mapper.MapModel("gpt-5(high)")
	if result != "gemini-2.5-pro" {
		t.Errorf("Expected gemini-2.5-pro, got %s", result)
	}
}

func TestModelMapper_Regex_ExactPrecedence(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-regex-2", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	reg.RegisterClient("test-client-regex-3", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", OwnedBy: "google", Type: "gemini"},
	})
	defer reg.UnregisterClient("test-client-regex-2")
	defer reg.UnregisterClient("test-client-regex-3")

	mappings := []config.AmpModelMapping{
		{From: "gpt-5", To: "claude-sonnet-4"},                 // exact
		{From: "^gpt-5.*$", To: "gemini-2.5-pro", Regex: true}, // regex
	}

	mapper := NewModelMapper(mappings)

	// Exact match should win over regex
	result := mapper.MapModel("gpt-5")
	if result != "claude-sonnet-4" {
		t.Errorf("Expected claude-sonnet-4, got %s", result)
	}
}

func TestModelMapper_Regex_InvalidPattern_Skipped(t *testing.T) {
	// Invalid regex should be skipped and not cause panic
	mappings := []config.AmpModelMapping{
		{From: "(", To: "target", Regex: true},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("anything")
	if result != "" {
		t.Errorf("Expected empty result due to invalid regex, got %s", result)
	}
}

func TestModelMapper_Regex_CaseInsensitive(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-client-regex-4", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-client-regex-4")

	mappings := []config.AmpModelMapping{
		{From: "^CLAUDE-OPUS-.*$", To: "claude-sonnet-4", Regex: true},
	}

	mapper := NewModelMapper(mappings)

	result := mapper.MapModel("claude-opus-4.5")
	if result != "claude-sonnet-4" {
		t.Errorf("Expected claude-sonnet-4, got %s", result)
	}
}
