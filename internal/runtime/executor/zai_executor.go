package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// zaiDataTag is a tag used for identifying zai data in SSE responses.
var zaiDataTag = []byte("data:")

// zaiEventTag is a tag used for identifying zai events in SSE responses.
var zaiEventTag = []byte("event:")

// ZAIExecutor is a stateless executor for z.ai GLM's Anthropic-compatible API.
type ZAIExecutor struct {
	cfg *config.Config
}

// NewZAIExecutor creates a new zAI executor.
func NewZAIExecutor(cfg *config.Config) *ZAIExecutor {
	return &ZAIExecutor{cfg: cfg}
}

// Identifier returns the provider identifier.
func (e *ZAIExecutor) Identifier() string {
	return "zai"
}

// PrepareRequest injects z.ai credentials into the outgoing HTTP request.
func (e *ZAIExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _ := zaiCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects z.ai credentials into the request and executes it.
func (e *ZAIExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("zai executor: request is nil")
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

// zaiCreds extracts the z.ai token from auth.
// The z.ai provider uses a Bearer token authentication scheme.
func zaiCreds(auth *cliproxyauth.Auth) (string, bool) {
	if auth == nil || auth.Token == "" {
		return "", false
	}
	return auth.Token, true
}