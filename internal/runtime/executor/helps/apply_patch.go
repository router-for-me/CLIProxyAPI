package helps

import (
	"bytes"
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// ApplyPatchUpstreamErrorMessage deliberately excludes upstream JSON and patch text.
const ApplyPatchUpstreamErrorMessage = "Invalid apply_patch tool arguments received from upstream."

// TranslatedClaudeErrorPayload returns the raw JSON payload when a non-streaming
// translation to Claude format produced a terminal error object instead of a message.
func TranslatedClaudeErrorPayload(responseFormat sdktranslator.Format, out []byte) (string, bool) {
	if responseFormat != sdktranslator.FormatClaude || len(out) == 0 {
		return "", false
	}
	if gjson.GetBytes(out, "type").String() != "error" {
		return "", false
	}
	return string(out), true
}

// TranslatedClaudeStreamErrorPayload returns the raw JSON data payload when a translated
// Claude SSE chunk contains an error event.
func TranslatedClaudeStreamErrorPayload(responseFormat sdktranslator.Format, lines [][]byte) (string, bool) {
	if responseFormat != sdktranslator.FormatClaude {
		return "", false
	}
	for _, chunk := range lines {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			payload := JSONPayload(line)
			if len(payload) == 0 {
				continue
			}
			if gjson.GetBytes(payload, "type").String() == "error" {
				return string(payload), true
			}
		}
	}
	return "", false
}

// CaptureTranslatedClaudeStreamError retains the first SSE error event payload
// so failure usage and terminal StreamChunk.Err can be emitted after any trailing usage chunk.
func CaptureTranslatedClaudeStreamError(responseFormat sdktranslator.Format, lines [][]byte, retainedErr *error, buildErr func(string) error) {
	if retainedErr == nil || *retainedErr != nil {
		return
	}
	errPayload, okErr := TranslatedClaudeStreamErrorPayload(responseFormat, lines)
	if !okErr {
		return
	}
	if buildErr != nil {
		*retainedErr = buildErr(errPayload)
	}
}

// PublishTranslatedClaudeStreamFailure publishes failure usage with buffered stream usage
// when a translated Claude stream retained an SSE error event.
func PublishTranslatedClaudeStreamFailure(ctx context.Context, retainedErr error, reporter *UsageReporter, streamUsage *StreamUsageBuffer) bool {
	if retainedErr == nil {
		return false
	}
	if streamUsage != nil && streamUsage.PublishFailure(ctx, reporter, retainedErr) {
		return true
	}
	if reporter != nil {
		reporter.PublishFailure(ctx, retainedErr)
	}
	return true
}

// StopTranslatedClaudeStream publishes failure usage and propagates a terminal StreamChunk.Err
// once after a translated Claude stream emitted an SSE error event.
func StopTranslatedClaudeStream(ctx context.Context, retainedErr *error, reporter *UsageReporter, streamUsage *StreamUsageBuffer, out chan<- cliproxyexecutor.StreamChunk) bool {
	if retainedErr == nil || *retainedErr == nil {
		return false
	}
	err := *retainedErr
	*retainedErr = nil
	PublishTranslatedClaudeStreamFailure(ctx, err, reporter, streamUsage)
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: err}:
		return true
	default:
	}
	select {
	case out <- cliproxyexecutor.StreamChunk{Err: err}:
	case <-ctx.Done():
	}
	return true
}

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

// CanFinalizeResponseStream reports whether EOF can safely synthesize the source terminator.
func CanFinalizeResponseStream(param any) bool {
	state, okState := param.(interface{ CanFinalizeResponseStream() bool })
	return okState && state.CanFinalizeResponseStream()
}

// RecordApplyPatchStreamFailure records validation before delivery can be canceled.
func RecordApplyPatchStreamFailure(ctx context.Context, param any, reporter *UsageReporter, gatewayErr error) bool {
	return RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, nil, gatewayErr)
}

// RecordApplyPatchStreamFailureWithUsage records validation with buffered upstream usage.
func RecordApplyPatchStreamFailureWithUsage(ctx context.Context, param any, reporter *UsageReporter, streamUsage *StreamUsageBuffer, gatewayErr error) bool {
	if ApplyPatchTranslationError(param) == nil {
		return false
	}
	if streamUsage != nil && streamUsage.PublishFailure(ctx, reporter, gatewayErr) {
		return true
	}
	reporter.PublishFailure(ctx, gatewayErr)
	return true
}

// StopApplyPatchStream propagates a retained failure after its one translated frame.
// The caller supplies its existing sanitized gateway status error.
func StopApplyPatchStream(ctx context.Context, param any, reporter *UsageReporter, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	return StopApplyPatchStreamWithUsage(ctx, param, reporter, nil, out, gatewayErr)
}

// StopApplyPatchStreamWithUsage propagates a retained failure with buffered upstream usage.
func StopApplyPatchStreamWithUsage(ctx context.Context, param any, reporter *UsageReporter, streamUsage *StreamUsageBuffer, out chan<- cliproxyexecutor.StreamChunk, gatewayErr error) bool {
	if !RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, streamUsage, gatewayErr) {
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
