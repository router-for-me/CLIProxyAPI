package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexClientModelsInheritLeafField(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{"context_window": 400000}),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.6-sol": map[string]any{
			"$inherit": map[string]any{"context_window": "gpt-5.5"},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	if got := models["gpt-5.6-sol"]["context_window"]; got != float64(372000) {
		t.Fatalf("context_window = %v, want 372000", got)
	}
	if got := models["gpt-5.6-sol"]["display_name"]; got != "Test gpt-5.6-sol" {
		t.Fatalf("display_name = %v, want the local name", got)
	}
}

func TestCodexClientModelsInheritWholeEntry(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"context_window": 372000,
			"extra_number":   7,
		}),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"my-fast": map[string]any{
			"$inherit":     "gpt-5.5",
			"display_name": "My Fast",
			"description":  "Local variant",
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	custom := models["my-fast"]
	if custom == nil {
		t.Fatal("custom model missing from the effective catalog")
	}
	if custom["slug"] != "my-fast" {
		t.Fatalf("slug = %v, want %q", custom["slug"], "my-fast")
	}
	if custom["display_name"] != "My Fast" || custom["description"] != "Local variant" {
		t.Fatalf("identity fields = %v / %v, want the local values", custom["display_name"], custom["description"])
	}
	if custom["base_instructions"] != "Test instructions" {
		t.Fatalf("base_instructions = %v, want the inherited value", custom["base_instructions"])
	}
	if custom["extra_number"] != float64(7) {
		t.Fatalf("extra_number = %v, want the inherited 7", custom["extra_number"])
	}
	raw, errMarshal := json.Marshal(custom)
	if errMarshal != nil {
		t.Fatalf("marshal custom entry: %v", errMarshal)
	}
	if strings.Contains(string(raw), CodexClientModelsInheritKeyword) {
		t.Fatalf("effective entry still contains %s: %s", CodexClientModelsInheritKeyword, raw)
	}
}

func TestCodexClientModelsInheritIdentityFieldsAreNeverInherited(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{"context_window": 400000}),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.6-sol": map[string]any{"$inherit": "gpt-5.5"},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	patched := models["gpt-5.6-sol"]
	if patched["slug"] != "gpt-5.6-sol" {
		t.Fatalf("slug = %v, want %q", patched["slug"], "gpt-5.6-sol")
	}
	if patched["display_name"] != "Test gpt-5.6-sol" || patched["description"] != "Test model" {
		t.Fatalf("identity fields = %v / %v, want the base values", patched["display_name"], patched["description"])
	}
	if patched["context_window"] != float64(372000) {
		t.Fatalf("context_window = %v, want the inherited 372000", patched["context_window"])
	}
}

func TestCodexClientModelsInheritDeeperSourceWins(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"model_messages": map[string]any{
				"instructions_template": "from gpt-5.5",
				"guardian_v2":           map[string]any{"enabled": true},
			},
		}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{
			"model_messages": map[string]any{"instructions_template": "from gpt-5.6-sol"},
		}),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"my-mix": map[string]any{
			"$inherit": map[string]any{
				"":                                     "gpt-5.5",
				"model_messages.instructions_template": "gpt-5.6-sol",
			},
			"display_name": "My Mix",
			"description":  "Mixed sources",
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	messages, ok := models["my-mix"]["model_messages"].(map[string]any)
	if !ok {
		t.Fatalf("model_messages = %#v, want object", models["my-mix"]["model_messages"])
	}
	if messages["instructions_template"] != "from gpt-5.6-sol" {
		t.Fatalf("instructions_template = %v, want the deeper source", messages["instructions_template"])
	}
	guardian, ok := messages["guardian_v2"].(map[string]any)
	if !ok || guardian["enabled"] != true {
		t.Fatalf("guardian_v2 = %#v, want the shallow source subtree", messages["guardian_v2"])
	}
}

func TestCodexClientModelsInheritLocalPatchWins(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"model_messages": map[string]any{"instructions_template": "from gpt-5.5"},
		}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{
			"model_messages": map[string]any{
				"instructions_template": "from gpt-5.6-sol",
				"guardian_v2":           map[string]any{"enabled": true},
			},
		}),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5": map[string]any{
			"$inherit": map[string]any{"model_messages": "gpt-5.6-sol"},
			"model_messages": map[string]any{
				"instructions_template": "local template",
			},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	messages, _ := models["gpt-5.5"]["model_messages"].(map[string]any)
	if messages["instructions_template"] != "local template" {
		t.Fatalf("instructions_template = %v, want the local value", messages["instructions_template"])
	}
	if guardian, ok := messages["guardian_v2"].(map[string]any); !ok || guardian["enabled"] != true {
		t.Fatalf("guardian_v2 = %#v, want the inherited subtree", messages["guardian_v2"])
	}
}

func TestCodexClientModelsInheritNullRemovesInheritedField(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, nil),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{
			"model_messages": map[string]any{
				"instructions_template": "inherited",
				"guardian_v2":           map[string]any{"enabled": true},
			},
		}),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5": map[string]any{
			"$inherit":       map[string]any{"model_messages": "gpt-5.6-sol"},
			"model_messages": map[string]any{"instructions_template": nil},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	messages, _ := models["gpt-5.5"]["model_messages"].(map[string]any)
	if _, exists := messages["instructions_template"]; exists {
		t.Fatalf("instructions_template = %v, want it removed", messages["instructions_template"])
	}
	if guardian, ok := messages["guardian_v2"].(map[string]any); !ok || guardian["enabled"] != true {
		t.Fatalf("guardian_v2 = %#v, want the untouched inherited subtree", messages["guardian_v2"])
	}
}

func TestCodexClientModelsInheritChainDepth(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}),
		testCodexClientModelWithExtras("chain-1", 2, map[string]any{"context_window": 1}),
		testCodexClientModelWithExtras("chain-2", 3, map[string]any{"context_window": 2}),
		testCodexClientModelWithExtras("chain-3", 4, map[string]any{"context_window": 3}),
		testCodexClientModelWithExtras("chain-4", 5, map[string]any{"context_window": 4}),
	)

	threeHops := map[string]any{
		"chain-1": map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.5"}},
		"chain-2": map[string]any{"$inherit": map[string]any{"context_window": "chain-1"}},
		"chain-3": map[string]any{"$inherit": map[string]any{"context_window": "chain-2"}},
	}
	models, err := applyOverrideForTest(t, base, threeHops)
	if err != nil {
		t.Fatalf("three hop chain rejected: %v", err)
	}
	if got := models["chain-3"]["context_window"]; got != float64(372000) {
		t.Fatalf("chain-3 context_window = %v, want the value three hops away", got)
	}

	fourHops := map[string]any{
		"chain-1": map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.5"}},
		"chain-2": map[string]any{"$inherit": map[string]any{"context_window": "chain-1"}},
		"chain-3": map[string]any{"$inherit": map[string]any{"context_window": "chain-2"}},
		"chain-4": map[string]any{"$inherit": map[string]any{"context_window": "chain-3"}},
	}
	if _, err = applyOverrideForTest(t, base, fourHops); err == nil {
		t.Fatal("four hop chain accepted, want a depth error")
	} else if !strings.Contains(err.Error(), "hops") {
		t.Fatalf("four hop chain error = %v, want a depth error", err)
	}
}

func TestCodexClientModelsInheritCycles(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, nil),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)

	// Different fields may reference each other: only resolving the same model and
	// path twice is a cycle.
	legal := map[string]any{
		"gpt-5.5":     map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.6-sol"}},
		"gpt-5.6-sol": map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.5"}},
	}
	models, err := applyOverrideForTest(t, base, legal)
	if err != nil {
		t.Fatalf("cross field inheritance rejected: %v", err)
	}
	if got := models["gpt-5.5"]["base_instructions"]; got != "Test instructions" {
		t.Fatalf("base_instructions = %v, want the source value", got)
	}

	cyclic := map[string]any{
		"gpt-5.5":     map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.6-sol"}},
		"gpt-5.6-sol": map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.5"}},
	}
	if _, err = applyOverrideForTest(t, base, cyclic); err == nil {
		t.Fatal("cycle accepted, want an error")
	} else if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v, want a cycle error", err)
	}
}

func TestCodexClientModelsInheritRejectsBadDirectives(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, nil),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)

	tests := []struct {
		name string
		doc  map[string]any
		want string
	}{
		{
			name: "unknown source",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"context_window": "missing"}}},
			want: "not a known model",
		},
		{
			name: "removed source",
			doc: map[string]any{
				"gpt-5.5":     map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.6-sol"}},
				"gpt-5.6-sol": nil,
			},
			want: "is removed by this document",
		},
		{
			name: "self reference",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.5"}}},
			want: "inherit from itself",
		},
		{
			name: "non inheritable field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"display_name": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "slug field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"slug": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "array index path",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"supported_reasoning_levels[0]": "gpt-5.6-sol"}}},
			want: "array",
		},
		{
			name: "empty path segment",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"model_messages..x": "gpt-5.6-sol"}}},
			want: "empty segment",
		},
		{
			name: "non string source",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"context_window": 5}}},
			want: "must name a model",
		},
		{
			name: "wrong directive type",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": 5}},
			want: "model slug or an object",
		},
		{
			name: "source missing the path",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"not_a_field": "gpt-5.6-sol"}}},
			want: "does not define",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyOverrideForTest(t, base, tt.doc)
			if err == nil {
				t.Fatal("apply override error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestCodexClientModelsInheritResilientDegradesSingleEntry(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)

	models, issues, err := applyResilientOverrideForTest(t, base, map[string]any{
		"gpt-5.5":     map[string]any{"$inherit": map[string]any{"context_window": "missing"}},
		"broken":      map[string]any{"$inherit": map[string]any{"context_window": "missing"}},
		"custom-good": map[string]any{"$inherit": "gpt-5.6-sol", "display_name": "Good", "description": "Good"},
	})
	if err != nil {
		t.Fatalf("resilient apply: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("issues = %#v, want 2 entries", issues)
	}
	if got := models["gpt-5.5"]["context_window"]; got != float64(372000) {
		t.Fatalf("gpt-5.5 context_window = %v, want the base value", got)
	}
	if _, ok := models["broken"]; ok {
		t.Fatal("broken custom entry stayed in the catalog")
	}
	if models["custom-good"] == nil {
		t.Fatal("valid entry was dropped together with the broken ones")
	}
}

func TestCodexClientModelsInheritUsage(t *testing.T) {
	doc := map[string]json.RawMessage{
		"a": json.RawMessage(`{"$inherit":{"":"gpt-5.5","model_messages.x":"gpt-5.6-sol"}}`),
		"b": json.RawMessage(`{"$inherit":"gpt-5.6-sol"}`),
		"c": json.RawMessage(`{"display_name":"plain"}`),
	}

	usage := CodexClientModelsInheritUsage(doc)
	if usage["gpt-5.5"] != 1 || usage["gpt-5.6-sol"] != 2 {
		t.Fatalf("usage = %#v, want gpt-5.5=1 and gpt-5.6-sol=2", usage)
	}
}

func TestSyncCodexClientModelsOverrideFileDegradesSingleEntry(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModelWithExtras("gpt-5.5", 1, nil))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}

	document := `{"gpt-5.5":{"display_name":"From File"},"broken":{"$inherit":{"context_window":"missing"}}}`
	if errWrite := os.WriteFile(overridePath, []byte(document), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)

	state := GetCodexClientModelsState()
	if state.OverrideError != "" {
		t.Fatalf("override error = %q, want empty", state.OverrideError)
	}
	if len(state.OverrideErrors) != 1 || state.OverrideErrors[0].Slug != "broken" {
		t.Fatalf("override errors = %#v, want one issue for %q", state.OverrideErrors, "broken")
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "display_name"); got != "From File" {
		t.Fatalf("display_name = %v, want the valid entry to stay applied", got)
	}
	if len(state.Override) != 2 {
		t.Fatalf("override entries = %d, want the file to stay on disk", len(state.Override))
	}
}

func TestRefreshCodexClientModelsKeepsDegradedOverride(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, nil),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	brokenDocument := `{"gpt-5.6-sol":{"$inherit":{"context_window":"missing"}}}`
	if errWrite := os.WriteFile(overridePath, []byte(brokenDocument), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)
	if len(GetCodexClientModelsState().OverrideErrors) != 1 {
		t.Fatalf("override errors = %#v, want one issue", GetCodexClientModelsState().OverrideErrors)
	}

	refreshed := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 100}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)
	changed, err := setCodexClientModelsBase(refreshed, "https://example.test/models.json")
	if err != nil {
		t.Fatalf("refresh with a degraded override: %v", err)
	}
	if !changed {
		t.Fatal("refresh changed = false, want the new base catalog")
	}
	state := GetCodexClientModelsState()
	if state.Source != "https://example.test/models.json" {
		t.Fatalf("source = %q, want the refreshed URL", state.Source)
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "context_window"); got != float64(100) {
		t.Fatalf("context_window = %v, want the refreshed value", got)
	}
	if len(state.OverrideErrors) != 1 {
		t.Fatalf("override errors after refresh = %#v, want the issue to stay reported", state.OverrideErrors)
	}
}

func applyResilientOverrideForTest(t *testing.T, base []byte, document map[string]any) (map[string]map[string]any, []CodexClientModelsOverrideIssue, error) {
	t.Helper()
	override := make(map[string]json.RawMessage, len(document))
	for slug, patch := range document {
		raw, errMarshal := json.Marshal(patch)
		if errMarshal != nil {
			t.Fatalf("marshal override patch for %q: %v", slug, errMarshal)
		}
		override[slug] = raw
	}

	data, issues, errApply := applyCodexClientModelsOverrideResilient(base, override)
	if errApply != nil {
		return nil, issues, errApply
	}
	if errValidate := ValidateCodexClientModelsJSON(data); errValidate != nil {
		return nil, issues, errValidate
	}
	models, _, errParse := parseCodexClientModels(data)
	if errParse != nil {
		return nil, issues, errParse
	}
	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		slug, _ := model["slug"].(string)
		bySlug[slug] = model
	}
	return bySlug, issues, nil
}
