package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestPrepareAntigravityClaudeStructuredOutput_ModelVariants(t *testing.T) {
	payload := []byte(`{"request":{"generationConfig":{"responseSchema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]},"responseMimeType":"application/json"},"toolConfig":{"functionCallingConfig":{"mode":"VALIDATED","allowedFunctionNames":["lookup"]}}}}`)

	cases := []struct {
		model       string
		wantConvert bool
	}{
		{model: "claude-opus-5-5", wantConvert: true},
		{model: "claude-opus-5-5-low", wantConvert: true},
		{model: "claude-opus-5-5-medium", wantConvert: true},
		{model: "claude-opus-5-5-high", wantConvert: true},
		{model: "claude-opus-5-5-max", wantConvert: true},
		{model: "claude-opus-5-5-reasoning", wantConvert: true},
		{model: "claude-sonnet-5-5", wantConvert: true},
		{model: "claude-sonnet-5-5-low", wantConvert: true},
		{model: "claude-sonnet-5-5-medium", wantConvert: true},
		{model: "claude-sonnet-5-5-high", wantConvert: true},
		{model: "claude-sonnet-5-5-max", wantConvert: true},
		{model: "claude-sonnet-5-5-reasoning", wantConvert: true},
		{model: "claude-opus-5-5-low-20261001[1m](high)", wantConvert: true},
		{model: "claude-sonnet-5-5-max-20261001", wantConvert: true},
		{model: "claude-opus-5-5-custom", wantConvert: false},
		{model: "claude-opus-5-50-low", wantConvert: false},
		{model: "claude-sonnet-5-50-max", wantConvert: false},
		{model: "claude-haiku-5-5", wantConvert: false},
		{model: "claude-opus-4-6", wantConvert: false},
		{model: "other/claude-opus-5-5-max", wantConvert: false},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			out, converted := PrepareAntigravityClaudeStructuredOutput(tc.model, payload)
			if converted != tc.wantConvert {
				t.Fatalf("PrepareAntigravityClaudeStructuredOutput(%q) converted = %t, want %t", tc.model, converted, tc.wantConvert)
			}
			if !tc.wantConvert {
				if !ojson.GetBytes(out, "request.generationConfig.responseSchema").Exists() {
					t.Fatalf("expected native responseSchema to remain untouched for %q", tc.model)
				}
				if mode := ojson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); mode != "VALIDATED" {
					t.Fatalf("tool mode = %q, want VALIDATED untouched for %q", mode, tc.model)
				}
				return
			}
			if ojson.GetBytes(out, "request.generationConfig.responseSchema").Exists() {
				t.Fatalf("expected responseSchema to be stripped for %q", tc.model)
			}
			if ojson.GetBytes(out, "request.generationConfig.responseMimeType").Exists() {
				t.Fatalf("expected responseMimeType to be stripped for %q", tc.model)
			}
			if mode := ojson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); mode != "AUTO" {
				t.Fatalf("tool mode = %q, want AUTO for %q", mode, tc.model)
			}
			if ojson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames").Exists() {
				t.Fatalf("expected allowedFunctionNames to be deleted after AUTO downgrade for %q", tc.model)
			}
			sysText := ojson.GetBytes(out, "request.systemInstruction.parts.0.text").String()
			if !strings.Contains(sysText, `"title"`) {
				t.Fatalf("expected structured output instruction containing schema in systemInstruction, got %q", sysText)
			}
		})
	}
}

func TestPrepareAntigravityClaudeStructuredOutput_NormalizesNumericProtoSchemaTypes_Issue8032(t *testing.T) {
	payload := []byte(`{
		"request": {
			"generationConfig": {
				"responseSchema": {
					"type": 6,
					"properties": {
						"title": {"type": 1},
						"tags": {"type": 5, "items": {"type": 1}}
					},
					"required": ["title"]
				},
				"responseMimeType": "application/json"
			}
		}
	}`)

	out, converted := PrepareAntigravityClaudeStructuredOutput("claude-sonnet-5-5", payload)
	if !converted {
		t.Fatalf("expected structured output downgrade for claude-sonnet-5-5")
	}
	sysText := ojson.GetBytes(out, "request.systemInstruction.parts.0.text").String()
	start := strings.Index(sysText, "{")
	end := strings.LastIndex(sysText, "}")
	if start < 0 || end <= start {
		t.Fatalf("expected embedded JSON schema in systemInstruction, got: %s", sysText)
	}
	embedded := ojson.Parse(sysText[start : end+1])
	for _, tc := range []struct {
		path string
		want string
	}{
		{"type", "object"},
		{"properties.title.type", "string"},
		{"properties.tags.type", "array"},
		{"properties.tags.items.type", "string"},
	} {
		got := embedded.Get(tc.path)
		if got.Type != ojson.String || got.String() != tc.want {
			t.Fatalf("embedded schema %s = %s (type %v), want string %q; sysText=%s", tc.path, got.Raw, got.Type, tc.want, sysText)
		}
	}
}
