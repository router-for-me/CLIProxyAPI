package executor

import (
	"net/http"
	"strings"

	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// responsesAPIAgentTypes lists input types that indicate agent/tool activity in the
// OpenAI Responses API format. When any of these types appear in the input array,
// the request should be marked as an agent call (X-Initiator: agent).
// See: https://platform.openai.com/docs/api-reference/responses
var responsesAPIAgentTypes = map[string]bool{
	"function_call":           true,
	"function_call_output":    true,
	"computer_call":           true,
	"computer_call_output":    true,
	"web_search_call":         true,
	"file_search_call":        true,
	"code_interpreter_call":   true,
	"local_shell_call":        true,
	"local_shell_call_output": true,
	"mcp_call":                true,
	"mcp_list_tools":          true,
	"mcp_approval_request":    true,
	"mcp_approval_response":   true,
	"image_generation_call":   true,
	"reasoning":               true,
}

// isResponsesAPIAgentItem checks if a single item from the Responses API input array
// indicates agent/tool activity. This is used to determine the X-Initiator header value.
func isResponsesAPIAgentItem(item gjson.Result) bool {
	// Check for assistant role
	if item.Get("role").String() == "assistant" {
		return true
	}
	// Check for agent-related input types
	return responsesAPIAgentTypes[item.Get("type").String()]
}

// isResponsesAPIVisionContent checks if a content part from the Responses API
// contains image data, indicating a vision request.
func isResponsesAPIVisionContent(part gjson.Result) bool {
	return part.Get("type").String() == "input_image"
}

type copilotHeaderHints struct {
	hasVision        bool
	agentFromPayload bool
	promptCacheKey   string
}

func promptCacheKeyFromPayload(payload []byte) string {
	if v := gjson.GetBytes(payload, "prompt_cache_key"); v.Exists() {
		if key := strings.TrimSpace(v.String()); key != "" {
			return key
		}
	}
	if v := gjson.GetBytes(payload, "metadata.prompt_cache_key"); v.Exists() {
		if key := strings.TrimSpace(v.String()); key != "" {
			return key
		}
	}
	return ""
}

func collectCopilotHeaderHints(payload []byte) copilotHeaderHints {
	hints := copilotHeaderHints{promptCacheKey: promptCacheKeyFromPayload(payload)}

	// Chat Completions format (messages array)
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			content := msg.Get("content")
			if content.IsArray() {
				for _, part := range content.Array() {
					if part.Get("type").String() == "image_url" {
						hints.hasVision = true
					}
				}
			}
			role := msg.Get("role").String()
			if role == "assistant" || role == "tool" {
				hints.agentFromPayload = true
			}
		}
	}

	// Responses API format (input array)
	input := gjson.GetBytes(payload, "input")
	if input.IsArray() {
		for _, item := range input.Array() {
			content := item.Get("content")
			if content.IsArray() {
				for _, part := range content.Array() {
					if isResponsesAPIVisionContent(part) {
						hints.hasVision = true
					}
				}
			}
			if isResponsesAPIAgentItem(item) {
				hints.agentFromPayload = true
			}
		}
	}

	return hints
}

func (e *CopilotExecutor) agentInitiatorPersistEnabled() bool {
	if e == nil || e.cfg == nil {
		return false
	}
	for i := range e.cfg.CopilotKey {
		if e.cfg.CopilotKey[i].AgentInitiatorPersist {
			return true
		}
	}
	return false
}

func (e *CopilotExecutor) shouldUseAgentInitiator(h copilotHeaderHints) bool {
	if e != nil && e.agentInitiatorPersistEnabled() && h.promptCacheKey != "" {
		e.mu.Lock()
		count := e.initiatorCount[h.promptCacheKey]
		e.initiatorCount[h.promptCacheKey] = count + 1
		e.mu.Unlock()

		if h.agentFromPayload {
			return true
		}
		return count > 0
	}

	return h.agentFromPayload
}

// applyCopilotHeaders applies all necessary headers to the request.
// It handles both Chat Completions format (messages array) and Responses API format (input array).
func (e *CopilotExecutor) applyCopilotHeaders(r *http.Request, copilotToken string, payload []byte) {
	hints := collectCopilotHeaderHints(payload)
	isAgentCall := e.shouldUseAgentInitiator(hints)

	headers := copilotauth.CopilotHeaders(copilotToken, "", hints.hasVision)
	for k, v := range headers {
		r.Header.Set(k, v)
	}

	// Align with Copilot CLI defaults
	r.Header.Set("X-Interaction-Type", "conversation-agent")
	r.Header.Set("Openai-Intent", "conversation-agent")
	r.Header.Set("X-Stainless-Retry-Count", "0")
	r.Header.Set("X-Stainless-Lang", "js")
	r.Header.Set("X-Stainless-Package-Version", "5.20.1")
	r.Header.Set("X-Stainless-OS", "Linux")
	r.Header.Set("X-Stainless-Arch", "arm64")
	r.Header.Set("X-Stainless-Runtime", "node")
	r.Header.Set("X-Stainless-Runtime-Version", "v22.15.0")
	r.Header.Set("User-Agent", copilotauth.CopilotUserAgent)
	if isAgentCall {
		r.Header.Set("X-Initiator", "agent")
		log.Info("copilot executor: [agent call]")
	} else {
		r.Header.Set("X-Initiator", "user")
		log.Info("copilot executor: [user call]")
	}
}
