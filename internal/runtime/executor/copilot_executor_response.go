package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func (e *CopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ cliproxyexecutor.Response, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts, false, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, thinking.ParseSuffix(req.Model).ModelName, auth)
	defer reporter.TrackFailure(ctx, &err)
	resp, err := e.send(ctx, auth, opts, &prepared, reporter)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer closeCopilotResponse(resp.Body)
	const maxResponse = 64 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if len(body) > maxResponse || !ojson.ValidBytes(body) {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "copilot: invalid upstream JSON response"}
	}
	if failure := copilotResponseError(body); failure != nil {
		return cliproxyexecutor.Response{}, failure
	}
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	var state any
	translatedBody := body
	if prepared.route.Format == sdktranslator.FormatOpenAI && format != prepared.route.Format {
		prepared.capture.Observe(ojson.ParseBytes(body))
		translatedBody = helps.CopilotVisibleChatReasoning(body, false)
	}
	if prepared.route.Format == sdktranslator.FormatOpenAIResponse && format == sdktranslator.FormatClaude {
		translatedBody = helps.CopilotVisibleResponsesForClaude(body)
	}
	out := prepared.responseTranslators.TranslateNonStream(ctx, prepared.route.Format, format, req.Model, prepared.original, prepared.body, translatedBody, &state)
	if len(out) == 0 || helps.ApplyPatchTranslationError(state) != nil {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: "copilot: unable to translate upstream response"}
	}
	if prepared.responsesCapture != nil {
		prepared.responsesCapture.Observe(ojson.ParseBytes(body), ojson.GetBytesNoCopy(out, "content"))
	}
	if prepared.messagesCapture != nil {
		prepared.messagesCapture.Observe(ojson.GetBytesNoCopy(body, "content"))
	}
	if format == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	e.turns.Observe(prepared.turn, prepared.route.Format, ojson.ParseBytes(body))
	reporter.ObserveResponseModel(body)
	if prepared.route.Format == sdktranslator.FormatClaude {
		reporter.Publish(ctx, helps.ParseClaudeUsage(body))
	} else {
		reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	}
	return cliproxyexecutor.Response{Payload: out, Headers: resp.Header.Clone(), Incomplete: ojson.GetBytesNoCopy(body, "status").String() == "incomplete"}, nil
}

func (e *CopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts, true, false)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, thinking.ParseSuffix(req.Model).ModelName, auth)
	defer reporter.TrackFailure(ctx, &err)
	resp, err := e.send(ctx, auth, opts, &prepared, reporter)
	if err != nil {
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go e.copilotStream(ctx, req, opts, prepared, resp.Body, reporter, out)
	return &cliproxyexecutor.StreamResult{Headers: resp.Header.Clone(), Chunks: out}, nil
}

func (e *CopilotExecutor) copilotStream(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, prepared copilotPreparedRequest, body io.ReadCloser, reporter *helps.UsageReporter, out chan<- cliproxyexecutor.StreamChunk) {
	defer close(out)
	defer closeCopilotResponse(body)
	scanner := helps.NewCodexSSEScanner(ctx, body, 16<<20)
	from, to := prepared.route.Format, cliproxyexecutor.ResponseFormatOrSource(opts)
	var state any
	var usage helps.StreamUsageBuffer
	usage.SetRequestPayload(prepared.body)
	inputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, from, to, prepared.original)
	translationOriginal := prepared.original
	if from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatClaude && ojson.GetBytesNoCopy(translationOriginal, "stream").Type != ojson.True {
		// ExecuteStream determines the response mode even if the caller reused a non-streaming body.
		// Set the hint only on the response translator's view of the request.
		doc := ojson.NewDoc(translationOriginal)
		_ = doc.Set("stream", true)
		translationOriginal = doc.Bytes()
	}
	emit := func(chunk cliproxyexecutor.StreamChunk) bool {
		select {
		case <-ctx.Done():
			return false
		case out <- chunk:
			return true
		}
	}
	fail := func(err error) {
		usage.PublishFailure(ctx, reporter, err)
		emit(cliproxyexecutor.StreamChunk{Err: err})
	}
	var signedBlocks *kimiThinkingReplayStreamAccumulator
	if prepared.messagesCapture != nil {
		signedBlocks = newKimiThinkingReplayStreamAccumulator()
	}
	var visibleClaude *kimiThinkingReplayStreamAccumulator
	if prepared.responsesCapture != nil && to == sdktranslator.FormatClaude {
		visibleClaude = newKimiThinkingReplayStreamAccumulator()
	}
	var terminal, chatFinished bool
	for scanner.Scan() {
		line := scanner.Bytes()
		data, dataLine := bytes.CutPrefix(line, []byte("data:"))
		if !dataLine {
			continue
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 {
			continue
		}
		done := bytes.Equal(data, []byte("[DONE]"))
		if !done && !ojson.ValidBytes(data) {
			fail(statusErr{code: http.StatusBadGateway, msg: "copilot: invalid SSE data"})
			return
		}
		if !done {
			if errEvent := copilotResponseError(data); errEvent != nil {
				fail(errEvent)
				return
			}
		}
		typ := ojson.GetBytesNoCopy(data, "type").String()
		switch from {
		case sdktranslator.FormatOpenAI:
			usage.ObserveOpenAIStream(line)
			chatFinished = chatFinished || ojson.GetBytesNoCopy(data, "choices.0.finish_reason").String() != ""
			terminal = done
		case sdktranslator.FormatClaude:
			usage.ObserveClaudeStream(line)
			terminal = typ == "message_stop"
		case sdktranslator.FormatOpenAIResponse:
			usage.ObserveResponsesStream(data)
			terminal = typ == "response.completed" || typ == "response.incomplete"
		}
		var chunks [][]byte
		if from == to && from != sdktranslator.FormatOpenAI {
			if typ == "" {
				continue
			}
			frame := common.SSEEventData(typ, data)
			chunks = prepared.responseTranslators.TranslateStream(ctx, from, to, req.Model, translationOriginal, prepared.body, frame, &state)
		} else if from != to || !done {
			translatedLine := bytes.Clone(line)
			if from == sdktranslator.FormatOpenAI && to != from && !done {
				prepared.capture.Observe(ojson.ParseBytes(data))
				if visible := helps.CopilotVisibleChatReasoning(data, true); !bytes.Equal(visible, data) {
					translatedLine = append([]byte("data: "), visible...)
				}
			}
			if from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude {
				visible := helps.CopilotVisibleResponsesForClaude(data)
				if visible == nil {
					continue
				}
				if !bytes.Equal(visible, data) {
					translatedLine = append([]byte("data: "), visible...)
				}
			}
			chunks = prepared.responseTranslators.TranslateStream(ctx, from, to, req.Model, translationOriginal, prepared.body, translatedLine, &state)
			chunks = inputTokens.FinalizeTranslatedUsage(ctx, to, chunks, &state)
		}
		if helps.ApplyPatchTranslationError(state) != nil {
			fail(statusErr{code: http.StatusBadGateway, msg: "copilot: unable to translate upstream tool call"})
			return
		}
		if !done {
			if prepared.responsesCapture != nil {
				var content ojson.Result
				if visibleClaude != nil {
					for _, chunk := range chunks {
						visibleClaude.observe(chunk)
					}
					if terminal {
						if raw, ok := visibleClaude.content(); ok {
							content = ojson.ParseBytes(raw)
						}
					}
				}
				prepared.responsesCapture.Observe(ojson.ParseBytes(data), content)
			}
			if signedBlocks != nil {
				signedBlocks.observe(line)
				if terminal {
					if content, complete := signedBlocks.content(); complete {
						prepared.messagesCapture.Observe(ojson.ParseBytes(content))
					}
				}
			}
			e.turns.Observe(prepared.turn, from, ojson.ParseBytes(data))
		}
		for _, chunk := range chunks {
			if len(chunk) > 0 && !bytes.Equal(bytes.TrimSpace(chunk), []byte("[DONE]")) && !emit(cliproxyexecutor.StreamChunk{Payload: chunk}) {
				return
			}
		}
		if terminal {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		fail(err)
		return
	}
	if !terminal && from == sdktranslator.FormatOpenAI && chatFinished {
		terminal = true
		if to != from {
			for _, chunk := range prepared.responseTranslators.TranslateStream(ctx, from, to, req.Model, translationOriginal, prepared.body, []byte("data: [DONE]"), &state) {
				if len(chunk) != 0 && !emit(cliproxyexecutor.StreamChunk{Payload: chunk}) {
					return
				}
			}
		}
	}
	if !terminal {
		if ctx.Err() == nil {
			fail(statusErr{code: http.StatusBadGateway, msg: "copilot: upstream stream ended before a terminal event"})
		}
		return
	}
	if model := usage.ResponseModel(); model != "" {
		reporter.SetUpstreamModel(model)
	}
	if !usage.Publish(ctx, reporter) {
		reporter.EnsurePublished(ctx)
	}
}

func copilotResponseError(body []byte) error {
	root := ojson.ParseBytes(body)
	var code string
	failure := root.Get("error")
	if !failure.Exists() || failure.Type == ojson.Null {
		failure = root.Get("response.error")
	}
	typ := root.Get("type").String()
	if (!failure.Exists() || failure.Type == ojson.Null) && typ != "error" && typ != "response.failed" && root.Get("status").String() != "failed" {
		return nil
	}
	code = failure.Get("code").String()
	if code == "" {
		code = root.Get("code").String()
	}
	if code == "rate_limit_exceeded" || failure.Get("type").String() == "rate_limit_error" {
		return statusErr{code: http.StatusTooManyRequests, msg: "copilot: upstream rate limit"}
	}
	return statusErr{code: http.StatusBadGateway, msg: "copilot: upstream reported a generation error"}
}
