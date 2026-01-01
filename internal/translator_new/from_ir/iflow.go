package from_ir

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type IFlowProvider struct{}

func NewIFlowProvider() *IFlowProvider {
	return &IFlowProvider{}
}

func (p *IFlowProvider) Identifier() string {
	return "iflow"
}

// GenerateRequest converts IR to iFlow JSON body and sets specific headers.
func (p *IFlowProvider) GenerateRequest(req *ir.UnifiedChatRequest, apiKey string, stream bool) ([]byte, map[string]string, error) {
	// 1. Convert to OpenAI-compatible base
	body, err := ToOpenAIRequest(req)
	if err != nil {
		return nil, nil, err
	}

	// 2. Apply iFlow-specific modifications
	// iFlow needs a tools array if it's empty, similar to Qwen quirks
	toolsResult := gjson.GetBytes(body, "tools")
	if !toolsResult.Exists() || (toolsResult.IsArray() && len(toolsResult.Array()) == 0) {
		placeholder := `[{"type":"function","function":{"name":"noop","description":"Placeholder tool to stabilise streaming","parameters":{"type":"object"}}}]`
		body, _ = sjson.SetRawBytes(body, "tools", []byte(placeholder))
	}

	// 3. Apply Thinking Config
	// iFlow uses chat_template_kwargs.enable_thinking (bool) instead of reasoning_effort
	// We check if reasoning_effort was set by ToOpenAIRequest (mapped from Thinking config)
	effort := gjson.GetBytes(body, "reasoning_effort")
	if effort.Exists() {
		val := strings.ToLower(strings.TrimSpace(effort.String()))
		enableThinking := val != "none" && val != ""
		
		body, _ = sjson.DeleteBytes(body, "reasoning_effort")
		body, _ = sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", enableThinking)
	} else if req.Thinking != nil && req.Thinking.IncludeThoughts {
		// Explicit fallback if ToOpenAIRequest didn't set reasoning_effort but Thinking is present
		body, _ = sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", true)
	}

	// 4. Headers
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + apiKey,
		"User-Agent":    "iFlow-Cli",
	}

	if stream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "application/json"
	}

	return body, headers, nil
}

func (p *IFlowProvider) ApplyHeadersToRequest(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}
