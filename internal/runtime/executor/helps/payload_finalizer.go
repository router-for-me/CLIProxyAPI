package helps

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// PayloadFinalizer applies user rules to a fully prepared business payload.
// Each rebuilt attempt must start from the unconfigured body, not a previous result.
type PayloadFinalizer func([]byte) []byte

// NewPayloadFinalizer snapshots matching context before built-in request mutations.
func NewPayloadFinalizer(cfg *config.Config, executor, model, protocol, root string, original []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) PayloadFinalizer {
	requestedModel := PayloadRequestedModel(opts, req.Model)
	requestPath := PayloadRequestPath(opts)
	headers := opts.Headers.Clone()
	source := append([]byte(nil), original...)
	return func(body []byte) []byte {
		return ApplyPayloadConfigWithRequestForExecutor(cfg, executor, model, protocol, opts.SourceFormat.String(), root, body, source, requestedModel, requestPath, headers)
	}
}

type payloadFinalizerKey struct{}

// WithPayloadFinalizer carries the final barrier through shared request builders.
func WithPayloadFinalizer(ctx context.Context, finalize PayloadFinalizer) context.Context {
	return context.WithValue(ctx, payloadFinalizerKey{}, finalize)
}

// FinalizePayload runs immediately before serialization or transport framing.
func FinalizePayload(ctx context.Context, body []byte) []byte {
	if finalize, ok := ctx.Value(payloadFinalizerKey{}).(PayloadFinalizer); ok && finalize != nil {
		return finalize(body)
	}
	return body
}
