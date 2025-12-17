package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules/amp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// AmpExecutor proxies LLM requests to ampcode.com using the existing Amp proxy infrastructure.
// It implements ProviderExecutor to integrate with AuthManager's retry/fallback logic.
// Supports routing to multiple upstream providers (anthropic, google, openai) based on SourceFormat.
type AmpExecutor struct {
	cfg          *config.Config
	secretSource amp.SecretSource
}

// NewAmpExecutor creates an executor that proxies requests to ampcode.com.
func NewAmpExecutor(cfg *config.Config, secretSource amp.SecretSource) *AmpExecutor {
	return &AmpExecutor{
		cfg:          cfg,
		secretSource: secretSource,
	}
}

func (e *AmpExecutor) Identifier() string { return "amp" }

func (e *AmpExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey, err := e.secretSource.Get(ctx)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("amp executor: failed to get API key: %w", err)
	}
	if apiKey == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("amp executor: no API key available")
	}

	body := opts.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}

	url := e.buildUpstreamURL(opts.SourceFormat, req.Model, false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	applyAmpHeaders(httpReq, apiKey, opts.Headers)

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
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: `{"error":"amp_upstream_proxy_error","message":"Failed to reach Amp upstream"}`}
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("amp request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("amp executor: response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	// Handle Content-Encoding if present (explicit gzip/br/zstd), otherwise check for gzip magic bytes
	contentEncoding := httpResp.Header.Get("Content-Encoding")
	var decodedBody io.ReadCloser
	if contentEncoding != "" {
		decodedBody, err = decodeResponseBody(httpResp.Body, contentEncoding)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			_ = httpResp.Body.Close()
			return cliproxyexecutor.Response{}, err
		}
	} else {
		// No Content-Encoding, but ampcode.com may still send gzip without the header
		if err := util.DecompressGzipIfNeeded(httpResp); err != nil {
			log.Warnf("amp executor: gzip decompression error: %v", err)
		}
		decodedBody = httpResp.Body
	}

	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("amp executor: response body close error: %v", errClose)
		}
	}()

	data, err := io.ReadAll(decodedBody)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	return cliproxyexecutor.Response{Payload: data}, nil
}

func (e *AmpExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	apiKey, err := e.secretSource.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("amp executor: failed to get API key: %w", err)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("amp executor: no API key available")
	}

	body := opts.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}

	url := e.buildUpstreamURL(opts.SourceFormat, req.Model, true)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	applyAmpHeaders(httpReq, apiKey, opts.Headers)

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
		return nil, statusErr{code: http.StatusBadGateway, msg: `{"error":"amp_upstream_proxy_error","message":"Failed to reach Amp upstream"}`}
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("amp stream request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("amp executor: stream body close error: %v", errClose)
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("amp executor: stream body close error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			cloned := make([]byte, len(line)+1)
			copy(cloned, line)
			cloned[len(line)] = '\n'
			out <- cliproxyexecutor.StreamChunk{Payload: cloned}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()

	return out, nil
}

func (e *AmpExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// CountTokens endpoint only exists for Claude/Anthropic
	// Other providers (OpenAI, Gemini) don't have a proxied count_tokens endpoint
	if opts.SourceFormat != sdktranslator.FormatClaude {
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: `{"error":"count_tokens not supported for this provider"}`}
	}

	apiKey, err := e.secretSource.Get(ctx)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("amp executor: failed to get API key: %w", err)
	}
	if apiKey == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("amp executor: no API key available")
	}

	body := opts.OriginalRequest
	if len(body) == 0 {
		body = req.Payload
	}

	url := fmt.Sprintf("%s/api/provider/anthropic/v1/messages/count_tokens", e.upstreamURL())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	applyAmpHeaders(httpReq, apiKey, opts.Headers)

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
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusBadGateway, msg: `{"error":"amp_upstream_proxy_error","message":"Failed to reach Amp upstream"}`}
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("amp count_tokens error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("amp executor: response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	// Handle Content-Encoding if present, otherwise check for gzip magic bytes
	contentEncoding := httpResp.Header.Get("Content-Encoding")
	var decodedBody io.ReadCloser
	if contentEncoding != "" {
		decodedBody, err = decodeResponseBody(httpResp.Body, contentEncoding)
		if err != nil {
			recordAPIResponseError(ctx, e.cfg, err)
			_ = httpResp.Body.Close()
			return cliproxyexecutor.Response{}, err
		}
	} else {
		if err := util.DecompressGzipIfNeeded(httpResp); err != nil {
			log.Warnf("amp executor: gzip decompression error: %v", err)
		}
		decodedBody = httpResp.Body
	}

	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("amp executor: response body close error: %v", errClose)
		}
	}()

	data, err := io.ReadAll(decodedBody)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	return cliproxyexecutor.Response{Payload: data}, nil
}

func (e *AmpExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *AmpExecutor) upstreamURL() string {
	return strings.TrimSuffix(e.cfg.AmpCode.UpstreamURL, "/")
}

// buildUpstreamURL constructs the appropriate ampcode.com endpoint URL based on the source format.
// For Gemini, the model name is included in the path with the appropriate action.
func (e *AmpExecutor) buildUpstreamURL(sourceFormat sdktranslator.Format, model string, stream bool) string {
	upstreamURL := e.upstreamURL()

	switch sourceFormat {
	case sdktranslator.FormatClaude:
		return fmt.Sprintf("%s/api/provider/anthropic/v1/messages", upstreamURL)

	case sdktranslator.FormatGemini, sdktranslator.FormatGeminiCLI:
		// Gemini uses path-based model/action format
		// AMP CLI sends to: /api/provider/google/v1beta1/publishers/google/models/{model}:{action}
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		return fmt.Sprintf("%s/api/provider/google/v1beta1/publishers/google/models/%s:%s", upstreamURL, model, action)

	case sdktranslator.FormatOpenAI, sdktranslator.FormatCodex:
		return fmt.Sprintf("%s/api/provider/openai/v1/chat/completions", upstreamURL)

	case sdktranslator.FormatOpenAIResponse:
		return fmt.Sprintf("%s/api/provider/openai/v1/responses", upstreamURL)

	default:
		// Default to OpenAI format for unknown formats
		return fmt.Sprintf("%s/api/provider/openai/v1/chat/completions", upstreamURL)
	}
}

// applyAmpHeaders copies all source headers and overrides auth for ampcode.com.
func applyAmpHeaders(r *http.Request, apiKey string, sourceHeaders http.Header) {
	for key, values := range sourceHeaders {
		for _, val := range values {
			r.Header.Add(key, val)
		}
	}

	// Override auth headers with our ampcode.com credentials
	r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	r.Header.Set("X-Api-Key", apiKey)

	// Ensure Content-Type is set
	if r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}
}
