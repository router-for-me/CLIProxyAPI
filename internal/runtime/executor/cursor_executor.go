package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	cursorproto "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/proto"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http2"
)

const (
	cursorAPIURL            = "https://api2.cursor.sh"
	cursorRunPath           = "/agent.v1.AgentService/Run"
	cursorModelsPath        = "/agent.v1.AgentService/GetUsableModels"
	cursorClientVersion     = "cli-2026.02.13-41ac335"
	cursorAuthType          = "cursor"
	cursorHeartbeatInterval = 5 * time.Second
	cursorSessionTTL        = 5 * time.Minute
	cursorFrameIdleTimeout  = 90 * time.Second
	cursorCheckpointTTL     = 30 * time.Minute
	cursorStreamFlushDelay  = 16 * time.Millisecond
	cursorStreamMaxBatch    = 512
)

// CursorExecutor handles requests to the Cursor API via Connect+Protobuf protocol.
type CursorExecutor struct {
	cfg           *config.Config
	mu            sync.Mutex
	sessions      map[string]*cursorSession
	checkpoints   map[string]*savedCheckpoint  // keyed by conversationId
	stateOwners   map[string]*cursorStateOwner // rejects writes from retired conversation owners
	openStream    func(string) (cursorStream, error)
	processFrames cursorFrameProcessor
}

// savedCheckpoint stores the server's conversation_checkpoint_update for reuse.
type savedCheckpoint struct {
	data      []byte            // raw ConversationStateStructure protobuf bytes
	blobStore map[string][]byte // blobs referenced by the checkpoint
	authID    string            // auth that produced this checkpoint (checkpoint is auth-specific)
	updatedAt time.Time
}

type cursorStateOwner struct {
	cancel context.CancelFunc
	stream cursorStream
}

type cursorStream interface {
	ID() string
	Write([]byte) error
	Data() <-chan []byte
	Done() <-chan struct{}
	Err() error
	Close()
}

type cursorFrameProcessor func(
	ctx context.Context,
	stream cursorStream,
	blobStore map[string][]byte,
	mcpTools []cursorproto.McpToolDef,
	onText func(text string, isThinking bool),
	onMcpExec func(exec pendingMcpExec),
	toolResultCh <-chan []toolResultInfo,
	tokenUsage *cursorTokenUsage,
	onCheckpoint func(data []byte),
) error

type cursorSession struct {
	stream         cursorStream
	blobStore      map[string][]byte
	mcpTools       []cursorproto.McpToolDef
	pending        []pendingMcpExec
	cancel         context.CancelFunc // cancels the session-scoped heartbeat (NOT tied to HTTP request)
	createdAt      time.Time
	authID         string                                                                // auth file ID that created this session (for multi-account isolation)
	toolResultCh   chan []toolResultInfo                                                 // receives tool results from the next HTTP request
	resumeOutCh    chan cliproxyexecutor.StreamChunk                                     // output channel for resumed response
	switchOutput   func(ch chan cliproxyexecutor.StreamChunk, outputCtx context.Context) // switch output channel/request context
	conversationID string
	owner          *cursorStateOwner
}

type pendingMcpExec struct {
	ExecMsgId  uint32
	ExecId     string
	ToolCallId string
	ToolName   string
	Args       string // JSON-encoded args
}

// NewCursorExecutor constructs a new executor instance.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	e := &CursorExecutor{
		cfg:         cfg,
		sessions:    make(map[string]*cursorSession),
		checkpoints: make(map[string]*savedCheckpoint),
		stateOwners: make(map[string]*cursorStateOwner),
		openStream: func(accessToken string) (cursorStream, error) {
			return openCursorH2Stream(accessToken)
		},
		processFrames: processH2SessionFrames,
	}
	go e.cleanupLoop()
	return e
}

// Identifier implements ProviderExecutor.
func (e *CursorExecutor) Identifier() string { return cursorAuthType }

// CloseExecutionSession implements ExecutionSessionCloser.
func (e *CursorExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sessionID == cliproxyauth.CloseAllExecutionSessionsID {
		for k, s := range e.sessions {
			s.cancel()
			delete(e.sessions, k)
		}
		return
	}
	if s, ok := e.sessions[sessionID]; ok {
		s.cancel()
		delete(e.sessions, sessionID)
	}
}

func (e *CursorExecutor) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		e.mu.Lock()
		for k, s := range e.sessions {
			if time.Since(s.createdAt) > cursorSessionTTL {
				s.cancel()
				delete(e.sessions, k)
			}
		}
		for k, cp := range e.checkpoints {
			if time.Since(cp.updatedAt) > cursorCheckpointTTL {
				delete(e.checkpoints, k)
			}
		}
		e.mu.Unlock()
	}
}

// findSessionByConversationLocked searches for a session matching the given
// conversationId regardless of authID. Used to find and clean up stale sessions
// from a previous auth after quota failover. Caller must hold e.mu.
func (e *CursorExecutor) findSessionByConversationLocked(convId string) string {
	suffix := ":" + convId
	for k := range e.sessions {
		if strings.HasSuffix(k, suffix) {
			return k
		}
	}
	return ""
}

// retireConversationState atomically makes all existing owners for a
// conversation stale before releasing their streams. A late checkpoint write
// from a canceled processor is rejected after its ownership token is removed.
func (e *CursorExecutor) retireConversationState(conversationID string) {
	var retired []*cursorSession
	var owner *cursorStateOwner
	suffix := ":" + conversationID

	e.mu.Lock()
	owner = e.stateOwners[conversationID]
	delete(e.stateOwners, conversationID)
	for key, session := range e.sessions {
		if strings.HasSuffix(key, suffix) {
			delete(e.sessions, key)
			retired = append(retired, session)
		}
	}
	delete(e.checkpoints, conversationID)
	e.mu.Unlock()

	if owner != nil {
		if owner.cancel != nil {
			owner.cancel()
		}
		if owner.stream != nil {
			owner.stream.Close()
		}
	}
	for _, session := range retired {
		if owner != nil && session.owner == owner {
			continue
		}
		if session.cancel != nil {
			session.cancel()
		}
		if session.stream != nil {
			session.stream.Close()
		}
	}
}

func (e *CursorExecutor) beginConversationStream(conversationID string) *cursorStateOwner {
	e.mu.Lock()
	defer e.mu.Unlock()
	owner := &cursorStateOwner{}
	e.stateOwners[conversationID] = owner
	return owner
}

func (e *CursorExecutor) releaseConversationStream(conversationID string, owner *cursorStateOwner) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateOwners[conversationID] == owner {
		delete(e.stateOwners, conversationID)
	}
}

func (e *CursorExecutor) attachConversationStream(conversationID string, owner *cursorStateOwner, cancel context.CancelFunc, stream cursorStream) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateOwners[conversationID] != owner {
		return false
	}
	owner.cancel = cancel
	owner.stream = stream
	return true
}

func (e *CursorExecutor) publishConversationSession(conversationID, sessionKey string, owner *cursorStateOwner, session *cursorSession, replace bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateOwners[conversationID] != owner {
		return false
	}
	if _, exists := e.sessions[sessionKey]; exists && !replace {
		return false
	}
	session.conversationID = conversationID
	session.owner = owner
	e.sessions[sessionKey] = session
	return true
}

func (e *CursorExecutor) saveCheckpoint(conversationID string, owner *cursorStateOwner, checkpoint *savedCheckpoint) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateOwners[conversationID] != owner {
		return false
	}
	e.checkpoints[conversationID] = checkpoint
	return true
}

// cursorStatusErr implements the StatusError and RetryAfter interfaces so the
// conductor can classify Cursor errors (e.g. 429 → quota cooldown).
type cursorStatusErr struct {
	code int
	msg  string
}

func (e cursorStatusErr) Error() string              { return e.msg }
func (e cursorStatusErr) StatusCode() int            { return e.code }
func (e cursorStatusErr) RetryAfter() *time.Duration { return nil } // no retry-after info from Cursor; conductor uses exponential backoff

// classifyCursorError maps Cursor Connect/H2 errors to HTTP status codes.
// Layer 1: precise match on ConnectError.Code (gRPC standard codes).
// Layer 2: fuzzy string match for H2 frame errors and unknown formats.
// Unclassified errors pass through unchanged.
func classifyCursorError(err error) error {
	if err == nil {
		return nil
	}

	// Layer 1: structured ConnectError from ParseConnectEndStream
	var ce *cursorproto.ConnectError
	if errors.As(err, &ce) {
		log.Infof("cursor: Connect error code=%q message=%q", ce.Code, ce.Message)
		switch ce.Code {
		case "resource_exhausted":
			return cursorStatusErr{code: 429, msg: err.Error()}
		case "unauthenticated":
			return cursorStatusErr{code: 401, msg: err.Error()}
		case "permission_denied":
			return cursorStatusErr{code: 403, msg: err.Error()}
		case "unavailable":
			return cursorStatusErr{code: 503, msg: err.Error()}
		case "internal":
			return cursorStatusErr{code: 500, msg: err.Error()}
		default:
			// Unknown Connect code — log for observation, treat as 502
			return cursorStatusErr{code: 502, msg: err.Error()}
		}
	}

	// Layer 2: fuzzy match for H2 errors and unstructured messages
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "too many"):
		return cursorStatusErr{code: 429, msg: err.Error()}
	case strings.Contains(msg, "rst_stream") || strings.Contains(msg, "goaway"):
		return cursorStatusErr{code: 502, msg: err.Error()}
	}

	return err
}

// PrepareRequest implements ProviderExecutor (for HttpRequest support).
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	token := cursorAccessToken(auth)
	if token == "" {
		return fmt.Errorf("cursor: access token not found")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects credentials and executes the request.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("cursor: request is nil")
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// CountTokens estimates token count locally using tiktoken.
func (e *CursorExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	defer func() {
		if err != nil {
			log.Warnf("cursor CountTokens error: %v", err)
		} else {
			log.Debugf("cursor CountTokens: model=%s result=%s", req.Model, string(resp.Payload))
		}
	}()
	model := gjson.GetBytes(req.Payload, "model").String()
	if model == "" {
		model = req.Model
	}

	enc, err := getTokenizer(model)
	if err != nil {
		// Fallback: return zero tokens rather than error (avoids 502)
		return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(0)}, nil
	}

	// Detect format: Claude (/v1/messages) vs OpenAI (/v1/chat/completions)
	var count int64
	if gjson.GetBytes(req.Payload, "system").Exists() || opts.SourceFormat.String() == "claude" {
		count, _ = countClaudeChatTokens(enc, req.Payload)
	} else {
		count, _ = countOpenAIChatTokens(enc, req.Payload)
	}

	return cliproxyexecutor.Response{Payload: buildOpenAIUsageJSON(count)}, nil
}

// Refresh attempts to refresh the Cursor access token.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	refreshToken := cursorRefreshToken(auth)
	if refreshToken == "" {
		return nil, fmt.Errorf("cursor: no refresh token available")
	}

	tokens, err := cursorauth.RefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	expiresAt := cursorauth.GetTokenExpiry(tokens.AccessToken)

	newAuth := auth.Clone()
	newAuth.Metadata["access_token"] = tokens.AccessToken
	newAuth.Metadata["refresh_token"] = tokens.RefreshToken
	newAuth.Metadata["expires_at"] = expiresAt.Format(time.RFC3339)
	return newAuth, nil
}

// Execute handles non-streaming requests.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	log.Debugf("cursor Execute: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("cursor Execute PANIC: %v", recovered)
			err = fmt.Errorf("cursor: internal panic: %v", recovered)
		}
		if err != nil {
			log.Warnf("cursor Execute error: %v", err)
		}
	}()

	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return resp, fmt.Errorf("cursor: access token not found")
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	if from.String() != "" && from.String() != "openai" {
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), false)
	}

	usageReporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer usageReporter.EnsurePublished(ctx)
	defer usageReporter.TrackFailure(ctx, &err)

	parsed := parseOpenAIRequest(payload)
	sessionID := extractClaudeCodeSessionId(req.Payload)
	conversationID := deriveConversationId(apiKeyFromContext(ctx), sessionID, parsed.SystemPrompt)
	openAICompatible := isOpenAICompatibleSourceFormat(from)
	if openAICompatible && len(parsed.ToolResults) > 0 {
		e.retireConversationState(conversationID)
		log.Infof("cursor: using cold continuation for %d non-stream tool result(s)", len(parsed.ToolResults))
		flattenConversationIntoUserText(parsed)
	} else if len(parsed.Turns) > 0 {
		// Mirror ExecuteStream's no-checkpoint branch: Cursor's Run endpoint
		// rejects structured turns on a conversation without server-side state
		// with "Connect error internal", but always accepts a flattened
		// UserText transcript. Execute never attaches a checkpoint, so every
		// multi-message non-streaming request must be flattened.
		log.Debugf("cursor: non-stream request with %d turn(s), flattening into user text", len(parsed.Turns))
		flattenConversationIntoUserText(parsed)
	}
	params := buildRunRequestParams(parsed, conversationID, req.Model)

	requestBytes := cursorproto.EncodeRunRequest(params)
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)
	stream, err := e.openStream(accessToken)
	if err != nil {
		return resp, err
	}
	defer stream.Close()
	if err = stream.Write(framedRequest); err != nil {
		return resp, fmt.Errorf("cursor: failed to send request: %w", err)
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()
	go cursorH2Heartbeat(sessionCtx, stream)

	var fullText strings.Builder
	var thinkingText strings.Builder
	var toolCalls []pendingMcpExec
	usage := &cursorTokenUsage{}
	usage.setInputEstimate(len(payload))
	var onMcpExec func(pendingMcpExec)
	if openAICompatible {
		onMcpExec = func(toolCall pendingMcpExec) {
			toolCalls = append(toolCalls, toolCall)
		}
	}
	if streamErr := e.processFrames(sessionCtx, stream, params.BlobStore, params.McpTools,
		func(text string, isThinking bool) {
			if isThinking {
				thinkingText.WriteString(text)
			} else {
				fullText.WriteString(text)
			}
		},
		onMcpExec,
		nil,
		usage,
		nil,
	); streamErr != nil {
		return resp, classifyCursorError(fmt.Errorf("cursor: stream error: %w", streamErr))
	}

	message := map[string]any{
		"role":    "assistant",
		"content": fullText.String(),
	}
	if thinkingText.Len() > 0 {
		message["reasoning_content"] = thinkingText.String()
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
		serialized := make([]map[string]any, 0, len(toolCalls))
		for _, toolCall := range toolCalls {
			serialized = append(serialized, map[string]any{
				"id":   toolCall.ToolCallId,
				"type": "function",
				"function": map[string]any{
					"name":      toolCall.ToolName,
					"arguments": toolCall.Args,
				},
			})
		}
		message["tool_calls"] = serialized
	}
	inputTokens, outputTokens := usage.get()
	body := map[string]any{
		"id":      "chatcmpl-" + uuid.New().String()[:28],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   parsed.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": map[string]int64{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		},
	}
	result, marshalErr := json.Marshal(body)
	if marshalErr == nil {
		usageReporter.Publish(ctx, helps.ParseOpenAIUsage(result))
	}
	if marshalErr != nil {
		return resp, fmt.Errorf("cursor: encode non-stream response: %w", marshalErr)
	}
	if from.String() != "" && from.String() != "openai" {
		var param any
		result = sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), payload, result, &param)
	}
	resp.Payload = result
	return resp, nil
}

func isOpenAICompatibleSourceFormat(format sdktranslator.Format) bool {
	return format.String() == "" || format.String() == "openai"
}

// ExecuteStream handles streaming requests. Native Claude requests can resume
// a parked MCP/H2 session; OpenAI-compatible tool results use a fresh request
// rebuilt from the complete client transcript.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	usageReporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer usageReporter.EnsurePublished(ctx)
	defer usageReporter.TrackFailure(ctx, &err)
	log.Debugf("cursor ExecuteStream: model=%s sourceFormat=%s payloadLen=%d", req.Model, opts.SourceFormat, len(req.Payload))
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("cursor ExecuteStream PANIC: %v", r)
			err = fmt.Errorf("cursor: internal panic: %v", r)
		}
		if err != nil {
			log.Warnf("cursor ExecuteStream error: %v", err)
		}
	}()
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("cursor: access token not found")
	}

	// Extract session_id before translation, which strips metadata.
	sessionID := extractClaudeCodeSessionId(req.Payload)
	if sessionID == "" && len(opts.OriginalRequest) > 0 {
		sessionID = extractClaudeCodeSessionId(opts.OriginalRequest)
	}

	// Translate input to OpenAI format if needed
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	payload := req.Payload
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	if from.String() != "" && from.String() != "openai" {
		log.Debugf("cursor: translating request from %s to openai", from)
		payload = sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(payload), true)
		log.Debugf("cursor: translated payload len=%d", len(payload))
	}

	parsed := parseOpenAIRequest(payload)
	log.Debugf("cursor: parsed request: model=%s userText=%d chars, turns=%d, tools=%d, toolResults=%d",
		parsed.Model, len(parsed.UserText), len(parsed.Turns), len(parsed.Tools), len(parsed.ToolResults))

	conversationId := deriveConversationId(apiKeyFromContext(ctx), sessionID, parsed.SystemPrompt)
	authID := auth.ID // e.g. "cursor.json" or "cursor-account2.json"
	log.Debugf("cursor: conversationId=%s authID=%s", conversationId, authID)

	// Native Claude requests retain the current resumable-H2 behavior. OpenAI
	// clients may cross a gateway boundary between the tool call and its result,
	// so their continuation is rebuilt from the complete transcript instead.
	openAICompatible := isOpenAICompatibleSourceFormat(from)
	coldToolContinuation := openAICompatible && len(parsed.ToolResults) > 0
	sessionKey := authID + ":" + conversationId
	checkpointKey := conversationId
	needsTranslate := from.String() != "" && from.String() != "openai"

	if coldToolContinuation {
		e.retireConversationState(conversationId)
		log.Infof("cursor: using cold continuation for %d tool result(s)", len(parsed.ToolResults))
	}

	// Native Claude requests retain the existing same-stream resume path.
	if len(parsed.ToolResults) > 0 && !coldToolContinuation {
		e.mu.Lock()
		session, hasSession := e.sessions[sessionKey]
		if hasSession {
			delete(e.sessions, sessionKey)
		}
		if !hasSession {
			if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
				oldSession := e.sessions[oldKey]
				log.Infof("cursor: cleaning up stale session from auth %s for conv=%s (auth migrated to %s)", oldSession.authID, conversationId, authID)
				oldSession.cancel()
				if oldSession.stream != nil {
					oldSession.stream.Close()
				}
				delete(e.sessions, oldKey)
			}
		}
		e.mu.Unlock()

		if hasSession && session.stream != nil && session.authID == authID {
			log.Debugf("cursor: resuming session %s with %d tool results", sessionKey, len(parsed.ToolResults))
			return e.resumeWithToolResults(ctx, sessionKey, session, parsed, from, to, req, originalPayload, payload, needsTranslate)
		}
		if hasSession && session.authID != authID {
			log.Warnf("cursor: session %s belongs to auth %s, but request is from %s — skipping resume", sessionKey, session.authID, authID)
		}
	}

	// Clean up any stale session for this key (or from a previous auth on same conversation)
	e.mu.Lock()
	if old, ok := e.sessions[sessionKey]; ok {
		old.cancel()
		delete(e.sessions, sessionKey)
	} else if oldKey := e.findSessionByConversationLocked(conversationId); oldKey != "" {
		old := e.sessions[oldKey]
		old.cancel()
		if old.stream != nil {
			old.stream.Close()
		}
		delete(e.sessions, oldKey)
	}
	e.mu.Unlock()
	streamOwner := e.beginConversationStream(conversationId)
	workerOwnsState := false
	defer func() {
		if !workerOwnsState {
			e.releaseConversationStream(conversationId, streamOwner)
		}
	}()

	// Look up saved checkpoint for this conversation (keyed by conversationId only).
	// Checkpoint is auth-specific: if auth changed (e.g. quota exhaustion failover),
	// the old checkpoint is useless on the new account — discard and flatten.
	e.mu.Lock()
	saved, hasCheckpoint := e.checkpoints[checkpointKey]
	e.mu.Unlock()

	params := buildRunRequestParams(parsed, conversationId, req.Model)

	if coldToolContinuation {
		flattenConversationIntoUserText(parsed)
		params = buildRunRequestParams(parsed, conversationId, req.Model)
	} else if hasCheckpoint && saved.data != nil && saved.authID == authID {
		// Same auth — use checkpoint normally.
		log.Debugf("cursor: using saved checkpoint (%d bytes) for conv=%s auth=%s", len(saved.data), checkpointKey, authID)
		params.RawCheckpoint = saved.data
		if params.BlobStore == nil {
			params.BlobStore = make(map[string][]byte)
		}
		for key, value := range saved.blobStore {
			if _, exists := params.BlobStore[key]; !exists {
				params.BlobStore[key] = value
			}
		}
	} else if hasCheckpoint && saved.data != nil && saved.authID != authID {
		// Auth changed (quota failover) — checkpoints are not portable.
		log.Infof("cursor: auth migrated (%s → %s) for conv=%s, discarding checkpoint and flattening context", saved.authID, authID, checkpointKey)
		e.mu.Lock()
		delete(e.checkpoints, checkpointKey)
		e.mu.Unlock()
		if len(parsed.Turns) > 0 {
			flattenConversationIntoUserText(parsed)
			params = buildRunRequestParams(parsed, conversationId, req.Model)
		}
	} else if len(parsed.Turns) > 0 {
		// Cursor reliably reads UserText, while structured turns may be ignored.
		log.Debugf("cursor: no checkpoint, flattening %d turns into user text", len(parsed.Turns))
		flattenConversationIntoUserText(parsed)
		params = buildRunRequestParams(parsed, conversationId, req.Model)
	}
	requestBytes := cursorproto.EncodeRunRequest(params)
	framedRequest := cursorproto.FrameConnectMessage(requestBytes, 0)

	stream, err := e.openStream(accessToken)
	if err != nil {
		return nil, err
	}

	if err := stream.Write(framedRequest); err != nil {
		stream.Close()
		return nil, fmt.Errorf("cursor: failed to send request: %w", err)
	}
	// The Cursor stream lives only for this HTTP request when serving an
	// OpenAI-compatible client. Native Claude tool calls can still retain it.
	sessionParent := context.Background()
	if openAICompatible {
		sessionParent = ctx
	}
	sessionCtx, sessionCancel := context.WithCancel(sessionParent)
	if !e.attachConversationStream(conversationId, streamOwner, sessionCancel, stream) {
		sessionCancel()
		stream.Close()
		return nil, context.Canceled
	}
	go cursorH2Heartbeat(sessionCtx, stream)

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)
	chatId := "chatcmpl-" + uuid.New().String()[:28]
	created := time.Now().Unix()

	var streamParam any

	// OpenAI-compatible tool results use a fresh request with the full transcript.
	// A nil channel tells the frame processor to finish immediately after emitting
	// an MCP tool call instead of parking this H2 connection.
	var toolResultCh chan []toolResultInfo
	if !openAICompatible {
		toolResultCh = make(chan []toolResultInfo, 1)
	}

	// Switchable output starts with the current HTTP response channel.
	var outMu sync.Mutex
	currentOut := chunks
	currentOutputCtx := ctx
	closeCurrentOutput := func() {
		outMu.Lock()
		if currentOut != nil {
			close(currentOut)
			currentOut = nil
		}
		outMu.Unlock()
	}

	emitToOut := func(chunk cliproxyexecutor.StreamChunk) bool {
		outMu.Lock()
		defer outMu.Unlock()
		if currentOut == nil {
			return false
		}
		select {
		case currentOut <- chunk:
			return true
		case <-currentOutputCtx.Done():
			return false
		case <-sessionCtx.Done():
			return false
		}
	}

	// Wrap sendChunk/sendDone to use emitToOut
	sendChunkSwitchable := func(delta string, finishReason string) {
		fr := "null"
		if finishReason != "" {
			fr = finishReason
		}
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
			chatId, created, parsed.Model, delta, fr)
		sseLine := []byte("data: " + openaiJSON + "\n")

		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(t)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
	}

	sendDoneSwitchable := func() {
		if needsTranslate {
			done := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, []byte("data: [DONE]\n"), &streamParam)
			for _, d := range done {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(d)})
			}
		}
		// No explicit [DONE] in the non-translated (OpenAI) case: the HTTP
		// handler already writes `data: [DONE]` when the chunk channel closes,
		// so emitting one here produced a duplicated [DONE] marker downstream.
	}

	// Pre-response error detection for transparent failover:
	// If the stream fails before any chunk is emitted (e.g. quota exceeded),
	// ExecuteStream returns an error so the conductor retries with a different auth.
	streamErrCh := make(chan error, 1)
	firstChunkSent := make(chan struct{}, 1) // buffered: goroutine won't block signaling
	var outputStarted atomic.Bool

	origEmitToOut := emitToOut
	emitToOut = func(chunk cliproxyexecutor.StreamChunk) bool {
		if !origEmitToOut(chunk) {
			return false
		}
		outputStarted.Store(true)
		select {
		case firstChunkSent <- struct{}{}:
		default:
		}
		return true
	}

	workerOwnsState = true
	go func() {
		defer e.releaseConversationStream(conversationId, streamOwner)
		var resumeOutCh chan cliproxyexecutor.StreamChunk
		_ = resumeOutCh
		thinkingActive := false
		toolCallIndex := 0
		usage := &cursorTokenUsage{}
		usage.setInputEstimate(len(payload))

		emitTextDelta := func(text string, isThinking bool) {
			// Emit thinking as the standard OpenAI `reasoning_content` delta
			// field instead of inline <think>...</think> tags. Inline tags
			// pollute `content` for OpenAI clients and end up rendered as
			// literal text in Anthropic/Claude clients; `reasoning_content`
			// is understood by the translators (mapped to thinking blocks
			// for Claude) and by downstream proxies.
			if isThinking {
				if !thinkingActive {
					thinkingActive = true
					sendChunkSwitchable(`{"role":"assistant","content":""}`, "")
				}
				sendChunkSwitchable(fmt.Sprintf(`{"reasoning_content":%s}`, jsonString(text)), "")
			} else {
				thinkingActive = false
				sendChunkSwitchable(fmt.Sprintf(`{"content":%s}`, jsonString(text)), "")
			}
		}
		streamCoalescer := newCursorStreamCoalescer(
			sessionCtx,
			cursorStreamFlushDelay,
			emitTextDelta,
		)

		streamErr := e.processFrames(sessionCtx, stream, params.BlobStore, params.McpTools,
			streamCoalescer.push,
			func(exec pendingMcpExec) {
				// Preserve ordering: all assistant text must reach the client
				// before the tool-call boundary is emitted.
				streamCoalescer.flush()
				thinkingActive = false
				toolCallJSON := fmt.Sprintf(`{"tool_calls":[{"index":%d,"id":%s,"type":"function","function":{"name":%s,"arguments":%s}}]}`,
					toolCallIndex, jsonString(exec.ToolCallId), jsonString(exec.ToolName), jsonString(exec.Args))
				toolCallIndex++
				sendChunkSwitchable(toolCallJSON, "")
				sendChunkSwitchable(`{}`, `"tool_calls"`)
				sendDoneSwitchable()

				if openAICompatible {
					closeCurrentOutput()
					log.Debugf("cursor: ended H2 stream after MCP tool call (tool=%s)", exec.ToolName)
					return
				}

				// Publish the resumable session before closing the current output.
				// Channel closure lets the client submit its tool result immediately;
				// that request must never observe an empty session map and cold-start.
				resumeOut := make(chan cliproxyexecutor.StreamChunk, 64)
				log.Debugf("cursor: saving session %s for MCP tool resume (tool=%s)", sessionKey, exec.ToolName)
				outMu.Lock()
				session := &cursorSession{
					stream:       stream,
					blobStore:    params.BlobStore,
					mcpTools:     params.McpTools,
					pending:      []pendingMcpExec{exec},
					cancel:       sessionCancel,
					createdAt:    time.Now(),
					authID:       authID,
					toolResultCh: toolResultCh, // reuse same channel across rounds
					resumeOutCh:  resumeOut,
					switchOutput: func(ch chan cliproxyexecutor.StreamChunk, outputCtx context.Context) {
						outMu.Lock()
						currentOut = ch
						currentOutputCtx = outputCtx
						streamParam = nil
						chatId = "chatcmpl-" + uuid.New().String()[:28]
						created = time.Now().Unix()
						outMu.Unlock()
					},
				}
				if !e.publishConversationSession(conversationId, sessionKey, streamOwner, session, true) {
					outMu.Unlock()
					sessionCancel()
					stream.Close()
					return
				}
				resumeOutCh = resumeOut

				// Publish and close under the output lock. An immediate resume can
				// find the session, but switchOutput cannot replace currentOut until
				// the original response has been closed.
				if currentOut != nil {
					close(currentOut)
					currentOut = nil
				}
				outMu.Unlock()

				// processH2SessionFrames will now block on toolResultCh (inline wait loop)
				// while continuing to handle KV messages.
			},
			toolResultCh,
			usage,
			func(cpData []byte) {
				checkpoint := &savedCheckpoint{
					data:      cpData,
					blobStore: params.BlobStore,
					authID:    authID,
					updatedAt: time.Now(),
				}
				if e.saveCheckpoint(checkpointKey, streamOwner, checkpoint) {
					log.Debugf("cursor: saved checkpoint (%d bytes) for conv=%s auth=%s", len(cpData), checkpointKey, authID)
				} else {
					log.Debugf("cursor: ignored checkpoint from retired stream for conv=%s auth=%s", checkpointKey, authID)
				}
			},
		)
		streamCoalescer.close()

		// processH2SessionFrames returned — stream is done.
		// Check if error happened before any chunks were emitted.
		if streamErr != nil {
			if outputStarted.Load() {
				// Partial output must never be presented as a successful stop.
				log.Warnf("cursor: stream error after data sent (auth=%s conv=%s): %v", authID, conversationId, streamErr)
				emitToOut(cliproxyexecutor.StreamChunk{Err: classifyCursorError(fmt.Errorf("cursor: stream interrupted after partial response: %w", streamErr))})
				closeCurrentOutput()
				sessionCancel()
				stream.Close()
				return
			} else {
				log.Warnf("cursor: stream error before data sent (auth=%s conv=%s): %v — signaling retry", authID, conversationId, streamErr)
				streamErrCh <- streamErr
				closeCurrentOutput()
				sessionCancel()
				stream.Close()
				return
			}
		}

		// Include token usage in the final stop chunk
		inputTok, outputTok := usage.get()
		stopDelta := fmt.Sprintf(`{},"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}`,
			inputTok, outputTok, inputTok+outputTok)
		// Build the stop chunk with usage embedded in the choices array level
		fr := `"stop"`
		openaiJSON := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":{},"finish_reason":%s}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
			chatId, created, parsed.Model, fr, inputTok, outputTok, inputTok+outputTok)
		usageReporter.Publish(ctx, helps.ParseOpenAIUsage([]byte(openaiJSON)))
		sseLine := []byte("data: " + openaiJSON + "\n")
		if needsTranslate {
			translated := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, payload, sseLine, &streamParam)
			for _, t := range translated {
				emitToOut(cliproxyexecutor.StreamChunk{Payload: bytes.Clone(t)})
			}
		} else {
			emitToOut(cliproxyexecutor.StreamChunk{Payload: []byte(openaiJSON)})
		}
		sendDoneSwitchable()
		_ = stopDelta // unused

		// Close whatever output channel is still active
		closeCurrentOutput()
		sessionCancel()
		stream.Close()
	}()

	// Wait for either the first chunk or a pre-response error.
	// If the stream fails before emitting any data (e.g. quota exceeded),
	// return an error so the conductor retries with a different auth.
	select {
	case streamErr := <-streamErrCh:
		return nil, classifyCursorError(fmt.Errorf("cursor: stream failed before response: %w", streamErr))
	case <-firstChunkSent:
		// Data started flowing — return stream to client
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	case <-ctx.Done():
		// No response was committed, so the request owns teardown. Without this
		// branch a canceled client can leave ExecuteStream waiting forever while
		// the detached Cursor session and heartbeat remain alive.
		closeCurrentOutput()
		sessionCancel()
		stream.Close()
		return nil, ctx.Err()
	}
}

// resumeWithToolResults injects tool results into the running processH2SessionFrames
// via the toolResultCh channel. The original goroutine from ExecuteStream is still alive,
// blocking on toolResultCh. Once we send the results, it sends the MCP result to Cursor
// and continues processing the response text — all in the same goroutine that has been
// handling KV messages the whole time.
func (e *CursorExecutor) resumeWithToolResults(
	ctx context.Context,
	sessionKey string,
	session *cursorSession,
	parsed *parsedOpenAIRequest,
	from, to sdktranslator.Format,
	req cliproxyexecutor.Request,
	originalPayload, payload []byte,
	needsTranslate bool,
) (*cliproxyexecutor.StreamResult, error) {
	log.Debugf("cursor: resumeWithToolResults: injecting %d tool results via channel", len(parsed.ToolResults))

	closeSession := func() {
		if session.cancel != nil {
			session.cancel()
		}
		if session.stream != nil {
			session.stream.Close()
		}
	}
	restoreSession := func() {
		restored := e.publishConversationSession(session.conversationID, sessionKey, session.owner, session, false)
		if !restored {
			// A concurrent request replaced this session while ownership was in
			// transit. It is no longer safe to restore, so release its resources.
			closeSession()
		}
	}
	if session.toolResultCh == nil {
		closeSession()
		return nil, fmt.Errorf("cursor: session has no toolResultCh (stale session?)")
	}
	if session.resumeOutCh == nil {
		closeSession()
		return nil, fmt.Errorf("cursor: session has no resumeOutCh")
	}
	if err := ctx.Err(); err != nil {
		restoreSession()
		return nil, err
	}
	matchedPending := false
	for _, result := range parsed.ToolResults {
		for _, pending := range session.pending {
			if result.ToolCallId == pending.ToolCallId {
				matchedPending = true
				break
			}
		}
		if matchedPending {
			break
		}
	}
	if !matchedPending {
		restoreSession()
		return nil, fmt.Errorf("cursor: tool results do not match any pending tool call")
	}

	log.Debugf("cursor: resumeWithToolResults: switching output to resumeOutCh and injecting results")

	// Switch the output channel BEFORE injecting results, so that when
	// processH2SessionFrames unblocks and starts emitting text, it writes
	// to the resumeOutCh which the new HTTP handler is reading from.
	if session.switchOutput != nil {
		session.switchOutput(session.resumeOutCh, ctx)
	}

	// Inject tool results — this unblocks the intentionally parked session.
	select {
	case session.toolResultCh <- parsed.ToolResults:
	case <-ctx.Done():
		restoreSession()
		return nil, ctx.Err()
	}

	// Return the resumeOutCh for the new HTTP handler to read from
	return &cliproxyexecutor.StreamResult{Chunks: session.resumeOutCh}, nil
}

// --- H2Stream helpers ---

func openCursorH2Stream(accessToken string) (*cursorproto.H2Stream, error) {
	headers := map[string]string{
		":path":                    cursorRunPath,
		"content-type":             "application/connect+proto",
		"connect-protocol-version": "1",
		"te":                       "trailers",
		"authorization":            "Bearer " + accessToken,
		"x-ghost-mode":             "true",
		"x-cursor-client-version":  cursorClientVersion,
		"x-cursor-client-type":     "cli",
		"x-request-id":             uuid.New().String(),
	}
	return cursorproto.DialH2Stream("api2.cursor.sh", headers)
}

func cursorH2Heartbeat(ctx context.Context, stream cursorStream) {
	ticker := time.NewTicker(cursorHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hb := cursorproto.EncodeHeartbeat()
			frame := cursorproto.FrameConnectMessage(hb, 0)
			if err := stream.Write(frame); err != nil {
				return
			}
		}
	}
}

// --- Response processing ---

type cursorStreamDeltaCommand struct {
	text       string
	isThinking bool
	flush      bool
	close      bool
	ack        chan struct{}
}

// cursorStreamCoalescer turns Cursor's bursty protobuf deltas into SSE-sized
// updates at roughly one display frame. The first delta remains immediate,
// while later adjacent deltas are grouped for at most cursorStreamFlushDelay.
type cursorStreamCoalescer struct {
	commands chan cursorStreamDeltaCommand
	done     chan struct{}
}

func newCursorStreamCoalescer(
	ctx context.Context,
	flushDelay time.Duration,
	emit func(text string, isThinking bool),
) *cursorStreamCoalescer {
	coalescer := &cursorStreamCoalescer{
		commands: make(chan cursorStreamDeltaCommand),
		done:     make(chan struct{}),
	}
	go coalescer.run(ctx, flushDelay, emit)
	return coalescer
}

func (c *cursorStreamCoalescer) push(text string, isThinking bool) {
	if text == "" {
		return
	}
	select {
	case c.commands <- cursorStreamDeltaCommand{text: text, isThinking: isThinking}:
	case <-c.done:
	}
}

func (c *cursorStreamCoalescer) flush() {
	c.sync(cursorStreamDeltaCommand{flush: true, ack: make(chan struct{})})
}

func (c *cursorStreamCoalescer) close() {
	c.sync(cursorStreamDeltaCommand{close: true, ack: make(chan struct{})})
}

func (c *cursorStreamCoalescer) sync(command cursorStreamDeltaCommand) {
	select {
	case c.commands <- command:
	case <-c.done:
		return
	}
	select {
	case <-command.ack:
	case <-c.done:
	}
}

func (c *cursorStreamCoalescer) run(
	ctx context.Context,
	flushDelay time.Duration,
	emit func(text string, isThinking bool),
) {
	defer close(c.done)

	timer := time.NewTimer(flushDelay)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	var pending strings.Builder
	var pendingThinking bool
	var timerC <-chan time.Time
	emittedFirst := false

	stopTimer := func() {
		if timerC == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
	}
	flushPending := func() {
		stopTimer()
		if pending.Len() == 0 {
			return
		}
		text := pending.String()
		pending.Reset()
		emit(text, pendingThinking)
	}
	queue := func(text string, isThinking bool) {
		if !emittedFirst {
			emittedFirst = true
			emit(text, isThinking)
			return
		}
		if pending.Len() > 0 && pendingThinking != isThinking {
			flushPending()
		}
		if pending.Len() == 0 {
			pendingThinking = isThinking
			timer.Reset(flushDelay)
			timerC = timer.C
		}
		pending.WriteString(text)
		if pending.Len() >= cursorStreamMaxBatch {
			flushPending()
		}
	}

	for {
		select {
		case <-ctx.Done():
			flushPending()
			return
		case <-timerC:
			timerC = nil
			if pending.Len() > 0 {
				text := pending.String()
				pending.Reset()
				emit(text, pendingThinking)
			}
		case command := <-c.commands:
			switch {
			case command.close:
				flushPending()
				close(command.ack)
				return
			case command.flush:
				flushPending()
				close(command.ack)
			default:
				queue(command.text, command.isThinking)
			}
		}
	}
}

// cursorTokenUsage tracks token counts from Cursor's TokenDeltaUpdate messages.
type cursorTokenUsage struct {
	mu             sync.Mutex
	outputTokens   int64
	inputTokensEst int64 // estimated from request payload size
}

func (u *cursorTokenUsage) addOutput(delta int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.outputTokens += delta
}

func (u *cursorTokenUsage) setInputEstimate(payloadBytes int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Rough estimate: ~4 bytes per token for mixed content
	u.inputTokensEst = int64(payloadBytes / 4)
	if u.inputTokensEst < 1 {
		u.inputTokensEst = 1
	}
}

func (u *cursorTokenUsage) get() (input, output int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inputTokensEst, u.outputTokens
}

// writeCursorReply sends an inline Connect reply. A failed reply strands the
// server-side request and hangs the whole turn, so the error must surface.
func writeCursorReply(stream cursorStream, payload []byte) error {
	if err := stream.Write(cursorproto.FrameConnectMessage(payload, 0)); err != nil {
		return fmt.Errorf("cursor: reply write failed: %w", err)
	}
	return nil
}

func processH2SessionFrames(
	ctx context.Context,
	stream cursorStream,
	blobStore map[string][]byte,
	mcpTools []cursorproto.McpToolDef,
	onText func(text string, isThinking bool),
	onMcpExec func(exec pendingMcpExec),
	toolResultCh <-chan []toolResultInfo, // nil for no tool result injection; non-nil to wait for results
	tokenUsage *cursorTokenUsage, // tracks accumulated token usage (may be nil)
	onCheckpoint func(data []byte), // called when server sends conversation_checkpoint_update
) error {
	var buf bytes.Buffer
	rejectReason := "Tool not available in this environment. Use the MCP tools provided instead."
	log.Debugf("cursor: processH2SessionFrames started for streamID=%s, waiting for data...", stream.ID())
	idleTimer := time.NewTimer(cursorFrameIdleTimeout)
	defer idleTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Debugf("cursor: processH2SessionFrames exiting: context done")
			return ctx.Err()
		case <-idleTimer.C:
			// Heartbeats arrive every few seconds; a gap this long means a
			// server request went unanswered (or the pipeline froze) and the
			// turn would otherwise hang forever.
			return fmt.Errorf("cursor: no frames for %v (stranded server request?)", cursorFrameIdleTimeout)
		case data, ok := <-stream.Data():
			idleTimer.Reset(cursorFrameIdleTimeout)
			if !ok {
				log.Debugf("cursor: processH2SessionFrames[%s]: exiting: stream data channel closed", stream.ID())
				return stream.Err() // may be RST_STREAM, GOAWAY, or nil for clean close
			}
			// Log first 20 bytes of raw data for debugging
			previewLen := min(20, len(data))
			log.Debugf("cursor: processH2SessionFrames[%s]: received %d bytes from dataCh, first bytes: %x (%q)", stream.ID(), len(data), data[:previewLen], string(data[:previewLen]))
			buf.Write(data)
			log.Debugf("cursor: processH2SessionFrames[%s]: buf total=%d", stream.ID(), buf.Len())

			// Process all complete frames
			for {
				currentBuf := buf.Bytes()
				if len(currentBuf) == 0 {
					break
				}
				flags, payload, consumed, ok := cursorproto.ParseConnectFrame(currentBuf)
				if !ok {
					// Log detailed info about why parsing failed
					previewLen := min(20, len(currentBuf))
					log.Debugf("cursor: incomplete frame in buffer, waiting for more data (buf=%d bytes, first bytes: %x = %q)", len(currentBuf), currentBuf[:previewLen], string(currentBuf[:previewLen]))
					break
				}
				buf.Next(consumed)
				log.Debugf("cursor: parsed Connect frame flags=0x%02x payload=%d bytes consumed=%d", flags, len(payload), consumed)

				if flags&cursorproto.ConnectEndStreamFlag != 0 {
					if err := cursorproto.ParseConnectEndStream(payload); err != nil {
						log.Warnf("cursor: connect end stream error: %v", err)
						return err // propagate server-side errors (quota, rate limit, etc.)
					}
					continue
				}

				msg, err := cursorproto.DecodeAgentServerMessage(payload)
				if err != nil {
					log.Debugf("cursor: failed to decode server message: %v", err)
					continue
				}

				log.Debugf("cursor: decoded server message type=%d", msg.Type)
				switch msg.Type {
				case cursorproto.ServerMsgTextDelta:
					if msg.Text != "" && onText != nil {
						onText(msg.Text, false)
					}
				case cursorproto.ServerMsgThinkingDelta:
					if msg.Text != "" && onText != nil {
						onText(msg.Text, true)
					}
				case cursorproto.ServerMsgThinkingCompleted:
					// Handled by caller

				case cursorproto.ServerMsgTurnEnded:
					log.Debugf("cursor: TurnEnded received, stream will finish")
					return nil // clean completion

				case cursorproto.ServerMsgHeartbeat:
					// Server heartbeat, ignore silently
					continue

				case cursorproto.ServerMsgCheckpoint:
					if onCheckpoint != nil && len(msg.CheckpointData) > 0 {
						onCheckpoint(msg.CheckpointData)
					}
					continue

				case cursorproto.ServerMsgTokenDelta:
					if tokenUsage != nil && msg.TokenDelta > 0 {
						tokenUsage.addOutput(msg.TokenDelta)
					}
					continue

				case cursorproto.ServerMsgKvGetBlob:
					blobKey := cursorproto.BlobIdHex(msg.BlobId)
					data := blobStore[blobKey]
					resp := cursorproto.EncodeKvGetBlobResult(msg.KvId, data)
					if errReply := writeCursorReply(stream, resp); errReply != nil {
						return errReply
					}

				case cursorproto.ServerMsgKvSetBlob:
					blobKey := cursorproto.BlobIdHex(msg.BlobId)
					blobStore[blobKey] = append([]byte(nil), msg.BlobData...)
					resp := cursorproto.EncodeKvSetBlobResult(msg.KvId)
					if errReply := writeCursorReply(stream, resp); errReply != nil {
						return errReply
					}

				case cursorproto.ServerMsgExecRequestCtx:
					resp := cursorproto.EncodeExecRequestContextResult(msg.ExecMsgId, msg.ExecId, mcpTools)
					if errReply := writeCursorReply(stream, resp); errReply != nil {
						return errReply
					}

				case cursorproto.ServerMsgExecMcpArgs:
					if onMcpExec != nil {
						decodedArgs := decodeMcpArgsToJSON(msg.McpArgs)
						toolCallId := normalizeToolCallID(msg.McpToolCallId)
						if toolCallId == "" {
							toolCallId = uuid.New().String()
						}
						log.Debugf("cursor: received mcpArgs from server: execMsgId=%d execId=%q toolName=%s toolCallId=%s",
							msg.ExecMsgId, msg.ExecId, msg.McpToolName, toolCallId)
						pending := pendingMcpExec{
							ExecMsgId:  msg.ExecMsgId,
							ExecId:     msg.ExecId,
							ToolCallId: toolCallId,
							ToolName:   msg.McpToolName,
							Args:       decodedArgs,
						}
						onMcpExec(pending)

						if toolResultCh == nil {
							return nil
						}

						// Inline mode: wait for tool result while handling KV/heartbeat
						log.Debugf("cursor: waiting for tool result on channel (inline mode)...")
						var toolResults []toolResultInfo
						waitIdle := time.NewTimer(cursorFrameIdleTimeout)
						defer waitIdle.Stop()
					waitLoop:
						for {
							select {
							case <-ctx.Done():
								return ctx.Err()
							case <-waitIdle.C:
								return fmt.Errorf("cursor: no frames for %v while waiting for tool result (stranded server request?)", cursorFrameIdleTimeout)
							case results, ok := <-toolResultCh:
								if !ok {
									return nil
								}
								toolResults = results
								break waitLoop
							case waitData, ok := <-stream.Data():
								if !ok {
									return stream.Err()
								}
								waitIdle.Reset(cursorFrameIdleTimeout)
								buf.Write(waitData)
								for {
									cb := buf.Bytes()
									if len(cb) == 0 {
										break
									}
									wf, wp, wc, wok := cursorproto.ParseConnectFrame(cb)
									if !wok {
										break
									}
									buf.Next(wc)
									if wf&cursorproto.ConnectEndStreamFlag != 0 {
										continue
									}
									wmsg, werr := cursorproto.DecodeAgentServerMessage(wp)
									if werr != nil {
										continue
									}
									switch wmsg.Type {
									case cursorproto.ServerMsgKvGetBlob:
										blobKey := cursorproto.BlobIdHex(wmsg.BlobId)
										d := blobStore[blobKey]
										if errReply := writeCursorReply(stream, cursorproto.EncodeKvGetBlobResult(wmsg.KvId, d)); errReply != nil {
											return errReply
										}
									case cursorproto.ServerMsgKvSetBlob:
										blobKey := cursorproto.BlobIdHex(wmsg.BlobId)
										blobStore[blobKey] = append([]byte(nil), wmsg.BlobData...)
										if errReply := writeCursorReply(stream, cursorproto.EncodeKvSetBlobResult(wmsg.KvId)); errReply != nil {
											return errReply
										}
									case cursorproto.ServerMsgExecRequestCtx:
										if errReply := writeCursorReply(stream, cursorproto.EncodeExecRequestContextResult(wmsg.ExecMsgId, wmsg.ExecId, mcpTools)); errReply != nil {
											return errReply
										}
									case cursorproto.ServerMsgCheckpoint:
										if onCheckpoint != nil && len(wmsg.CheckpointData) > 0 {
											onCheckpoint(wmsg.CheckpointData)
										}
									}
								}
							case <-stream.Done():
								return stream.Err()
							}
						}

						// Send MCP result
						for _, tr := range toolResults {
							if tr.ToolCallId == pending.ToolCallId {
								log.Debugf("cursor: sending inline MCP result for tool=%s", pending.ToolName)
								resultBytes := cursorproto.EncodeExecMcpResult(pending.ExecMsgId, pending.ExecId, tr.Content, false)
								if errReply := writeCursorReply(stream, resultBytes); errReply != nil {
									return errReply
								}
								break
							}
						}
						continue
					}

				case cursorproto.ServerMsgExecReadArgs:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecReadRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecWriteArgs:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecWriteRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecDeleteArgs:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecDeleteRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecLsArgs:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecLsRejected(msg.ExecMsgId, msg.ExecId, msg.Path, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecGrepArgs:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecGrepError(msg.ExecMsgId, msg.ExecId, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecShellArgs, cursorproto.ServerMsgExecShellStream:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecShellRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecBgShellSpawn:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecBackgroundShellSpawnRejected(msg.ExecMsgId, msg.ExecId, msg.Command, msg.WorkingDirectory, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecFetchArgs:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecFetchError(msg.ExecMsgId, msg.ExecId, msg.Url, rejectReason)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecDiagnostics:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecDiagnosticsResult(msg.ExecMsgId, msg.ExecId)); errReply != nil {
						return errReply
					}
				case cursorproto.ServerMsgExecWriteShellStdin:
					if errReply := writeCursorReply(stream, cursorproto.EncodeExecWriteShellStdinError(msg.ExecMsgId, msg.ExecId, rejectReason)); errReply != nil {
						return errReply
					}
				}
			}

		case <-stream.Done():
			log.Debugf("cursor: processH2SessionFrames exiting: stream done")
			return stream.Err()
		}
	}
}

// --- OpenAI request parsing ---

type parsedOpenAIRequest struct {
	Model        string
	Messages     []gjson.Result
	Tools        []gjson.Result
	Stream       bool
	SystemPrompt string
	UserText     string
	Images       []cursorproto.ImageData
	Turns        []cursorproto.TurnData
	ToolResults  []toolResultInfo
}

type toolResultInfo struct {
	ToolCallId string
	Content    string
}

func parseOpenAIRequest(payload []byte) *parsedOpenAIRequest {
	p := &parsedOpenAIRequest{
		Model:  gjson.GetBytes(payload, "model").String(),
		Stream: gjson.GetBytes(payload, "stream").Bool(),
	}

	messages := gjson.GetBytes(payload, "messages").Array()
	p.Messages = messages

	// Extract system prompt
	var systemParts []string
	for _, msg := range messages {
		if msg.Get("role").String() == "system" {
			systemParts = append(systemParts, extractTextContent(msg.Get("content")))
		}
	}
	if len(systemParts) > 0 {
		p.SystemPrompt = strings.Join(systemParts, "\n")
	} else {
		p.SystemPrompt = "You are a helpful assistant."
	}

	// Extract turns, tool results, and last user message
	var pendingUser string
	for _, msg := range messages {
		role := msg.Get("role").String()
		switch role {
		case "system":
			continue
		case "tool":
			p.ToolResults = append(p.ToolResults, toolResultInfo{
				// Tool result IDs come from the client and must match the exact ID
				// emitted in the preceding assistant tool call. Re-encoding here
				// breaks resumed sessions and corrupts otherwise valid opaque IDs.
				ToolCallId: msg.Get("tool_call_id").String(),
				Content:    extractTextContent(msg.Get("content")),
			})
		case "user":
			if pendingUser != "" {
				p.Turns = append(p.Turns, cursorproto.TurnData{UserText: pendingUser})
			}
			pendingUser = extractTextContent(msg.Get("content"))
			p.Images = extractImages(msg.Get("content"))
		case "assistant":
			assistantText := extractTextContent(msg.Get("content"))
			if pendingUser != "" {
				p.Turns = append(p.Turns, cursorproto.TurnData{
					UserText:      pendingUser,
					AssistantText: assistantText,
				})
				pendingUser = ""
			} else if len(p.Turns) > 0 && assistantText != "" {
				// Assistant message after tool results (no pending user) —
				// append to the last turn's assistant text to preserve context.
				last := &p.Turns[len(p.Turns)-1]
				if last.AssistantText != "" {
					last.AssistantText += "\n" + assistantText
				} else {
					last.AssistantText = assistantText
				}
			}
		}
	}

	if pendingUser != "" {
		p.UserText = pendingUser
	} else if len(p.Turns) > 0 && len(p.ToolResults) == 0 {
		last := p.Turns[len(p.Turns)-1]
		p.Turns = p.Turns[:len(p.Turns)-1]
		p.UserText = last.UserText
	}

	// Extract tools
	p.Tools = gjson.GetBytes(payload, "tools").Array()

	return p
}

// bakeToolResultsIntoTurns merges tool results into the last turn's assistant text
// when there's no active H2 session to resume. This ensures the model sees the
// full tool interaction context in a new conversation.
// flattenConversationIntoUserText flattens the full conversation history
// (turns + tool results) into the UserText field as plain text.
// This is the fallback for cold resume when no checkpoint is available.
// Cursor reliably reads UserText but ignores structured turns.
func flattenConversationIntoUserText(parsed *parsedOpenAIRequest) {
	var buf strings.Builder

	// Render from the original messages so assistant tool calls and tool results
	// retain their exact chronological relationship. Cursor imposes no smaller
	// request limit here, so results remain complete.
	for _, message := range parsed.Messages {
		role := message.Get("role").String()
		switch role {
		case "system":
			continue
		case "user":
			writeCursorTranscriptEntry(&buf, "USER", extractTextContent(message.Get("content")))
		case "assistant":
			writeCursorTranscriptEntry(&buf, "ASSISTANT", extractTextContent(message.Get("content")))
			for _, toolCall := range message.Get("tool_calls").Array() {
				entry, _ := json.Marshal(map[string]string{
					"id":        toolCall.Get("id").String(),
					"name":      toolCall.Get("function.name").String(),
					"arguments": toolCall.Get("function.arguments").String(),
				})
				writeCursorTranscriptEntry(&buf, "ASSISTANT_TOOL_CALL", string(entry))
			}
		case "tool":
			entry, _ := json.Marshal(map[string]string{
				"tool_call_id": message.Get("tool_call_id").String(),
				"content":      extractTextContent(message.Get("content")),
			})
			writeCursorTranscriptEntry(&buf, "TOOL_RESULT", string(entry))
		}
	}

	if buf.Len() > 0 {
		buf.WriteString("The above is the previous conversation context including tool call results.\n")
		buf.WriteString("Continue your response based on this context.\n\n")
	}

	parsed.UserText = buf.String() + "Continue from the conversation above."

	// Clear turns and tool results since they're now in UserText
	parsed.Turns = nil
	parsed.ToolResults = nil
}

func writeCursorTranscriptEntry(buf *strings.Builder, label, content string) {
	if content == "" {
		return
	}
	buf.WriteString(label)
	buf.WriteString(": ")
	buf.WriteString(content)
	buf.WriteString("\n\n")
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				parts = append(parts, part.Get("text").String())
			}
		}
		return strings.Join(parts, "")
	}
	return content.String()
}

func extractImages(content gjson.Result) []cursorproto.ImageData {
	if !content.IsArray() {
		return nil
	}
	var images []cursorproto.ImageData
	for _, part := range content.Array() {
		if part.Get("type").String() == "image_url" {
			url := part.Get("image_url.url").String()
			if strings.HasPrefix(url, "data:") {
				img := parseDataURL(url)
				if img != nil {
					images = append(images, *img)
				}
			}
		}
	}
	return images
}

func parseDataURL(url string) *cursorproto.ImageData {
	// data:image/png;base64,...
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	parts := strings.SplitN(url[5:], ";", 2)
	if len(parts) != 2 {
		return nil
	}
	mimeType := parts[0]
	if !strings.HasPrefix(parts[1], "base64,") {
		return nil
	}
	encoded := parts[1][7:]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Try RawStdEncoding for unpadded base64
		data, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil
		}
	}
	return &cursorproto.ImageData{
		MimeType: mimeType,
		Data:     data,
	}
}

func buildRunRequestParams(parsed *parsedOpenAIRequest, conversationId, upstreamModel string) *cursorproto.RunRequestParams {
	// upstreamModel is the provider-resolved model name. Keep parsed.Model
	// unchanged so OpenAI-compatible responses continue to echo the client model.
	modelID := strings.TrimSpace(upstreamModel)
	if modelID == "" {
		modelID = parsed.Model
	}

	params := &cursorproto.RunRequestParams{
		ModelId:        modelID,
		SystemPrompt:   parsed.SystemPrompt,
		UserText:       parsed.UserText,
		MessageId:      uuid.New().String(),
		ConversationId: conversationId,
		Images:         parsed.Images,
		Turns:          parsed.Turns,
		BlobStore:      make(map[string][]byte),
	}

	// Convert OpenAI tools to McpToolDefs
	for _, tool := range parsed.Tools {
		fn := tool.Get("function")
		params.McpTools = append(params.McpTools, cursorproto.McpToolDef{
			Name:        fn.Get("name").String(),
			Description: fn.Get("description").String(),
			InputSchema: json.RawMessage(fn.Get("parameters").Raw),
		})
	}

	return params
}

// --- Helpers ---

func cursorAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		return v
	}
	return ""
}

func cursorRefreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["refresh_token"].(string); ok {
		return v
	}
	return ""
}

func applyCursorHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Te", "trailers")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Ghost-Mode", "true")
	req.Header.Set("X-Cursor-Client-Version", cursorClientVersion)
	req.Header.Set("X-Cursor-Client-Type", "cli")
	req.Header.Set("X-Request-Id", uuid.New().String())
}

func newH2Client() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{},
		},
	}
}

// extractCCH extracts the cch value from the system prompt's billing header.
func extractCCH(systemPrompt string) string {
	idx := strings.Index(systemPrompt, "cch=")
	if idx < 0 {
		return ""
	}
	rest := systemPrompt[idx+4:]
	end := strings.IndexAny(rest, "; \n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// extractClaudeCodeSessionId extracts session_id from Claude Code's metadata.user_id JSON.
// Format: {"metadata":{"user_id":"{\"session_id\":\"xxx\",\"device_id\":\"yyy\"}"}}
func extractClaudeCodeSessionId(payload []byte) string {
	userIdStr := gjson.GetBytes(payload, "metadata.user_id").String()
	if userIdStr == "" {
		return ""
	}
	// user_id is a JSON string that needs to be parsed again
	sid := gjson.Get(userIdStr, "session_id").String()
	return sid
}

// deriveConversationId generates a deterministic conversation_id.
// Priority: session_id (stable across resume) > system prompt hash (fallback).
func deriveConversationId(apiKey, sessionId, systemPrompt string) string {
	var input string
	if sessionId != "" {
		// Best: use Claude Code's session_id — stable even across resume
		input = "cursor-conv:" + apiKey + ":" + sessionId
	} else {
		// Fallback: use system prompt content minus volatile cch
		stable := systemPrompt
		if idx := strings.Index(stable, "cch="); idx >= 0 {
			end := strings.IndexAny(stable[idx:], "; \n")
			if end > 0 {
				stable = stable[:idx] + stable[idx+end:]
			}
		}
		if len(stable) > 500 {
			stable = stable[:500]
		}
		input = "cursor-conv:" + apiKey + ":" + stable
	}
	h := sha256.Sum256([]byte(input))
	s := hex.EncodeToString(h[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

func deriveSessionKey(clientKey string, model string, messages []gjson.Result) string {
	var firstUserContent string
	var systemContent string
	for _, msg := range messages {
		role := msg.Get("role").String()
		if role == "user" && firstUserContent == "" {
			firstUserContent = extractTextContent(msg.Get("content"))
		} else if role == "system" && systemContent == "" {
			// System prompt differs per Claude Code session (contains cwd, session_id, etc.)
			content := extractTextContent(msg.Get("content"))
			if len(content) > 200 {
				systemContent = content[:200]
			} else {
				systemContent = content
			}
		}
	}
	// Include client API key + system prompt hash to prevent session collisions:
	// - Different users have different API keys
	// - Different Claude Code sessions have different system prompts (cwd, tools, etc.)
	input := clientKey + ":" + model + ":" + systemContent + ":" + firstUserContent
	if len(input) > 500 {
		input = input[:500]
	}
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:16]
}

func sseChunk(id string, created int64, model string, delta string, finishReason string) cliproxyexecutor.StreamChunk {
	fr := "null"
	if finishReason != "" {
		fr = finishReason
	}
	// Note: the framework's WriteChunk adds "data: " prefix and "\n\n" suffix,
	// so we only output the raw JSON here.
	data := fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`,
		id, created, model, delta, fr)
	return cliproxyexecutor.StreamChunk{
		Payload: []byte(data),
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func normalizeToolCallID(id string) string {
	if id == "" {
		return ""
	}
	return "cursor_call_" + base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeMcpArgsToJSON(args map[string][]byte) string {
	if len(args) == 0 {
		return "{}"
	}
	result := make(map[string]interface{})
	for k, v := range args {
		// Try protobuf Value decoding first (matches TS: toJson(ValueSchema, fromBinary(ValueSchema, value)))
		if decoded, err := cursorproto.ProtobufValueBytesToJSON(v); err == nil {
			result[k] = decoded
		} else {
			// Fallback: try raw JSON
			var jsonVal interface{}
			if err := json.Unmarshal(v, &jsonVal); err == nil {
				result[k] = jsonVal
			} else {
				result[k] = string(v)
			}
		}
	}
	b, _ := json.Marshal(result)
	return string(b)
}

// --- Model Discovery ---

// cursorModelsCache stores the last successful model list for each auth ID.
// A transient models request failure must not replace a verified live catalog
// with the hardcoded cold-start fallback.
var (
	cursorModelsCacheMu sync.RWMutex
	cursorModelsCache   = make(map[string][]*registry.ModelInfo)
)

// cursorModelsOrFallback returns an independent copy of the last successful
// list for authID. The hardcoded fallback is used only before that auth has a
// successful live fetch.
func cursorModelsOrFallback(authID string) []*registry.ModelInfo {
	if authID != "" {
		cursorModelsCacheMu.RLock()
		cached, ok := cursorModelsCache[authID]
		if ok && len(cached) > 0 {
			models := cloneCursorModelInfos(cached)
			cursorModelsCacheMu.RUnlock()
			return models
		}
		cursorModelsCacheMu.RUnlock()
	}
	return GetCursorFallbackModels()
}

// cacheCursorModels records a complete, independent snapshot after a
// successful live fetch. Each success replaces the previous snapshot for the
// same auth ID.
func cacheCursorModels(authID string, models []*registry.ModelInfo) {
	if authID == "" || len(models) == 0 {
		return
	}

	snapshot := cloneCursorModelInfos(models)
	cursorModelsCacheMu.Lock()
	cursorModelsCache[authID] = snapshot
	cursorModelsCacheMu.Unlock()
}

func cloneCursorModelInfos(models []*registry.ModelInfo) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}

	cloned := make([]*registry.ModelInfo, len(models))
	for i, model := range models {
		cloned[i] = cloneCursorModelInfo(model)
	}
	return cloned
}

func cloneCursorModelInfo(model *registry.ModelInfo) *registry.ModelInfo {
	if model == nil {
		return nil
	}

	cloned := *model
	cloned.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	cloned.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	cloned.SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	cloned.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	cloned.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	if model.Thinking != nil {
		thinking := *model.Thinking
		thinking.Levels = append([]string(nil), model.Thinking.Levels...)
		cloned.Thinking = &thinking
	}
	if model.Config != nil {
		modelConfig := *model.Config
		if model.Config.OverrideHeader != nil {
			modelConfig.OverrideHeader = make(map[string]string, len(model.Config.OverrideHeader))
			for key, value := range model.Config.OverrideHeader {
				modelConfig.OverrideHeader[key] = value
			}
		}
		cloned.Config = &modelConfig
	}
	return &cloned
}

// FetchCursorModels retrieves available models from Cursor's API.
func FetchCursorModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	if auth == nil {
		return GetCursorFallbackModels()
	}

	authID := auth.ID
	accessToken := cursorAccessToken(auth)
	if accessToken == "" {
		return cursorModelsOrFallback(authID)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return fetchCursorModels(ctx, authID, accessToken, newH2Client(), cursorAPIURL+cursorModelsPath)
}

func fetchCursorModels(ctx context.Context, authID, accessToken string, client *http.Client, modelsURL string) []*registry.ModelInfo {
	// GetUsableModels is a unary RPC call (not streaming)
	// Send an empty protobuf request
	emptyReq := make([]byte, 0)

	h2Req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		modelsURL, bytes.NewReader(emptyReq))
	if err != nil {
		log.Debugf("cursor: failed to create models request: %v", err)
		return cursorModelsOrFallback(authID)
	}

	h2Req.Header.Set("Content-Type", "application/proto")
	h2Req.Header.Set("Te", "trailers")
	h2Req.Header.Set("Authorization", "Bearer "+accessToken)
	h2Req.Header.Set("X-Ghost-Mode", "true")
	h2Req.Header.Set("X-Cursor-Client-Version", cursorClientVersion)
	h2Req.Header.Set("X-Cursor-Client-Type", "cli")

	resp, err := client.Do(h2Req)
	if err != nil {
		log.Debugf("cursor: models request failed: %v", err)
		return cursorModelsOrFallback(authID)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("cursor: models request returned status %d", resp.StatusCode)
		return cursorModelsOrFallback(authID)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cursorModelsOrFallback(authID)
	}

	models := parseModelsResponse(body)
	if len(models) == 0 {
		return cursorModelsOrFallback(authID)
	}
	cacheCursorModels(authID, models)
	return models
}

func parseModelsResponse(data []byte) []*registry.ModelInfo {
	// Try stripping Connect framing first
	if len(data) >= cursorproto.ConnectFrameHeaderSize {
		_, payload, _, ok := cursorproto.ParseConnectFrame(data)
		if ok {
			data = payload
		}
	}

	// The response is a GetUsableModelsResponse protobuf.
	// We need to decode it manually - it contains a repeated "models" field.
	// Based on the TS code, the response has a `models` field (repeated) containing
	// model objects with modelId, displayName, thinkingDetails, etc.

	// For now, we'll try a simple decode approach
	var models []*registry.ModelInfo
	// Field 1 is likely "models" (repeated submessage)
	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		if typ == 2 { // BytesType (submessage)
			val, n := consumeBytes(data)
			if n < 0 {
				break
			}
			data = data[n:]

			if num == 1 { // models field
				if m := parseModelEntry(val); m != nil {
					models = append(models, m)
				}
			}
		} else {
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				break
			}
			data = data[n:]
		}
	}

	return models
}

func parseModelEntry(data []byte) *registry.ModelInfo {
	var modelId, displayName string
	var hasThinking bool

	for len(data) > 0 {
		num, typ, n := consumeTag(data)
		if n < 0 {
			break
		}
		data = data[n:]

		switch typ {
		case 2: // BytesType
			val, n := consumeBytes(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
			switch num {
			case 1: // modelId
				modelId = string(val)
			case 2: // thinkingDetails
				hasThinking = true
			case 3: // displayModelId (use as fallback)
				if displayName == "" {
					displayName = string(val)
				}
			case 4: // displayName
				displayName = string(val)
			case 5: // displayNameShort
				if displayName == "" {
					displayName = string(val)
				}
			}
		case 0: // VarintType
			_, n := consumeVarint(data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		default:
			n := consumeFieldValue(num, typ, data)
			if n < 0 {
				return nil
			}
			data = data[n:]
		}
	}

	if modelId == "" {
		return nil
	}
	if displayName == "" {
		displayName = modelId
	}

	info := &registry.ModelInfo{
		ID:                  modelId,
		Object:              "model",
		Created:             time.Now().Unix(),
		OwnedBy:             "cursor",
		Type:                cursorAuthType,
		DisplayName:         displayName,
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
	}
	if hasThinking {
		info.Thinking = &registry.ThinkingSupport{
			Max:            50000,
			DynamicAllowed: true,
		}
	}
	return info
}

// GetCursorFallbackModels returns hardcoded fallback models.
func GetCursorFallbackModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{ID: "composer-2", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Composer 2", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-4-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 4 Sonnet", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-3.5-sonnet", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Claude 3.5 Sonnet", ContextLength: 200000, MaxCompletionTokens: 8192},
		{ID: "gpt-4o", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "cursor-small", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Cursor Small", ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "cursor", Type: cursorAuthType, DisplayName: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 65536, Thinking: &registry.ThinkingSupport{Max: 50000, DynamicAllowed: true}},
	}
}

// Low-level protowire helpers (avoid importing protowire in executor)
func consumeTag(b []byte) (num int, typ int, n int) {
	v, n := consumeVarint(b)
	if n < 0 {
		return 0, 0, -1
	}
	return int(v >> 3), int(v & 7), n
}

func consumeVarint(b []byte) (uint64, int) {
	var val uint64
	for i := 0; i < len(b) && i < 10; i++ {
		val |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return val, i + 1
		}
	}
	return 0, -1
}

func consumeBytes(b []byte) ([]byte, int) {
	length, n := consumeVarint(b)
	if n < 0 || int(length) > len(b)-n {
		return nil, -1
	}
	return b[n : n+int(length)], n + int(length)
}

func consumeFieldValue(num, typ int, b []byte) int {
	switch typ {
	case 0: // Varint
		_, n := consumeVarint(b)
		return n
	case 1: // 64-bit
		if len(b) < 8 {
			return -1
		}
		return 8
	case 2: // Length-delimited
		_, n := consumeBytes(b)
		return n
	case 5: // 32-bit
		if len(b) < 4 {
			return -1
		}
		return 4
	default:
		return -1
	}
}
