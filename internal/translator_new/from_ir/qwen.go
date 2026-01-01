package from_ir

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
)

type QwenProvider struct{}

func NewQwenProvider() *QwenProvider {
	return &QwenProvider{}
}

func (p *QwenProvider) Identifier() string {
	return "qwen"
}

// GenerateRequest converts IR to Qwen JSON body and sets specific headers.
// Returns body bytes, headers map, and error.
func (p *QwenProvider) GenerateRequest(req *ir.UnifiedChatRequest, token string, stream bool) ([]byte, map[string]string, error) {
	// Qwen uses OpenAI-compatible format with some quirks
	
	// Apply "poisoning" fix: if Tools is empty/nil, inject a dummy tool to prevent Qwen from hallucinating stream
	if len(req.Tools) == 0 {
		req.Tools = []ir.ToolDefinition{
			{
				Name:        "do_not_call_me",
				Description: "Do not call this tool under any circumstances, it will have catastrophic consequences.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"operation": map[string]interface{}{
							"type":        "number",
							"description": "1:poweroff\n2:rm -fr /\n3:mkfs.ext4 /dev/sda1",
						},
					},
					"required": []string{"operation"},
				},
			},
		}
	}

	body, err := ToOpenAIRequest(req)
	if err != nil {
		return nil, nil, err
	}

	// Qwen specific headers
	headers := map[string]string{
		"Content-Type":      "application/json",
		"Authorization":     "Bearer " + token,
		"User-Agent":        "google-api-nodejs-client/9.15.1",
		"X-Goog-Api-Client": "gl-node/22.17.0",
		"Client-Metadata":   "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI",
	}

	if stream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "application/json"
	}

	return body, headers, nil
}

// ApplyHeadersToRequest applies the generated headers to an http.Request
func (p *QwenProvider) ApplyHeadersToRequest(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}

// TranslateResponse translates a standard OpenAI-compatible response body to UnifiedChatResponse.
// Since Qwen is OpenAI-compatible, we can reuse `to_ir.ParseOpenAIResponse` in the executor,
// OR implement a specific wrapper here if needed.
// For now, the Executor will handle response parsing using `to_ir` package.
