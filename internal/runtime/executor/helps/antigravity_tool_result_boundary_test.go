package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestArgonToolResultBoundaryScope(t *testing.T) {
	const result = `{"functionResponse":{"name":"read","id":"one","response":{"result":"data"}}}`
	for _, tc := range []struct {
		name, model, contents string
		wantLabel             string
	}{
		{"parallel", "gemini-4-argon-high", `[{"role":"model","parts":[` + result + `,{"functionResponse":{"name":"list","response":{}}}]}]`, "read\nlist"},
		{"empty sibling", "gemini-4-argon-low", `[{"role":"user","parts":[` + result + `,{"text":""}]}]`, "read"},
		{"other Gemini", "gemini-3.8-flash", `[{"role":"model","parts":[` + result + `]}]`, ""},
		{"real text", "gemini-4-argon", `[{"role":"user","parts":[` + result + `,{"text":"existing"}]}]`, ""},
		{"historical result", "gemini-4-argon", `[{"role":"model","parts":[` + result + `]},{"role":"user","parts":[{"text":"continue"}]}]`, ""},
		{"media", "gemini-4-argon", `[{"role":"model","parts":[{"functionResponse":{"name":"image","response":{},"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}}]}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"request":{"contents":` + tc.contents + `}}`)
			got := EnsureAntigravityGeminiToolResultBoundary(tc.model, payload, "request.contents")
			if tc.wantLabel == "" {
				if !bytes.Equal(got, payload) {
					t.Fatal("changed an unaffected payload")
				}
				return
			}
			parts := gjson.GetBytes(got, "request.contents.0.parts").Array()
			if label := parts[len(parts)-1].Get("text").String(); label != tc.wantLabel {
				t.Fatalf("label = %q, want %q", label, tc.wantLabel)
			}
			if gjson.GetBytes(got, "request.contents.0.role").String() != "user" {
				t.Fatal("result turn is not user-side")
			}
		})
	}
}
