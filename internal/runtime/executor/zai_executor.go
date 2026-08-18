package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	token, ok := zaiCreds(auth)
	if !ok {
		return nil
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

// Execute handles non-streaming execution for z.ai.
// It prepares the request with credentials and executes the HTTP request via HttpRequest.
func (e *ZAIExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	httpResp, err := e.HttpRequest(ctx, auth, &http.Request{
		Header: http.Header{},
		Body:   http.NoBody,
	})
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	// Read the response body
	body := make([]byte, 0, 4096)
	if httpResp.Body != nil {
		_, err := httpResp.Body.Read(body)
		httpResp.Body.Close()
		if err != nil {
			return cliproxyexecutor.Response{}, fmt.Errorf("zai executor: read response error: %w", err)
		}
	}
	return cliproxyexecutor.Response{Payload: body}, nil
}

// ExecuteStream handles streaming execution for z.ai.
// It returns a StreamResult containing upstream headers and a channel of provider chunks.
func (e *ZAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	httpResp, err := e.HttpRequest(ctx, auth, &http.Request{
		Header: http.Header{},
		Body:   http.NoBody,
	})
	if err != nil {
		return nil, err
	}
	// Create a channel to stream chunks
	ch := make(chan cliproxyexecutor.StreamChunk, 100)
	// Start a goroutine to read and chunk the response
	go func() {
		defer close(ch)
		// Read the response body in chunks
		reader := httpResp.Body
		buf := make([]byte, 4096)
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				ch <- cliproxyexecutor.StreamChunk{Payload: buf[:n]}
			}
			if readErr != nil {
				break
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{
		Headers: httpResp.Header,
		Chunks:  ch,
	}, nil
}

// zaiCreds extracts the z.ai token from auth.
// The z.ai provider uses a Bearer token authentication scheme.
// The token is stored in the auth Metadata field.
func zaiCreds(auth *cliproxyauth.Auth) (string, bool) {
	if auth == nil || auth.Metadata == nil {
		return "", false
	}
	// Token can be stored as "api_key" or "access_token" in Metadata
	if v, ok := auth.Metadata["api_key"]; ok {
		if token, ok := v.(string); ok && strings.TrimSpace(token) != "" {
			return token, true
		}
	}
	if v, ok := auth.Metadata["access_token"]; ok {
		if token, ok := v.(string); ok && strings.TrimSpace(token) != "" {
			return token, true
		}
	}
	return "", false
}

// CountTokens returns the number of tokens in the given request.
// For z.ai, this approximates based on the request payload length.
func (e *ZAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	payload := []byte("{}")
	if len(req.Payload) > 0 {
		payload = req.Payload
	}
	count := int64(len(payload))
	return cliproxyexecutor.Response{Payload: []byte(fmt.Sprintf(`{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}`, count, count))}, nil
}

// Refresh attempts to refresh provider credentials and returns the updated auth state.
func (e *ZAIExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	// z.ai uses static API keys, no refresh needed
	return auth, nil
}