package translator

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Registry manages translation functions across schemas.
type Registry struct {
	mu        sync.RWMutex
	requests  map[Format]map[Format]RequestTransform
	responses map[Format]map[Format]ResponseTransform
	hooks     PluginHooks
}

// NewRegistry constructs an empty translator registry.
func NewRegistry() *Registry {
	return &Registry{
		requests:  make(map[Format]map[Format]RequestTransform),
		responses: make(map[Format]map[Format]ResponseTransform),
	}
}

// Register stores request/response transforms between two formats.
func (r *Registry) Register(from, to Format, request RequestTransform, response ResponseTransform) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.requests[from]; !ok {
		r.requests[from] = make(map[Format]RequestTransform)
	}
	if request != nil {
		r.requests[from][to] = request
	}

	if _, ok := r.responses[from]; !ok {
		r.responses[from] = make(map[Format]ResponseTransform)
	}
	r.responses[from][to] = response
}

// SetPluginHooks stores translator plugin hooks for this registry.
func (r *Registry) SetPluginHooks(hooks PluginHooks) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hooks = hooks
}

// TranslateRequest converts a payload between schemas, returning the original payload
// if no translator is registered. When falling back to the original payload, the
// "model" field is still updated to match the resolved model name so that
// client-side prefixes (e.g. "copilot/gpt-5-mini") are not leaked upstream.
func (r *Registry) TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	r.mu.RLock()
	var fn RequestTransform
	if byTarget, ok := r.requests[from]; ok {
		fn = byTarget[to]
	}
	hooks := r.hooks
	r.mu.RUnlock()

	body := rawJSON
	if fn != nil {
		body = fn(model, body, stream)
	} else {
		if model != "" && gjson.GetBytes(body, "model").String() != model {
			if updated, err := sjson.SetBytes(body, "model", model); err != nil {
				log.Warnf("translator: failed to normalize model in request fallback: %v", err)
			} else {
				body = updated
			}
		}
	}

	if hooks != nil {
		body = hooks.NormalizeRequest(context.Background(), from, to, model, body, stream)
		if fn == nil {
			if translated, ok := hooks.TranslateRequest(context.Background(), from, to, model, body, stream); ok {
				body = translated
			}
		}
	}
	return body
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
		if _, isOk := byTarget[to]; isOk {
			return true
		}
	}
	return false
}

// TranslateStream applies the registered streaming response translator.
func (r *Registry) TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	r.mu.RLock()
	var fn ResponseTransform
	if byTarget, ok := r.responses[to]; ok {
		fn = byTarget[from]
	}
	hooks := r.hooks
	r.mu.RUnlock()

	body := rawJSON
	if hooks != nil {
		body = hooks.NormalizeResponseBefore(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true)
	}

	var outputs [][]byte
	if fn.Stream != nil {
		outputs = fn.Stream(ctx, model, originalRequestRawJSON, requestRawJSON, body, param)
	} else if hooks != nil {
		if translated, ok := hooks.TranslateResponse(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, body, true); ok {
			outputs = [][]byte{translated}
		}
	}
	if outputs == nil {
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

var defaultRegistry = NewRegistry()

// Default exposes the package-level registry for shared use.
func Default() *Registry {
	return defaultRegistry
}

// Register attaches transforms to the default registry.
func Register(from, to Format, request RequestTransform, response ResponseTransform) {
	defaultRegistry.Register(from, to, request, response)
}

// SetPluginHooks stores plugin hooks on the default registry.
func SetPluginHooks(hooks PluginHooks) {
	defaultRegistry.SetPluginHooks(hooks)
}

// TranslateRequest is a helper on the default registry.
func TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	return defaultRegistry.TranslateRequest(from, to, model, rawJSON, stream)
}

// HasRequestTransformer inspects the default registry.
func HasRequestTransformer(from, to Format) bool {
	return defaultRegistry.HasRequestTransformer(from, to)
}

// HasResponseTransformer inspects the default registry.
func HasResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasResponseTransformer(from, to)
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
