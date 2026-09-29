package common

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	common2 "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyParamOverrideTrimPrefix(t *testing.T) {
	// trim_prefix example:
	// {"operations":[{"path":"model","mode":"trim_prefix","value":"openai/"}]}
	input := []byte(`{"model":"openai/gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "trim_prefix",
				"value": "openai/",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideTrimSuffix(t *testing.T) {
	// trim_suffix example:
	// {"operations":[{"path":"model","mode":"trim_suffix","value":"-latest"}]}
	input := []byte(`{"model":"gpt-4-latest","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "trim_suffix",
				"value": "-latest",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideTrimNoop(t *testing.T) {
	// trim_prefix no-op example:
	// {"operations":[{"path":"model","mode":"trim_prefix","value":"openai/"}]}
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "trim_prefix",
				"value": "openai/",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideMixedLegacyAndOperations(t *testing.T) {
	input := []byte(`{"model":"openai/gpt-4","temperature":0.7}`)
	override := map[string]any{
		"temperature": 0.2,
		"top_p":       0.95,
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "trim_prefix",
				"value": "openai/",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.2,"top_p":0.95}`, string(out))
}

func TestApplyParamOverrideMixedLegacyAndOperationsConflictPrefersOperations(t *testing.T) {
	input := []byte(`{"model":"openai/gpt-4","temperature":0.7}`)
	override := map[string]any{
		"model":       "legacy-model",
		"temperature": 0.2,
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "set",
				"value": "op-model",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"op-model","temperature":0.2}`, string(out))
}

func TestApplyParamOverrideTrimRequiresValue(t *testing.T) {
	// trim_prefix requires value example:
	// {"operations":[{"path":"model","mode":"trim_prefix"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "trim_prefix",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideReplace(t *testing.T) {
	// replace example:
	// {"operations":[{"path":"model","mode":"replace","from":"openai/","to":""}]}
	input := []byte(`{"model":"openai/gpt-4o-mini","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "replace",
				"from": "openai/",
				"to":   "",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4o-mini","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideRegexReplace(t *testing.T) {
	// regex_replace example:
	// {"operations":[{"path":"model","mode":"regex_replace","from":"^gpt-","to":"openai/gpt-"}]}
	input := []byte(`{"model":"gpt-4o-mini","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "regex_replace",
				"from": "^gpt-",
				"to":   "openai/gpt-",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"openai/gpt-4o-mini","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideReplaceRequiresFrom(t *testing.T) {
	// replace requires from example:
	// {"operations":[{"path":"model","mode":"replace"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "replace",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideRegexReplaceRequiresPattern(t *testing.T) {
	// regex_replace requires from(pattern) example:
	// {"operations":[{"path":"model","mode":"regex_replace"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "regex_replace",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideDelete(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "temperature",
				"mode": "delete",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("failed to unmarshal output JSON: %v", err)
	}
	if _, exists := got["temperature"]; exists {
		t.Fatalf("expected temperature to be deleted")
	}
}

func TestApplyParamOverrideDeleteWildcardPath(t *testing.T) {
	input := []byte(`{"tools":[{"type":"bash","custom":{"input_examples":["a"],"other":1}},{"type":"code","custom":{"input_examples":["b"]}},{"type":"noop","custom":{"other":2}}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "tools.*.custom.input_examples",
				"mode": "delete",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"tools":[{"type":"bash","custom":{"other":1}},{"type":"code","custom":{}},{"type":"noop","custom":{"other":2}}]}`, string(out))
}

func TestApplyParamOverrideSetWildcardPath(t *testing.T) {
	input := []byte(`{"tools":[{"custom":{"tag":"A"}},{"custom":{"tag":"B"}},{"custom":{"tag":"C"}}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "tools.*.custom.enabled",
				"mode":  "set",
				"value": true,
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	var got struct {
		Tools []struct {
			Custom struct {
				Enabled bool `json:"enabled"`
			} `json:"custom"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("failed to unmarshal output JSON: %v", err)
	}

	if !lo.EveryBy(got.Tools, func(item struct {
		Custom struct {
			Enabled bool `json:"enabled"`
		} `json:"custom"`
	}) bool {
		return item.Custom.Enabled
	}) {
		t.Fatalf("expected wildcard set to enable all tools, got: %s", string(out))
	}
}

func TestApplyParamOverrideTrimSpaceWildcardPath(t *testing.T) {
	input := []byte(`{"tools":[{"custom":{"name":" alpha "}},{"custom":{"name":" beta"}},{"custom":{"name":"gamma "}}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "tools.*.custom.name",
				"mode": "trim_space",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	var got struct {
		Tools []struct {
			Custom struct {
				Name string `json:"name"`
			} `json:"custom"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("failed to unmarshal output JSON: %v", err)
	}

	names := lo.Map(got.Tools, func(item struct {
		Custom struct {
			Name string `json:"name"`
		} `json:"custom"`
	}, _ int) string {
		return item.Custom.Name
	})
	if !reflect.DeepEqual(names, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("unexpected names after wildcard trim_space: %v", names)
	}
}

func TestApplyParamOverrideDeleteWildcardEqualsIndexedPaths(t *testing.T) {
	input := []byte(`{"tools":[{"custom":{"input_examples":["a"],"other":1}},{"custom":{"input_examples":["b"],"other":2}},{"custom":{"input_examples":["c"],"other":3}}]}`)

	wildcardOverride := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "tools.*.custom.input_examples",
				"mode": "delete",
			},
		},
	}

	indexedOverride := map[string]any{
		"operations": lo.Map(lo.Range(3), func(index int, _ int) any {
			return map[string]any{
				"path": fmt.Sprintf("tools.%d.custom.input_examples", index),
				"mode": "delete",
			}
		}),
	}

	wildcardOut, err := ApplyParamOverride(input, wildcardOverride, nil)
	if err != nil {
		t.Fatalf("wildcard ApplyParamOverride returned error: %v", err)
	}

	indexedOut, err := ApplyParamOverride(input, indexedOverride, nil)
	if err != nil {
		t.Fatalf("indexed ApplyParamOverride returned error: %v", err)
	}

	assertJSONEqual(t, string(indexedOut), string(wildcardOut))
}

func TestApplyParamOverrideSetWildcardKeepOrigin(t *testing.T) {
	input := []byte(`{"tools":[{"custom":{"tag":"A"}},{"custom":{"tag":"B","enabled":false}},{"custom":{"tag":"C"}}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":        "tools.*.custom.enabled",
				"mode":        "set",
				"value":       true,
				"keep_origin": true,
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	var got struct {
		Tools []struct {
			Custom struct {
				Enabled bool `json:"enabled"`
			} `json:"custom"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("failed to unmarshal output JSON: %v", err)
	}

	enabledValues := lo.Map(got.Tools, func(item struct {
		Custom struct {
			Enabled bool `json:"enabled"`
		} `json:"custom"`
	}, _ int) bool {
		return item.Custom.Enabled
	})
	if !reflect.DeepEqual(enabledValues, []bool{true, false, true}) {
		t.Fatalf("unexpected enabled values after wildcard keep_origin set: %v", enabledValues)
	}
}

func TestApplyParamOverrideTrimSpaceMultiWildcardPath(t *testing.T) {
	input := []byte(`{"tools":[{"custom":{"items":[{"name":" alpha "},{"name":" beta "}]}},{"custom":{"items":[{"name":" gamma"}]}}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "tools.*.custom.items.*.name",
				"mode": "trim_space",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	var got struct {
		Tools []struct {
			Custom struct {
				Items []struct {
					Name string `json:"name"`
				} `json:"items"`
			} `json:"custom"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("failed to unmarshal output JSON: %v", err)
	}

	names := lo.FlatMap(got.Tools, func(tool struct {
		Custom struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		} `json:"custom"`
	}, _ int) []string {
		return lo.Map(tool.Custom.Items, func(item struct {
			Name string `json:"name"`
		}, _ int) string {
			return item.Name
		})
	})
	if !reflect.DeepEqual(names, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("unexpected names after multi wildcard trim_space: %v", names)
	}
}

func TestApplyParamOverrideSet(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.1}`, string(out))
}

func TestApplyParamOverrideSetWithDescriptionKeepsCompatibility(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	overrideWithoutDesc := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
			},
		},
	}
	overrideWithDesc := map[string]any{
		"operations": []any{
			map[string]any{
				"description": "set temperature for deterministic output",
				"path":        "temperature",
				"mode":        "set",
				"value":       0.1,
			},
		},
	}

	outWithoutDesc, err := ApplyParamOverride(input, overrideWithoutDesc, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride without description returned error: %v", err)
	}

	outWithDesc, err := ApplyParamOverride(input, overrideWithDesc, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride with description returned error: %v", err)
	}

	assertJSONEqual(t, string(outWithoutDesc), string(outWithDesc))
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.1}`, string(outWithDesc))
}

func TestApplyParamOverrideSetKeepOrigin(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":        "temperature",
				"mode":        "set",
				"value":       0.1,
				"keep_origin": true,
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideMove(t *testing.T) {
	input := []byte(`{"model":"gpt-4","meta":{"x":1}}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "move",
				"from": "model",
				"to":   "meta.model",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"meta":{"x":1,"model":"gpt-4"}}`, string(out))
}

func TestApplyParamOverrideMoveMissingSource(t *testing.T) {
	input := []byte(`{"meta":{"x":1}}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "move",
				"from": "model",
				"to":   "meta.model",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverridePrependAppendString(t *testing.T) {
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "prepend",
				"value": "openai/",
			},
			map[string]any{
				"path":  "model",
				"mode":  "append",
				"value": "-latest",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"openai/gpt-4-latest"}`, string(out))
}

func TestApplyParamOverridePrependAppendArray(t *testing.T) {
	input := []byte(`{"arr":[1,2]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "arr",
				"mode":  "prepend",
				"value": 0,
			},
			map[string]any{
				"path":  "arr",
				"mode":  "append",
				"value": []any{3, 4},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"arr":[0,1,2,3,4]}`, string(out))
}

func TestApplyParamOverrideAppendObjectMergeKeepOrigin(t *testing.T) {
	input := []byte(`{"obj":{"a":1}}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":        "obj",
				"mode":        "append",
				"keep_origin": true,
				"value": map[string]any{
					"a": 2,
					"b": 3,
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"obj":{"a":1,"b":3}}`, string(out))
}

func TestApplyParamOverrideAppendObjectMergeOverride(t *testing.T) {
	input := []byte(`{"obj":{"a":1}}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "obj",
				"mode": "append",
				"value": map[string]any{
					"a": 2,
					"b": 3,
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"obj":{"a":2,"b":3}}`, string(out))
}

func TestApplyParamOverrideConditionORDefault(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":  "model",
						"mode":  "prefix",
						"value": "gpt",
					},
					map[string]any{
						"path":  "model",
						"mode":  "prefix",
						"value": "claude",
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.1}`, string(out))
}

func TestApplyParamOverrideConditionAND(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"logic": "AND",
				"conditions": []any{
					map[string]any{
						"path":  "model",
						"mode":  "prefix",
						"value": "gpt",
					},
					map[string]any{
						"path":  "temperature",
						"mode":  "gt",
						"value": 0.5,
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.1}`, string(out))
}

func TestApplyParamOverrideConditionInvert(t *testing.T) {
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":   "model",
						"mode":   "prefix",
						"value":  "gpt",
						"invert": true,
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideConditionPassMissingKey(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":             "model",
						"mode":             "prefix",
						"value":            "gpt",
						"pass_missing_key": true,
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverrideConditionFromContext(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":  "model",
						"mode":  "prefix",
						"value": "gpt",
					},
				},
			},
		},
	}
	ctx := map[string]any{
		"model": "gpt-4",
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverrideNegativeIndexPath(t *testing.T) {
	input := []byte(`{"arr":[{"model":"a"},{"model":"b"}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "arr.-1.model",
				"mode":  "set",
				"value": "c",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"arr":[{"model":"a"},{"model":"c"}]}`, string(out))
}

func TestApplyParamOverrideRegexReplaceInvalidPattern(t *testing.T) {
	// regex_replace invalid pattern example:
	// {"operations":[{"path":"model","mode":"regex_replace","from":"(","to":"x"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "regex_replace",
				"from": "(",
				"to":   "x",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideCopy(t *testing.T) {
	// copy example:
	// {"operations":[{"mode":"copy","from":"model","to":"original_model"}]}
	input := []byte(`{"model":"gpt-4","temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "copy",
				"from": "model",
				"to":   "original_model",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","original_model":"gpt-4","temperature":0.7}`, string(out))
}

func TestApplyParamOverrideCopyMissingSource(t *testing.T) {
	// copy missing source example:
	// {"operations":[{"mode":"copy","from":"model","to":"original_model"}]}
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "copy",
				"from": "model",
				"to":   "original_model",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideCopyRequiresFromTo(t *testing.T) {
	// copy requires from/to example:
	// {"operations":[{"mode":"copy"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "copy",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideEnsurePrefix(t *testing.T) {
	// ensure_prefix example:
	// {"operations":[{"path":"model","mode":"ensure_prefix","value":"openai/"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "ensure_prefix",
				"value": "openai/",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"openai/gpt-4"}`, string(out))
}

func TestApplyParamOverrideEnsurePrefixNoop(t *testing.T) {
	// ensure_prefix no-op example:
	// {"operations":[{"path":"model","mode":"ensure_prefix","value":"openai/"}]}
	input := []byte(`{"model":"openai/gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "ensure_prefix",
				"value": "openai/",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"openai/gpt-4"}`, string(out))
}

func TestApplyParamOverrideEnsureSuffix(t *testing.T) {
	// ensure_suffix example:
	// {"operations":[{"path":"model","mode":"ensure_suffix","value":"-latest"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "ensure_suffix",
				"value": "-latest",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4-latest"}`, string(out))
}

func TestApplyParamOverrideEnsureSuffixNoop(t *testing.T) {
	// ensure_suffix no-op example:
	// {"operations":[{"path":"model","mode":"ensure_suffix","value":"-latest"}]}
	input := []byte(`{"model":"gpt-4-latest"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "model",
				"mode":  "ensure_suffix",
				"value": "-latest",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4-latest"}`, string(out))
}

func TestApplyParamOverrideEnsureRequiresValue(t *testing.T) {
	// ensure_prefix requires value example:
	// {"operations":[{"path":"model","mode":"ensure_prefix"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "ensure_prefix",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideTrimSpace(t *testing.T) {
	// trim_space example:
	// {"operations":[{"path":"model","mode":"trim_space"}]}
	input := []byte("{\"model\":\"  gpt-4 \\n\"}")
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "trim_space",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4"}`, string(out))
}

func TestApplyParamOverrideToLower(t *testing.T) {
	// to_lower example:
	// {"operations":[{"path":"model","mode":"to_lower"}]}
	input := []byte(`{"model":"GPT-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "to_lower",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4"}`, string(out))
}

func TestApplyParamOverrideToUpper(t *testing.T) {
	// to_upper example:
	// {"operations":[{"path":"model","mode":"to_upper"}]}
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "model",
				"mode": "to_upper",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"GPT-4"}`, string(out))
}

func TestApplyParamOverrideReturnError(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "return_error",
				"value": map[string]any{
					"message":     "forced bad request by param override",
					"status_code": 422,
					"code":        "forced_bad_request",
					"type":        "invalid_request_error",
					"skip_retry":  true,
				},
				"conditions": []any{
					map[string]any{
						"path":  "retry.is_retry",
						"mode":  "full",
						"value": true,
					},
				},
			},
		},
	}
	ctx := map[string]any{
		"retry": map[string]any{
			"index":    1,
			"is_retry": true,
		},
	}

	_, err := ApplyParamOverride(input, override, ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	returnErr, ok := AsParamOverrideReturnError(err)
	if !ok {
		t.Fatalf("expected ParamOverrideReturnError, got %T: %v", err, err)
	}
	if returnErr.StatusCode != 422 {
		t.Fatalf("expected status 422, got %d", returnErr.StatusCode)
	}
	if returnErr.Code != "forced_bad_request" {
		t.Fatalf("expected code forced_bad_request, got %s", returnErr.Code)
	}
	if !returnErr.SkipRetry {
		t.Fatalf("expected skip_retry true")
	}
}

func TestApplyParamOverridePruneObjectsByTypeString(t *testing.T) {
	input := []byte(`{
		"messages":[
			{"role":"assistant","content":[
				{"type":"output_text","text":"a"},
				{"type":"redacted_thinking","text":"secret"},
				{"type":"tool_call","name":"tool_a"}
			]},
			{"role":"assistant","content":[
				{"type":"output_text","text":"b"},
				{"type":"wrapper","parts":[
					{"type":"redacted_thinking","text":"secret2"},
					{"type":"output_text","text":"c"}
				]}
			]}
		]
	}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode":  "prune_objects",
				"value": "redacted_thinking",
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{
		"messages":[
			{"role":"assistant","content":[
				{"type":"output_text","text":"a"},
				{"type":"tool_call","name":"tool_a"}
			]},
			{"role":"assistant","content":[
				{"type":"output_text","text":"b"},
				{"type":"wrapper","parts":[
					{"type":"output_text","text":"c"}
				]}
			]}
		]
	}`, string(out))
}

func TestApplyParamOverridePruneObjectsWhereAndPath(t *testing.T) {
	input := []byte(`{
		"a":{"items":[{"type":"redacted_thinking","id":1},{"type":"output_text","id":2}]},
		"b":{"items":[{"type":"redacted_thinking","id":3},{"type":"output_text","id":4}]}
	}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path": "a",
				"mode": "prune_objects",
				"value": map[string]any{
					"where": map[string]any{
						"type": "redacted_thinking",
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{
		"a":{"items":[{"type":"output_text","id":2}]},
		"b":{"items":[{"type":"redacted_thinking","id":3},{"type":"output_text","id":4}]}
	}`, string(out))
}

func TestApplyParamOverrideNormalizeThinkingSignatureUnsupported(t *testing.T) {
	input := []byte(`{"items":[{"type":"redacted_thinking"}]}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "normalize_thinking_signature",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideConditionFromRetryAndLastErrorContext(t *testing.T) {
	info := &RelayInfo{
		RetryIndex: 1,
		LastError: types.WithOpenAIError(types.OpenAIError{
			Message: "invalid thinking signature",
			Type:    "invalid_request_error",
			Code:    "bad_thought_signature",
		}, 400),
	}
	ctx := BuildParamOverrideContext(info)

	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"logic": "AND",
				"conditions": []any{
					map[string]any{
						"path":  "is_retry",
						"mode":  "full",
						"value": true,
					},
					map[string]any{
						"path":  "last_error.code",
						"mode":  "contains",
						"value": "thought_signature",
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverrideConditionByUserAndGPTModel(t *testing.T) {
	paramOverride := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "service_tier",
				"mode":  "set",
				"value": "priority",
				"logic": "AND",
				"conditions": []any{
					map[string]any{
						"path":  "user_id",
						"mode":  "full",
						"value": 1,
					},
					map[string]any{
						"path":  "upstream_model",
						"mode":  "contains",
						"value": "gpt",
					},
				},
			},
		},
	}

	tests := []struct {
		name     string
		userID   int
		model    string
		expected string
	}{
		{
			name:     "target user and GPT model",
			userID:   1,
			model:    "gpt-5.2",
			expected: `{"model":"gpt-5.2","service_tier":"priority"}`,
		},
		{
			name:     "other user",
			userID:   2,
			model:    "gpt-5.2",
			expected: `{"model":"gpt-5.2"}`,
		},
		{
			name:     "non-GPT model",
			userID:   1,
			model:    "claude-sonnet-4-5",
			expected: `{"model":"claude-sonnet-4-5"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &RelayInfo{
				UserId: tt.userID,
				ChannelMeta: &ChannelMeta{
					ParamOverride:     paramOverride,
					UpstreamModelName: tt.model,
				},
			}
			input := []byte(fmt.Sprintf(`{"model":%q}`, tt.model))

			out, err := ApplyParamOverrideWithRelayInfo(input, info)

			require.NoError(t, err)
			require.JSONEq(t, tt.expected, string(out))
		})
	}
}

func TestApplyParamOverrideConditionByGroupContext(t *testing.T) {
	info := &RelayInfo{
		UserGroup:  "vip",
		TokenGroup: "premium",
		UsingGroup: "priority-route",
	}
	ctx := BuildParamOverrideContext(info)
	paramOverride := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "service_tier",
				"mode":  "set",
				"value": "priority",
				"logic": "AND",
				"conditions": []any{
					map[string]any{
						"path":  "user_group",
						"mode":  "full",
						"value": "vip",
					},
					map[string]any{
						"path":  "token_group",
						"mode":  "full",
						"value": "premium",
					},
					map[string]any{
						"path":  "using_group",
						"mode":  "full",
						"value": "priority-route",
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride([]byte(`{"model":"gpt-5.2"}`), paramOverride, ctx)

	require.NoError(t, err)
	require.JSONEq(t, `{"model":"gpt-5.2","service_tier":"priority"}`, string(out))
}

func TestApplyParamOverrideConditionFromRequestHeaders(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":  "request_headers.authorization",
						"mode":  "contains",
						"value": "Bearer ",
					},
				},
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"authorization": "Bearer token-123",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverrideSetHeaderAndUseInLaterCondition(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode":  "set_header",
				"path":  "X-Debug-Mode",
				"value": "enabled",
			},
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":  "header_override.x-debug-mode",
						"mode":  "full",
						"value": "enabled",
					},
				},
			},
		},
	}

	out, err := ApplyParamOverride(input, override, nil)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverrideCopyHeaderFromRequestHeaders(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "copy_header",
				"from": "Authorization",
				"to":   "X-Upstream-Auth",
			},
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"conditions": []any{
					map[string]any{
						"path":  "header_override.x-upstream-auth",
						"mode":  "contains",
						"value": "Bearer ",
					},
				},
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"authorization": "Bearer token-123",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverridePassHeadersSkipsMissingHeaders(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode":  "pass_headers",
				"value": []any{"X-Codex-Beta-Features", "Session_id"},
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"session_id": "sess-123",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["session_id"] != "sess-123" {
		t.Fatalf("expected session_id to be passed, got: %v", headers["session_id"])
	}
	if _, exists := headers["x-codex-beta-features"]; exists {
		t.Fatalf("expected missing header to be skipped")
	}
}

func TestApplyParamOverrideCopyHeaderSkipsMissingSource(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "copy_header",
				"from": "X-Missing-Header",
				"to":   "X-Upstream-Auth",
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"authorization": "Bearer token-123",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := headers["x-upstream-auth"]; exists {
		t.Fatalf("expected X-Upstream-Auth to be skipped when source header is missing")
	}
}

func TestApplyParamOverrideMoveHeaderSkipsMissingSource(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "move_header",
				"from": "X-Missing-Header",
				"to":   "X-Upstream-Auth",
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"authorization": "Bearer token-123",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		return
	}
	if _, exists := headers["x-upstream-auth"]; exists {
		t.Fatalf("expected X-Upstream-Auth to be skipped when source header is missing")
	}
}

func TestApplyParamOverrideSyncFieldsHeaderToJSON(t *testing.T) {
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "sync_fields",
				"from": "header:session_id",
				"to":   "json:prompt_cache_key",
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"session_id": "sess-123",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","prompt_cache_key":"sess-123"}`, string(out))
}

func TestApplyParamOverrideSyncFieldsJSONToHeader(t *testing.T) {
	input := []byte(`{"model":"gpt-4","prompt_cache_key":"cache-abc"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "sync_fields",
				"from": "header:session_id",
				"to":   "json:prompt_cache_key",
			},
		},
	}
	ctx := map[string]any{}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","prompt_cache_key":"cache-abc"}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["session_id"] != "cache-abc" {
		t.Fatalf("expected session_id to be synced from prompt_cache_key, got: %v", headers["session_id"])
	}
}

func TestApplyParamOverrideSyncFieldsNoChangeWhenBothExist(t *testing.T) {
	input := []byte(`{"model":"gpt-4","prompt_cache_key":"cache-body"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "sync_fields",
				"from": "header:session_id",
				"to":   "json:prompt_cache_key",
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"session_id": "cache-header",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-4","prompt_cache_key":"cache-body"}`, string(out))

	headers, _ := ctx["header_override"].(map[string]any)
	if headers != nil {
		if _, exists := headers["session_id"]; exists {
			t.Fatalf("expected no override when both sides already have value")
		}
	}
}

func TestApplyParamOverrideSyncFieldsInvalidTarget(t *testing.T) {
	input := []byte(`{"model":"gpt-4"}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "sync_fields",
				"from": "foo:session_id",
				"to":   "json:prompt_cache_key",
			},
		},
	}

	_, err := ApplyParamOverride(input, override, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestApplyParamOverrideSetHeaderKeepOrigin(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode":        "set_header",
				"path":        "X-Feature-Flag",
				"value":       "new-value",
				"keep_origin": true,
			},
		},
	}
	ctx := map[string]any{
		"header_override": map[string]any{
			"x-feature-flag": "legacy-value",
		},
	}

	_, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["x-feature-flag"] != "legacy-value" {
		t.Fatalf("expected keep_origin to preserve old value, got: %v", headers["x-feature-flag"])
	}
}

func TestApplyParamOverrideSetHeaderMapRewritesCommaSeparatedHeader(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "set_header",
				"path": "anthropic-beta",
				"value": map[string]any{
					"advanced-tool-use-2025-11-20": nil,
					"computer-use-2025-01-24":      "computer-use-2025-01-24",
				},
			},
		},
	}
	ctx := map[string]any{
		"request_headers": map[string]any{
			"anthropic-beta": "advanced-tool-use-2025-11-20, computer-use-2025-01-24",
		},
	}

	_, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["anthropic-beta"] != "computer-use-2025-01-24" {
		t.Fatalf("expected anthropic-beta to keep only mapped value, got: %v", headers["anthropic-beta"])
	}
}

func TestApplyParamOverrideSetHeaderMapDeleteWholeHeaderWhenAllTokensCleared(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "set_header",
				"path": "anthropic-beta",
				"value": map[string]any{
					"advanced-tool-use-2025-11-20": nil,
					"computer-use-2025-01-24":      nil,
				},
			},
		},
	}
	ctx := map[string]any{
		"header_override": map[string]any{
			"anthropic-beta": "advanced-tool-use-2025-11-20,computer-use-2025-01-24",
		},
	}

	_, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if _, exists := headers["anthropic-beta"]; exists {
		t.Fatalf("expected anthropic-beta to be deleted when all mapped values are null")
	}
}

func TestApplyParamOverrideSetHeaderMapAppendsTokens(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "set_header",
				"path": "anthropic-beta",
				"value": map[string]any{
					"$append": []any{"context-1m-2025-08-07", "computer-use-2025-01-24"},
				},
			},
		},
	}
	ctx := map[string]any{
		"header_override": map[string]any{
			"anthropic-beta": "computer-use-2025-01-24",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["anthropic-beta"] != "computer-use-2025-01-24,context-1m-2025-08-07" {
		t.Fatalf("expected anthropic-beta to append new token without duplicates, got: %v", headers["anthropic-beta"])
	}
}

func TestApplyParamOverrideSetHeaderMapAppendsTokensWhenHeaderMissing(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "set_header",
				"path": "anthropic-beta",
				"value": map[string]any{
					"$append": []any{"context-1m-2025-08-07", "computer-use-2025-01-24"},
				},
			},
		},
	}

	ctx := map[string]any{}
	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["anthropic-beta"] != "context-1m-2025-08-07,computer-use-2025-01-24" {
		t.Fatalf("expected anthropic-beta to be created from appended tokens, got: %v", headers["anthropic-beta"])
	}
}

func TestApplyParamOverrideSetHeaderMapKeepOnlyDeclaredDropsUndeclaredTokens(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "set_header",
				"path": "anthropic-beta",
				"value": map[string]any{
					"computer-use-2025-01-24": "computer-use-2025-01-24",
					"$append":                 []any{"context-1m-2025-08-07"},
					"$keep_only_declared":     true,
				},
			},
		},
	}
	ctx := map[string]any{
		"header_override": map[string]any{
			"anthropic-beta": "advanced-tool-use-2025-11-20,computer-use-2025-01-24",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if headers["anthropic-beta"] != "computer-use-2025-01-24,context-1m-2025-08-07" {
		t.Fatalf("expected anthropic-beta to keep only declared tokens, got: %v", headers["anthropic-beta"])
	}
}

func TestApplyParamOverrideSetHeaderMapKeepOnlyDeclaredDeletesHeaderWhenNothingDeclaredMatches(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "set_header",
				"path": "anthropic-beta",
				"value": map[string]any{
					"computer-use-2025-01-24": "computer-use-2025-01-24",
					"$keep_only_declared":     true,
				},
			},
		},
	}
	ctx := map[string]any{
		"header_override": map[string]any{
			"anthropic-beta": "advanced-tool-use-2025-11-20",
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	headers, ok := ctx["header_override"].(map[string]any)
	if !ok {
		t.Fatalf("expected header_override context map")
	}
	if _, exists := headers["anthropic-beta"]; exists {
		t.Fatalf("expected anthropic-beta to be deleted when no declared tokens remain, got: %v", headers["anthropic-beta"])
	}
}

func TestApplyParamOverrideConditionsObjectShorthand(t *testing.T) {
	input := []byte(`{"temperature":0.7}`)
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"path":  "temperature",
				"mode":  "set",
				"value": 0.1,
				"logic": "AND",
				"conditions": map[string]any{
					"is_retry":               true,
					"last_error.status_code": 400.0,
				},
			},
		},
	}
	ctx := map[string]any{
		"is_retry": true,
		"last_error": map[string]any{
			"status_code": 400.0,
		},
	}

	out, err := ApplyParamOverride(input, override, ctx)
	if err != nil {
		t.Fatalf("ApplyParamOverride returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.1}`, string(out))
}

func TestApplyParamOverrideWithRelayInfoSyncRuntimeHeaders(t *testing.T) {
	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode":  "set_header",
						"path":  "X-Injected-By-Param-Override",
						"value": "enabled",
					},
					map[string]any{
						"mode": "delete_header",
						"path": "X-Delete-Me",
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Delete-Me": "legacy",
				"X-Keep-Me":   "keep",
			},
		},
	}

	input := []byte(`{"temperature":0.7}`)
	out, err := ApplyParamOverrideWithRelayInfo(input, info)
	if err != nil {
		t.Fatalf("ApplyParamOverrideWithRelayInfo returned error: %v", err)
	}
	assertJSONEqual(t, `{"temperature":0.7}`, string(out))

	if !info.UseRuntimeHeadersOverride {
		t.Fatalf("expected runtime header override to be enabled")
	}
	if info.RuntimeHeadersOverride["x-keep-me"] != "keep" {
		t.Fatalf("expected x-keep-me header to be preserved, got: %v", info.RuntimeHeadersOverride["x-keep-me"])
	}
	if info.RuntimeHeadersOverride["x-injected-by-param-override"] != "enabled" {
		t.Fatalf("expected x-injected-by-param-override header to be set, got: %v", info.RuntimeHeadersOverride["x-injected-by-param-override"])
	}
	if _, exists := info.RuntimeHeadersOverride["x-delete-me"]; exists {
		t.Fatalf("expected x-delete-me header to be deleted")
	}
}

func TestApplyParamOverrideWithRelayInfoMixedLegacyAndOperations(t *testing.T) {
	info := &RelayInfo{
		RequestHeaders: map[string]string{
			"Originator": "Codex CLI",
		},
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"temperature": 0.2,
				"operations": []any{
					map[string]any{
						"mode":  "pass_headers",
						"value": []any{"Originator"},
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Static": "legacy-static",
			},
		},
	}

	out, err := ApplyParamOverrideWithRelayInfo([]byte(`{"model":"gpt-5","temperature":0.7}`), info)
	if err != nil {
		t.Fatalf("ApplyParamOverrideWithRelayInfo returned error: %v", err)
	}
	assertJSONEqual(t, `{"model":"gpt-5","temperature":0.2}`, string(out))

	if !info.UseRuntimeHeadersOverride {
		t.Fatalf("expected runtime header override to be enabled")
	}
	if info.RuntimeHeadersOverride["x-static"] != "legacy-static" {
		t.Fatalf("expected x-static to be preserved, got: %v", info.RuntimeHeadersOverride["x-static"])
	}
	if info.RuntimeHeadersOverride["originator"] != "Codex CLI" {
		t.Fatalf("expected originator header to be passed, got: %v", info.RuntimeHeadersOverride["originator"])
	}
}

func TestApplyParamOverrideWithRelayInfoMoveAndCopyHeaders(t *testing.T) {
	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode": "move_header",
						"from": "X-Legacy-Trace",
						"to":   "X-Trace",
					},
					map[string]any{
						"mode": "copy_header",
						"from": "X-Trace",
						"to":   "X-Trace-Backup",
					},
				},
			},
			HeadersOverride: map[string]any{
				"X-Legacy-Trace": "trace-123",
			},
		},
	}

	input := []byte(`{"temperature":0.7}`)
	_, err := ApplyParamOverrideWithRelayInfo(input, info)
	if err != nil {
		t.Fatalf("ApplyParamOverrideWithRelayInfo returned error: %v", err)
	}
	if _, exists := info.RuntimeHeadersOverride["x-legacy-trace"]; exists {
		t.Fatalf("expected source header to be removed after move")
	}
	if info.RuntimeHeadersOverride["x-trace"] != "trace-123" {
		t.Fatalf("expected x-trace to be set, got: %v", info.RuntimeHeadersOverride["x-trace"])
	}
	if info.RuntimeHeadersOverride["x-trace-backup"] != "trace-123" {
		t.Fatalf("expected x-trace-backup to be copied, got: %v", info.RuntimeHeadersOverride["x-trace-backup"])
	}
}

func TestApplyParamOverrideWithRelayInfoSetHeaderMapRewritesAnthropicBeta(t *testing.T) {
	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode": "set_header",
						"path": "anthropic-beta",
						"value": map[string]any{
							"advanced-tool-use-2025-11-20": nil,
							"computer-use-2025-01-24":      "computer-use-2025-01-24",
						},
					},
				},
			},
			HeadersOverride: map[string]any{
				"anthropic-beta": "advanced-tool-use-2025-11-20, computer-use-2025-01-24",
			},
		},
	}

	_, err := ApplyParamOverrideWithRelayInfo([]byte(`{"temperature":0.7}`), info)
	if err != nil {
		t.Fatalf("ApplyParamOverrideWithRelayInfo returned error: %v", err)
	}

	if !info.UseRuntimeHeadersOverride {
		t.Fatalf("expected runtime header override to be enabled")
	}
	if info.RuntimeHeadersOverride["anthropic-beta"] != "computer-use-2025-01-24" {
		t.Fatalf("expected anthropic-beta to be rewritten, got: %v", info.RuntimeHeadersOverride["anthropic-beta"])
	}
}

func TestGetEffectiveHeaderOverrideUsesRuntimeOverrideAsFinalResult(t *testing.T) {
	info := &RelayInfo{
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]any{
			"x-runtime": "runtime-only",
		},
		ChannelMeta: &ChannelMeta{
			HeadersOverride: map[string]any{
				"X-Static":  "static-value",
				"X-Deleted": "should-not-exist",
			},
		},
	}

	effective := GetEffectiveHeaderOverride(info)
	if effective["x-runtime"] != "runtime-only" {
		t.Fatalf("expected x-runtime from runtime override, got: %v", effective["x-runtime"])
	}
	if _, exists := effective["x-static"]; exists {
		t.Fatalf("expected runtime override to be final and not merge channel headers")
	}
}

func TestRemoveDisabledFieldsSkipWhenChannelPassThroughEnabled(t *testing.T) {
	input := `{
		"service_tier":"flex",
		"safety_identifier":"user-123",
		"store":true,
		"stream_options":{"include_obfuscation":false}
	}`
	settings := dto.ChannelOtherSettings{}

	out, err := RemoveDisabledFields([]byte(input), settings, true)
	if err != nil {
		t.Fatalf("RemoveDisabledFields returned error: %v", err)
	}
	assertJSONEqual(t, input, string(out))
}

func TestRemoveDisabledFieldsSkipWhenGlobalPassThroughEnabled(t *testing.T) {
	original := model_setting.GetGlobalSettings().PassThroughRequestEnabled
	model_setting.GetGlobalSettings().PassThroughRequestEnabled = true
	t.Cleanup(func() {
		model_setting.GetGlobalSettings().PassThroughRequestEnabled = original
	})

	input := `{
		"service_tier":"flex",
		"safety_identifier":"user-123",
		"stream_options":{"include_obfuscation":false}
	}`
	settings := dto.ChannelOtherSettings{}

	out, err := RemoveDisabledFields([]byte(input), settings, false)
	if err != nil {
		t.Fatalf("RemoveDisabledFields returned error: %v", err)
	}
	assertJSONEqual(t, input, string(out))
}

func TestRemoveDisabledFieldsDefaultFiltering(t *testing.T) {
	input := `{
		"service_tier":"flex",
		"inference_geo":"eu",
		"speed":"fast",
		"cache_control":{"type":"ephemeral"},
		"safety_identifier":"user-123",
		"store":true,
		"stream_options":{"include_obfuscation":false}
	}`
	settings := dto.ChannelOtherSettings{}

	out, err := RemoveDisabledFields([]byte(input), settings, false)
	if err != nil {
		t.Fatalf("RemoveDisabledFields returned error: %v", err)
	}
	assertJSONEqual(t, `{"cache_control":{"type":"ephemeral"},"store":true}`, string(out))
}

func TestRemoveDisabledFieldsNoControlledFieldsKeepsBody(t *testing.T) {
	input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	settings := dto.ChannelOtherSettings{}

	out, err := RemoveDisabledFields([]byte(input), settings, false)
	if err != nil {
		t.Fatalf("RemoveDisabledFields returned error: %v", err)
	}
	require.Equal(t, input, string(out))
}

func TestRemoveDisabledFieldsAllowInferenceGeo(t *testing.T) {
	input := `{
		"inference_geo":"eu",
		"store":true
	}`
	settings := dto.ChannelOtherSettings{
		AllowInferenceGeo: true,
	}

	out, err := RemoveDisabledFields([]byte(input), settings, false)
	if err != nil {
		t.Fatalf("RemoveDisabledFields returned error: %v", err)
	}
	assertJSONEqual(t, `{"inference_geo":"eu","store":true}`, string(out))
}

func TestRemoveDisabledFieldsAllowSpeed(t *testing.T) {
	input := `{
		"speed":"fast",
		"store":true
	}`
	settings := dto.ChannelOtherSettings{
		AllowSpeed: true,
	}

	out, err := RemoveDisabledFields([]byte(input), settings, false)
	if err != nil {
		t.Fatalf("RemoveDisabledFields returned error: %v", err)
	}
	assertJSONEqual(t, `{"speed":"fast","store":true}`, string(out))
}

func TestApplyParamOverrideWithRelayInfoRecordsOperationAuditInDebugMode(t *testing.T) {
	originalDebugEnabled := common2.DebugEnabled
	common2.DebugEnabled = true
	t.Cleanup(func() {
		common2.DebugEnabled = originalDebugEnabled
	})

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode": "copy",
						"from": "metadata.target_model",
						"to":   "model",
					},
					map[string]any{
						"mode":  "set",
						"path":  "service_tier",
						"value": "flex",
					},
					map[string]any{
						"mode":  "set",
						"path":  "temperature",
						"value": 0.1,
					},
				},
			},
		},
	}

	out, err := ApplyParamOverrideWithRelayInfo([]byte(`{
		"model":"gpt-4.1",
		"temperature":0.7,
		"metadata":{"target_model":"gpt-4.1-mini"}
	}`), info)
	if err != nil {
		t.Fatalf("ApplyParamOverrideWithRelayInfo returned error: %v", err)
	}
	assertJSONEqual(t, `{
		"model":"gpt-4.1-mini",
		"temperature":0.1,
		"service_tier":"flex",
		"metadata":{"target_model":"gpt-4.1-mini"}
	}`, string(out))

	expected := []string{
		"copy metadata.target_model -> model",
		"set service_tier = flex",
		"set temperature = 0.1",
	}
	if !reflect.DeepEqual(info.ParamOverrideAudit, expected) {
		t.Fatalf("unexpected param override audit, got %#v", info.ParamOverrideAudit)
	}
}

func TestApplyParamOverrideWithRelayInfoRecordsOnlyKeyOperationsWhenDebugDisabled(t *testing.T) {
	originalDebugEnabled := common2.DebugEnabled
	common2.DebugEnabled = false
	t.Cleanup(func() {
		common2.DebugEnabled = originalDebugEnabled
	})

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode": "copy",
						"from": "metadata.target_model",
						"to":   "model",
					},
					map[string]any{
						"mode":  "set",
						"path":  "temperature",
						"value": 0.1,
					},
				},
			},
		},
	}

	_, err := ApplyParamOverrideWithRelayInfo([]byte(`{
		"model":"gpt-4.1",
		"temperature":0.7,
		"metadata":{"target_model":"gpt-4.1-mini"}
	}`), info)
	if err != nil {
		t.Fatalf("ApplyParamOverrideWithRelayInfo returned error: %v", err)
	}

	expected := []string{
		"copy metadata.target_model -> model",
	}
	if !reflect.DeepEqual(info.ParamOverrideAudit, expected) {
		t.Fatalf("unexpected param override audit, got %#v", info.ParamOverrideAudit)
	}
}

func TestApplyParamOverrideWithRelayInfoRecordsConversationBodyOperationsWhenDebugDisabled(t *testing.T) {
	originalDebugEnabled := common2.DebugEnabled
	common2.DebugEnabled = false
	t.Cleanup(func() {
		common2.DebugEnabled = originalDebugEnabled
	})

	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ParamOverride: map[string]any{
				"operations": []any{
					map[string]any{
						"mode": "replace",
						"path": "messages.0.content",
						"from": "hello",
						"to":   "hi",
					},
					map[string]any{
						"mode":  "set",
						"path":  "input.0.content.0.text",
						"value": "rewritten response input",
					},
					map[string]any{
						"mode":  "set",
						"path":  "instructions",
						"value": "new instruction",
					},
					map[string]any{
						"mode":  "append",
						"path":  "contents.0.parts",
						"value": map[string]any{"text": "new gemini part"},
					},
					map[string]any{
						"mode": "copy",
						"from": "system",
						"to":   "metadata.system_copy",
					},
					map[string]any{
						"mode":  "set",
						"path":  "temperature",
						"value": 0.1,
					},
				},
			},
		},
	}

	out, err := ApplyParamOverrideWithRelayInfo([]byte(`{
		"messages":[{"role":"user","content":"hello world"}],
		"input":[{"role":"user","content":[{"type":"input_text","text":"original response input"}]}],
		"instructions":"old instruction",
		"system":"old system",
		"contents":[{"role":"user","parts":[{"text":"hello gemini"}]}],
		"temperature":0.7
	}`), info)
	require.NoError(t, err)
	assertJSONEqual(t, `{
		"messages":[{"role":"user","content":"hi world"}],
		"input":[{"role":"user","content":[{"type":"input_text","text":"rewritten response input"}]}],
		"instructions":"new instruction",
		"system":"old system",
		"contents":[{"role":"user","parts":[{"text":"hello gemini"},{"text":"new gemini part"}]}],
		"temperature":0.1,
		"metadata":{"system_copy":"old system"}
	}`, string(out))

	require.Equal(t, []string{
		"replace messages.0.content from hello to hi",
		"set input.0.content.0.text = rewritten response input",
		"set instructions = new instruction",
		"append contents.0.parts with {\"text\":\"new gemini part\"}",
		"copy system -> metadata.system_copy",
	}, info.ParamOverrideAudit)
}

func TestShouldAuditParamPathUsesFieldBoundaryPrefixMatching(t *testing.T) {
	originalDebugEnabled := common2.DebugEnabled
	common2.DebugEnabled = false
	t.Cleanup(func() {
		common2.DebugEnabled = originalDebugEnabled
	})

	require.True(t, shouldAuditParamPath("messages"))
	require.True(t, shouldAuditParamPath("messages.0.content"))
	require.True(t, shouldAuditParamPath("systemInstruction.parts.0.text"))
	require.False(t, shouldAuditParamPath("model_name"))
	require.False(t, shouldAuditParamPath("message"))
}

func assertJSONEqual(t *testing.T, want, got string) {
	t.Helper()

	var wantObj any
	var gotObj any

	if err := json.Unmarshal([]byte(want), &wantObj); err != nil {
		t.Fatalf("failed to unmarshal want JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotObj); err != nil {
		t.Fatalf("failed to unmarshal got JSON: %v", err)
	}

	if !reflect.DeepEqual(wantObj, gotObj) {
		t.Fatalf("json not equal\nwant: %s\ngot:  %s", want, got)
	}
}

func TestApplyParamOverrideWithRelayInfoSynchronizesReasoningEffort(t *testing.T) {
	originalDebugEnabled := common2.DebugEnabled
	common2.DebugEnabled = false
	t.Cleanup(func() {
		common2.DebugEnabled = originalDebugEnabled
	})

	tests := []struct {
		name          string
		relayFormat   types.RelayFormat
		initialEffort string
		input         string
		operation     map[string]any
		expected      string
	}{
		{
			name:          "Responses set",
			relayFormat:   types.RelayFormatOpenAIResponses,
			initialEffort: "high",
			input:         `{"reasoning":{"effort":"high"}}`,
			operation:     map[string]any{"mode": "set", "path": "reasoning.effort", "value": "max"},
			expected:      "max",
		},
		{
			name:          "chat delete",
			relayFormat:   types.RelayFormatOpenAI,
			initialEffort: "high",
			input:         `{"reasoning_effort":"high"}`,
			operation:     map[string]any{"mode": "delete", "path": "reasoning_effort"},
			expected:      "",
		},
		{
			name:          "OpenRouter nested set",
			relayFormat:   types.RelayFormatOpenAI,
			initialEffort: "medium",
			input:         `{"reasoning":{"effort":"medium"}}`,
			operation:     map[string]any{"mode": "set", "path": "reasoning.effort", "value": "xhigh"},
			expected:      "xhigh",
		},
		{
			name:          "Claude output config set",
			relayFormat:   types.RelayFormatClaude,
			initialEffort: "high",
			input:         `{"output_config":{"effort":"high"}}`,
			operation:     map[string]any{"mode": "set", "path": "output_config.effort", "value": "max"},
			expected:      "max",
		},
		{
			name:          "Gemini thinking level set",
			relayFormat:   types.RelayFormatGemini,
			initialEffort: "medium",
			input:         `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`,
			operation:     map[string]any{"mode": "set", "path": "generationConfig.thinkingConfig.thinkingLevel", "value": "high"},
			expected:      "high",
		},
		{
			name:          "non-string value clears effort",
			relayFormat:   types.RelayFormatOpenAIResponses,
			initialEffort: "high",
			input:         `{"reasoning":{"effort":"high"}}`,
			operation:     map[string]any{"mode": "set", "path": "reasoning.effort", "value": 42},
			expected:      "",
		},
		{
			name:          "unrelated override preserves converter-derived effort",
			relayFormat:   types.RelayFormatClaude,
			initialEffort: "high",
			input:         `{"thinking":{"type":"adaptive"},"max_tokens":4096}`,
			operation:     map[string]any{"mode": "set", "path": "max_tokens", "value": 8192},
			expected:      "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &RelayInfo{
				RelayFormat:     tt.relayFormat,
				ReasoningEffort: tt.initialEffort,
				ChannelMeta: &ChannelMeta{ParamOverride: map[string]any{
					"operations": []any{tt.operation},
				}},
			}

			_, err := ApplyParamOverrideWithRelayInfo([]byte(tt.input), info)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, info.ReasoningEffort)
		})
	}
}

func TestReasoningEffortOverrideIsAuditedWithoutDebugMode(t *testing.T) {
	originalDebugEnabled := common2.DebugEnabled
	common2.DebugEnabled = false
	t.Cleanup(func() {
		common2.DebugEnabled = originalDebugEnabled
	})
	info := &RelayInfo{
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &ChannelMeta{ParamOverride: map[string]any{
			"operations": []any{
				map[string]any{"mode": "set", "path": "reasoning.effort", "value": "max"},
			},
		}},
	}

	_, err := ApplyParamOverrideWithRelayInfo([]byte(`{"reasoning":{"effort":"high"}}`), info)
	require.NoError(t, err)
	assert.Equal(t, []string{"set reasoning.effort = max"}, info.ParamOverrideAudit)
}
