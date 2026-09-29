package toolconv

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// claudeWebSearchInputFromResponses narrows the richer Responses action union
// to the only operation exposed by Claude's web-search server tool: one query.
func claudeWebSearchInputFromResponses(raw json.RawMessage) (map[string]any, error) {
	canonical, err := dto.NormalizeResponsesWebSearchAction(raw)
	if err != nil {
		return nil, err
	}
	var action struct {
		Type    string   `json:"type"`
		Query   string   `json:"query"`
		Queries []string `json:"queries"`
	}
	if err := kitutil.Unmarshal(canonical, &action); err != nil {
		return nil, fmt.Errorf("decode normalized Responses web-search action: %w", err)
	}
	if action.Type != "search" {
		return nil, fmt.Errorf("Responses web-search action %q has no Claude equivalent", action.Type)
	}

	queries := make([]string, 0, len(action.Queries)+1)
	for _, query := range action.Queries {
		query = strings.TrimSpace(query)
		if query != "" {
			queries = append(queries, query)
		}
	}
	deprecatedQuery := strings.TrimSpace(action.Query)
	if len(queries) == 0 && deprecatedQuery != "" {
		queries = append(queries, deprecatedQuery)
	} else if deprecatedQuery != "" && (len(queries) != 1 || queries[0] != deprecatedQuery) {
		return nil, fmt.Errorf("Responses web-search action contains conflicting query and queries fields")
	}
	if len(queries) != 1 {
		return nil, fmt.Errorf("Claude web search requires exactly one query, got %d", len(queries))
	}
	return map[string]any{"query": queries[0]}, nil
}

// responsesMCPArgumentsFromClaude converts Claude's JSON-object input into the
// JSON string required by a Responses mcp_call.arguments field.
func responsesMCPArgumentsFromClaude(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || kitutil.GetJsonType(raw) != "object" {
		return nil, fmt.Errorf("Claude MCP input must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := kitutil.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("decode Claude MCP input: %w", err)
	}
	encoded, err := kitutil.Marshal(trimmed)
	if err != nil {
		return nil, fmt.Errorf("encode Responses MCP arguments: %w", err)
	}
	return encoded, nil
}

// claudeMCPInputFromResponses decodes the outer Responses JSON string and
// validates that its contents satisfy Claude's JSON-object input contract.
func claudeMCPInputFromResponses(raw json.RawMessage) (any, error) {
	var encoded string
	if len(raw) == 0 || kitutil.GetJsonType(raw) != "string" {
		return nil, fmt.Errorf("Responses MCP arguments must be a JSON string")
	}
	if err := kitutil.Unmarshal(raw, &encoded); err != nil {
		return nil, fmt.Errorf("decode Responses MCP arguments string: %w", err)
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || kitutil.GetJsonType(json.RawMessage(encoded)) != "object" {
		return nil, fmt.Errorf("Responses MCP arguments must contain a JSON object")
	}
	var input map[string]any
	if err := kitutil.Unmarshal([]byte(encoded), &input); err != nil {
		return nil, fmt.Errorf("decode Responses MCP arguments object: %w", err)
	}
	return input, nil
}

// responsesMCPStringFromClaudeContent maps the Claude result shapes that can
// be represented without changing their meaning. A single text block is the
// structured form of a plain MCP text result; other block arrays can contain
// media/resources that a Responses string cannot faithfully preserve.
func responsesMCPStringFromClaudeContent(raw json.RawMessage) (json.RawMessage, bool, error) {
	switch kitutil.GetJsonType(raw) {
	case "string":
		var value string
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return nil, false, fmt.Errorf("decode Claude MCP result string: %w", err)
		}
		return append(json.RawMessage(nil), raw...), false, nil
	case "array":
		var blocks []map[string]json.RawMessage
		if err := kitutil.Unmarshal(raw, &blocks); err != nil {
			return nil, false, fmt.Errorf("decode Claude MCP result blocks: %w", err)
		}
		if len(blocks) == 0 {
			encoded, err := kitutil.Marshal("")
			return encoded, true, err
		}
		if len(blocks) != 1 {
			return nil, false, fmt.Errorf("Responses MCP output cannot preserve %d Claude content blocks", len(blocks))
		}
		var blockType string
		if err := kitutil.Unmarshal(blocks[0]["type"], &blockType); err != nil || blockType != "text" {
			return nil, false, fmt.Errorf("Responses MCP output can only preserve a Claude text result block")
		}
		var text string
		if err := kitutil.Unmarshal(blocks[0]["text"], &text); err != nil {
			return nil, false, fmt.Errorf("decode Claude MCP text result: %w", err)
		}
		encoded, err := kitutil.Marshal(text)
		if err != nil {
			return nil, false, fmt.Errorf("encode Responses MCP output: %w", err)
		}
		return encoded, true, nil
	default:
		return nil, false, fmt.Errorf("Responses MCP output cannot preserve Claude result type %q", kitutil.GetJsonType(raw))
	}
}

func claudeMCPContentFromResponsesString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || kitutil.GetJsonType(raw) != "string" {
		return "", fmt.Errorf("Responses MCP output/error must be a JSON string")
	}
	var content string
	if err := kitutil.Unmarshal(raw, &content); err != nil {
		return "", fmt.Errorf("decode Responses MCP output/error string: %w", err)
	}
	return content, nil
}

func claudeHostedResultFailure(blockType string, content json.RawMessage, explicitError *bool, explicitCode string) (bool, string) {
	if explicitError != nil && *explicitError {
		return true, strings.TrimSpace(explicitCode)
	}
	if strings.TrimSpace(explicitCode) != "" {
		return true, strings.TrimSpace(explicitCode)
	}
	if !strings.HasSuffix(strings.TrimSpace(blockType), "_tool_result") || kitutil.GetJsonType(content) != "object" {
		return false, ""
	}
	var resultError struct {
		Type      string `json:"type"`
		ErrorCode string `json:"error_code"`
	}
	if kitutil.Unmarshal(content, &resultError) != nil {
		return false, ""
	}
	if !strings.HasSuffix(strings.TrimSpace(resultError.Type), "_error") && strings.TrimSpace(resultError.ErrorCode) == "" {
		return false, ""
	}
	return true, strings.TrimSpace(resultError.ErrorCode)
}

func responsesMCPErrorFromClaudeContent(raw json.RawMessage, errorCode string) (json.RawMessage, bool, error) {
	if errorCode = strings.TrimSpace(errorCode); errorCode != "" {
		encoded, err := kitutil.Marshal(errorCode)
		return encoded, false, err
	}
	return responsesMCPStringFromClaudeContent(raw)
}
