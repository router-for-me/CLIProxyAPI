package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToAntigravity(t *testing.T) {
	input := []byte(`{
		"model": "gemini-pro",
		"contents": [
			{"role": "user", "parts": [{"text": "hello"}]},
			{"parts": [{"text": "hi"}]}
		],
		"system_instruction": {"parts": [{"text": "be kind"}]}
	}`)
	
	got := ConvertGeminiRequestToAntigravity("gemini-1.5-pro", input, false)
	
	res := gjson.ParseBytes(got)
	if res.Get("model").String() != "gemini-1.5-pro" {
		t.Errorf("expected model gemini-1.5-pro, got %q", res.Get("model").String())
	}
	
	// Check role normalization
	role1 := res.Get("request.contents.0.role").String()
	role2 := res.Get("request.contents.1.role").String()
	if role1 != "user" || role2 != "model" {
		t.Errorf("expected roles user/model, got %q/%q", role1, role2)
	}
	
	// Check system instruction rename
	if !res.Get("request.systemInstruction").Exists() {
		t.Error("expected systemInstruction to exist")
	}
}

func TestFixCLIToolResponse(t *testing.T) {
	input := `{
		"request": {
			"contents": [
				{"role": "user", "parts": [{"text": "call tool"}]},
				{"role": "model", "parts": [{"functionCall": {"name": "test", "args": {}}}]},
				{"role": "user", "parts": [{"functionResponse": {"name": "test", "response": {"result": "ok"}}}]}
			]
		}
	}`
	
	got, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}
	
	res := gjson.Parse(got)
	contents := res.Get("request.contents").Array()
	if len(contents) != 3 {
		t.Errorf("expected 3 content blocks, got %d", len(contents))
	}
	
	lastRole := contents[2].Get("role").String()
	if lastRole != "function" {
		t.Errorf("expected last role to be function, got %q", lastRole)
	}
}
