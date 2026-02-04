package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/rovo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/sjson"
)

// RovoExecutor is a stateless executor for Atlassian Rovo (Bedrock proxy).
type RovoExecutor struct {
	cfg *config.Config
}

func NewRovoExecutor(cfg *config.Config) *RovoExecutor { return &RovoExecutor{cfg: cfg} }

func (e *RovoExecutor) Identifier() string { return "rovo" }

// rovoCreds extracts API key, email, cloud ID, and base URL from auth attributes/metadata.
func rovoCreds(a *cliproxyauth.Auth) (apiKey, email, cloudID, baseURL string) {
	if a == nil {
		return "", "", "", ""
	}
	if a.Attributes != nil {
		apiKey = strings.TrimSpace(a.Attributes["api_key"])
		email = strings.TrimSpace(a.Attributes["email"])
		cloudID = strings.TrimSpace(a.Attributes["cloud_id"])
		baseURL = strings.TrimSpace(a.Attributes["base_url"])
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["api_key"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
	}
	if email == "" && a.Metadata != nil {
		if v, ok := a.Metadata["email"].(string); ok {
			email = strings.TrimSpace(v)
		}
	}
	if cloudID == "" && a.Metadata != nil {
		if v, ok := a.Metadata["cloud_id"].(string); ok {
			cloudID = strings.TrimSpace(v)
		}
	}
	if baseURL == "" && a.Metadata != nil {
		if v, ok := a.Metadata["base_url"].(string); ok {
			baseURL = strings.TrimSpace(v)
		}
	}
	return
}

// PrepareRequest injects Rovo credentials into the outgoing HTTP request.
func (e *RovoExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, email, cloudID, _ := rovoCreds(auth)
	if apiKey == "" {
		return nil
	}

	// RovoDev API requires Basic auth with email:apiKey
	if email != "" {
		req.SetBasicAuth(email, apiKey)
	} else {
		// Fallback to Bearer if no email available
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("X-Api-Key", apiKey)

	if encoded := rovo.EncodeEmailToken(email, apiKey); encoded != "" {
		req.Header.Set("X-Atlassian-EncodedToken", encoded)
	}

	if cloudID != "" {
		req.Header.Set("X-Atlassian-CloudId", cloudID)
		req.Header.Set("X-RovoDev-Billing-CloudId", cloudID)
	}

	// Set session IDs per request
	req.Header.Set("X-RovoDev-Xid", "rovodev-cli")
	req.Header.Set("X-RovoDev-Version", "0.13.39")
	req.Header.Set("X-RovoDev-Session-Id", uuid.New().String())
	req.Header.Set("X-RovoDev-Session-Agent-Run-Id", uuid.New().String()+"_"+uuid.New().String())
	req.Header.Set("anthropic-version", "2023-06-01")

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Rovo credentials into the request and executes it.
func (e *RovoExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("rovo executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *RovoExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if cliResp, cliErr := rovoCLIExecute(ctx, auth, req, opts); cliErr != nil || len(cliResp.Payload) > 0 {
		return cliResp, cliErr
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	_, _, _, baseURL := rovoCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.atlassian.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// Resolve upstream model name via config if possible
	upstreamModel := strings.TrimPrefix(baseModel, "rovo-")
	// Rovo requires specific upstream model IDs (e.g. anthropic.claude-haiku-4-5-20251001-v1:0)
	// We rely on service layer config resolution or default mapping.
	// Here we try to map common aliases if they match exactly.
	switch strings.ToLower(upstreamModel) {
	case "claude-haiku-4.5":
		upstreamModel = "anthropic.claude-haiku-4-5-20251001-v1:0"
	case "claude-sonnet-4.5":
		upstreamModel = "anthropic.claude-sonnet-4-5-20250929-v1:0"
	case "claude-sonnet-4":
		upstreamModel = "anthropic.claude-sonnet-4-20241022-v1:0"
	case "claude-opus-4.5":
		upstreamModel = "anthropic.claude-opus-4-5-20251101-v1:0"
	case "gpt-5.2-codex":
		upstreamModel = "gpt-5.2-codex"
	case "gpt-5.2":
		upstreamModel = "gpt-5.2"
	case "gpt-5.1":
		upstreamModel = "gpt-5.1"
	case "gpt-5":
		upstreamModel = "gpt-5"
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	// Rovo uses Claude format
	to := sdktranslator.FromString("claude")
	stream := false

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}

	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), stream)

	// Rovo specific: remove model from body, set anthropic_version
	body, _ = sjson.DeleteBytes(body, "model")
	body, _ = sjson.SetBytes(body, "anthropic_version", "bedrock-2023-05-31")

	// Apply thinking if configured
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	// Apply payload overrides
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, nil, requestedModel)

	// Build URL
	url := fmt.Sprintf("%s/rovodev/v2/proxy/ai/v1/bedrock/model/%s/invoke", baseURL, upstreamModel)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return resp, err
	}

	// Log request
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
		_ = httpResp.Body.Close()
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		appendAPIResponseChunk(ctx, e.cfg, respBody)
		logWithRequestID(ctx).Debugf("rovo request error: %d, %s", httpResp.StatusCode, string(respBody))
		return resp, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
	}

	appendAPIResponseChunk(ctx, e.cfg, respBody)
	reporter.publish(ctx, parseClaudeUsage(respBody))
	reporter.ensurePublished(ctx)

	// Translate response back to source format
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, respBody, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *RovoExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if cliStream, cliErr := rovoCLIExecuteStream(ctx, auth, req, opts); cliErr != nil || cliStream != nil {
		return cliStream, cliErr
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	_, _, _, baseURL := rovoCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.atlassian.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	upstreamModel := baseModel
	switch strings.ToLower(baseModel) {
	case "claude-haiku-4.5":
		upstreamModel = "anthropic.claude-haiku-4-5-20251001-v1:0"
	case "claude-sonnet-4.5":
		upstreamModel = "anthropic.claude-sonnet-4-5-20250929-v1:0"
	case "claude-sonnet-4":
		upstreamModel = "anthropic.claude-sonnet-4-20241022-v1:0"
	case "claude-opus-4.5":
		upstreamModel = "anthropic.claude-opus-4-5-20251101-v1:0"
	case "gpt-5.2-codex":
		upstreamModel = "gpt-5.2-codex"
	case "gpt-5.2":
		upstreamModel = "gpt-5.2"
	case "gpt-5.1":
		upstreamModel = "gpt-5.1"
	case "gpt-5":
		upstreamModel = "gpt-5"
	}

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	streamReq := true

	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}

	body := sdktranslator.TranslateRequest(from, to, baseModel, bytes.Clone(req.Payload), streamReq)
	body, _ = sjson.DeleteBytes(body, "model")
	body, _ = sjson.SetBytes(body, "anthropic_version", "bedrock-2023-05-31")

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, nil, requestedModel)

	url := fmt.Sprintf("%s/rovodev/v2/proxy/ai/v1/bedrock/model/%s/invoke-with-response-stream", baseURL, upstreamModel)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}

	// Log request
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
		respBody, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, respBody)
		_ = httpResp.Body.Close()
		logWithRequestID(ctx).Debugf("rovo request error: %d, %s", httpResp.StatusCode, string(respBody))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(respBody)}
	}

	outCh := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(outCh)
		defer httpResp.Body.Close()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB

		// Reuse Claude streaming translation since Bedrock format is similar
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			if detail, ok := parseClaudeStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}

			// If to == from, pass through directly (Claude format)
			if from == to {
				cloned := make([]byte, len(line)+1)
				copy(cloned, line)
				cloned[len(line)] = '\n'
				outCh <- cliproxyexecutor.StreamChunk{Payload: cloned}
				continue
			}

			// Translate chunks
			chunks := sdktranslator.TranslateStream(
				ctx,
				to,
				from,
				req.Model,
				originalPayload,
				body,
				bytes.Clone(line),
				&param,
			)
			for i := range chunks {
				outCh <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			outCh <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()

	return outCh, nil
}

func (e *RovoExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *RovoExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Not implemented for Rovo/Bedrock directly
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported"}
}
