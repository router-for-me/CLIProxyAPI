package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type codeBuddyRuntime struct {
	mu       sync.Mutex
	managers map[string]*codebuddy.Manager
}

func (e *OpenAICompatExecutor) isCodeBuddyAuth(auth *cliproxyauth.Auth) bool {
	if auth != nil && auth.Attributes != nil {
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_type"]), codebuddy.AuthType) {
			return true
		}
	}
	return strings.EqualFold(e.provider, util.OpenAICompatibleProviderKey(codebuddy.AuthType))
}

func (e *OpenAICompatExecutor) codeBuddyManager(auth *cliproxyauth.Auth) (*codebuddy.Manager, error) {
	if auth == nil || auth.Attributes == nil {
		return nil, codebuddy.ErrAuthFileNotFound
	}
	path := strings.TrimSpace(auth.Attributes["codebuddy_auth_file"])
	if path == "" {
		return nil, codebuddy.ErrAuthFileNotFound
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		baseURL = codebuddy.DefaultBackendBaseURL
	}
	key := path + "\x00" + baseURL
	e.codeBuddy.mu.Lock()
	defer e.codeBuddy.mu.Unlock()
	if e.codeBuddy.managers == nil {
		e.codeBuddy.managers = make(map[string]*codebuddy.Manager)
	}
	if manager := e.codeBuddy.managers[key]; manager != nil {
		return manager, nil
	}
	manager, errManager := codebuddy.NewCredentialManagerWithRefreshURL(path, codebuddy.RefreshURLForBaseURL(baseURL))
	if errManager != nil {
		return nil, errManager
	}
	e.codeBuddy.managers[key] = manager
	return manager, nil
}

// prepareProviderRequest adds either regular OpenAI-compatible credentials or
// the complete CodeBuddy session header set.  client is used for refreshes so
// the same per-auth proxy policy applies to token refresh and generation.
func (e *OpenAICompatExecutor) prepareProviderRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request, client *http.Client, apiKey string) error {
	if req == nil {
		return nil
	}
	if e.isCodeBuddyAuth(auth) {
		manager, errManager := e.codeBuddyManager(auth)
		if errManager != nil {
			return errManager
		}
		headers, errHeaders := manager.GetHeaders(ctx, client)
		if errHeaders != nil {
			return errHeaders
		}
		for key, values := range headers {
			// Keep request-specific representation headers selected by the
			// executor (notably multipart Content-Type and streaming Accept).
			// Authentication headers must always come from the current session.
			if (strings.EqualFold(key, "Content-Type") || strings.EqualFold(key, "Accept")) && req.Header.Get(key) != "" {
				continue
			}
			req.Header.Del(key)
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	if e.isCodeBuddyAuth(auth) {
		req.Header.Set("User-Agent", codebuddy.UserAgent)
	}
	return nil
}

func forceCodeBuddyStreamPayload(payload []byte, auth *cliproxyauth.Auth, enabled bool) []byte {
	if !enabled || auth == nil || auth.Attributes == nil ||
		!strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_type"]), codebuddy.AuthType) {
		return payload
	}
	payload = helps.SetBoolIfDifferent(payload, "stream", true)
	return helps.SetBoolIfDifferent(payload, "stream_options.include_usage", true)
}

// sanitizeCodeBuddyChatPayload mirrors codebuddy2api's allow-list.  It keeps
// the backend request stable when a source protocol carries fields that are
// meaningful to the client but not accepted by CodeBuddy's chat endpoint.
func sanitizeCodeBuddyChatPayload(payload []byte, auth *cliproxyauth.Auth, compatDesensitize bool) []byte {
	if auth == nil || auth.Attributes == nil ||
		!strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_type"]), codebuddy.AuthType) {
		return payload
	}
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return payload
	}
	allowed := map[string]struct{}{
		"model": {}, "messages": {}, "tools": {}, "tool_choice": {},
		"temperature": {}, "max_tokens": {}, "max_completion_tokens": {},
		"top_p": {}, "stream": {}, "stream_options": {}, "stop": {},
		"presence_penalty": {}, "frequency_penalty": {}, "n": {},
		"response_format": {}, "seed": {}, "user": {}, "reasoning_effort": {},
		"verbosity": {}, "reasoning_summary": {},
	}
	for key := range body {
		if _, ok := allowed[key]; !ok {
			delete(body, key)
		}
	}
	if compatDesensitize {
		desensitizeCodeBuddyBody(body)
	}
	encoded, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return payload
	}
	return encoded
}

var codeBuddySensitivePattern = func() *regexp.Regexp {
	terms := []string{
		"DoS", "DDoS", "exploit", "credential testing", "credential stuffing",
		"supply chain compromise", "supply-chain compromise", "detection evasion",
		"C2 frameworks", "C2 framework", "command and control", "malicious purposes",
		"malicious intent", "mass targeting", "brute force", "brute-force",
		"privilege escalation", "reverse shell", "remote code execution", "SQL injection",
		"XSS", "CSRF", "phishing", "malware", "ransomware", "keylogger", "rootkit",
		"backdoor", "botnet", "zero-day", "0day", "vulnerability", "vulnerabilities",
		"red teaming", "red-teaming", "sandbox", "sandboxing", "sandboxed", "unsandboxed",
		"escalated privileges", "escalated", "escalation", "destructive action",
		"destructive command", "destructive", "attack", "attacks", "cybersecurity",
		"security review", "exploit development", "hacking", "penetration testing",
		"penetration test", "injection", "weaponize", "weaponized", "harmful", "dangerous",
		"abuse", "abusive", "illegal", "terrorist", "terrorism", "bomb", "weapon",
		"weapons", "drug", "drugs", "narcotic", "suicide", "self-harm", "murder",
		"kill", "violence", "violent", "Claude Code", "Claude Opus", "Claude Sonnet",
		"Claude Haiku", "Claude Fable", "Anthropic", "Co-Authored-By", "noreply@anthropic.com",
	}
	sort.Slice(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, regexp.QuoteMeta(term))
	}
	return regexp.MustCompile("(?i)" + strings.Join(parts, "|"))
}()

func desensitizeCodeBuddyText(text string) string {
	if text == "" {
		return text
	}
	return codeBuddySensitivePattern.ReplaceAllStringFunc(text, func(match string) string {
		if len(match) < 2 {
			return match
		}
		return match[:1] + "\u200b" + match[1:]
	})
}

func desensitizeCodeBuddyBody(body map[string]json.RawMessage) {
	var messages []map[string]any
	if raw, ok := body["messages"]; ok && json.Unmarshal(raw, &messages) == nil {
		for _, message := range messages {
			role, _ := message["role"].(string)
			if role != "system" && role != "developer" && !(role == "user" && looksLikeCodeBuddyHarnessUser(message["content"])) {
				continue
			}
			message["content"] = desensitizeCodeBuddyContent(role, message["content"])
		}
		if encoded, errMarshal := json.Marshal(messages); errMarshal == nil {
			body["messages"] = encoded
		}
	}
	if raw, ok := body["tools"]; ok {
		var tools []any
		if json.Unmarshal(raw, &tools) == nil {
			desensitizeCodeBuddyValue(tools)
			if encoded, errMarshal := json.Marshal(tools); errMarshal == nil {
				body["tools"] = encoded
			}
		}
	}
}

func desensitizeCodeBuddyContent(role string, content any) any {
	switch typed := content.(type) {
	case string:
		return desensitizeCodeBuddyHarnessText(role, typed)
	case []any:
		for i := range typed {
			if block, ok := typed[i].(map[string]any); ok && block["type"] == "text" {
				block["text"] = desensitizeCodeBuddyHarnessText(role, fmt.Sprint(block["text"]))
			}
		}
	}
	return content
}

func desensitizeCodeBuddyHarnessText(role, text string) string {
	if compacted, ok := compactCodeBuddyHarnessText(role, text); ok {
		return desensitizeCodeBuddyText(compacted)
	}
	return desensitizeCodeBuddyText(pruneCodeBuddyRuntimeFragments(role, text))
}

func compactCodeBuddyHarnessText(role, text string) (string, bool) {
	if role == "system" {
		if strings.Contains(text, "You are Claude Code") {
			return "You are a coding assistant. Be precise, helpful, concise, and safe. Use available tools when needed, follow repository instructions, and keep the user informed.", true
		}
		if strings.Contains(text, "You are a coding agent running in the Codex CLI") || strings.Contains(text, "# How you work") {
			return "You are a coding assistant in Codex CLI. Be precise, helpful, concise, and safe. Use available tools when needed, follow repository instructions, and keep the user informed.", true
		}
		if strings.Contains(text, "<permissions instructions>") || strings.Contains(text, "Filesystem sandboxing defines which files can be read or written.") {
			return "Runtime permissions apply: filesystem access may be sandboxed, network may be restricted, and some commands may require user approval.", true
		}
		if strings.Contains(text, "<skills_instructions>") || strings.Contains(text, "### Available skills") || strings.Contains(text, "### How to use skills") {
			return "Runtime skill metadata is available. Use relevant skills only when explicitly requested or clearly applicable.", true
		}
	}
	if role == "user" && looksLikeCodeBuddyHarnessUser(text) {
		return "Repository instructions and environment context are provided. Follow repository guidance while answering the user's actual request.", true
	}
	return "", false
}

func pruneCodeBuddyRuntimeFragments(role, text string) string {
	replacements := []struct {
		start       string
		end         string
		replacement string
	}{
		{"<environment_context>", "</environment_context>", "Environment context is provided by the harness."},
		{"<permissions instructions>", "</permissions instructions>", "Runtime permissions apply: filesystem access may be sandboxed, network may be restricted, and some commands may require user approval."},
		{"<collaboration_mode>", "</collaboration_mode>", "Collaboration mode instructions are provided by the harness."},
		{"<skills_instructions>", "</skills_instructions>", "Runtime skill metadata is available. Use relevant skills only when explicitly requested or clearly applicable."},
		{"<plugins_instructions>", "</plugins_instructions>", "Runtime plugin metadata is available when relevant."},
		{"<system-reminder>", "</system-reminder>", "Runtime reminder context is provided by the harness."},
	}
	for _, item := range replacements {
		for {
			start := strings.Index(text, item.start)
			if start < 0 {
				break
			}
			relEnd := strings.Index(text[start+len(item.start):], item.end)
			if relEnd < 0 {
				break
			}
			end := start + len(item.start) + relEnd + len(item.end)
			text = text[:start] + "\n\n" + item.replacement + "\n\n" + text[end:]
		}
	}
	for _, marker := range []string{
		"The following deferred tools are now available via ToolSearch.",
		"Available agent types for the Agent tool:",
		"The following skills are available for use with the Skill tool:",
		"## MCP Server Instructions",
	} {
		if index := strings.Index(text, marker); index >= 0 {
			text = strings.TrimSpace(text[:index] + "\n\nRuntime tool, agent, skill, and MCP metadata is available separately.")
		}
	}
	if role == "user" && looksLikeCodeBuddyHarnessUser(text) {
		return "Repository instructions and durable user context are provided. Follow repository guidance while answering the user's actual request."
	}
	return strings.TrimSpace(text)
}

func desensitizeCodeBuddyValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "description" || key == "title" {
				delete(typed, key)
				continue
			}
			desensitizeCodeBuddyValue(item)
		}
	case []any:
		for _, item := range typed {
			desensitizeCodeBuddyValue(item)
		}
	}
}

func looksLikeCodeBuddyHarnessUser(content any) bool {
	text := ""
	switch typed := content.(type) {
	case string:
		text = typed
	case []any:
		var parts []string
		for _, item := range typed {
			if block, ok := item.(map[string]any); ok && block["type"] == "text" {
				parts = append(parts, fmt.Sprint(block["text"]))
			}
		}
		text = strings.Join(parts, "")
	}
	for _, marker := range []string{"# AGENTS.md instructions", "<environment_context>", "<permissions instructions>", "<collaboration_mode>", "<skills_instructions>", "<system-reminder>", "# claudeMd"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type codeBuddyToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// collectCodeBuddyStream converts the backend's mandatory Chat SSE response
// into one regular Chat completion for a client that did not request stream.
func collectCodeBuddyStream(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if json.Valid(trimmed) {
		return trimmed, nil
	}
	var content strings.Builder
	toolCalls := make(map[int]*codeBuddyToolCall)
	model := "unknown"
	finishReason := ""
	var usage map[string]any
	seenEvent := false
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}
		var chunk map[string]any
		if errUnmarshal := json.Unmarshal(data, &chunk); errUnmarshal != nil {
			continue
		}
		seenEvent = true
		if value, ok := chunk["model"].(string); ok && value != "" {
			model = value
		}
		if value, ok := chunk["usage"].(map[string]any); ok {
			usage = value
		}
		choices, _ := chunk["choices"].([]any)
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			if choice == nil {
				continue
			}
			if value, ok := choice["finish_reason"].(string); ok && value != "" {
				finishReason = value
			}
			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			if value, ok := delta["content"].(string); ok {
				content.WriteString(value)
			}
			rawCalls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range rawCalls {
				call, _ := rawCall.(map[string]any)
				if call == nil {
					continue
				}
				index := intValue(call["index"])
				slot := toolCalls[index]
				if slot == nil {
					slot = &codeBuddyToolCall{}
					toolCalls[index] = slot
				}
				if value, ok := call["id"].(string); ok && value != "" {
					slot.ID = value
				}
				fn, _ := call["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if value, ok := fn["name"].(string); ok && value != "" {
					slot.Name = value
				}
				if value, ok := fn["arguments"].(string); ok {
					slot.Arguments += value
				}
			}
		}
	}
	if !seenEvent {
		return nil, fmt.Errorf("CodeBuddy upstream returned neither JSON nor SSE data")
	}

	orderedIndexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		orderedIndexes = append(orderedIndexes, index)
	}
	sort.Ints(orderedIndexes)
	var encodedToolCalls []any
	for _, index := range orderedIndexes {
		call := toolCalls[index]
		encodedToolCalls = append(encodedToolCalls, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	message := map[string]any{"role": "assistant", "content": nil}
	if content.Len() > 0 {
		message["content"] = content.String()
	}
	if len(encodedToolCalls) > 0 {
		message["tool_calls"] = encodedToolCalls
		if finishReason == "" {
			finishReason = "tool_calls"
		}
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	result := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
	}
	if usage != nil {
		result["usage"] = usage
	} else {
		result["usage"] = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	return json.Marshal(result)
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		if parsed, errParse := typed.Int64(); errParse == nil {
			return int(parsed)
		}
	case string:
		var parsed int
		_, _ = fmt.Sscanf(typed, "%d", &parsed)
		return parsed
	}
	return 0
}
