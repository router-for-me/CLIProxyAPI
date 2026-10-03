package helps

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// ApplyPatchUpstreamErrorMessage deliberately excludes upstream JSON and patch text.
const ApplyPatchUpstreamErrorMessage = "Invalid apply_patch tool arguments received from upstream."

// ApplyPatchTranslationError consumes only the optional translator error state.
func ApplyPatchTranslationError(param any) error {
	state, okState := param.(interface{ ToolInputError() error })
	if !okState {
		return nil
	}
	return state.ToolInputError()
}

// FinalizeApplyPatchStream notifies request-local state of a transport ending.
// It must never manufacture a successful protocol terminator.
func FinalizeApplyPatchStream(param any) [][]byte {
	state, okState := param.(interface{ FinalizeToolInput() [][]byte })
	if !okState {
		return nil
	}
	return state.FinalizeToolInput()
}

// RecordApplyPatchStreamFailure records validation before delivery can be canceled.
func RecordApplyPatchStreamFailure(ctx context.Context, param any, reporter *UsageReporter, gatewayErr error) bool {
	if ApplyPatchTranslationError(param) == nil {
		return false
	}
	reporter.PublishFailure(ctx, gatewayErr)
	return true
}

// StopApplyPatchStream propagates a retained failure after its one translated frame.
// The caller supplies its existing sanitized gateway status error.
func StopApplyPatchStream(ctx context.Context, param any, reporter *UsageReporter, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	if !RecordApplyPatchStreamFailure(ctx, param, reporter, gatewayErr) {
		return false
	}
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: gatewayErr}:
	case <-ctx.Done():
	}
	return true
}

// EndApplyPatchStream checks EOF before any synthetic success or usage publication.
func EndApplyPatchStream(ctx context.Context, param any, reporter *UsageReporter, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	chunks := FinalizeApplyPatchStream(param)
	RecordApplyPatchStreamFailure(ctx, param, reporter, gatewayErr)
	for _, chunk := range chunks {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
		case <-ctx.Done():
			return true
		}
	}
	return StopApplyPatchStream(ctx, param, reporter, out, gatewayErr)
}

// ApplyPatchRequested resolves only original winning custom declarations.
func ApplyPatchRequested(original []byte) bool {
	for _, identity := range util.ResponsesToolReverseIdentityMap(original) {
		if identity.ApplyPatch {
			return true
		}
	}
	return false
}

// IsApplyPatchUpstreamTool preserves the original declaration's provenance.
func IsApplyPatchUpstreamTool(original []byte, name string) bool {
	return util.ResponsesToolReverseIdentityMap(original)[name].ApplyPatch
}

// InitializeApplyPatchStream prepares optional canonical state even for an empty EOF.
// An empty source has no progress events and does not opt native Codex into bridging.
func InitializeApplyPatchStream(ctx context.Context, from, to sdktranslator.Format, model string, original, effective []byte, param *any) {
	if to != sdktranslator.FormatOpenAIResponse || !ApplyPatchRequested(original) {
		return
	}
	_ = sdktranslator.TranslateStream(ctx, from, to, model, original, effective, nil, param)
}

// ApplyPatchOriginalRequest retains source declarations when SDK callers omit OriginalRequest.
func ApplyPatchOriginalRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}
