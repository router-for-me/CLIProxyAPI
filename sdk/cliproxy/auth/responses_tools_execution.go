package auth

import (
	"context"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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
	call func(cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
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
	call func(cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
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
	call func(cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	if m == nil || call == nil {
		return cliproxyexecutor.Response{}, nil
	}
	// Only Responses-family input targeting a Responses-family downstream
	// enters adaptation. Anything else (or unknown) keeps legacy behavior even
	// when a route matches the model name.
	if !isResponsesFamily(execOpts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(execOpts)) {
		_ = toFormat
		resp, err := call(execReq, execOpts)
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
		resp, err := call(execReq, execOpts)
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
	resp, err := call(normalizedReq, normalizedOpts)
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
	call func(cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error),
) (cliproxyexecutor.Response, error) {
	if m == nil || call == nil {
		return cliproxyexecutor.Response{}, nil
	}
	if !isResponsesFamily(execOpts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(execOpts)) {
		return call(execReq, execOpts)
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
		return call(execReq, execOpts)
	}
	defer attempt.Close()
	normalizedReq := execReq
	normalizedReq.Payload = wireBody
	normalizedOpts := execOpts
	if len(execOpts.OriginalRequest) > 0 {
		normalizedOpts.OriginalRequest = wireBody
	}
	execCtx = withResponsesToolsContract(execCtx, wire)
	return call(normalizedReq, normalizedOpts)
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
	call func(cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, bool, error) {
	if m == nil || call == nil {
		return nil, false, nil
	}
	if !isResponsesFamily(execOpts.SourceFormat, cliproxyexecutor.ResponseFormatOrSource(execOpts)) {
		result, err := call(execReq, execOpts)
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
		result, err := call(execReq, execOpts)
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
	parentCtx = withResponsesToolsContract(parentCtx, wire)
	result, err := call(normalizedReq, normalizedOpts)
	if err != nil {
		attempt.Close()
		return nil, false, err
	}
	if result == nil || result.Chunks == nil {
		attempt.Close()
		return result, false, nil
	}
	adapted := adaptResponsesToolsStream(parentCtx, attempt, result)
	return adapted, true, nil
}

// adaptResponsesToolsStream converts one upstream chunk channel through the
// attempt feed. Feed output of zero events means buffered, never
// pass-through. Terminal stream errors emit once and cancel the attempt.
func adaptResponsesToolsStream(parentCtx context.Context, attempt interface {
	Feed([]byte) ([][]byte, error)
	Finish() ([][]byte, error)
	Close()
}, result *cliproxyexecutor.StreamResult) *cliproxyexecutor.StreamResult {
	if attempt == nil || result == nil {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	ctx, cancel := context.WithCancel(parentCtx)
	go func() {
		defer close(out)
		defer cancel()
		defer attempt.Close()
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx == nil {
				select {
				case out <- chunk:
					return true
				default:
					return false
				}
			}
			select {
			case <-ctx.Done():
				return false
			case out <- chunk:
				return true
			}
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				if tail, errTail := attempt.Finish(); errTail != nil {
					if !send(cliproxyexecutor.StreamChunk{Err: errTail}) {
						return
					}
				} else {
					for _, frame := range tail {
						if !send(cliproxyexecutor.StreamChunk{Payload: frame}) {
							return
						}
					}
				}
				send(cliproxyexecutor.StreamChunk{Err: chunk.Err})
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			frames, errFeed := attempt.Feed(chunk.Payload)
			if errFeed != nil {
				send(cliproxyexecutor.StreamChunk{Err: errFeed})
				return
			}
			for _, frame := range frames {
				if !send(cliproxyexecutor.StreamChunk{Payload: frame}) {
					return
				}
			}
		}
		tail, errFinish := attempt.Finish()
		if errFinish != nil {
			send(cliproxyexecutor.StreamChunk{Err: errFinish})
			return
		}
		for _, frame := range tail {
			if !send(cliproxyexecutor.StreamChunk{Payload: frame}) {
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
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
	call func(cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error),
) (*cliproxyexecutor.StreamResult, error) {
	result, _, err := m.responsesToolsStreamCall(ctx, auth, provider, executor, execReq, execOpts, call)
	return result, err
}
