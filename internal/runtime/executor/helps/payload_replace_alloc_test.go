package helps

import (
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPayloadReplaceNoMatchAllocationsDoNotScaleWithTextFields(t *testing.T) {
	for _, tt := range []struct {
		name, protocol, root, role, layout string
	}{
		{"chat strings", "openai", "", "user", "strings"},
		{"chat blocks", "openai", "", "developer", "blocks"},
		{"claude system blocks", "claude", "", "system", "system-blocks"},
		{"responses blocks", "codex", "", "developer", "blocks"},
		{"gemini parts", "gemini", "", "user", "parts"},
		{"gemini system parts", "gemini", "", "system", "system-parts"},
		{"antigravity rooted system parts", "antigravity", "request", "system", "system-parts"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			makeBody := func(count int) []byte {
				items := make([]any, count)
				for i := range items {
					switch tt.layout {
					case "strings":
						items[i] = map[string]any{"role": tt.role, "content": "unchanged"}
					case "parts":
						items[i] = map[string]any{"role": tt.role, "parts": []any{map[string]any{"text": "unchanged"}, map[string]any{"text": "unchanged"}}}
					case "system-parts":
						items[i] = map[string]any{"text": "unchanged"}
					default:
						typ := "text"
						if tt.protocol == "codex" {
							typ = "input_text"
						}
						items[i] = map[string]any{"type": typ, "text": "unchanged"}
					}
				}
				var payload map[string]any
				switch tt.layout {
				case "strings":
					payload = map[string]any{"messages": items}
				case "blocks":
					key := "messages"
					if tt.protocol == "codex" {
						key = "input"
					}
					payload = map[string]any{key: []any{map[string]any{"role": tt.role, "content": items}}}
				case "parts":
					payload = map[string]any{"contents": items}
				case "system-blocks":
					payload = map[string]any{"system": items}
				case "system-parts":
					payload = map[string]any{"systemInstruction": map[string]any{"parts": items}}
				}
				if tt.root != "" {
					payload = map[string]any{tt.root: payload}
				}
				body, errMarshal := json.Marshal(payload)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				return body
			}
			for _, source := range []string{"missing", "unchanged"} {
				rule := &config.PayloadReplaceRule{
					Target:       config.PayloadReplaceTarget{Role: tt.role, All: true},
					Replacements: []config.PayloadReplacement{{source: "unchanged"}},
				}
				measure := func(body []byte) float64 {
					return testing.AllocsPerRun(20, func() {
						doc := ojson.NewDoc(body)
						if replacePayloadTextDoc(doc, tt.protocol, tt.root, rule, 0, func(path string) { t.Errorf("unexpected write: %s", path) }) {
							t.Error("no-op replacement changed the document")
						}
					})
				}
				// Cross the integer formatting cache boundary for message and block indexes.
				small := measure(makeBody(1))
				large := measure(makeBody(256))
				if large > small {
					t.Fatalf("source=%q allocations grew with unchanged text fields: small=%v large=%v", source, small, large)
				}
			}
		})
	}
}
