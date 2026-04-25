package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexHeaderInstallationID = "X-Codex-Installation-Id"
	codexHeaderWindowID       = "X-Codex-Window-Id"
	codexHeaderParentThreadID = "X-Codex-Parent-Thread-Id"
	codexHeaderMemgenRequest  = "X-OpenAI-Memgen-Request"

	codexClientMetadataInstallationID = "x-codex-installation-id"
	codexClientMetadataWindowID       = "x-codex-window-id"
	codexClientMetadataParentThreadID = "x-codex-parent-thread-id"
	codexClientMetadataSubagent       = "x-openai-subagent"
	codexClientMetadataTurnMetadata   = "x-codex-turn-metadata"
	codexWSClientMetadataTraceparent  = "ws_request_header_traceparent"
	codexWSClientMetadataTracestate   = "ws_request_header_tracestate"
)

var (
	codexInstallationIDOnce sync.Once
	codexInstallationID     string
)

func codexGinHeadersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return nil
	}
	return ginCtx.Request.Header
}

func codexApplyHTTPClientMetadata(body []byte, req *http.Request, auth *cliproxyauth.Auth, cfg *config.Config) []byte {
	if len(bytes.TrimSpace(body)) == 0 || req == nil {
		return body
	}
	source := codexGinHeadersFromContext(req.Context())
	if !codexShouldApplyClientMetadata(body, source, auth, cfg) {
		return body
	}
	return codexSetClientMetadataString(
		body,
		codexClientMetadataInstallationID,
		codexResolvedInstallationID(req.Header, source, auth, cfg),
		false,
	)
}

func codexApplyWebsocketClientMetadata(ctx context.Context, body []byte, headers http.Header, auth *cliproxyauth.Auth, cfg *config.Config) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}

	source := codexGinHeadersFromContext(ctx)
	if !codexShouldApplyClientMetadata(body, source, auth, cfg) {
		return body
	}
	body = codexSetClientMetadataString(body, codexClientMetadataInstallationID, codexResolvedInstallationID(headers, source, auth, cfg), false)
	body = codexSetClientMetadataString(body, codexClientMetadataWindowID, firstNonEmptyHeaderValue(headers, source, codexHeaderWindowID), false)
	body = codexSetClientMetadataString(body, codexClientMetadataSubagent, firstNonEmptyHeaderValue(headers, source, "X-OpenAI-Subagent"), false)
	body = codexSetClientMetadataString(body, codexClientMetadataParentThreadID, firstNonEmptyHeaderValue(headers, source, codexHeaderParentThreadID), false)
	body = codexSetClientMetadataString(body, codexClientMetadataTurnMetadata, firstNonEmptyHeaderValue(headers, source, codexHeaderTurnMetadata), false)
	body = codexSetClientMetadataString(body, codexWSClientMetadataTraceparent, firstNonEmptyHeaderValue(headers, source, "Traceparent"), false)
	body = codexSetClientMetadataString(body, codexWSClientMetadataTracestate, firstNonEmptyHeaderValue(headers, source, "Tracestate"), false)

	// codex-rs carries websocket trace context through client_metadata, not a
	// top-level trace field.
	if gjson.GetBytes(body, "trace").Exists() {
		if updated, err := sjson.DeleteBytes(body, "trace"); err == nil {
			body = updated
		}
	}
	return body
}

func codexShouldApplyClientMetadata(body []byte, source http.Header, auth *cliproxyauth.Auth, cfg *config.Config) bool {
	if !codexIsAPIKeyAuth(auth) {
		return true
	}
	metadata := gjson.GetBytes(body, "client_metadata")
	if metadata.Exists() && metadata.IsObject() {
		return true
	}
	return codexExplicitClientMetadataRequested(source, auth, cfg)
}

func codexExplicitClientMetadataRequested(source http.Header, auth *cliproxyauth.Auth, cfg *config.Config) bool {
	for _, header := range []string{
		codexHeaderInstallationID,
		codexHeaderWindowID,
		codexHeaderParentThreadID,
		codexHeaderMemgenRequest,
		codexHeaderTurnMetadata,
		"X-OpenAI-Subagent",
		"Traceparent",
		"Tracestate",
	} {
		if source != nil && strings.TrimSpace(source.Get(header)) != "" {
			return true
		}
		if codexAuthStringValue(auth, []string{"header:" + header, "header:" + strings.ToLower(header)}) != "" {
			return true
		}
	}
	if cfg != nil && strings.TrimSpace(cfg.CodexHeaderDefaults.InstallationID) != "" {
		return true
	}
	if codexAuthStringValue(auth, []string{
		"x-codex-installation-id",
		"installation_id",
		"codex_installation_id",
	}) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv("CODEX_INSTALLATION_ID")) != ""
}

func codexEnsureResponsesIdentityHeaders(target http.Header, source http.Header) {
	if target == nil {
		return
	}
	ensureHeaderWithPriority(target, source, codexHeaderParentThreadID, "", "")
	ensureHeaderWithPriority(target, source, codexHeaderMemgenRequest, "", "")
	ensureHeaderWithPriority(target, source, codexHeaderWindowID, "", "")
	if strings.TrimSpace(target.Get(codexHeaderWindowID)) == "" {
		if sessionID := strings.TrimSpace(target.Get(codexHeaderSessionID)); sessionID != "" {
			target.Set(codexHeaderWindowID, sessionID+":0")
		}
	}
}

func codexResetRequestBody(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
}

func codexSetClientMetadataString(body []byte, key string, value string, overwrite bool) []byte {
	value = strings.TrimSpace(value)
	if value == "" || key == "" || !codexCanSetClientMetadata(body) {
		return body
	}
	path := "client_metadata." + key
	if !overwrite && gjson.GetBytes(body, path).Exists() {
		return body
	}
	updated, err := sjson.SetBytes(body, path, value)
	if err != nil {
		return body
	}
	return updated
}

func codexCanSetClientMetadata(body []byte) bool {
	metadata := gjson.GetBytes(body, "client_metadata")
	return !metadata.Exists() || metadata.Type == gjson.Null || metadata.IsObject()
}

func codexResolvedInstallationID(target http.Header, source http.Header, auth *cliproxyauth.Auth, cfg *config.Config) string {
	if id := firstNonEmptyHeaderValue(target, source, codexHeaderInstallationID); id != "" {
		return id
	}
	if cfg != nil {
		if id := strings.TrimSpace(cfg.CodexHeaderDefaults.InstallationID); id != "" {
			return id
		}
	}
	if id := codexAuthStringValue(auth, []string{
		"header:x-codex-installation-id",
		"header:X-Codex-Installation-Id",
		"x-codex-installation-id",
		"installation_id",
		"codex_installation_id",
	}); id != "" {
		return id
	}
	if id := strings.TrimSpace(os.Getenv("CODEX_INSTALLATION_ID")); id != "" {
		return id
	}
	return codexDefaultInstallationID()
}

func codexDefaultInstallationID() string {
	codexInstallationIDOnce.Do(func() {
		codexInstallationID = uuid.NewString()
	})
	return codexInstallationID
}

func codexAuthStringValue(auth *cliproxyauth.Auth, keys []string) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		for _, key := range keys {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return value
			}
		}
	}
	if auth.Metadata != nil {
		for _, key := range keys {
			if value, ok := auth.Metadata[key].(string); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}
