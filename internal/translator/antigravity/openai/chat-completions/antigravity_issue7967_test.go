package chat_completions

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIssue7967UsesNativeToolResultOutputKey(t *testing.T) {
	input := []byte(`{
        "messages":[
            {"role":"user","content":"inspect"},
            {"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"Read","arguments":"{}"}}]},
            {"role":"tool","tool_call_id":"call-1","content":"tool text"}
        ]
    }`)

	out, err := ConvertOpenAIRequestToAntigravity("gemini-3-flash", input, false)
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
