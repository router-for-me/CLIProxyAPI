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
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type CursorExecutor struct {
	cfg *config.Config
}

func NewCursorExecutor(cfg *config.Config) *CursorExecutor { return &CursorExecutor{cfg: cfg} }

func (e *CursorExecutor) Identifier() string { return "cursor" }

func (e *CursorExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

type cursorCompletionResult struct {
	resultText string
	translated []byte
	usage      usage.Detail
}

type cursorStreamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Message   *cursorMessage  `json:"message,omitempty"`
	ToolCall  json.RawMessage `json:"tool_call,omitempty"`
	Result    string          `json:"result,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type cursorMessage struct {
	Role    string              `json:"role"`
	Content []cursorContentPart `json:"content"`
}

type cursorContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cursorStreamState struct {
	id          string
	created     int64
	model       string
	toolCallIdx int
	toolCallIDs map[string]int
	sentRole    bool
}

type cursorStreamReader struct {
	io.Reader
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (r *cursorStreamReader) Close() error {
	r.cancel()
	_ = r.cmd.Wait()
	return nil
}

func (e *CursorExecutor) getCursorAgentCompletion(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cursorCompletionResult, error) {
	apiKey := cursorAPIKey(auth, e.cfg)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing Cursor API key (set CURSOR_API_KEY or provide api_key in auth)"}
	}

	binary, err := cursorAgentBinary(e.cfg)
	if err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: err.Error()}
	}

	model := req.Model
	if override := e.resolveUpstreamModel(req.Model, auth); override != "" {
		model = override
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, model, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, model, bytes.Clone(req.Payload), true)
	translated = applyPayloadConfigWithRoot(e.cfg, model, "cursor", "", translated, originalTranslated)

	prompt := cursorPromptFromOpenAI(translated)
	if strings.TrimSpace(prompt) == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "cursor executor: empty prompt"}
	}

	upstreamModel := cursorUpstreamModel(req.Model)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	reqHeaders := make(http.Header)
	reqHeaders.Set("X-Cursor-Model", upstreamModel)
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       binary,
		Method:    "EXEC",
		Headers:   reqHeaders,
		Body:      []byte(prompt),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	resultText, err := e.runWithRetryAndFallback(ctx, binary, upstreamModel, prompt, apiKey)
	if err != nil {
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, http.StatusOK, http.Header{"X-Cursor-Model": []string{upstreamModel}})
	_, usageDetail := buildOpenAIChatCompletionResponse(req.Model, resultText, translated, cursorTokenizerModel(req.Model))

	return &cursorCompletionResult{
		resultText: resultText,
		translated: translated,
		usage:      usageDetail,
	}, nil
}

func (e *CursorExecutor) runWithRetryAndFallback(ctx context.Context, binary, upstreamModel, prompt, apiKey string) (string, error) {
	stdout, stderr, runErr := runCursorAgent(ctx, binary, upstreamModel, prompt, apiKey)

	if runErr != nil {
		msg := extractErrorMessage(stdout, stderr, runErr)
		msgLower := strings.ToLower(msg)

		if upstreamModel == "composer-1" && isTransientErrorMessage(msgLower) {
			fallback := cursorComposerFallbackModel()
			stdout, stderr, runErr = runCursorAgent(ctx, binary, fallback, prompt, apiKey)
			if runErr == nil {
				appendAPIResponseChunk(ctx, e.cfg, stdout)
				if resultText, errParse := parseCursorAgentOutput(stdout); errParse == nil {
					return resultText, nil
				}
			}
		}

		recordAPIResponseError(ctx, e.cfg, fmt.Errorf("cursor-agent failed: %w", runErr))
		appendAPIResponseChunk(ctx, e.cfg, stderr)
		appendAPIResponseChunk(ctx, e.cfg, stdout)
		return "", cursorStatusError(msg)
	}

	appendAPIResponseChunk(ctx, e.cfg, stdout)
	resultText, errParse := parseCursorAgentOutput(stdout)

	if errParse != nil {
		resultText, errParse = e.retryOnTransientError(ctx, binary, upstreamModel, prompt, apiKey, errParse)
	}

	if errParse != nil {
		if upstreamModel == "composer-1" && isTransientCursorAgentError(errParse) {
			fallback := cursorComposerFallbackModel()
			stdout, _, runErr = runCursorAgent(ctx, binary, fallback, prompt, apiKey)
			if runErr == nil {
				appendAPIResponseChunk(ctx, e.cfg, stdout)
				resultText, errParse = parseCursorAgentOutput(stdout)
			}
		}
		if errParse != nil {
			return "", errParse
		}
	}

	return resultText, nil
}

func (e *CursorExecutor) retryOnTransientError(ctx context.Context, binary, upstreamModel, prompt, apiKey string, initialErr error) (string, error) {
	if !isTransientCursorAgentError(initialErr) {
		return "", initialErr
	}

	for attempt := 0; attempt < 2; attempt++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 400 * time.Millisecond):
		}

		stdout, stderr, runErr := runCursorAgent(ctx, binary, upstreamModel, prompt, apiKey)
		if runErr != nil {
			msg := extractErrorMessage(stdout, stderr, runErr)
			recordAPIResponseError(ctx, e.cfg, fmt.Errorf("cursor-agent failed: %w", runErr))
			appendAPIResponseChunk(ctx, e.cfg, stderr)
			appendAPIResponseChunk(ctx, e.cfg, stdout)
			return "", cursorStatusError(msg)
		}

		appendAPIResponseChunk(ctx, e.cfg, stdout)
		resultText, errParse := parseCursorAgentOutput(stdout)
		if errParse == nil {
			return resultText, nil
		}
		if !isTransientCursorAgentError(errParse) {
			return "", errParse
		}
	}

	return "", initialErr
}

func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	result, err := e.getCursorAgentCompletion(ctx, auth, req, opts)
	if err != nil {
		return resp, err
	}

	reporter.publish(ctx, result.usage)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	openAIResp, _ := buildOpenAIChatCompletionResponse(req.Model, result.resultText, result.translated, cursorTokenizerModel(req.Model))

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), result.translated, openAIResp, &param)
	return cliproxyexecutor.Response{Payload: []byte(out)}, nil
}

func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	apiKey := cursorAPIKey(auth, e.cfg)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing Cursor API key (set CURSOR_API_KEY or provide api_key in auth)"}
	}

	binary, err := cursorAgentBinary(e.cfg)
	if err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: err.Error()}
	}

	model := req.Model
	if override := e.resolveUpstreamModel(req.Model, auth); override != "" {
		model = override
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, model, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, model, bytes.Clone(req.Payload), true)
	translated = applyPayloadConfigWithRoot(e.cfg, model, "cursor", "", translated, originalTranslated)

	prompt := cursorPromptFromOpenAI(translated)
	if strings.TrimSpace(prompt) == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "cursor executor: empty prompt"}
	}

	upstreamModel := cursorUpstreamModel(model)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	reqHeaders := make(http.Header)
	reqHeaders.Set("X-Cursor-Model", upstreamModel)
	reqHeaders.Set("X-Cursor-Stream", "true")
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       binary,
		Method:    "EXEC",
		Headers:   reqHeaders,
		Body:      []byte(prompt),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	stdout, _, err := runCursorAgentStream(ctx, binary, upstreamModel, prompt, apiKey)
	if err != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: err.Error()}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go e.streamCursorEvents(ctx, stdout, out, req, opts, translated, reporter)

	return out, nil
}

func runCursorAgentStream(ctx context.Context, binary, model, prompt, apiKey string) (io.ReadCloser, context.CancelFunc, error) {
	args := []string{
		"--output-format", "stream-json",
		"--stream-partial-output",
		"--model", cursorAgentModelName(model),
		"-p", prompt,
	}

	cmdCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cmdCtx, binary, args...)

	env := os.Environ()
	if apiKey != "" {
		env = append(env, "CURSOR_API_KEY="+apiKey)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, err
	}

	return &cursorStreamReader{
		Reader: stdout,
		cmd:    cmd,
		cancel: cancel,
	}, cancel, nil
}

func (e *CursorExecutor) streamCursorEvents(
	ctx context.Context,
	stdout io.ReadCloser,
	out chan<- cliproxyexecutor.StreamChunk,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	translated []byte,
	reporter *usageReporter,
) {
	defer close(out)
	defer stdout.Close()

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	state := &cursorStreamState{
		id:          "chatcmpl-" + uuid.NewString(),
		created:     time.Now().Unix(),
		model:       req.Model,
		toolCallIDs: make(map[string]int),
	}

	var param any

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(nil, 10_485_760)

	for scanner.Scan() {
		line := scanner.Bytes()
		appendAPIResponseChunk(ctx, e.cfg, line)

		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var event cursorStreamEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		chunks := e.translateCursorEvent(state, &event, req.Model)
		for _, chunk := range chunks {
			sseChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, chunk, &param)
			for _, sseChunk := range sseChunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: []byte(sseChunk)}:
				case <-ctx.Done():
					return
				}
			}
		}

		if event.Type == "result" {
			if event.Subtype == "success" {
				_, usageDetail := buildOpenAIChatCompletionResponse(req.Model, event.Result, translated, cursorTokenizerModel(req.Model))
				reporter.publish(ctx, usageDetail)
			}
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Warnf("cursor stream: scanner error: %v", err)
		recordAPIResponseError(ctx, e.cfg, err)
		return
	}

	finishChunk := makeStreamChunk(state.id, state.created, state.model, map[string]any{}, "stop")
	finishSSE := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, finishChunk, &param)
	for _, chunk := range finishSSE {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk)}:
		case <-ctx.Done():
			return
		}
	}

	doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, []byte("data: [DONE]"), &param)
	for _, chunk := range doneChunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunk)}:
		case <-ctx.Done():
			return
		}
	}
}

func (e *CursorExecutor) translateCursorEvent(state *cursorStreamState, event *cursorStreamEvent, model string) [][]byte {
	var chunks [][]byte

	switch event.Type {
	case "assistant":
		if !state.sentRole {
			roleChunk := makeStreamChunk(state.id, state.created, model, map[string]any{"role": "assistant"}, "")
			chunks = append(chunks, roleChunk)
			state.sentRole = true
		}
		if event.Message != nil {
			for _, part := range event.Message.Content {
				if part.Type == "text" && part.Text != "" {
					chunk := makeStreamChunk(state.id, state.created, model, map[string]any{"content": part.Text}, "")
					chunks = append(chunks, chunk)
				}
			}
		}

	case "tool_call":
		if event.Subtype == "started" {
			toolName, toolArgs := parseCursorToolCall(event.ToolCall)
			if toolName == "" {
				return nil
			}

			callID := sanitizeToolCallID(event.CallID)
			idx := state.toolCallIdx
			state.toolCallIDs[callID] = idx
			state.toolCallIdx++

			if !state.sentRole {
				roleChunk := makeStreamChunk(state.id, state.created, model, map[string]any{"role": "assistant"}, "")
				chunks = append(chunks, roleChunk)
				state.sentRole = true
			}

			toolCallDelta := map[string]any{
				"tool_calls": []any{
					map[string]any{
						"index": idx,
						"id":    callID,
						"type":  "function",
						"function": map[string]any{
							"name":      toolName,
							"arguments": toolArgs,
						},
					},
				},
			}
			chunk := makeStreamChunk(state.id, state.created, model, toolCallDelta, "")
			chunks = append(chunks, chunk)
		}
	}

	return chunks
}

func makeStreamChunk(id string, created int64, model string, delta map[string]any, finishReason string) []byte {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{choice},
	}
	data, _ := json.Marshal(chunk)
	return append([]byte("data: "), data...)
}

func parseCursorToolCall(raw json.RawMessage) (name string, args string) {
	if len(raw) == 0 {
		return "", ""
	}
	parsed := gjson.ParseBytes(raw)

	if r := parsed.Get("readToolCall"); r.Exists() {
		argsJSON, _ := json.Marshal(r.Get("args").Value())
		return "read_file", string(argsJSON)
	}
	if w := parsed.Get("writeToolCall"); w.Exists() {
		argsJSON, _ := json.Marshal(w.Get("args").Value())
		return "write_file", string(argsJSON)
	}
	if b := parsed.Get("bashToolCall"); b.Exists() {
		argsJSON, _ := json.Marshal(b.Get("args").Value())
		return "bash", string(argsJSON)
	}
	if g := parsed.Get("grepToolCall"); g.Exists() {
		argsJSON, _ := json.Marshal(g.Get("args").Value())
		return "grep", string(argsJSON)
	}
	if l := parsed.Get("listToolCall"); l.Exists() {
		argsJSON, _ := json.Marshal(l.Get("args").Value())
		return "list_directory", string(argsJSON)
	}
	if f := parsed.Get("function"); f.Exists() {
		return f.Get("name").String(), f.Get("arguments").String()
	}

	return "", ""
}

func sanitizeToolCallID(callID string) string {
	return strings.ReplaceAll(callID, "\n", "_")
}

func (e *CursorExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	modelForCounting := cursorTokenizerModel(req.Model)
	enc, err := tokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cursor executor: tokenizer init failed: %w", err)
	}
	count, err := countOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cursor executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: []byte(translatedUsage)}, nil
}

func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debug("cursor executor: refresh called")
	_ = ctx
	return auth, nil
}

func (e *CursorExecutor) resolveUpstreamModel(alias string, auth *cliproxyauth.Auth) string {
	trimmed := strings.TrimSpace(alias)
	if trimmed == "" {
		return ""
	}

	if e.cfg != nil {
		for _, ck := range e.cfg.CursorKey {
			for _, m := range ck.Models {
				if strings.EqualFold(m.Alias, trimmed) {
					return m.Name
				}
			}
		}
	}

	if auth != nil && auth.Metadata != nil {
		if overrides, ok := auth.Metadata["model_overrides"].(map[string]any); ok {
			if override, found := overrides[trimmed]; found {
				if overrideStr, isStr := override.(string); isStr && strings.TrimSpace(overrideStr) != "" {
					return strings.TrimSpace(overrideStr)
				}
			}
		}

		if upstream, ok := auth.Metadata["upstream_model"].(string); ok {
			if u := strings.TrimSpace(upstream); u != "" {
				return u
			}
		}
	}

	return ""
}

func extractErrorMessage(stdout, stderr []byte, runErr error) string {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(stdout))
	}
	if msg == "" {
		msg = runErr.Error()
	}
	return msg
}

func isTransientErrorMessage(msgLower string) bool {
	return strings.Contains(msgLower, "connecterror") || strings.Contains(msgLower, "auth_unavailable")
}

func cursorStatusError(msg string) error {
	status := http.StatusBadGateway
	msgLower := strings.ToLower(msg)
	if strings.Contains(msgLower, "unauthorized") || strings.Contains(msg, "CURSOR_API_KEY") {
		status = http.StatusUnauthorized
	}
	return statusErr{code: status, msg: msg}
}

func cursorAPIKey(auth *cliproxyauth.Auth, cfg *config.Config) string {
	if auth != nil {
		if auth.Attributes != nil {
			if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
				return v
			}
		}
		if auth.Metadata != nil {
			if v, ok := auth.Metadata["api_key"].(string); ok {
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					return trimmed
				}
			}
			if v, ok := auth.Metadata["cursor_api_key"].(string); ok {
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	if cfg != nil {
		for _, ck := range cfg.CursorKey {
			if v := strings.TrimSpace(ck.APIKey); v != "" {
				return v
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_API_KEY")); v != "" {
		return v
	}
	return ""
}

func cursorTokenizerModel(alias string) string {
	model := cursorUpstreamModel(alias)
	if model == "auto" {
		return "gpt-5"
	}
	return model
}

func cursorComposerFallbackModel() string {
	if v := strings.TrimSpace(os.Getenv("CURSOR_COMPOSER_FALLBACK_MODEL")); v != "" {
		return v
	}
	return "composer-auto"
}

func cursorAgentModelName(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "auto"
	}
	if strings.EqualFold(trimmed, "composer-auto") {
		return "auto"
	}
	return trimmed
}

func isTransientCursorAgentError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connecterror") || strings.Contains(msg, "auth_unavailable")
}

func cursorUpstreamModel(alias string) string {
	trimmed := strings.TrimSpace(alias)
	if trimmed == "" {
		return "auto"
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "cursor-") {
		trimmed = trimmed[len("cursor-"):]
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			switch strings.ToLower(trimmed) {
			case "claude-opus-4.5":
				return "opus-4.5"
			case "claude-sonnet-4.5":
				return "sonnet-4.5"
			case "grok-code":
				return "grok"
			default:
				return trimmed
			}
		}
	}
	return trimmed
}

func cursorAgentBinary(cfg *config.Config) (string, error) {
	if cfg != nil {
		for _, ck := range cfg.CursorKey {
			if v := strings.TrimSpace(ck.AgentPath); v != "" {
				if info, err := os.Stat(v); err == nil && !info.IsDir() {
					return v, nil
				}
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("CURSOR_AGENT_PATH")); v != "" {
		if info, err := os.Stat(v); err == nil && !info.IsDir() {
			return v, nil
		}
		return "", fmt.Errorf("cursor executor: CURSOR_AGENT_PATH does not exist: %s", v)
	}
	if path, err := exec.LookPath("cursor-agent"); err == nil {
		return path, nil
	}
	candidates := []string{
		"/opt/homebrew/bin/cursor-agent",
		"/usr/local/bin/cursor-agent",
		"/usr/bin/cursor-agent",
		"/Applications/Cursor.app/Contents/Resources/app/bin/cursor-agent",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local/bin/cursor-agent"),
			filepath.Join(home, "bin/cursor-agent"),
			filepath.Join(home, "go/bin/cursor-agent"),
		)
	}
	for _, cand := range candidates {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("cursor executor: cursor-agent not found (install Cursor Agent CLI or set CURSOR_AGENT_PATH)")
}

func runCursorAgent(ctx context.Context, binary, model, prompt, apiKey string) (stdout []byte, stderr []byte, err error) {
	args := []string{"--output-format", "json", "--model", cursorAgentModelName(model), "-p", prompt}
	env := os.Environ()
	if apiKey != "" {
		env = append(env, "CURSOR_API_KEY="+apiKey)
	}

	for attempt := 0; attempt < 3; attempt++ {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = env
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		runErr := cmd.Run()
		if runErr == nil {
			return outBuf.Bytes(), errBuf.Bytes(), nil
		}

		combinedLower := strings.ToLower(outBuf.String() + "\n" + errBuf.String())
		transient := strings.Contains(combinedLower, "connecterror") ||
			strings.Contains(combinedLower, "auth_unavailable")
		if transient && attempt < 2 {
			select {
			case <-ctx.Done():
				return outBuf.Bytes(), errBuf.Bytes(), ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 400 * time.Millisecond):
				continue
			}
		}

		return outBuf.Bytes(), errBuf.Bytes(), runErr
	}

	return nil, nil, fmt.Errorf("cursor executor: unreachable")
}

type cursorAgentJSON struct {
	Type    string `json:"type"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

func parseCursorAgentOutput(stdout []byte) (string, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return "", statusErr{code: http.StatusBadGateway, msg: "cursor executor: empty cursor-agent output"}
	}

	var candidate []byte
	if json.Valid(trimmed) {
		candidate = trimmed
	} else {
		lines := bytes.Split(trimmed, []byte("\n"))
		for i := len(lines) - 1; i >= 0; i-- {
			line := bytes.TrimSpace(lines[i])
			if len(line) == 0 {
				continue
			}
			if json.Valid(line) {
				candidate = line
				break
			}
		}
	}

	if len(candidate) == 0 {
		return string(trimmed), nil
	}

	var parsed cursorAgentJSON
	if err := json.Unmarshal(candidate, &parsed); err != nil {
		return "", statusErr{code: http.StatusBadGateway, msg: "cursor executor: failed to parse cursor-agent JSON output"}
	}
	if parsed.IsError || strings.EqualFold(strings.TrimSpace(parsed.Type), "error") {
		msg := strings.TrimSpace(parsed.Error)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Message)
		}
		if msg == "" {
			msg = "cursor executor: cursor-agent returned error"
		}
		status := http.StatusBadGateway
		if strings.Contains(strings.ToLower(msg), "unauthorized") || strings.Contains(msg, "CURSOR_API_KEY") {
			status = http.StatusUnauthorized
		}
		return "", statusErr{code: status, msg: msg}
	}
	if strings.TrimSpace(parsed.Result) != "" {
		return parsed.Result, nil
	}
	return string(trimmed), nil
}

func cursorPromptFromOpenAI(payload []byte) string {
	root := gjson.ParseBytes(payload)
	lines := make([]string, 0, 32)

	if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := strings.TrimSpace(msg.Get("role").String())
			if role == "" {
				role = "user"
			}
			segments := make([]string, 0, 8)
			collectOpenAIContent(msg.Get("content"), &segments)

			if role == "tool" {
				toolCallID := msg.Get("tool_call_id").String()
				if toolCallID != "" {
					segments = append([]string{fmt.Sprintf("[Tool Result for %s]", toolCallID)}, segments...)
				}
			}

			if tc := msg.Get("tool_calls"); tc.Exists() && tc.IsArray() {
				tc.ForEach(func(_, call gjson.Result) bool {
					callID := call.Get("id").String()
					fnName := call.Get("function.name").String()
					fnArgs := call.Get("function.arguments").String()
					segments = append(segments, fmt.Sprintf("[Tool Call: %s (id=%s)]\nArguments: %s", fnName, callID, fnArgs))
					return true
				})
			}

			content := strings.TrimSpace(strings.Join(segments, "\n"))
			if content == "" {
				return true
			}
			lines = append(lines, fmt.Sprintf("%s:\n%s", strings.ToUpper(role), content))
			return true
		})
	}

	if len(lines) == 0 {
		if v := strings.TrimSpace(root.Get("input").String()); v != "" {
			return v
		}
		if v := strings.TrimSpace(root.Get("prompt").String()); v != "" {
			return v
		}
	}

	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func buildOpenAIChatCompletionResponse(model, content string, requestPayload []byte, tokenizerModel string) ([]byte, usage.Detail) {
	respID := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()

	promptTokens := int64(0)
	completionTokens := int64(0)
	if enc, err := tokenizerForModel(tokenizerModel); err == nil {
		if count, errCount := countOpenAIChatTokens(enc, requestPayload); errCount == nil {
			promptTokens = count
		}
		if count, errCount := enc.Count(content); errCount == nil {
			completionTokens = int64(count)
		}
	}
	totalTokens := promptTokens + completionTokens

	payload := map[string]any{
		"id":      respID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		},
	}

	data, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		data = []byte(fmt.Sprintf(`{"id":%q,"object":"chat.completion","created":%d,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`,
			respID, created, model, content,
		))
		return data, usage.Detail{}
	}

	return data, usage.Detail{InputTokens: promptTokens, OutputTokens: completionTokens, TotalTokens: totalTokens}
}
