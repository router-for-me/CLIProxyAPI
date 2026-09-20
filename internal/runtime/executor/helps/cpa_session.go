package helps

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// EnsureSessionContext ensures that ctx carries the internal session identity
// for $CPA-SESSION-ID expansion in custom headers.
func EnsureSessionContext(ctx context.Context, opts cliproxyexecutor.Options, payload []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id := util.SessionIDFromContext(ctx); id != "" {
		return ctx
	}
	if util.HasExplicitSessionID(ctx) {
		return ctx
	}
	if meta := logging.GetClientRequestMetadata(ctx); meta.SessionID != "" {
		return util.WithSessionID(ctx, meta.SessionID)
	}
	evalPayload := opts.OriginalRequest
	if len(evalPayload) == 0 {
		evalPayload = payload
	}
	canonical := cliproxyauth.CanonicalSessionID(opts.Headers, evalPayload, opts.Metadata)
	if canonical != "" {
		return util.WithSessionID(ctx, canonical)
	}
	return ctx
}
