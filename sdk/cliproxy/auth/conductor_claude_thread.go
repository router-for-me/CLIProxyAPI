package auth

import (
	"context"
	"net/http"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// withClaudeThreadAuth pins continuations before ordinary selection, including
// default round-robin. It never bypasses the selector's availability checks.
func withClaudeThreadAuth(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Options, error) {
	if opts.SourceFormat != sdktranslator.FormatClaude {
		return opts, nil
	}
	hasClaude := false
	for _, provider := range providers {
		if provider == "claude" {
			hasClaude = true
			break
		}
	}
	if !hasClaude {
		return opts, nil
	}
	previousID := ojson.GetBytesNoCopy(req.Payload, "thread.previous_message_id").String()
	if previousID == "" {
		return opts, nil
	}
	callerScope := cliproxysession.ClaudeThreadCallerScope(coreusage.APIKeyFromContext(ctx), opts.Metadata, req.Metadata)
	owner, found, errOwner := cliproxysession.ClaudeThreadAuth(ctx, callerScope, previousID)
	if errOwner != nil {
		if ctx != nil && ctx.Err() != nil {
			return opts, ctx.Err()
		}
		log.WithError(errOwner).Warn("Claude Thread credential binding lookup failed")
		return opts, &Error{Code: "thread_auth_unavailable", Message: "Claude Thread credential binding is temporarily unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}
	if !found {
		return opts, nil
	}
	if pinned := pinnedAuthIDFromMetadata(opts.Metadata); pinned != "" && pinned != owner {
		return opts, &Error{Code: "thread_auth_conflict", Message: "the pinned credential does not own this Claude Thread; replay with thread create", HTTPStatus: http.StatusConflict}
	}
	opts.Metadata = cloneRequestMetadata(opts.Metadata)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = owner
	return opts, nil
}
