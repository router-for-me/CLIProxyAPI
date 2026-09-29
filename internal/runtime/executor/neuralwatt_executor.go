package executor

import (
	"context"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// neuralwattCompat is the subset of OpenAICompatExecutor that NeuralwattExecutor
// delegates to. It exists so tests can inject a fake that captures the context
// handed to each entry point and assert the metadata holder was installed.
// Refresh is intentionally excluded: the wrapper keeps it as a no-op and does
// not delegate the method, so it does not belong in this seam.
type neuralwattCompat interface {
	Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error
	HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error)
}

// NeuralwattExecutor executes inference for the Neuralwatt provider.
//
// Neuralwatt's API is OpenAI-compatible: a static bearer API key authenticates
// inference against https://api.neuralwatt.com/v1. The executor therefore
// delegates all inference to the shared OpenAI-compatible executor bound to
// the "neuralwatt" provider key, and additionally installs a response sink
// (neuralwatt_metadata.go) that captures Neuralwatt's cost headers, SSE cost
// comment, and energy object into the usage record.
type NeuralwattExecutor struct {
	compat neuralwattCompat
	cfg    *config.Config
}

// NewNeuralwattExecutor creates a Neuralwatt executor. The provider may be the
// bare executor channel; inference is routed by the shared OpenAI-compatible
// path.
func NewNeuralwattExecutor(cfg *config.Config) *NeuralwattExecutor {
	compat := NewOpenAICompatExecutor("neuralwatt", cfg)
	compat.SetResponseSink(neuralwattResponseSink{})
	return &NeuralwattExecutor{compat: compat, cfg: cfg}
}

// Identifier returns the provider identifier.
func (e *NeuralwattExecutor) Identifier() string {
	return "neuralwatt"
}

// Execute delegates to the OpenAI-compatible inference path.
func (e *NeuralwattExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	req.Payload = applyNeuralwattServiceTier(req.Payload, auth)
	ctx = helps.EnsureProviderUsageMetadata(ctx)
	return e.compat.Execute(ctx, auth, req, opts)
}

// ExecuteStream delegates to the OpenAI-compatible streaming path.
func (e *NeuralwattExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	req.Payload = applyNeuralwattServiceTier(req.Payload, auth)
	ctx = helps.EnsureProviderUsageMetadata(ctx)
	return e.compat.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens delegates to the OpenAI-compatible token accounting path.
func (e *NeuralwattExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.CountTokens(ctx, auth, req, opts)
}

// PrepareRequest injects Neuralwatt credentials into the outgoing HTTP request.
func (e *NeuralwattExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if e == nil || e.compat == nil {
		return fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	return e.compat.PrepareRequest(req, auth)
}

// HttpRequest injects Neuralwatt credentials into the request and executes it.
func (e *NeuralwattExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	ctx = helps.EnsureProviderUsageMetadata(ctx)
	return e.compat.HttpRequest(ctx, auth, req)
}

// Refresh is a no-op: Neuralwatt credentials are static API keys with no OAuth
// or minted-token refresh. Returning the auth unchanged keeps the conductor's
// refresh loop from erroring on this channel.
func (e *NeuralwattExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}
