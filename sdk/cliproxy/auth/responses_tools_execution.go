package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// responsesToolsCall wraps one actual executor call with core tool protocol
// adaptation. The sequence is fixed: after-auth interceptor output, route
// resolution against the real credential and format, Prepare against the same
// config snapshot, normalized executor call, then output restore or a typed
// error. Every refresh, credential rotation, or model change rebuilds from
// the original client contract with a fresh Prepare; normalized bodies are
// never re-wrapped.
//
// On success the attempt is closed before returning. On bridge errors the
// attempt is closed and the error takes the request-scoped stop branch
// regardless of user-configured error regex rules.
func (m *Manager) responsesToolsCall(
	execCtx context.Context,
	auth *Auth,
	provider string,
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	call func(context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	toFormat := requestToFormat(provider, nil, execReq, execOpts)
	// Without an executor instance the RequestToFormat capability is unknown;
	// callers pass the real executor through responsesToolsCallWithExecutor.
	return m.responsesToolsCallToFormat(execCtx, auth, provider, toFormat, execReq, execOpts, call)
}

// responsesToolsCallWithExecutor is the executor-aware form: executor
// capabilities (RequestToFormat) are preserved because the original instance
// is never wrapped or replaced.
func (m *Manager) responsesToolsCallWithExecutor(
	execCtx context.Context,
	auth *Auth,
	provider string,
	executor ProviderExecutor,
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	call func(context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	toFormat := requestToFormat(provider, executor, execReq, execOpts)
	return m.responsesToolsCallToFormat(execCtx, auth, provider, toFormat, execReq, execOpts, call)
}

func (m *Manager) responsesToolsCallToFormat(
	execCtx context.Context,
	auth *Auth,
	provider string,
	toFormat sdktranslator.Format,
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	call func(context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	if m == nil || call == nil {
		return cliproxyexecutor.Response{}, nil
	}
	// Only Responses-family input targeting a Responses-family downstream
	// enters adaptation. Anything else (or unknown) passes through unchanged
	// even when a route matches the model name.
	if !isResponsesFamily(execOpts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(execOpts)) {
		resp, err := call(execCtx, execReq, execOpts)
		return resp, err
	}
	route := responsesToolsRoute(auth, provider, execReq.Model, toFormat)
	route.AuthKind = normalizeAuthKindForPolicy(route.AuthKind)
	payload := responsesToolsPayload(execReq, execOpts)
	wireBody, attempt, wire, errPrepare := m.prepareResponsesToolsAttempt(route, payload)
	if errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}
	if attempt == nil {
		if req, opts, repaired := repairedResponsesToolsRequest(execReq, execOpts, payload, wireBody); repaired {
			return call(execCtx, req, opts)
		}
		resp, err := call(execCtx, execReq, execOpts)
		return resp, err
	}
	defer attempt.Close()
	// The executor must see the normalized contract in both views, otherwise
	// a downstream translator could restore custom/search declarations once
	// before this attempt restores them again.
	normalizedReq := execReq
	normalizedReq.Payload = wireBody
	normalizedOpts := execOpts
	if len(execOpts.OriginalRequest) > 0 {
		normalizedOpts.OriginalRequest = wireBody
	}
	// The shared metadata map is copied before tagging so session hierarchy,
	// requested-model, Home lifecycle, and usage semantics stay intact.
	if normalizedOpts.Metadata == nil {
		normalizedOpts.Metadata = make(map[string]any, len(execOpts.Metadata)+1)
	} else {
		copied := make(map[string]any, len(execOpts.Metadata)+1)
		for key, value := range execOpts.Metadata {
			copied[key] = value
		}
		normalizedOpts.Metadata = copied
	}
	execCtx = withResponsesToolsContract(execCtx, wire)
	resp, err := call(execCtx, normalizedReq, normalizedOpts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	restored, errRestore := attempt.RewriteResponse(resp.Payload)
	if errRestore != nil {
		return cliproxyexecutor.Response{}, errRestore
	}
	resp.Payload = restored
	return resp, nil
}

// repairedResponsesToolsRequest reports whether preparation changed a request
// that no attempt owns. Item id repair is the only pass-through rewrite, so
// an unchanged payload keeps the original request and its original slices.
func repairedResponsesToolsRequest(
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	payload, wireBody []byte,
) (cliproxyexecutor.Request, cliproxyexecutor.Options, bool) {
	if len(wireBody) == 0 || bytes.Equal(wireBody, payload) {
		return execReq, execOpts, false
	}
	repaired := execReq
	repaired.Payload = wireBody
	repairedOpts := execOpts
	if len(execOpts.OriginalRequest) > 0 {
		repairedOpts.OriginalRequest = wireBody
	}
	return repaired, repairedOpts, true
}

// responsesToolsCountCall applies request preparation and declaration budget
// to token counting without building response stream state. Executors that
// cannot count keep their original error behavior; counts are never faked.
func (m *Manager) responsesToolsCountCall(
	execCtx context.Context,
	auth *Auth,
	provider string,
	executor ProviderExecutor,
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	call func(context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	if m == nil || call == nil {
		return cliproxyexecutor.Response{}, nil
	}
	if !isResponsesFamily(execOpts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(execOpts)) {
		return call(execCtx, execReq, execOpts)
	}
	toFormat := requestToFormat(provider, executor, execReq, execOpts)
	route := responsesToolsRoute(auth, provider, execReq.Model, toFormat)
	route.AuthKind = normalizeAuthKindForPolicy(route.AuthKind)
	payload := responsesToolsPayload(execReq, execOpts)
	wireBody, attempt, wire, errPrepare := m.prepareResponsesToolsAttempt(route, payload)
	if errPrepare != nil {
		return cliproxyexecutor.Response{}, errPrepare
	}
	if attempt == nil {
		if req, opts, repaired := repairedResponsesToolsRequest(execReq, execOpts, payload, wireBody); repaired {
			return call(execCtx, req, opts)
		}
		return call(execCtx, execReq, execOpts)
	}
	defer attempt.Close()
	normalizedReq := execReq
	normalizedReq.Payload = wireBody
	normalizedOpts := execOpts
	if len(execOpts.OriginalRequest) > 0 {
		normalizedOpts.OriginalRequest = wireBody
	}
	execCtx = withResponsesToolsContract(execCtx, wire)
	return call(execCtx, normalizedReq, normalizedOpts)
}

// responsesToolsStreamCall wraps one streaming executor call with core tool
// protocol adaptation. Preparation errors surface before the first chunk so
// callers return real HTTP errors through bootstrap. After the first chunk,
// failures travel once through the Responses stream error shape and the
// attempt is cancelled; the HTTP status is never rewritten and no completed
// event follows a failure.
//
// The adapter goroutine owns Close and the lease: parent cancellation stops
// upstream consumption and releases resources without closing channels owned
// by other components.
func (m *Manager) responsesToolsStreamCall(
	parentCtx context.Context,
	auth *Auth,
	provider string,
	executor ProviderExecutor,
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	call func(context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, bool, error) {
	if m == nil || call == nil {
		return nil, false, nil
	}
	if !isResponsesFamily(execOpts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(execOpts)) {
		result, err := call(parentCtx, execReq, execOpts)
		return result, false, err
	}
	toFormat := requestToFormat(provider, executor, execReq, execOpts)
	route := responsesToolsRoute(auth, provider, execReq.Model, toFormat)
	route.AuthKind = normalizeAuthKindForPolicy(route.AuthKind)
	payload := responsesToolsPayload(execReq, execOpts)
	wireBody, attempt, wire, errPrepare := m.prepareResponsesToolsAttempt(route, payload)
	if errPrepare != nil {
		return nil, false, errPrepare
	}
	if attempt == nil {
		if req, opts, repaired := repairedResponsesToolsRequest(execReq, execOpts, payload, wireBody); repaired {
			result, err := call(parentCtx, req, opts)
			return result, false, err
		}
		result, err := call(parentCtx, execReq, execOpts)
		return result, false, err
	}
	normalizedReq := execReq
	normalizedReq.Payload = wireBody
	normalizedOpts := execOpts
	if len(execOpts.OriginalRequest) > 0 {
		normalizedOpts.OriginalRequest = wireBody
	}
	if normalizedOpts.Metadata == nil {
		normalizedOpts.Metadata = make(map[string]any, len(execOpts.Metadata)+1)
	} else {
		copied := make(map[string]any, len(execOpts.Metadata)+1)
		for key, value := range execOpts.Metadata {
			copied[key] = value
		}
		normalizedOpts.Metadata = copied
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	attemptCtx, cancelAttempt := context.WithCancel(withResponsesToolsContract(parentCtx, wire))
	result, err := call(attemptCtx, normalizedReq, normalizedOpts)
	if err != nil {
		cancelAttempt()
		attempt.Close()
		return nil, false, err
	}
	if result == nil || result.Chunks == nil {
		cancelAttempt()
		attempt.Close()
		return result, false, nil
	}
	codexDataLineChunks := toFormat == sdktranslator.FormatCodex
	adapted := adaptResponsesToolsStream(parentCtx, attemptCtx, cancelAttempt, attempt, result, codexDataLineChunks)
	return adapted, true, nil
}

// adaptResponsesToolsStream converts one upstream chunk channel through the
// attempt feed. Feed output of zero events means buffered, never
// pass-through. Terminal stream errors emit once and cancel the attempt.
//
// Restored tool frames leave the pure protocol feed as bare JSON; this
// adapter wraps them as SSE data: frames because downstream framing (the
// handlers_stream.go SSE JSON validator and the OpenAI responses SSE framer)
// only recognizes data:-prefixed frames. Pass-through frames already carry
// their own SSE framing and are forwarded untouched.
func adaptResponsesToolsStream(parentCtx, attemptCtx context.Context, cancelAttempt context.CancelFunc, attempt interface {
	Feed([]byte) ([][]byte, error)
	Finish() ([][]byte, error)
	Close()
}, result *cliproxyexecutor.StreamResult, codexDataLineChunks bool) *cliproxyexecutor.StreamResult {
	if attempt == nil || result == nil {
		if cancelAttempt != nil {
			cancelAttempt()
		}
		return result
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		if cancelAttempt != nil {
			defer cancelAttempt()
		}
		defer attempt.Close()
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			select {
			case <-parentCtx.Done():
				return false
			case out <- chunk:
				return true
			}
		}
		for {
			var chunk cliproxyexecutor.StreamChunk
			var ok bool
			select {
			case <-parentCtx.Done():
				return
			case <-attemptCtx.Done():
				return
			case chunk, ok = <-result.Chunks:
				if !ok {
					goto finish
				}
			}
			if chunk.Err != nil {
				if cancelAttempt != nil {
					cancelAttempt()
				}
				send(chunk)
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			feedPayload := chunk.Payload
			if codexDataLineChunks {
				feedPayload = normalizeCodexResponsesDataLine(chunk.Payload)
			} else {
				feedPayload = normalizeResponsesStreamFrame(chunk.Payload)
			}
			if len(feedPayload) == 0 {
				continue
			}
			frames, errFeed := attempt.Feed(feedPayload)
			if errFeed != nil {
				if cancelAttempt != nil {
					cancelAttempt()
				}
				send(cliproxyexecutor.StreamChunk{Err: errFeed})
				return
			}
			for _, frame := range frames {
				if !send(cliproxyexecutor.StreamChunk{Payload: wrapResponsesToolsSSEFrame(chunk.Payload, frame)}) {
					return
				}
			}
		}
	finish:
		tail, errFinish := attempt.Finish()
		if errFinish != nil {
			send(cliproxyexecutor.StreamChunk{Err: errFinish})
			return
		}
		for _, frame := range tail {
			if !send(cliproxyexecutor.StreamChunk{Payload: wrapResponsesToolsSSEFrame(nil, frame)}) {
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// normalizeCodexResponsesDataLine handles the Codex executor's line-oriented
// stream contract: one complete Responses event is one chunk. A bare data: JSON
// line is unwrapped to its payload; a redundant event:, id:, retry:, or comment
// field line is dropped; anything else passes through untouched so byte-split
// frames keep buffering.
func normalizeCodexResponsesDataLine(frame []byte) []byte {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return nil
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		if isRedundantCodexStreamField(trimmed) {
			return nil
		}
		return frame
	}
	data := bytes.TrimSpace(trimmed[len("data:"):])
	if bytes.IndexAny(data, "\r\n") >= 0 {
		return frame
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &event); err != nil || !strings.HasPrefix(event.Type, "response.") {
		return frame
	}
	return bytes.Clone(data)
}

// isRedundantCodexStreamField reports whether the chunk is one single-line SSE
// field line carrying no payload of its own. Keeping one would flip the feed
// into SSE framing, which the bare payload chunks cannot satisfy.
func isRedundantCodexStreamField(trimmed []byte) bool {
	if bytes.IndexAny(trimmed, "\r\n") >= 0 {
		return false
	}
	for _, prefix := range [][]byte{
		[]byte("event:"), []byte("id:"), []byte("retry:"), []byte(":"),
	} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// normalizeResponsesStreamFrame terminates a chunk that is one complete
// Responses SSE frame without its blank-line terminator, the shape the
// Responses translators emit. Anything else passes through untouched so
// byte-split frames keep buffering.
func normalizeResponsesStreamFrame(chunk []byte) []byte {
	if bytes.HasSuffix(chunk, []byte("\n\n")) || bytes.HasSuffix(chunk, []byte("\r\n\r\n")) {
		return chunk
	}
	if !isSelfContainedResponsesFrame(chunk) {
		return chunk
	}
	return append(bytes.TrimRight(chunk, "\r\n"), '\n', '\n')
}

// isSelfContainedResponsesFrame reports whether the chunk is exactly one
// complete Responses frame: only SSE field lines, and a data payload that is
// [DONE] or a response.* event object. Truncated JSON fails this test.
func isSelfContainedResponsesFrame(chunk []byte) bool {
	body := bytes.TrimRight(chunk, "\r\n")
	if len(bytes.TrimSpace(body)) == 0 {
		return false
	}
	var dataLines []string
	sawField := false
	for _, rawLine := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimSpace(bytes.TrimRight(rawLine, "\r"))
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			sawField = true
			dataLines = append(dataLines, string(bytes.TrimSpace(line[len("data:"):])))
		case bytes.HasPrefix(line, []byte("event:")), bytes.HasPrefix(line, []byte("id:")), bytes.HasPrefix(line, []byte("retry:")):
			sawField = true
		default:
			return false
		}
	}
	if !sawField || len(dataLines) == 0 {
		return false
	}
	payload := bytes.TrimSpace([]byte(strings.Join(dataLines, "\n")))
	if len(payload) == 0 {
		return false
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		return true
	}
	if !json.Valid(payload) {
		return false
	}
	var event struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &event) == nil && strings.HasPrefix(event.Type, "response.")
}

// wrapResponsesToolsSSEFrame keeps SSE framing intact across tool restore.
// Pass-through frames already carry their own framing and are forwarded
// untouched. Restored bare-JSON tool frames are wrapped as SSE data: frames
// so downstream validators and framers recognize them.
func wrapResponsesToolsSSEFrame(original, frame []byte) []byte {
	if len(frame) == 0 {
		return frame
	}
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return frame
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("id:")) || bytes.HasPrefix(trimmed, []byte("retry:")) ||
		bytes.HasPrefix(trimmed, []byte(":")) {
		return frame
	}
	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return append(append([]byte("data: "), trimmed...), []byte("\n\n")...)
	}
	eventType, _ := payload["type"].(string)
	metadata := responsesToolsSSEMetadata(original)
	wrapped := make([]byte, 0, len(metadata)+len(trimmed)+32)
	for _, line := range metadata {
		wrapped = append(wrapped, line...)
		wrapped = append(wrapped, '\n')
	}
	if eventType != "" {
		wrapped = append(wrapped, "event: "...)
		wrapped = append(wrapped, eventType...)
		wrapped = append(wrapped, '\n')
	}
	wrapped = append(wrapped, "data: "...)
	wrapped = append(wrapped, trimmed...)
	wrapped = append(wrapped, '\n', '\n')
	return wrapped
}

func responsesToolsSSEMetadata(original []byte) [][]byte {
	var metadata [][]byte
	for _, line := range bytes.Split(bytes.ReplaceAll(bytes.Clone(original), []byte("\r\n"), []byte("\n")), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("id:")) || bytes.HasPrefix(line, []byte("retry:")) {
			metadata = append(metadata, bytes.Clone(line))
		}
	}
	return metadata
}

// responsesToolsExecuteStream adapts one streaming call and reports whether
// the returned result carries adapted chunks. Callers keep their existing
// bootstrap, refresh, and wrap logic; adapted results already flow through
// the attempt feed before reaching readStreamBootstrap.
func (m *Manager) responsesToolsExecuteStream(
	ctx context.Context,
	auth *Auth,
	provider string,
	executor ProviderExecutor,
	execReq cliproxyexecutor.Request,
	execOpts cliproxyexecutor.Options,
	call func(context.Context, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, error) {
	result, _, err := m.responsesToolsStreamCall(ctx, auth, provider, executor, execReq, execOpts, call)
	return result, err
}
