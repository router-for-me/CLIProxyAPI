package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codearts"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codeartsChatURL   = "https://snap-access.cn-north-4.myhuaweicloud.com/v1/chat/chat"
	codeArtsUserAgent = "DevKit-VSCode:huaweicloud.codearts-snap|CodeArts Agent:D1"
)

// CodeArtsExecutor executes chat completions against the HuaweiCloud CodeArts API.
type CodeArtsExecutor struct {
	cfg *config.Config
}

// NewCodeArtsExecutor constructs a new executor instance.
func NewCodeArtsExecutor(cfg *config.Config) *CodeArtsExecutor {
	return &CodeArtsExecutor{cfg: cfg}
}

// Identifier returns the executor's provider key.
func (e *CodeArtsExecutor) Identifier() string { return "codearts" }

// PrepareRequest sets CodeArts-specific headers and signs the request.
func (e *CodeArtsExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("codearts: missing auth metadata")
	}

	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	securityToken, _ := auth.Metadata["security_token"].(string)

	if ak == "" || sk == "" {
		return fmt.Errorf("codearts: missing AK/SK credentials")
	}

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	}

	traceID := generateTraceID()

	req.Header.Set("User-Agent", codeArtsUserAgent)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Agent-Type", "ChatAgent")
	req.Header.Set("Client-Version", "Vscode_26.3.5")
	req.Header.Set("Heartbeat-Enable", "true")
	req.Header.Set("Ide-Name", "CodeArts Agent")
	req.Header.Set("Ide-Version", "1.96.4")
	req.Header.Set("Is-Confidential", "false")
	req.Header.Set("Plugin-Name", "snap_vscode")
	req.Header.Set("Plugin-Version", "26.3.5")
	req.Header.Set("X-Language", "zh-cn")
	req.Header.Set("X-Snap-Traceid", traceID)

	codearts.SignRequest(req, bodyBytes, ak, sk, securityToken)

	log.Debugf("codearts: signing request url=%s, body_len=%d, ak=%s, headers=%v",
		req.URL.String(), len(bodyBytes), ak[:min(4, len(ak))]+"...", req.Header)
	return nil
}

// HttpRequest executes a signed HTTP request to CodeArts.
func (e *CodeArtsExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 5*time.Minute)

	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codearts: request failed: %w", err)
	}
	return resp, nil
}

// Execute handles non-streaming chat completions.
func (e *CodeArtsExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	agentID := codearts.DefaultAgentID
	if auth.Attributes != nil {
		if aid := strings.TrimSpace(auth.Attributes["agent_id"]); aid != "" {
			agentID = aid
		}
	}

	userID := extractUserID(auth)

	payload := buildCodeArtsPayload(req.Payload, baseModel, agentID, userID, opts)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", codeartsChatURL, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return resp, err
	}
	defer httpResp.Body.Close()

	log.Debugf("codearts: Execute response status=%d, content_type=%s", httpResp.StatusCode, httpResp.Header.Get("Content-Type"))

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		return resp, statusErr{
			code: httpResp.StatusCode,
			msg:  fmt.Sprintf("codearts: API returned %d: %s", httpResp.StatusCode, string(body)),
		}
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var promptTokens, completionTokens int64
	var respModel string
	toolCallsAccumulated := make(map[int]map[string]interface{})

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ":heartbeat") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") && !strings.HasPrefix(line, "data:") {
			continue
		}
		var data string
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		} else {
			data = strings.TrimPrefix(line, "data:")
		}
		if data == "[DONE]" || (gjson.Get(data, "text").String() == "[DONE]") {
			break
		}

		errorCode := gjson.Get(data, "error_code")
		if errorCode.Exists() && errorCode.Int() != 0 {
			errMsg := gjson.Get(data, "error_msg").String()
			return cliproxyexecutor.Response{}, fmt.Errorf("codearts: error %d: %s", errorCode.Int(), errMsg)
		}

		delta := gjson.Get(data, "delta")
		if delta.Exists() {
			if c := delta.Get("content").String(); c != "" {
				contentBuilder.WriteString(c)
			}
			if r := delta.Get("reasoning_content").String(); r != "" {
				reasoningBuilder.WriteString(r)
			}
			if tcList := delta.Get("tool_calls"); tcList.Exists() && len(tcList.Array()) > 0 {
				for _, tc := range tcList.Array() {
					idx := int(tc.Get("index").Int())
					if _, exists := toolCallsAccumulated[idx]; !exists {
						toolCallsAccumulated[idx] = map[string]interface{}{
							"id":   tc.Get("id").String(),
							"type": tc.Get("type").String(),
							"function": map[string]interface{}{
								"name":      tc.Get("function.name").String(),
								"arguments": tc.Get("function.arguments").String(),
							},
						}
					} else {
						existing := toolCallsAccumulated[idx]
						if id := tc.Get("id").String(); id != "" {
							existing["id"] = id
						}
						fnMap, _ := existing["function"].(map[string]interface{})
						if name := tc.Get("function.name").String(); name != "" {
							fnMap["name"] = name
						}
						if args := tc.Get("function.arguments").String(); args != "" {
							fnMap["arguments"] = fnMap["arguments"].(string) + args
						}
					}
				}
			}
		}
		if mn := gjson.Get(data, "model_name").String(); mn != "" {
			respModel = mn
		}
		if pt := gjson.Get(data, "prompt_tokens").Int(); pt > 0 {
			promptTokens = pt
		}
		if ct := gjson.Get(data, "completion_tokens").Int(); ct > 0 {
			completionTokens = ct
		}
	}

	var toolCallsList []map[string]interface{}
	if len(toolCallsAccumulated) > 0 {
		indices := make([]int, 0, len(toolCallsAccumulated))
		for k := range toolCallsAccumulated {
			indices = append(indices, k)
		}
		sort.Ints(indices)
		for _, k := range indices {
			toolCallsList = append(toolCallsList, toolCallsAccumulated[k])
		}
	}

	fullContent := contentBuilder.String()
	if len(toolCallsList) == 0 && fullContent != "" && strings.Contains(fullContent, "<tool_call_id>") {
		xmlToolCalls := parseXMLToolCalls(fullContent)
		if len(xmlToolCalls) > 0 {
			toolCallsList = xmlToolCalls
			stripped := stripXMLToolCalls(fullContent)
			if stripped == "" {
				fullContent = ""
			} else {
				fullContent = stripped
			}
		}
	}

	if respModel == "" {
		respModel = req.Model
	}

	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("codearts")

	openAIResp := buildOpenAINonStreamResponse(fullContent, reasoningBuilder.String(), respModel, promptTokens, completionTokens, toolCallsList)
	var param any
	translated := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, openAIResp, &param)

	reporter.Publish(ctx, usage.Detail{
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
	})

	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:      codeartsChatURL,
		Method:   "POST",
		Provider: "codearts",
		AuthID:   auth.ID,
	})

	return cliproxyexecutor.Response{Payload: translated}, nil
}

// ExecuteStream handles streaming chat completions.
func (e *CodeArtsExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	parsed := thinking.ParseSuffix(req.Model)
	baseModel := parsed.ModelName

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	agentID := codearts.DefaultAgentID
	if auth.Attributes != nil {
		if aid := strings.TrimSpace(auth.Attributes["agent_id"]); aid != "" {
			agentID = aid
		}
	}

	userID := extractUserID(auth)

	payload := buildCodeArtsPayload(req.Payload, baseModel, agentID, userID, opts)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", codeartsChatURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	httpResp, err := e.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode != 200 {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		log.Debugf("codearts: non-200 response status=%d, body=%s", httpResp.StatusCode, string(body))
		return nil, statusErr{
			code: httpResp.StatusCode,
			msg:  fmt.Sprintf("codearts: API returned %d: %s", httpResp.StatusCode, string(body)),
		}
	}

	log.Debugf("codearts: stream response status=%d, content_type=%s, content_length=%d",
		httpResp.StatusCode, httpResp.Header.Get("Content-Type"), httpResp.ContentLength)

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer close(chunks)
		defer httpResp.Body.Close()

		from := sdktranslator.FromString("openai")
		to := sdktranslator.FromString("codearts")
		var streamParam any
		var totalPromptTokens, totalCompletionTokens int64
		var lineCount int
		var dataLineCount int
		var firstNonEmptyLine string
		var accumulatedContent strings.Builder
		var hasToolCalls bool

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			lineCount++
			if strings.HasPrefix(line, ":heartbeat") || line == "" {
				continue
			}
			if firstNonEmptyLine == "" {
				firstNonEmptyLine = line
			}
			var data string
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			} else if strings.HasPrefix(line, "data:") {
				data = strings.TrimPrefix(line, "data:")
			} else {
				log.Debugf("codearts: unexpected SSE line %d: %q", lineCount, line)
				continue
			}
			if data == "[DONE]" || (gjson.Get(data, "text").String() == "[DONE]") {
				break
			}
			dataLineCount++

			result := convertCodeArtsSSEToOpenAI(data, req.Model)
			if result.Err != nil {
				log.Warnf("codearts: chunk error: %v", result.Err)
				continue
			}
			if result.Chunk == nil {
				if pt := gjson.Get(data, "prompt_tokens").Int(); pt > 0 {
					totalPromptTokens = pt
				}
				if ct := gjson.Get(data, "completion_tokens").Int(); ct > 0 {
					totalCompletionTokens = ct
				}
				continue
			}

			if result.HasToolCalls {
				hasToolCalls = true
			} else if result.HasContent {
				accumulatedContent.WriteString(result.ContentValue)
			}

			if result.FinishReason == "stop" {
				if pt := gjson.Get(data, "prompt_tokens").Int(); pt > 0 {
					totalPromptTokens = pt
				}
				if ct := gjson.Get(data, "completion_tokens").Int(); ct > 0 {
					totalCompletionTokens = ct
				}
			}

			translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, result.Chunk, &streamParam)
			for _, tc := range translatedChunks {
				if len(tc) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tc}
				}
			}
		}

		if !hasToolCalls && accumulatedContent.Len() > 0 && strings.Contains(accumulatedContent.String(), "<tool_call_id>") {
			xmlToolCalls := parseXMLToolCalls(accumulatedContent.String())
			if len(xmlToolCalls) > 0 {
				hasToolCalls = true
				for i, tc := range xmlToolCalls {
					chunk := buildToolCallStreamChunk(req.Model, i, tc)
					translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, chunk, &streamParam)
					for _, tChunk := range translatedChunks {
						if len(tChunk) > 0 {
							chunks <- cliproxyexecutor.StreamChunk{Payload: tChunk}
						}
					}
				}
			}
		}

		if hasToolCalls {
			finishChunk := buildFinishReasonStreamChunk(req.Model, "tool_calls")
			translatedChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, req.Payload, finishChunk, &streamParam)
			for _, tChunk := range translatedChunks {
				if len(tChunk) > 0 {
					chunks <- cliproxyexecutor.StreamChunk{Payload: tChunk}
				}
			}
		}

		if dataLineCount == 0 {
			log.Warnf("codearts: stream ended with no data lines (total_lines=%d, first_non_empty=%q)", lineCount, firstNonEmptyLine)
		}

		if err := scanner.Err(); err != nil {
			log.Warnf("codearts: stream scanner error: %v", err)
			chunks <- cliproxyexecutor.StreamChunk{Err: err}
		}

		reporter.Publish(ctx, usage.Detail{
			InputTokens:  totalPromptTokens,
			OutputTokens: totalCompletionTokens,
		})

		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:      codeartsChatURL,
			Method:   "POST",
			Provider: "codearts",
			AuthID:   auth.ID,
		})
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  chunks,
	}, nil
}

// CountTokens is not supported by CodeArts.
func (e *CodeArtsExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("codearts: token counting not supported")
}

// Refresh refreshes the CodeArts security token.
func (e *CodeArtsExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("codearts: no metadata to refresh")
	}

	currentToken := extractCodeArtsToken(auth)
	if currentToken == nil {
		return nil, fmt.Errorf("codearts: no valid token data found for refresh")
	}

	if !codearts.NeedsRefresh(currentToken) {
		return auth, nil
	}

	caAuth := codearts.NewCodeArtsAuth(nil)
	newToken, err := caAuth.RefreshToken(ctx, currentToken)
	if err != nil {
		return nil, fmt.Errorf("codearts: refresh failed: %w", err)
	}

	updated := auth.Clone()
	updated.Metadata["ak"] = newToken.AK
	updated.Metadata["sk"] = newToken.SK
	updated.Metadata["security_token"] = newToken.SecurityToken
	updated.Metadata["expires_at"] = newToken.ExpiresAt.Format(time.RFC3339)
	if newToken.XAuthToken != "" {
		updated.Metadata["x_auth_token"] = newToken.XAuthToken
	}

	log.Infof("codearts: successfully refreshed token, expires at %s", newToken.ExpiresAt.Format(time.RFC3339))
	return updated, nil
}

// extractCodeArtsToken extracts token data from auth metadata.
func extractCodeArtsToken(auth *cliproxyauth.Auth) *codearts.CodeArtsTokenData {
	if auth == nil || auth.Metadata == nil {
		return nil
	}

	ak, _ := auth.Metadata["ak"].(string)
	sk, _ := auth.Metadata["sk"].(string)
	if ak == "" || sk == "" {
		return nil
	}

	token := &codearts.CodeArtsTokenData{
		AK:            ak,
		SK:            sk,
		SecurityToken: metadataStr(auth.Metadata, "security_token"),
		XAuthToken:    metadataStr(auth.Metadata, "x_auth_token"),
		Email:         metadataStr(auth.Metadata, "email"),
	}

	if expiresStr := metadataStr(auth.Metadata, "expires_at"); expiresStr != "" {
		if t, err := time.Parse(time.RFC3339, expiresStr); err == nil {
			token.ExpiresAt = t
		}
	}

	return token
}

func metadataStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extractUserID(auth *cliproxyauth.Auth) string {
	if auth.Metadata != nil {
		if uid, ok := auth.Metadata["user_id"].(string); ok {
			return uid
		}
		if did, ok := auth.Metadata["domain_id"].(string); ok {
			return did
		}
	}
	return ""
}

func generateTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateChatID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func generateToolCallID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("call_%019d", time.Now().UnixNano())
	}
	return "call_" + hex.EncodeToString(b)
}

const toolsSystemPromptTemplate = "# Available Tools\n\nYou have access to the following tools. You MUST respond with tool calls using the exact XML format specified below.\n\n%s\n\n# Tool Call Format\n\nWhen you need to use a tool, you MUST output the tool call in the following XML format:\n\n<tool_call_id>call_<random_hex_24chars></tool_call_id>\n<tool_name>function_name_here</tool_name>\n<tool_arguments>\n{\"param1\": \"value1\", \"param2\": \"value2\"}\n</tool_arguments>\n\nRules:\n- Each tool call MUST have a unique tool_call_id starting with \"call_\" followed by 24 random hex characters.\n- tool_arguments MUST be valid JSON matching the function's parameters schema.\n- You may make multiple tool calls in a single response.\n- When you want to call tools, output ONLY the tool call XML blocks, do NOT output any other text.\n- Do NOT wrap tool calls in markdown code blocks.\n- The tool_call_id MUST be unique for each tool call."

func buildToolsSystemPrompt(tools gjson.Result) string {
	var toolDefs []string
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			continue
		}
		fn := tool.Get("function")
		name := fn.Get("name").String()
		desc := fn.Get("description").String()
		params := fn.Get("parameters").Raw
		if params == "" {
			params = "{}"
		}
		toolDefs = append(toolDefs, fmt.Sprintf("## %s\n%s\nParameters: %s", name, desc, params))
	}
	if len(toolDefs) == 0 {
		return ""
	}
	return fmt.Sprintf(toolsSystemPromptTemplate, strings.Join(toolDefs, "\n\n"))
}

func parseXMLToolCalls(text string) []map[string]interface{} {
	var results []map[string]interface{}
	segments := strings.Split(text, "<tool_call_id>")
	for _, seg := range segments[1:] {
		idEnd := strings.Index(seg, "</tool_call_id>")
		if idEnd < 0 {
			continue
		}
		tcID := strings.TrimSpace(seg[:idEnd])

		rest := seg[idEnd+len("</tool_call_id>"):]
		nameStart := strings.Index(rest, "<tool_name>")
		if nameStart < 0 {
			continue
		}
		nameStart += len("<tool_name>")
		nameEnd := strings.Index(rest, "</tool_name>")
		if nameEnd < 0 || nameEnd < nameStart {
			continue
		}
		tcName := strings.TrimSpace(rest[nameStart:nameEnd])

		argsRest := rest[nameEnd+len("</tool_name>"):]
		argsStart := strings.Index(argsRest, "<tool_arguments>")
		if argsStart < 0 {
			continue
		}
		argsStart += len("<tool_arguments>")
		argsEnd := strings.Index(argsRest, "</tool_arguments>")
		if argsEnd < 0 || argsEnd < argsStart {
			continue
		}
		argsStr := strings.TrimSpace(argsRest[argsStart:argsEnd])

		if tcID == "" {
			tcID = generateToolCallID()
		}
		results = append(results, map[string]interface{}{
			"id":   tcID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      tcName,
				"arguments": argsStr,
			},
		})
	}
	return results
}

func stripXMLToolCalls(text string) string {
	result := text
	for strings.Contains(result, "<tool_call_id>") && strings.Contains(result, "</tool_arguments>") {
		start := strings.Index(result, "<tool_call_id>")
		end := strings.Index(result, "</tool_arguments>") + len("</tool_arguments>")
		if end <= start {
			break
		}
		result = result[:start] + result[end:]
	}
	return strings.TrimSpace(result)
}

func buildToolCallStreamChunk(model string, index int, toolCall map[string]interface{}) []byte {
	tc := map[string]interface{}{
		"index":    index,
		"id":       toolCall["id"],
		"type":     "function",
		"function": toolCall["function"],
	}
	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{tc},
				},
			},
		},
	}
	result, _ := json.Marshal(chunk)
	return result
}

func buildFinishReasonStreamChunk(model string, finishReason string) []byte {
	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": finishReason,
			},
		},
	}
	result, _ := json.Marshal(chunk)
	return result
}

// buildCodeArtsPayload converts the OpenAI-format payload to CodeArts format.
func buildCodeArtsPayload(openaiPayload []byte, modelName, agentID, userID string, opts cliproxyexecutor.Options) []byte {
	messages := gjson.GetBytes(openaiPayload, "messages")
	if !messages.Exists() {
		log.Warn("codearts: no messages found in payload")
		return openaiPayload
	}

	var codeArtsMessages []map[string]string
	for _, msg := range messages.Array() {
		role := msg.Get("role").String()
		content := extractTextContent(msg.Get("content"))

		var formattedContent string
		switch role {
		case "system":
			formattedContent = "[System]\n" + content
		case "assistant":
			toolCalls := msg.Get("tool_calls")
			if toolCalls.Exists() && len(toolCalls.Array()) > 0 {
				var parts []string
				if content != "" {
					parts = append(parts, content)
				}
				for _, tc := range toolCalls.Array() {
					name := tc.Get("function.name").String()
					id := tc.Get("id").String()
					args := tc.Get("function.arguments").String()
					parts = append(parts, fmt.Sprintf("[Tool Call: %s] (id: %s)\n%s", name, id, args))
				}
				formattedContent = "[Assistant]\n" + strings.Join(parts, "\n")
			} else {
				formattedContent = "[Assistant]\n" + content
			}
		case "tool":
			toolName := msg.Get("name").String()
			toolID := msg.Get("tool_call_id").String()
			if toolName == "" {
				toolName = "unknown"
			}
			formattedContent = fmt.Sprintf("[Tool Result: %s] (id: %s)\n%s", toolName, toolID, content)
		case "user":
			formattedContent = content
		default:
			formattedContent = content
		}

		codeArtsMessages = append(codeArtsMessages, map[string]string{
			"type":    "text",
			"content": formattedContent,
		})
	}

	taskParameters := map[string]interface{}{
		"is_intent_recognition":   false,
		"W3_Search":               false,
		"codebase_search":         false,
		"related_question":        true,
		"preferred_language":      "zh-cn",
		"enable_code_interpreter": false,
		"projectLevelPrompt":      "",
		"contexts":                []interface{}{},
		"expert_rules":            []interface{}{},
		"ide":                     "CodeArts Agent",
		"routerVersion":           "v2",
		"isNewClient":             true,
		"features":                map[string]interface{}{"support_end_tag": true},
	}

	if tools := gjson.GetBytes(openaiPayload, "tools"); tools.Exists() {
		taskParameters["tools"] = tools.Value()
		toolsPrompt := buildToolsSystemPrompt(tools)
		if toolsPrompt != "" {
			hasSystem := false
			for i, msg := range codeArtsMessages {
				if strings.HasPrefix(msg["content"], "[System]") {
					codeArtsMessages[i]["content"] = msg["content"] + "\n\n" + toolsPrompt
					hasSystem = true
					break
				}
			}
			if !hasSystem {
				codeArtsMessages = append(
					[]map[string]string{{"type": "text", "content": "[System]\n" + toolsPrompt}},
					codeArtsMessages...,
				)
			}
		}
	}
	if temp := gjson.GetBytes(openaiPayload, "temperature"); temp.Exists() {
		taskParameters["temperature"] = temp.Value()
	}

	chatID := generateChatID()

	request := map[string]interface{}{
		"chat_id":               chatID,
		"messages":              codeArtsMessages,
		"client":                "IDE",
		"task":                  "chat",
		"task_parameters":       taskParameters,
		"batch_task_parameters": []interface{}{},
		"attempt":               1,
		"user_id":               userID,
		"parent_message_id":     "",
		"is_delta_response":     true,
		"model_id":              modelName,
	}

	result, err := json.Marshal(request)
	if err != nil {
		log.Errorf("codearts: failed to marshal payload: %v", err)
		return openaiPayload
	}
	return result
}

// convertCodeArtsSSEToOpenAI converts a CodeArts SSE data line to OpenAI SSE format.
type codeartsStreamResult struct {
	Chunk        []byte
	HasToolCalls bool
	HasContent   bool
	ContentValue string
	FinishReason string
	Err          error
}

func convertCodeArtsSSEToOpenAI(data string, model string) codeartsStreamResult {
	errorCode := gjson.Get(data, "error_code")
	if errorCode.Exists() && errorCode.Int() != 0 {
		errMsg := gjson.Get(data, "error_msg").String()
		return codeartsStreamResult{Err: fmt.Errorf("CodeArts error %d: %s", errorCode.Int(), errMsg)}
	}

	delta := gjson.Get(data, "delta")
	if !delta.Exists() {
		return codeartsStreamResult{}
	}

	contentResult := delta.Get("content")
	reasoningResult := delta.Get("reasoning_content")
	toolCallsResult := delta.Get("tool_calls")

	contentExists := contentResult.Exists()
	contentValue := contentResult.String()
	reasoningExists := reasoningResult.Exists()
	reasoningValue := reasoningResult.String()
	hasToolCalls := toolCallsResult.Exists() && len(toolCallsResult.Array()) > 0

	openaiDelta := make(map[string]interface{})

	if contentExists {
		openaiDelta["content"] = contentValue
	} else if reasoningExists || hasToolCalls {
		openaiDelta["content"] = ""
	}

	if reasoningExists {
		openaiDelta["reasoning_content"] = reasoningValue
	}

	if hasToolCalls {
		openaiDelta["tool_calls"] = toolCallsResult.Value()
	}

	if !contentExists && !reasoningExists && !hasToolCalls {
		role := delta.Get("role").String()
		if role != "" {
			openaiDelta["role"] = role
		}
	}

	if len(openaiDelta) == 0 {
		return codeartsStreamResult{}
	}

	finishReason := ""
	promptTokens := gjson.Get(data, "prompt_tokens").Int()
	completionTokens := gjson.Get(data, "completion_tokens").Int()
	totalTokens := gjson.Get(data, "total_tokens").Int()

	if completionTokens > 0 && !contentExists && !reasoningExists && !hasToolCalls {
		finishReason = "stop"
	}

	respModel := gjson.Get(data, "model_name").String()
	if respModel == "" {
		respModel = model
	}

	chunk := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   respModel,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         openaiDelta,
				"finish_reason": nil,
			},
		},
	}

	if finishReason != "" {
		chunk["choices"].([]map[string]interface{})[0]["finish_reason"] = finishReason
	}

	if totalTokens > 0 {
		chunk["usage"] = map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		}
	}

	result, err := json.Marshal(chunk)
	if err != nil {
		return codeartsStreamResult{}
	}

	return codeartsStreamResult{
		Chunk:        result,
		HasToolCalls: hasToolCalls,
		HasContent:   contentExists && contentValue != "",
		ContentValue: contentValue,
		FinishReason: finishReason,
	}
}

// buildOpenAINonStreamResponse builds a complete OpenAI non-stream response.
func buildOpenAINonStreamResponse(content, reasoning, model string, promptTokens, completionTokens int64, toolCalls []map[string]interface{}) []byte {
	message := map[string]interface{}{
		"role": "assistant",
	}
	if content != "" {
		message["content"] = content
	} else {
		message["content"] = nil
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		message["tool_calls"] = toolCalls
	}

	resp := map[string]interface{}{
		"id":      "chatcmpl-codearts",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}

	result, _ := json.Marshal(resp)
	return result
}
