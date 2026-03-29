package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// QoderExecutor executes requests against the Qoder API with COSY authentication
type QoderExecutor struct {
	cfg        *config.Config
	httpClient *http.Client
}

// NewQoderExecutor creates a new Qoder executor
func NewQoderExecutor(cfg *config.Config) *QoderExecutor {
	return &QoderExecutor{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

// Identifier returns the provider identifier
func (e *QoderExecutor) Identifier() string {
	return "qoder"
}

// ExecuteStream executes a streaming request against Qoder API
func (e *QoderExecutor) ExecuteStream(ctx context.Context, authRecord *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	// Get token storage from auth record
	storage, ok := authRecord.Storage.(*qoderauth.QoderTokenStorage)
	if !ok {
		return nil, fmt.Errorf("invalid auth storage type for qoder: %T", authRecord.Storage)
	}

	// Check if token needs refresh
	bufferSeconds := int64(600) // 10 minutes
	if err := qoderauth.RefreshTokenIfNeeded(ctx, e.cfg, storage, bufferSeconds); err != nil {
		log.Warnf("Qoder token refresh failed: %v", err)
	}

	// Parse request to get model and messages
	var chatReq map[string]interface{}
	if err := json.Unmarshal(req.Payload, &chatReq); err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	// Map model name
	model, _ := chatReq["model"].(string)
	qoderModel := model
	if mapped, ok := qoderauth.ModelMap[model]; ok {
		qoderModel = mapped
	}

	// Convert messages to prompt format and normalize tool history
	messagesRaw, _ := chatReq["messages"].([]interface{})
	toolsRaw := chatReq["tools"]
	normalized := normalizeQoderMessages(messagesRaw)
	useNormalized := hasToolHistory(messagesRaw)
	prompt := messagesToPromptGeneric(normalized, toolsRaw)

	requestID := uuid.New().String()
	sessionID := uuid.New().String()

	// Build request body for Qoder API (agent router payload)
	reqBody := map[string]interface{}{
		"requestId":           requestID,
		"sessionId":           sessionID,
		"questionText":        prompt,
		"references":          []interface{}{},
		"mode":                "agent",
		"sessionType":         "ASSISTANT",
		"chatTask":            "FREE_INPUT",
		"stream":              true,
		"source":              1,
		"isReply":             false,
		"taskDefinitionType":  "system",
		"codeLanguage":        "",
		"preferredLanguage":   "English",
		"closeTypewriter":     true,
		"pluginPayloadConfig": map[string]interface{}{},
		"chatContext": map[string]interface{}{
			"text":              prompt,
			"localeLang":        "English",
			"preferredLanguage": "English",
		},
		"extra": map[string]interface{}{
			"modelConfig": map[string]interface{}{
				"key": qoderModel,
			},
		},
		"request_id":       requestID,
		"request_set_id":   requestID,
		"chat_record_id":   requestID,
		"session_id":       sessionID,
		"agent_id":         "agent_common",
		"task_id":          "common",
		"chat_task":        "FREE_INPUT",
		"version":          "3",
		"aliyun_user_type": "personal_standard",
		"session_type":     "ASSISTANT",
		"parameters": map[string]interface{}{
			"max_new_tokens": 16384,
			"max_tokens":     16384,
		},
		"model_config": map[string]interface{}{
			"key":                   qoderModel,
			"display_name":          qoderModel,
			"model":                 "",
			"format":                "",
			"is_vl":                 false,
			"is_reasoning":          false,
			"api_key":               "",
			"url":                   "",
			"source":                "",
			"max_input_tokens":      0,
			"enable":                false,
			"price_factor":          0,
			"original_price_factor": 0,
			"is_default":            false,
			"is_new":                false,
			"exclude_tags":          nil,
			"tags":                  nil,
			"icon":                  nil,
			"strategies":            nil,
		},
		"messages": func() []interface{} {
			if useNormalized {
				return normalized
			}
			return messagesRaw
		}(),
	}
	if toolsRaw != nil {
		reqBody["tools"] = toolsRaw
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build COSY auth headers
	headers, err := qoderauth.BuildAuthHeaders(
		bodyBytes,
		qoderauth.QoderChatURL,
		storage.UserID,
		storage.Token,
		storage.Name,
		storage.Email,
		qoderauth.QoderCLIVersion,
		qoderauth.QoderMachineOS,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build COSY auth: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", qoderauth.QoderChatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", headers.Authorization)
	httpReq.Header.Set("Cosy-Key", headers.CosyKey)
	httpReq.Header.Set("Cosy-User", headers.CosyUser)
	httpReq.Header.Set("Cosy-Date", headers.CosyDate)
	httpReq.Header.Set("X-Request-Id", headers.XRequestID)
	httpReq.Header.Set("X-Machine-OS", headers.XMachineOS)
	httpReq.Header.Set("X-IDE-Platform", headers.XIDEPlatform)
	httpReq.Header.Set("X-Version", headers.XVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	// Send request
	httpResp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer func() { _ = httpResp.Body.Close() }()
		body, _ := io.ReadAll(httpResp.Body)
		return nil, newQoderStatusError(httpResp.StatusCode, string(body))
	}

	// Create streaming channel
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = httpResp.Body.Close() }()

		var debugFile *os.File
		if debugPath := strings.TrimSpace(os.Getenv("QODER_DEBUG_SSE")); debugPath != "" {
			if f, err := os.OpenFile(debugPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				debugFile = f
				defer func() { _ = f.Close() }()
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB max line

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			if debugFile != nil {
				_, _ = debugFile.Write(append([]byte("[raw] "), append(line, '\n')...))
			}

			// Skip non-data lines
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}

			data := bytes.TrimPrefix(line, []byte("data:"))
			data = bytes.TrimPrefix(data, []byte(" "))
			if bytes.Equal(data, []byte("[DONE]")) {
				return
			}
			if debugFile != nil {
				_, _ = debugFile.Write(append([]byte("[data] "), append(data, '\n')...))
			}

			// Parse Qoder response envelope
			var event map[string]interface{}
			if err := json.Unmarshal(data, &event); err != nil {
				continue
			}
			statusVal := 200
			if rawStatus, ok := event["statusCodeValue"]; ok {
				switch v := rawStatus.(type) {
				case float64:
					statusVal = int(v)
				case int:
					statusVal = v
				}
			}
			innerStr, _ := event["body"].(string)
			if statusVal != http.StatusOK {
				msg := innerStr
				if msg == "" {
					msg = fmt.Sprintf("upstream status %d", statusVal)
				}
				out <- cliproxyexecutor.StreamChunk{Err: newQoderStatusError(statusVal, msg)}
				return
			}
			if innerStr == "" {
				continue
			}
			if innerStr == "[DONE]" {
				return
			}
			var inner map[string]interface{}
			if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
				continue
			}
			chunkBytes, err := buildOpenAIChunk(inner, model)
			if err != nil {
				continue
			}
			out <- cliproxyexecutor.StreamChunk{Payload: chunkBytes}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// messagesToPromptGeneric converts generic messages to Qoder prompt format

const qoderToolCallInstructions = "[TOOL CALL INSTRUCTIONS]\nWhen you need to use a tool, output EXACTLY this on its own line and stop:\n\nCalled tool: tool_name({\"arg\": \"value\"})\n\nRules — no exceptions:\n- ONLY use the format above. No JSON-only blocks. No ```bash blocks.\n- If a tool is needed, call it IMMEDIATELY — do not describe what you are about to do, just do it.\n- Do NOT say \"I'll run...\", \"Let me check...\", \"Running now\", \"On it\" — output the Called tool line and stop.\n- To run a shell command: Called tool: exec({\"command\":\"your command here\"})\n- Do NOT invent or fabricate tool results. No results until the system returns them.\n- After receiving a tool result, call another tool or write your final answer.\n- Do NOT offer to perform tasks that require tools you do not have access to.\n- If no tool is needed, respond normally."

const qoderBehaviorInstructions = "[BEHAVIOR INSTRUCTIONS]\nPlan before a multi-step task:\n- If completing the task will require more than 2 tool calls, state your plan in one sentence before the first call.\n- Then execute — do not re-explain the plan on each step.\n\nNarrate progress between calls:\n- After every 2-3 tool calls, emit one short status line so the user can follow along (e.g. \"Found the file, now checking contents...\").\n- Keep it to one line — then immediately make the next tool call.\n\nPersist until the task is done:\n- Do NOT give up after one failed attempt. Try at least 2-5 different approaches before concluding something is impossible.\n- If a command fails, read the error message and fix it — wrong flags, wrong path, wrong syntax. Adjust and retry.\n- Only report failure after genuinely exhausting options. Describe what you tried and what each attempt returned.\n\nVerify before you state:\n- Do NOT state facts about emails, files, data, or system state from memory. If you can check it with a tool, check it first.\n- If you are unsure whether something exists or is true, run a tool to find out before answering.\n- Be honest about things you failed to do or are not sure about — do not make claims not supported by what the tools returned.\n\nRead the help before using an unfamiliar command:\n- If you are unsure what flags or arguments a CLI tool accepts, run it with --help first.\n- Example: Called tool: exec({\"command\":\"gog gmail --help\"})\n- The help output will tell you exactly what to do. Use it — do not guess."

func messagesToPromptGeneric(messages []interface{}, tools interface{}) string {
	parts := make([]string, 0, len(messages)+2)
	if tools != nil {
		parts = append(parts, qoderToolCallInstructions)
		parts = append(parts, qoderBehaviorInstructions)
	}

	for _, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		content := extractContentGeneric(msgMap["content"])

		switch role {
		case "system":
			parts = append(parts, "[System Instructions]\n"+content)
		case "assistant":
			parts = append(parts, "[Previous Assistant Response]\n"+content)
		case "user":
			parts = append(parts, content)
		case "tool":
			name, _ := msgMap["name"].(string)
			if name == "" {
				name = "tool"
			}
			parts = append(parts, fmt.Sprintf("[Tool Result for %s]\n%s", name, content))
		}
	}

	return strings.Join(parts, "\n\n")
}

// extractContentGeneric extracts text content from message content field
func extractContentGeneric(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if itemMap["type"] == "text" {
					if text, ok := itemMap["text"].(string); ok {
						parts = append(parts, text)
					}
					continue
				}
				if text, ok := itemMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", content)
	}
}

func normalizeQoderMessages(messages []interface{}) []interface{} {
	if len(messages) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(messages))
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		switch role {
		case "tool":
			name, _ := msgMap["name"].(string)
			if name == "" {
				name = "tool"
			}
			content := extractContentGeneric(msgMap["content"])
			out = append(out, map[string]interface{}{
				"role":    "user",
				"content": fmt.Sprintf("[Tool Result for %s]\n%s", name, content),
			})
		case "assistant":
			if toolCalls, ok := msgMap["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
				parts := make([]string, 0, len(toolCalls))
				for _, call := range toolCalls {
					callMap, ok := call.(map[string]interface{})
					if !ok {
						continue
					}
					fn, _ := callMap["function"].(map[string]interface{})
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					if name == "" {
						name = "?"
					}
					if args == "" {
						args = "{}"
					}
					parts = append(parts, fmt.Sprintf("Called tool: %s(%s)", name, args))
				}
				content := extractContentGeneric(msgMap["content"])
				text := strings.Join(parts, "\n")
				if content != "" {
					text = content + "\n" + text
				}
				out = append(out, map[string]interface{}{
					"role":    "assistant",
					"content": text,
				})
				continue
			}
			out = append(out, msgMap)
		default:
			out = append(out, msgMap)
		}
	}
	return out
}

func hasToolHistory(messages []interface{}) bool {
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role == "tool" {
			return true
		}
		if role == "assistant" {
			if toolCalls, ok := msgMap["tool_calls"].([]interface{}); ok && len(toolCalls) > 0 {
				return true
			}
		}
	}
	return false
}

func buildOpenAIChunk(inner map[string]interface{}, model string) ([]byte, error) {
	if inner == nil {
		return nil, fmt.Errorf("empty inner payload")
	}
	if _, ok := inner["model"]; !ok || inner["model"] == "" {
		inner["model"] = model
	}
	if choices, ok := inner["choices"].([]interface{}); ok {
		if len(choices) == 0 {
			if inner["finish_reason"] != nil || inner["stop"] != nil {
				inner["choices"] = []map[string]interface{}{{
					"index":         0,
					"delta":         map[string]interface{}{},
					"finish_reason": "stop",
				}}
			}
		}
	}
	return json.Marshal(inner)
}

// convertToOpenAIChunk converts Qoder response chunk to OpenAI format
func convertToOpenAIChunk(qoderChunk map[string]interface{}, model string) map[string]interface{} {
	choices, _ := qoderChunk["choices"].([]interface{})
	if len(choices) == 0 {
		return map[string]interface{}{
			"id":      fmt.Sprintf("qoder-%d", time.Now().UnixNano()),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"}},
		}
	}

	choice, _ := choices[0].(map[string]interface{})
	delta, _ := choice["delta"].(map[string]interface{})
	finishReasonRaw, _ := choice["finish_reason"].(interface{})

	var finishReason *string
	if finishReasonRaw != nil {
		fr := fmt.Sprintf("%v", finishReasonRaw)
		finishReason = &fr
	}

	return map[string]interface{}{
		"id":      fmt.Sprintf("qoder-%d", time.Now().UnixNano()),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"content": delta["content"],
				},
				"finish_reason": finishReason,
			},
		},
	}
}

// qoderStatusError implements StatusError for Qoder API errors
type qoderStatusError struct {
	status  int
	message string
}

func newQoderStatusError(status int, message string) *qoderStatusError {
	return &qoderStatusError{status: status, message: message}
}

func (e *qoderStatusError) Error() string {
	return fmt.Sprintf("Qoder API error %d: %s", e.status, e.message)
}

func (e *qoderStatusError) StatusCode() int {
	return e.status
}

// CountTokens estimates token count for the request (placeholder implementation)
func (e *QoderExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Simple estimation: 1 token ≈ 4 characters
	var chatReq map[string]interface{}
	if err := json.Unmarshal(req.Payload, &chatReq); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	messagesRaw, _ := chatReq["messages"].([]interface{})
	totalChars := 0
	for _, msg := range messagesRaw {
		if msgMap, ok := msg.(map[string]interface{}); ok {
			content := extractContentGeneric(msgMap["content"])
			totalChars += len(content)
		}
	}

	estimatedTokens := totalChars / 4
	if estimatedTokens < 1 {
		estimatedTokens = 1
	}

	response := map[string]interface{}{
		"usage": map[string]int{
			"prompt_tokens":     estimatedTokens,
			"completion_tokens": 0,
			"total_tokens":      estimatedTokens,
		},
	}

	responseBytes, _ := json.Marshal(response)
	return cliproxyexecutor.Response{
		Payload: responseBytes,
	}, nil
}

// Execute executes a non-streaming request against Qoder API
func (e *QoderExecutor) Execute(ctx context.Context, authRecord *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Use streaming executor and accumulate
	streamResult, err := e.ExecuteStream(ctx, authRecord, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	// Accumulate all chunks
	var content strings.Builder
	var finishReason string
	type pendingToolCall struct {
		ID        string
		Name      string
		Arguments string
	}
	pendingToolCalls := make(map[int]*pendingToolCall)

	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			return cliproxyexecutor.Response{}, chunk.Err
		}

		var oiChunk map[string]interface{}
		if err := json.Unmarshal(chunk.Payload, &oiChunk); err == nil {
			if choices, ok := oiChunk["choices"].([]interface{}); ok && len(choices) > 0 {
				if choice, ok := choices[0].(map[string]interface{}); ok {
					if delta, ok := choice["delta"].(map[string]interface{}); ok {
						if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
							for _, call := range toolCalls {
								callMap, ok := call.(map[string]interface{})
								if !ok {
									continue
								}
								idx := 0
								if rawIdx, ok := callMap["index"].(float64); ok {
									idx = int(rawIdx)
								}
								entry := pendingToolCalls[idx]
								if entry == nil {
									entry = &pendingToolCall{}
									pendingToolCalls[idx] = entry
								}
								if id, ok := callMap["id"].(string); ok && id != "" {
									entry.ID = id
								}
								if fn, ok := callMap["function"].(map[string]interface{}); ok {
									if name, ok := fn["name"].(string); ok && name != "" {
										entry.Name = name
									}
									if args, ok := fn["arguments"].(string); ok && args != "" {
										entry.Arguments += args
									}
								}
							}
						}
						if contentStr, ok := delta["content"].(string); ok {
							content.WriteString(contentStr)
						}
					}
					if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
						finishReason = fr
					}
				}
			}
		}
	}

	var toolCalls []map[string]interface{}
	if finishReason == "tool_calls" && len(pendingToolCalls) > 0 {
		for i := 0; i < len(pendingToolCalls); i++ {
			entry, ok := pendingToolCalls[i]
			if !ok || entry == nil {
				continue
			}
			id := entry.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", time.Now().UnixNano())
			}
			args := entry.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   id,
				"type": "function",
				"function": map[string]interface{}{
					"name":      entry.Name,
					"arguments": args,
				},
			})
		}
	}

	// Build final response
	message := map[string]interface{}{
		"role":    "assistant",
		"content": content.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	response := map[string]interface{}{
		"id":      fmt.Sprintf("qoder-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}

	responseBytes, _ := json.Marshal(response)
	return cliproxyexecutor.Response{
		Payload: responseBytes,
		Headers: streamResult.Headers,
	}, nil
}

// Refresh attempts to refresh Qoder credentials
func (e *QoderExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	storage, ok := auth.Storage.(*qoderauth.QoderTokenStorage)
	if !ok {
		return nil, fmt.Errorf("invalid auth storage type for qoder")
	}

	qoderAuth := qoderauth.NewQoderAuth(e.cfg)
	tokenData, err := qoderAuth.RefreshTokens(ctx, storage.Token, storage.RefreshToken)
	if err != nil {
		return nil, err
	}

	qoderAuth.UpdateTokenStorage(storage, tokenData)
	return auth, nil
}

// HttpRequest injects Qoder COSY authentication into the HTTP request and executes it
func (e *QoderExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	storage, ok := auth.Storage.(*qoderauth.QoderTokenStorage)
	if !ok {
		return nil, fmt.Errorf("invalid auth storage type for qoder")
	}

	// Read request body for COSY signing
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Build COSY auth headers
	headers, err := qoderauth.BuildAuthHeaders(
		bodyBytes,
		req.URL.String(),
		storage.UserID,
		storage.Token,
		storage.Name,
		storage.Email,
		qoderauth.QoderCLIVersion,
		qoderauth.QoderMachineOS,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build COSY auth: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", headers.Authorization)
	req.Header.Set("Cosy-Key", headers.CosyKey)
	req.Header.Set("Cosy-User", headers.CosyUser)
	req.Header.Set("Cosy-Date", headers.CosyDate)
	req.Header.Set("X-Request-Id", headers.XRequestID)
	req.Header.Set("X-Machine-OS", headers.XMachineOS)
	req.Header.Set("X-IDE-Platform", headers.XIDEPlatform)
	req.Header.Set("X-Version", headers.XVersion)

	// Execute request
	req = req.WithContext(ctx)
	return e.httpClient.Do(req)
}
