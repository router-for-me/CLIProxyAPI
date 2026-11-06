package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAICompatExecutorStructuredCompletionContent(t *testing.T) {
	const imageURL = "data:image/png;base64,iVBORw0KGgo="
	const content = `[{"type":"thinking","thinking":[{"type":"text","text":"A triangle is visible."}],"closed":true},{"type":"text","text":"purple triangle"}]`
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.FormatGemini} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", format, stream), func(t *testing.T) {
				requests := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						http.Error(w, errRead.Error(), http.StatusBadRequest)
						return
					}
					requests <- body
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"id\":\"chat-content\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"vision-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":null}]}\n\n", content)
						_, _ = io.WriteString(w, "data: {\"id\":\"chat-content\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"vision-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":8,\"total_tokens\":28,\"prompt_tokens_details\":{\"cached_tokens\":5}}}\n\ndata: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"id":"chat-content","object":"chat.completion","created":1,"model":"vision-model","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28,"prompt_tokens_details":{"cached_tokens":5}}}`, content)
				}))
				defer server.Close()
				executor := NewOpenAICompatExecutor("structured-content", &config.Config{})
				auth := &cliproxyauth.Auth{Provider: "structured-content", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-key"}}
				req := cliproxyexecutor.Request{Model: "vision-model", Payload: []byte(`{"model":"vision-model","messages":[{"role":"user","content":[{"type":"text","text":"Describe this image."},{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: format, Stream: stream}
				req.Payload, _ = sjson.SetBytes(req.Payload, "stream", stream)
				var output strings.Builder
				if stream {
					result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output.Write(chunk.Payload)
						output.WriteByte('\n')
					}
				} else {
					result, errExecute := executor.Execute(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					output.Write(result.Payload)
				}
				captured := <-requests
				if got := gjson.GetBytes(captured, "messages.0.content.1.image_url.url").String(); got != imageURL {
					t.Fatalf("image input changed: %s", captured)
				}
				text, reasoning := completionContentTexts(format, output.String(), stream)
				if text != "purple triangle" || reasoning != "A triangle is visible." {
					t.Fatalf("text=%q reasoning=%q; response=%s", text, reasoning, output.String())
				}
			})
		}
	}
}

func completionContentTexts(format sdktranslator.Format, raw string, stream bool) (string, string) {
	var text, reasoning strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !gjson.Valid(line) {
			continue
		}
		root := gjson.Parse(line)
		switch format {
		case sdktranslator.FormatOpenAI:
			path := "choices.0.message"
			if stream {
				path = "choices.0.delta"
			}
			text.WriteString(root.Get(path + ".content").String())
			reasoning.WriteString(root.Get(path + ".reasoning_content").String())
		case sdktranslator.FormatOpenAIResponse:
			if stream {
				switch root.Get("type").String() {
				case "response.output_text.delta":
					text.WriteString(root.Get("delta").String())
				case "response.reasoning_summary_text.delta":
					reasoning.WriteString(root.Get("delta").String())
				}
			} else {
				for _, item := range root.Get("output").Array() {
					for _, part := range item.Get("content").Array() {
						text.WriteString(part.Get("text").String())
					}
					for _, part := range item.Get("summary").Array() {
						reasoning.WriteString(part.Get("text").String())
					}
				}
			}
		case sdktranslator.FormatClaude:
			if stream {
				text.WriteString(root.Get("delta.text").String())
				reasoning.WriteString(root.Get("delta.thinking").String())
			} else {
				for _, part := range root.Get("content").Array() {
					if part.Get("type").String() == "thinking" {
						reasoning.WriteString(part.Get("thinking").String())
					} else if part.Get("type").String() == "text" {
						text.WriteString(part.Get("text").String())
					}
				}
			}
		case sdktranslator.FormatGemini:
			for _, part := range root.Get("candidates.0.content.parts").Array() {
				if part.Get("thought").Bool() {
					reasoning.WriteString(part.Get("text").String())
				} else {
					text.WriteString(part.Get("text").String())
				}
			}
		}
	}
	return text.String(), reasoning.String()
}
