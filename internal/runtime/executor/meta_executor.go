package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// MetaExecutor executes OpenAI-compatible inference for the Meta provider.
//
// Meta's API is OpenAI-compatible: identity is established through an OAuth
// device flow and a minted LLM API key is used as the inference bearer (see
// the Phase 4 design doc). This executor delegates all inference to the shared
// OpenAI-compatible executor bound to the "meta" provider key and supplies the
// Meta-specific credential refresh (re-mint) in meta_executor_auth.go.
type MetaExecutor struct {
	compat *OpenAICompatExecutor
	cfg    *config.Config
}

// NewMetaExecutor creates a Meta executor. The provider may be the bare
// executor channel; inference is routed by the shared OpenAI-compatible path.
func NewMetaExecutor(cfg *config.Config) *MetaExecutor {
	return &MetaExecutor{
		compat: NewOpenAICompatExecutor("meta", cfg),
		cfg:    cfg,
	}
}

// Identifier returns the provider identifier.
func (e *MetaExecutor) Identifier() string {
	return "meta"
}

// Execute delegates to the OpenAI-compatible inference path.
func (e *MetaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("meta executor: compat executor is nil")
	}
	return e.compat.Execute(ctx, auth, req, opts)
}

// ExecuteStream delegates to the OpenAI-compatible streaming path.
func (e *MetaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("meta executor: compat executor is nil")
	}
	return e.compat.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens delegates to the OpenAI-compatible token accounting path.
func (e *MetaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e == nil || e.compat == nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("meta executor: compat executor is nil")
	}
	return e.compat.CountTokens(ctx, auth, req, opts)
}

// PrepareRequest injects Meta credentials into the outgoing HTTP request.
func (e *MetaExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if e == nil || e.compat == nil {
		return fmt.Errorf("meta executor: compat executor is nil")
	}
	return e.compat.PrepareRequest(req, auth)
}

// HttpRequest injects Meta credentials into the request and executes it.
func (e *MetaExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.compat == nil {
		return nil, fmt.Errorf("meta executor: compat executor is nil")
	}
	return e.compat.HttpRequest(ctx, auth, req)
}

// metaMetadataString extracts a string from auth metadata for Meta credentials.
func metaMetadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	switch v := meta[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}
