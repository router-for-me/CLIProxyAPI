package helps

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// BenchmarkPayloadReplacePathsOverlapping measures repeated selection of long
// strings with a no-op regex, where deduplication must avoid redundant work.
func BenchmarkPayloadReplacePathsOverlapping(b *testing.B) {
	body, errMarshal := json.Marshal(map[string]any{"values": []string{strings.Repeat("hero ", 8192)}})
	if errMarshal != nil {
		b.Fatal(errMarshal)
	}
	for _, count := range []int{1, 8} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			paths := make([]string, count)
			for i := range paths {
				paths[i] = "values.0"
			}
			rule := &config.PayloadReplaceRule{
				Target:       config.PayloadReplaceTarget{Paths: paths},
				Replacements: []config.PayloadReplacement{{"pattern": `hero\s+`, "replacement": "hero "}},
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				doc := ojson.NewDoc(body)
				if replacePayloadTextDoc(doc, "custom", "", rule, 0, func(string) {}) {
					b.Fatal("no-op regex mutated document")
				}
			}
		})
	}
}

// BenchmarkPayloadReplacePathsSelection isolates repeated prefix selection under
// an outer projection, without writeback or final serialization costs.
func BenchmarkPayloadReplacePathsSelection(b *testing.B) {
	group := `{"tool_choice":"auto","tools":[{"text":"hero","content":"hero","parts":[{"text":"hero"}]}],"tool*":[{"text":"hero"}]}`
	body := `{"groups":[` + strings.TrimSuffix(strings.Repeat(group+",", 128), ",") + `]}`
	for _, tt := range []struct{ name, path string }{
		{"Wildcard", "groups.#.tool*.#.text"},
		{"EscapedWildcard", `groups.#.tool\*.#.text`},
		{"QueryPattern", `groups.#.tools.#(content%"*hero*").parts.#.text`},
		{"Direct", "groups.#.tools.0.text"},
	} {
		b.Run(tt.name, func(b *testing.B) {
			rule := &config.PayloadReplaceRule{Target: config.PayloadReplaceTarget{Paths: []string{tt.path}}, Replacements: []config.PayloadReplacement{{"absent": "changed"}}}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				doc := ojson.NewDocString(body)
				if replacePayloadTextDoc(doc, "custom", "", rule, 0, func(string) {}) {
					b.Fatal("no-match selection mutated document")
				}
			}
		})
	}
}

// BenchmarkPayloadReplacePathsScaling separates no-match selection from real
// mutations, including writeback and final serialization. Faster selection alone
// does not imply linear total mutation cost.
func BenchmarkPayloadReplacePathsScaling(b *testing.B) {
	for _, count := range []int{128, 1000} {
		messages := make([]any, count)
		text := "hero " + strings.Repeat("x", 1019)
		for i := range messages {
			messages[i] = map[string]any{"role": "user", "content": text}
		}
		body, errMarshal := json.Marshal(map[string]any{"messages": messages})
		if errMarshal != nil {
			b.Fatal(errMarshal)
		}
		for _, selection := range []string{"Paths", "Role"} {
			for _, operation := range []string{"NoMatch", "Writes"} {
				b.Run(selection+"/"+operation+"/"+strconv.Itoa(count), func(b *testing.B) {
					target := config.PayloadReplaceTarget{Role: "user", All: true}
					if selection == "Paths" {
						target = config.PayloadReplaceTarget{Paths: []string{`messages.#(role=="user")#.content`}}
					}
					from := "absent replacement source"
					if operation == "Writes" {
						from = "hero"
					}
					cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
						Models:       []config.PayloadModelRule{{Name: "*"}},
						Target:       target,
						Replacements: []config.PayloadReplacement{{from: "changed"}},
					}}}}
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					for b.Loop() {
						_ = ApplyPayloadConfigWithRoot(cfg, "model", "openai", "", body, nil, "", "")
					}
				})
			}
		}
	}
}
