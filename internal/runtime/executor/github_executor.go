package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	githubauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/github"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// copilotEditorVersion is sent as Editor-Version to identify the integration.
const copilotEditorVersion = "vscode/1.96.0"

// GithubCopilotExecutor handles API requests to GitHub Copilot chat completions.
// It automatically refreshes the short-lived Copilot token using the stored GitHub user token.
type GithubCopilotExecutor struct {
	cfg *config.Config
}

// NewGithubCopilotExecutor creates a new GitHub Copilot executor.
func NewGithubCopilotExecutor(cfg *config.Config) *GithubCopilotExecutor {
	return &GithubCopilotExecutor{cfg: cfg}
}

// Identifier returns the executor provider key.
func (e *GithubCopilotExecutor) Identifier() string { return "github-copilot" }

// Refresh fetches a new Copilot API token using the stored GitHub user token.
// The Copilot token is short-lived (~30 minutes); this is called automatically before expiry.
func (e *GithubCopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debug("github-copilot executor: refreshing Copilot token")

	if auth == nil {
		return nil, fmt.Errorf("github-copilot executor: auth is nil")
	}

	githubToken := githubTokenFromAuth(auth)
	if githubToken == "" {
		return auth, nil
	}

	authSvc := githubauth.NewGithubAuth(e.cfg)
	copilotToken, err := authSvc.FetchCopilotToken(ctx, githubToken)
	if err != nil {
		return nil, fmt.Errorf("github-copilot executor: token refresh failed: %w", err)
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = copilotToken.Token
	auth.Metadata["expired"] = copilotToken.ExpiresAt.String()
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)

	return auth, nil
}

// Execute performs a non-streaming chat completion request to GitHub Copilot.
func (e *GithubCopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	var body []byte
	var token string
	body, token, err = e.buildRequestBody(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}

	url := githubauth.CopilotAPIBaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, fmt.Errorf("github-copilot executor: failed to create request: %w", err)
	}
	applyCopilotHeaders(httpReq, token, false)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("github-copilot request error: status %d: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		return resp, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	reporter.publish(ctx, parseOpenAIUsage(data))

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming chat completion request to GitHub Copilot.
func (e *GithubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	var body []byte
	var token string
	body, token, err = e.buildRequestBody(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}

	url := githubauth.CopilotAPIBaseURL + "/chat/completions"
	var httpReq *http.Request
	httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("github-copilot executor: failed to create stream request: %w", err)
	}
	applyCopilotHeaders(httpReq, token, true)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("github-copilot stream error: status %d: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close error body: %v", errClose)
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("github-copilot executor: close stream body: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 1_048_576) // 1MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param)
		for i := range doneChunks {
			out <- cliproxyexecutor.StreamChunk{Payload: doneChunks[i]}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens is not natively supported by GitHub Copilot; delegates to the OpenAI-compatible path.
func (e *GithubCopilotExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("github-copilot executor: token counting not supported")
}

// HttpRequest injects Copilot credentials into an arbitrary HTTP request and executes it.
func (e *GithubCopilotExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("github-copilot executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	token := copilotTokenFromAuth(auth)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	httpReq := req.WithContext(ctx)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// buildRequestBody translates the incoming request to OpenAI format and resolves the Copilot token.
func (e *GithubCopilotExecutor) buildRequestBody(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (body []byte, token string, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(originalPayloadSource), stream)
	body = sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)

	// Strip any "github-copilot-" prefix added for routing purposes.
	upstreamModel := stripCopilotPrefix(baseModel)
	body, err = sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return nil, "", fmt.Errorf("github-copilot executor: failed to set model: %w", err)
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

	token = copilotTokenFromAuth(auth)
	if token == "" {
		return nil, "", fmt.Errorf("github-copilot executor: no Copilot token available")
	}

	return body, token, nil
}

// applyCopilotHeaders sets the required headers for GitHub Copilot API requests.
func applyCopilotHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Copilot-Integration-Id", "vscode-chat")
	r.Header.Set("openai-intent", "conversation-panel")
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
}

// copilotTokenFromAuth extracts the short-lived Copilot API token from auth metadata.
func copilotTokenFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			if t := strings.TrimSpace(v); t != "" {
				return t
			}
		}
	}
	if auth.Attributes != nil {
		if v := auth.Attributes["api_key"]; v != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// githubTokenFromAuth extracts the long-lived GitHub user token from auth metadata.
func githubTokenFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["github_token"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// stripCopilotPrefix removes the "github-copilot-" prefix from model names for the upstream API.
func stripCopilotPrefix(model string) string {
	model = strings.TrimSpace(model)
	const prefix = "github-copilot-"
	if strings.HasPrefix(strings.ToLower(model), prefix) {
		return model[len(prefix):]
	}
	return model
}
