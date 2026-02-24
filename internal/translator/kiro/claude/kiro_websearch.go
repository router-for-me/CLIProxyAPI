// Package claude provides web search functionality for Kiro translator.
// This file implements detection, MCP request/response types, and pure data
// transformation utilities for web search. SSE event generation, stream analysis,
// and HTTP I/O logic reside in the executor package (kiro_executor.go).
package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// cachedToolDescription stores the dynamically-fetched web_search tool description.
// Written by the executor via SetWebSearchDescription, read by the translator
// when building the remote_web_search tool for Kiro API requests.
var cachedToolDescription atomic.Value // stores string

// GetWebSearchDescription returns the cached web_search tool description,
// or empty string if not yet fetched. Lock-free via atomic.Value.
func GetWebSearchDescription() string {
	if v := cachedToolDescription.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// SetWebSearchDescription stores the dynamically-fetched web_search tool description.
// Called by the executor after fetching from MCP tools/list.
func SetWebSearchDescription(desc string) {
	cachedToolDescription.Store(desc)
}

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

// GenerateToolUseID generates a Kiro-style tool use ID (base62-like UUID)
func GenerateToolUseID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")[:22]
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
// Use ReplaceWebSearchToolDescription to keep the tool available with a minimal description.
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

	log.Infof("kiro/websearch: injected tool_use+tool_result (toolUseId=%s, messages=%d)",
		toolUseId, len(messages))

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

// BuildMcpEndpoint constructs the MCP endpoint URL for the given AWS region.
// Centralizes the URL pattern used by both handleWebSearch and handleWebSearchStream.
func BuildMcpEndpoint(region string) string {
	return fmt.Sprintf("https://q.%s.amazonaws.com/mcp", region)
}

// ParseSearchResults extracts WebSearchResults from MCP response
func ParseSearchResults(response *McpResponse) *WebSearchResults {
	if response == nil || response.Result == nil || len(response.Result.Content) == 0 {
		return nil
	}

	content := response.Result.Content[0]
	if content.ContentType != "text" {
		return nil
	}

	var results WebSearchResults
	if err := json.Unmarshal([]byte(content.Text), &results); err != nil {
		log.Warnf("kiro/websearch: failed to parse search results: %v", err)
		return nil
	}

	return &results
}
