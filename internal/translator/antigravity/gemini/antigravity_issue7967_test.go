package gemini

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIssue7967MergesAntigravitySystemInstructionParts(t *testing.T) {
	input := []byte(`{
        "systemInstruction":{"role":"system","parts":[{"text":"first rule"},{"text":"second rule"}]},
        "contents":[{"role":"user","parts":[{"text":"hello"}]}]
    }`)

	out, err := ConvertGeminiRequestToAntigravity("gemini-3-flash", input, false)
	if err != nil {
		t.Fatal(err)
	}
	systemInstruction := ojson.GetBytesNoCopy(out, "request.systemInstruction")
	if got := systemInstruction.Get("role").String(); got != "user" {
		t.Fatalf("systemInstruction.role = %q, want user; output=%s", got, out)
	}
	parts := systemInstruction.Get("parts").Array()
	if len(parts) != 1 {
		t.Fatalf("systemInstruction.parts length = %d, want 1; output=%s", len(parts), out)
	}
	if got := parts[0].Get("text").String(); got != "first rule\n\nsecond rule" {
		t.Fatalf("systemInstruction.parts[0].text = %q, want merged text; output=%s", got, out)
	}
}

func TestIssue7967PreservesStructuredFunctionResponseFields(t *testing.T) {
	input := []byte(`{
        "contents":[
            {"role":"model","parts":[{"functionCall":{"name":"Read","id":"call-1","args":{}}}]},
            {"role":"user","parts":[{"functionResponse":{"name":"Read","id":"call-1","response":{"result":"opaque result","output":"native output"}}}]}
        ]
    }`)
	out, err := ConvertGeminiRequestToAntigravity("gemini-3-flash", input, false)
	if err != nil {
		t.Fatal(err)
	}
	response := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0.functionResponse.response")
	if response.Get("result").String() != "opaque result" || response.Get("output").String() != "native output" {
		t.Fatalf("structured response fields changed: %s", response.Raw)
	}
}
