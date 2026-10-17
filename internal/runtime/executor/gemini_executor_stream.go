package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type geminiStreamRequest struct {
	request         cliproxyexecutor.Request
	options         cliproxyexecutor.Options
	auth            *cliproxyauth.Auth
	model           string
	url             string
	body            []byte
	originalPayload []byte
	responseFormat  sdktranslator.Format
	finalize        helps.PayloadFinalizer
	client          *http.Client
}

type geminiStreamAttempt struct {
	response  *http.Response
	reader    io.Reader
	body      []byte
	reporter  *helps.UsageReporter
	recovered bool
}

func (e *GeminiExecutor) executeGeminiStream(ctx context.Context, request *geminiStreamRequest, reporter *helps.UsageReporter) (*cliproxyexecutor.StreamResult, error) {
	attempt, err := e.openGeminiStream(ctx, request, request.finalize(request.body), reporter)
	if err != nil {
		return nil, err
	}
	if request.options.SourceFormat == sdktranslator.FormatClaude && request.responseFormat == sdktranslator.FormatClaude {
		attempt, err = e.recoverGeminiStream(ctx, request, attempt)
		if err != nil {
			return nil, err
		}
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go e.consumeGeminiStream(ctx, request, attempt, out)
	return &cliproxyexecutor.StreamResult{Headers: attempt.response.Header.Clone(), Chunks: out}, nil
}

func (e *GeminiExecutor) openGeminiStream(ctx context.Context, request *geminiStreamRequest, body []byte, reporter *helps.UsageReporter) (_ *geminiStreamAttempt, err error) {
	defer reporter.TrackFailure(ctx, &err)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, request.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := geminiAPIKey(request.auth); apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, request.auth, request.options.Headers)
	authID, authLabel, authType, authValue := geminiAuthLogFields(request.auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL: request.url, Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: body,
		Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue,
	})
	reporter.SetTranslatedReasoningEffort(body, sdktranslator.FormatGemini.String())
	response, err := reporter.TrackHTTPClient(request.client).Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, response.StatusCode, response.Header.Clone())
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer e.closeGeminiStreamResponse(ctx, response)
		payload, errRead := io.ReadAll(response.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, payload)
		if errRead != nil {
			return nil, errors.Join(statusErr{code: response.StatusCode, msg: string(payload)}, fmt.Errorf("read Gemini error response: %w", errRead))
		}
		return nil, statusErr{code: response.StatusCode, msg: string(payload)}
	}
	return &geminiStreamAttempt{response: response, reader: response.Body, body: body, reporter: reporter}, nil
}

func (e *GeminiExecutor) recoverGeminiStream(ctx context.Context, request *geminiStreamRequest, first *geminiStreamAttempt) (*geminiStreamAttempt, error) {
	prefix, err := helps.ReadGeminiStreamPrefix(first.response.Body, request.body, first.body, streamScannerBuffer)
	first.reader = prefix.Reader
	if err != nil {
		e.failGeminiStreamPrefix(ctx, first, prefix.Bytes, err)
		return nil, err
	}
	if prefix.Tool == "" {
		return first, nil
	}
	body, err := helps.ConstrainGeminiRecoveryTool(request.body, prefix.Tool)
	if err != nil {
		e.failGeminiStreamPrefix(ctx, first, prefix.Bytes, err)
		return nil, err
	}
	body = request.finalize(body)
	preserved, err := helps.GeminiRecoveryBodyPreserved(first.body, body)
	if err != nil {
		e.failGeminiStreamPrefix(ctx, first, prefix.Bytes, err)
		return nil, err
	}
	if !preserved || !helps.GeminiRecoveryToolConfigured(body, prefix.Tool) {
		return first, nil
	}
	if err = ctx.Err(); err != nil {
		e.failGeminiStreamPrefix(ctx, first, prefix.Bytes, err)
		return nil, err
	}
	failure := statusErr{code: http.StatusBadGateway, msg: "Gemini response finished with MALFORMED_FUNCTION_CALL"}
	e.failGeminiStreamPrefix(ctx, first, prefix.Bytes, failure)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, request.model, request.auth)
	second, err := e.openGeminiStream(ctx, request, body, reporter)
	if err != nil {
		return nil, fmt.Errorf("Gemini malformed-call recovery: %w", err)
	}
	second.recovered = true
	return second, nil
}

func (e *GeminiExecutor) failGeminiStreamPrefix(ctx context.Context, attempt *geminiStreamAttempt, prefix []byte, err error) {
	var usage helps.StreamUsageBuffer
	scanner := bufio.NewScanner(bytes.NewReader(prefix))
	scanner.Buffer(nil, streamScannerBuffer)
	for scanner.Scan() {
		e.observeGeminiStreamLine(ctx, attempt.reporter, &usage, scanner.Bytes())
	}
	if errScan := scanner.Err(); errScan != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errScan)
	}
	usage.PublishFailure(ctx, attempt.reporter, err)
	e.closeGeminiStreamResponse(ctx, attempt.response)
}

func (e *GeminiExecutor) closeGeminiStreamResponse(ctx context.Context, response *http.Response) {
	if errClose := response.Body.Close(); errClose != nil {
		helps.LogWithRequestID(ctx).Errorf("gemini executor: close response body error: %v", errClose)
	}
}

func (e *GeminiExecutor) observeGeminiStreamLine(ctx context.Context, reporter *helps.UsageReporter, usage *helps.StreamUsageBuffer, line []byte) {
	helps.AppendAPIResponseChunk(ctx, e.cfg, line)
	reporter.ObserveResponseModel(line)
	if detail, ok := helps.ParseGeminiStreamUsage(helps.JSONPayload(line)); ok {
		usage.Observe(detail, true)
	}
}

type geminiStreamDecoder struct {
	executor        *GeminiExecutor
	request         *geminiStreamRequest
	attempt         *geminiStreamAttempt
	out             chan<- cliproxyexecutor.StreamChunk
	usage           helps.StreamUsageBuffer
	err             error
	param           any
	inputTokens     *helps.ClaudeInputTokenState
	originalRequest []byte
	hasResult       bool
}

func (e *GeminiExecutor) consumeGeminiStream(ctx context.Context, request *geminiStreamRequest, attempt *geminiStreamAttempt, out chan<- cliproxyexecutor.StreamChunk) {
	defer close(out)
	defer attempt.reporter.EnsurePublished(ctx)
	defer e.closeGeminiStreamResponse(ctx, attempt.response)
	decoder := &geminiStreamDecoder{
		executor: e, request: request, attempt: attempt, out: out,
		inputTokens:     helps.NewClaudeInputTokenState(request.options.SourceFormat, sdktranslator.FormatGemini, request.responseFormat, request.originalPayload),
		originalRequest: helps.ApplyPatchOriginalRequest(request.request, request.options),
	}
	defer decoder.usage.Publish(ctx, attempt.reporter)
	defer func() {
		helps.StopTranslatedClaudeStream(ctx, &decoder.err, attempt.reporter, &decoder.usage, out)
	}()
	helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatGemini, request.responseFormat, request.request.Model, decoder.originalRequest, attempt.body, &decoder.param)
	scanner := bufio.NewScanner(attempt.reader)
	scanner.Buffer(nil, streamScannerBuffer)
	for scanner.Scan() {
		line := scanner.Bytes()
		e.observeGeminiStreamLine(ctx, attempt.reporter, &decoder.usage, line)
		filtered := line
		if request.responseFormat != sdktranslator.FormatClaude {
			filtered = helps.FilterSSEUsageMetadata(line)
		}
		payload := helps.JSONPayload(filtered)
		if len(payload) == 0 {
			continue
		}
		if !decoder.translate(ctx, bytes.Clone(payload)) {
			return
		}
	}
	decoder.finish(ctx, scanner.Err())
}

func (d *geminiStreamDecoder) translate(ctx context.Context, payload []byte) bool {
	if d.attempt.recovered {
		root := gjson.ParseBytes(payload)
		if upstreamError := root.Get("error"); upstreamError.Exists() && upstreamError.Type != gjson.Null {
			d.err = statusErr{code: http.StatusBadGateway, msg: upstreamError.Raw}
			return false
		}
		if root.Get("candidates.#").Int() > 0 {
			d.hasResult = d.hasResult || helps.IsGeminiTokenEvent(payload)
		}
	}
	lines := helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatGemini, d.request.responseFormat,
		d.request.request.Model, d.originalRequest, d.attempt.body, payload, &d.param, d.inputTokens)
	failure := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	helps.RecordApplyPatchStreamFailure(ctx, d.param, d.attempt.reporter, failure)
	helps.CaptureTranslatedClaudeStreamError(d.request.responseFormat, lines, &d.err, func(message string) error {
		return statusErr{code: http.StatusBadGateway, msg: message}
	})
	for _, line := range lines {
		select {
		case d.out <- cliproxyexecutor.StreamChunk{Payload: line}:
		case <-ctx.Done():
			if d.err == nil {
				d.err = ctx.Err()
			}
			return false
		}
	}
	return !helps.StopApplyPatchStream(ctx, d.param, d.attempt.reporter, d.out, failure)
}

func (d *geminiStreamDecoder) finish(ctx context.Context, scanErr error) {
	failure := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	if helps.EndApplyPatchStream(ctx, d.param, d.attempt.reporter, d.out, failure) {
		return
	}
	if helps.StopTranslatedClaudeStream(ctx, &d.err, d.attempt.reporter, &d.usage, d.out) {
		return
	}
	if scanErr != nil {
		helps.RecordAPIResponseError(ctx, d.executor.cfg, scanErr)
		d.err = scanErr
		return
	}
	if err := ctx.Err(); err != nil {
		d.err = err
		return
	}
	if d.attempt.recovered && !d.hasResult {
		d.err = statusErr{code: http.StatusBadGateway, msg: "Gemini recovery stream ended without a response"}
		return
	}
	if !d.translate(ctx, []byte("[DONE]")) {
		return
	}
	helps.StopTranslatedClaudeStream(ctx, &d.err, d.attempt.reporter, &d.usage, d.out)
}
