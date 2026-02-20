package claude

import (
	"testing"
	"strings"
)

func TestSimplifyInputSchema(t *testing.T) {
	input := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"foo": map[string]interface{}{
				"type": "string",
				"description": "extra info",
			},
		},
		"required": []interface{}{"foo"},
		"extra": "discard me",
	}

	simplified := simplifyInputSchema(input).(map[string]interface{})
	
	if simplified["type"] != "object" {
		t.Error("missing type")
	}
	if _, ok := simplified["extra"]; ok {
		t.Error("extra field not discarded")
	}
	
	props := simplified["properties"].(map[string]interface{})
	foo := props["foo"].(map[string]interface{})
	if foo["type"] != "string" {
		t.Error("nested type missing")
	}
	if _, ok := foo["description"]; ok {
		t.Error("nested description not discarded")
	}
}

func TestCompressToolDescription(t *testing.T) {
	desc := "This is a very long tool description that should be compressed to a shorter version."
	compressed := compressToolDescription(desc, 60)
	
	if !strings.HasSuffix(compressed, "...") {
		t.Error("expected suffix ...")
	}
	if len(compressed) > 60 {
		t.Errorf("expected length <= 60, got %d", len(compressed))
	}
}

func TestCompressToolsIfNeeded(t *testing.T) {
	tools := []KiroToolWrapper{
		{
			ToolSpecification: KiroToolSpecification{
				Name: "t1",
				Description: "d1",
				InputSchema: KiroInputSchema{JSON: map[string]interface{}{"type": "object"}},
			},
		},
	}
	
	// No compression needed
	result := compressToolsIfNeeded(tools)
	if len(result) != 1 || result[0].ToolSpecification.Name != "t1" {
		t.Error("unexpected result for no compression")
	}
}
