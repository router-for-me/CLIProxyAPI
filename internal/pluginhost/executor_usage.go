package pluginhost

import (
	"bytes"
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type pluginExecutorUsage struct {
	reporter *helps.UsageReporter
}

func newPluginExecutorUsage(ctx context.Context, adapter *executorAdapter, model string, auth *coreauth.Auth) *pluginExecutorUsage {
	return &pluginExecutorUsage{reporter: helps.NewExecutorUsageReporter(ctx, adapter, model, auth)}
}

func (u *pluginExecutorUsage) trackFailure(ctx context.Context, errPtr *error) {
	if u == nil || u.reporter == nil {
		return
	}
	u.reporter.TrackFailure(ctx, errPtr)
}

func (u *pluginExecutorUsage) publishNonStream(ctx context.Context, format sdktranslator.Format, payload []byte) {
	if u == nil || u.reporter == nil {
		return
	}
	detail, ok := pluginExecutorNonStreamUsage(format, payload)
	if ok {
		u.reporter.Publish(ctx, detail)
		return
	}
	u.reporter.EnsurePublished(ctx)
}

func (u *pluginExecutorUsage) observeStream(ctx context.Context, format sdktranslator.Format, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	if u == nil || u.reporter == nil || in == nil {
		return in
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		var usageBuffer helps.StreamUsageBuffer
		var pendingUsagePayload []byte
		var terminalErr error
		for chunk := range in {
			if chunk.Err != nil {
				terminalErr = chunk.Err
			} else {
				pendingUsagePayload = observePluginExecutorStreamChunk(&usageBuffer, format, pendingUsagePayload, chunk.Payload)
			}
			if !sendExecutorPluginStreamChunk(ctx, out, chunk) {
				if errContext := ctx.Err(); errContext != nil {
					u.reporter.PublishFailure(ctx, errContext)
				}
				return
			}
		}
		if terminalErr != nil {
			u.reporter.PublishFailure(ctx, terminalErr)
			return
		}
		if len(pendingUsagePayload) > 0 {
			observePluginExecutorStreamUsage(&usageBuffer, format, pendingUsagePayload)
		}
		if !usageBuffer.Publish(ctx, u.reporter) {
			u.reporter.EnsurePublished(ctx)
		}
	}()
	return out
}

func observePluginExecutorStreamChunk(buffer *helps.StreamUsageBuffer, format sdktranslator.Format, pending, payload []byte) []byte {
	if !pluginExecutorSupportsStreamUsage(format) {
		return nil
	}
	pending = append(pending, payload...)
	lastNewline := bytes.LastIndexByte(pending, '\n')
	if lastNewline < 0 {
		return pending
	}
	observePluginExecutorStreamUsage(buffer, format, pending[:lastNewline+1])
	return bytes.Clone(pending[lastNewline+1:])
}

func pluginExecutorNonStreamUsage(format sdktranslator.Format, payload []byte) (coreusage.Detail, bool) {
	switch format {
	case sdktranslator.FormatClaude:
		return helps.ParseClaudeUsage(payload), bytes.Contains(payload, []byte(`"usage"`))
	case sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse:
		return helps.ParseOpenAIUsage(payload), bytes.Contains(payload, []byte(`"usage"`))
	case sdktranslator.FormatGemini:
		return helps.ParseGeminiUsage(payload), bytes.Contains(payload, []byte(`"usageMetadata"`)) || bytes.Contains(payload, []byte(`"usage_metadata"`))
	default:
		return coreusage.Detail{}, false
	}
}

func pluginExecutorSupportsStreamUsage(format sdktranslator.Format) bool {
	switch format {
	case sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatGemini:
		return true
	default:
		return false
	}
}

func observePluginExecutorStreamUsage(buffer *helps.StreamUsageBuffer, format sdktranslator.Format, payload []byte) {
	for _, line := range bytes.Split(payload, []byte("\n")) {
		switch format {
		case sdktranslator.FormatClaude:
			detail, ok := helps.ParseClaudeStreamUsage(line)
			buffer.Observe(detail, ok)
		case sdktranslator.FormatOpenAI:
			detail, ok := helps.ParseOpenAIStreamUsage(line)
			buffer.Observe(detail, ok)
		case sdktranslator.FormatOpenAIResponse:
			jsonPayload := helps.JSONPayload(line)
			detail, ok := helps.ParseCodexUsage(jsonPayload)
			buffer.Observe(detail, ok)
		case sdktranslator.FormatGemini:
			detail, ok := helps.ParseGeminiStreamUsage(line)
			buffer.Observe(detail, ok)
		}
	}
}
