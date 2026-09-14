package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodexClientModelsInheritLeafField(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"minimal_client_version": "0.140.0"}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.6-sol": map[string]any{
			"$inherit": map[string]any{"minimal_client_version": "gpt-5.5"},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	if got := models["gpt-5.6-sol"]["minimal_client_version"]; got != "0.140.0" {
		t.Fatalf("minimal_client_version = %v, want 0.140.0", got)
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
		testCodexClientModel("my-fast", 9),
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

func TestCodexClientModelsInheritModelFieldsAreNeverInherited(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"base_instructions":          "from gpt-5.5",
			"visibility":                 "hide",
			"context_window":             100000,
			"max_context_window":         200000,
			"max_tokens":                 1234,
			"auto_compact_token_limit":   5678,
			"default_reasoning_level":    "xhigh",
			"default_reasoning_summary":  "detailed",
			"default_verbosity":          "high",
			"support_verbosity":          true,
			"supported_reasoning_levels": []map[string]any{{"effort": "xhigh", "description": "Deep"}},
		}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{
			"context_window":     300000,
			"max_context_window": 400000,
			"max_tokens":         4321,
		}),
	)
	own := codexClientModelDefaultsForTest(t, base)["gpt-5.6-sol"]

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
	// Visibility, position and the context and reasoning envelope belong to the model,
	// so a whole entry source leaves them alone even though it supplies everything else.
	if patched["visibility"] != "list" {
		t.Fatalf("visibility = %v, want the entry's own list", patched["visibility"])
	}
	if patched["priority"] != float64(2) {
		t.Fatalf("priority = %v, want the entry's own 2", patched["priority"])
	}
	if patched["context_window"] != float64(300000) || patched["max_context_window"] != float64(400000) {
		t.Fatalf("context windows = %v / %v, want the entry's own values", patched["context_window"], patched["max_context_window"])
	}
	// The context and reasoning envelope follows the model and the provider behind it,
	// so a whole entry source leaves it alone too.
	if patched["max_tokens"] != float64(4321) {
		t.Fatalf("max_tokens = %v, want the entry's own 4321", patched["max_tokens"])
	}
	if patched["default_reasoning_level"] != "medium" {
		t.Fatalf("default_reasoning_level = %v, want the entry's own medium", patched["default_reasoning_level"])
	}
	if !reflect.DeepEqual(patched["supported_reasoning_levels"], own["supported_reasoning_levels"]) {
		t.Fatalf("supported_reasoning_levels = %v, want the entry's own levels", patched["supported_reasoning_levels"])
	}
	// A field the entry does not define stays absent rather than being filled in from
	// the source.
	for _, field := range []string{
		"auto_compact_token_limit",
		"default_reasoning_summary",
		"default_verbosity",
		"support_verbosity",
	} {
		if value, exists := patched[field]; exists {
			t.Fatalf("%s = %v, want it absent on a model that does not define it", field, value)
		}
	}
	if patched["base_instructions"] != "from gpt-5.5" {
		t.Fatalf("base_instructions = %v, want the inherited value", patched["base_instructions"])
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
		testCodexClientModel("my-mix", 9),
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
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"base_instructions": "from gpt-5.5"}),
		testCodexClientModelWithExtras("chain-1", 2, map[string]any{"base_instructions": "from chain-1"}),
		testCodexClientModelWithExtras("chain-2", 3, map[string]any{"base_instructions": "from chain-2"}),
		testCodexClientModelWithExtras("chain-3", 4, map[string]any{"base_instructions": "from chain-3"}),
		testCodexClientModelWithExtras("chain-4", 5, map[string]any{"base_instructions": "from chain-4"}),
	)

	threeHops := map[string]any{
		"chain-1": map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.5"}},
		"chain-2": map[string]any{"$inherit": map[string]any{"base_instructions": "chain-1"}},
		"chain-3": map[string]any{"$inherit": map[string]any{"base_instructions": "chain-2"}},
	}
	models, err := applyOverrideForTest(t, base, threeHops)
	if err != nil {
		t.Fatalf("three hop chain rejected: %v", err)
	}
	if got := models["chain-3"]["base_instructions"]; got != "from gpt-5.5" {
		t.Fatalf("chain-3 base_instructions = %v, want the value three hops away", got)
	}

	fourHops := map[string]any{
		"chain-1": map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.5"}},
		"chain-2": map[string]any{"$inherit": map[string]any{"base_instructions": "chain-1"}},
		"chain-3": map[string]any{"$inherit": map[string]any{"base_instructions": "chain-2"}},
		"chain-4": map[string]any{"$inherit": map[string]any{"base_instructions": "chain-3"}},
	}
	if _, err = applyOverrideForTest(t, base, fourHops); err == nil {
		t.Fatal("four hop chain accepted, want a depth error")
	} else if !strings.Contains(err.Error(), "hops") {
		t.Fatalf("four hop chain error = %v, want a depth error", err)
	}
}

func TestCodexClientModelsInheritFieldLevelChainDepth(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"web_search_tool_type": "native"}),
		testCodexClientModelWithExtras("field-a", 2, nil),
		testCodexClientModelWithExtras("field-b", 3, map[string]any{"base_instructions": "from field-b"}),
		testCodexClientModelWithExtras("field-c", 4, map[string]any{"minimal_client_version": "0.160.0"}),
		testCodexClientModelWithExtras("field-d", 5, map[string]any{"model_specialty": "coding"}),
	)

	// Each entry borrows one field from the next model, so the entries form a four
	// model graph while no field resolution follows more than one hop.
	document := map[string]any{
		"field-a": map[string]any{"$inherit": map[string]any{"base_instructions": "field-b"}},
		"field-b": map[string]any{"$inherit": map[string]any{"minimal_client_version": "field-c"}},
		"field-c": map[string]any{"$inherit": map[string]any{"model_specialty": "field-d"}},
		"field-d": map[string]any{"$inherit": map[string]any{"web_search_tool_type": "gpt-5.5"}},
	}

	models, err := applyOverrideForTest(t, base, document)
	if err != nil {
		t.Fatalf("field level inheritance rejected: %v", err)
	}
	if got := models["field-a"]["base_instructions"]; got != "from field-b" {
		t.Fatalf("field-a base_instructions = %v, want the value one hop away", got)
	}
	if got := models["field-b"]["minimal_client_version"]; got != "0.160.0" {
		t.Fatalf("field-b minimal_client_version = %v, want the value one hop away", got)
	}
	if got := models["field-c"]["model_specialty"]; got != "coding" {
		t.Fatalf("field-c model_specialty = %v, want the value one hop away", got)
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
		"gpt-5.6-sol": map[string]any{"$inherit": map[string]any{"minimal_client_version": "gpt-5.5"}},
	}
	models, err := applyOverrideForTest(t, base, legal)
	if err != nil {
		t.Fatalf("cross field inheritance rejected: %v", err)
	}
	if got := models["gpt-5.5"]["base_instructions"]; got != "Test instructions" {
		t.Fatalf("base_instructions = %v, want the source value", got)
	}

	cyclic := map[string]any{
		"gpt-5.5":     map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.6-sol"}},
		"gpt-5.6-sol": map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.5"}},
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
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"base_instructions": "missing"}}},
			want: "not a served model",
		},
		{
			name: "removed source",
			doc: map[string]any{
				"gpt-5.5":     map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.6-sol"}},
				"gpt-5.6-sol": nil,
			},
			want: "is removed by this document",
		},
		{
			name: "self reference",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"base_instructions": "gpt-5.5"}}},
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
			name: "visibility field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"visibility": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "priority field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"priority": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "context window field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"context_window": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "token budget field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"max_tokens": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "reasoning level field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"supported_reasoning_levels": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "verbosity field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"default_verbosity": "gpt-5.6-sol"}}},
			want: "cannot be inherited",
		},
		{
			name: "opt out on a model field",
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"visibility": nil}}},
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
			doc:  map[string]any{"gpt-5.5": map[string]any{"$inherit": map[string]any{"base_instructions": 5}}},
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

func TestCodexClientModelsInheritOptOutKeepsTheModelsOwnValue(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"base_instructions": "from gpt-5.5",
			"model_messages": map[string]any{
				"instructions_template": "from gpt-5.5",
				"guardian_v2":           map[string]any{"enabled": true},
			},
		}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, map[string]any{
			"base_instructions": "from gpt-5.6-sol",
		}),
	)

	// A null source opts a root field out: the model keeps its own value while the
	// rest of the entry still comes from the source.
	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.6-sol": map[string]any{
			"$inherit": map[string]any{"": "gpt-5.5", "base_instructions": nil},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}
	patched := models["gpt-5.6-sol"]
	if patched["base_instructions"] != "from gpt-5.6-sol" {
		t.Fatalf("base_instructions = %v, want the model's own value", patched["base_instructions"])
	}
	messages, _ := patched["model_messages"].(map[string]any)
	if guardian, ok := messages["guardian_v2"].(map[string]any); !ok || guardian["enabled"] != true {
		t.Fatalf("model_messages.guardian_v2 = %#v, want the inherited subtree", messages["guardian_v2"])
	}
	if messages["instructions_template"] != "from gpt-5.5" {
		t.Fatalf("instructions_template = %v, want the inherited value", messages["instructions_template"])
	}

	// The same works below the root: the opted-out key drops the inherited value and
	// the model has none of its own to put back.
	models, err = applyOverrideForTest(t, base, map[string]any{
		"gpt-5.6-sol": map[string]any{
			"$inherit": map[string]any{"": "gpt-5.5", "model_messages.instructions_template": nil},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}
	messages, _ = models["gpt-5.6-sol"]["model_messages"].(map[string]any)
	if _, exists := messages["instructions_template"]; exists {
		t.Fatalf("instructions_template = %v, want it dropped rather than inherited", messages["instructions_template"])
	}
	if guardian, ok := messages["guardian_v2"].(map[string]any); !ok || guardian["enabled"] != true {
		t.Fatalf("model_messages.guardian_v2 = %#v, want the untouched inherited subtree", messages["guardian_v2"])
	}
}

func TestCodexClientModelsInheritOptOutKeepsTheEntryValid(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"model_messages": map[string]any{"instructions_template": "from gpt-5.5"},
		}),
		testCodexClientModel("my-custom", 9),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"my-custom": map[string]any{
			"$inherit":     map[string]any{"": "gpt-5.5", "model_messages": nil},
			"display_name": "My Custom",
			"description":  "Custom variant",
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	custom := models["my-custom"]
	if custom == nil {
		t.Fatal("custom model missing from the effective catalog")
	}
	if _, exists := custom["model_messages"]; exists {
		t.Fatalf("model_messages = %#v, want the opted-out field dropped", custom["model_messages"])
	}
	if custom["base_instructions"] != "Test instructions" {
		t.Fatalf("base_instructions = %v, want the rest of the entry inherited", custom["base_instructions"])
	}
}

func TestCodexClientModelsInheritResilientDegradesSingleEntry(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)

	models, issues, err := applyResilientOverrideForTest(t, base, map[string]any{
		"gpt-5.5":     map[string]any{"$inherit": map[string]any{"base_instructions": "missing"}},
		"gpt-5.6-sol": map[string]any{"display_name": "Good"},
	})
	if err != nil {
		t.Fatalf("resilient apply: %v", err)
	}

	// Only the entry that cannot be applied is reported, and it keeps the entry the
	// server assembled for its model.
	if len(issues) != 1 || issues[0].Slug != "gpt-5.5" {
		t.Fatalf("issues = %#v, want one issue for %q", issues, "gpt-5.5")
	}
	if got := models["gpt-5.5"]["context_window"]; got != float64(372000) {
		t.Fatalf("gpt-5.5 context_window = %v, want the default value", got)
	}
	if got := models["gpt-5.6-sol"]["display_name"]; got != "Good" {
		t.Fatalf("gpt-5.6-sol display_name = %v, want the valid override applied", got)
	}
}

func TestCodexClientModelsInheritUsage(t *testing.T) {
	doc := map[string]json.RawMessage{
		"a": json.RawMessage(`{"$inherit":{"":"gpt-5.5","model_messages.x":"gpt-5.6-sol"}}`),
		"b": json.RawMessage(`{"$inherit":"gpt-5.6-sol"}`),
		"c": json.RawMessage(`{"display_name":"plain"}`),
		// An opt-out names no source, so it adds no usage for the path it keeps.
		"d": json.RawMessage(`{"$inherit":{"":"gpt-5.5","model_messages.x":null}}`),
	}

	usage := CodexClientModelsInheritUsage(doc)
	if usage["gpt-5.5"] != 2 || usage["gpt-5.6-sol"] != 2 {
		t.Fatalf("usage = %#v, want gpt-5.5=2 and gpt-5.6-sol=2", usage)
	}
}

func TestSyncCodexClientModelsOverrideFileLoadsEveryEntry(t *testing.T) {
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

	document := `{"gpt-5.5":{"display_name":"From File"},"gpt-5.6-sol":{"$inherit":{"base_instructions":"missing"}}}`
	if errWrite := os.WriteFile(overridePath, []byte(document), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)

	state := GetCodexClientModelsState()
	if state.OverrideError != "" {
		t.Fatalf("override error = %q, want empty", state.OverrideError)
	}
	if len(state.Override) != 2 {
		t.Fatalf("override entries = %d, want the file to be loaded as written", len(state.Override))
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "display_name"); got != "From File" {
		t.Fatalf("display_name = %v, want the valid entry to stay applied", got)
	}

	// The entry that cannot be applied is reported when the entries are resolved, and
	// its model keeps the entry the server assembled for it.
	raw, _ := GetCodexClientModelsSnapshot()
	served, issues := ResolveCodexClientModelOverrides(codexClientModelDefaultsForTest(t, raw), state.Override)
	if len(issues) != 1 || issues[0].Slug != "gpt-5.6-sol" {
		t.Fatalf("issues = %#v, want one issue for %q", issues, "gpt-5.6-sol")
	}
	if got := served["gpt-5.6-sol"]["base_instructions"]; got != "Test instructions" {
		t.Fatalf("base_instructions = %v, want the assembled default", got)
	}
}

func TestRefreshCodexClientModelsKeepsTheOverride(t *testing.T) {
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
	document := `{"gpt-5.6-sol":{"display_name":"From File"}}`
	if errWrite := os.WriteFile(overridePath, []byte(document), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)

	refreshed := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 100}),
		testCodexClientModelWithExtras("gpt-5.6-sol", 2, nil),
	)
	changed, err := setCodexClientModelsBase(refreshed, "https://example.test/models.json")
	if err != nil {
		t.Fatalf("refresh with an override: %v", err)
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
	// A base refresh does not touch the override layer, so the patch still shape the
	// refreshed entries.
	if got := codexClientModelStateValue(t, state, "gpt-5.6-sol", "display_name"); got != "From File" {
		t.Fatalf("display_name = %v, want the override to stay applied", got)
	}
}

func TestCodexClientModelsInheritRemovedSourceAncestor(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"model_messages": map[string]any{
				"guardian_v2": map[string]any{"enabled": true},
				"deep":        map[string]any{"a": map[string]any{"b": "kept"}},
			},
		}),
		testCodexClientModel("gpt-5.6-sol", 2),
	)

	removed := []struct {
		name  string
		patch map[string]any
		leaf  string
	}{
		{
			name:  "null subtree",
			patch: map[string]any{"model_messages": map[string]any{"guardian_v2": nil}},
			leaf:  "model_messages.guardian_v2.enabled",
		},
		{
			name:  "replaced ancestor",
			patch: map[string]any{"model_messages": "replaced"},
			leaf:  "model_messages.guardian_v2.enabled",
		},
		{
			name:  "null grandparent",
			patch: map[string]any{"model_messages": map[string]any{"deep": map[string]any{"a": nil}}},
			leaf:  "model_messages.deep.a.b",
		},
	}
	for _, tc := range removed {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyOverrideForTest(t, base, map[string]any{
				"gpt-5.5": tc.patch,
				"gpt-5.6-sol": map[string]any{
					"$inherit": map[string]any{tc.leaf: "gpt-5.5"},
				},
			})
			if err == nil {
				t.Fatalf("inheriting %s from a source that removed it succeeded, want the source reported as missing", tc.leaf)
			}
			if !strings.Contains(err.Error(), "does not define") {
				t.Fatalf("error = %v, want a missing source value error", err)
			}
		})
	}

	// A removal elsewhere in the source must not stop other fields from inheriting.
	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5": map[string]any{"model_messages": map[string]any{"guardian_v2": nil}},
		"gpt-5.6-sol": map[string]any{
			"$inherit": map[string]any{"model_messages.deep.a.b": "gpt-5.5"},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}
	messages, _ := models["gpt-5.6-sol"]["model_messages"].(map[string]any)
	deep, _ := messages["deep"].(map[string]any)
	nested, _ := deep["a"].(map[string]any)
	if nested["b"] != "kept" {
		t.Fatalf("model_messages.deep.a.b = %v, want the untouched inherited value", nested["b"])
	}
}
