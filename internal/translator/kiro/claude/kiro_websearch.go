// Package claude provides web search functionality for Kiro translator.
// This file implements detection and MCP request/response types for web search.
package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// McpRequest represents a JSON-RPC 2.0 request to Kiro MCP API
type McpRequest struct {
	ID      string    `json:"id"`
	JSONRPC string    `json:"jsonrpc"`
	Method  string    `json:"method"`
	Params  McpParams `json:"params"`
}

// McpParams represents MCP request parameters
type McpParams struct {
	Name      string       `json:"name"`
	Arguments McpArguments `json:"arguments"`
}

// McpArgumentsMeta represents the _meta field in MCP arguments
type McpArgumentsMeta struct {
	IsValid        bool       `json:"_isValid"`
	ActivePath     []string   `json:"_activePath"`
	CompletedPaths [][]string `json:"_completedPaths"`
}

// McpArguments represents MCP request arguments
type McpArguments struct {
	Query string            `json:"query"`
	Meta  *McpArgumentsMeta `json:"_meta,omitempty"`
}

// McpResponse represents a JSON-RPC 2.0 response from Kiro MCP API
type McpResponse struct {
	Error   *McpError  `json:"error,omitempty"`
	ID      string     `json:"id"`
	JSONRPC string     `json:"jsonrpc"`
	Result  *McpResult `json:"result,omitempty"`
}

// McpError represents an MCP error
type McpError struct {
	Code    *int    `json:"code,omitempty"`
	Message *string `json:"message,omitempty"`
}

// McpResult represents MCP result
type McpResult struct {
	Content []McpContent `json:"content"`
	IsError bool         `json:"isError"`
}

// McpContent represents MCP content item
type McpContent struct {
	ContentType string `json:"type"`
	Text        string `json:"text"`
}

// WebSearchResults represents parsed search results
type WebSearchResults struct {
	Results      []WebSearchResult `json:"results"`
	TotalResults *int              `json:"totalResults,omitempty"`
	Query        *string           `json:"query,omitempty"`
	Error        *string           `json:"error,omitempty"`
}

// WebSearchResult represents a single search result
type WebSearchResult struct {
	Title                string  `json:"title"`
	URL                  string  `json:"url"`
	Snippet              *string `json:"snippet,omitempty"`
	PublishedDate        *int64  `json:"publishedDate,omitempty"`
	ID                   *string `json:"id,omitempty"`
	Domain               *string `json:"domain,omitempty"`
	MaxVerbatimWordLimit *int    `json:"maxVerbatimWordLimit,omitempty"`
	PublicDomain         *bool   `json:"publicDomain,omitempty"`
}

// isWebSearchTool checks if a tool name or type indicates a web_search tool.
func isWebSearchTool(name, toolType string) bool {
	return name == "web_search" ||
		strings.HasPrefix(toolType, "web_search") ||
		toolType == "web_search_20250305"
}

// HasWebSearchTool checks if the request contains ONLY a web_search tool.
// Returns true only if tools array has exactly one tool named "web_search".
// Only intercept pure web_search requests (single-tool array).
func HasWebSearchTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}

	toolsArray := tools.Array()
	if len(toolsArray) != 1 {
		return false
	}

	// Check if the single tool is web_search
	tool := toolsArray[0]

	// Check both name and type fields for web_search detection
	name := strings.ToLower(tool.Get("name").String())
	toolType := strings.ToLower(tool.Get("type").String())

	return isWebSearchTool(name, toolType)
}

// ExtractSearchQuery extracts the search query from the request.
// Reads messages[0].content and removes "Perform a web search for the query: " prefix.
func ExtractSearchQuery(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return ""
	}

	firstMsg := messages.Array()[0]
	content := firstMsg.Get("content")

	var text string
	if content.IsArray() {
		// Array format: [{"type": "text", "text": "..."}]
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				text = block.Get("text").String()
				break
			}
		}
	} else {
		// String format
		text = content.String()
	}

	// Remove prefix "Perform a web search for the query: "
	const prefix = "Perform a web search for the query: "
	if strings.HasPrefix(text, prefix) {
		text = text[len(prefix):]
	}

	return strings.TrimSpace(text)
}

// generateRandomID8 generates an 8-character random lowercase alphanumeric string
func generateRandomID8() string {
	u := uuid.New()
	return strings.ToLower(strings.ReplaceAll(u.String(), "-", "")[:8])
}

// CreateMcpRequest creates an MCP request for web search.
// Returns (toolUseID, McpRequest)
// ID format: web_search_tooluse_{22 random}_{timestamp_millis}_{8 random}
func CreateMcpRequest(query string) (string, *McpRequest) {
	random22 := GenerateToolUseID()
	timestamp := time.Now().UnixMilli()
	random8 := generateRandomID8()

	requestID := fmt.Sprintf("web_search_tooluse_%s_%d_%s", random22, timestamp, random8)

	// tool_use_id format: srvtoolu_{32 hex chars}
	toolUseID := "srvtoolu_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:32]

	request := &McpRequest{
		ID:      requestID,
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: McpParams{
			Name: "web_search",
			Arguments: McpArguments{
				Query: query,
				Meta: &McpArgumentsMeta{
					IsValid:        true,
					ActivePath:     []string{"query"},
					CompletedPaths: [][]string{{"query"}},
				},
			},
		},
	}

	return toolUseID, request
}

// GenerateMessageID generates a Claude-style message ID
func GenerateMessageID() string {
	return "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
}

// GenerateToolUseID generates a Kiro-style tool use ID (base62-like UUID)
func GenerateToolUseID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")[:22]
}

// ContainsWebSearchTool checks if the request contains a web_search tool (among any tools).
// Unlike HasWebSearchTool, this detects web_search even in mixed-tool arrays.
func ContainsWebSearchTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}

	for _, tool := range tools.Array() {
		name := strings.ToLower(tool.Get("name").String())
		toolType := strings.ToLower(tool.Get("type").String())

		if isWebSearchTool(name, toolType) {
			return true
		}
	}

	return false
}

// ReplaceWebSearchToolDescription replaces the web_search tool description with
// a minimal version that allows re-search without the restrictive "do not search
// non-coding topics" instruction from the original Kiro tools/list response.
// This keeps the tool available so the model can request additional searches.
func ReplaceWebSearchToolDescription(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}

	var updated []json.RawMessage
	for _, tool := range tools.Array() {
		name := strings.ToLower(tool.Get("name").String())
		toolType := strings.ToLower(tool.Get("type").String())

		if isWebSearchTool(name, toolType) {
			// Replace with a minimal web_search tool definition
			minimalTool := map[string]interface{}{
				"name":        "web_search",
				"description": "Search the web for information. Use this when the previous search results are insufficient or when you need additional information on a different aspect of the query. Provide a refined or different search query.",
				"input_schema": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query to execute",
						},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			}
			minimalJSON, err := json.Marshal(minimalTool)
			if err != nil {
				return body, fmt.Errorf("failed to marshal minimal tool: %w", err)
			}
			updated = append(updated, json.RawMessage(minimalJSON))
		} else {
			updated = append(updated, json.RawMessage(tool.Raw))
		}
	}

	updatedJSON, err := json.Marshal(updated)
	if err != nil {
		return body, fmt.Errorf("failed to marshal updated tools: %w", err)
	}
	result, err := sjson.SetRawBytes(body, "tools", updatedJSON)
	if err != nil {
		return body, fmt.Errorf("failed to set updated tools: %w", err)
	}

	return result, nil
}

// StripWebSearchTool removes web_search tool entries from the request's tools array.
// If the tools array becomes empty after removal, it is removed entirely.
func StripWebSearchTool(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}

	var filtered []json.RawMessage
	for _, tool := range tools.Array() {
		name := strings.ToLower(tool.Get("name").String())
		toolType := strings.ToLower(tool.Get("type").String())

		if !isWebSearchTool(name, toolType) {
			filtered = append(filtered, json.RawMessage(tool.Raw))
		}
	}

	var result []byte
	var err error

	if len(filtered) == 0 {
		// Remove tools array entirely
		result, err = sjson.DeleteBytes(body, "tools")
		if err != nil {
			return body, fmt.Errorf("failed to delete tools: %w", err)
		}
	} else {
		// Replace with filtered array
		filteredJSON, marshalErr := json.Marshal(filtered)
		if marshalErr != nil {
			return body, fmt.Errorf("failed to marshal filtered tools: %w", marshalErr)
		}
		result, err = sjson.SetRawBytes(body, "tools", filteredJSON)
		if err != nil {
			return body, fmt.Errorf("failed to set filtered tools: %w", err)
		}
	}

	return result, nil
}

// FormatSearchContextPrompt formats search results as a structured text block
// for injection into the system prompt.
func FormatSearchContextPrompt(query string, results *WebSearchResults) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Web Search Results for \"%s\"]\n", query))

	if results != nil && len(results.Results) > 0 {
		for i, r := range results.Results {
			sb.WriteString(fmt.Sprintf("%d. %s - %s\n", i+1, r.Title, r.URL))
			if r.Snippet != nil && *r.Snippet != "" {
				snippet := *r.Snippet
				if len(snippet) > 500 {
					snippet = snippet[:500] + "..."
				}
				sb.WriteString(fmt.Sprintf("   %s\n", snippet))
			}
		}
	} else {
		sb.WriteString("No results found.\n")
	}

	sb.WriteString("[End Web Search Results]")
	return sb.String()
}

// FormatToolResultText formats search results as JSON text for the toolResults content field.
// This matches the format observed in Kiro IDE HAR captures.
func FormatToolResultText(results *WebSearchResults) string {
	if results == nil || len(results.Results) == 0 {
		return "No search results found."
	}

	text := fmt.Sprintf("Found %d search result(s):\n\n", len(results.Results))
	resultJSON, err := json.MarshalIndent(results.Results, "", "  ")
	if err != nil {
		return text + "Error formatting results."
	}
	return text + string(resultJSON)
}

// InjectToolResultsClaude modifies a Claude-format JSON payload to append
// tool_use (assistant) and tool_result (user) messages to the messages array.
// BuildKiroPayload correctly translates:
//   - assistant tool_use → KiroAssistantResponseMessage.toolUses
//   - user tool_result   → KiroUserInputMessageContext.toolResults
//
// This produces the exact same GAR request format as the Kiro IDE (HAR captures).
// IMPORTANT: The web_search tool must remain in the "tools" array for this to work.
// Use ReplaceWebSearchToolDescription (not StripWebSearchTool) to keep the tool available.
func InjectToolResultsClaude(claudePayload []byte, toolUseId, query string, results *WebSearchResults) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(claudePayload, &payload); err != nil {
		return claudePayload, fmt.Errorf("failed to parse claude payload: %w", err)
	}

	messages, _ := payload["messages"].([]interface{})

	// 1. Append assistant message with tool_use (matches HAR: assistantResponseMessage.toolUses)
	assistantMsg := map[string]interface{}{
		"role": "assistant",
		"content": []interface{}{
			map[string]interface{}{
				"type":  "tool_use",
				"id":    toolUseId,
				"name":  "web_search",
				"input": map[string]interface{}{"query": query},
			},
		},
	}
	messages = append(messages, assistantMsg)

	// 2. Append user message with tool_result + search behavior instructions.
	// NOTE: We embed search instructions HERE (not in system prompt) because
	// BuildKiroPayload clears the system prompt when len(history) > 0,
	// which is always true after injecting assistant + user messages.
	now := time.Now()
	searchGuidance := fmt.Sprintf(`<search_guidance>
Current date: %s (%s)

IMPORTANT: Evaluate the search results above carefully. If the results are:
- Mostly spam, SEO junk, or unrelated websites
- Missing actual information about the query topic
- Outdated or not matching the requested time frame

Then you MUST use the web_search tool again with a refined query. Try:
- Rephrasing in English for better coverage
- Using more specific keywords
- Adding date context

Do NOT apologize for bad results without first attempting a re-search.
</search_guidance>`, now.Format("January 2, 2006"), now.Format("Monday"))

	userMsg := map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": toolUseId,
				"content":     FormatToolResultText(results),
			},
			map[string]interface{}{
				"type": "text",
				"text": searchGuidance,
			},
		},
	}
	messages = append(messages, userMsg)

	payload["messages"] = messages

	result, err := json.Marshal(payload)
	if err != nil {
		return claudePayload, fmt.Errorf("failed to marshal updated payload: %w", err)
	}

	log.Infof("kiro/websearch: injected tool_use+tool_result (toolUseId=%s, query=%s, messages=%d)",
		toolUseId, query, len(messages))

	return result, nil
}

// InjectSearchIndicatorsInResponse prepends server_tool_use + web_search_tool_result
// content blocks into a non-streaming Claude JSON response. Claude Code counts
// server_tool_use blocks to display "Did X searches in Ys".
//
// Input response:  {"content": [{"type":"text","text":"..."}], ...}
// Output response: {"content": [{"type":"server_tool_use",...}, {"type":"web_search_tool_result",...}, {"type":"text","text":"..."}], ...}
func InjectSearchIndicatorsInResponse(responsePayload []byte, searches []SearchIndicator) ([]byte, error) {
	if len(searches) == 0 {
		return responsePayload, nil
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(responsePayload, &resp); err != nil {
		return responsePayload, fmt.Errorf("failed to parse response: %w", err)
	}

	existingContent, _ := resp["content"].([]interface{})

	// Build new content: search indicators first, then existing content
	newContent := make([]interface{}, 0, len(searches)*2+len(existingContent))

	for _, s := range searches {
		// server_tool_use block
		newContent = append(newContent, map[string]interface{}{
			"type":  "server_tool_use",
			"id":    s.ToolUseID,
			"name":  "web_search",
			"input": map[string]interface{}{"query": s.Query},
		})

		// web_search_tool_result block
		searchContent := make([]map[string]interface{}, 0)
		if s.Results != nil {
			for _, r := range s.Results.Results {
				snippet := ""
				if r.Snippet != nil {
					snippet = *r.Snippet
				}
				searchContent = append(searchContent, map[string]interface{}{
					"type":              "web_search_result",
					"title":             r.Title,
					"url":               r.URL,
					"encrypted_content": snippet,
					"page_age":          nil,
				})
			}
		}
		newContent = append(newContent, map[string]interface{}{
			"type":        "web_search_tool_result",
			"tool_use_id": s.ToolUseID,
			"content":     searchContent,
		})
	}

	// Append existing content blocks
	newContent = append(newContent, existingContent...)
	resp["content"] = newContent

	result, err := json.Marshal(resp)
	if err != nil {
		return responsePayload, fmt.Errorf("failed to marshal response: %w", err)
	}

	log.Infof("kiro/websearch: injected %d search indicator(s) into non-stream response", len(searches))
	return result, nil
}

// SearchIndicator holds the data for one search operation to inject into a response.
type SearchIndicator struct {
	ToolUseID string
	Query     string
	Results   *WebSearchResults
}

// ══════════════════════════════════════════════════════════════════════════════
// SSE Event Generation
// ══════════════════════════════════════════════════════════════════════════════

// SseEvent represents a Server-Sent Event
type SseEvent struct {
	Event string
	Data  interface{}
}

// ToSSEString converts the event to SSE wire format
func (e *SseEvent) ToSSEString() string {
	dataBytes, _ := json.Marshal(e.Data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", e.Event, string(dataBytes))
}

// GenerateWebSearchEvents generates the 11-event SSE sequence for web search.
// Events: message_start, content_block_start(server_tool_use), content_block_delta(input_json),
// content_block_stop, content_block_start(web_search_tool_result), content_block_stop,
// content_block_start(text), content_block_delta(text), content_block_stop, message_delta, message_stop
func GenerateWebSearchEvents(
	model string,
	query string,
	toolUseID string,
	searchResults *WebSearchResults,
	inputTokens int,
) []SseEvent {
	events := make([]SseEvent, 0, 15)
	messageID := GenerateMessageID()

	// 1. message_start
	events = append(events, SseEvent{
		Event: "message_start",
		Data: map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            messageID,
				"type":          "message",
				"role":          "assistant",
				"model":         model,
				"content":       []interface{}{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]interface{}{
					"input_tokens":                inputTokens,
					"output_tokens":               0,
					"cache_creation_input_tokens": 0,
					"cache_read_input_tokens":     0,
				},
			},
		},
	})

	// 2. content_block_start (server_tool_use)
	events = append(events, SseEvent{
		Event: "content_block_start",
		Data: map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]interface{}{
				"id":    toolUseID,
				"type":  "server_tool_use",
				"name":  "web_search",
				"input": map[string]interface{}{},
			},
		},
	})

	// 3. content_block_delta (input_json_delta)
	inputJSON, _ := json.Marshal(map[string]string{"query": query})
	events = append(events, SseEvent{
		Event: "content_block_delta",
		Data: map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": string(inputJSON),
			},
		},
	})

	// 4. content_block_stop (server_tool_use)
	events = append(events, SseEvent{
		Event: "content_block_stop",
		Data: map[string]interface{}{
			"type":  "content_block_stop",
			"index": 0,
		},
	})

	// 5. content_block_start (web_search_tool_result)
	searchContent := make([]map[string]interface{}, 0)
	if searchResults != nil {
		for _, r := range searchResults.Results {
			snippet := ""
			if r.Snippet != nil {
				snippet = *r.Snippet
			}
			searchContent = append(searchContent, map[string]interface{}{
				"type":              "web_search_result",
				"title":             r.Title,
				"url":               r.URL,
				"encrypted_content": snippet,
				"page_age":          nil,
			})
		}
	}
	events = append(events, SseEvent{
		Event: "content_block_start",
		Data: map[string]interface{}{
			"type":  "content_block_start",
			"index": 1,
			"content_block": map[string]interface{}{
				"type":        "web_search_tool_result",
				"tool_use_id": toolUseID,
				"content":     searchContent,
			},
		},
	})

	// 6. content_block_stop (web_search_tool_result)
	events = append(events, SseEvent{
		Event: "content_block_stop",
		Data: map[string]interface{}{
			"type":  "content_block_stop",
			"index": 1,
		},
	})

	// 7. content_block_start (text)
	events = append(events, SseEvent{
		Event: "content_block_start",
		Data: map[string]interface{}{
			"type":  "content_block_start",
			"index": 2,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		},
	})

	// 8. content_block_delta (text_delta) - generate search summary
	summary := generateSearchSummary(query, searchResults)

	// Split text into chunks for streaming effect
	chunkSize := 100
	runes := []rune(summary)
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[i:end])
		events = append(events, SseEvent{
			Event: "content_block_delta",
			Data: map[string]interface{}{
				"type":  "content_block_delta",
				"index": 2,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": chunk,
				},
			},
		})
	}

	// 9. content_block_stop (text)
	events = append(events, SseEvent{
		Event: "content_block_stop",
		Data: map[string]interface{}{
			"type":  "content_block_stop",
			"index": 2,
		},
	})

	// 10. message_delta
	outputTokens := (len(summary) + 3) / 4 // Simple estimation
	events = append(events, SseEvent{
		Event: "message_delta",
		Data: map[string]interface{}{
			"type": "message_delta",
			"delta": map[string]interface{}{
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
			},
			"usage": map[string]interface{}{
				"output_tokens": outputTokens,
			},
		},
	})

	// 11. message_stop
	events = append(events, SseEvent{
		Event: "message_stop",
		Data: map[string]interface{}{
			"type": "message_stop",
		},
	})

	return events
}

// generateSearchSummary generates a text summary of search results
func generateSearchSummary(query string, results *WebSearchResults) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Here are the search results for \"%s\":\n\n", query))

	if results != nil && len(results.Results) > 0 {
		for i, r := range results.Results {
			sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, r.Title))
			if r.Snippet != nil {
				snippet := *r.Snippet
				if len(snippet) > 200 {
					snippet = snippet[:200] + "..."
				}
				sb.WriteString(fmt.Sprintf("   %s\n", snippet))
			}
			sb.WriteString(fmt.Sprintf("   Source: %s\n\n", r.URL))
		}
	} else {
		sb.WriteString("No results found.\n")
	}

	sb.WriteString("\nPlease note that these are web search results and may not be fully accurate or up-to-date.")

	return sb.String()
}

// GenerateSearchIndicatorEvents generates ONLY the search indicator SSE events
// (server_tool_use + web_search_tool_result) without text summary or message termination.
// These events trigger Claude Code's search indicator UI.
// The caller is responsible for sending message_start before and message_delta/stop after.
func GenerateSearchIndicatorEvents(
	query string,
	toolUseID string,
	searchResults *WebSearchResults,
	startIndex int,
) []SseEvent {
	events := make([]SseEvent, 0, 4)

	// 1. content_block_start (server_tool_use)
	events = append(events, SseEvent{
		Event: "content_block_start",
		Data: map[string]interface{}{
			"type":  "content_block_start",
			"index": startIndex,
			"content_block": map[string]interface{}{
				"id":    toolUseID,
				"type":  "server_tool_use",
				"name":  "web_search",
				"input": map[string]interface{}{},
			},
		},
	})

	// 2. content_block_delta (input_json_delta)
	inputJSON, _ := json.Marshal(map[string]string{"query": query})
	events = append(events, SseEvent{
		Event: "content_block_delta",
		Data: map[string]interface{}{
			"type":  "content_block_delta",
			"index": startIndex,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": string(inputJSON),
			},
		},
	})

	// 3. content_block_stop (server_tool_use)
	events = append(events, SseEvent{
		Event: "content_block_stop",
		Data: map[string]interface{}{
			"type":  "content_block_stop",
			"index": startIndex,
		},
	})

	// 4. content_block_start (web_search_tool_result)
	searchContent := make([]map[string]interface{}, 0)
	if searchResults != nil {
		for _, r := range searchResults.Results {
			snippet := ""
			if r.Snippet != nil {
				snippet = *r.Snippet
			}
			searchContent = append(searchContent, map[string]interface{}{
				"type":              "web_search_result",
				"title":             r.Title,
				"url":               r.URL,
				"encrypted_content": snippet,
				"page_age":          nil,
			})
		}
	}
	events = append(events, SseEvent{
		Event: "content_block_start",
		Data: map[string]interface{}{
			"type":  "content_block_start",
			"index": startIndex + 1,
			"content_block": map[string]interface{}{
				"type":        "web_search_tool_result",
				"tool_use_id": toolUseID,
				"content":     searchContent,
			},
		},
	})

	// 5. content_block_stop (web_search_tool_result)
	events = append(events, SseEvent{
		Event: "content_block_stop",
		Data: map[string]interface{}{
			"type":  "content_block_stop",
			"index": startIndex + 1,
		},
	})

	return events
}

// ══════════════════════════════════════════════════════════════════════════════
// Stream Analysis & Manipulation
// ══════════════════════════════════════════════════════════════════════════════

// AdjustStreamIndices adjusts content block indices in SSE event data by adding an offset.
// It also suppresses duplicate message_start events (returns shouldForward=false).
// This is used to combine search indicator events (indices 0,1) with Kiro model response events.
//
// The data parameter is a single SSE "data:" line payload (JSON).
// Returns: adjusted data, shouldForward (false = skip this event).
func AdjustStreamIndices(data []byte, offset int) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}

	// Quick check: parse the JSON
	var event map[string]interface{}
	if err := json.Unmarshal(data, &event); err != nil {
		// Not valid JSON, pass through
		return data, true
	}

	eventType, _ := event["type"].(string)

	// Suppress duplicate message_start events
	if eventType == "message_start" {
		return data, false
	}

	// Adjust index for content_block events
	switch eventType {
	case "content_block_start", "content_block_delta", "content_block_stop":
		if idx, ok := event["index"].(float64); ok {
			event["index"] = int(idx) + offset
			adjusted, err := json.Marshal(event)
			if err != nil {
				return data, true
			}
			return adjusted, true
		}
	}

	// Pass through all other events unchanged (message_delta, message_stop, ping, etc.)
	return data, true
}

// AdjustSSEChunk processes a raw SSE chunk (potentially containing multiple "event:/data:" pairs)
// and adjusts content block indices. Suppresses duplicate message_start events.
// Returns the adjusted chunk and whether it should be forwarded.
func AdjustSSEChunk(chunk []byte, offset int) ([]byte, bool) {
	chunkStr := string(chunk)

	// Fast path: if no "data:" prefix, pass through
	if !strings.Contains(chunkStr, "data: ") {
		return chunk, true
	}

	var result strings.Builder
	hasContent := false

	lines := strings.Split(chunkStr, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if strings.HasPrefix(line, "data: ") {
			dataPayload := strings.TrimPrefix(line, "data: ")
			dataPayload = strings.TrimSpace(dataPayload)

			if dataPayload == "[DONE]" {
				result.WriteString(line + "\n")
				hasContent = true
				continue
			}

			adjusted, shouldForward := AdjustStreamIndices([]byte(dataPayload), offset)
			if !shouldForward {
				// Skip this event and its preceding "event:" line
				// Also skip the trailing empty line
				continue
			}

			result.WriteString("data: " + string(adjusted) + "\n")
			hasContent = true
		} else if strings.HasPrefix(line, "event: ") {
			// Check if the next data line will be suppressed
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				dataPayload := strings.TrimPrefix(lines[i+1], "data: ")
				dataPayload = strings.TrimSpace(dataPayload)

				var event map[string]interface{}
				if err := json.Unmarshal([]byte(dataPayload), &event); err == nil {
					if eventType, ok := event["type"].(string); ok && eventType == "message_start" {
						// Skip both the event: and data: lines
						i++ // skip the data: line too
						continue
					}
				}
			}
			result.WriteString(line + "\n")
			hasContent = true
		} else {
			result.WriteString(line + "\n")
			if strings.TrimSpace(line) != "" {
				hasContent = true
			}
		}
	}

	if !hasContent {
		return nil, false
	}

	return []byte(result.String()), true
}

// BufferedStreamResult contains the analysis of buffered SSE chunks from a Kiro API response.
type BufferedStreamResult struct {
	// StopReason is the detected stop_reason from the stream (e.g., "end_turn", "tool_use")
	StopReason string
	// WebSearchQuery is the extracted query if the model requested another web_search
	WebSearchQuery string
	// WebSearchToolUseId is the tool_use ID from the model's response (needed for toolResults)
	WebSearchToolUseId string
	// HasWebSearchToolUse indicates whether the model requested web_search
	HasWebSearchToolUse bool
	// WebSearchToolUseIndex is the content_block index of the web_search tool_use
	WebSearchToolUseIndex int
}

// AnalyzeBufferedStream scans buffered SSE chunks to detect stop_reason and web_search tool_use.
// This is used in the search loop to determine if the model wants another search round.
func AnalyzeBufferedStream(chunks [][]byte) BufferedStreamResult {
	result := BufferedStreamResult{WebSearchToolUseIndex: -1}

	// Track tool use state across chunks
	var currentToolName string
	var currentToolIndex int = -1
	var toolInputBuilder strings.Builder

	for _, chunk := range chunks {
		chunkStr := string(chunk)
		lines := strings.Split(chunkStr, "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			dataPayload := strings.TrimPrefix(line, "data: ")
			dataPayload = strings.TrimSpace(dataPayload)
			if dataPayload == "[DONE]" || dataPayload == "" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(dataPayload), &event); err != nil {
				continue
			}

			eventType, _ := event["type"].(string)

			switch eventType {
			case "message_delta":
				// Extract stop_reason from message_delta
				if delta, ok := event["delta"].(map[string]interface{}); ok {
					if sr, ok := delta["stop_reason"].(string); ok && sr != "" {
						result.StopReason = sr
					}
				}

			case "content_block_start":
				// Detect tool_use content blocks
				if cb, ok := event["content_block"].(map[string]interface{}); ok {
					if cbType, ok := cb["type"].(string); ok && cbType == "tool_use" {
						if name, ok := cb["name"].(string); ok {
							currentToolName = strings.ToLower(name)
							if idx, ok := event["index"].(float64); ok {
								currentToolIndex = int(idx)
							}
							// Capture tool use ID for toolResults handshake
							if id, ok := cb["id"].(string); ok {
								result.WebSearchToolUseId = id
							}
							toolInputBuilder.Reset()
						}
					}
				}

			case "content_block_delta":
				// Accumulate tool input JSON
				if currentToolName != "" {
					if delta, ok := event["delta"].(map[string]interface{}); ok {
						if deltaType, ok := delta["type"].(string); ok && deltaType == "input_json_delta" {
							if partial, ok := delta["partial_json"].(string); ok {
								toolInputBuilder.WriteString(partial)
							}
						}
					}
				}

			case "content_block_stop":
				// Finalize tool use detection
				if currentToolName == "web_search" || currentToolName == "websearch" || currentToolName == "remote_web_search" {
					result.HasWebSearchToolUse = true
					result.WebSearchToolUseIndex = currentToolIndex
					// Extract query from accumulated input JSON
					inputJSON := toolInputBuilder.String()
					var input map[string]string
					if err := json.Unmarshal([]byte(inputJSON), &input); err == nil {
						if q, ok := input["query"]; ok {
							result.WebSearchQuery = q
						}
					}
					log.Debugf("kiro/websearch: detected web_search tool_use, query: %s", result.WebSearchQuery)
				}
				currentToolName = ""
				currentToolIndex = -1
				toolInputBuilder.Reset()
			}
		}
	}

	return result
}

// FilterChunksForClient processes buffered SSE chunks and removes web_search tool_use
// content blocks. This prevents the client from seeing "Tool use" prompts for web_search
// when the proxy is handling the search loop internally.
// Also suppresses message_start and message_delta/message_stop events since those
// are managed by the outer handleWebSearchStream.
func FilterChunksForClient(chunks [][]byte, wsToolIndex int, indexOffset int) [][]byte {
	var filtered [][]byte

	for _, chunk := range chunks {
		chunkStr := string(chunk)
		lines := strings.Split(chunkStr, "\n")

		var resultBuilder strings.Builder
		hasContent := false

		for i := 0; i < len(lines); i++ {
			line := lines[i]

			if strings.HasPrefix(line, "data: ") {
				dataPayload := strings.TrimPrefix(line, "data: ")
				dataPayload = strings.TrimSpace(dataPayload)

				if dataPayload == "[DONE]" {
					// Skip [DONE] — the outer loop manages stream termination
					continue
				}

				var event map[string]interface{}
				if err := json.Unmarshal([]byte(dataPayload), &event); err != nil {
					resultBuilder.WriteString(line + "\n")
					hasContent = true
					continue
				}

				eventType, _ := event["type"].(string)

				// Skip message_start (outer loop sends its own)
				if eventType == "message_start" {
					continue
				}

				// Skip message_delta and message_stop (outer loop manages these)
				if eventType == "message_delta" || eventType == "message_stop" {
					continue
				}

				// Check if this event belongs to the web_search tool_use block
				if wsToolIndex >= 0 {
					if idx, ok := event["index"].(float64); ok && int(idx) == wsToolIndex {
						// Skip events for the web_search tool_use block
						continue
					}
				}

				// Apply index offset for remaining events
				if indexOffset > 0 {
					switch eventType {
					case "content_block_start", "content_block_delta", "content_block_stop":
						if idx, ok := event["index"].(float64); ok {
							event["index"] = int(idx) + indexOffset
							adjusted, err := json.Marshal(event)
							if err == nil {
								resultBuilder.WriteString("data: " + string(adjusted) + "\n")
								hasContent = true
								continue
							}
						}
					}
				}

				resultBuilder.WriteString(line + "\n")
				hasContent = true
			} else if strings.HasPrefix(line, "event: ") {
				// Check if the next data line will be suppressed
				if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
					nextData := strings.TrimPrefix(lines[i+1], "data: ")
					nextData = strings.TrimSpace(nextData)

					var nextEvent map[string]interface{}
					if err := json.Unmarshal([]byte(nextData), &nextEvent); err == nil {
						nextType, _ := nextEvent["type"].(string)
						if nextType == "message_start" || nextType == "message_delta" || nextType == "message_stop" {
							i++ // skip the data line
							continue
						}
						if wsToolIndex >= 0 {
							if idx, ok := nextEvent["index"].(float64); ok && int(idx) == wsToolIndex {
								i++ // skip the data line
								continue
							}
						}
					}
				}
				resultBuilder.WriteString(line + "\n")
				hasContent = true
			} else {
				resultBuilder.WriteString(line + "\n")
				if strings.TrimSpace(line) != "" {
					hasContent = true
				}
			}
		}

		if hasContent {
			filtered = append(filtered, []byte(resultBuilder.String()))
		}
	}

	return filtered
}
