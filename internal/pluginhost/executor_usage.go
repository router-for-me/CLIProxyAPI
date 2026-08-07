package pluginhost

import (
	"bytes"
	"context"
	"net/http"
	"strings"

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

func (u *pluginExecutorUsage) observeStream(ctx context.Context, format sdktranslator.Format, headers http.Header, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	if u == nil || u.reporter == nil || in == nil {
		return in
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		var usageBuffer helps.StreamUsageBuffer
		var sseBuffer executorSSERecordBuffer
		var pendingUsagePayload []byte
		var terminalErr error
		isSSE := pluginExecutorSupportsStreamUsage(format) && strings.Contains(strings.ToLower(headers.Get("Content-Type")), "text/event-stream")
		for chunk := range in {
			if chunk.Err != nil {
				terminalErr = chunk.Err
			} else if isSSE {
				for _, record := range sseBuffer.Push(chunk.Payload) {
					observePluginExecutorStreamUsage(&usageBuffer, format, record)
				}
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
		if isSSE {
			for _, record := range sseBuffer.Flush() {
				observePluginExecutorStreamUsage(&usageBuffer, format, record)
			}
		} else if len(pendingUsagePayload) > 0 {
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
	usagePayloads := executorStreamTranslationPayloads(payload)
	if len(usagePayloads) == 1 && bytes.Equal(usagePayloads[0], payload) {
		usagePayloads = bytes.Split(payload, []byte("\n"))
	}
	for _, usagePayload := range usagePayloads {
		switch format {
		case sdktranslator.FormatClaude:
			detail, ok := helps.ParseClaudeStreamUsage(usagePayload)
			if previous, exists := buffer.Detail(); ok && exists {
				detail = mergeClaudePluginStreamUsage(previous, detail)
			}
			buffer.Observe(detail, ok)
		case sdktranslator.FormatOpenAI:
			detail, ok := helps.ParseOpenAIStreamUsage(usagePayload)
			buffer.Observe(detail, ok)
		case sdktranslator.FormatOpenAIResponse:
			jsonPayload := helps.JSONPayload(usagePayload)
			detail, ok := helps.ParseCodexUsage(jsonPayload)
			buffer.Observe(detail, ok)
		case sdktranslator.FormatGemini:
			detail, ok := helps.ParseGeminiStreamUsage(usagePayload)
			buffer.Observe(detail, ok)
		}
	}
}

func mergeClaudePluginStreamUsage(previous, current coreusage.Detail) coreusage.Detail {
	current.InputTokens = max(previous.InputTokens, current.InputTokens)
	current.OutputTokens = max(previous.OutputTokens, current.OutputTokens)
	current.ReasoningTokens = max(previous.ReasoningTokens, current.ReasoningTokens)
	current.CacheReadTokens = max(previous.CacheReadTokens, current.CacheReadTokens)
	current.CacheCreationTokens = max(previous.CacheCreationTokens, current.CacheCreationTokens)
	current.CachedTokens = current.CacheReadTokens
	if current.CachedTokens == 0 {
		current.CachedTokens = current.CacheCreationTokens
	}
	if current.ResponseServiceTier == "" {
		current.ResponseServiceTier = previous.ResponseServiceTier
	}
	nonReasoningOutput := current.OutputTokens
	if current.ReasoningTokens > 0 && current.ReasoningTokens <= current.OutputTokens {
		nonReasoningOutput -= current.ReasoningTokens
	} else if current.ReasoningTokens > current.OutputTokens {
		nonReasoningOutput = 0
	}
	current.TotalTokens = current.InputTokens + current.OutputTokens + current.CacheReadTokens + current.CacheCreationTokens
	current.TokenBreakdown = coreusage.NewIndependentTokenBreakdown(
		current.InputTokens,
		current.CacheReadTokens,
		current.CacheCreationTokens,
		nonReasoningOutput,
		current.ReasoningTokens,
		current.TotalTokens,
	)
	return current
}
