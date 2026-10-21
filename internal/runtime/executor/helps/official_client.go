package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	codexoptimize "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// NewOfficialClientVerifier returns the verifier that enforces the per-credential
// official_client_only restriction. It inspects client-supplied headers and body,
// so it is a policy gate against accidental or casual use of a subscription
// credential through the proxy, not a defense against a client that deliberately
// forges those headers.
func NewOfficialClientVerifier(currentConfig func() *config.Config) cliproxyauth.OfficialClientVerifier {
	return func(ctx context.Context, provider string, opts cliproxyexecutor.Options) bool {
		headers := officialClientRequestHeaders(ctx, opts.Headers)
		switch provider {
		case "claude":
			var cfg *config.Config
			if currentConfig != nil {
				cfg = currentConfig()
			}
			path, _ := opts.Metadata[cliproxyexecutor.RequestPathMetadataKey].(string)
			countTokens := strings.HasSuffix(strings.TrimSpace(path), "/count_tokens")
			return IsOfficialClaudeCodeRequest(headers, opts.OriginalRequest, countTokens, cfg)
		case "codex":
			return codexoptimize.IsCodexClientUserAgent(headerValue(headers, "User-Agent"))
		default:
			return false
		}
	}
}

// IsOfficialClaudeCodeRequest reports whether a request carries the signals of a
// first-party Claude Code CLI/VS Code session. Unlike DetectClaudeCodeRequest's
// Confirmed flag it does not pin the User-Agent version to the configured
// baseline, so a Claude Code auto-update never locks genuine users out.
func IsOfficialClaudeCodeRequest(headers http.Header, payload []byte, countTokens bool, cfg *config.Config) bool {
	if DetectClaudeCodeRequest(headers, payload, countTokens, cfg).Confirmed {
		return true
	}
	userAgent := strings.TrimSpace(headerValue(headers, "User-Agent"))
	entrypoint, _ := parseClaudeCodeUserAgentDetails(userAgent)
	if !claudeCodeNativeUserAgentPattern.MatchString(userAgent) || !nativeClaudeEntrypoints[entrypoint] {
		return false
	}
	if headerValue(headers, "X-App") != "cli" || !headerContainsClaudeCodeBeta(headers) {
		return false
	}
	if countTokens {
		return true
	}
	userID := gjson.GetBytes(payload, "metadata.user_id")
	return userID.Type == gjson.String && isValidUserID(userID.String())
}

// officialClientRequestHeaders merges the inbound gin request headers with the
// headers carried on the execution options, the latter taking precedence.
func officialClientRequestHeaders(ctx context.Context, incoming http.Header) http.Header {
	resolved := make(http.Header, len(incoming))
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			resolved = ginCtx.Request.Header.Clone()
		}
	}
	for key, values := range incoming {
		resolved[key] = append([]string(nil), values...)
	}
	return resolved
}
