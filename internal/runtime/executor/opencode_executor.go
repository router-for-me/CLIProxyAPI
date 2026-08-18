package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	clipoauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// opencodeDataTag is a tag used for identifying opencode data in SSE responses.
var opencodeDataTag = []byte("data:")

// opencodeEventTag is a tag used for identifying opencode events in SSE responses.
var opencodeEventTag = []byte("event:")

// OpenCodeExecutor is a stateless executor for OpenCode/Zen's OpenAI-compatible API.
type OpenCodeExecutor struct {
	cfg *config.Config
}

// NewOpenCodeExecutor creates a new OpenCode executor.
func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return &OpenCodeExecutor{cfg: cfg}
}

// Identifier returns the provider identifier.
func (e *OpenCodeExecutor) Identifier() string {
	return "opencode"
}

// PrepareRequest injects OpenCode/Zen credentials into the outgoing HTTP request.
func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *clipoauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _ := opencodeCreds(auth)
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

// HttpRequest injects OpenCode/Zen credentials into the request and executes it.
func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *clipoauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("opencode executor: request is nil")
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

// opencodeCreds extracts the OpenCode/Zen token from auth.
func opencodeCreds(auth *clipoauth.Auth) (string, bool) {
	if auth == nil || auth.Token == "" {
		return "", false
	}
	return auth.Token, true
}