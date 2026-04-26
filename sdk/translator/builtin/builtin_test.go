package builtin

import (
	"context"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPipeline_UsesDefaultRequestMiddleware(t *testing.T) {
	from := sdktranslator.Format("builtin-test-openai-source")
	sdktranslator.Register(from, sdktranslator.FormatOpenAI, func(_ string, _ []byte, _ bool) []byte {
		return []byte(`{
			"model":"test-model",
			"reasoning_effort":"high",
			"messages":[
				{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}
			]
		}`)
	}, sdktranslator.ResponseTransform{})

	req := sdktranslator.RequestEnvelope{
		Format: from,
		Model:  "test-model",
		Stream: false,
		Body:   []byte(`{}`),
	}

	out, err := Pipeline().TranslateRequest(context.Background(), from, sdktranslator.FormatOpenAI, req)
	if err != nil {
		t.Fatalf("Pipeline().TranslateRequest() error = %v", err)
	}
	if got := gjson.GetBytes(out.Body, "messages.0.reasoning_content").String(); got == "" {
		t.Fatalf("messages.0.reasoning_content should be injected, payload=%s", string(out.Body))
	}
}
