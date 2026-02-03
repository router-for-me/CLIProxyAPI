// Package logging provides request logging functionality for the CLI Proxy API server.
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// SSEParser parses Server-Sent Events and extracts content.
type SSEParser struct {
	aggregator   *StreamingContentAggregator
	tokenUsage   *TokenSummary
	modelVersion string
}

// NewSSEParser creates a new SSE parser.
func NewSSEParser() *SSEParser {
	return &SSEParser{
		aggregator: NewStreamingContentAggregator(),
	}
}

// ParseChunk parses an SSE data line and extracts content.
// It handles various upstream formats (Vertex/Gemini, Claude, etc.)
func (p *SSEParser) ParseChunk(chunk []byte) {
	// Skip empty lines and event lines
	chunk = bytes.TrimSpace(chunk)
	if len(chunk) == 0 {
		return
	}

	// Extract data payload if prefixed with "data: "
	if bytes.HasPrefix(chunk, []byte("data: ")) {
		chunk = chunk[6:]
	} else if bytes.HasPrefix(chunk, []byte("data:")) {
		chunk = chunk[5:]
	}

	// Skip [DONE] markers
	if bytes.Equal(chunk, []byte("[DONE]")) {
		return
	}

	// Try to parse as JSON
	var data map[string]interface{}
	if err := json.Unmarshal(chunk, &data); err != nil {
		// Not JSON, treat as raw text
		p.aggregator.AddResponseChunk(string(chunk))
		return
	}

	// Parse based on format - stop after first successful parse
	if p.parseVertexFormat(data) {
		return
	}
	if p.parseClaudeFormat(data) {
		return
	}
	p.parseOpenAIFormat(data)
}

// parseVertexFormat handles Vertex/Gemini API response format.
// Returns true if the format was matched and processed.
func (p *SSEParser) parseVertexFormat(data map[string]interface{}) bool {
	// Check for response.candidates[0].content.parts
	response, ok := data["response"].(map[string]interface{})
	if !ok {
		return false
	}

	// Extract model version
	if modelVersion, ok := response["modelVersion"].(string); ok {
		p.modelVersion = modelVersion
	}

	// Extract usage metadata
	if usage, ok := response["usageMetadata"].(map[string]interface{}); ok {
		p.extractTokenUsage(usage)
	}

	candidates, ok := response["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return true // Matched Vertex format even without candidates
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return true
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return true
	}

	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return true
	}

	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}

		text, hasText := partMap["text"].(string)
		if !hasText || text == "" {
			continue
		}

		// Check if this is thinking content
		isThought, _ := partMap["thought"].(bool)
		if isThought {
			p.aggregator.AddThinkingChunk(text)
		} else {
			p.aggregator.AddResponseChunk(text)
		}
	}
	return true
}

// parseClaudeFormat handles Claude API SSE response format.
// Returns true if the format was matched and processed.
func (p *SSEParser) parseClaudeFormat(data map[string]interface{}) bool {
	// Check for content_block_delta type
	msgType, ok := data["type"].(string)
	if !ok {
		return false
	}

	switch msgType {
	case "content_block_delta":
		delta, ok := data["delta"].(map[string]interface{})
		if !ok {
			return true
		}

		deltaType, _ := delta["type"].(string)
		switch deltaType {
		case "thinking_delta":
			if thinking, ok := delta["thinking"].(string); ok && thinking != "" {
				p.aggregator.AddThinkingChunk(thinking)
			}
		case "text_delta":
			if text, ok := delta["text"].(string); ok && text != "" {
				p.aggregator.AddResponseChunk(text)
			}
		}

	case "message_start":
		msg, ok := data["message"].(map[string]interface{})
		if !ok {
			return true
		}
		if model, ok := msg["model"].(string); ok {
			p.modelVersion = model
		}
		if usage, ok := msg["usage"].(map[string]interface{}); ok {
			p.extractTokenUsage(usage)
		}

	case "message_delta":
		if usage, ok := data["usage"].(map[string]interface{}); ok {
			p.extractTokenUsage(usage)
		}
	default:
		// Unknown Claude message type, but still matched the format
	}
	return true
}

// parseOpenAIFormat handles OpenAI-compatible SSE response format.
func (p *SSEParser) parseOpenAIFormat(data map[string]interface{}) {
	choices, ok := data["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return
	}

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return
	}

	if content, ok := delta["content"].(string); ok && content != "" {
		p.aggregator.AddResponseChunk(content)
	}

	// OpenAI reasoning tokens (if present)
	if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
		p.aggregator.AddThinkingChunk(reasoning)
	}

	// Extract model
	if model, ok := data["model"].(string); ok {
		p.modelVersion = model
	}

	// Extract usage
	if usage, ok := data["usage"].(map[string]interface{}); ok {
		p.extractTokenUsage(usage)
	}
}

// extractTokenUsage extracts token counts from various API formats.
func (p *SSEParser) extractTokenUsage(usage map[string]interface{}) {
	if p.tokenUsage == nil {
		p.tokenUsage = &TokenSummary{}
	}

	// Extract input tokens (prioritize Vertex format)
	if v, ok := usage["promptTokenCount"].(float64); ok {
		p.tokenUsage.Input = int(v)
	} else if v, ok := usage["input_tokens"].(float64); ok { // Claude format
		p.tokenUsage.Input = int(v)
	} else if v, ok := usage["prompt_tokens"].(float64); ok { // OpenAI format
		p.tokenUsage.Input = int(v)
	}

	// Extract output tokens (prioritize Vertex format)
	if v, ok := usage["candidatesTokenCount"].(float64); ok {
		p.tokenUsage.Output = int(v)
	} else if v, ok := usage["output_tokens"].(float64); ok { // Claude format
		p.tokenUsage.Output = int(v)
	} else if v, ok := usage["completion_tokens"].(float64); ok { // OpenAI format
		p.tokenUsage.Output = int(v)
	}

	// Extract total tokens
	if v, ok := usage["totalTokenCount"].(float64); ok {
		p.tokenUsage.Total = int(v)
	} else if v, ok := usage["total_tokens"].(float64); ok { // OpenAI format
		p.tokenUsage.Total = int(v)
	}

	// Calculate total if not provided
	if p.tokenUsage.Total == 0 && (p.tokenUsage.Input > 0 || p.tokenUsage.Output > 0) {
		p.tokenUsage.Total = p.tokenUsage.Input + p.tokenUsage.Output
	}
}

// GetContent returns the aggregated streaming content.
func (p *SSEParser) GetContent() *StreamingContent {
	return p.aggregator.ToStreamingContent()
}

// GetTokenUsage returns the extracted token usage.
func (p *SSEParser) GetTokenUsage() *TokenSummary {
	return p.tokenUsage
}

// GetModelVersion returns the extracted model version.
func (p *SSEParser) GetModelVersion() string {
	return p.modelVersion
}

// ParseRawSSE parses raw SSE data (multiple lines) and returns aggregated content.
func ParseRawSSE(data []byte) (*StreamingContent, *TokenSummary, string) {
	parser := NewSSEParser()

	// Split by lines and parse each
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		parser.ParseChunk(line)
	}

	return parser.GetContent(), parser.GetTokenUsage(), parser.GetModelVersion()
}

// ExtractModelFromBody extracts the model name from a request body.
func ExtractModelFromBody(body []byte) string {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	if model, ok := data["model"].(string); ok {
		return model
	}
	return ""
}

// ExtractThinkingBudgetFromBody extracts thinking budget from various request formats.
func ExtractThinkingBudgetFromBody(body []byte) int {
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0
	}

	// Claude format: thinking.budget_tokens
	if thinking, ok := data["thinking"].(map[string]interface{}); ok {
		if budget, ok := thinking["budget_tokens"].(float64); ok {
			return int(budget)
		}
	}

	// Vertex format: nested in request.generationConfig.thinkingConfig
	if request, ok := data["request"].(map[string]interface{}); ok {
		if genConfig, ok := request["generationConfig"].(map[string]interface{}); ok {
			if thinkConfig, ok := genConfig["thinkingConfig"].(map[string]interface{}); ok {
				if budget, ok := thinkConfig["thinkingBudget"].(float64); ok {
					return int(budget)
				}
			}
		}
	}

	return 0
}

// ExtractProtocolTransformations compares client and upstream requests to find transformations.
func ExtractProtocolTransformations(clientBody, upstreamBody []byte) map[string]string {
	transforms := make(map[string]string)

	var client, upstream map[string]interface{}
	if err := json.Unmarshal(clientBody, &client); err != nil {
		return transforms
	}
	if err := json.Unmarshal(upstreamBody, &upstream); err != nil {
		return transforms
	}

	// Model transformation
	clientModel := extractString(client, "model")
	upstreamModel := extractNestedString(upstream, "model")
	if upstreamModel == "" {
		upstreamModel = extractNestedString(upstream, "request.model")
	}
	if clientModel != "" && upstreamModel != "" && clientModel != upstreamModel {
		transforms["model"] = clientModel + " → " + upstreamModel
	}

	// Thinking budget transformation
	clientBudget := extractThinkingBudget(client)
	upstreamBudget := extractUpstreamThinkingBudget(upstream)
	if clientBudget > 0 || upstreamBudget > 0 {
		if clientBudget != upstreamBudget {
			transforms["thinking_budget"] = formatTransform(clientBudget, upstreamBudget)
		} else if clientBudget > 0 {
			transforms["thinking_budget"] = formatInt(clientBudget) + " (preserved)"
		}
	}

	// Temperature transformation
	clientTemp := extractFloat(client, "temperature")
	upstreamTemp := extractNestedFloat(upstream, "request.generationConfig.temperature")
	if clientTemp >= 0 && upstreamTemp >= 0 && clientTemp != upstreamTemp {
		transforms["temperature"] = formatFloatTransform(clientTemp, upstreamTemp)
	}

	// Max tokens transformation
	clientMaxTokens := extractInt(client, "max_tokens")
	upstreamMaxTokens := extractNestedInt(upstream, "request.generationConfig.maxOutputTokens")
	if clientMaxTokens > 0 || upstreamMaxTokens > 0 {
		if clientMaxTokens != upstreamMaxTokens {
			transforms["max_tokens"] = formatTransform(clientMaxTokens, upstreamMaxTokens)
		}
	}

	return transforms
}

// Helper functions for nested JSON extraction
func extractString(data map[string]interface{}, key string) string {
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}

func extractNestedString(data map[string]interface{}, path string) string {
	parts := strings.Split(path, ".")
	current := data
	for i, part := range parts {
		if i == len(parts)-1 {
			if v, ok := current[part].(string); ok {
				return v
			}
			return ""
		}
		if next, ok := current[part].(map[string]interface{}); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

func extractFloat(data map[string]interface{}, key string) float64 {
	if v, ok := data[key].(float64); ok {
		return v
	}
	return -1
}

func extractNestedFloat(data map[string]interface{}, path string) float64 {
	parts := strings.Split(path, ".")
	current := data
	for i, part := range parts {
		if i == len(parts)-1 {
			if v, ok := current[part].(float64); ok {
				return v
			}
			return -1
		}
		if next, ok := current[part].(map[string]interface{}); ok {
			current = next
		} else {
			return -1
		}
	}
	return -1
}

func extractInt(data map[string]interface{}, key string) int {
	if v, ok := data[key].(float64); ok {
		return int(v)
	}
	return 0
}

func extractNestedInt(data map[string]interface{}, path string) int {
	parts := strings.Split(path, ".")
	current := data
	for i, part := range parts {
		if i == len(parts)-1 {
			if v, ok := current[part].(float64); ok {
				return int(v)
			}
			return 0
		}
		if next, ok := current[part].(map[string]interface{}); ok {
			current = next
		} else {
			return 0
		}
	}
	return 0
}

func extractThinkingBudget(data map[string]interface{}) int {
	if thinking, ok := data["thinking"].(map[string]interface{}); ok {
		if budget, ok := thinking["budget_tokens"].(float64); ok {
			return int(budget)
		}
	}
	return 0
}

func extractUpstreamThinkingBudget(data map[string]interface{}) int {
	if request, ok := data["request"].(map[string]interface{}); ok {
		if genConfig, ok := request["generationConfig"].(map[string]interface{}); ok {
			if thinkConfig, ok := genConfig["thinkingConfig"].(map[string]interface{}); ok {
				if budget, ok := thinkConfig["thinkingBudget"].(float64); ok {
					return int(budget)
				}
			}
		}
	}
	return 0
}

func formatTransform(from, to int) string {
	return formatInt(from) + " → " + formatInt(to)
}

func formatFloatTransform(from, to float64) string {
	return fmt.Sprintf("%.2g → %.2g", from, to)
}

func formatInt(v int) string {
	return fmt.Sprintf("%d", v)
}
