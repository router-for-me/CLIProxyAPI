package helps

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// ModelUsesV1Compaction uses the model selected for this attempt before configuration fallbacks.
func ModelUsesV1Compaction[T interface {
	GetName() string
	GetAlias() string
	GetUseV1Compaction() bool
}](req cliproxyexecutor.Request, opts cliproxyexecutor.Options, models []T, prefix string) bool {
	if options, ok := cliproxyauth.ResolvedHomeModelOptions(req); ok {
		return options.UseV1Compaction
	}
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		return info.UseV1Compaction
	}
	requested := thinking.ParseSuffix(PayloadRequestedModel(opts, req.Model)).ModelName
	requested = strings.TrimPrefix(requested, strings.TrimSpace(prefix)+"/")
	upstream := thinking.ParseSuffix(req.Model).ModelName
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.GetAlias()), requested) {
			return model.GetUseV1Compaction()
		}
	}
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.GetName()), upstream) || strings.EqualFold(strings.TrimSpace(model.GetAlias()), upstream) {
			return model.GetUseV1Compaction()
		}
	}
	return false
}

// ValidateV1CompactionContext prevents conversion from dropping server-owned conversation history.
func ValidateV1CompactionContext(ctx context.Context, payload []byte) error {
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return cliproxyexecutor.NewUpstreamWebsocketReplayRequiredError()
	}
	if gjson.GetBytes(payload, "previous_response_id").String() != "" {
		return ResponsesCompactionError{Message: "use-v1-compaction requires complete input without previous_response_id"}
	}
	return nil
}

// ResponsesCompactionStreamResult sends only the validated result and stops on cancellation.
func ResponsesCompactionStreamResult(ctx context.Context, response cliproxyexecutor.Response) *cliproxyexecutor.StreamResult {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	headers := response.Headers.Clone()
	if headers == nil {
		headers = make(map[string][]string)
	}
	headers.Set("Content-Type", "text/event-stream")
	headers.Del("Content-Length")
	go func() {
		defer close(chunks)
		for _, frame := range BuildResponsesCompactionStream(response.Payload) {
			select {
			case chunks <- cliproxyexecutor.StreamChunk{Payload: frame}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: chunks}
}
