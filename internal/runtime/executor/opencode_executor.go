package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	clipoauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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

// Execute handles non-streaming execution for OpenCode/Zen with usage tracking.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpResp, err := e.HttpRequest(ctx, auth, &http.Request{
		Header: http.Header{},
		Body:   http.NoBody,
	})
	if err != nil {
		return resp, err
	}
	defer httpResp.Body.Close()

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, fmt.Errorf("opencode executor: read response error: %w", err)
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)

	return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream handles streaming execution for OpenCode/Zen with usage tracking.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result *cliproxyexecutor.StreamResult, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "", http.NoBody)
	if err != nil {
		return nil, err
	}
	if err = e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	reporter.ObserveResponse(httpResp)

	// Create a channel to stream chunks
	ch := make(chan cliproxyexecutor.StreamChunk, 100)
	// Start a goroutine to read and chunk the response
	go func() {
		defer close(ch)
		defer httpResp.Body.Close()
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

// opencodeCreds extracts the OpenCode/Zen token from auth.
// The token is stored in the auth Metadata field.
func opencodeCreds(auth *clipoauth.Auth) (string, bool) {
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
// For OpenCode/Zen, this approximates based on the request payload length.
func (e *OpenCodeExecutor) CountTokens(ctx context.Context, auth *clipoauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	payload := []byte("{}")
	if len(req.Payload) > 0 {
		payload = req.Payload
	}
	count := int64(len(payload))
	return cliproxyexecutor.Response{Payload: []byte(fmt.Sprintf(`{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}`, count, count))}, nil
}

// Refresh attempts to refresh provider credentials and returns the updated auth state.
func (e *OpenCodeExecutor) Refresh(ctx context.Context, auth *clipoauth.Auth) (*clipoauth.Auth, error) {
	// OpenCode/Zen uses static API keys, no refresh needed
	return auth, nil
}