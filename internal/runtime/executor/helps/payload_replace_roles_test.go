package helps

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPayloadReplaceChatRoles(t *testing.T) {
	input := `{"messages":[{"role":"system","content":"hero"},{"role":"developer","content":[{"type":"text","text":"hero"}]},{"role":"user","content":"hero"},{"role":"assistant","content":"hero","tool_calls":[{"id":"hero","function":{"name":"hero","arguments":"hero"}}]},{"role":"tool","content":"hero","tool_call_id":"hero"},{"role":"system","content":"hero"},{"role":"developer","content":"hero"},{"role":"assistant","content":[{"type":"text","text":"hero"},{"type":"thinking","thinking":"hero","signature":"hero"},{"type":"tool_use","input":{"text":"hero"}},{"type":"image","source":{"data":"hero"}}]}]}`
	for _, tt := range []struct {
		role  string
		paths []string
	}{
		{"system", []string{"messages.0.content", "messages.5.content"}},
		{"developer", []string{"messages.1.content.0.text", "messages.6.content"}},
	} {
		for _, all := range []bool{false, true} {
			t.Run(tt.role+map[bool]string{false: "/positions", true: "/all"}[all], func(t *testing.T) {
				target := config.PayloadReplaceTarget{Role: tt.role, All: all}
				paths := tt.paths
				if !all {
					target.Positions = []int{2, 2, 99}
					paths = paths[1:]
				}
				cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Target: target, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}}}}}
				out := ApplyPayloadConfigWithRoot(cfg, "model", "openai", "", []byte(input), nil, "", "")
				want := ojson.NewDocString(input)
				for _, path := range paths {
					if errSet := want.Set(path, "hero!"); errSet != nil {
						t.Fatal(errSet)
					}
				}
				if !reflect.DeepEqual(ojson.ParseBytes(out).Value(), ojson.ParseBytes(want.Bytes()).Value()) {
					t.Fatalf("unexpected role selection or unrelated change: %s", out)
				}
			})
		}
	}
}

func TestPayloadReplaceRejectsAssistant(t *testing.T) {
	for _, tt := range []struct {
		protocol, root, input string
	}{
		{"openai", "", `{"messages":[{"role":"user","content":"hero"},{"role":"assistant","content":"hero"}]}`},
		{"claude", "", `{"system":"hero","messages":[{"role":"assistant","content":[{"type":"text","text":"hero"}]}]}`},
		{"responses", "", `{"instructions":"hero","input":[{"role":"assistant","content":[{"type":"output_text","text":"hero"}]}]}`},
		{"codex", "", `{"input":[{"role":"assistant","content":"hero"}]}`},
		{"gemini", "", `{"contents":[{"role":"model","parts":[{"text":"hero"}]}]}`},
		{"antigravity", "request", `{"request":{"contents":[{"role":"model","parts":[{"text":"hero"}]}]}}`},
	} {
		t.Run(tt.protocol, func(t *testing.T) {
			rule := config.PayloadReplaceRule{
				Models:       []config.PayloadModelRule{{Name: "*"}},
				Target:       config.PayloadReplaceTarget{Role: "assistant", All: true},
				Replacements: []config.PayloadReplacement{{"hero": "hero!"}, {"pattern": "hero", "replacement": "changed"}},
			}
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{rule}}}
			out := ApplyPayloadConfigWithRoot(cfg, "model", tt.protocol, tt.root, []byte(tt.input), nil, "", "")
			if string(out) != tt.input {
				t.Fatalf("unsupported assistant rule modified the request: %s", out)
			}
			doc := ojson.NewDocString(tt.input)
			if replacePayloadTextDoc(doc, tt.protocol, tt.root, &rule, 0, func(path string) { t.Fatalf("unexpected write: %s", path) }) {
				t.Fatal("direct traversal accepted assistant role")
			}
			if string(doc.Bytes()) != tt.input {
				t.Fatalf("direct traversal modified assistant text: %s", doc.Bytes())
			}
		})
	}
}

func TestPayloadReplaceRegexRoles(t *testing.T) {
	for _, tt := range []struct {
		name, protocol, root, role, input, path string
	}{
		{"claude system", "claude", "", "system", `{"system":[{"type":"text","text":"User-42 hero"}]}`, "system.0.text"},
		{"responses system", "responses", "", "system", `{"instructions":"User-42 hero"}`, "instructions"},
		{"codex developer", "codex", "", "developer", `{"input":[{"role":"developer","content":[{"type":"input_text","text":"User-42 hero"}]}]}`, "input.0.content.0.text"},
		{"antigravity system", "antigravity", "request", "system", `{"request":{"systemInstruction":{"parts":[{"text":"User-42 hero"}]}}}`, "request.systemInstruction.parts.0.text"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rule := config.PayloadReplaceRule{
				Models: []config.PayloadModelRule{{Name: "*"}},
				Target: config.PayloadReplaceTarget{Role: tt.role, All: true},
				Replacements: []config.PayloadReplacement{
					{"pattern": "[invalid", "replacement": "ignored"},
					{"pattern": `(?i)user-(\d+)`, "replacement": "account-${1}"},
					{"hero": "friend"},
					{"pattern": "", "replacement": "ignored"},
				},
			}
			nextRule := rule
			nextRule.Replacements = []config.PayloadReplacement{{"account": "acct"}}
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{rule, nextRule}}}
			out := ApplyPayloadConfigWithRoot(cfg, "model", tt.protocol, tt.root, []byte(tt.input), nil, "", "")
			want := ojson.NewDocString(tt.input)
			if errSet := want.Set(tt.path, "acct-42 friend"); errSet != nil {
				t.Fatal(errSet)
			}
			if !reflect.DeepEqual(ojson.ParseBytes(out).Value(), ojson.ParseBytes(want.Bytes()).Value()) {
				t.Fatalf("regex and role changes must compose while skipping only invalid entries: %s", out)
			}
		})
	}
}

func TestPayloadReplaceNativeRoles(t *testing.T) {
	for _, tt := range []struct {
		name, protocol, root, role, input string
		positions                         []int
		paths                             []string
	}{
		{"claude system string", "claude", "", "system", `{"system":"hero","messages":[{"role":"user","content":"hero"}]}`, []int{1, 2}, []string{"system"}},
		{"claude system blocks count as one", "claude", "", "system", `{"system":[{"type":"text","text":"hero","cache_control":{"type":"ephemeral"}},{"type":"text","text":"hero"},{"type":"thinking","text":"hero","signature":"hero"}],"messages":[{"role":"user","content":"hero"}]}`, []int{1}, []string{"system.0.text", "system.1.text"}},
		{"claude missing second system", "claude", "", "system", `{"system":[{"type":"text","text":"hero"},{"type":"text","text":"hero"}]}`, []int{2}, nil},
		{"claude assistant rejected", "claude", "", "assistant", `{"messages":[{"role":"assistant","content":[{"type":"text","text":"hero"},{"type":"thinking","thinking":"hero","signature":"hero"},{"type":"tool_use","id":"hero","input":{"text":"hero"}}]},{"role":"user","content":[{"type":"tool_result","content":"hero"}]}]}`, nil, nil},
		{"claude developer not restored", "claude", "", "developer", `{"system":[{"type":"text","text":"hero"}],"messages":[{"role":"user","content":"hero"}]}`, nil, nil},
		{"gemini system", "gemini", "", "system", `{"systemInstruction":{"parts":[{"text":"hero"},{"text":"hero"}]},"contents":[{"role":"user","parts":[{"text":"hero"}]}]}`, []int{1}, []string{"systemInstruction.parts.0.text", "systemInstruction.parts.1.text"}},
		{"gemini snake case system", "gemini", "", "system", `{"system_instruction":{"parts":[{"text":"hero"}]}}`, nil, []string{"system_instruction.parts.0.text"}},
		{"antigravity rooted system", "antigravity", "request", "system", `{"request":{"systemInstruction":{"parts":[{"text":"hero"}]}},"systemInstruction":{"parts":[{"text":"hero"}]}}`, nil, []string{"request.systemInstruction.parts.0.text"}},
		{"gemini model is not editable", "gemini", "", "assistant", `{"contents":[{"role":"user","parts":[{"text":"hero"}]},{"role":"model","parts":[{"text":"hero","thought":true,"thoughtSignature":"hero"},{"functionCall":{"name":"hero","args":{"text":"hero"}},"text":"hero"},{"functionResponse":{"response":{"text":"hero"}},"text":"hero"},{"inlineData":{"data":"hero"}},{"text":"hero"}]}]}`, nil, nil},
		{"gemini developer not restored", "gemini", "", "developer", `{"systemInstruction":{"parts":[{"text":"hero"}]}}`, nil, nil},
		{"responses instructions first", "responses", "", "system", `{"instructions":"hero","input":[{"role":"system","content":[{"type":"input_text","text":"hero"}]},{"role":"developer","content":"hero"}]}`, []int{1}, []string{"instructions"}},
		{"responses explicit system second", "responses", "", "system", `{"instructions":"hero","input":[{"role":"system","content":[{"type":"input_text","text":"hero"}]}]}`, []int{2}, []string{"input.0.content.0.text"}},
		{"responses all systems", "meta", "", "system", `{"instructions":"hero","input":[{"role":"system","content":"hero"}]}`, nil, []string{"instructions", "input.0.content"}},
		{"responses empty instructions still count", "responses", "", "system", `{"instructions":"","input":[{"role":"system","content":"hero"}]}`, []int{2}, []string{"input.0.content"}},
		{"responses developer excludes instructions", "codex", "", "developer", `{"instructions":"hero","input":[{"role":"system","content":"hero"},{"role":"developer","content":[{"type":"input_text","text":"hero"}]}]}`, nil, []string{"input.1.content.0.text"}},
		{"responses assistant output text is not editable", "openai-response", "", "assistant", `{"input":[{"type":"reasoning","role":"assistant","content":"hero","encrypted_content":"hero"},{"type":"function_call","role":"assistant","content":"hero","arguments":"hero"},{"type":"function_call_output","role":"assistant","output":"hero"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hero","annotations":[{"text":"hero"}]},{"type":"input_image","image_url":"hero"},{"type":"refusal","refusal":"hero"}]}]}`, []int{1}, nil},
		{"string input is not system", "codex", "", "system", `{"input":"hero"}`, nil, nil},
		{"string input is not developer", "codex", "", "developer", `{"input":"hero"}`, nil, nil},
		{"string input is not assistant", "codex", "", "assistant", `{"input":"hero"}`, nil, nil},
		{"assistant messages are not editable", "openai", "", "assistant", `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"hero","arguments":"hero"}}]},{"role":"assistant","content":"hero"}]}`, []int{2}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rule := config.PayloadReplaceRule{Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: tt.role, Positions: tt.positions, All: len(tt.positions) == 0}, Replacements: []config.PayloadReplacement{{"hero": strings.Repeat("hero!", 64)}}}
			for _, prepared := range []bool{false, true} {
				doc := ojson.NewDocString(tt.input)
				if prepared {
					if errSet := doc.Set("metadata.prepared", true); errSet != nil {
						t.Fatal(errSet)
					}
				}
				want := ojson.NewDoc(doc.Bytes())
				for _, path := range tt.paths {
					if errSet := want.Set(path, strings.Repeat("hero!", 64)); errSet != nil {
						t.Fatal(errSet)
					}
				}
				var writes []string
				changed := replacePayloadTextDoc(doc, tt.protocol, tt.root, &rule, 0, func(path string) { writes = append(writes, path) })
				if changed != (len(tt.paths) != 0) || !reflect.DeepEqual(writes, tt.paths) {
					t.Fatalf("prepared=%v changed=%v writes=%v, want %v", prepared, changed, writes, tt.paths)
				}
				if !reflect.DeepEqual(ojson.ParseBytes(doc.Bytes()).Value(), ojson.ParseBytes(want.Bytes()).Value()) {
					t.Fatalf("prepared=%v unexpected structural change: %s", prepared, doc.Bytes())
				}
			}
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{rule}}}
			out := ApplyPayloadConfigWithRoot(cfg, "model", tt.protocol, tt.root, []byte(tt.input), nil, "", "")
			want := ojson.NewDocString(tt.input)
			for _, path := range tt.paths {
				if errSet := want.Set(path, strings.Repeat("hero!", 64)); errSet != nil {
					t.Fatal(errSet)
				}
			}
			if !reflect.DeepEqual(ojson.ParseBytes(out).Value(), ojson.ParseBytes(want.Bytes()).Value()) {
				t.Fatalf("payload config differs from direct traversal: %s", out)
			}
		})
	}
}
