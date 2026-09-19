package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestInputStringRemainsAUserMessage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		out := ConvertOpenAIResponsesRequestToClaude("claude-haiku-4-5-20251001", []byte(`{"input":"Reply \"lab-ok\".\nNext line","max_output_tokens":64}`), stream)
		if gjson.GetBytes(out, "messages.#").Int() != 1 || gjson.GetBytes(out, "messages.0.role").String() != "user" || gjson.GetBytes(out, "messages.0.content").String() != "Reply \"lab-ok\".\nNext line" {
			t.Fatalf("plain input disappeared or changed: %s", out)
		}
		if gjson.GetBytes(out, "max_tokens").Int() != 64 {
			t.Fatalf("output limit lost: %s", out)
		}
	}
}
