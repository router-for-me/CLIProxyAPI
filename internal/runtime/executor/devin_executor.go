package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// DevinExecutor handles requests to the Devin (Codeium Cascade) API using the
// Connect-RPC protocol. It impersonates the Devin CLI (chisel) and translates
// between OpenAI chat completion format and the Devin GetChatMessage protobuf.
type DevinExecutor struct {
	cfg *config.Config
}

// NewDevinExecutor creates a new Devin executor instance.
func NewDevinExecutor(cfg *config.Config) *DevinExecutor {
	return &DevinExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *DevinExecutor) Identifier() string { return "devin" }

// PrepareRequest prepares a raw HTTP request with Devin auth headers.
func (e *DevinExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token := devinCredentials(auth)
	if token == "" {
		return fmt.Errorf("devin: missing session token")
	}
	req.Header.Set("Authorization", "Basic "+token+"-"+token)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes a raw HTTP request with Devin auth.
func (e *DevinExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("devin executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request. Devin only supports streaming,
// so we collect all stream chunks and assemble a single response.
func (e *DevinExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	streamResult, err := e.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		return resp, err
	}

	var allText strings.Builder
	var thinkingText strings.Builder
	var stopReason string
	var usageDetail *devinUsageStats
	var toolCalls []devinToolCall
	var messageID string

	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			return resp, chunk.Err
		}
		// Chunks from ExecuteStream are OpenAI chat completion chunk JSON.
		// Extract fields using gjson.
		id := gjson.GetBytes(chunk.Payload, "id").String()
		if id != "" {
			messageID = strings.TrimPrefix(id, "chatcmpl-")
		}
		if content := gjson.GetBytes(chunk.Payload, "choices.0.delta.content").String(); content != "" {
			allText.WriteString(content)
		}
		if reasoning := gjson.GetBytes(chunk.Payload, "choices.0.delta.reasoning").String(); reasoning != "" {
			thinkingText.WriteString(reasoning)
		}
		if fr := gjson.GetBytes(chunk.Payload, "choices.0.finish_reason"); fr.Exists() && fr.Type == gjson.String {
			stopReason = fr.String()
		}
		if pt := gjson.GetBytes(chunk.Payload, "usage.prompt_tokens"); pt.Exists() {
			if usageDetail == nil {
				usageDetail = &devinUsageStats{}
			}
			usageDetail.InputTokens = uint64(pt.Int())
			usageDetail.OutputTokens = uint64(gjson.GetBytes(chunk.Payload, "usage.completion_tokens").Int())
		}
		// Extract tool calls from delta. Tool call arguments arrive in
		// fragments across multiple chunks: the first chunk carries the
		// id and function name, subsequent chunks carry argument pieces
		// with empty id/name. Merge fragments into the last tool call.
		toolCallsResult := gjson.GetBytes(chunk.Payload, "choices.0.delta.tool_calls")
		if toolCallsResult.IsArray() {
			toolCallsResult.ForEach(func(_, tc gjson.Result) bool {
				tcID := tc.Get("id").String()
				tcName := tc.Get("function.name").String()
				tcArgs := tc.Get("function.arguments").String()
				if tcID != "" && tcName != "" {
					toolCalls = append(toolCalls, devinToolCall{
						ID:            tcID,
						Name:          tcName,
						ArgumentsJSON: tcArgs,
					})
				} else if len(toolCalls) > 0 {
					toolCalls[len(toolCalls)-1].ArgumentsJSON += tcArgs
				}
				return true
			})
		}
	}

	if stopReason == "" {
		stopReason = "stop"
	}

	openaiResp := buildOpenAIChatCompletion(messageID, req.Model, allText.String(), thinkingText.String(), stopReason, toolCalls, usageDetail)
	resp = cliproxyexecutor.Response{Payload: openaiResp}
	return resp, nil
}

// ExecuteStream performs a streaming request to the Devin GetChatMessage endpoint.
func (e *DevinExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	token := devinCredentials(auth)
	if token == "" {
		return nil, fmt.Errorf("devin: missing session token")
	}

	parsedReq, err := devinParseOpenAIRequest(req.Payload)
	if err != nil {
		return nil, fmt.Errorf("devin: failed to parse request: %w", err)
	}

	devinReq := buildDevinRequest(parsedReq, token, baseModel)
	reqBytes := encodeGetChatMessageRequest(devinReq)
	frame := frameConnectMessage(reqBytes)

	url := devinAPIBaseURL + devinChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", devinConnectProto)
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	httpReq.Header.Set("Authorization", "Basic "+token+"-"+token)
	httpReq.Header.Set("User-Agent", "")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Set("Accept", "*/*")

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      frame,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		httpResp.Body.Close()
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("devin: failed to close response body: %v", errClose)
			}
		}()

		buf := make([]byte, 0, 64*1024)
		tmp := make([]byte, 32*1024)
		var usagePublished bool

		for {
			n, errRead := httpResp.Body.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				appendAPIResponseChunk(ctx, e.cfg, tmp[:n])
			}

			// Parse complete Connect frames from the buffer.
			for len(buf) >= 5 {
				f, remaining, errFrame := readConnectFrame(buf)
				if errFrame != nil {
					break // need more data
				}
				buf = remaining

				if f.Flag&connectFlagEndStream != 0 {
					continue // trailer frame (JSON), skip
				}

				chunk, errParse := parseGetChatMessageResponse(f.Payload)
				if errParse != nil {
					log.Debugf("devin: failed to parse response frame: %v", errParse)
					continue
				}

				// Publish usage stats once.
				if chunk.Usage != nil && !usagePublished {
					reporter.publish(ctx, usage.Detail{
						InputTokens:     int64(chunk.Usage.InputTokens),
						OutputTokens:    int64(chunk.Usage.OutputTokens),
						CacheReadTokens: int64(chunk.Usage.CacheReadTokens),
						TotalTokens:     int64(chunk.Usage.InputTokens + chunk.Usage.OutputTokens),
					})
					usagePublished = true
				}

				sseData := buildOpenAIStreamChunk(chunk, req.Model)
				if sseData != "" {
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(sseData)}
				}
			}

			if errRead != nil {
				if errRead != io.EOF {
					recordAPIResponseError(ctx, e.cfg, errRead)
					reporter.publishFailure(ctx)
					out <- cliproxyexecutor.StreamChunk{Err: errRead}
				}
				break
			}
		}

		if !usagePublished {
			reporter.ensurePublished(ctx)
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

// Refresh validates the Devin token. Devin session tokens are static and don't
// support refresh, so we just return the auth unchanged.
func (e *DevinExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *DevinExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("devin: count tokens not supported")
}

// devinCredentials extracts the session token from auth metadata/attributes.
func devinCredentials(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["devin_session_token"].(string); ok && token != "" {
			return normalizeDevinSessionToken(token)
		}
		if token, ok := auth.Metadata["session_token"].(string); ok && token != "" {
			return normalizeDevinSessionToken(token)
		}
		if token, ok := auth.Metadata["api_key"].(string); ok && token != "" {
			return normalizeDevinSessionToken(token)
		}
	}
	if auth.Attributes != nil {
		if token := auth.Attributes["devin_session_token"]; token != "" {
			return normalizeDevinSessionToken(token)
		}
		if token := auth.Attributes["session_token"]; token != "" {
			return normalizeDevinSessionToken(token)
		}
		if token := auth.Attributes[cliproxyauth.AttributeAPIKey]; token != "" {
			return normalizeDevinSessionToken(token)
		}
	}
	return ""
}

// devinOpenAIMessage represents a message in the OpenAI chat format.
type devinOpenAIMessage struct {
	Role       string                `json:"role"`
	Content    json.RawMessage       `json:"content,omitempty"`
	ToolCalls  []devinOpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	Name       string                `json:"name,omitempty"`
}

type devinOpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type devinOpenAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// devinParsedRequest holds the extracted fields from an OpenAI chat request.
type devinParsedRequest struct {
	Model        string
	Messages     []devinOpenAIMessage
	Tools        []devinOpenAITool
	Temperature  *float64
	MaxTokens    *int
	TopP         *float64
	Stream       bool
	SystemPrompt string
}

// devinParseOpenAIRequest extracts messages, tools, and config from an OpenAI chat request payload.
func devinParseOpenAIRequest(payload []byte) (*devinParsedRequest, error) {
	parsed := &devinParsedRequest{}
	parsed.Model = gjson.GetBytes(payload, "model").String()
	parsed.Stream = gjson.GetBytes(payload, "stream").Bool()

	if temp := gjson.GetBytes(payload, "temperature"); temp.Exists() {
		t := temp.Float()
		parsed.Temperature = &t
	}
	if mt := gjson.GetBytes(payload, "max_tokens"); mt.Exists() {
		m := int(mt.Int())
		parsed.MaxTokens = &m
	}
	if tp := gjson.GetBytes(payload, "top_p"); tp.Exists() {
		p := tp.Float()
		parsed.TopP = &p
	}

	messagesResult := gjson.GetBytes(payload, "messages")
	if !messagesResult.IsArray() {
		return nil, fmt.Errorf("messages field is required and must be an array")
	}
	messagesResult.ForEach(func(_, msg gjson.Result) bool {
		var m devinOpenAIMessage
		m.Role = msg.Get("role").String()
		contentResult := msg.Get("content")
		if contentResult.Exists() {
			if contentResult.Type == gjson.String {
				m.Content = json.RawMessage(`"` + contentResult.String() + `"`)
			} else {
				m.Content = json.RawMessage(contentResult.Raw)
			}
		}
		toolCallsResult := msg.Get("tool_calls")
		if toolCallsResult.IsArray() {
			toolCallsResult.ForEach(func(_, tc gjson.Result) bool {
				var openAITC devinOpenAIToolCall
				openAITC.ID = tc.Get("id").String()
				openAITC.Type = tc.Get("type").String()
				openAITC.Function.Name = tc.Get("function.name").String()
				openAITC.Function.Arguments = tc.Get("function.arguments").String()
				m.ToolCalls = append(m.ToolCalls, openAITC)
				return true
			})
		}
		m.ToolCallID = msg.Get("tool_call_id").String()
		m.Name = msg.Get("name").String()
		parsed.Messages = append(parsed.Messages, m)
		return true
	})

	for _, msg := range parsed.Messages {
		if msg.Role == "system" {
			if len(msg.Content) > 0 {
				var s string
				if err := json.Unmarshal(msg.Content, &s); err == nil {
					if parsed.SystemPrompt != "" {
						parsed.SystemPrompt += "\n"
					}
					parsed.SystemPrompt += s
				}
			}
		}
	}

	toolsResult := gjson.GetBytes(payload, "tools")
	if toolsResult.IsArray() {
		toolsResult.ForEach(func(_, tool gjson.Result) bool {
			var t devinOpenAITool
			t.Type = tool.Get("type").String()
			t.Function.Name = tool.Get("function.name").String()
			t.Function.Description = tool.Get("function.description").String()
			paramsResult := tool.Get("function.parameters")
			if paramsResult.Exists() {
				t.Function.Parameters = json.RawMessage(paramsResult.Raw)
			}
			parsed.Tools = append(parsed.Tools, t)
			return true
		})
	}

	return parsed, nil
}

// buildDevinRequest converts an OpenAI request to a Devin GetChatMessageRequest.
func buildDevinRequest(parsed *devinParsedRequest, token, model string) devinRequest {
	cascadeID := uuid.New().String()
	installID := "cliproxyapi-" + cascadeID[:8]

	req := devinRequest{
		Metadata: devinMetadata{
			IdeName:          "devin-cli",
			ExtensionVersion: "3000.4.25",
			ApiKey:           token,
			Locale:           "en",
			OS:               "darwin",
			IdeVersion:       "3000.4.25",
			ExtensionName:    "chisel",
			IdeType:          "chisel",
			F:                generateAttestationF(installID),
		},
		Prompt:      parsed.SystemPrompt,
		RequestType: devinRequestTypeCascade,
		Configuration: devinCompletionConfig{
			NumCompletions: 1,
			MaxTokens:      128000,
			MaxNewlines:    400,
			Temperature:    1.0,
			TopK:           40,
			TopP:           0.95,
		},
		CascadeID:    cascadeID,
		PlannerMode:  devinPlannerModeDefault,
		ChatModelUID: model,
	}

	if parsed.MaxTokens != nil {
		req.Configuration.MaxTokens = uint64(*parsed.MaxTokens)
	}
	if parsed.Temperature != nil {
		req.Configuration.Temperature = *parsed.Temperature
	}
	if parsed.TopP != nil {
		req.Configuration.TopP = *parsed.TopP
	}

	for _, msg := range parsed.Messages {
		if msg.Role == "system" {
			continue
		}

		prompt := devinChatMessagePrompt{
			MessageID: uuid.New().String(),
		}

		switch msg.Role {
		case "user":
			prompt.Source = devinMsgSourceUser
			prompt.Prompt = devinExtractTextContent(msg.Content)
		case "assistant":
			prompt.Source = devinMsgSourceSystem
			prompt.Prompt = devinExtractTextContent(msg.Content)
			prompt.MessageID = "bot-" + uuid.New().String()
			for _, tc := range msg.ToolCalls {
				prompt.ToolCalls = append(prompt.ToolCalls, devinToolCall{
					ID:            tc.ID,
					Name:          tc.Function.Name,
					ArgumentsJSON: tc.Function.Arguments,
				})
			}
		case "tool":
			prompt.Source = devinMsgSourceTool
			prompt.Prompt = devinExtractTextContent(msg.Content)
			prompt.ToolCallID = msg.ToolCallID
		}

		req.ChatMessagePrompts = append(req.ChatMessagePrompts, prompt)
	}

	for _, tool := range parsed.Tools {
		req.Tools = append(req.Tools, devinToolDefinition{
			Name:             tool.Function.Name,
			Description:      tool.Function.Description,
			JSONSchemaString: string(tool.Function.Parameters),
		})
	}

	return req
}

// devinExtractTextContent extracts text from an OpenAI content field (string or array of parts).
func devinExtractTextContent(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// buildOpenAIChatCompletion creates a non-streaming OpenAI chat completion response.
func buildOpenAIChatCompletion(messageID, model, text, thinking, stopReason string, toolCalls []devinToolCall, usageStats *devinUsageStats) []byte {
	var b strings.Builder
	b.WriteString(`{"id":"chatcmpl-`)
	b.WriteString(messageID)
	b.WriteString(`","object":"chat.completion","created":`)
	b.WriteString(fmt.Sprintf("%d", time.Now().Unix()))
	b.WriteString(`,"model":"`)
	b.WriteString(model)
	b.WriteString(`","choices":[{"index":0,"message":{"role":"assistant"`)

	if thinking != "" {
		b.WriteString(`,"reasoning":"`)
		b.WriteString(devinJSONEscape(thinking))
		b.WriteString(`"`)
	}

	if text != "" {
		b.WriteString(`,"content":"`)
		b.WriteString(devinJSONEscape(text))
		b.WriteString(`"`)
	} else {
		b.WriteString(`,"content":null`)
	}

	if len(toolCalls) > 0 {
		b.WriteString(`,"tool_calls":[`)
		for i, tc := range toolCalls {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"id":"`)
			b.WriteString(devinJSONEscape(tc.ID))
			b.WriteString(`","type":"function","function":{"name":"`)
			b.WriteString(devinJSONEscape(tc.Name))
			b.WriteString(`","arguments":"`)
			b.WriteString(devinJSONEscape(tc.ArgumentsJSON))
			b.WriteString(`"}}`)
		}
		b.WriteString(`]`)
	}

	b.WriteString(`},"finish_reason":"`)
	b.WriteString(stopReason)
	b.WriteString(`"}],"usage":{"prompt_tokens":`)
	if usageStats != nil {
		b.WriteString(fmt.Sprintf("%d", usageStats.InputTokens))
		b.WriteString(`,"completion_tokens":`)
		b.WriteString(fmt.Sprintf("%d", usageStats.OutputTokens))
		b.WriteString(`,"total_tokens":`)
		b.WriteString(fmt.Sprintf("%d", usageStats.InputTokens+usageStats.OutputTokens))
	} else {
		b.WriteString("0,\"completion_tokens\":0,\"total_tokens\":0")
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}

// buildOpenAIStreamChunk converts a Devin response chunk to an OpenAI chat completion
// chunk JSON object. The proxy wraps each chunk with "data: %s\n\n" framing.
func buildOpenAIStreamChunk(chunk *devinStreamChunk, model string) string {
	if chunk.MessageID == "" && chunk.DeltaText == "" && chunk.DeltaThinking == "" && chunk.StopReason == 0 && len(chunk.DeltaToolCalls) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(`{"id":"chatcmpl-`)
	if chunk.MessageID != "" {
		b.WriteString(devinJSONEscape(chunk.MessageID))
	} else {
		b.WriteString("devin-stream")
	}
	b.WriteString(`","object":"chat.completion.chunk","created":`)
	b.WriteString(fmt.Sprintf("%d", time.Now().Unix()))
	b.WriteString(`,"model":"`)
	b.WriteString(devinJSONEscape(model))
	b.WriteString(`","choices":[{"index":0,"delta":{`)

	first := true
	if chunk.DeltaThinking != "" {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"reasoning":"`)
		b.WriteString(devinJSONEscape(chunk.DeltaThinking))
		b.WriteString(`"`)
	}
	if chunk.DeltaText != "" {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"content":"`)
		b.WriteString(devinJSONEscape(chunk.DeltaText))
		b.WriteString(`"`)
	}
	if len(chunk.DeltaToolCalls) > 0 {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"tool_calls":[`)
		for i, tc := range chunk.DeltaToolCalls {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"index":0,"id":"`)
			b.WriteString(devinJSONEscape(tc.ID))
			b.WriteString(`","type":"function","function":{"name":"`)
			b.WriteString(devinJSONEscape(tc.Name))
			b.WriteString(`","arguments":"`)
			b.WriteString(devinJSONEscape(tc.ArgumentsJSON))
			b.WriteString(`"}}`)
		}
		b.WriteString(`]`)
	}
	if chunk.StopReason != 0 {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"role":"assistant"`)
	}

	b.WriteString(`},"finish_reason":`)
	if chunk.StopReason != 0 {
		b.WriteString(`"`)
		b.WriteString(devinStopReasonToString(chunk.StopReason))
		b.WriteString(`"`)
	} else {
		b.WriteString("null")
	}
	b.WriteString(`}]`)

	if chunk.Usage != nil && (chunk.Usage.InputTokens > 0 || chunk.Usage.OutputTokens > 0) {
		b.WriteString(`,"usage":{"prompt_tokens":`)
		b.WriteString(fmt.Sprintf("%d", chunk.Usage.InputTokens))
		b.WriteString(`,"completion_tokens":`)
		b.WriteString(fmt.Sprintf("%d", chunk.Usage.OutputTokens))
		b.WriteString(`,"total_tokens":`)
		b.WriteString(fmt.Sprintf("%d", chunk.Usage.InputTokens+chunk.Usage.OutputTokens))
		b.WriteString(`}`)
	}

	b.WriteString("}")
	return b.String()
}

// devinJSONEscape escapes a string for safe embedding in JSON.
func devinJSONEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b[1 : len(b)-1])
}
