package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureAntigravityGeminiToolResultBoundary(t *testing.T) {
	const singleResult = `{"functionResponse":{"name":"TaskList","id":"call-1","response":{"result":"ok"}}}`
	tests := []struct {
		name      string
		model     string
		contents  string
		wantLabel string
	}{
		{
			name:      "argon high single result",
			model:     "gemini-4-argon-high",
			contents:  `[{"role":"model","parts":[` + singleResult + `]}]`,
			wantLabel: "TaskList",
		},
		{
			name:      "argon parallel results",
			model:     "models/gemini-4-argon",
			contents:  `[{"role":"model","parts":[` + singleResult + `,{"functionResponse":{"name":"Read","id":"call-2","response":{"result":"data"}}}]}]`,
			wantLabel: "TaskList\nRead",
		},
		{
			name:      "argon whitespace text sibling",
			model:     "gemini-4-argon-low",
			contents:  `[{"role":"user","parts":[` + singleResult + `,{"text":"  "}]}]`,
			wantLabel: "TaskList",
		},
		{
			name:     "non-argon gemini untouched",
			model:    "gemini-3.8-flash-high",
			contents: `[{"role":"model","parts":[` + singleResult + `]}]`,
		},
		{
			name:     "claude model untouched",
			model:    "claude-opus-5-5-high",
			contents: `[{"role":"model","parts":[` + singleResult + `]}]`,
		},
		{
			name:     "existing non-empty user text untouched",
			model:    "gemini-4-argon-high",
			contents: `[{"role":"user","parts":[` + singleResult + `,{"text":"continue"}]}]`,
		},
		{
			name:     "historical tool result untouched",
			model:    "gemini-4-argon-high",
			contents: `[{"role":"model","parts":[` + singleResult + `]},{"role":"user","parts":[{"text":"next"}]}]`,
		},
		{
			name:     "nested media tool result untouched",
			model:    "gemini-4-argon-high",
			contents: `[{"role":"model","parts":[{"functionResponse":{"name":"view_image","response":{"result":""},"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}}]}]`,
		},
		{
			name:     "missing function name untouched",
			model:    "gemini-4-argon-high",
			contents: `[{"role":"model","parts":[{"functionResponse":{"id":"call-1","response":{"result":"ok"}}}]}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"request":{"contents":` + tc.contents + `}}`)
			got := EnsureAntigravityGeminiToolResultBoundary(tc.model, payload, "request.contents")
			if tc.wantLabel == "" {
				if !bytes.Equal(got, payload) {
					t.Fatalf("expected payload unchanged, got %s", got)
				}
				return
			}
			if role := gjson.GetBytes(got, "request.contents.0.role").String(); role != "user" {
				t.Fatalf("role = %q, want user", role)
			}
			parts := gjson.GetBytes(got, "request.contents.0.parts").Array()
			if len(parts) < 2 {
				t.Fatalf("expected appended label part, got %s", got)
			}
			if label := parts[len(parts)-1].Get("text").String(); label != tc.wantLabel {
				t.Fatalf("label = %q, want %q", label, tc.wantLabel)
			}
			if again := EnsureAntigravityGeminiToolResultBoundary(tc.model, got, "request.contents"); !bytes.Equal(again, got) {
				t.Fatalf("expected idempotent result, got %s", again)
			}
		})
	}
}
