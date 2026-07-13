package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	windsurfmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/windsurf/models"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	windsurfBaseURL           = "https://server.codeium.com"
	windsurfGetUserJwt        = "/exa.auth_pb.AuthService/GetUserJwt"
	windsurfGetChatMsg        = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	windsurfVersion           = "2.0.0"
	windsurfRequestTimeout    = 120 * time.Second
	windsurfMaxForwardedTools = 30
	jwtCacheMargin            = 60 * time.Second
)

// WindsurfExecutor executes requests against the Windsurf / Devin CLI backend.
// It implements the cliproxy ProviderExecutor interface.
type WindsurfExecutor struct {
	cfg *config.Config

	// jwtCache stores the most recently obtained JWT per API key. Protected by mu.
	jwtCache map[string]*windsurfJwtEntry
	mu       sync.Mutex
}

type windsurfJwtEntry struct {
	apiKey    string
	host      string
	jwt       string
	expiresAt time.Time
}

// NewWindsurfExecutor creates a new Windsurf executor.
func NewWindsurfExecutor(cfg *config.Config) *WindsurfExecutor {
	return &WindsurfExecutor{
		cfg:      cfg,
		jwtCache: make(map[string]*windsurfJwtEntry),
	}
}

// Identifier returns the provider key handled by this executor.
func (e *WindsurfExecutor) Identifier() string { return "windsurf" }

// HttpRequest injects Windsurf credentials into the supplied HTTP request and executes it.
func (e *WindsurfExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("windsurf executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	apiKey, baseURL := windsurfCreds(auth)
	if apiKey == "" {
		return nil, fmt.Errorf("windsurf executor: missing api_key")
	}
	if req.URL != nil {
		req.URL.Scheme = "https"
		req.URL.Host = strings.TrimPrefix(baseURL, "https://")
	}
	req.Header.Set("content-type", "application/proto")
	req.Header.Set("connect-protocol-version", "1")
	return helps.NewUtlsHTTPClient(ctx, e.cfg, auth, windsurfRequestTimeout).Do(req)
}

// Execute handles non-streaming execution.
func (e *WindsurfExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	resp, err := e.executeChat(ctx, auth, req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	// In non-streaming mode, read the full response from the stream channel.
	var full []byte
	for chunk := range resp.Chunks {
		if chunk.Err != nil {
			return cliproxyexecutor.Response{}, chunk.Err
		}
		full = append(full, chunk.Payload...)
	}
	return cliproxyexecutor.Response{Payload: full, Headers: resp.Headers}, nil
}

// ExecuteStream handles streaming execution.
func (e *WindsurfExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.executeChat(ctx, auth, req, opts, true)
}

// CountTokens returns a token count for the given request. This is a best-effort
// approximation since the Windsurf backend does not expose a token endpoint.
func (e *WindsurfExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var body openAIChatRequest
	if err := json.Unmarshal(req.Payload, &body); err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("windsurf count tokens: invalid json: %w", err)
	}
	inputTokens := estimateMessagesTokens(body.Messages)
	out := map[string]any{
		"object":        "text",
		"total_tokens":  inputTokens,
		"prompt_tokens": inputTokens,
		"text":          "",
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: payload}, nil
}

// Refresh attempts to refresh provider credentials. For Windsurf the API key is
// static; we only validate it by fetching a fresh JWT.
func (e *WindsurfExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	apiKey, baseURL := windsurfCreds(auth)
	if apiKey == "" {
		return auth, fmt.Errorf("windsurf executor: missing api_key for refresh")
	}
	if _, err := e.getUserJwt(ctx, apiKey, baseURL); err != nil {
		return auth, err
	}
	return auth, nil
}

// executeChat is the core dispatcher for Windsurf chat requests.
func (e *WindsurfExecutor) executeChat(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*cliproxyexecutor.StreamResult, error) {
	apiKey, baseURL := windsurfCreds(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "windsurf executor: missing api_key"}
	}

	var body openAIChatRequest
	if err := json.Unmarshal(req.Payload, &body); err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("windsurf executor: invalid json: %v", err)}
	}

	modelCfg := windsurfmodels.ParseConfig("")
	modelUid := mapOpenAIModelToWindsurf(req.Model, modelCfg)
	if modelUid == "" {
		modelUid = modelCfg.DefaultModelID
	}

	userJwt, err := e.getUserJwt(ctx, apiKey, baseURL)
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("windsurf auth failed: %v", err)}
	}

	protoReq, err := e.buildGetChatMessageRequest(apiKey, userJwt, modelUid, body)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("windsurf request build failed: %v", err)}
	}

	compressed, err := encodeCompressedConnectEnvelope(protoReq)
	if err != nil {
		return nil, fmt.Errorf("windsurf gzip: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+windsurfGetChatMsg, bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/connect+proto")
	httpReq.Header.Set("connect-protocol-version", "1")
	httpReq.Header.Set("connect-content-encoding", "gzip")
	httpReq.Header.Set("connect-accept-encoding", "gzip")
	httpReq.Header.Set("accept", "application/connect+proto")
	httpReq.Header.Set("user-agent", "devin-sub-wrapper/0.1.0")

	client := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("windsurf request: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, statusErr{code: httpResp.StatusCode, msg: fmt.Sprintf("windsurf returned %d: %s", httpResp.StatusCode, string(body))}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk)
	go e.streamResponse(ctx, httpResp, chunks, stream, req.Model)

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  chunks,
	}, nil
}

// getUserJwt obtains a JWT from the Windsurf auth service, with in-memory caching.
func (e *WindsurfExecutor) getUserJwt(ctx context.Context, apiKey, host string) (string, error) {
	host = strings.TrimSuffix(host, "/")
	e.mu.Lock()
	entry, ok := e.jwtCache[apiKey]
	if ok && entry.host == host && entry.expiresAt.After(time.Now().Add(jwtCacheMargin)) {
		jwt := entry.jwt
		e.mu.Unlock()
		return jwt, nil
	}
	e.mu.Unlock()

	metadata := newProtoBuf()
	metadata.writeStringField(1, "windsurf")
	metadata.writeStringField(2, windsurfVersion)
	metadata.writeStringField(3, apiKey)
	metadata.writeStringField(4, "en")
	metadata.writeStringField(5, "darwin") // OS
	metadata.writeStringField(7, windsurfVersion)
	metadata.writeVarintField(9, uint64(time.Now().UnixMilli()))
	metadata.writeStringField(10, uuid.New().String())
	metadata.writeStringField(12, "windsurf")
	metadata.writeMessageField(16, func(p *protoBuf) {
		p.writeVarintField(1, uint64(time.Now().Unix()))
		p.writeVarintField(2, uint64(time.Now().Nanosecond())*1_000_000)
	})
	metadata.writeStringField(25, uuid.New().String())
	metadata.writeStringField(26, "Unset")
	metadata.writeStringField(28, "windsurf")

	metadataBytes := metadata.bytes()
	var reqBuf bytes.Buffer
	reqBuf.Write(encodeVarint(uint64(1<<3 | 2)))
	reqBuf.Write(encodeVarint(uint64(len(metadataBytes))))
	reqBuf.Write(metadataBytes)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, host+windsurfGetUserJwt, &reqBuf)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("content-type", "application/proto")
	httpReq.Header.Set("connect-protocol-version", "1")
	httpReq.Header.Set("user-agent", "devin-sub-wrapper/0.1.0")

	client := helps.NewUtlsHTTPClient(ctx, e.cfg, nil, 30*time.Second)
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GetUserJwt %d: %s", resp.StatusCode, string(data))
	}

	fields, err := decodeFields(data)
	if err != nil {
		return "", err
	}
	for _, f := range fields {
		if f.Field == 1 && f.WireType == 2 {
			jwt := f.AsString()
			expiresAt := time.Now().Add(10 * time.Minute)
			if exp := jwtExpiresAt(jwt); exp > 0 {
				expiresAt = time.Unix(exp, 0)
			}
			e.mu.Lock()
			e.jwtCache[apiKey] = &windsurfJwtEntry{
				apiKey:    apiKey,
				host:      host,
				jwt:       jwt,
				expiresAt: expiresAt,
			}
			e.mu.Unlock()
			return jwt, nil
		}
	}
	return "", fmt.Errorf("GetUserJwt returned no JWT")
}

// buildGetChatMessageRequest builds the protobuf request for GetChatMessage.
func (e *WindsurfExecutor) buildGetChatMessageRequest(apiKey, userJwt, modelUid string, body openAIChatRequest) ([]byte, error) {
	sessionID := uuid.New().String()
	cascadeID := uuid.New().String()
	promptID := uuid.New().String()

	metadata := newProtoBuf()
	metadata.writeStringField(1, "windsurf")
	metadata.writeStringField(2, windsurfVersion)
	metadata.writeStringField(3, apiKey)
	metadata.writeStringField(4, "en")
	metadata.writeStringField(5, "darwin")
	metadata.writeStringField(7, windsurfVersion)
	metadata.writeVarintField(9, uint64(time.Now().UnixMilli()))
	metadata.writeStringField(10, sessionID)
	metadata.writeStringField(12, "windsurf")
	metadata.writeMessageField(16, func(p *protoBuf) {
		p.writeVarintField(1, uint64(time.Now().Unix()))
		p.writeVarintField(2, uint64(time.Now().Nanosecond())*1_000_000)
	})
	metadata.writeStringField(21, userJwt)
	metadata.writeStringField(25, uuid.New().String())
	metadata.writeStringField(26, "Unset")
	metadata.writeStringField(28, "windsurf")

	instructionPrefix := systemInstructionPrefix(body.Messages)
	promptMessages := make([]openAIMessage, 0, len(body.Messages))
	for _, m := range body.Messages {
		if m.Role != "system" && m.Role != "developer" {
			promptMessages = append(promptMessages, m)
		}
	}
	if len(promptMessages) == 0 {
		promptMessages = append(promptMessages, openAIMessage{Role: "user", Content: ""})
	}

	cfg := windsurfmodels.ParseConfig("")
	ctxWindow := modelContextWindow(reqModelFallback(body.Model, cfg.DefaultModelID))
	budget := computeCompletionBudget(ctxWindow, body.MaxTokens)

	// Limit prompt budget to the Windsurf backend cap (64K).
	if budget.PromptBudget > 64000 {
		budget.PromptBudget = 64000
	}

	// Forward tools; the backend rejects too many tool definitions, so cap the count.
	tools := body.Tools
	if len(tools) > windsurfMaxForwardedTools {
		tools = reduceToolCountForBackend(tools)
	}
	toolTokenEstimate := estimateToolDefinitionsTokens(tools)
	messageBudget := max(256, budget.PromptBudget-toolTokenEstimate)
	budgetedMessages := truncateMessagesForPromptBudget(promptMessages, messageBudget)

	p := newProtoBuf()
	p.writeBytesField(1, metadata.bytes())
	if instructionPrefix != "" {
		p.writeStringField(2, instructionPrefix)
	}
	for _, m := range budgetedMessages {
		p.writeBytesField(3, encodeChatMessagePrompt(m))
	}
	p.writeVarintField(7, 5)
	p.writeBytesField(8, encodeCompletionConfig(budget, body.Temperature, body.TopP))
	for _, t := range tools {
		p.writeBytesField(10, encodeToolDef(t))
	}
	p.writeStringField(16, cascadeID)
	p.writeStringField(21, modelUid)
	p.writeStringField(22, promptID)
	return p.bytes(), nil
}

func (e *WindsurfExecutor) streamResponse(ctx context.Context, resp *http.Response, chunks chan<- cliproxyexecutor.StreamChunk, stream bool, requestedModel string) {
	defer close(chunks)
	defer resp.Body.Close()

	if !stream {
		// Non-streaming: accumulate and emit a single OpenAI response JSON.
		var text string
		var reasoning strings.Builder
		var finish string
		var promptTokens, completionTokens int
		if err := e.readStream(resp.Body, func(event cloudChatEvent) {
			switch event.Kind {
			case "text":
				text += event.Text
			case "reasoning":
				reasoning.WriteString(event.Text)
			case "finish":
				finish = event.Reason
			case "usage":
				promptTokens = event.PromptTokens
				completionTokens = event.CompletionTokens
			}
		}); err != nil {
			chunks <- cliproxyexecutor.StreamChunk{Err: err}
			return
		}
		payload := openAICompletionResponse(requestedModel, text, finish, promptTokens, completionTokens)
		chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		return
	}

	// Streaming: emit OpenAI SSE chunks.
	id := "windsurf-" + uuid.New().String()
	if err := e.readStream(resp.Body, func(event cloudChatEvent) {
		switch event.Kind {
		case "text":
			payload := openAIStreamChunk(requestedModel, id, event.Text, false, "")
			chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		case "reasoning":
			// Map reasoning as a reasoning_content field in the delta.
			payload := openAIStreamChunkReasoning(requestedModel, id, event.Text)
			chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		case "tool_call_start":
			payload := openAIStreamChunkToolCallStart(requestedModel, id, event.ID, event.Name)
			chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		case "tool_call_args":
			payload := openAIStreamChunkToolCallArgs(requestedModel, id, event.ID, event.ArgsDelta)
			chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		case "finish":
			payload := openAIStreamChunk(requestedModel, id, "", true, event.Reason)
			chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		case "usage":
			payload := openAIStreamChunkUsage(requestedModel, id, event.PromptTokens, event.CompletionTokens)
			chunks <- cliproxyexecutor.StreamChunk{Payload: payload}
		}
	}); err != nil {
		chunks <- cliproxyexecutor.StreamChunk{Err: err}
	}
}

func (e *WindsurfExecutor) readStream(r io.Reader, onEvent func(cloudChatEvent)) error {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for len(buf) >= 5 {
				flags := buf[0]
				length := int(binaryBigEndianUint32(buf[1:5]))
				if len(buf) < 5+length {
					break
				}
				payload := buf[5 : 5+length]
				buf = buf[5+length:]
				if flags&0x02 != 0 {
					if trailer := parseTrailerError(payload); trailer != "" {
						return fmt.Errorf("windsurf trailer: %s", trailer)
					}
					continue
				}
				if flags&0x01 != 0 {
					decompressed, err := decompressGzip(payload)
					if err != nil {
						return err
					}
					payload = decompressed
				}
				events, err := decodeChatEvents(payload)
				if err != nil {
					return err
				}
				for _, ev := range events {
					onEvent(ev)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// decodeChatEvents decodes a protobuf payload into a slice of CloudChatEvents.
func decodeChatEvents(payload []byte) ([]cloudChatEvent, error) {
	fields, err := decodeFields(payload)
	if err != nil {
		return nil, err
	}
	var events []cloudChatEvent
	for _, f := range fields {
		switch f.Field {
		case 3:
			if f.WireType == 2 {
				if t := f.AsString(); t != "" {
					events = append(events, cloudChatEvent{Kind: "text", Text: t})
				}
			}
		case 9:
			if f.WireType == 2 {
				if t := f.AsString(); t != "" {
					events = append(events, cloudChatEvent{Kind: "reasoning", Text: t})
				}
			}
		case 6:
			if f.WireType == 2 {
				if ev := decodeToolCallDelta(f.Data); ev != nil {
					events = append(events, *ev)
				}
			}
		case 5:
			if f.WireType == 0 {
				events = append(events, cloudChatEvent{Kind: "finish", Reason: stopReason(int(f.Value))})
			}
		case 28:
			if f.WireType == 2 {
				if ev := decodeUsage(f.Data); ev != nil {
					events = append(events, *ev)
				}
			}
		}
	}
	return events, nil
}

func decodeToolCallDelta(data []byte) *cloudChatEvent {
	fields, err := decodeFields(data)
	if err != nil {
		return nil
	}
	var id, name, args string
	for _, f := range fields {
		if f.WireType != 2 {
			continue
		}
		switch f.Field {
		case 1:
			id = f.AsString()
		case 2:
			name = f.AsString()
		case 3:
			args = f.AsString()
		}
	}
	if id != "" && name != "" {
		return &cloudChatEvent{Kind: "tool_call_start", ID: id, Name: name}
	}
	if args != "" {
		return &cloudChatEvent{Kind: "tool_call_args", ID: id, ArgsDelta: args}
	}
	return nil
}

func decodeUsage(data []byte) *cloudChatEvent {
	fields, err := decodeFields(data)
	if err != nil {
		return nil
	}
	var prompt, completion int
	for _, f := range fields {
		if f.Field != 2 || f.WireType != 2 {
			continue
		}
		var metric string
		var value float32
		for _, sub := range f.Data {
			_ = sub
		}
		subFields, err := decodeFields(f.Data)
		if err != nil {
			continue
		}
		for _, sf := range subFields {
			if sf.Field == 5 && sf.WireType == 2 {
				metric = sf.AsString()
			}
			if sf.Field == 4 && sf.WireType == 2 {
				for _, vf := range sf.Data {
					_ = vf
				}
				valueFields, err := decodeFields(sf.Data)
				if err != nil {
					continue
				}
				for _, vf := range valueFields {
					if vf.Field == 2 && vf.WireType == 5 {
						value = vf.AsFloat32()
					}
				}
			}
		}
		if metric == "input_tokens" {
			prompt = int(value + 0.5)
		}
		if metric == "output_tokens" {
			completion = int(value + 0.5)
		}
	}
	if prompt == 0 && completion == 0 {
		return nil
	}
	return &cloudChatEvent{Kind: "usage", PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

func stopReason(value int) string {
	switch value {
	case 10:
		return "tool_calls"
	case 11:
		return "content_filter"
	case 1, 3:
		return "length"
	case 13:
		return "stop"
	default:
		return "stop"
	}
}

func parseTrailerError(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if text == "" || text == "{}" {
		return ""
	}
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &parsed); err == nil && parsed.Error.Message != "" {
		return fmt.Sprintf("%s: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if strings.Contains(text, "error") {
		return text
	}
	return ""
}

func windsurfCreds(auth *cliproxyauth.Auth) (apiKey, baseURL string) {
	if auth == nil {
		return "", windsurfBaseURL
	}
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		if u := strings.TrimSpace(auth.Attributes["base_url"]); u != "" {
			baseURL = u
		}
	}
	if apiKey == "" && auth.Metadata != nil {
		if s, ok := auth.Metadata["api_key"].(string); ok {
			apiKey = strings.TrimSpace(s)
		}
	}
	if baseURL == "" {
		baseURL = windsurfBaseURL
	}
	return apiKey, baseURL
}

// openAIChatRequest mirrors the OpenAI chat completion request body.
type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	Temperature float64         `json:"temperature"`
	TopP        float64         `json:"top_p"`
	MaxTokens   int             `json:"max_tokens"`
	Tools       []openAITool    `json:"tools"`
	ToolChoice  string          `json:"tool_choice"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCallID string           `json:"tool_call_id"`
	ToolCalls  []openAIToolCall `json:"tool_calls"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func systemInstructionPrefix(messages []openAIMessage) string {
	var parts []string
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			text := nativePromptText(m.Content)
			if text == "" {
				continue
			}
			parts = append(parts, m.Role+":\n"+text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return strings.TrimPrefix(parts[0], "system:\n")
		return strings.TrimPrefix(parts[0], "developer:\n")
	}
	return strings.Join(parts, "\n\n")
}

func encodeChatMessagePrompt(m openAIMessage) []byte {
	p := newProtoBuf()
	p.writeVarintField(2, uint64(promptSource(m.Role)))
	text := nativePromptText(m.Content)
	p.writeStringField(3, text)
	p.writeVarintField(4, uint64(mathMax(1, len(text)/4)))
	p.writeVarintField(5, 1)
	if images := nativeImageParts(m.Content); len(images) > 0 {
		for _, img := range images {
			p.writeMessageField(10, func(np *protoBuf) {
				np.writeStringField(1, img.Base64)
				np.writeStringField(2, img.MIMEType)
			})
		}
	}
	if m.Role == "tool" && m.ToolCallID != "" {
		p.writeStringField(7, m.ToolCallID)
	}
	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			p.writeMessageField(6, func(np *protoBuf) {
				np.writeStringField(1, tc.ID)
				np.writeStringField(2, tc.Function.Name)
				np.writeStringField(3, tc.Function.Arguments)
			})
		}
	}
	return p.bytes()
}

func encodeCompletionConfig(budget completionBudget, temperature, topP float64) []byte {
	p := newProtoBuf()
	p.writeVarintField(1, 1)
	p.writeVarintField(2, uint64(budget.PromptBudget))
	p.writeVarintField(3, uint64(budget.MaxTokens))
	p.writeFixed64Field(5, defaultFloat(temperature, 0.7))
	p.writeFixed64Field(6, defaultFloat(topP, 0.95))
	p.writeVarintField(7, 50)
	p.writeFixed64Field(8, 1.0)
	p.writeFixed64Field(11, 1.0)
	return p.bytes()
}

func encodeToolDef(tool openAITool) []byte {
	p := newProtoBuf()
	p.writeStringField(1, tool.Function.Name)
	p.writeStringField(2, sanitizeBackendText(tool.Function.Description))
	params := sanitizeToolParameters(tool.Function.Parameters)
	b, _ := json.Marshal(params)
	p.writeStringField(3, string(b))
	return p.bytes()
}

func promptSource(role string) int {
	switch role {
	case "assistant":
		return 2
	case "system", "developer":
		return 3
	case "tool":
		return 4
	default:
		return 1
	}
}

func nativePromptText(content string) string {
	return content
}

func nativeImageParts(content string) []nativeImagePart {
	return nil
}

type nativeImagePart struct {
	Base64   string
	MIMEType string
}

func sanitizeBackendText(value string) string {
	return strings.NewReplacer(
		"model context protocol", "client-side server protocol",
		"model Context protocol", "client-side server protocol",
		"MCP", "client",
		"mcp", "client",
	).Replace(value)
}

func sanitizeToolParameters(value map[string]interface{}) map[string]interface{} {
	if value == nil {
		return nil
	}
	out := make(map[string]interface{}, len(value))
	for k, v := range value {
		if k == "strict" {
			continue
		}
		out[k] = sanitizeToolValue(v)
	}
	return out
}

func sanitizeToolValue(v interface{}) interface{} {
	switch val := v.(type) {
	case []any:
		for i := range val {
			val[i] = sanitizeToolValue(val[i])
		}
		return val
	case string:
		return sanitizeBackendText(val)
	case map[string]any:
		return sanitizeToolParameters(val)
	default:
		return v
	}
}

func computeCompletionBudget(contextWindow, requestedMaxTokens int) completionBudget {
	const backendPromptBudgetCap = 64000
	if contextWindow <= 0 {
		return completionBudget{PromptBudget: backendPromptBudgetCap, MaxTokens: defaultInt(requestedMaxTokens, 128000)}
	}
	var maxOutputShare int
	if contextWindow <= 20000 {
		maxOutputShare = max(512, min(4096, contextWindow/4))
	} else {
		maxOutputShare = max(4096, min(128000, contextWindow/4))
	}
	defaultMaxTokens := min(maxOutputShare, 4096)
	requested := defaultInt(requestedMaxTokens, defaultMaxTokens)
	maxTokens := max(1, min(min(requested, maxOutputShare), contextWindow-1))
	promptBudget := max(1, min(backendPromptBudgetCap, contextWindow-maxTokens))
	return completionBudget{PromptBudget: promptBudget, MaxTokens: maxTokens}
}

type completionBudget struct {
	PromptBudget int
	MaxTokens    int
}

func modelContextWindow(modelID string) int {
	switch {
	case strings.Contains(modelID, "glm-5-2"):
		return 1000000
	case strings.Contains(modelID, "glm-5-1"):
		return 128000
	case strings.Contains(modelID, "gpt-5-5"):
		return 1000000
	case strings.Contains(modelID, "gpt-5-4"):
		return 1000000
	case strings.Contains(modelID, "gpt-5-4-mini"):
		return 128000
	case strings.Contains(modelID, "gpt-5-3-codex"):
		return 128000
	case strings.Contains(modelID, "gpt-5-2"):
		return 128000
	case strings.Contains(modelID, "claude-opus-4-8"):
		return 1000000
	case strings.Contains(modelID, "claude-5-fable"):
		return 1000000
	case strings.Contains(modelID, "claude-sonnet-5"):
		return 1000000
	case strings.Contains(modelID, "claude-opus-4-7"):
		return 1000000
	case strings.Contains(modelID, "claude-opus-4-6"):
		return 1000000
	case strings.Contains(modelID, "claude-opus-4-5"):
		return 1000000
	case strings.Contains(modelID, "claude-sonnet-4-6"):
		return 200000
	case strings.Contains(modelID, "claude-sonnet-4-5"):
		return 200000
	case strings.Contains(modelID, "claude-haiku-4-5"):
		return 200000
	case strings.Contains(modelID, "gemini-3-5-flash"):
		return 128000
	case strings.Contains(modelID, "gemini-3.1-pro"):
		return 200000
	case strings.Contains(modelID, "gemini-3.0-flash"):
		return 128000
	case strings.Contains(modelID, "swe-1-6"):
		return 128000
	case strings.Contains(modelID, "swe-1-7"):
		return 128000
	case strings.Contains(modelID, "kimi"):
		return 256000
	case strings.Contains(modelID, "deepseek"):
		return 128000
	default:
		return 128000
	}
}

func reqModelFallback(model, fallback string) string {
	if strings.TrimSpace(model) != "" {
		return model
	}
	return fallback
}

func openAICompletionResponse(model, text, finishReason string, promptTokens, completionTokens int) []byte {
	finish := finishReason
	if finish == "" {
		finish = "stop"
	}
	resp := map[string]any{
		"id":      "windsurf-" + uuid.New().String(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": finish,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func openAIStreamChunk(model, id, delta string, done bool, finishReason string) []byte {
	choice := map[string]any{
		"index": 0,
		"delta": map[string]any{
			"role":    "assistant",
			"content": delta,
		},
	}
	if done {
		choice["finish_reason"] = defaultString(finishReason, "stop")
		choice["delta"] = map[string]any{}
	}
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{choice},
	}
	b, _ := json.Marshal(resp)
	return b
}

func openAIStreamChunkReasoning(model, id, reasoning string) []byte {
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role":              "assistant",
					"reasoning_content": reasoning,
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func openAIStreamChunkToolCallStart(model, id, toolID, name string) []byte {
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{
							"index": 0,
							"id":    toolID,
							"type":  "function",
							"function": map[string]any{
								"name":      name,
								"arguments": "",
							},
						},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func openAIStreamChunkToolCallArgs(model, id, toolID, argsDelta string) []byte {
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{
						{
							"index":    0,
							"function": map[string]any{"arguments": argsDelta},
						},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func openAIStreamChunkUsage(model, id string, prompt, completion int) []byte {
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"total_tokens":      prompt + completion,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func estimateMessagesTokens(messages []openAIMessage) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) / 4
		if m.Role == "system" || m.Role == "developer" {
			total += 2
		}
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Name) / 4
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

func jwtExpiresAt(jwt string) int64 {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return 0
	}
	payload := parts[1]
	padding := (4 - len(payload)%4) % 4
	payload += strings.Repeat("=", padding)
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return 0
	}
	var parsed struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return 0
	}
	return parsed.Exp
}

func binaryBigEndianUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func decompressGzip(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
}

func defaultInt(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

func defaultFloat(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}

func defaultString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func mathMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func min3(a, b, c int) int {
	return min(min(a, b), c)
}

// reduceToolCountForBackend keeps the first N essential tools. The backend
// rejects requests with too many tool definitions, so we cap at the known limit.
func reduceToolCountForBackend(tools []openAITool) []openAITool {
	if len(tools) <= windsurfMaxForwardedTools {
		return tools
	}
	// Prefer core tools (common agent primitives) first, then keep the rest.
	priorityPrefixes := []string{"exec", "write", "read", "search", "bash", "shell", "file", "codex_apps__"}
	scored := make([]struct {
		score int
		tool  openAITool
	}, 0, len(tools))
	for _, t := range tools {
		score := 0
		name := strings.ToLower(t.Function.Name)
		for i, prefix := range priorityPrefixes {
			if strings.HasPrefix(name, prefix) {
				score = len(priorityPrefixes) - i
				break
			}
		}
		scored = append(scored, struct {
			score int
			tool  openAITool
		}{score, t})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return i < j
	})
	reduced := make([]openAITool, 0, windsurfMaxForwardedTools)
	for i := 0; i < windsurfMaxForwardedTools && i < len(scored); i++ {
		reduced = append(reduced, scored[i].tool)
	}
	return reduced
}

func estimateToolDefinitionsTokens(tools []openAITool) int {
	if len(tools) == 0 {
		return 0
	}
	totalChars := 0
	for _, t := range tools {
		totalChars += len(t.Function.Name)
		totalChars += len(t.Function.Description)
		totalChars += len(jsonString(t.Function.Parameters))
	}
	return max(1, totalChars/4)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func truncateMessagesForPromptBudget(messages []openAIMessage, promptBudget int) []openAIMessage {
	targetTokens := max(256, promptBudget-512)
	if estimateMessagesTokens(messages) <= targetTokens {
		return messages
	}
	// Keep the first user message (anchor) and as many recent messages as fit.
	var anchor []openAIMessage
	anchorTokens := 0
	for i, m := range messages {
		tokens := estimateMessageTokens(m)
		if anchorTokens+tokens <= targetTokens/4 {
			anchor = append(anchor, m)
			anchorTokens += tokens
			if m.Role == "user" {
				break
			}
		} else {
			break
		}
		_ = i
	}
	selected := make([]openAIMessage, 0, len(messages))
	usedTokens := anchorTokens
	for i := len(messages) - 1; i >= 0; i-- {
		tokens := estimateMessageTokens(messages[i])
		if usedTokens+tokens <= targetTokens {
			selected = append([]openAIMessage{messages[i]}, selected...)
			usedTokens += tokens
		} else {
			break
		}
	}
	return append(anchor, selected...)
}

func estimateMessageTokens(m openAIMessage) int {
	contentTokens := len(nativePromptText(m.Content)) / 4
	toolCallTokens := 0
	for _, tc := range m.ToolCalls {
		toolCallTokens += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return 8 + contentTokens + toolCallTokens
}

// cloudChatEvent is a decoded Windsurf backend event.
type cloudChatEvent struct {
	Kind             string
	Text             string
	ID               string
	Name             string
	ArgsDelta        string
	Reason           string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

var _ = min3 // avoid unused warning if unused
var _ = sdktranslator.FormatOpenAI
var _ = log.Fields{}
