package executor

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	apiKeyAuth := codexIsAPIKeyAuth(auth)
	requestKind := codexFinalUpstreamResponses
	if r != nil && r.URL != nil {
		requestKind = codexFinalUpstreamRequestKindForURL(r.URL.String())
	}

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	cfgUserAgent, cfgBetaFeatures := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithPriority(r.Header, ginHeaders, "X-Codex-Beta-Features", cfgBetaFeatures, "")
	misc.EnsureHeader(r.Header, ginHeaders, "Version", codexDefaultVersionHeader())
	misc.EnsureHeader(r.Header, ginHeaders, "X-OpenAI-Subagent", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Traceparent", "")
	misc.EnsureHeader(r.Header, ginHeaders, "Tracestate", "")
	identity := codexResolvedIdentity(r.Header, ginHeaders, auth, cfg)
	r.Header.Set("User-Agent", identity.userAgent)
	sessionID := codexEnsureSessionHeaders(r.Header, ginHeaders, auth, codexSessionHeaderOptions{
		includeRequestID: requestKind != codexFinalUpstreamCompact,
	})
	if requestKind == codexFinalUpstreamCompact {
		misc.EnsureHeader(r.Header, ginHeaders, codexHeaderTurnMetadata, "")
		misc.EnsureHeader(r.Header, ginHeaders, codexHeaderTurnState, "")
	} else {
		codexEnsureTurnMetadataHeader(r.Header, ginHeaders, codexTurnMetadataDefaults{
			sessionID:    sessionID,
			threadSource: codexDefaultThreadSource,
			turnID:       uuid.NewString(),
			sandbox:      codexDefaultSandboxTag,
		})
		misc.EnsureHeader(r.Header, ginHeaders, codexHeaderTurnState, "")
	}
	codexEnsureResponsesIdentityHeaders(r.Header, ginHeaders)

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}

	r.Header.Set("Originator", identity.originator)
	if residency := strings.TrimSpace(ginHeaders.Get(misc.CodexResidencyHeader)); residency != "" {
		r.Header.Set(misc.CodexResidencyHeader, residency)
	} else if residency := codexResidencyFor(cfg); residency != "" && strings.TrimSpace(r.Header.Get(misc.CodexResidencyHeader)) == "" {
		r.Header.Set(misc.CodexResidencyHeader, residency)
	}
	if !apiKeyAuth {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				if trimmed := strings.TrimSpace(accountID); trimmed != "" {
					r.Header.Set("Chatgpt-Account-Id", trimmed)
				}
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
	if cfgUserAgent != "" {
		r.Header.Set("User-Agent", cfgUserAgent)
	}
}

func codexDefaultVersionHeader() string {
	return strings.TrimSpace(buildinfo.Version)
}

// codexOriginatorFor resolves the originator value for the given config,
// honouring config > env > built-in default.
func codexOriginatorFor(cfg *config.Config) string {
	configured := ""
	if cfg != nil {
		configured = cfg.CodexHeaderDefaults.Originator
	}
	return misc.ResolveCodexOriginator(configured)
}

// codexResidencyFor resolves the residency header value; empty means "do not
// send" (matches codex-rs behaviour).
func codexResidencyFor(cfg *config.Config) string {
	configured := ""
	if cfg != nil {
		configured = cfg.CodexHeaderDefaults.Residency
	}
	return misc.ResolveCodexResidency(configured)
}

func codexAuthUserAgent(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if ua := strings.TrimSpace(auth.Attributes["header:User-Agent"]); ua != "" {
			return ua
		}
		if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
			return ua
		}
		if ua := strings.TrimSpace(auth.Attributes["user-agent"]); ua != "" {
			return ua
		}
	}
	if auth.Metadata == nil {
		return ""
	}
	if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
		return strings.TrimSpace(ua)
	}
	if ua, ok := auth.Metadata["user-agent"].(string); ok && strings.TrimSpace(ua) != "" {
		return strings.TrimSpace(ua)
	}
	return ""
}
