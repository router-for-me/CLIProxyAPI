package amp

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestHasModelMappingsChanged_DetectsWhenChange(t *testing.T) {
	mod := &AmpModule{}
	old := &config.AmpCode{ModelMappings: []config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}},
	}}
	newer := &config.AmpCode{ModelMappings: []config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{ToolChoice: "create_handoff_context"}},
	}}
	if !mod.hasModelMappingsChanged(old, newer) {
		t.Error("expected change to be detected when only When clause changes")
	}
}

func TestHasModelMappingsChanged_DetectsReorder(t *testing.T) {
	mod := &AmpModule{}
	a := config.AmpModelMapping{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}}
	b := config.AmpModelMapping{From: "gemini-3-flash-preview", To: "gpt-default"}
	old := &config.AmpCode{ModelMappings: []config.AmpModelMapping{a, b}}
	newer := &config.AmpCode{ModelMappings: []config.AmpModelMapping{b, a}}
	if !mod.hasModelMappingsChanged(old, newer) {
		t.Error("expected change to be detected when mappings are reordered")
	}
}

func TestHasModelMappingsChanged_NoChange(t *testing.T) {
	mod := &AmpModule{}
	mappings := []config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}},
		{From: "gemini-3-flash-preview", To: "gpt-default"},
	}
	old := &config.AmpCode{ModelMappings: mappings}
	newer := &config.AmpCode{ModelMappings: append([]config.AmpModelMapping(nil), mappings...)}
	if mod.hasModelMappingsChanged(old, newer) {
		t.Error("expected no change for identical mappings")
	}
}

func TestHasModelMappingsChanged_DetectsAddedWhen(t *testing.T) {
	mod := &AmpModule{}
	old := &config.AmpCode{ModelMappings: []config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-x"},
	}}
	newer := &config.AmpCode{ModelMappings: []config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-x", When: &config.AmpMappingCondition{Feature: "handoff"}},
	}}
	if !mod.hasModelMappingsChanged(old, newer) {
		t.Error("expected change when When is added")
	}
}
