package executor

import (
	"context"
	"errors"
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
//
// On the "flex" tier, a 503 from the upstream signals capacity has been shed
// and the request is retried exactly once on the "default" tier. The retry
// stamps "flex_downgraded": true on the ctx-scoped provider metadata so the
// dashboard / audit log can show the shed.
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

// shouldRetryFlex503 reports whether err is a 503 from the compat executor AND
// the credential asks for the "flex" service tier. The tier guard is the
// critical half: only flex requests get the flex→default shed; default
// (or absent) requests propagate a 503 untouched so the caller can decide
// whether to retry against another credential.
//
// StatusError is detected via errors.As so a wrapped error from a future
// plugin or middleware path would still resolve to the underlying status.
func shouldRetryFlex503(auth *cliproxyauth.Auth, err error) bool {
	if err == nil || auth == nil {
		return false
	}
	if auth.Attributes["service_tier"] != "flex" {
		return false
	}
	var statusErr cliproxyexecutor.StatusError
	if !errors.As(err, &statusErr) || statusErr == nil {
		return false
	}
	return statusErr.StatusCode() == http.StatusServiceUnavailable
}

// retryAuthWithDefaultTier returns a shallow copy of auth whose Attributes map
// is freshly allocated (so the retry does not leak back into the caller's
// auth) and whose service_tier is forced to "default". A nil auth yields nil;
// a non-nil auth without an Attributes map gets one allocated for the single
// override.
func retryAuthWithDefaultTier(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	retryAuth := *auth
	attrs := make(map[string]string, len(auth.Attributes)+1)
	for k, v := range auth.Attributes {
		attrs[k] = v
	}
	attrs["service_tier"] = "default"
	retryAuth.Attributes = attrs
	return &retryAuth
}

// stampFlexDowngraded records that the current request was retried from flex
// to default. The flag lives in the ctx-scoped provider usage metadata holder
// so the response sink (which fires on the retry's success path) carries it
// into the usage record. It must be called AFTER the first attempt has
// failed (so successful first tries don't get a false shed) and BEFORE the
// retry call (so the sink captures it on the retry response, not the failed
// first try).
func stampFlexDowngraded(ctx context.Context) {
	helps.SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"flex_downgraded": true})
}

// Execute delegates to the OpenAI-compatible inference path. On a flex-503,
// retries the request once on the default tier and stamps flex_downgraded on
// the retried request's metadata.
func (e *NeuralwattExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	// Preserve the pre-stamp payload so the retry can rebuild the body from
	// the original input rather than from the already-flex-stamped attempt.
	originalPayload := req.Payload
	req.Payload = applyNeuralwattServiceTier(req.Payload, auth)
	ctx = helps.EnsureProviderUsageMetadata(ctx)
	resp, err := e.compat.Execute(ctx, auth, req, opts)
	if err == nil {
		return resp, nil
	}
	if !shouldRetryFlex503(auth, err) {
		return resp, err
	}
	stampFlexDowngraded(ctx)
	retryAuth := retryAuthWithDefaultTier(auth)
	req.Payload = applyNeuralwattServiceTier(originalPayload, retryAuth)
	return e.compat.Execute(ctx, retryAuth, req, opts)
}

// ExecuteStream delegates to the OpenAI-compatible streaming path. The flex
// retry here is well-defined because the compat executor's ExecuteStream
// returns the statusErr BEFORE opening the chunk channel (line ~461 of
// openai_compat_executor.go): a 503 cannot be followed by partial chunks.
// The retry call therefore starts from a clean slate, just like Execute.
func (e *NeuralwattExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("neuralwatt executor: compat executor is nil")
	}
	originalPayload := req.Payload
	req.Payload = applyNeuralwattServiceTier(req.Payload, auth)
	ctx = helps.EnsureProviderUsageMetadata(ctx)
	result, err := e.compat.ExecuteStream(ctx, auth, req, opts)
	if err == nil {
		return result, nil
	}
	if !shouldRetryFlex503(auth, err) {
		return result, err
	}
	stampFlexDowngraded(ctx)
	retryAuth := retryAuthWithDefaultTier(auth)
	req.Payload = applyNeuralwattServiceTier(originalPayload, retryAuth)
	return e.compat.ExecuteStream(ctx, retryAuth, req, opts)
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
