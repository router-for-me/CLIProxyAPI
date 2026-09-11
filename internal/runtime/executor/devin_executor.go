package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DevinExecutor executes requests against the Codeium/Devin Connect-RPC backend.
type DevinExecutor struct {
	cfg *config.Config
}

// NewDevinExecutor creates a new Devin executor instance.
func NewDevinExecutor(cfg *config.Config) *DevinExecutor {
	return &DevinExecutor{
		cfg: cfg,
	}
}

// Identifier returns the unique provider key for Devin.
func (e *DevinExecutor) Identifier() string {
	return "devin"
}

// RequestToFormat specifies that Devin expects the interactions intermediate format.
func (e *DevinExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatInteractions
}

// PrepareRequest decorates the outgoing HTTP request with Devin Connect-RPC headers.
func (e *DevinExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _, _ := devinAuthCredentials(auth)
	if apiKey != "" {
		// Codeium/Devin Connect-RPC upstream expects "Basic <token>-<token>" as its wire authentication header.
		req.Header.Set("Authorization", "Basic "+apiKey+"-"+apiKey)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("User-Agent", "connect-go/1.19.1 (go1.25.0)")

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Devin credentials into the request and executes it.
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
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Refresh updates Devin user status, plan, and quota signals.
func (e *DevinExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, errors.New("devin executor: auth is nil")
	}

	sessionToken := strings.TrimSpace(auth.Attributes["api_key"])
	if sessionToken == "" {
		sessionToken = strings.TrimSpace(auth.Attributes["session_token"])
	}
	if sessionToken == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["api_key"].(string); ok {
			sessionToken = strings.TrimSpace(v)
		}
		if sessionToken == "" {
			if v, ok := auth.Metadata["session_token"].(string); ok {
				sessionToken = strings.TrimSpace(v)
			}
		}
	}
	if sessionToken == "" {
		return auth, nil
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	authService := devinauth.NewDevinAuthService(httpClient)
	if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
		authService.SetServerBaseURL(baseURL)
	} else if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" {
			authService.SetServerBaseURL(v)
		}
	}
	deviceSeed := strings.TrimSpace(auth.Attributes["device_seed"])
	if deviceSeed == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["device_seed"].(string); ok {
			deviceSeed = strings.TrimSpace(v)
		}
	}

	status, err := authService.FetchUserStatus(ctx, sessionToken, deviceSeed)
	if err != nil {
		log.Warnf("devin executor: failed to refresh user status for %s: %v", auth.ID, err)
		return auth, err
	}

	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	if updated.Attributes == nil {
		updated.Attributes = make(map[string]string)
	}

	if status.Email != "" {
		updated.Metadata["email"] = status.Email
		updated.Attributes["email"] = status.Email
	}
	if status.UserName != "" {
		updated.Metadata["user_name"] = status.UserName
		updated.Attributes["user_name"] = status.UserName
	}
	if status.UserID != "" {
		updated.Metadata["user_id"] = status.UserID
		updated.Attributes["user_id"] = status.UserID
	}
	if status.TeamID != "" {
		updated.Metadata["team_id"] = status.TeamID
		updated.Attributes["team_id"] = status.TeamID
	}
	if status.Plan != "" {
		updated.Metadata["plan"] = status.Plan
		updated.Attributes["plan"] = status.Plan
	}
	if status.OrgID != "" {
		updated.Metadata["org_id"] = status.OrgID
		updated.Attributes["org_id"] = status.OrgID
	}
	if status.OrgName != "" {
		updated.Metadata["org_name"] = status.OrgName
		updated.Attributes["org_name"] = status.OrgName
	}

	updated.Metadata["daily_quota_remaining_percent"] = status.DailyQuotaRemainingPercent
	updated.Metadata["weekly_quota_remaining_percent"] = status.WeeklyQuotaRemainingPercent
	if !status.DailyQuotaResetAt.IsZero() {
		updated.Metadata["daily_quota_reset_at"] = status.DailyQuotaResetAt.Format(time.RFC3339)
	}
	if !status.WeeklyQuotaResetAt.IsZero() {
		updated.Metadata["weekly_quota_reset_at"] = status.WeeklyQuotaResetAt.Format(time.RFC3339)
	}
	if !status.PlanStart.IsZero() {
		updated.Metadata["plan_start"] = status.PlanStart.Format(time.RFC3339)
	}
	if !status.PlanEnd.IsZero() {
		updated.Metadata["plan_end"] = status.PlanEnd.Format(time.RFC3339)
	}

	// Quota observation signals for management UI and conductor
	if updated.Quota.Signals == nil {
		updated.Quota.Signals = make(map[string]string)
	}
	if status.Plan != "" {
		updated.Quota.Signals["plan"] = status.Plan
	}
	updated.Quota.Signals["daily_quota_remaining_percent"] = fmt.Sprintf("%d%%", status.DailyQuotaRemainingPercent)
	updated.Quota.Signals["weekly_quota_remaining_percent"] = fmt.Sprintf("%d%%", status.WeeklyQuotaRemainingPercent)
	if !status.DailyQuotaResetAt.IsZero() {
		updated.Quota.Signals["daily_quota_reset_at"] = status.DailyQuotaResetAt.Format(time.RFC3339)
	}
	if !status.WeeklyQuotaResetAt.IsZero() {
		updated.Quota.Signals["weekly_quota_reset_at"] = status.WeeklyQuotaResetAt.Format(time.RFC3339)
	}
	updated.Quota.ObservedAt = time.Now()
	updated.LastRefreshedAt = time.Now()

	return updated, nil
}

// CountTokens provides token counting for Devin requests.
func (e *DevinExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Devin has no standalone token count endpoint; estimate via length heuristic.
	promptTokens := int64(len(req.Payload) / 4)
	out := []byte(fmt.Sprintf(`{"total_tokens":%d,"input_tokens":%d}`, promptTokens, promptTokens))
	return cliproxyexecutor.Response{Payload: out}, nil
}

// Execute performs non-streaming execution against the Devin backend.
func (e *DevinExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	targetModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, targetModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, chatModelUID, errPrep := e.prepareDevinHTTPRequest(ctx, auth, req, opts)
	if errPrep != nil {
		return resp, errPrep
	}

	authID, authLabel, authType, authValue := devinAuthLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       httpReq.URL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      []byte(fmt.Sprintf("[connect-proto GetChatMessage: model=%s]", chatModelUID)),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errData, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, errData)
		return resp, newDevinStatusError(httpResp.StatusCode, httpResp.Header, errData)
	}

	interactionsJSON, errConsume := consumeDevinFramesToInteractions(httpResp.Body, req.Model, chatModelUID)
	if errConsume != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errConsume)
		return resp, errConsume
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, interactionsJSON)

	reporter.Publish(ctx, helps.ParseInteractionsUsage(interactionsJSON))

	targetFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatInteractions, targetFormat, req.Model, opts.OriginalRequest, req.Payload, interactionsJSON, &param)
	if targetFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream performs streaming execution against the Devin backend.
func (e *DevinExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	targetModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, targetModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, chatModelUID, errPrep := e.prepareDevinHTTPRequest(ctx, auth, req, opts)
	if errPrep != nil {
		return nil, errPrep
	}

	authID, authLabel, authType, authValue := devinAuthLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       httpReq.URL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      []byte(fmt.Sprintf("[connect-proto GetChatMessage: model=%s]", chatModelUID)),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		errData, _ := io.ReadAll(httpResp.Body)
		_ = httpResp.Body.Close()
		helps.AppendAPIResponseChunk(ctx, e.cfg, errData)
		return nil, statusErr{code: httpResp.StatusCode, msg: string(errData)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)

	streamCtx, cancelStream := context.WithCancel(ctx)

	go func() {
		defer close(out)
		defer cancelStream()
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("devin executor: close stream body error: %v", errClose)
			}
		}()

		e.streamDevinFrames(streamCtx, httpResp.Body, req, opts, chatModelUID, responseFormat, reporter, out)
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header.Clone(),
		Chunks:  out,
	}, nil
}

func (e *DevinExecutor) prepareDevinHTTPRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*http.Request, string, error) {
	apiKey, baseURL, deviceSeed := devinAuthCredentials(auth)
	if apiKey == "" {
		return nil, "", fmt.Errorf("devin credentials missing: api_key or session_token required")
	}
	if deviceSeed == "" {
		deviceSeed = apiKey
	}

	payload := req.Payload
	if opts.SourceFormat != "" && opts.SourceFormat != sdktranslator.FormatInteractions {
		payload = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatInteractions, req.Model, payload, opts.Stream)
	}
	systemPrompt, prompts, tools, temp, maxTokens, sessionID, cascadeID, thinkingLevel, budgetTokens := parseInteractionsPayload(payload, opts.OriginalRequest)
	sessionID, cascadeID = resolveDevinSessionAndCascadeIDs(ctx, sessionID, cascadeID, opts)

	chatModelUID := helps.ResolveDevinChatModelUID(req.Model, thinkingLevel, budgetTokens)

	sensitiveWords := e.getSensitiveWords()
	var matcher *helps.SensitiveWordMatcher
	if len(sensitiveWords) > 0 {
		matcher = helps.BuildSensitiveWordMatcher(sensitiveWords)
	}

	protoBytes := helps.BuildDevinGetChatMessageRequest(
		apiKey,
		deviceSeed,
		chatModelUID,
		systemPrompt,
		prompts,
		tools,
		temp,
		maxTokens,
		sessionID,
		cascadeID,
		matcher,
	)

	framed := helps.WrapConnectEnvelope(protoBytes)
	url := strings.TrimRight(baseURL, "/") + helps.DevinChatPath

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(framed))
	if err != nil {
		return nil, "", err
	}

	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, "", err
	}

	return httpReq, chatModelUID, nil
}

func (e *DevinExecutor) streamDevinFrames(
	ctx context.Context,
	body io.Reader,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
	chatModelUID string,
	responseFormat sdktranslator.Format,
	reporter *helps.UsageReporter,
	out chan<- cliproxyexecutor.StreamChunk,
) {
	interactionID := fmt.Sprintf("interaction_%s", uuid.New().String()[:12])
	stepIndex := 0
	thoughtStarted := false
	contentStarted := false
	currentToolCallActive := false
	thinkingBuf := &helps.UTF8SplitBuffer{}
	contentBuf := &helps.UTF8SplitBuffer{}
	var finalUsage *helps.DevinUsage
	var accumulatedSignature []byte
	var signatureType string

	claudeInputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, sdktranslator.FormatInteractions, responseFormat, opts.OriginalRequest)
	var translateParam any

	emitInteractionsEvent := func(rawJSON []byte) bool {
		if len(rawJSON) == 0 {
			return true
		}
		trimmed := bytes.TrimSpace(rawJSON)
		helps.AppendAPIResponseChunk(ctx, e.cfg, append(append([]byte{}, trimmed...), '\n'))

		if responseFormat == sdktranslator.FormatInteractions {
			frame := []byte(fmt.Sprintf("data: %s\n\n", string(trimmed)))
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: frame}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		lines := helps.TranslateStreamWithClaudeInputTokens(
			ctx,
			sdktranslator.FormatInteractions,
			responseFormat,
			req.Model,
			opts.OriginalRequest,
			req.Payload,
			trimmed,
			&translateParam,
			claudeInputTokens,
		)
		for _, line := range lines {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: line}:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}

	// 1. Send initial interaction.created event
	createdEvent, _ := sjson.SetBytes([]byte(`{"event_type":"interaction.created","interaction":{"id":"","model":""}}`), "interaction.id", interactionID)
	createdEvent, _ = sjson.SetBytes(createdEvent, "interaction.model", req.Model)
	if !emitInteractionsEvent(createdEvent) {
		return
	}

	thoughtStepIndex := -1

	// 2. Consume streaming Connect-proto frames
	for {
		flag, payload, errRead := helps.ReadConnectFrame(body)
		if errRead != nil {
			if errors.Is(errRead, io.EOF) || errors.Is(errRead, io.ErrUnexpectedEOF) {
				break
			}
			log.Debugf("devin executor: read connect frame error: %v", errRead)
			break
		}

		// EOS Trailer
		if flag&helps.ConnectFlagEndStream != 0 {
			code, errTrailer := helps.ParseDevinTrailerError(payload)
			if errTrailer != nil {
				log.Warnf("devin executor: trailer error (%d): %v", code, errTrailer)
				failedEvent, _ := sjson.SetBytes([]byte(`{"event_type":"response.failed","error":{"message":"","code":""}}`), "error.message", errTrailer.Error())
				failedEvent, _ = sjson.SetBytes(failedEvent, "error.code", fmt.Sprintf("%d", code))
				_ = emitInteractionsEvent(failedEvent)
				return
			}
			break
		}

		frameRes, errParse := helps.ParseDevinFrame(payload)
		if errParse != nil {
			log.Debugf("devin executor: parse frame error: %v", errParse)
			continue
		}

		if frameRes.Usage != nil {
			finalUsage = frameRes.Usage
		}
		if len(frameRes.DeltaSignature) > 0 {
			accumulatedSignature = append(accumulatedSignature, frameRes.DeltaSignature...)
		}
		if frameRes.DeltaSignatureType != "" {
			signatureType = frameRes.DeltaSignatureType
		}

		// Emit thinking delta
		if frameRes.ThinkingText != "" {
			chunk := thinkingBuf.Feed([]byte(frameRes.ThinkingText))
			if chunk != "" {
				if !thoughtStarted {
					thoughtStepIndex = stepIndex
					startEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.start","index":0,"step":{"type":"thought"}}`), "index", stepIndex)
					if !emitInteractionsEvent(startEvent) {
						return
					}
					thoughtStarted = true
				}
				deltaEvent := []byte(`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","text":"","content":{"type":"text","text":""}}}`)
				deltaEvent, _ = sjson.SetBytes(deltaEvent, "index", thoughtStepIndex)
				deltaEvent, _ = sjson.SetBytes(deltaEvent, "delta.text", chunk)
				deltaEvent, _ = sjson.SetBytes(deltaEvent, "delta.content.text", chunk)
				if !emitInteractionsEvent(deltaEvent) {
					return
				}
			}
		}

		// Emit thinking signature delta targeting the thought step
		if len(frameRes.DeltaSignature) > 0 {
			targetIdx := thoughtStepIndex
			if targetIdx < 0 {
				targetIdx = 0
			}
			sigEvent := []byte(`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":""}}`)
			sigEvent, _ = sjson.SetBytes(sigEvent, "index", targetIdx)
			sigEvent, _ = sjson.SetBytes(sigEvent, "delta.signature", string(frameRes.DeltaSignature))
			_ = emitInteractionsEvent(sigEvent)
		}

		// Emit content text delta
		if frameRes.ContentText != "" {
			chunk := contentBuf.Feed([]byte(frameRes.ContentText))
			if chunk != "" {
				if thoughtStarted {
					stopEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.stop","index":0}`), "index", stepIndex)
					if !emitInteractionsEvent(stopEvent) {
						return
					}
					thoughtStarted = false
					stepIndex++
				}
				if !contentStarted {
					startEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`), "index", stepIndex)
					if !emitInteractionsEvent(startEvent) {
						return
					}
					contentStarted = true
				}
				deltaEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":""}}`), "index", stepIndex)
				deltaEvent, _ = sjson.SetBytes(deltaEvent, "delta.text", chunk)
				if !emitInteractionsEvent(deltaEvent) {
					return
				}
			}
		}

		// Emit tool call deltas
		for _, tc := range frameRes.ToolCallDeltas {
			if thoughtStarted {
				stopEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.stop","index":0}`), "index", stepIndex)
				_ = emitInteractionsEvent(stopEvent)
				thoughtStarted = false
				stepIndex++
			}
			if contentStarted {
				stopEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.stop","index":0}`), "index", stepIndex)
				_ = emitInteractionsEvent(stopEvent)
				contentStarted = false
				stepIndex++
			}

			if tc.Name != "" || tc.ID != "" {
				if currentToolCallActive {
					stopEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.stop","index":0}`), "index", stepIndex)
					_ = emitInteractionsEvent(stopEvent)
					stepIndex++
				}
				startEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.start","index":0,"step":{"type":"function_call","name":"","id":"","call_id":"","arguments":{}}}`), "index", stepIndex)
				startEvent, _ = sjson.SetBytes(startEvent, "step.name", tc.Name)
				startEvent, _ = sjson.SetBytes(startEvent, "step.id", tc.ID)
				startEvent, _ = sjson.SetBytes(startEvent, "step.call_id", tc.ID)
				if !emitInteractionsEvent(startEvent) {
					return
				}
				currentToolCallActive = true
			}
			if tc.Arguments != "" {
				deltaEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":""}}`), "index", stepIndex)
				deltaEvent, _ = sjson.SetBytes(deltaEvent, "delta.arguments", tc.Arguments)
				if !emitInteractionsEvent(deltaEvent) {
					return
				}
			}
		}
	}

	// 3. Emit accumulated signature if present
	if len(accumulatedSignature) > 0 {
		targetIdx := thoughtStepIndex
		if targetIdx < 0 {
			targetIdx = 0
		}
		sigBase64 := base64.StdEncoding.EncodeToString(accumulatedSignature)
		sigEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":""}}`), "index", targetIdx)
		sigEvent, _ = sjson.SetBytes(sigEvent, "delta.signature", sigBase64)
		if signatureType != "" {
			sigEvent, _ = sjson.SetBytes(sigEvent, "delta.signature_type", signatureType)
		}
		_ = emitInteractionsEvent(sigEvent)
	}

	// 4. Close open steps
	if thoughtStarted || contentStarted || currentToolCallActive {
		stopEvent, _ := sjson.SetBytes([]byte(`{"event_type":"step.stop","index":0}`), "index", stepIndex)
		_ = emitInteractionsEvent(stopEvent)
	}

	// 5. Emit interaction.completed with final usage
	completedEvent := []byte(`{"event_type":"interaction.completed","interaction":{"id":"","model":"","status":"completed","usage":{"total_input_tokens":0,"total_output_tokens":0,"total_cached_tokens":0}}}`)
	completedEvent, _ = sjson.SetBytes(completedEvent, "interaction.id", interactionID)
	completedEvent, _ = sjson.SetBytes(completedEvent, "interaction.model", req.Model)
	if finalUsage != nil {
		completedEvent, _ = sjson.SetBytes(completedEvent, "interaction.usage.total_input_tokens", finalUsage.PromptTokens)
		completedEvent, _ = sjson.SetBytes(completedEvent, "interaction.usage.total_output_tokens", finalUsage.CompletionTokens)
		completedEvent, _ = sjson.SetBytes(completedEvent, "interaction.usage.total_cached_tokens", finalUsage.CachedTokens)
		if detail, ok := helps.ParseInteractionsStreamUsage(completedEvent); ok {
			reporter.Publish(ctx, detail)
		}
	}
	_ = emitInteractionsEvent(completedEvent)

	// 6. Terminate stream with [DONE]
	if responseFormat == sdktranslator.FormatInteractions {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}:
		case <-ctx.Done():
		}
	} else {
		lines := helps.TranslateStreamWithClaudeInputTokens(
			ctx,
			sdktranslator.FormatInteractions,
			responseFormat,
			req.Model,
			opts.OriginalRequest,
			req.Payload,
			[]byte("[DONE]"),
			&translateParam,
			claudeInputTokens,
		)
		for _, line := range lines {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: line}:
			case <-ctx.Done():
			}
		}
	}
}

func consumeDevinFramesToInteractions(body io.Reader, model, chatModelUID string) ([]byte, error) {
	interactionID := fmt.Sprintf("interaction_%s", uuid.New().String()[:12])
	var textParts []string
	var thinkingParts []string
	var toolCalls []helps.DevinToolCall
	var finalUsage *helps.DevinUsage
	var accumulatedSignature []byte

	for {
		flag, payload, errRead := helps.ReadConnectFrame(body)
		if errRead != nil {
			if errors.Is(errRead, io.EOF) || errors.Is(errRead, io.ErrUnexpectedEOF) {
				break
			}
			return nil, errRead
		}

		if flag&helps.ConnectFlagEndStream != 0 {
			code, errTrailer := helps.ParseDevinTrailerError(payload)
			if errTrailer != nil {
				return nil, statusErr{code: code, msg: errTrailer.Error()}
			}
			break
		}

		frameRes, errParse := helps.ParseDevinFrame(payload)
		if errParse != nil {
			continue
		}

		if frameRes.Usage != nil {
			finalUsage = frameRes.Usage
		}
		if len(frameRes.DeltaSignature) > 0 {
			accumulatedSignature = append(accumulatedSignature, frameRes.DeltaSignature...)
		}
		if frameRes.ThinkingText != "" {
			thinkingParts = append(thinkingParts, frameRes.ThinkingText)
		}
		if frameRes.ContentText != "" {
			textParts = append(textParts, frameRes.ContentText)
		}
		for _, tc := range frameRes.ToolCallDeltas {
			if tc.Name != "" || tc.ID != "" {
				toolCalls = append(toolCalls, helps.DevinToolCall{
					ID:        tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
			} else if len(toolCalls) > 0 {
				toolCalls[len(toolCalls)-1].Arguments += tc.Arguments
			}
		}
	}

	out := []byte(`{"id":"","model":"","status":"completed","steps":[],"usage":{"total_input_tokens":0,"total_output_tokens":0,"total_cached_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", interactionID)
	out, _ = sjson.SetBytes(out, "model", model)

	var steps [][]byte

	if len(thinkingParts) > 0 {
		thoughtStep := []byte(`{"type":"thought","content":[{"type":"text","text":""}]}`)
		thoughtStep, _ = sjson.SetBytes(thoughtStep, "content.0.text", strings.Join(thinkingParts, ""))
		if len(accumulatedSignature) > 0 {
			sigStr := string(accumulatedSignature)
			thoughtStep, _ = sjson.SetBytes(thoughtStep, "signature", sigStr)
			thoughtStep, _ = sjson.SetBytes(thoughtStep, "thought_signature", sigStr)
		}
		steps = append(steps, thoughtStep)
	}

	if len(textParts) > 0 {
		modelStep := []byte(`{"type":"model_output","content":[{"type":"text","text":""}]}`)
		modelStep, _ = sjson.SetBytes(modelStep, "content.0.text", strings.Join(textParts, ""))
		steps = append(steps, modelStep)
	}

	for _, tc := range toolCalls {
		fnStep := []byte(`{"type":"function_call","name":"","id":"","call_id":"","arguments":{}}`)
		fnStep, _ = sjson.SetBytes(fnStep, "name", tc.Name)
		fnStep, _ = sjson.SetBytes(fnStep, "id", tc.ID)
		fnStep, _ = sjson.SetBytes(fnStep, "call_id", tc.ID)
		if tc.Arguments != "" && json.Valid([]byte(tc.Arguments)) {
			fnStep, _ = sjson.SetRawBytes(fnStep, "arguments", []byte(tc.Arguments))
		}
		steps = append(steps, fnStep)
	}

	if len(steps) > 0 {
		var stepsRaw strings.Builder
		stepsRaw.WriteString("[")
		for i, s := range steps {
			if i > 0 {
				stepsRaw.WriteString(",")
			}
			stepsRaw.Write(s)
		}
		stepsRaw.WriteString("]")
		out, _ = sjson.SetRawBytes(out, "steps", []byte(stepsRaw.String()))
	}

	if finalUsage != nil {
		out, _ = sjson.SetBytes(out, "usage.total_input_tokens", finalUsage.PromptTokens)
		out, _ = sjson.SetBytes(out, "usage.total_output_tokens", finalUsage.CompletionTokens)
		out, _ = sjson.SetBytes(out, "usage.total_cached_tokens", finalUsage.CachedTokens)
	}

	return out, nil
}

func parseInteractionsPayload(payload, originalRequest []byte) (
	systemPrompt string,
	prompts []helps.DevinPrompt,
	tools []helps.DevinTool,
	temperature *float64,
	maxTokens int,
	sessionID string,
	cascadeID string,
	thinkingLevel string,
	budgetTokens int,
) {
	root := gjson.ParseBytes(payload)

	// 1. System prompt
	systemPrompt = strings.TrimSpace(root.Get("system_instruction").String())
	if systemPrompt == "" {
		systemPrompt = strings.TrimSpace(root.Get("systemInstruction").String())
	}

	// 2. Generation config
	genCfg := root.Get("generation_config")
	if !genCfg.Exists() {
		genCfg = root.Get("generationConfig")
	}
	if genCfg.Exists() {
		if t := genCfg.Get("temperature"); t.Exists() {
			val := t.Float()
			temperature = &val
		}
		maxTokens = int(genCfg.Get("max_output_tokens").Int())
		thinkingLevel = genCfg.Get("thinking_level").String()
		budgetTokens = int(genCfg.Get("thinking_config.thinking_budget").Int())
	}
	if temperature == nil {
		if origRoot := gjson.ParseBytes(originalRequest); origRoot.Get("temperature").Exists() {
			val := origRoot.Get("temperature").Float()
			temperature = &val
		} else if root.Get("temperature").Exists() {
			val := root.Get("temperature").Float()
			temperature = &val
		}
	}
	if maxTokens <= 0 {
		maxTokens = helps.DevinDefaultMaxTokens
	}

	// 3. Session and Cascade ID
	sessionID = strings.TrimSpace(firstNonEmpty(
		root.Get("previous_interaction_id").String(),
		root.Get("session_id").String(),
		root.Get("sessionId").String(),
		root.Get("conversation_id").String(),
	))
	if sessionID == "" && len(originalRequest) > 0 {
		origRoot := gjson.ParseBytes(originalRequest)
		sessionID = strings.TrimSpace(firstNonEmpty(
			origRoot.Get("previous_interaction_id").String(),
			origRoot.Get("session_id").String(),
			origRoot.Get("sessionId").String(),
			origRoot.Get("conversation_id").String(),
		))
	}
	cascadeID = sessionID

	// 4. Repeated History Prompts
	inputRes := root.Get("input")
	if inputRes.IsArray() {
		for _, step := range inputRes.Array() {
			stepType := strings.ToLower(strings.TrimSpace(step.Get("type").String()))
			switch stepType {
			case "user_input":
				text, images := extractInteractionsStepContent(step)
				prompts = append(prompts, helps.DevinPrompt{
					MessageID: uuid.New().String(),
					Source:    1,
					Content:   text,
					Images:    images,
				})

			case "model_output":
				text := extractInteractionsStepText(step)
				sigStr := firstNonEmpty(step.Get("signature").String(), step.Get("thought_signature").String())
				sigBytes, sigType := parseSignatureBytes(sigStr)
				if len(prompts) > 0 && prompts[len(prompts)-1].Source == 2 {
					if prompts[len(prompts)-1].Content != "" {
						prompts[len(prompts)-1].Content += "\n" + text
					} else {
						prompts[len(prompts)-1].Content = text
					}
					if len(sigBytes) > 0 && len(prompts[len(prompts)-1].Signature) == 0 {
						prompts[len(prompts)-1].Signature = sigBytes
						prompts[len(prompts)-1].SignatureType = sigType
					}
				} else {
					prompts = append(prompts, helps.DevinPrompt{
						MessageID:     uuid.New().String(),
						Source:        2,
						Content:       text,
						Signature:     sigBytes,
						SignatureType: sigType,
					})
				}

			case "thought":
				// If previous prompt was assistant, attach thinking; otherwise append assistant prompt
				text := extractInteractionsStepText(step)
				sigStr := firstNonEmpty(step.Get("signature").String(), step.Get("thought_signature").String())
				sigBytes, sigType := parseSignatureBytes(sigStr)
				if len(prompts) > 0 && prompts[len(prompts)-1].Source == 2 {
					if prompts[len(prompts)-1].Thinking != "" {
						prompts[len(prompts)-1].Thinking += "\n\n" + text
					} else {
						prompts[len(prompts)-1].Thinking = text
					}
					if len(sigBytes) > 0 && len(prompts[len(prompts)-1].Signature) == 0 {
						prompts[len(prompts)-1].Signature = sigBytes
						prompts[len(prompts)-1].SignatureType = sigType
					}
				} else {
					prompts = append(prompts, helps.DevinPrompt{
						MessageID:     uuid.New().String(),
						Source:        2,
						Thinking:      text,
						Signature:     sigBytes,
						SignatureType: sigType,
					})
				}

			case "function_call":
				name := step.Get("name").String()
				id := firstNonEmpty(step.Get("id").String(), step.Get("call_id").String())
				args := step.Get("arguments").Raw
				tc := helps.DevinToolCall{ID: id, Name: name, Arguments: args}
				if len(prompts) > 0 && prompts[len(prompts)-1].Source == 2 {
					prompts[len(prompts)-1].ToolCalls = append(prompts[len(prompts)-1].ToolCalls, tc)
				} else {
					prompts = append(prompts, helps.DevinPrompt{
						MessageID: uuid.New().String(),
						Source:    2,
						ToolCalls: []helps.DevinToolCall{tc},
					})
				}

			case "function_result":
				id := firstNonEmpty(step.Get("id").String(), step.Get("call_id").String())
				resText := firstNonEmpty(
					step.Get("result").String(),
					step.Get("output").String(),
					step.Get("content").String(),
				)
				if resText == "" {
					if r := step.Get("result"); r.Exists() {
						resText = r.Raw
					} else if o := step.Get("output"); o.Exists() {
						resText = o.Raw
					} else if c := step.Get("content"); c.Exists() {
						resText = c.Raw
					}
				}
				prompts = append(prompts, helps.DevinPrompt{
					MessageID:  uuid.New().String(),
					Source:     4,
					ToolCallID: id,
					Content:    resText,
				})
			}
		}
	} else if messagesRes := root.Get("messages"); messagesRes.IsArray() {
		// Fallback for direct OpenAI format if not translated
		for _, m := range messagesRes.Array() {
			role := strings.ToLower(strings.TrimSpace(m.Get("role").String()))
			switch role {
			case "system", "developer":
				if systemPrompt == "" {
					systemPrompt = m.Get("content").String()
				}
			case "user":
				text, images := extractInteractionsStepContent(m)
				prompts = append(prompts, helps.DevinPrompt{
					MessageID: uuid.New().String(),
					Source:    1,
					Content:   text,
					Images:    images,
				})
			case "assistant":
				text := extractInteractionsStepText(m)
				prompts = append(prompts, helps.DevinPrompt{
					MessageID: uuid.New().String(),
					Source:    2,
					Content:   text,
				})
			case "tool":
				id := firstNonEmpty(m.Get("tool_call_id").String(), m.Get("id").String())
				resText := firstNonEmpty(
					m.Get("content").String(),
					m.Get("output").String(),
					m.Get("result").String(),
				)
				if resText == "" {
					if c := m.Get("content"); c.Exists() {
						resText = c.Raw
					}
				}
				prompts = append(prompts, helps.DevinPrompt{
					MessageID:  uuid.New().String(),
					Source:     4,
					ToolCallID: id,
					Content:    resText,
				})
			}
		}
	}

	// 5. Bypass from OriginalRequest if signatures, reasoning, or images were lost in translator
	if len(originalRequest) > 0 {
		supplementSignaturesFromOriginal(originalRequest, prompts)
		supplementImagesFromOriginal(originalRequest, prompts)
	}

	// 6. Tools
	toolsRes := root.Get("tools")
	if toolsRes.IsArray() {
		for _, t := range toolsRes.Array() {
			name := t.Get("name").String()
			desc := t.Get("description").String()
			params := t.Get("parameters").Raw
			tools = append(tools, helps.DevinTool{
				Name:        name,
				Description: desc,
				Parameters:  []byte(params),
			})
		}
	}

	return
}

func parseDataURL(raw string) (mimeType string, data string, ok bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "data:") {
		return "", "", false
	}
	idxComma := strings.Index(raw, ",")
	if idxComma < 0 {
		return "", "", false
	}
	header := raw[5:idxComma]
	data = raw[idxComma+1:]
	parts := strings.Split(header, ";")
	mimeType = strings.TrimSpace(parts[0])
	if mimeType == "" {
		mimeType = "image/png"
	}
	return mimeType, data, true
}

func mimeExtension(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return "png"
	}
}

func extractInteractionsStepContent(step gjson.Result) (string, []helps.DevinImage) {
	content := step.Get("content")
	var textParts []string
	var images []helps.DevinImage

	extractFromPart := func(p gjson.Result) {
		partType := strings.ToLower(strings.TrimSpace(p.Get("type").String()))
		switch partType {
		case "text":
			if t := p.Get("text").String(); t != "" {
				textParts = append(textParts, t)
			}
		case "image", "input_image", "image_url":
			base64Data := strings.TrimSpace(p.Get("data").String())
			mimeType := strings.TrimSpace(p.Get("mime_type").String())

			if base64Data == "" {
				base64Data = strings.TrimSpace(p.Get("source.data").String())
				if mimeType == "" {
					mimeType = strings.TrimSpace(p.Get("source.media_type").String())
				}
			}

			if base64Data == "" {
				url := firstNonEmpty(p.Get("image_url.url").String(), p.Get("image_url").String(), p.Get("url").String())
				if m, d, ok := parseDataURL(url); ok {
					base64Data = d
					if mimeType == "" {
						mimeType = m
					}
				}
			}

			if base64Data == "" {
				base64Data = strings.TrimSpace(p.Get("inline_data.data").String())
				if mimeType == "" {
					mimeType = strings.TrimSpace(p.Get("inline_data.mime_type").String())
				}
			}

			if base64Data != "" {
				if mimeType == "" {
					mimeType = "image/png"
				}
				images = append(images, helps.DevinImage{
					Base64Data: base64Data,
					MimeType:   mimeType,
				})
			}
		default:
			if t := p.Get("text").String(); t != "" {
				textParts = append(textParts, t)
			}
		}
	}

	if content.Type == gjson.String {
		textParts = append(textParts, content.String())
	} else if content.IsArray() {
		for _, p := range content.Array() {
			extractFromPart(p)
		}
	} else if step.Get("text").Exists() {
		textParts = append(textParts, step.Get("text").String())
	}

	text := strings.Join(textParts, "\n")

	if len(images) > 0 && !strings.Contains(text, "[Image ") {
		var imgHeaders []string
		for i, img := range images {
			ext := mimeExtension(img.MimeType)
			imgHeaders = append(imgHeaders, fmt.Sprintf("[Image %d: pasted_image_%d.%s]", i+1, i+1, ext))
		}
		header := strings.Join(imgHeaders, "\n")
		if text != "" {
			text = header + "\n\n" + text
		} else {
			text = header
		}
	}

	return text, images
}

func extractInteractionsStepText(step gjson.Result) string {
	content := step.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var parts []string
		for _, p := range content.Array() {
			if t := p.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return step.Get("text").String()
}

func supplementImagesFromOriginal(original []byte, prompts []helps.DevinPrompt) {
	origRoot := gjson.ParseBytes(original)
	messages := origRoot.Get("messages")
	if !messages.IsArray() {
		return
	}

	var userImages [][]helps.DevinImage
	for _, m := range messages.Array() {
		if strings.EqualFold(m.Get("role").String(), "user") {
			var imgs []helps.DevinImage
			content := m.Get("content")
			if content.IsArray() {
				for _, part := range content.Array() {
					partType := strings.ToLower(part.Get("type").String())
					if partType == "image" || partType == "image_url" || partType == "input_image" {
						data := strings.TrimSpace(part.Get("source.data").String())
						mime := strings.TrimSpace(part.Get("source.media_type").String())
						if data == "" {
							url := firstNonEmpty(part.Get("image_url.url").String(), part.Get("image_url").String(), part.Get("url").String())
							if m, d, ok := parseDataURL(url); ok {
								data = d
								mime = m
							}
						}
						if data != "" {
							if mime == "" {
								mime = "image/png"
							}
							imgs = append(imgs, helps.DevinImage{Base64Data: data, MimeType: mime})
						}
					}
				}
			}
			userImages = append(userImages, imgs)
		}
	}

	userPromptIdx := 0
	for i := range prompts {
		if prompts[i].Source == 1 {
			if len(prompts[i].Images) == 0 && userPromptIdx < len(userImages) && len(userImages[userPromptIdx]) > 0 {
				prompts[i].Images = userImages[userPromptIdx]
				if !strings.Contains(prompts[i].Content, "[Image ") {
					var imgHeaders []string
					for imgIdx, img := range prompts[i].Images {
						imgHeaders = append(imgHeaders, fmt.Sprintf("[Image %d: pasted_image_%d.%s]", imgIdx+1, imgIdx+1, mimeExtension(img.MimeType)))
					}
					header := strings.Join(imgHeaders, "\n")
					if prompts[i].Content != "" {
						prompts[i].Content = header + "\n\n" + prompts[i].Content
					} else {
						prompts[i].Content = header
					}
				}
			}
			userPromptIdx++
		}
	}
}

func parseSignatureBytes(sigStr string) ([]byte, string) {
	s := strings.TrimSpace(sigStr)
	if s == "" {
		return nil, ""
	}
	if strings.HasPrefix(s, "sealed.v1.") {
		return []byte(s), "sealed"
	}
	if strings.HasPrefix(s, "AY") {
		return []byte(s), "gemini"
	}
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil && len(decoded) > 0 {
		decStr := string(decoded)
		if strings.HasPrefix(decStr, "sealed.v1.") {
			return decoded, "sealed"
		}
		if strings.HasPrefix(decStr, "CAQS") || strings.HasPrefix(decStr, "CAIS") {
			return decoded, "anthropic"
		}
		if strings.HasPrefix(decStr, "gAAAA") {
			return decoded, "openai"
		}
		if decoded[0] == 0x01 {
			return []byte(s), "gemini"
		}
	}
	return []byte(s), detectSignatureType(s)
}

func detectSignatureType(sig string) string {
	s := strings.TrimSpace(sig)
	if strings.HasPrefix(s, "sealed.v1.") {
		return "sealed"
	}
	if strings.HasPrefix(s, "CAQS") || strings.HasPrefix(s, "CAIS") {
		return "anthropic"
	}
	if strings.HasPrefix(s, "gAAAA") {
		return "openai"
	}
	if strings.HasPrefix(s, "AY") {
		return "gemini"
	}
	return "sealed"
}

func supplementSignaturesFromOriginal(original []byte, prompts []helps.DevinPrompt) {
	origRoot := gjson.ParseBytes(original)
	messages := origRoot.Get("messages")
	if !messages.IsArray() {
		return
	}
	var assistantSigs [][]byte
	var assistantSigTypes []string
	for _, m := range messages.Array() {
		if strings.EqualFold(m.Get("role").String(), "assistant") {
			content := m.Get("content")
			if content.IsArray() {
				for _, part := range content.Array() {
					if part.Get("type").String() == "thinking" {
						if sig := part.Get("signature").String(); sig != "" {
							bytes, sType := parseSignatureBytes(sig)
							if len(bytes) > 0 {
								assistantSigs = append(assistantSigs, bytes)
								assistantSigTypes = append(assistantSigTypes, sType)
							}
						}
					}
				}
			}
		}
	}

	sigIdx := 0
	for i := range prompts {
		if prompts[i].Source == 2 && len(prompts[i].Signature) == 0 && sigIdx < len(assistantSigs) {
			prompts[i].Signature = assistantSigs[sigIdx]
			prompts[i].SignatureType = assistantSigTypes[sigIdx]
			sigIdx++
		}
	}
}

func (e *DevinExecutor) getSensitiveWords() []string {
	if e != nil && e.cfg != nil && len(e.cfg.Devin.SensitiveWords) > 0 {
		return e.cfg.Devin.SensitiveWords
	}
	return nil
}

func newDevinStatusError(code int, headers http.Header, body []byte) statusErr {
	err := statusErr{code: code, msg: string(body)}
	if code == http.StatusTooManyRequests && headers != nil {
		if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
			if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds >= 0 {
				delay := time.Duration(seconds) * time.Second
				err.retryAfter = &delay
			} else if deadline, errParse := http.ParseTime(raw); errParse == nil {
				delay := time.Until(deadline)
				if delay > 0 {
					err.retryAfter = &delay
				}
			}
		}
	}
	return err
}

func devinAuthCredentials(auth *cliproxyauth.Auth) (apiKey string, baseURL string, deviceSeed string) {
	if auth == nil {
		return "", helps.DevinDefaultBaseURL, ""
	}
	baseURL = helps.DevinDefaultBaseURL
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			apiKey = v
		}
		if v := strings.TrimSpace(auth.Attributes["session_token"]); v != "" && apiKey == "" {
			apiKey = v
		}
		if v := strings.TrimSpace(auth.Attributes["token"]); v != "" && apiKey == "" {
			apiKey = v
		}
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			baseURL = v
		}
		if v := strings.TrimSpace(auth.Attributes["device_seed"]); v != "" {
			deviceSeed = v
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["api_key"].(string); ok && strings.TrimSpace(v) != "" && apiKey == "" {
			apiKey = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["session_token"].(string); ok && strings.TrimSpace(v) != "" && apiKey == "" {
			apiKey = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(v) != "" && baseURL == helps.DevinDefaultBaseURL {
			baseURL = strings.TrimSpace(v)
		}
	}
	return
}

func devinAuthLogFields(auth *cliproxyauth.Auth) (authID, authLabel, authType, authValue string) {
	if auth == nil {
		return "", "", "devin", ""
	}
	authID = auth.ID
	authLabel = auth.Label
	authType = "devin"
	apiKey, _, _ := devinAuthCredentials(auth)
	if apiKey != "" {
		if len(apiKey) > 8 {
			authValue = apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
		} else {
			authValue = "***"
		}
	}
	return
}

func resolveDevinSessionAndCascadeIDs(ctx context.Context, sessionID, cascadeID string, opts cliproxyexecutor.Options) (string, string) {
	if sessionID == "" {
		if ctxSession := util.SessionIDFromContext(ctx); ctxSession != "" {
			sessionID = ctxSession
		} else if canon := cliproxyauth.CanonicalSessionID(opts.Headers, opts.OriginalRequest, opts.Metadata); canon != "" {
			sessionID = canon
		}
	}
	sessionID = normalizeDevinUUID(sessionID)
	if cascadeID == "" {
		cascadeID = sessionID
	} else {
		cascadeID = normalizeDevinUUID(cascadeID)
	}
	return sessionID, cascadeID
}

func normalizeDevinUUID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.New().String()
	}
	if _, err := uuid.Parse(raw); err == nil {
		return raw
	}
	// Deterministically map any non-UUID session string (e.g. lcp:hash, conv:id) to an RFC 4122 UUID v5
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw)).String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
