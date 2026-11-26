package responses

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIssue7967UsesNativeToolResultOutputKey(t *testing.T) {
	input := []byte(`{
        "input":[
            {"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]},
            {"type":"function_call","call_id":"call-1","name":"Read","arguments":"{}"},
            {"type":"function_call_output","call_id":"call-1","output":"tool text"}
        ],
        "tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}]
    }`)

	out, err := ConvertOpenAIResponsesRequestToAntigravity("gemini-3-flash", input, false)
	if err != nil {
		t.Fatal(err)
	}
	response := ojson.GetBytesNoCopy(out, "request.contents.2.parts.0.functionResponse.response")
	if got := response.Get("output").String(); got != "tool text" {
		t.Fatalf("response.output = %q, want tool text; output=%s", got, out)
	}
	if response.Get("result").Exists() {
		t.Fatalf("response.result must not be emitted; output=%s", out)
	}
}
