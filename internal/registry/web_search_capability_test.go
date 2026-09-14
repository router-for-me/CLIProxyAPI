package registry

import (
	"encoding/json"
	"testing"
)

func testNativeCapabilities(value *bool) *NativeCapabilities {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &NativeCapabilities{WebSearch: &copyValue}
}

func TestResolveResponsesWebSearchCapability(t *testing.T) {
	trueValue, falseValue := true, false
	tests := []struct {
		name    string
		routes  []NativeCapabilityRoute
		want    bool
		present bool
	}{
		{name: "supported codex", routes: []NativeCapabilityRoute{{Provider: "codex", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: true, present: true},
		{name: "supported xai", routes: []NativeCapabilityRoute{{Provider: "xai", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: true, present: true},
		{name: "supported claude", routes: []NativeCapabilityRoute{{Provider: "claude", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: true, present: true},
		{name: "explicit unsupported model", routes: []NativeCapabilityRoute{{Provider: "codex", NativeCapabilities: testNativeCapabilities(&falseValue)}}, want: false, present: true},
		{name: "unsupported provider path", routes: []NativeCapabilityRoute{{Provider: "gemini", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: false, present: true},
		{name: "unknown metadata", routes: []NativeCapabilityRoute{{Provider: "codex"}}, present: false},
		{name: "unknown provider", routes: []NativeCapabilityRoute{{Provider: "custom", NativeCapabilities: testNativeCapabilities(&trueValue)}}, present: false},
		{name: "explicit false on unknown provider", routes: []NativeCapabilityRoute{{Provider: "custom", NativeCapabilities: testNativeCapabilities(&falseValue)}}, want: false, present: true},
		{name: "unknown plus true is unknown", routes: []NativeCapabilityRoute{{Provider: "codex", NativeCapabilities: testNativeCapabilities(&trueValue)}, {Provider: "xai"}}, present: false},
		{name: "false wins over unknown", routes: []NativeCapabilityRoute{{Provider: "codex"}, {Provider: "openai", NativeCapabilities: testNativeCapabilities(&trueValue)}}, want: false, present: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveResponsesWebSearchCapability(tt.routes)
			if (got != nil) != tt.present {
				t.Fatalf("presence = %v, want %v", got != nil, tt.present)
			}
			if got != nil && *got != tt.want {
				t.Fatalf("capability = %v, want %v", *got, tt.want)
			}
		})
	}
}

func TestModelRegistryResponsesWebSearchCapabilityChecksEveryExactClientRoute(t *testing.T) {
	reg := newTestModelRegistry()
	trueValue, falseValue := true, false
	reg.RegisterClient("codex-a", "codex", []*ModelInfo{
		{ID: "shared", MetadataModelID: "upstream-a", NativeCapabilities: testNativeCapabilities(&trueValue)},
		{ID: "alias", MetadataModelID: "upstream-capable", NativeCapabilities: testNativeCapabilities(&trueValue)},
		{ID: "team/alias", MetadataModelID: "upstream-incapable", NativeCapabilities: testNativeCapabilities(&falseValue)},
	})
	reg.RegisterClient("codex-b", "codex", []*ModelInfo{
		{ID: "shared", MetadataModelID: "upstream-b"},
		{ID: "mixed-same-provider", MetadataModelID: "upstream-capable", NativeCapabilities: testNativeCapabilities(&trueValue)},
	})
	reg.RegisterClient("codex-c", "codex", []*ModelInfo{
		{ID: "mixed-same-provider", MetadataModelID: "upstream-incapable", NativeCapabilities: testNativeCapabilities(&falseValue)},
		{ID: "mixed-provider", NativeCapabilities: testNativeCapabilities(&trueValue)},
	})
	reg.RegisterClient("gemini-a", "gemini", []*ModelInfo{
		{ID: "mixed-provider", NativeCapabilities: testNativeCapabilities(&trueValue)},
	})
	reg.RegisterClient("missing-info", "codex", []*ModelInfo{
		{ID: "missing-client-info", NativeCapabilities: testNativeCapabilities(&trueValue)},
	})
	delete(reg.clientModelInfos["missing-info"], "missing-client-info")

	assertCapability := func(id string, want bool, present bool) {
		t.Helper()
		got := reg.GetResponsesWebSearchCapability(id)
		if (got != nil) != present {
			t.Fatalf("%s presence = %v, want %v", id, got != nil, present)
		}
		if got != nil && *got != want {
			t.Fatalf("%s = %v, want %v", id, *got, want)
		}
	}
	assertCapability("shared", false, false)
	assertCapability("mixed-same-provider", false, true)
	assertCapability("mixed-provider", false, true)
	assertCapability("alias", true, true)
	assertCapability("team/alias", false, true)
	assertCapability("upstream-capable", false, false)
	assertCapability("missing-client-info", false, false)
}

func TestNativeCapabilitiesCloneIsolationAndJSONVisibility(t *testing.T) {
	reg := newTestModelRegistry()
	value := true
	input := &ModelInfo{ID: "clone-test", NativeCapabilities: testNativeCapabilities(&value)}
	reg.RegisterClient("clone-client", "codex", []*ModelInfo{input})

	*input.NativeCapabilities.WebSearch = false
	got := reg.GetModelsForClient("clone-client")
	if len(got) != 1 || got[0].NativeCapabilities == nil || got[0].NativeCapabilities.WebSearch == nil || !*got[0].NativeCapabilities.WebSearch {
		t.Fatalf("registered metadata changed through input alias: %+v", got)
	}
	*got[0].NativeCapabilities.WebSearch = false
	gotAgain := reg.GetModelsForClient("clone-client")
	if !*gotAgain[0].NativeCapabilities.WebSearch {
		t.Fatal("registered metadata changed through output clone")
	}

	raw, err := json.Marshal(gotAgain[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || json.Valid(raw) == false {
		t.Fatalf("invalid JSON: %s", raw)
	}
	var exposed map[string]any
	if err := json.Unmarshal(raw, &exposed); err != nil {
		t.Fatal(err)
	}
	if _, exists := exposed["native_capabilities"]; exists {
		t.Fatalf("native metadata leaked into normal serialization: %s", raw)
	}
}

func TestModelSectionChangedIncludesNativeCapabilities(t *testing.T) {
	trueValue, falseValue := true, false
	oldModels := []*ModelInfo{{ID: "remote", NativeCapabilities: testNativeCapabilities(&trueValue)}}
	newModels := []*ModelInfo{{ID: "remote", NativeCapabilities: testNativeCapabilities(&falseValue)}}
	if !modelSectionChanged(oldModels, newModels) {
		t.Fatal("native capability update was not detected")
	}
}

func TestModelSectionChangedPreservesExistingComparisonSemantics(t *testing.T) {
	if modelSectionChanged(nil, []*ModelInfo{}) {
		t.Fatal("nil and empty sections should remain equivalent")
	}
	if modelSectionChanged([]*ModelInfo{{ID: "a", MetadataModelID: "old"}}, []*ModelInfo{{ID: "a", MetadataModelID: "new"}}) {
		t.Fatal("unrelated serialization-excluded fields should remain ignored")
	}
	if !modelSectionChanged([]*ModelInfo{nil}, []*ModelInfo{{ID: "a"}}) {
		t.Fatal("nil model replacement should be detected")
	}
}

func TestModelInfoUnmarshalNativeCapabilitiesTriState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		raw     string
		present bool
		want    bool
	}{
		{name: "true", raw: `{"id":"a","native_capabilities":{"web_search":true}}`, present: true, want: true},
		{name: "false", raw: `{"id":"a","native_capabilities":{"web_search":false}}`, present: true, want: false},
		{name: "absent", raw: `{"id":"a"}`, present: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var model ModelInfo
			if err := json.Unmarshal([]byte(tt.raw), &model); err != nil {
				t.Fatal(err)
			}
			present := model.NativeCapabilities != nil && model.NativeCapabilities.WebSearch != nil
			if present != tt.present {
				t.Fatalf("presence = %v, want %v", present, tt.present)
			}
			if present && *model.NativeCapabilities.WebSearch != tt.want {
				t.Fatalf("value = %v, want %v", *model.NativeCapabilities.WebSearch, tt.want)
			}
		})
	}
}
