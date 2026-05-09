package helps

import (
	"context"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

const usageRequestCaptureKey = "__cliproxy_usage_request_capture__"

type usageRequestCapture struct {
	body   []byte
	format string
}

func captureUsageRequest(ctx context.Context, info UpstreamRequestLog) {
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil || len(info.Body) == 0 {
		return
	}
	body := append([]byte(nil), info.Body...)
	ginCtx.Set(usageRequestCaptureKey, usageRequestCapture{
		body:   body,
		format: inferUsageThinkingFormat(info),
	})
}

func capturedUsageThinkingEffort(ctx context.Context, model string) (string, bool) {
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return "", false
	}
	raw, exists := ginCtx.Get(usageRequestCaptureKey)
	if !exists {
		return "", false
	}
	capture, ok := raw.(usageRequestCapture)
	if !ok {
		return "", false
	}
	return thinking.ExtractEffort(capture.body, "", "", capture.format), true
}

func inferUsageThinkingFormat(info UpstreamRequestLog) string {
	provider := strings.ToLower(strings.TrimSpace(info.Provider))
	path := lowerURLPath(info.URL)
	if strings.Contains(path, "/responses") || strings.Contains(path, "/responses/compact") {
		return "openai-response"
	}
	if strings.Contains(provider, "claude") {
		return "claude"
	}
	if strings.Contains(provider, "gemini-cli") {
		return "gemini-cli"
	}
	if strings.Contains(provider, "gemini") || strings.Contains(provider, "aistudio") || strings.Contains(provider, "vertex") {
		return "gemini"
	}
	if strings.Contains(provider, "antigravity") {
		return "antigravity"
	}
	if strings.Contains(provider, "kimi") {
		return "kimi"
	}
	if strings.Contains(provider, "codex") {
		return "codex"
	}
	return "openai"
}

func lowerURLPath(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Path == "" {
		return strings.ToLower(rawURL)
	}
	return strings.ToLower(parsed.Path)
}
