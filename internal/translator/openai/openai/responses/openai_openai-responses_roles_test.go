package responses

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesInstructionRoles(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, arrayContent := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/array=%t", stream, arrayContent), func(t *testing.T) {
				wantRoles := []string{"system", "system", "developer", "user", "assistant", "developer", "user", "developer"}
				wantTexts := []string{"base instructions", "session instructions", "mode: plan", "mode: default is only user text", "acknowledged", "mode: default", "continue", "mode: plan"}
				input := make([]any, 0, len(wantRoles)-1)
				for index := 1; index < len(wantRoles); index++ {
					var content any = wantTexts[index]
					if arrayContent {
						contentType := "input_text"
						if wantRoles[index] == "assistant" {
							contentType = "output_text"
						}
						content = []any{map[string]any{"type": contentType, "text": wantTexts[index]}}
					}
					input = append(input, map[string]any{"type": "message", "role": wantRoles[index], "content": content})
				}
				payload, errMarshal := json.Marshal(map[string]any{"instructions": wantTexts[0], "input": input})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				output, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", payload, stream)
				if errConvert != nil {
					t.Fatal(errConvert)
				}
				if gotStream := gjson.GetBytes(output, "stream").Bool(); gotStream != stream {
					t.Fatalf("stream = %t, want %t", gotStream, stream)
				}
				messages := gjson.GetBytes(output, "messages").Array()
				if len(messages) != len(wantRoles) {
					t.Fatalf("message count = %d, want %d; output=%s", len(messages), len(wantRoles), output)
				}
				for index, message := range messages {
					if got := message.Get("role").String(); got != wantRoles[index] {
						t.Errorf("message %d role = %q, want %q", index, got, wantRoles[index])
					}
					text := message.Get("content")
					if text.IsArray() {
						text = text.Get("0.text")
					}
					if text.String() != wantTexts[index] {
						t.Errorf("message %d text = %q, want %q", index, text.String(), wantTexts[index])
					}
				}
			})
		}
	}
}
