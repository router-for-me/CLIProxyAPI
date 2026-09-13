package registry

import (
	"reflect"
	"testing"
)

func TestModelModalitiesChecksEveryExactClientRoute(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			r := newTestModelRegistry()
			r.RegisterClient("a", "codex", []*ModelInfo{{ID: "shared", SupportedInputModalities: []string{"text", "image", "audio"}, SupportedOutputModalities: []string{"text", "image"}}})
			r.RegisterClient("b", provider, []*ModelInfo{{ID: "shared", SupportedInputModalities: []string{"TEXT"}, SupportedOutputModalities: []string{"text"}}})
			inputs, outputs := r.GetModelModalities("shared")
			if !reflect.DeepEqual(inputs, []string{"text"}) || !reflect.DeepEqual(outputs, []string{"text"}) {
				t.Fatalf("shared route overclaimed modalities: %v / %v", inputs, outputs)
			}
			inputs[0] = "mutated"
			inputs, _ = r.GetModelModalities("shared")
			if inputs[0] != "text" {
				t.Fatal("returned modalities share registry memory")
			}
			r.RegisterClient("unknown", provider, []*ModelInfo{{ID: "shared", SupportedOutputModalities: []string{"text"}}})
			inputs, outputs = r.GetModelModalities("shared")
			if inputs != nil || !reflect.DeepEqual(outputs, []string{"text"}) {
				t.Fatalf("unknown inputs affected the wrong direction: %v / %v", inputs, outputs)
			}
			r.RegisterClient("unknown-output", provider, []*ModelInfo{{ID: "shared", SupportedInputModalities: []string{"text"}}})
			if inputs, outputs = r.GetModelModalities("shared"); inputs != nil || outputs != nil {
				t.Fatalf("unknown declarations must remain unknown: %v / %v", inputs, outputs)
			}
			if inputs, outputs = r.GetModelModalities("prefix/shared"); inputs != nil || outputs != nil {
				t.Fatal("unregistered public route borrowed suffix capabilities")
			}
		})
	}
}

func TestModelModalitiesDistinguishesEmptyIntersectionFromUnknown(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("a", "codex", []*ModelInfo{{ID: "shared", SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}}})
	r.RegisterClient("b", "codex", []*ModelInfo{{ID: "shared", SupportedInputModalities: []string{"image"}, SupportedOutputModalities: []string{"image"}}})
	r.RegisterClient("c", "codex", []*ModelInfo{{ID: "shared", SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}}})
	inputs, outputs := r.GetModelModalities("shared")
	if inputs == nil || outputs == nil || len(inputs) != 0 || len(outputs) != 0 {
		t.Fatalf("known empty intersection became unknown: %#v / %#v", inputs, outputs)
	}
	delete(r.clientModelInfos["a"], "shared")
	if inputs, outputs = r.GetModelModalities("shared"); inputs != nil || outputs != nil {
		t.Fatal("missing client info must not imply support")
	}
}
