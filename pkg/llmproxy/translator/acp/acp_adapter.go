// Package acp provides an ACP (Agent Communication Protocol) translator for CLIProxy.
// acp_adapter.go implements translation between Claude/OpenAI format and ACP format,
// and a lightweight registry for adapter lookup.
package acp

import (
	"context"
	"fmt"
)

// Adapter translates between Claude/OpenAI request format and ACP format.
type Adapter interface {
	// Translate converts a ChatCompletionRequest to an ACPRequest.
	Translate(ctx context.Context, req *ChatCompletionRequest) (*ACPRequest, error)
}

// ACPAdapter implements the Adapter interface.
type ACPAdapter struct {
	baseURL string
}

// NewACPAdapter returns an ACPAdapter configured to forward requests to baseURL.
func NewACPAdapter(baseURL string) *ACPAdapter {
	return &ACPAdapter{baseURL: baseURL}
}

// Translate converts a ChatCompletionRequest to an ACPRequest.
// Message role and content fields are preserved verbatim; the model ID is passed through.
func (a *ACPAdapter) Translate(_ context.Context, req *ChatCompletionRequest) (*ACPRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("request must not be nil")
	}
	acpMessages := make([]ACPMessage, len(req.Messages))
	for i, m := range req.Messages {
		acpMessages[i] = ACPMessage{Role: m.Role, Content: m.Content}
	}
	return &ACPRequest{
		Model:    req.Model,
		Messages: acpMessages,
	}, nil
}

// Registry is a simple name-keyed registry of Adapter instances.
type Registry struct {
	adapters map[string]Adapter
}

// NewTranslatorRegistry returns a Registry pre-populated with the default ACP adapter.
func NewTranslatorRegistry() *Registry {
	r := &Registry{adapters: make(map[string]Adapter)}
	// Register the ACP adapter by default.
	r.Register("acp", NewACPAdapter("http://localhost:9000"))
	return r
}

// Register stores an adapter under the given name.
func (r *Registry) Register(name string, adapter Adapter) {
	r.adapters[name] = adapter
}

// HasTranslator reports whether an adapter is registered for name.
func (r *Registry) HasTranslator(name string) bool {
	_, ok := r.adapters[name]
	return ok
}

// GetTranslator returns the adapter registered under name, or nil when absent.
func (r *Registry) GetTranslator(name string) Adapter {
	return r.adapters[name]
}
