package helps

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPayloadReplacePaths(t *testing.T) {
	for _, tt := range []struct {
		name, protocol, root, input string
		paths                       []string
		want                        map[string]string
	}{
		{
			name: "arbitrary strings without protocol adapter", protocol: "custom",
			input: `{"metadata":{"description":"hero","encoded":"{\"description\":\"hero\"}"},"messages":[{"role":"assistant","content":"hero"}],"tools":[{"type":"function","function":{"description":"hero"}},{"type":"function","function":{"description":"hero"}},{"type":"other","function":{"description":"hero"}}]}`,
			paths: []string{"metadata.description", "metadata.encoded", "messages.0.content", `tools.#(type=="function")#.function.description`},
			want:  map[string]string{"metadata.description": "hero!", "metadata.encoded": `{"description":"hero!"}`, "messages.0.content": "hero!", "tools.0.function.description": "hero!", "tools.1.function.description": "hero!", "tools.2.function.description": "hero"},
		},
		{
			name: "root and nested query deduplication", protocol: "antigravity", root: "request",
			input: `{"metadata":{"description":"hero"},"request":{"metadata":{"description":"hero"},"input":[{"type":"additional_tools","tools":[{"name":"functions","tools":[{"name":"search","description":"hero"},{"name":"search","description":"hero"}]}]}]}}`,
			paths: []string{"metadata.description", " metadata.description ", `input.#(type=="additional_tools")#.tools.#(name=="functions")#.tools.#(name=="search")#.description`, "input.0.tools.0.tools.0.description"},
			want:  map[string]string{"metadata.description": "hero", "request.metadata.description": "hero!", "request.input.0.tools.0.tools.0.description": "hero!", "request.input.0.tools.0.tools.1.description": "hero!"},
		},
		{
			name: "query targets are resolved before writes", protocol: "openai",
			input: `{"items":[{"name":"hero","description":"hero"},{"name":"hero","description":"hero"}]}`,
			paths: []string{`items.#(name=="hero")#.name`, `items.#(name=="hero")#.description`},
			want:  map[string]string{"items.0.name": "hero!", "items.0.description": "hero!", "items.1.name": "hero!", "items.1.description": "hero!"},
		},
		{
			name: "provider root with mixed projections", protocol: "antigravity", root: "request",
			input: `{"request":{"systemInstruction":{"parts":[{"text":"hero"}]},"contents":[{"parts":[{"thought":true,"text":"hero"},{"thought":false,"text":"hero"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","description":"hero"},{"name":"other","description":"hero"}]}]}}`,
			paths: []string{"systemInstruction.parts.#.text", `contents.#.parts.#(thought==true)#.text`, `tools.#.functionDeclarations.#(name=="lookup")#.description`},
			want: map[string]string{
				"request.systemInstruction.parts.0.text": "hero!", "request.contents.0.parts.0.text": "hero!", "request.contents.0.parts.1.text": "hero",
				"request.tools.0.functionDeclarations.0.description": "hero!", "request.tools.0.functionDeclarations.1.description": "hero",
			},
		},
		{
			name: "escaped keys and array strings", protocol: "claude",
			input: `{"metadata":{"a.b":"hero"},"values":["hero","hero"],"1":"hero"}`,
			paths: []string{`metadata.a\.b`, "values.0", "1"},
			want:  map[string]string{`metadata.a\.b`: "hero!", "values.0": "hero!", "values.1": "hero", "1": "hero!"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
				Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Paths: tt.paths},
				Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
			}}}}
			for _, mode := range []string{"bytes", "doc"} {
				doc := ojson.NewDocString(tt.input)
				var touched map[string]bool
				if mode == "doc" {
					touched = ApplyPayloadConfigWithTrackedPathsDoc(cfg, "model", tt.protocol, "", tt.root, doc, nil, "", "", nil, "tools", "metadata", "request")
				} else {
					out, writes := ApplyPayloadConfigWithTrackedPaths(cfg, "model", tt.protocol, "", tt.root, []byte(tt.input), nil, "", "", nil, "tools", "metadata", "request")
					doc, touched = ojson.NewDoc(out), writes
				}
				for path, want := range tt.want {
					if got := doc.Get(path).String(); got != want {
						t.Fatalf("%s %s = %q, want %q; body: %s", mode, path, got, want, doc.String())
					}
				}
				if tt.root != "" && !touched[tt.root] {
					t.Fatalf("root write not tracked: %v", touched)
				}
				if tt.name == "arbitrary strings without protocol adapter" && (!touched["tools"] || !touched["metadata"]) {
					t.Fatalf("arbitrary writes not tracked: %v", touched)
				}
			}
		})
	}
}

func TestPayloadReplaceWildcardKey(t *testing.T) {
	for _, tt := range []struct {
		part string
		want bool
	}{
		{"tool*", true}, {"tool?", true}, {`tool\.*`, true}, {`tool\|*`, true},
		{`\@tool*`, true}, {`tool\**`, true}, {"", false}, {"tools", false},
		{`tool\*`, false}, {`tool\?`, false}, {`t*|@values`, false}, {`t*|@keys`, false},
		{`t*|!["x"]`, false}, {`[t*,!1]`, false}, {`{a:t*}`, false},
		{`@pretty:{"prefix":"*"}`, false}, {`#(content%"*hero*")`, false},
	} {
		if got := payloadReplaceWildcardKey(tt.part); got != tt.want {
			t.Errorf("wildcard key %q = %v, want %v", tt.part, got, tt.want)
		}
	}
}

func TestPayloadReplaceSourceChild(t *testing.T) {
	parent := ojson.Get(`{"text":"hero","items":["hero"]}`, "@this")
	child := parent.Get("text")
	if !payloadReplaceSourceChild(parent, child) || !payloadReplaceSourceChild(parent, parent.Get("items")) {
		t.Fatal("source-owned values were rejected")
	}
	copied := child
	copied.Raw = strings.Clone(child.Raw)
	if payloadReplaceSourceChild(parent, copied) {
		t.Fatal("equal copied text was accepted as a source node")
	}
	for _, index := range []int{-1, 0, len(parent.Raw), len(parent.Raw) + 1} {
		shifted := child
		shifted.Index = index
		if payloadReplaceSourceChild(parent, shifted) {
			t.Fatalf("wrong source offset %d was accepted", index)
		}
	}
	if payloadReplaceSourceChild(parent, ojson.Result{}) {
		t.Fatal("missing result was accepted")
	}
}

func TestPayloadReplacePathsComputedSelections(t *testing.T) {
	for _, tt := range []struct{ name, input, path string }{
		{"pipe values", `{"messages":[{"text":"hero"}],"tools":{"a":{"text":"hero"}}}`, `t*|@values.#.text`},
		{"pipe keys", `{"messages":["hero"],"tools":{"a":{"text":"hero"}}}`, `t*|@keys.#.@this`},
		{"pipe literal", `{"messages":[{"text":"hero"}],"tools":[]}`, `t*|!["x"].#.text`},
		{"array multipath", `{"messages":[{"text":"hero"}],"tools":[]}`, `[t*,!1].#.text`},
		{"object multipath", `{"messages":[{"text":"hero"}],"tools":[]}`, `{a:t*}.#.text`},
		{"modifier argument", `{"messages":[{"text":"hero"}],"tools":[]}`, `@pretty:{"prefix":"*"}.#.text`},
		{"pipe reverse", `{"tools":[{"text":"hero"},{"text":"other"}]}`, `t*|@reverse.#.text`},
		{"wildcard keys collision", `{"tool_choice":{"hero":1}}`, `t*.@keys.#.@this`},
		{"plain keys collision", `{"hero":{"hero":1}}`, `hero.@keys.#.@this`},
		{"keys before wildcard", `{"hero":{"hero":1}}`, `hero.@keys.t*.#.text`},
		{"reordered equal bytes", `{"tools":["hero","hero"]}`, `tools.@reverse.#.@this`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, prepared := range []bool{false, true} {
				doc := ojson.NewDocString("\n  " + tt.input)
				if prepared {
					if errSet := doc.Set("prepared", true); errSet != nil {
						t.Fatal(errSet)
					}
				}
				before := doc.String()
				source := doc.Get("@this")
				source.Index = 0
				visitPayloadReplacePath(source, tt.path, func(text ojson.Result, _ []payloadReplacePathStep) {
					t.Fatalf("computed path reached source visitor: raw=%q index=%d", text.Raw, text.Index)
				})
				rule := &config.PayloadReplaceRule{Target: config.PayloadReplaceTarget{Paths: []string{tt.path}}, Replacements: []config.PayloadReplacement{{"hero": "friend"}}}
				var writes []string
				mutated := replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) })
				if mutated || len(writes) != 0 || doc.String() != before {
					t.Fatalf("prepared=%v computed path wrote source: mutated=%v writes=%v body=%s", prepared, mutated, writes, doc.String())
				}
			}
		})
	}
}

func TestPayloadReplacePathsComputedBeforeConcrete(t *testing.T) {
	doc := ojson.NewDocString(`{"messages":[{"text":"hero"}],"tools":{"a":{"text":"hero"}}}`)
	rule := &config.PayloadReplaceRule{
		Target:       config.PayloadReplaceTarget{Paths: []string{`t*|@values.#.text`, "messages.0.text"}},
		Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
	}
	var writes []string
	if !replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) }) ||
		!reflect.DeepEqual(writes, []string{"messages.0.text"}) || doc.Get("messages.0.text").String() != "hero!" || doc.Get("tools.a.text").String() != "hero" {
		t.Fatalf("computed path masked or changed concrete target: writes=%v body=%s", writes, doc.String())
	}
}

func TestPayloadReplacePathsConcreteWrites(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
		paths, writes     []string
	}{
		{
			name:  "array projection skips missing and nonstring fields",
			input: `{"tools":[{"description":"hero"},{},{"description":123},{"description":"hero"}]}`,
			want:  `{"tools":[{"description":"hero!"},{},{"description":123},{"description":"hero!"}]}`,
			paths: []string{"tools.#.description", "tools.0.description"}, writes: []string{"tools.0.description", "tools.3.description"},
		},
		{
			name:  "nested projections",
			input: `{"contents":[{"parts":[{"text":"hero"},{"text":"hero"}]},{"parts":[{"text":"hero"}]}]}`,
			want:  `{"contents":[{"parts":[{"text":"hero!"},{"text":"hero!"}]},{"parts":[{"text":"hero!"}]}]}`,
			paths: []string{"contents.#.parts.#.text"}, writes: []string{"contents.0.parts.0.text", "contents.0.parts.1.text", "contents.1.parts.0.text"},
		},
		{
			name:  "projection before query preserves parent",
			input: `{"contents":[{"parts":[{"thought":true,"text":"hero"},{"thought":false,"text":"hero"}]},{"parts":[{"thought":true,"text":"hero"}]}]}`,
			want:  `{"contents":[{"parts":[{"thought":true,"text":"hero!"},{"thought":false,"text":"hero"}]},{"parts":[{"thought":true,"text":"hero!"}]}]}`,
			paths: []string{`contents.#.parts.#(thought==true)#.text`}, writes: []string{"contents.0.parts.0.text", "contents.1.parts.0.text"},
		},
		{
			name:  "query before projection",
			input: `{"groups":[{"enabled":true,"items":[{"text":"hero"},{"text":"hero"}]},{"enabled":false,"items":[{"text":"hero"}]}]}`,
			want:  `{"groups":[{"enabled":true,"items":[{"text":"hero!"},{"text":"hero!"}]},{"enabled":false,"items":[{"text":"hero"}]}]}`,
			paths: []string{`groups.#(enabled==true)#.items.#.text`}, writes: []string{"groups.0.items.0.text", "groups.0.items.1.text"},
		},
		{
			name:  "projection preserves first query match without suffix",
			input: `{"groups":[{"items":[{"name":"selected"},{"name":"selected","text":"hero"}]},{"items":[{"name":"selected","text":"hero"}]}]}`,
			want:  `{"groups":[{"items":[{"name":"selected"},{"name":"selected","text":"hero"}]},{"items":[{"name":"selected","text":"hero!"}]}]}`,
			paths: []string{`groups.#.items.#(name=="selected").text`}, writes: []string{"groups.1.items.0.text"},
		},
		{
			name:  "terminal hash remains array length",
			input: `{"values":["hero"]}`,
			want:  `{"values":["hero"]}`,
			paths: []string{"values.#"},
		},
		{
			name:  "escaped hash remains literal field",
			input: `{"values":{"#":{"text":"hero"}}}`,
			want:  `{"values":{"#":{"text":"hero!"}}}`,
			paths: []string{`values.\#.text`}, writes: []string{`values.\#.text`},
		},
		{
			name:  "projection does not traverse objects",
			input: `{"values":{"text":"hero"}}`,
			want:  `{"values":{"text":"hero"}}`,
			paths: []string{"values.#.text"},
		},
		{
			name:  "identity suffix folds into array element path",
			input: `{"values":["hero","hero"]}`,
			want:  `{"values":["hero!","hero!"]}`,
			paths: []string{`values.#(=="hero")#.@this`, "values.0"}, writes: []string{"values.0", "values.1"},
		},
		{
			name:  "wildcard array query preserves scalar source offsets",
			input: `{"value_choice":"auto","values":["hero","hero"]}`,
			want:  `{"value_choice":"auto","values":["hero!","hero!"]}`,
			paths: []string{`value*.#(=="hero")#.@this`, "values.0"}, writes: []string{"values.0", "values.1"},
		},
		{
			name:  "wildcard projection preserves scalar source offsets",
			input: `{"value_choice":"auto","values":["hero","hero"]}`,
			want:  `{"value_choice":"auto","values":["hero!","hero!"]}`,
			paths: []string{"value*.#.@this", "values.0"}, writes: []string{"values.0", "values.1"},
		},
		{
			name:  "identity projection folds into array element path",
			input: `{"values":["hero","hero"]}`,
			want:  `{"values":["hero!","hero!"]}`,
			paths: []string{"values.#.@this", "values.1"}, writes: []string{"values.0", "values.1"},
		},
		{
			name:  "identity prefix folds before query",
			input: `{"values":[{"text":"hero"},{"text":"hero"}]}`,
			want:  `{"values":[{"text":"hero!"},{"text":"hero!"}]}`,
			paths: []string{`@this.values.#(text=="hero")#.text`, "values.0.text"}, writes: []string{"values.0.text", "values.1.text"},
		},
		{
			name:  "literal identity key stays escaped",
			input: `{"values":{"@this":"hero"}}`,
			want:  `{"values":{"@this":"hero!"}}`,
			paths: []string{`values.\@this`}, writes: []string{`values.\@this`},
		},
		{
			name:  "computed string cannot mask source node",
			input: `{"values":["hero"]}`,
			want:  `{"values":["hero!"]}`,
			paths: []string{`values.#(%"*hero*")#.@tostr`, "values.0"}, writes: []string{"values.0"},
		},
		{
			name:  "computed string after source node",
			input: `{"values":["hero"]}`,
			want:  `{"values":["hero!"]}`,
			paths: []string{"values.0", `values.#(%"*hero*")#.@tostr`}, writes: []string{"values.0"},
		},
		{
			name:  "computed strings alone do not write",
			input: `{"values":["hero"]}`,
			want:  `{"values":["hero"]}`,
			paths: []string{`values.#(%"*hero*")#.@tostr`},
		},
		{
			name:  "literal leading colon",
			input: `{"metadata":{":description":"hero","description":123}}`,
			want:  `{"metadata":{":description":"hero!","description":123}}`,
			paths: []string{"metadata.:description"}, writes: []string{`metadata.\:description`},
		},
		{
			name:  "escaped leading colon",
			input: `{"metadata":{":description":"hero","description":123}}`,
			want:  `{"metadata":{":description":"hero!","description":123}}`,
			paths: []string{`metadata.\:description`}, writes: []string{`metadata.\:description`},
		},
		{
			name:  "colon aliases deduplicate",
			input: `{"metadata":{":description":"hero","description":123}}`,
			want:  `{"metadata":{":description":"hero!","description":123}}`,
			paths: []string{"metadata.:description", `metadata.\:description`}, writes: []string{`metadata.\:description`},
		},
		{
			name:  "wildcard and explicit path deduplicate",
			input: `{"tools":[{"output_format":"hero"}]}`,
			want:  `{"tools":[{"output_format":"hero!"}]}`,
			paths: []string{"t*.0.output_format", "tools.0.output_format"}, writes: []string{"tools.0.output_format"},
		},
		{
			name:  "wildcard selects first complete path",
			input: `{"tree":[{}],"tools":[{"output_format":"hero"}],"toys":[{"output_format":"hero"}]}`,
			want:  `{"tree":[{}],"tools":[{"output_format":"hero!"}],"toys":[{"output_format":"hero"}]}`,
			paths: []string{"t*.0.output_format"}, writes: []string{"tools.0.output_format"},
		},
		{
			name:  "wildcard does not skip nonstring complete match",
			input: `{"tree":{"description":123},"tools":{"description":"hero"}}`,
			want:  `{"tree":{"description":123},"tools":{"description":"hero"}}`,
			paths: []string{"t*.description"},
		},
		{
			name:  "wildcard is not array iteration",
			input: `{"tools":[{"output_format":"hero"}]}`,
			want:  `{"tools":[{"output_format":"hero"}]}`,
			paths: []string{"tools.*.output_format"},
		},
		{
			name:   "wildcard query selection precedes all writes",
			input:  `{"items":[{"name":"hero","description":"hero"},{"name":"hero","description":"hero"}]}`,
			want:   `{"items":[{"name":"hero!","description":"hero!"},{"name":"hero!","description":"hero!"}]}`,
			paths:  []string{`i*.#(name=="hero")#.name`, `items.#(name=="hero")#.description`},
			writes: []string{"items.0.name", "items.1.name", "items.0.description", "items.1.description"},
		},
		{
			name:  "wildcard query keeps its selected parent",
			input: `{"tree":[{"name":"selected"}],"tools":[{"name":"unselected","description":"hero"}]}`,
			want:  `{"tree":[{"name":"selected"}],"tools":[{"name":"unselected","description":"hero"}]}`,
			paths: []string{`t*.#(name=="selected")#.description`},
		},
		{
			name:  "nested wildcard queries keep each selected parent",
			input: `{"tree":[{"name":"selected","nested":[{"kind":"keep"}]}],"tools":[{"name":"unselected","nested":[{"kind":"keep","description":"hero"}]}]}`,
			want:  `{"tree":[{"name":"selected","nested":[{"kind":"keep"}]}],"tools":[{"name":"unselected","nested":[{"kind":"keep","description":"hero"}]}]}`,
			paths: []string{`t*.#(name=="selected")#.n*.#(kind=="keep")#.description`},
		},
		{
			name:   "nested queries do not mix sibling parents",
			input:  `{"groups":[{"enabled":true,"tree":[{"name":"selected"}],"tools":[{"name":"unselected","description":"hero"}]},{"enabled":true,"tree":[{"name":"selected","description":"hero"}]},{"enabled":false,"tree":[{"name":"selected","description":"hero"}]}]}`,
			want:   `{"groups":[{"enabled":true,"tree":[{"name":"selected"}],"tools":[{"name":"unselected","description":"hero"}]},{"enabled":true,"tree":[{"name":"selected","description":"hero!"}]},{"enabled":false,"tree":[{"name":"selected","description":"hero"}]}]}`,
			paths:  []string{`groups.#(enabled==true)#.t*.#(name=="selected")#.description`, "groups.1.tree.0.description"},
			writes: []string{"groups.1.tree.0.description"},
		},
		{
			name:  "first query match does not fall through a missing suffix",
			input: `{"items":[{"name":"selected"},{"name":"selected","description":"hero"}]}`,
			want:  `{"items":[{"name":"selected"},{"name":"selected","description":"hero"}]}`,
			paths: []string{`items.#(name=="selected").description`},
		},
		{
			name:  "query is first match without trailing hash",
			input: `{"items":[{"name":"hero"},{"name":"hero"}]}`,
			want:  `{"items":[{"name":"hero!"},{"name":"hero"}]}`,
			paths: []string{`items.#(name=="hero").name`}, writes: []string{"items.0.name"},
		},
		{
			name:  "escaped literal wildcard and colon keys",
			input: `{"t*":{":a.b":"hero","a.b":123}}`,
			want:  `{"t*":{":a.b":"hero!","a.b":123}}`,
			paths: []string{`t\*.:a\.b`, `t\*.\:a\.b`}, writes: []string{`t\*.\:a\.b`},
		},
	} {
		for _, prepared := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "/original", true: "/prepared"}[prepared], func(t *testing.T) {
				// Leading whitespace and prior mutations must not shift node identity.
				doc := ojson.NewDocString("\n  " + tt.input)
				want := ojson.NewDocString("\n  " + tt.want)
				if prepared {
					for _, d := range []*ojson.Doc{doc, want} {
						if errSet := d.Set("prepared", "before replacement"); errSet != nil {
							t.Fatal(errSet)
						}
					}
				}
				rule := &config.PayloadReplaceRule{
					Target:       config.PayloadReplaceTarget{Paths: tt.paths},
					Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
				}
				var writes []string
				mutated := replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) })
				if mutated != (len(tt.writes) > 0) || !reflect.DeepEqual(writes, tt.writes) {
					t.Fatalf("mutated=%v writes=%v, want writes=%v", mutated, writes, tt.writes)
				}
				if got, expected := doc.String(), want.String(); got != expected {
					t.Fatalf("body = %s, want %s", got, expected)
				}
			})
		}
	}
}

func TestPayloadReplacePathsWildcardArrayParent(t *testing.T) {
	for _, tt := range []struct {
		name, input, prefix, want string
		writes                    []string
	}{
		{
			name:   "scalar before array",
			input:  `{"tool_choice":"auto","tools":[{"type":"function","description":"old name"}]}`,
			prefix: "tool*",
			want:   `{"tool_choice":"auto","tools":[{"type":"function","description":"new name"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "array before scalar",
			input:  `{"tools":[{"type":"function","description":"old name"}],"tool_choice":"auto"}`,
			prefix: "tool*",
			want:   `{"tools":[{"type":"function","description":"new name"}],"tool_choice":"auto"}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "object before array",
			input:  `{"tool_choice":{"type":"auto"},"tools":[{"type":"function","description":"old name"}]}`,
			prefix: "tool*",
			want:   `{"tool_choice":{"type":"auto"},"tools":[{"type":"function","description":"new name"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "question mark wildcard",
			input:  `{"toolx":false,"tools":[{"type":"function","description":"old name"}]}`,
			prefix: "tool?",
			want:   `{"toolx":false,"tools":[{"type":"function","description":"new name"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "escaped dot before wildcard",
			input:  `{"tool.choice":"auto","tool.list":[{"type":"function","description":"old name"}]}`,
			prefix: `tool\.*`,
			want:   `{"tool.choice":"auto","tool.list":[{"type":"function","description":"new name"}]}`,
			writes: []string{`tool\.list.0.description`},
		},
		{
			name:   "nested wildcard prefixes",
			input:  `{"groupA":{"tool_choice":"auto"},"groupB":{"tool_choice":"auto","tools":[{"type":"function","description":"old name"}]}}`,
			prefix: "group*.tool*",
			want:   `{"groupA":{"tool_choice":"auto"},"groupB":{"tool_choice":"auto","tools":[{"type":"function","description":"new name"}]}}`,
			writes: []string{"groupB.tools.0.description"},
		},
		{
			name:   "wildcard after array projection",
			input:  `{"groups":[{"tool_choice":"auto","tools":[{"type":"function","description":"old name"}]}]}`,
			prefix: "groups.#.tool*",
			want:   `{"groups":[{"tool_choice":"auto","tools":[{"type":"function","description":"new name"}]}]}`,
			writes: []string{"groups.0.tools.0.description"},
		},
		{
			name:   "empty array remains selected",
			input:  `{"tool_empty":[],"tools":[{"type":"function","description":"old name"}]}`,
			prefix: "tool*",
			want:   `{"tool_empty":[],"tools":[{"type":"function","description":"old name"}]}`,
		},
		{
			name:   "only the first array parent is selected",
			input:  `{"tool_first":[{"type":"function","description":"old name"}],"tools":[{"type":"function","description":"old name"}]}`,
			prefix: "tool*",
			want:   `{"tool_first":[{"type":"function","description":"new name"}],"tools":[{"type":"function","description":"old name"}]}`,
			writes: []string{"tool_first.0.description"},
		},
		{
			name:   "first query still commits its parent before array suffix",
			input:  `{"tree":[{"name":"selected","tools":"auto"}],"tools":[{"name":"selected","tools":[{"type":"function","description":"old name"}]}]}`,
			prefix: `t*.#(name=="selected").tools`,
			want:   `{"tree":[{"name":"selected","tools":"auto"}],"tools":[{"name":"selected","tools":[{"type":"function","description":"old name"}]}]}`,
		},
		{
			name:   "escaped wildcard does not select another key",
			input:  `{"tool*":"auto","tools":[{"type":"function","description":"old name"}]}`,
			prefix: `tool\*`,
			want:   `{"tool*":"auto","tools":[{"type":"function","description":"old name"}]}`,
		},
	} {
		for _, suffix := range []string{`.#(type=="function")#.description`, ".#.description"} {
			t.Run(tt.name+suffix, func(t *testing.T) {
				for _, prepared := range []bool{false, true} {
					doc := ojson.NewDocString("\n  " + tt.input)
					want := ojson.NewDocString("\n  " + tt.want)
					if prepared {
						for _, d := range []*ojson.Doc{doc, want} {
							if errSet := d.Set("prepared", true); errSet != nil {
								t.Fatal(errSet)
							}
						}
					}
					rule := &config.PayloadReplaceRule{
						Target:       config.PayloadReplaceTarget{Paths: []string{tt.prefix + suffix}},
						Replacements: []config.PayloadReplacement{{"old name": "new name"}},
					}
					var writes []string
					mutated := replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) })
					if mutated != (len(tt.writes) > 0) || !reflect.DeepEqual(writes, tt.writes) {
						t.Fatalf("prepared=%v mutated=%v writes=%v, want writes=%v", prepared, mutated, writes, tt.writes)
					}
					if got := doc.String(); got != want.String() {
						t.Fatalf("prepared=%v body = %s, want %s", prepared, got, want.String())
					}
				}
			})
		}
	}
}

func TestPayloadReplacePathsWildcardFirstMatch(t *testing.T) {
	for _, tt := range []struct {
		name, input, path, want string
		writes                  []string
	}{
		{
			name:   "skip parent without query match",
			input:  `{"tree":[{"name":"other"}],"tools":[{"name":"hero","description":"hero"}]}`,
			path:   `t*.#(name=="hero").description`,
			want:   `{"tree":[{"name":"other"}],"tools":[{"name":"hero","description":"friend"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "AND query skips parent without a qualifying element",
			input:  `{"tree":[{"name":"other","enabled":true}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:   `t*.#(name=="hero" && enabled==true).description`,
			want:   `{"tree":[{"name":"other","enabled":true}],"tools":[{"name":"hero","enabled":true,"description":"friend"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "OR query matches its second term in a later parent",
			input:  `{"tree":[{"name":"other","enabled":false}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:   `t*.#(name=="absent" || enabled==true).description`,
			want:   `{"tree":[{"name":"other","enabled":false}],"tools":[{"name":"hero","enabled":true,"description":"friend"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "compound first query skips an empty array",
			input:  `{"tree":[],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:   `t*.#(name=="hero" && enabled==true).description`,
			want:   `{"tree":[],"tools":[{"name":"hero","enabled":true,"description":"friend"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "compound query with nested wildcard parents",
			input:  `{"groupA":{"tree":[{"name":"other","enabled":true}]},"groupB":{"tools":[{"name":"hero","enabled":true,"description":"hero"}]}}`,
			path:   `g*.t*.#(name=="hero" && enabled==true).description`,
			want:   `{"groupA":{"tree":[{"name":"other","enabled":true}]},"groupB":{"tools":[{"name":"hero","enabled":true,"description":"friend"}]}}`,
			writes: []string{"groupB.tools.0.description"},
		},
		{
			name:  "compound query without any qualifying parent",
			input: `{"tree":[{"name":"other","enabled":true}],"tools":[{"name":"hero","enabled":false,"description":"hero"}]}`,
			path:  `t*.#(name=="hero" && enabled==true).description`,
			want:  `{"tree":[{"name":"other","enabled":true}],"tools":[{"name":"hero","enabled":false,"description":"hero"}]}`,
		},
		{
			name:  "AND query commits the first qualifying element despite a missing suffix",
			input: `{"tree":[{"name":"hero","enabled":true},{"name":"hero","enabled":true,"description":"hero"}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:  `t*.#(name=="hero" && enabled==true).description`,
			want:  `{"tree":[{"name":"hero","enabled":true},{"name":"hero","enabled":true,"description":"hero"}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
		},
		{
			name:  "OR query commits the first qualifying element despite a nonstring suffix",
			input: `{"tree":[{"name":"hero","enabled":true,"description":null}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:  `t*.#(name=="hero" || enabled==true).description`,
			want:  `{"tree":[{"name":"hero","enabled":true,"description":null}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
		},
		{
			name:  "earlier native first query remains committed",
			input: `{"tree":[{"name":"selected","items":[{"name":"other","enabled":true}]}],"tools":[{"name":"selected","items":[{"name":"hero","enabled":true,"description":"hero"}]}]}`,
			path:  `t*.#(name=="selected").items.#(name=="hero" && enabled==true).description`,
			want:  `{"tree":[{"name":"selected","items":[{"name":"other","enabled":true}]}],"tools":[{"name":"selected","items":[{"name":"hero","enabled":true,"description":"hero"}]}]}`,
		},
		{
			name:  "compound all match query keeps the first array parent",
			input: `{"tree":[{"name":"other","enabled":true}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:  `t*.#(name=="hero" && enabled==true)#.description`,
			want:  `{"tree":[{"name":"other","enabled":true}],"tools":[{"name":"hero","enabled":true,"description":"hero"}]}`,
		},
		{
			name:   "skip nonarray parent",
			input:  `{"tree":"hero","tools":[{"name":"hero","description":"hero"}]}`,
			path:   `t*.#(name=="hero").description`,
			want:   `{"tree":"hero","tools":[{"name":"hero","description":"friend"}]}`,
			writes: []string{"tools.0.description"},
		},
		{
			name:   "nested wildcard parents",
			input:  `{"groupA":{"tree":[{"name":"other"}]},"groupB":{"tree":[{"name":"other"}],"tools":[{"name":"hero","description":"hero"}]}}`,
			path:   `g*.t*.#(name=="hero").description`,
			want:   `{"groupA":{"tree":[{"name":"other"}]},"groupB":{"tree":[{"name":"other"}],"tools":[{"name":"hero","description":"friend"}]}}`,
			writes: []string{"groupB.tools.0.description"},
		},
		{
			name:   "first query inside all match query",
			input:  `{"groups":[{"enabled":true,"tree":[{"name":"other"}],"tools":[{"name":"hero","description":"hero"}]}]}`,
			path:   `groups.#(enabled==true)#.t*.#(name=="hero").description`,
			want:   `{"groups":[{"enabled":true,"tree":[{"name":"other"}],"tools":[{"name":"hero","description":"friend"}]}]}`,
			writes: []string{"groups.0.tools.0.description"},
		},
		{
			name:   "all match query after first query",
			input:  `{"tree":[{"name":"other"}],"tools":[{"name":"hero","parts":[{"type":"text","text":"hero"},{"type":"text","text":"hero"}]}]}`,
			path:   `t*.#(name=="hero").parts.#(type=="text")#.text`,
			want:   `{"tree":[{"name":"other"}],"tools":[{"name":"hero","parts":[{"type":"text","text":"friend"},{"type":"text","text":"friend"}]}]}`,
			writes: []string{"tools.0.parts.0.text", "tools.0.parts.1.text"},
		},
		{
			name:  "missing suffix stops at first matching element and parent",
			input: `{"tree":[{"name":"hero"},{"name":"hero","description":"hero"}],"tools":[{"name":"hero","description":"hero"}]}`,
			path:  `t*.#(name=="hero").description`,
			want:  `{"tree":[{"name":"hero"},{"name":"hero","description":"hero"}],"tools":[{"name":"hero","description":"hero"}]}`,
		},
		{
			name:  "nonstring suffix stops at first matching parent",
			input: `{"tree":[{"name":"hero","description":null}],"tools":[{"name":"hero","description":"hero"}]}`,
			path:  `t*.#(name=="hero").description`,
			want:  `{"tree":[{"name":"hero","description":null}],"tools":[{"name":"hero","description":"hero"}]}`,
		},
		{
			name:   "matching parents still select only the first",
			input:  `{"tree":[{"name":"hero","description":"hero"}],"tools":[{"name":"hero","description":"hero"}]}`,
			path:   `t*.#(name=="hero").description`,
			want:   `{"tree":[{"name":"hero","description":"friend"}],"tools":[{"name":"hero","description":"hero"}]}`,
			writes: []string{"tree.0.description"},
		},
		{
			name:  "escaped wildcard remains literal",
			input: `{"t*":[{"name":"other"}],"tools":[{"name":"hero","description":"hero"}]}`,
			path:  `t\*.#(name=="hero").description`,
			want:  `{"t*":[{"name":"other"}],"tools":[{"name":"hero","description":"hero"}]}`,
		},
		{
			name:   "logical query remains supported",
			input:  `{"items":[{"name":"hero","enabled":false,"description":"hero"},{"name":"hero","enabled":true,"description":"hero"}]}`,
			path:   `items.#(name=="hero" && enabled==true).description`,
			want:   `{"items":[{"name":"hero","enabled":false,"description":"hero"},{"name":"hero","enabled":true,"description":"friend"}]}`,
			writes: []string{"items.1.description"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDocString(tt.input)
			rule := &config.PayloadReplaceRule{
				Target:       config.PayloadReplaceTarget{Paths: []string{tt.path}},
				Replacements: []config.PayloadReplacement{{"hero": "friend"}},
			}
			var writes []string
			mutated := replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) })
			if mutated != (len(tt.writes) > 0) || !reflect.DeepEqual(writes, tt.writes) {
				t.Fatalf("mutated=%v writes=%v, want writes=%v", mutated, writes, tt.writes)
			}
			if got := doc.String(); got != tt.want {
				t.Fatalf("body = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPayloadReplaceWritePathIdentity(t *testing.T) {
	const input = `{"values":["hero"]}`
	root := payloadReplacePathStep{parent: input, value: ojson.Get(input, "@this"), index: -1}
	values := payloadReplacePathStep{parent: input, value: ojson.Get(input, "values"), index: -1}
	item := payloadReplacePathStep{parent: `"hero"`, value: ojson.Get(`"hero"`, "@this"), index: -1}
	computed := payloadReplacePathStep{parent: `"hero"`, value: ojson.Get(`"hero"`, "@tostr"), index: -1}
	for _, tt := range []struct {
		name, want string
		steps      []payloadReplacePathStep
	}{
		{"root only", "", []payloadReplacePathStep{root}},
		{"identity around concrete steps", "values.0", []payloadReplacePathStep{root, values, {index: 0}, item}},
		{"computed step", "", []payloadReplacePathStep{root, values, {index: 0}, computed}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Reusing resolved steps must preserve identity handling and separators.
			for range 2 {
				if got := payloadReplaceWritePath(tt.steps); got != tt.want {
					t.Fatalf("path = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func TestPayloadReplacePathsRootIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
		paths, writes     []string
	}{
		{
			name: "root array", input: `[{"role":"user","content":"hero"}]`, want: `[{"role":"user","content":"hero!"}]`,
			paths: []string{`@this.#(role=="user")#.content`, "0.content"}, writes: []string{"0.content"},
		},
		{
			name: "root string is not writable", input: `"hero"`, want: `"hero"`,
			paths: []string{"@this"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, prefix := range []string{"", "\n  "} {
				doc := ojson.NewDocString(prefix + tt.input)
				rule := &config.PayloadReplaceRule{
					Target: config.PayloadReplaceTarget{Paths: tt.paths}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
				}
				var writes []string
				mutated := replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) })
				if mutated != (len(tt.writes) > 0) || !reflect.DeepEqual(writes, tt.writes) {
					t.Fatalf("mutated=%v writes=%v, want writes=%v", mutated, writes, tt.writes)
				}
				if got := doc.String(); got != prefix+tt.want {
					t.Fatalf("body = %s, want %s", got, prefix+tt.want)
				}
			}
		})
	}
}

func TestPayloadReplacePathsWildcardTracking(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
		Models:       []config.PayloadModelRule{{Name: "*"}},
		Target:       config.PayloadReplaceTarget{Paths: []string{"t*.0.output_format"}},
		Replacements: []config.PayloadReplacement{{"png": "webp"}},
	}}}}
	for _, mode := range []string{"bytes", "doc"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(`{"tools":[{"output_format":"png"}]}`)
			doc := ojson.NewDoc(body)
			var touched map[string]bool
			if mode == "doc" {
				touched = ApplyPayloadConfigWithTrackedPathsDoc(cfg, "model", "custom", "", "", doc, nil, "", "", nil, "tools", "tools.0", "tools.0.output_format")
			} else {
				out, writes := ApplyPayloadConfigWithTrackedPaths(cfg, "model", "custom", "", "", body, nil, "", "", nil, "tools", "tools.0", "tools.0.output_format")
				doc, touched = ojson.NewDoc(out), writes
			}
			if got := doc.Get("tools.0.output_format").String(); got != "webp" {
				t.Fatalf("replacement missing: %s", doc.String())
			}
			if !touched["tools"] || !touched["tools.0"] || !touched["tools.0.output_format"] {
				t.Fatalf("concrete tool write not tracked: %v", touched)
			}
		})
	}
}

func TestPayloadReplacePathsNoOp(t *testing.T) {
	input := []byte(`{"object":{"text":"hero"},"array":["hero"],"number":123,"bool":true,"null":null,"text":"unchanged"}`)
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
		Models:       []config.PayloadModelRule{{Name: "*"}},
		Target:       config.PayloadReplaceTarget{Paths: []string{"object", "array", "number", "bool", "null", "text", "missing", "missing.0.text", `array.#(text=="hero")#.text`}},
		Replacements: []config.PayloadReplacement{{"hero": "changed"}},
	}}}}
	out, touched := ApplyPayloadConfigWithTrackedPaths(cfg, "model", "", "", "", input, nil, "", "", nil, "object", "array", "number", "bool", "null", "text", "missing")
	if !bytes.Equal(out, input) || &out[0] != &input[0] || len(touched) != 0 {
		t.Fatalf("no-op changed input or tracked writes: %s, touched=%v", out, touched)
	}
}

func TestPayloadReplacePathsDeferredWritesAndRegex(t *testing.T) {
	grown := strings.Repeat("expanded ", 512)
	escaped := "\"\\\n你好"
	doc := ojson.NewDocString(`{"values":["grow","shrink","escaped","User-42","unchanged"]}`)
	rule := &config.PayloadReplaceRule{
		Target: config.PayloadReplaceTarget{Paths: []string{"values.0", "values.1", "values.2", "values.3", "values.4", "values.0"}},
		Replacements: []config.PayloadReplacement{
			{"grow": grown}, {"shrink": ""}, {"escaped": escaped},
			{"pattern": "[invalid", "replacement": "ignored"},
			{"pattern": `(?i)user-(\d+)`, "replacement": "account-${1}"},
			{"account": "acct"},
		},
	}
	var writes []string
	if !replacePayloadTextDoc(doc, "custom", "", rule, 0, func(path string) { writes = append(writes, path) }) {
		t.Fatal("replacement did not mutate doc")
	}
	if !reflect.DeepEqual(writes, []string{"values.0", "values.1", "values.2", "values.3"}) {
		t.Fatalf("unexpected writes: %v", writes)
	}
	for i, want := range []string{grown, "", escaped, "acct-42", "unchanged"} {
		if got := doc.Get("values." + strconv.Itoa(i)).String(); got != want {
			t.Fatalf("values[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestPayloadReplacePathsStageOrderAndGates(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "alias-*", Protocol: "custom", FromProtocol: "responses", Headers: map[string]string{"X-Tier": "prod-*"}, Match: []map[string]any{{"stage": "ready"}}, Exist: []string{"metadata.description"}}}
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"stage": "ready"}}},
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"metadata.description": "hero"}}},
		Replace: []config.PayloadReplaceRule{
			{Models: append(models, models[0]), Target: config.PayloadReplaceTarget{Paths: []string{"metadata.description"}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}},
			{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"metadata.description": "hero!"}}}}, Target: config.PayloadReplaceTarget{Paths: []string{"metadata.description"}}, Replacements: []config.PayloadReplacement{{"!": "!!"}}},
		},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"metadata.description": "hero!!"}}}}, Params: []string{"stage"}}},
	}}
	for _, header := range []string{"prod-a", "dev"} {
		doc := ojson.NewDocString(`{"metadata":{"description":"original"}}`)
		ApplyPayloadConfigWithRequestDoc(cfg, "model", "custom", "responses", "", doc, nil, "alias-model", "", http.Header{"X-Tier": {header}})
		want := "hero"
		if header == "prod-a" {
			want = "hero!!"
		}
		if got := doc.Get("metadata.description").String(); got != want || doc.Get("stage").Exists() != (header == "dev") {
			t.Fatalf("unexpected stage order or gates: %s", doc.String())
		}
	}
}

func TestPayloadReplacePathsCodexImageToolTracking(t *testing.T) {
	const original = `{"model":"gpt-6-astra","input":[]}`
	for _, tt := range []struct {
		name, path, from, to, effort string
		wantTool                     bool
	}{
		{"explicit tool edit", `tools.#(type=="image_generation")#.output_format`, "png", "webp", "medium", true},
		{"wildcard tool edit", "t*.0.output_format", "png", "webp", "medium", true},
		{"projection tool edit", "tools.#.output_format", "png", "webp", "medium", true},
		{"identity tool edit", `tools.#(type=="image_generation")#.@this.output_format`, "png", "webp", "medium", true},
		{"unchanged tool remains automatic", "tools.0.output_format", "jpeg", "webp", "medium", false},
		{"configured reasoning retracts automatic tool", "reasoning.effort", "xhigh", "medium", "xhigh", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
				Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Paths: []string{tt.path}},
				Replacements: []config.PayloadReplacement{{tt.from: tt.to}},
			}}}}
			doc := ojson.NewDocString(`{"model":"gpt-6-astra","input":[],"reasoning":{"effort":"` + tt.effort + `"},"tools":[{"type":"image_generation","output_format":"png"}]}`)
			ApplyPayloadConfigWithRequestForExecutorDoc(cfg, "codex", "gpt-6-astra", "codex", "", "", doc, []byte(original), "", "", nil)
			if doc.Get("tools").Exists() != tt.wantTool {
				t.Fatalf("explicit/automatic image tool tracking failed: %s", doc.String())
			}
			if tt.wantTool && doc.Get("tools.0.output_format").String() != "webp" {
				t.Fatalf("explicit tool edit lost: %s", doc.String())
			}
			if got := doc.Get("reasoning.effort").String(); got != "medium" {
				t.Fatalf("reasoning was rewritten after payload: %s", doc.String())
			}
		})
	}
}

func TestPayloadReplacePathsIdentityToolTracking(t *testing.T) {
	const original = `{"model":"gpt-6-astra","input":[]}`
	for _, tt := range []struct {
		name, path, from string
		wantTool         bool
	}{
		{"identity edit", `tools.0.labels.#(=="hero")#.@this`, "hero", true},
		{"identity no match", `tools.0.labels.#(=="hero")#.@this`, "absent", false},
		{"computed string", `tools.0.labels.#(=="hero")#.@tostr`, "hero", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
				Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Paths: []string{tt.path}},
				Replacements: []config.PayloadReplacement{{tt.from: "friend"}},
			}}}}
			const input = `{"model":"gpt-6-astra","input":[],"reasoning":{"effort":"medium"},"tools":[{"type":"image_generation","output_format":"png","labels":["hero"]}]}`
			for _, mode := range []string{"bytes", "doc"} {
				doc := ojson.NewDocString(input)
				var touched map[string]bool
				if mode == "doc" {
					touched = ApplyPayloadConfigWithTrackedPathsForExecutorDoc(cfg, "codex", "gpt-6-astra", "codex", "", "", doc, []byte(original), "", "", nil, "tools.0", "tools.0.labels.0")
				} else {
					out, writes := ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, "codex", "gpt-6-astra", "codex", "", "", []byte(input), []byte(original), "", "", nil, "tools.0", "tools.0.labels.0")
					doc, touched = ojson.NewDoc(out), writes
				}
				if doc.Get("tools").Exists() != tt.wantTool || touched["tools.0"] != tt.wantTool || touched["tools.0.labels.0"] != tt.wantTool {
					t.Fatalf("%s: unexpected tracking: body=%s touched=%v", mode, doc.String(), touched)
				}
				if tt.wantTool && doc.Get("tools.0.labels.0").String() != "friend" {
					t.Fatalf("%s: identity edit did not change source node: %s", mode, doc.String())
				}
				if doc.Get("reasoning.effort").String() != "medium" {
					t.Fatalf("%s: configured reasoning changed: %s", mode, doc.String())
				}
			}
		})
	}
}

func TestPayloadReplacePathsFinalizerRetries(t *testing.T) {
	raw := []byte(`{"request":{"metadata":{"description":"hero"}}}`)
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
		Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Paths: []string{"metadata.description"}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
	}}}}
	finalize := NewPayloadFinalizer(cfg, "custom", "model", "custom", "request", raw, cliproxyexecutor.Request{Model: "model"}, cliproxyexecutor.Options{})
	for attempt := 0; attempt < 3; attempt++ {
		before := PayloadConfigEvaluationCount()
		doc := ojson.NewDoc(append([]byte(nil), raw...))
		FinalizePayloadDoc(WithPayloadFinalizer(context.Background(), finalize), doc)
		if got := doc.Get("request.metadata.description").String(); got != "hero!" || PayloadConfigEvaluationCount()-before != 1 {
			t.Fatalf("attempt %d: replacement repeated or missing: %s", attempt, doc.String())
		}
	}
	if got := ojson.GetBytes(raw, "request.metadata.description").String(); got != "hero" {
		t.Fatal("finalizer mutated original request")
	}
}
