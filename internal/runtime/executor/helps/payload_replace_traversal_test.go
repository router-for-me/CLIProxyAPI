package helps

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPayloadReplaceDeferredWrites(t *testing.T) {
	grown := strings.Repeat("expanded ", 512)
	escaped := "\"\\\n你好"
	for _, scenario := range []struct {
		protocol, root, input string
		changed               []string
	}{
		{"openai", "", `{"messages":[{"role":"assistant","content":"grow"},{"role":"user","content":"grow"},{"role":"user","content":[{"type":"text","text":"shrink"},{"type":"image_url","image_url":{"url":"grow"}},{"type":"text","text":"escaped"},{"type":"text","text":"unchanged"}]},{"role":"user","content":"grow"}]}`, []string{"messages.1.content", "messages.2.content.0.text", "messages.2.content.2.text"}},
		{"claude", "", `{"messages":[{"role":"user","content":"grow"},{"role":"user","content":[{"type":"text","text":"shrink"},{"type":"tool_result","content":"grow"},{"type":"text","text":"escaped"},{"type":"text","text":"unchanged"}]},{"role":"user","content":"grow"}]}`, []string{"messages.0.content", "messages.1.content.0.text", "messages.1.content.2.text"}},
		{"codex", "", `{"input":[{"type":"function_call_output","role":"user","content":"grow"},{"type":"message","role":"user","content":"grow"},{"role":"user","content":[{"type":"input_text","text":"shrink"},{"type":"input_image","image_url":"grow"},{"type":"input_text","text":"escaped"},{"type":"input_text","text":"unchanged"}]},{"role":"user","content":"grow"}]}`, []string{"input.1.content", "input.2.content.0.text", "input.2.content.2.text"}},
		{"gemini", "", `{"contents":[{"role":"model","parts":[{"text":"grow"}]},{"role":"user","parts":[{"text":"grow"}]},{"role":"user","parts":[{"text":"shrink"},{"functionResponse":{"response":{"text":"grow"}}},{"text":"escaped"},{"text":"unchanged"}]},{"role":"user","parts":[{"text":"grow"}]}]}`, []string{"contents.1.parts.0.text", "contents.2.parts.0.text", "contents.2.parts.2.text"}},
		{"antigravity", "request", `{"request":{"contents":[{"role":"user","parts":[{"text":"grow"}]},{"role":"user","parts":[{"text":"shrink"},{"inlineData":{"data":"grow"}},{"text":"escaped"},{"text":"unchanged"}]},{"role":"user","parts":[{"text":"grow"}]}]}}`, []string{"request.contents.0.parts.0.text", "request.contents.1.parts.0.text", "request.contents.1.parts.2.text"}},
	} {
		for _, prepared := range []bool{false, true} {
			name := scenario.protocol + "/raw"
			if prepared {
				name = scenario.protocol + "/prepared-doc"
			}
			t.Run(name, func(t *testing.T) {
				doc := ojson.NewDocString(scenario.input)
				if prepared {
					if errSet := doc.Set("metadata.prepared", true); errSet != nil {
						t.Fatal(errSet)
					}
					if errSet := doc.Set("metadata.counter", 7); errSet != nil {
						t.Fatal(errSet)
					}
				}
				want := ojson.NewDoc(doc.Bytes())
				for i, value := range []string{grown, "", escaped} {
					if errSet := want.Set(scenario.changed[i], value); errSet != nil {
						t.Fatal(errSet)
					}
				}
				rule := &config.PayloadReplaceRule{
					Target:       config.PayloadReplaceTarget{Role: "user", Positions: []int{2, 1, 2}},
					Replacements: []config.PayloadReplacement{{"grow": grown}, {"shrink": ""}, {"escaped": escaped}},
				}
				var writes []string
				if !replacePayloadUserTextDoc(doc, scenario.protocol, scenario.root, rule, func(path string) { writes = append(writes, path) }) {
					t.Fatal("expected text changes")
				}
				if !reflect.DeepEqual(writes, scenario.changed) {
					t.Fatalf("tracked writes = %v, want %v", writes, scenario.changed)
				}
				if !reflect.DeepEqual(ojson.ParseBytes(doc.Bytes()).Value(), ojson.ParseBytes(want.Bytes()).Value()) {
					t.Fatalf("deferred writes changed unrelated content or lost text: %s", doc.Bytes())
				}
			})
		}
	}
}

func TestPayloadReplaceNoOpDoesNotTrackWrites(t *testing.T) {
	for _, replacements := range [][]config.PayloadReplacement{
		{{"absent": "replacement"}},
		{{"hero": "hero"}},
		{{"hero": "fool"}, {"fool": "hero"}},
	} {
		doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"hero"}]}`)
		rule := &config.PayloadReplaceRule{Target: config.PayloadReplaceTarget{Role: "user", All: true}, Replacements: replacements}
		changed := replacePayloadUserTextDoc(doc, "openai", "", rule, func(path string) { t.Errorf("no-op tracked write: %s", path) })
		if changed || doc.Get("messages.0.content").String() != "hero" {
			t.Fatal("no-op changed the document")
		}
	}
}
