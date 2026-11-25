package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGroupLegacyKeysHandlesNullAndEmptyGroupNames(t *testing.T) {
	raw := []byte(`- api-key: key-a
  name: null
  models:
    - name: gemini-4-hydrogen
- api-key: key-b
  name: ""
  models:
    - name: gemini-4-hydrogen
`)
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode {
		t.Fatalf("unexpected YAML shape")
	}
	groups := groupLegacyKeys(doc.Content[0], "gemini")
	if len(groups.Content) != 2 {
		t.Fatalf("group count = %d, want 2", len(groups.Content))
	}
	for i, group := range groups.Content {
		name := yamlPath(group, "name")
		want := "gemini-" + string(rune('1'+i))
		if name == nil || name.Value != want {
			t.Fatalf("group[%d].name = %v, want %q", i, name, want)
		}
		keys := yamlPath(group, "keys")
		if keys == nil || len(keys.Content) != 1 {
			t.Fatalf("group[%d] keys malformed", i)
		}
		if childName := yamlPath(keys.Content[0], "name"); childName != nil {
			t.Fatalf("group[%d] child key still has name: %v", i, childName.Value)
		}
	}
}
