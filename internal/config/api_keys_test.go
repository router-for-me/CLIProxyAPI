package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// allowedModelsPtr is a tiny helper for building an *[]string in literals.
func allowedModelsPtr(models ...string) *[]string {
	normalized := normalizeModelList(models)
	return &normalized
}

func TestAPIKeyList_AllowsModel(t *testing.T) {
	list := APIKeyList{
		{APIKey: "full"}, // unrestricted
		{APIKey: "deny", AllowedModels: allowedModelsPtr()},
		{APIKey: "guest", AllowedModels: allowedModelsPtr("gpt-4o-mini", "claude-3-5-haiku*")},
	}

	tests := []struct {
		name      string
		clientKey string
		model     string
		want      bool
	}{
		{"unrestricted key allows anything", "full", "anything-model", true},
		{"empty client key (open proxy) allows", "", "anything-model", true},
		{"unknown key falls back to unrestricted", "not-listed", "anything-model", true},
		{"deny-all rejects non-empty model", "deny", "gpt-4o", false},
		{"deny-all rejects even on empty model name handled elsewhere", "deny", "x", false},
		{"allowlist exact match", "guest", "gpt-4o-mini", true},
		{"allowlist exact mismatch", "guest", "gpt-4o", false},
		{"allowlist wildcard prefix", "guest", "claude-3-5-haiku-20241022", true},
		{"allowlist case-insensitive match", "guest", "GPT-4O-MINI", true},
		{"allowlist case-insensitive wildcard", "guest", "CLAUDE-3-5-HAIKU-LONG", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := list.AllowsModel(tc.clientKey, tc.model); got != tc.want {
				t.Fatalf("AllowsModel(%q, %q) = %v, want %v", tc.clientKey, tc.model, got, tc.want)
			}
		})
	}
}

func TestAPIKeyList_EmptyModelNameAlwaysAllowedByACL(t *testing.T) {
	// An empty/whitespace model name should not be turned into a 404 by the
	// ACL layer; downstream validation handles empty models. The guard in
	// handlers treats empty model as allowed (returns nil); verify the helper
	// itself reports deny for empty so callers must check, mirroring intent.
	list := APIKeyList{{APIKey: "guest", AllowedModels: allowedModelsPtr("gpt-4o")}}
	if list.AllowsModel("guest", "") {
		t.Fatalf("AllowsModel with empty model = true, want false (callers must guard empty)")
	}
	if list.AllowsModel("guest", "   ") {
		t.Fatalf("AllowsModel with blank model = true, want false")
	}
}

func TestAPIKeyList_DuplicateKeyLastWins(t *testing.T) {
	list := APIKeyList{
		{APIKey: "k", AllowedModels: allowedModelsPtr("a")},
		{APIKey: "k", AllowedModels: allowedModelsPtr("b")},
	}
	entry, ok := list.Lookup("k")
	if !ok {
		t.Fatal("Lookup failed for duplicated key")
	}
	if got := *entry.AllowedModels; !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("duplicate key last-wins failed, got %v want [b]", got)
	}
	if !list.AllowsModel("k", "b") {
		t.Fatal("last-wins allowlist should permit b")
	}
	if list.AllowsModel("k", "a") {
		t.Fatal("last-wins allowlist should NOT permit a")
	}
}

func TestAPIKeyList_FilterModelIDs(t *testing.T) {
	t.Run("no restriction returns input as-is", func(t *testing.T) {
		list := APIKeyList{{APIKey: "k"}}
		in := []string{"a", "b"}
		if got := list.FilterModelIDs("k", in); !reflect.DeepEqual(got, in) {
			t.Fatalf("got %v want %v", got, in)
		}
	})
	t.Run("allowlist filters", func(t *testing.T) {
		list := APIKeyList{{APIKey: "k", AllowedModels: allowedModelsPtr("keep-*", "exact")}}
		in := []string{"keep-1", "drop", "exact", "other"}
		want := []string{"keep-1", "exact"}
		if got := list.FilterModelIDs("k", in); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v want %v", got, want)
		}
	})
}

func TestAPIKeyList_FilterModelMaps_GeminiPrefix(t *testing.T) {
	list := APIKeyList{{APIKey: "k", AllowedModels: allowedModelsPtr("gemini-2.5-flash")}}
	in := []map[string]any{
		{"name": "models/gemini-2.5-flash"},
		{"name": "models/gemini-2.5-pro"},
	}
	got := list.FilterModelMaps("k", in, "name")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if name, _ := got[0]["name"].(string); name != "models/gemini-2.5-flash" {
		t.Fatalf("kept wrong entry: %v", got[0])
	}
}

func TestAPIKeyList_YAMLRoundTripMixed(t *testing.T) {
	src := `
api-keys:
  - "plain-key"
  - api-key: "obj-key"
    allowed-models:
      - "gpt-4o-mini"
      - "claude-*"
  - api-key: "deny-key"
    allowed-models: []
`
	var wrapped struct {
		Keys APIKeyList `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal([]byte(src), &wrapped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wrapped.Keys) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(wrapped.Keys))
	}
	if wrapped.Keys[0].APIKey != "plain-key" || wrapped.Keys[0].AllowedModels != nil {
		t.Fatalf("entry 0 wrong: %+v", wrapped.Keys[0])
	}
	if wrapped.Keys[1].APIKey != "obj-key" || wrapped.Keys[1].AllowedModels == nil {
		t.Fatalf("entry 1 wrong: %+v", wrapped.Keys[1])
	}
	if got := *wrapped.Keys[1].AllowedModels; !reflect.DeepEqual(got, []string{"gpt-4o-mini", "claude-*"}) {
		t.Fatalf("entry 1 allowed-models wrong: %v", got)
	}
	if wrapped.Keys[2].AllowedModels == nil {
		t.Fatal("deny-key should have non-nil empty allowlist")
	}
	if len(*wrapped.Keys[2].AllowedModels) != 0 {
		t.Fatalf("deny-key allowlist should be empty, got %v", *wrapped.Keys[2].AllowedModels)
	}

	// Marshal back and re-parse to confirm symmetry.
	out, err := yaml.Marshal(struct {
		Keys APIKeyList `yaml:"api-keys"`
	}{wrapped.Keys})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reparsed struct {
		Keys APIKeyList `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal(out, &reparsed); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !reflect.DeepEqual(reparsed.Keys, wrapped.Keys) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", reparsed.Keys, wrapped.Keys)
	}
}

func TestAPIKeyList_JSONRoundTripMixed(t *testing.T) {
	in := APIKeyList{
		{APIKey: "plain"},
		{APIKey: "obj", AllowedModels: allowedModelsPtr("a", "b*")},
		{APIKey: "deny", AllowedModels: allowedModelsPtr()},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out APIKeyList
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round-trip mismatch:\n got=%+v\nwant=%+v", out, in)
	}
}

func TestAPIKeyList_HasRestrictions(t *testing.T) {
	plain := APIKeyList{{APIKey: "a"}}
	if plain.HasRestrictions() {
		t.Fatal("plain-only list should have no restrictions")
	}
	restricted := APIKeyList{{APIKey: "a"}, {APIKey: "b", AllowedModels: allowedModelsPtr("x")}}
	if !restricted.HasRestrictions() {
		t.Fatal("list with one allowlist entry should report restrictions")
	}
}
