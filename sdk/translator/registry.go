package translator

import (
	"context"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Registry manages translation functions across schemas.
type Registry struct {
	mu        sync.RWMutex
	requests  map[Format]map[Format]RequestEnvelopeTransform
	responses map[Format]map[Format]ResponseTransform
	hooks     PluginHooks
}

// NewRegistry constructs an empty translator registry.
func NewRegistry() *Registry {
	return &Registry{
		requests:  make(map[Format]map[Format]RequestEnvelopeTransform),
		responses: make(map[Format]map[Format]ResponseTransform),
	}
}

// Register stores request/response transforms between two formats.
func (r *Registry) Register(from, to Format, request RequestTransform, response ResponseTransform) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.requests[from]; !ok {
		r.requests[from] = make(map[Format]RequestEnvelopeTransform)
	}
	if request != nil {
		r.requests[from][to] = func(_ context.Context, req RequestEnvelope) RequestEnvelope {
			req.Body = request(req.Model, req.Body, req.Stream)
			return req
		}
	}

	if _, ok := r.responses[from]; !ok {
		r.responses[from] = make(map[Format]ResponseTransform)
	}
	r.responses[from][to] = response
}

// RegisterRequestEnvelope stores a request transform that consumes the complete
// request envelope, including request-scoped model metadata.
func (r *Registry) RegisterRequestEnvelope(from, to Format, request RequestEnvelopeTransform) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.requests[from]; !ok {
		r.requests[from] = make(map[Format]RequestEnvelopeTransform)
	}
	if request != nil {
		r.requests[from][to] = request
	}
}

// SetPluginHooks stores translator plugin hooks for this registry.
func (r *Registry) SetPluginHooks(hooks PluginHooks) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hooks = hooks
}

// HasPluginHooks reports whether request or response translation hooks are installed.
func (r *Registry) HasPluginHooks() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.hooks != nil
}

// TranslateRequest converts a payload between schemas, returning the original payload
// if no translator is registered. When falling back to the original payload, the
// "model" field is still updated to match the resolved model name so that
// client-side prefixes (e.g. "copilot/gpt-5-mini") are not leaked upstream.
func (r *Registry) TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	req := r.TranslateRequestEnvelope(context.Background(), from, to, RequestEnvelope{
		Format: from,
		Model:  model,
		Stream: stream,
		Body:   rawJSON,
	})
	return req.Body
}

// TranslateRequestEnvelope translates a complete request envelope while preserving
// request-scoped metadata for the selected transform.
func (r *Registry) TranslateRequestEnvelope(ctx context.Context, from, to Format, req RequestEnvelope) RequestEnvelope {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	var fn RequestEnvelopeTransform
	if byTarget, ok := r.requests[from]; ok {
		fn = byTarget[to]
	}
	hooks := r.hooks
	r.mu.RUnlock()

	if fn != nil {
		summaryConfig := thinking.ExtractSummaryConfig(req.Body, from.String())
		req = fn(ctx, req)
		req.Body = thinking.ApplySummaryConfigForModel(req.Body, to.String(), req.Model, summaryConfig)
		if hooks != nil {
			// Request normalizers run after native translation and own the final
			// provider payload, including any summary field they remove.
			req.Body = hooks.NormalizeRequest(ctx, from, to, req.Model, req.Body, req.Stream)
		}
		req.Format = to
		return req
	}

	if req.Model != "" && gjson.GetBytes(req.Body, "model").String() != req.Model {
		if updated, err := sjson.SetBytes(req.Body, "model", req.Model); err != nil {
			log.Warnf("translator: failed to normalize model in request fallback: %v", err)
		} else {
			req.Body = updated
		}
	}
	if hooks == nil {
		// No translation occurred. Preserve the documented fallback shape instead
		// of mixing target-protocol summary fields into the source payload.
		req.Format = to
		return req
	}

	// Plugin request normalizers canonicalize the source before a plugin request
	// translator gets a chance to handle a missing native route. Extract summary
	// intent from that normalized source so a normalizer can remove or rewrite it.
	req.Body = hooks.NormalizeRequest(ctx, from, to, req.Model, req.Body, req.Stream)
	summaryConfig := thinking.ExtractSummaryConfig(req.Body, from.String())
	if translated, ok := hooks.TranslateRequest(ctx, from, to, req.Model, req.Body, req.Stream); ok {
		req.Body = thinking.ApplySummaryConfigForModel(translated, to.String(), req.Model, summaryConfig)
	}
	req.Format = to
	return req
}

// HasRequestTransformer indicates whether a request translator exists.
func (r *Registry) HasRequestTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.requests[from]; ok {
		if fn, isOk := byTarget[to]; isOk && fn != nil {
			return true
		}
	}
	return false
}

// HasResponseTransformer indicates whether a response translator exists.
func (r *Registry) HasResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[from]; ok {
		if fn, isOk := byTarget[to]; isOk && hasAnyResponseTransform(fn) {
			return true
		}
	}
	return false
}

// HasStreamResponseTransformer indicates whether a streaming response translator exists.
func (r *Registry) HasStreamResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[from]; ok {
		if fn, isOk := byTarget[to]; isOk && fn.Stream != nil {
			return true
		}
	}
	return false
}

// HasNonStreamResponseTransformer indicates whether a non-streaming response translator exists.
func (r *Registry) HasNonStreamResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[from]; ok {
		if fn, isOk := byTarget[to]; isOk && fn.NonStream != nil {
			return true
		}
	}
	return false
}

// TranslateStream applies the registered streaming response translator.
func (r *Registry) TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	r.mu.RLock()
	var stream ResponseStreamTransform
	if byTarget, ok := r.responses[to]; ok {
		stream = byTarget[from].Stream
	}
	hooks := r.hooks
	r.mu.RUnlock()

	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true)
	}

	var outputs [][]byte
	usedNativeTransform := false
	if stream != nil {
		usedNativeTransform = true
		outputs = stream(ctx, model, originalRequestRawJSON, requestRawJSON, body, param)
	} else if hooks != nil {
		if translated, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true); ok {
			outputs = [][]byte{translated}
		}
	}
	if outputs == nil && !usedNativeTransform {
		outputs = [][]byte{body}
	}
	if hooks != nil {
		for i, output := range outputs {
			outputs[i] = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, output, true)
		}
	}
	return outputs
}

// TranslateNonStream applies the registered non-stream response translator.
func (r *Registry) TranslateNonStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	r.mu.RLock()
	var fn ResponseTransform
	if byTarget, ok := r.responses[to]; ok {
		fn = byTarget[from]
	}
	hooks := r.hooks
	r.mu.RUnlock()

	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
	}
	if fn.NonStream != nil {
		body = fn.NonStream(ctx, model, originalRequestRawJSON, requestRawJSON, body, param)
	} else if hooks != nil {
		if translated, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false); ok {
			body = translated
		}
	}
	if hooks != nil {
		body = hooks.NormalizeResponseAfter(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, false)
	}
	return body
}

// TranslateTokenCount applies the registered token count response translator.
func (r *Registry) TranslateTokenCount(ctx context.Context, from, to Format, count int64, rawJSON []byte) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if byTarget, ok := r.responses[to]; ok {
		if fn, isOk := byTarget[from]; isOk && fn.TokenCount != nil {
			return fn.TokenCount(ctx, count)
		}
	}
	return rawJSON
}

// NormalizeRequest executes registered plugin request normalizer hooks, returning
// the payload unmodified if no hooks are registered.
func (r *Registry) NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	hooks := r.hooks
	r.mu.RUnlock()

	if hooks != nil {
		return hooks.NormalizeRequest(ctx, from, to, model, body, stream)
	}
	return body
}

var defaultRegistry = NewRegistry()

// Default exposes the package-level registry for shared use.
func Default() *Registry {
	return defaultRegistry
}

// Register attaches transforms to the default registry.
func Register(from, to Format, request RequestTransform, response ResponseTransform) {
	defaultRegistry.Register(from, to, request, response)
}

// RegisterRequestEnvelope stores an envelope-aware transform on the default registry.
func RegisterRequestEnvelope(from, to Format, request RequestEnvelopeTransform) {
	defaultRegistry.RegisterRequestEnvelope(from, to, request)
}

// SetPluginHooks stores plugin hooks on the default registry.
func SetPluginHooks(hooks PluginHooks) {
	defaultRegistry.SetPluginHooks(hooks)
}

// HasPluginHooks reports whether hooks are installed on the default registry.
func HasPluginHooks() bool {
	return defaultRegistry.HasPluginHooks()
}

// TranslateRequest is a helper on the default registry.
func TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	return defaultRegistry.TranslateRequest(from, to, model, rawJSON, stream)
}

// TranslateRequestEnvelope translates a complete request envelope using the default registry.
func TranslateRequestEnvelope(ctx context.Context, from, to Format, req RequestEnvelope) RequestEnvelope {
	return defaultRegistry.TranslateRequestEnvelope(ctx, from, to, req)
}

// NormalizeRequest executes registered plugin request normalizer hooks on the default registry.
func NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte {
	return defaultRegistry.NormalizeRequest(ctx, from, to, model, body, stream)
}

// HasRequestTransformer inspects the default registry.
func HasRequestTransformer(from, to Format) bool {
	return defaultRegistry.HasRequestTransformer(from, to)
}

// HasResponseTransformer inspects the default registry.
func HasResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasResponseTransformer(from, to)
}

// HasStreamResponseTransformer inspects the default registry for a streaming response translator.
func HasStreamResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasStreamResponseTransformer(from, to)
}

// HasNonStreamResponseTransformer inspects the default registry for a non-streaming response translator.
func HasNonStreamResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasNonStreamResponseTransformer(from, to)
}

// TranslateStream is a helper on the default registry.
func TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	return defaultRegistry.TranslateStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateNonStream is a helper on the default registry.
func TranslateNonStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	return defaultRegistry.TranslateNonStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateTokenCount is a helper on the default registry.
func TranslateTokenCount(ctx context.Context, from, to Format, count int64, rawJSON []byte) []byte {
	return defaultRegistry.TranslateTokenCount(ctx, from, to, count, rawJSON)
}

func hasAnyResponseTransform(fn ResponseTransform) bool {
	return fn.Stream != nil || fn.NonStream != nil || fn.TokenCount != nil
}
