package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *QoderExecutor) openResponse(ctx context.Context, auth *cliproxyauth.Auth, prepared *qoderPreparedRequest, reporter *helps.UsageReporter) (*http.Response, error) {
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	resp, err := client.Do(prepared.httpRequest)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer closeQoderResponse(resp)
		body, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		return nil, statusErr{code: resp.StatusCode, msg: string(body)}
	}
	return resp, nil
}

func (e *QoderExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, normalizeQoderModel(req.Model), auth)
	reporter.SetStream(true)
	defer reporter.TrackFailure(ctx, &err)
	prepared, err := e.buildRequest(ctx, auth, req, opts, reporter)
	if err != nil {
		return nil, err
	}
	resp, err := e.openResponse(ctx, auth, prepared, reporter)
	if err != nil {
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer closeQoderResponse(resp)
		var streamUsage helps.StreamUsageBuffer
		defer streamUsage.Publish(ctx, reporter)
		var param any
		responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
		if responseFormat == "" {
			responseFormat = sdktranslator.FormatOpenAI
		}
		originalRequest := helps.ApplyPatchOriginalRequest(req, opts)
		emit := func(payload []byte) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		emitError := func(failure error) {
			reporter.PublishFailure(ctx, failure)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: failure}:
			case <-ctx.Done():
			}
		}
		emitDone := func() bool {
			frames := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, originalRequest, prepared.openAIRequest, []byte("[DONE]"), &param)
			for _, frame := range frames {
				if !emit(frame) {
					return false
				}
			}
			reporter.EnsurePublished(ctx)
			return true
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 52_428_800)
		completed := false
		eventType := ""
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("event:")) {
				eventType = strings.TrimSpace(string(trimmed[len("event:"):]))
				continue
			}
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			data := bytes.TrimSpace(trimmed[len("data:"):])
			currentEvent := eventType
			eventType = ""
			if bytes.Equal(data, []byte("[DONE]")) {
				emitDone()
				return
			}
			if currentEvent == "error" {
				emitError(statusErr{code: http.StatusBadGateway, msg: "qoder: upstream stream error: " + string(data)})
				return
			}
			inner, status, terminal, errEvent := qoderEventPayload(data)
			if errEvent != nil {
				emitError(errEvent)
				return
			}
			if status != 0 && status != http.StatusOK {
				emitError(statusErr{code: status, msg: string(inner)})
				return
			}
			if terminal {
				emitDone()
				return
			}
			if len(inner) == 0 || !gjson.ValidBytes(inner) {
				continue
			}
			if !gjson.GetBytes(inner, "model").Exists() {
				inner, _ = qoderSetModel(inner, req.Model)
			}
			reporter.ObserveResponseModel(inner)
			if finish := gjson.GetBytes(inner, "choices.0.finish_reason"); finish.Exists() && finish.Type != gjson.Null && finish.String() != "" {
				completed = true
			}
			ssePayload := append([]byte("data: "), inner...)
			streamUsage.ObserveOpenAIStream(ssePayload)
			frames := sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, originalRequest, prepared.openAIRequest, ssePayload, &param)
			for _, frame := range frames {
				if !emit(frame) {
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			emitError(errScan)
			return
		}
		if !completed {
			emitError(statusErr{code: http.StatusBadGateway, msg: "qoder: upstream stream ended before completion"})
			return
		}
		emitDone()
	}()
	return &cliproxyexecutor.StreamResult{Headers: resp.Header.Clone(), Chunks: out}, nil
}

func (e *QoderExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	internalOpts := opts
	internalOpts.ResponseFormat = sdktranslator.FormatOpenAI
	stream, err := e.ExecuteStream(ctx, auth, req, internalOpts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	aggregate := newQoderAggregate(req.Model)
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			return cliproxyexecutor.Response{}, chunk.Err
		}
		if errConsume := aggregate.consume(chunk.Payload); errConsume != nil {
			for range stream.Chunks {
			}
			return cliproxyexecutor.Response{}, errConsume
		}
	}
	responseBody, err := aggregate.response()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	if responseFormat == "" {
		responseFormat = sdktranslator.FormatOpenAI
	}
	translationSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		translationSource = opts.OriginalRequest
	}
	translatedRequest := sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatOpenAI, req.Model, translationSource, false)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translatedRequest, responseBody, &param)
	if helps.ApplyPatchTranslationError(param) != nil || len(out) == 0 {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	return cliproxyexecutor.Response{Payload: out, Headers: stream.Headers}, nil
}

func qoderEventPayload(data []byte) (inner []byte, status int, terminal bool, err error) {
	var envelope map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&envelope); errDecode != nil {
		return nil, 0, false, nil
	}
	status = int(qoderNumber(envelope["statusCodeValue"]))
	if status == 0 {
		status = int(qoderNumber(envelope["statusCode"]))
	}
	body, exists := envelope["body"]
	if !exists {
		return data, status, false, nil
	}
	switch typed := body.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "[DONE]" {
			return nil, status, true, nil
		}
		return []byte(text), status, false, nil
	case map[string]any, []any:
		encoded, errMarshal := qoderMarshalJSON(typed)
		return encoded, status, false, errMarshal
	case nil:
		return nil, status, false, nil
	default:
		return []byte(fmt.Sprint(typed)), status, false, nil
	}
}

func qoderSetModel(data []byte, model string) ([]byte, error) {
	if !gjson.ParseBytes(data).IsObject() {
		return data, nil
	}
	return sjson.SetBytes(data, "model", model)
}

func qoderMarshalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

type qoderToolAggregate struct {
	ID        string
	Name      string
	Arguments string
}

type qoderAggregate struct {
	model      string
	id         string
	created    int64
	content    strings.Builder
	reasoning  strings.Builder
	finish     string
	tools      map[int]*qoderToolAggregate
	usage      map[string]any
	responseID string
}

func newQoderAggregate(model string) *qoderAggregate {
	return &qoderAggregate{model: model, tools: make(map[int]*qoderToolAggregate)}
}

func (a *qoderAggregate) consume(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	raw = bytes.TrimPrefix(raw, []byte("data:"))
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("[DONE]")) {
		return nil
	}
	var chunk map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&chunk); err != nil {
		return statusErr{code: http.StatusBadGateway, msg: "qoder: invalid OpenAI stream chunk"}
	}
	if id, ok := chunk["id"].(string); ok && id != "" {
		a.id = id
	}
	if created := qoderNumber(chunk["created"]); created > 0 {
		a.created = created
	}
	if usage, ok := chunk["usage"].(map[string]any); ok && usage != nil {
		a.usage = usage
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
		a.finish = finish
	}
	delta, _ := choice["delta"].(map[string]any)
	if content, ok := delta["content"].(string); ok {
		a.content.WriteString(content)
	}
	if reasoning, ok := delta["reasoning_content"].(string); ok {
		a.reasoning.WriteString(reasoning)
	}
	calls, _ := delta["tool_calls"].([]any)
	for _, rawCall := range calls {
		call, _ := rawCall.(map[string]any)
		index := int(qoderNumber(call["index"]))
		entry := a.tools[index]
		if entry == nil {
			entry = &qoderToolAggregate{}
			a.tools[index] = entry
		}
		if id, ok := call["id"].(string); ok {
			entry.ID = id
		}
		function, _ := call["function"].(map[string]any)
		if name, ok := function["name"].(string); ok {
			entry.Name = name
		}
		if arguments, ok := function["arguments"].(string); ok {
			entry.Arguments += arguments
		}
	}
	return nil
}

func (a *qoderAggregate) response() ([]byte, error) {
	message := map[string]any{"role": "assistant", "content": a.content.String()}
	if reasoning := a.reasoning.String(); reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(a.tools) > 0 {
		indices := make([]int, 0, len(a.tools))
		for index := range a.tools {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		calls := make([]any, 0, len(indices))
		for position, index := range indices {
			entry := a.tools[index]
			id := entry.ID
			if id == "" {
				id = fmt.Sprintf("call_qoder_%d_%d", time.Now().UnixNano(), position)
			}
			arguments := entry.Arguments
			if strings.TrimSpace(arguments) == "" {
				arguments = "{}"
			}
			calls = append(calls, map[string]any{
				"id": id, "type": "function", "function": map[string]any{"name": entry.Name, "arguments": arguments},
			})
		}
		message["tool_calls"] = calls
	}
	finish := a.finish
	if finish == "" {
		if len(a.tools) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	if a.id == "" {
		a.id = fmt.Sprintf("chatcmpl-qoder-%d", time.Now().UnixNano())
	}
	if a.created == 0 {
		a.created = time.Now().Unix()
	}
	response := map[string]any{
		"id": a.id, "object": "chat.completion", "created": a.created, "model": a.model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	}
	if a.usage != nil {
		response["usage"] = a.usage
	}
	return json.Marshal(response)
}
