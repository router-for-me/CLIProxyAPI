package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var (
	xaiDataTag  = []byte("data:")
	xaiEventTag = []byte("event:")
)

const (
	xaiImageHandlerType        = "openai-image"
	xaiVideoHandlerType        = "openai-video"
	xaiSpeechHandlerType       = "openai-speech"
	xaiCustomToolType          = "custom"
	xaiFunctionToolType        = "function"
	xaiImageGenerationToolType = "image_generation"
	xaiNamespaceToolType       = "namespace"
	xaiToolSearchType          = "tool_search"
	xaiWebSearchToolType       = "web_search"
	xaiClientWebSearchAlias    = "clientfn_web_search"
	xaiXSearchToolType         = "x_search"
	xaiMaxTools                = 200
	// Codex Desktop injects codex_app.automation_update with a large oneOf+$ref
	// schema. xAI's free/build Responses path accepts the HTTP request but never
	// emits SSE when that schema is present, so Desktop hangs on "thinking".
	xaiCodexAppNamespaceName    = "codex_app"
	xaiAutomationUpdateToolName = "automation_update"
	// Permissive placeholder schema: keeps the tool callable without the hang.
	xaiSafeFunctionParameters   = `{"type":"object","properties":{},"additionalProperties":true}`
	xaiImagesGenerationsPath    = "/images/generations"
	xaiImagesEditsPath          = "/images/edits"
	xaiDefaultImageEndpointPath = xaiImagesGenerationsPath
	xaiVideosGenerationsPath    = "/videos/generations"
	xaiVideosEditsPath          = "/videos/edits"
	xaiVideosExtensionsPath     = "/videos/extensions"
	xaiVideosPath               = "/videos"
	xaiTTSPath                  = "/tts"
	xaiIdempotencyKeyMetaKey    = "idempotency_key"
	xaiComposerModelPrefix      = "grok-composer-"
	xaiTokenAuthHeader          = "X-XAI-Token-Auth"
	xaiTokenAuthValue           = "xai-grok-cli"
	xaiClientVersionHeader      = "x-grok-client-version"
	// Keep in sync with the current Grok CLI client version that chat-proxy
	// expects. The server rejects older versions with HTTP 426; it required
	// 1.0.13+ as of 2026-10-01 (#6249).
	// This hardcoded value serves as fallback if npm registry resolution fails.
	xaiClientVersionFallback      = helps.DefaultXAIFallbackClientVersion
	xaiClientVersionValue         = xaiClientVersionFallback
	xaiClientIdentifierHeader     = "x-grok-client-identifier"
	xaiClientIdentifierValue      = "grok-shell"
	xaiAuthenticateResponseHeader = "x-authenticateresponse"
	xaiAuthenticateResponseValue  = "authenticate-response"
	// xaiUsingAPIAttr enables the official API path for HTTP chat and media.
	xaiUsingAPIAttr = "using_api"
)

// xaiXSearchToolJSON is the native X Search tool injected when enabled by config.
// Internal subtool traces are still filtered downstream when this tool is present.
var xaiXSearchToolJSON = []byte(`{"type":"x_search"}`)

// XAIExecutor is a stateless executor for xAI Grok's Responses API.
type XAIExecutor struct {
	cfg *config.Config
}

// NewXAIExecutor creates a new xAI executor.
func NewXAIExecutor(cfg *config.Config) *XAIExecutor {
	return &XAIExecutor{cfg: cfg}
}

// xaiClientVersion returns the active Grok CLI client version (dynamically fetched
// from npm or falling back to xaiClientVersionFallback).
func xaiClientVersion() string {
	return helps.GetXAIClientVersion()
}

// StartXAIVersionUpdater starts the periodic Grok CLI version updater from npm.
// proxyURL is the global outbound proxy; an empty value inherits the process environment.
func StartXAIVersionUpdater(ctx context.Context, proxyURL string) {
	helps.StartXAIVersionUpdater(ctx, proxyURL)
}

// Identifier returns the provider identifier.
func (e *XAIExecutor) Identifier() string {
	return "xai"
}

// PrepareRequest injects xAI credentials into the outgoing HTTP request.
func (e *XAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _ := xaiCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Del("Authorization")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects xAI credentials into the request and executes it.
func (e *XAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("xai executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *XAIExecutor) SupportsApplyPatch() bool { return e != nil }
