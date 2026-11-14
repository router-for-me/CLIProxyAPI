package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const vertexClaudeAnthropicVersion = "vertex-2023-10-16"

// vertexClaudeBaseURL resolves the Vertex host for a location; tests override it.
var vertexClaudeBaseURL = vertexBaseURL

// vertexClaudeRoute reports whether the request must use the Anthropic publisher
// rawPredict path, returning the service account credentials when it does.
func (e *GeminiVertexExecutor) vertexClaudeRoute(auth *cliproxyauth.Auth, model string) (projectID, location string, saJSON []byte, ok bool) {
	if e == nil || e.cfg == nil || len(e.cfg.VertexClaudeModels) == 0 {
		return "", "", nil, false
	}
	baseModel := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	matched := false
	for _, m := range e.cfg.VertexClaudeModels {
		// Registration dedups case-insensitively, so routing must match the same way.
		if baseModel != "" && strings.EqualFold(strings.TrimSpace(m), baseModel) {
			matched = true
			break
		}
	}
	if !matched {
		return "", "", nil, false
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	if apiKey, _ := vertexAPICreds(auth); apiKey != "" {
		log.Warnf("vertex executor: model %s is listed in vertex claude-models but auth %s uses an API key or access token; rawPredict requires a service account", baseModel, authID)
		return "", "", nil, false
	}
	projectID, location, saJSON, err := vertexCreds(auth)
	if err != nil {
		log.Warnf("vertex executor: model %s is listed in vertex claude-models but auth %s has unusable service account credentials: %v", baseModel, authID, err)
		return "", "", nil, false
	}
	return projectID, location, saJSON, true
}

func vertexClaudeCountTokensError() error {
	return statusErr{code: http.StatusNotImplemented, msg: "count tokens is not supported for Claude models on Vertex AI"}
}

func vertexClaudeURL(location, projectID, model string, stream bool) string {
	action := "rawPredict"
	if stream {
		action = "streamRawPredict"
	}
	return fmt.Sprintf("%s/%s/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
		vertexClaudeBaseURL(location), vertexAPIVersion, projectID, location, strings.ToLower(model), action)
}

// buildVertexClaudeRequest returns the upstream Anthropic body and the translated original payload.
func (e *GeminiVertexExecutor) buildVertexClaudeRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, baseModel string, stream bool) (body, originalTranslated []byte, err error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	originalTranslated, body, err = helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, stream, false)
	if err != nil {
		return nil, nil, err
	}
	doc := ojson.NewDoc(body)
	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier()); err != nil {
		return nil, nil, err
	}
	// Anthropic rejects thinking combined with forced tool use; mirror the Claude executor.
	disableThinkingIfToolChoiceForcedDoc(doc)
	// Vertex takes the model from the URL and requires anthropic_version in the body.
	_ = doc.Delete("model")
	_ = doc.Delete("session_id")
	_ = doc.Set("anthropic_version", vertexClaudeAnthropicVersion)
	if stream {
		_ = doc.Set("stream", true)
	} else {
		_ = doc.Delete("stream")
	}
	// User payload rules are the last semantic step and are applied exactly once.
	helps.ApplyPayloadConfigWithRequestDoc(e.cfg, baseModel, to.String(), from.String(), "", doc, originalTranslated, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	return doc.Bytes(), originalTranslated, nil
}

func (e *GeminiVertexExecutor) doVertexClaudeRequest(ctx context.Context, auth *cliproxyauth.Auth, reporter *helps.UsageReporter, url string, body []byte, saJSON []byte, opts cliproxyexecutor.Options) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	token, errTok := vertexAccessToken(ctx, e.cfg, auth, saJSON)
	if errTok != nil || token == "" {
		log.Errorf("vertex executor: access token error: %v", errTok)
		return nil, statusErr{code: http.StatusInternalServerError, msg: "internal server error"}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	var attrs map[string]string
	var authID, authLabel, authType, authValue string
	if auth != nil {
		attrs = auth.Attributes
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	// Never forward the bearer token to a redirect target.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("vertex executor: close response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}
	return httpResp, nil
}

func (e *GeminiVertexExecutor) executeVertexClaude(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, projectID, location string, saJSON []byte) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, _, err := e.buildVertexClaudeRequest(ctx, req, opts, baseModel, false)
	if err != nil {
		return resp, err
	}
	reporter.SetTranslatedReasoningEffort(body, "claude")
	httpResp, err := e.doVertexClaudeRequest(ctx, auth, reporter, vertexClaudeURL(location, projectID, baseModel, false), body, saJSON, opts)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("vertex executor: close response body error: %v", errClose)
		}
	}()
	data, errRead := io.ReadAll(httpResp.Body)
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	reporter.ObserveResponseModel(data)
	// Match the Claude executor: a policy refusal is surfaced as a 400 error, as on the stream path.
	if refusalMsg, blocked := clienterror.ClaudePolicyRefusalMessage(data); blocked {
		errRefusal := statusErr{code: http.StatusBadRequest, msg: refusalMsg}
		helps.RecordAPIResponseError(ctx, e.cfg, errRefusal)
		reporter.PublishFailureWithDetail(ctx, helps.ParseClaudeUsage(data), errRefusal)
		return resp, errRefusal
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	translateInput := data
	if responseFormat != sdktranslator.FormatClaude {
		// rawPredict returns a Messages JSON object; some translators only read SSE events.
		translateInput, _ = translatorcommon.ClaudeMessagesJSONToSSE(data)
	}
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("claude"), responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, translateInput, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	reporter.Publish(ctx, helps.ParseClaudeUsage(data))
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *GeminiVertexExecutor) executeVertexClaudeStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, projectID, location string, saJSON []byte) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, _, err := e.buildVertexClaudeRequest(ctx, req, opts, baseModel, true)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(body, "claude")
	httpResp, err := e.doVertexClaudeRequest(ctx, auth, reporter, vertexClaudeURL(location, projectID, baseModel, true), body, saJSON, opts)
	if err != nil {
		return nil, err
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("claude")
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("vertex executor: close response body error: %v", errClose)
			}
		}()
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		fail := func(errFail error) {
			helps.RecordAPIResponseError(ctx, e.cfg, errFail)
			streamUsage.PublishFailure(ctx, reporter, errFail)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errFail}:
			case <-ctx.Done():
			}
		}
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		var param any
		helps.InitializeApplyPatchStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, &param)
		var messageID string
		var streamErr error
		var eventBuf bytes.Buffer
		completed := false
		// cancelled records a client cancellation before the terminal event as a failure;
		// a disconnect after message_stop keeps the request successful.
		cancelled := func() {
			if completed {
				return
			}
			errCancel := ctx.Err()
			if errCancel == nil {
				errCancel = context.Canceled
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errCancel)
			streamUsage.PublishFailure(ctx, reporter, errCancel)
		}
		// Claude clients get complete upstream SSE events forwarded as-is; other formats are translated line by line.
		passthrough := responseFormat == to
		observeEvent := func() bool {
			if eventBuf.Len() == 0 {
				return true
			}
			observeClaudeSSEEvent(eventBuf.Bytes(), &messageID, &completed, &streamErr)
			streamUsage.ObserveClaudeSSEEvent(eventBuf.Bytes())
			event := bytes.Clone(eventBuf.Bytes())
			eventBuf.Reset()
			// An event flushed at EOF may lack the blank-line delimiter; restore it for passthrough.
			if !bytes.HasSuffix(event, []byte("\n\n")) {
				event = append(event, '\n')
			}
			if !passthrough {
				return true
			}
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: event}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for scanner.Scan() {
			line := scanner.Bytes()
			eventBuf.Write(line)
			eventBuf.WriteByte('\n')
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			reporter.ObserveResponseModel(line)
			streamUsage.ObserveClaudeStream(line)
			if passthrough {
				if len(bytes.TrimSpace(line)) == 0 {
					if !observeEvent() {
						cancelled()
						return
					}
					if completed || (streamErr != nil && !clienterror.IsClaudePolicyRefusalError(streamErr)) {
						break
					}
				}
				continue
			}
			if len(bytes.TrimSpace(line)) == 0 {
				observeEvent()
			}
			chunks := sdktranslator.TranslateStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), body, bytes.Clone(line), &param)
			if responseFormat == sdktranslator.FormatOpenAIResponse && helps.ApplyPatchTranslationError(param) == nil {
				for i, chunk := range chunks {
					chunks[i] = helps.EnsureResponsesUsageDetails(chunk)
				}
			}
			helps.RecordApplyPatchStreamFailure(ctx, param, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					cancelled()
					return
				}
			}
			if helps.StopApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return
			}
			if completed || (streamErr != nil && !clienterror.IsClaudePolicyRefusalError(streamErr)) {
				break
			}
		}
		if !observeEvent() {
			cancelled()
			return
		}
		if helps.EndApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
			return
		}
		if !completed || streamErr != nil {
			errScan := scanner.Err()
			if errScan == nil {
				errScan = streamErr
			}
			if errScan == nil && ctx.Err() == nil {
				errScan = statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before message_stop", streamTruncated: true}
			}
			if errScan != nil {
				fail(errScan)
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
