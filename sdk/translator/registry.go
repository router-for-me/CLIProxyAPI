package translator

import (
	"context"
	"sync"
)

// Registry manages translation functions across schemas.
type Registry struct {
	mu        sync.RWMutex
	requests  map[Format]map[Format]RequestTransform
	responses map[Format]map[Format]ResponseTransform
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

// formatAliases returns compatible aliases for a format, ordered by preference.
func formatAliases(format Format) []Format {
	switch format {
	case "codex":
		return []Format{"codex", "openai-response"}
	case "openai-response":
		return []Format{"openai-response", "codex"}
	default:
		return []Format{format}
	}
}

// TranslateRequest converts a payload between schemas, returning the original payload
// if no translator is registered.
func (r *Registry) TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, fromFormat := range formatAliases(from) {
		if byTarget, ok := r.requests[fromFormat]; ok {
			for _, toFormat := range formatAliases(to) {
				if fn, isOk := byTarget[toFormat]; isOk && fn != nil {
					return fn(model, rawJSON, stream)
				}
			}
		}
	}
	return rawJSON
}

// HasResponseTransformer indicates whether a response translator exists.
func (r *Registry) HasResponseTransformer(from, to Format) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, toFormat := range formatAliases(to) {
		if byTarget, ok := r.responses[toFormat]; ok {
			for _, fromFormat := range formatAliases(from) {
				if _, isOk := byTarget[fromFormat]; isOk {
					return true
				}
			}
		}
	}
	return false
}

// TranslateStream applies the registered streaming response translator.
func (r *Registry) TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, toFormat := range formatAliases(to) {
		if byTarget, ok := r.responses[toFormat]; ok {
			for _, fromFormat := range formatAliases(from) {
				if fn, isOk := byTarget[fromFormat]; isOk && fn.Stream != nil {
					return fn.Stream(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
				}
			}
		}
	}
	return []string{string(rawJSON)}
}

// TranslateNonStream applies the registered non-stream response translator.
func (r *Registry) TranslateNonStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, toFormat := range formatAliases(to) {
		if byTarget, ok := r.responses[toFormat]; ok {
			for _, fromFormat := range formatAliases(from) {
				if fn, isOk := byTarget[fromFormat]; isOk && fn.NonStream != nil {
					return fn.NonStream(ctx, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
				}
			}
		}
	}
	return string(rawJSON)
}

// TranslateNonStream applies the registered non-stream response translator.
func (r *Registry) TranslateTokenCount(ctx context.Context, from, to Format, count int64, rawJSON []byte) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, toFormat := range formatAliases(to) {
		if byTarget, ok := r.responses[toFormat]; ok {
			for _, fromFormat := range formatAliases(from) {
				if fn, isOk := byTarget[fromFormat]; isOk && fn.TokenCount != nil {
					return fn.TokenCount(ctx, count)
				}
			}
		}
	}
	return string(rawJSON)
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

// TranslateRequest is a helper on the default registry.
func TranslateRequest(from, to Format, model string, rawJSON []byte, stream bool) []byte {
	return defaultRegistry.TranslateRequest(from, to, model, rawJSON, stream)
}

// HasResponseTransformer inspects the default registry.
func HasResponseTransformer(from, to Format) bool {
	return defaultRegistry.HasResponseTransformer(from, to)
}

// TranslateStream is a helper on the default registry.
func TranslateStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	return defaultRegistry.TranslateStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateNonStream is a helper on the default registry.
func TranslateNonStream(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) string {
	return defaultRegistry.TranslateNonStream(ctx, from, to, model, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

// TranslateTokenCount is a helper on the default registry.
func TranslateTokenCount(ctx context.Context, from, to Format, count int64, rawJSON []byte) string {
	return defaultRegistry.TranslateTokenCount(ctx, from, to, count, rawJSON)
}
