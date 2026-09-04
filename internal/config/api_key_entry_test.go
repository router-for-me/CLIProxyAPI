package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAPIKeyEntryUnmarshalMixedYAML(t *testing.T) {
	const doc = `api-keys:
  - "plain-key"
  - key: "named-key"
    name: "alice"
  - unquoted-key
`

	var parsed struct {
		APIKeys []APIKeyEntry `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []APIKeyEntry{
		{Key: "plain-key"},
		{Key: "named-key", Name: "alice"},
		{Key: "unquoted-key"},
	}
	if len(parsed.APIKeys) != len(want) {
		t.Fatalf("entries = %#v, want %#v", parsed.APIKeys, want)
	}
	for i := range want {
		if parsed.APIKeys[i] != want[i] {
			t.Fatalf("entry[%d] = %#v, want %#v", i, parsed.APIKeys[i], want[i])
		}
	}
}

func TestAPIKeyEntryMarshalYAMLScalarForUnnamed(t *testing.T) {
	entries := []APIKeyEntry{
		{Key: "plain-key"},
		{Key: "named-key", Name: "alice"},
	}

	out, err := yaml.Marshal(map[string]any{"api-keys": entries})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "- plain-key\n") {
		t.Fatalf("unnamed entry not rendered as scalar: %s", got)
	}
	if !strings.Contains(got, "key: named-key") || !strings.Contains(got, "name: alice") {
		t.Fatalf("named entry not rendered as mapping: %s", got)
	}
}

func TestAPIKeyEntryJSONRoundTrip(t *testing.T) {
	var entries []APIKeyEntry
	if err := json.Unmarshal([]byte(`["plain-key",{"key":"named-key","name":"alice"}]`), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 2 || entries[0] != (APIKeyEntry{Key: "plain-key"}) || entries[1] != (APIKeyEntry{Key: "named-key", Name: "alice"}) {
		t.Fatalf("entries = %#v", entries)
	}

	out, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `["plain-key",{"key":"named-key","name":"alice"}]` {
		t.Fatalf("marshal = %s", out)
	}
}

func TestAPIKeyValues(t *testing.T) {
	cfg := &SDKConfig{APIKeys: []APIKeyEntry{{Key: "a"}, {Key: "b", Name: "bob"}}}
	got := cfg.APIKeyValues()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("APIKeyValues() = %#v, want [a b]", got)
	}
	if values := (*SDKConfig)(nil).APIKeyValues(); values != nil {
		t.Fatalf("nil config APIKeyValues() = %#v, want nil", values)
	}
}
