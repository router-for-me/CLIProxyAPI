package session

import (
	"encoding/json"
	"fmt"
	"time"
)

// timeNow is a variable for testability
var timeNow = time.Now

// InjectHistory injects session message history into a request payload.
// It modifies the request JSON to prepend historical messages before new user messages.
// Supports OpenAI, Claude, and Gemini message formats.
func InjectHistory(requestJSON []byte, session *Session) ([]byte, error) {
	if session == nil || len(session.Messages) == 0 {
		return requestJSON, nil
	}

	// Parse request as generic map
	var req map[string]interface{}
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return nil, fmt.Errorf("session inject: unmarshal request failed: %w", err)
	}

	// Extract existing messages from request
	existingMsgs, ok := req["messages"].([]interface{})
	if !ok {
		// No messages field - might be a completion request or unsupported format
		return requestJSON, nil
	}

	// Convert session history to generic message format
	historyMsgs := make([]interface{}, 0, len(session.Messages))
	for _, msg := range session.Messages {
		historyMsg := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		historyMsgs = append(historyMsgs, historyMsg)
	}

	// Prepend history to existing messages
	allMessages := append(historyMsgs, existingMsgs...)
	req["messages"] = allMessages

	// Re-serialize with history included
	modifiedJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("session inject: marshal modified request failed: %w", err)
	}

	return modifiedJSON, nil
}

// ExtractAssistantMessage extracts the assistant's response from a completion response payload.
// It parses the response and returns a Message suitable for appending to session history.
// Returns nil if the response doesn't contain a valid assistant message.
func ExtractAssistantMessage(responseJSON []byte, provider, model string) (*Message, error) {
	if len(responseJSON) == 0 {
		return nil, nil
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(responseJSON, &resp); err != nil {
		return nil, fmt.Errorf("session extract: unmarshal response failed: %w", err)
	}

	// Try to extract message from choices array (OpenAI format)
	if choices, ok := resp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				return extractMessageFromMap(message, provider, model, resp)
			}
		}
	}

	// Try to extract from content array (Claude format)
	if content, ok := resp["content"].([]interface{}); ok && len(content) > 0 {
		// Claude returns content blocks
		msg := &Message{
			Role:      "assistant",
			Content:   content,
			Provider:  provider,
			Model:     model,
			Timestamp: timeNow(),
		}
		if usage := extractTokenUsage(resp); usage > 0 {
			msg.TokensUsed = usage
		}
		return msg, nil
	}

	// Try to extract from text field (Gemini format)
	if candidates, ok := resp["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					// Extract text from first part
					if part, ok := parts[0].(map[string]interface{}); ok {
						if text, ok := part["text"].(string); ok {
							msg := &Message{
								Role:      "assistant",
								Content:   text,
								Provider:  provider,
								Model:     model,
								Timestamp: timeNow(),
							}
							if usage := extractTokenUsage(resp); usage > 0 {
								msg.TokensUsed = usage
							}
							return msg, nil
						}
					}
				}
			}
		}
	}

	return nil, nil
}

func extractMessageFromMap(message map[string]interface{}, provider, model string, fullResp map[string]interface{}) (*Message, error) {
	role, _ := message["role"].(string)
	if role == "" {
		role = "assistant"
	}

	content := message["content"]

	msg := &Message{
		Role:      role,
		Content:   content,
		Provider:  provider,
		Model:     model,
		Timestamp: timeNow(),
	}

	if usage := extractTokenUsage(fullResp); usage > 0 {
		msg.TokensUsed = usage
	}

	return msg, nil
}

func extractTokenUsage(resp map[string]interface{}) int {
	// Try OpenAI/Claude standard usage object
	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		// Try completion_tokens (OpenAI)
		if tokens, ok := usage["completion_tokens"].(float64); ok {
			return int(tokens)
		}
		// Try output_tokens (Claude)
		if tokens, ok := usage["output_tokens"].(float64); ok {
			return int(tokens)
		}
	}
	// Try Gemini usageMetadata object (uses candidatesTokenCount for completion tokens)
	if usageMeta, ok := resp["usageMetadata"].(map[string]interface{}); ok {
		// Use candidatesTokenCount for accurate completion token count
		if tokens, ok := usageMeta["candidatesTokenCount"].(float64); ok {
			return int(tokens)
		}
	}
	return 0
}
