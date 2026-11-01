package executor

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Muse returns reasoning as an opaque encrypted_content envelope, and Meta binds
// each envelope to the account that issued it: replaying one through any other
// account is rejected with 400 "reasoning encrypted_content was not issued to
// this caller". A client such as Claude Code resends the whole history on every
// turn, and that history mixes envelopes from every pool account that served an
// earlier turn, so forwarding it verbatim fails in production even though a
// single-account test passes.
//
// The feature therefore carries provenance end to end:
//
//   - Responses leaving this executor have each reasoning envelope wrapped as
//     meta#<account-key>#<envelope> (see signature.TagMetaReasoning), whether
//     the client reads it as an Anthropic thinking signature or as a Responses
//     encrypted_content, streaming or not.
//   - Requests entering this executor keep an envelope only when its tag names
//     the credential selected for this very request, and strip the tag first.
//     Everything else (another account's tag, an untagged legacy envelope) is
//     dropped. With the feature off, untagged envelopes are forwarded as is.
//   - If Meta still rejects a replayed envelope, the request is retried once
//     with every envelope removed, so the caller never sees that 400.
//
// The replay policy itself (account-key derivation, per-request policy, stats
// and tag rewriting) is standalone support code and lives in helps; only the
// *MetaExecutor-bound integration stays here.

func (e *MetaExecutor) newMetaReasoningReplay(opts cliproxyexecutor.Options, requestModel, baseModel string, auth *cliproxyauth.Auth) helps.MetaReasoningReplay {
	replay := helps.MetaReasoningReplay{Stats: &helps.MetaReasoningReplayStats{}}
	replay.BaseURL, _ = metaCreds(auth)
	if e == nil || e.cfg == nil {
		return replay
	}
	requested := helps.PayloadRequestedModel(opts, requestModel)
	if !e.cfg.Meta.ReasoningReplay.AppliesToModel(requested, requestModel, baseModel) {
		return replay
	}
	replay.AccountKey = helps.MetaReasoningAccountKey(auth)
	replay.Enabled = replay.AccountKey != ""
	return replay
}

// sanitizeMetaReasoningRequest applies the account policy to a Responses request body and
// then the native-only Meta sanitizer, which still strips anything that is not
// a recognized Meta envelope (fail-closed).
func sanitizeMetaReasoningRequest(ctx context.Context, replay helps.MetaReasoningReplay, body []byte) []byte {
	body = replay.ApplyAccountPolicy(ctx, body)
	return sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURL(ctx, "meta executor", body, replay.BaseURL)
}
