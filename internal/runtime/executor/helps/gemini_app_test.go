package helps

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func appFrame(text string, done bool) string {
	candidate := make([]any, 9)
	candidate[0], candidate[1], candidate[8] = "candidate", []any{text}, []any{1}
	if done {
		candidate[8] = []any{2}
	}
	inner, _ := json.Marshal([]any{nil, []any{"conversation", "response"}, nil, nil, []any{candidate}})
	frame, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(inner)}})
	return fmt.Sprintf("%d\n%s\n", len(frame), frame)
}

func TestGeminiAppPrompt(t *testing.T) {
	input := []byte(`{"systemInstruction":{"parts":[{"text":"Be brief"}]},"contents":[{"role":"user","parts":[{"text":"One"},{"text":" two"}]},{"role":"model","parts":[{"text":"private thought","thought":true},{"text":"Three"}]},{"role":"user","parts":[{"text":"Four"}]}]}`)
	prompt, err := GeminiAppPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "System:\nBe brief\n\nUser:\nOne two\n\nAssistant:\nThree\n\nUser:\nFour" {
		t.Fatalf("unexpected history: %q", prompt)
	}
	for _, input := range []string{
		`{}`, `bad json`, `{"tools":[{"functionDeclarations":[]}]}`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}]}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"test"}}]}]}`,
		`{"generationConfig":{"responseMimeType":"application/json"},"contents":[{"parts":[{"text":"Hello"}]}]}`,
		`{"generationConfig":{"responseSchema":{"type":"OBJECT"}},"contents":[{"parts":[{"text":"Hello"}]}]}`,
		`{"generationConfig":{"responseJsonSchema":{"type":"object"}},"contents":[{"parts":[{"text":"Hello"}]}]}`,
	} {
		if _, err := GeminiAppPrompt([]byte(input)); err == nil {
			t.Fatalf("accepted unsupported request: %s", input)
		}
	}
}

func TestReadGeminiAppStreamDeltas(t *testing.T) {
	wire := ")]}'\n\n" + appFrame("你", false) + appFrame("你", false) + appFrame("你好 🌏", true)
	var deltas []string
	text, err := ReadGeminiAppStream(strings.NewReader(wire), func(delta string) error { deltas = append(deltas, delta); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if text != "你好 🌏" || strings.Join(deltas, "|") != "你|好 🌏" {
		t.Fatalf("text %q, deltas %v", text, deltas)
	}
}

func TestReadGeminiAppStreamErrors(t *testing.T) {
	quota, _ := json.Marshal([]any{[]any{"wrb.fr", nil, nil, nil, nil, []any{nil, nil, []any{[]any{nil, []any{1037}}}}}})
	for name, input := range map[string]string{
		"empty": "", "truncated answer": appFrame("partial", false),
		"malformed": ")]}'\n100\n[[", "RPC": `[["er",null,500]]`,
		"quota": string(quota), "revised snapshot": appFrame("first", false) + appFrame("changed", true),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadGeminiAppStream(strings.NewReader(input), func(string) error { return nil })
			if err == nil {
				t.Fatal("expected upstream failure")
			}
			if name == "quota" {
				var status interface{ StatusCode() int }
				if !errors.As(err, &status) || status.StatusCode() != 429 {
					t.Fatalf("expected 429, got %v", err)
				}
			}
		})
	}
	stop := errors.New("consumer canceled")
	_, err := ReadGeminiAppStream(strings.NewReader(appFrame("answer", true)), func(string) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("lost consumer error: %v", err)
	}
}
