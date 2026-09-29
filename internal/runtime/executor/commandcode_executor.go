package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	responses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	// CommandCodeProviderKey is the unique identifier for the Command Code provider.
	CommandCodeProviderKey = "commandcode"

	// CommandCodeDefaultBaseURL is the official production gateway URL for Command Code.
	CommandCodeDefaultBaseURL = "https://api.commandcode.ai"

	// CommandCodeGenerateEndpoint is the /alpha/generate route used by the CLI.
	CommandCodeGenerateEndpoint = "/alpha/generate"

	// CommandCodeProviderResponsesEndpoint is the official Provider API responses route.
	CommandCodeProviderResponsesEndpoint = "/provider/v1/responses"

	// CommandCodeDefaultVersion is the fallback CLI version header (verified from CLI release 0.52.1).
	CommandCodeDefaultVersion = "0.52.1"

	// CommandCodeMaxErrorBodySize defines the maximum bytes to read from non-2xx responses (1 MiB).
	CommandCodeMaxErrorBodySize = 1 * 1024 * 1024
)

// commandCodeStatusError carries the upstream HTTP status so auth lifecycle
// code can classify 401/402/429 etc. via errors.As(... StatusError). It
// follows the repository convention used by other executors (statusErr).
type commandCodeStatusError struct {
	code int
	msg  string
}

func (e commandCodeStatusError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("commandcode upstream error (status %d)", e.code)
}

func (e commandCodeStatusError) StatusCode() int { return e.code }

// normalizeCommandCodeStatusError maps a commandcode upstream failure status to
// the semantically correct billing status. commandcode.ai returns billing
// exhaustion as "400 bad_request" with an "insufficient credits" message
// instead of 402; the auth lifecycle (clienterror/conductor) classifies every
// 400 as a request-scoped fault and skips credential rotation for it, so the
// billing evidence in the body must be promoted to 402 up-front. Real
// PaymentRequired/429/5xx statuses pass through unchanged, and non-billing 400s
// (invalid model, context overflow, ...) keep their original request-fault
// classification.
func normalizeCommandCodeStatusError(httpStatus int, body []byte) int {
	if httpStatus == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "insufficient credits") {
		return http.StatusPaymentRequired
	}
	return httpStatus
}

func init() {
	// Register custom translators between OpenAI format and Command Code format.
	sdktranslator.Register(
		sdktranslator.FormatOpenAI,
		helps.FormatCommandCode,
		helps.ConvertOpenAIToCommandCodeRequest,
		sdktranslator.ResponseTransform{
			Stream:    helps.ConvertCommandCodeStreamToOpenAI,
			NonStream: helps.ConvertCommandCodeNonStreamToOpenAI,
		},
	)
}

// CommandCodeExecutor implements the Executor interface for Command Code /alpha/generate.
type CommandCodeExecutor struct {
	cfg *config.Config

	// BaseURL allows overriding the upstream gateway endpoint (useful for testing).
	BaseURL string

	// Version allows overriding the x-command-code-version header.
	Version string

	// HTTPClient allows custom HTTP transport (e.g. for testing or proxying).
	HTTPClient *http.Client
}

// NewCommandCodeExecutor creates a new Command Code executor instance.
func NewCommandCodeExecutor(cfg *config.Config) *CommandCodeExecutor {
	return &CommandCodeExecutor{
		cfg: cfg,
	}
}

// Identifier returns the provider key.
func (e *CommandCodeExecutor) Identifier() string {
	return CommandCodeProviderKey
}

// RequestToFormat reports the upstream request format used after auth selection.
func (e *CommandCodeExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return helps.FormatCommandCode
}

// resolveBaseURL gets the upstream base URL from Auth attributes, env, struct default, or config.
func (e *CommandCodeExecutor) resolveBaseURL(a *cliproxyauth.Auth) string {
	return e.resolveEndpoint(a, CommandCodeGenerateEndpoint)
}

func (e *CommandCodeExecutor) resolveProviderResponsesURL(a *cliproxyauth.Auth) string {
	return e.resolveEndpoint(a, CommandCodeProviderResponsesEndpoint)
}

func (e *CommandCodeExecutor) resolveEndpoint(a *cliproxyauth.Auth, defaultSuffix string) string {
	if a != nil && a.Attributes != nil {
		if ep := strings.TrimSpace(a.Attributes["endpoint"]); ep != "" {
			return ep
		}
		if bu := strings.TrimSpace(a.Attributes["base_url"]); bu != "" {
			return strings.TrimRight(bu, "/") + defaultSuffix
		}
	}
	if env := strings.TrimSpace(os.Getenv("COMMANDCODE_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/") + defaultSuffix
	}
	if env := strings.TrimSpace(os.Getenv("COMMAND_CODE_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/") + defaultSuffix
	}
	if e.BaseURL != "" {
		if strings.HasSuffix(e.BaseURL, CommandCodeGenerateEndpoint) || strings.HasSuffix(e.BaseURL, CommandCodeProviderResponsesEndpoint) {
			return e.BaseURL
		}
		return strings.TrimRight(e.BaseURL, "/") + defaultSuffix
	}
	return CommandCodeDefaultBaseURL + defaultSuffix
}

// shouldUseNativeResponses determines whether to route a responses request directly to official Provider API.
func (e *CommandCodeExecutor) shouldUseNativeResponses(ctx context.Context, a *cliproxyauth.Auth, model string) bool {
	apiKey, err := e.resolveAPIKey(a)
	if err != nil || apiKey == "" {
		return false
	}

	// 1. Check model capability
	if !registry.CommandCodeModelSupportsEndpoint(model, "/responses") {
		return false
	}

	// 2. Trigger non-blocking background tier sync
	registry.BackgroundSyncCommandCodeTier(ctx, apiKey, e.buildHTTPClient(a))

	// 3. Only route native if confirmed as Goat/Pro/Max/Team tier
	tier := registry.GetCommandCodeTier(apiKey)
	return tier == registry.CommandCodeTierGoat
}

// resolveVersion gets the x-command-code-version to send.
func (e *CommandCodeExecutor) resolveVersion(a *cliproxyauth.Auth) string {
	if a != nil && a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["version"]); v != "" {
			return v
		}
	}
	if env := strings.TrimSpace(os.Getenv("COMMANDCODE_VERSION")); env != "" {
		return env
	}
	if env := strings.TrimSpace(os.Getenv("COMMAND_CODE_VERSION")); env != "" {
		return env
	}
	if e.Version != "" {
		return e.Version
	}
	return CommandCodeDefaultVersion
}

// resolveAPIKey extracts the API key from Auth or environment.
func (e *CommandCodeExecutor) resolveAPIKey(a *cliproxyauth.Auth) (string, error) {
	if a != nil {
		if a.Attributes != nil {
			for _, k := range []string{"api_key", "apiKey"} {
				if ak := strings.TrimSpace(a.Attributes[k]); ak != "" {
					return ak, nil
				}
			}
		}
		if a.Metadata != nil {
			for _, k := range []string{"api_key", "apiKey"} {
				if ak, ok := a.Metadata[k].(string); ok && strings.TrimSpace(ak) != "" {
					return strings.TrimSpace(ak), nil
				}
			}
		}
	}
	if env := strings.TrimSpace(os.Getenv("COMMANDCODE_API_KEY")); env != "" {
		return env, nil
	}
	if env := strings.TrimSpace(os.Getenv("COMMAND_CODE_API_KEY")); env != "" {
		return env, nil
	}
	return "", errors.New("commandcode: missing API key (provide via Auth attributes or COMMANDCODE_API_KEY env)")
}

// buildHTTPClient constructs the HTTP client respecting proxy settings.
func (e *CommandCodeExecutor) buildHTTPClient(a *cliproxyauth.Auth) *http.Client {
	if e.HTTPClient != nil {
		return e.HTTPClient
	}
	if a == nil || strings.TrimSpace(a.ProxyURL) == "" {
		return http.DefaultClient
	}
	u, err := url.Parse(a.ProxyURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(u),
		},
	}
}

// PrepareRequest injects required authentication and protocol headers into the outbound HTTP request.
func (e *CommandCodeExecutor) PrepareRequest(req *http.Request, a *cliproxyauth.Auth) error {
	return e.prepareRequestWithAccept(req, a, "application/x-ndjson")
}

func (e *CommandCodeExecutor) PrepareNativeRequest(req *http.Request, a *cliproxyauth.Auth, stream bool) error {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	return e.prepareRequestWithAccept(req, a, accept)
}

func (e *CommandCodeExecutor) prepareRequestWithAccept(req *http.Request, a *cliproxyauth.Auth, accept string) error {
	if req == nil {
		return errors.New("commandcode: request is nil")
	}

	apiKey, errKey := e.resolveAPIKey(a)
	if errKey != nil {
		return errKey
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-command-code-version", e.resolveVersion(a))

	if req.Header.Get("x-session-id") == "" {
		sessionID := uuid.New().String()
		if a != nil && a.Attributes != nil {
			if sid := strings.TrimSpace(a.Attributes["session_id"]); sid != "" {
				sessionID = sid
			}
		}
		req.Header.Set("x-session-id", sessionID)
	}

	if os.Getenv("CMD_ZDR") == "1" {
		req.Header.Set("x-cmd-zdr", "1")
	}

	return nil
}

// Execute handles non-streaming client requests (stream: false).
// It sends stream: true to Command Code upstream and aggregates the resulting NDJSON lines
// into a single standard OpenAI ChatCompletion JSON response.
func (e *CommandCodeExecutor) Execute(ctx context.Context, a *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	client := e.buildHTTPClient(a)

	// If client requested Responses API and the account is eligible for native Provider API,
	// try the official /provider/v1/responses endpoint first.
	if opts.SourceFormat == sdktranslator.FormatOpenAIResponse && e.shouldUseNativeResponses(ctx, a, req.Model) {
		res, err := e.executeNativeResponses(ctx, a, client, req, opts)
		if err == nil {
			return res, nil
		}
		// If native endpoint returned 403 upgrade_required, record Go tier and smoothly fall back
		var statusErr commandCodeStatusError
		if errors.As(err, &statusErr) && statusErr.code == http.StatusForbidden && strings.Contains(statusErr.msg, "upgrade_required") {
			if apiKey, errKey := e.resolveAPIKey(a); errKey == nil && apiKey != "" {
				registry.SetCommandCodeTier(apiKey, registry.CommandCodeTierGo)
			}
			log.Warnf("commandcode: native responses 403 upgrade_required, falling back to harness translation for model %s", req.Model)
		} else {
			return cliproxyexecutor.Response{}, err
		}
	}

	endpoint := e.resolveBaseURL(a)

	payload := req.Payload
	if opts.SourceFormat == sdktranslator.FormatOpenAI || opts.SourceFormat == "" {
		// The gateway matches model ids strictly against the official catalog
		// spelling, while local registrations are lowercase; rewrite before
		// building the upstream envelope.
		upstreamModel := registry.CommandCodeUpstreamModelID(req.Model)
		payload = helps.ConvertOpenAIToCommandCodeRequest(upstreamModel, req.Payload, false)
	} else if opts.SourceFormat == sdktranslator.FormatOpenAIResponse {
		// OpenAI Responses API requests arrive as raw Responses JSON. Convert
		// them to Chat Completions first, then reuse the existing Chat
		// Completions → harness envelope conversion; skipping this chain sent
		// the untranslatable body upstream, which dropped the model field and
		// surfaced plan-gate 403s (MODEL_NOT_IN_PLAN for the default model).
		chatPayload := responses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(registry.CommandCodeUpstreamModelID(req.Model), req.Payload, false)
		payload = helps.ConvertOpenAIToCommandCodeRequest(registry.CommandCodeUpstreamModelID(req.Model), chatPayload, false)
	}

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if errNew != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode execute: failed to create request: %w", errNew)
	}

	if errPrep := e.PrepareRequest(httpReq, a); errPrep != nil {
		return cliproxyexecutor.Response{}, errPrep
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode execute: network error: %w", errDo)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, CommandCodeMaxErrorBodySize))
		code := normalizeCommandCodeStatusError(resp.StatusCode, bodyBytes)
		return cliproxyexecutor.Response{
			Payload: bodyBytes,
			Headers: resp.Header,
		}, commandCodeStatusError{code: code, msg: fmt.Sprintf("commandcode upstream error (status %d): %s", code, string(bodyBytes))}
	}

	// Read and aggregate the NDJSON stream into full ChatCompletion JSON
	ndjsonBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil && !errors.Is(errRead, io.EOF) {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode execute: failed to read response body: %w", errRead)
	}

	openaiJSON, errAccum := helps.AccumulateNDJSON(ctx, req.Model, ndjsonBytes)
	if errAccum != nil {
		return cliproxyexecutor.Response{
			Headers: resp.Header,
		}, fmt.Errorf("commandcode execute aggregation error: %w", errAccum)
	}

	// Downstream /v1/responses clients expect a Responses object, not a Chat
	// Completions payload; the gateway does not run an executor-side response
	// translation pass, so translate here.
	if opts.ResponseFormat == sdktranslator.FormatOpenAIResponse {
		openaiJSON = responses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, req.Model, opts.OriginalRequest, req.Payload, openaiJSON, nil)
	}

	return cliproxyexecutor.Response{
		Payload: openaiJSON,
		Headers: resp.Header,
	}, nil
}

func (e *CommandCodeExecutor) executeNativeResponses(ctx context.Context, a *cliproxyauth.Auth, client *http.Client, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	endpoint := e.resolveProviderResponsesURL(a)
	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(req.Payload))
	if errNew != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode execute_native_responses: failed to create request: %w", errNew)
	}
	if errPrep := e.PrepareNativeRequest(httpReq, a, false); errPrep != nil {
		return cliproxyexecutor.Response{}, errPrep
	}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode execute_native_responses: network error: %w", errDo)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, CommandCodeMaxErrorBodySize))
		code := normalizeCommandCodeStatusError(resp.StatusCode, bodyBytes)
		return cliproxyexecutor.Response{
			Payload: bodyBytes,
			Headers: resp.Header,
		}, commandCodeStatusError{code: code, msg: fmt.Sprintf("commandcode upstream error (status %d): %s", code, string(bodyBytes))}
	}

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil && !errors.Is(errRead, io.EOF) {
		return cliproxyexecutor.Response{}, fmt.Errorf("commandcode execute_native_responses: failed to read response body: %w", errRead)
	}
	return cliproxyexecutor.Response{
		Payload: bodyBytes,
		Headers: resp.Header,
	}, nil
}

// ExecuteStream handles streaming client requests (stream: true).
// It streams NDJSON from upstream and emits OpenAI SSE chunks (`chat.completion.chunk`).
func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, a *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	client := e.buildHTTPClient(a)

	// If client requested Responses API and the account is eligible for native Provider API,
	// try the official /provider/v1/responses endpoint first.
	if opts.SourceFormat == sdktranslator.FormatOpenAIResponse && e.shouldUseNativeResponses(ctx, a, req.Model) {
		res, err := e.executeNativeResponsesStream(ctx, a, client, req, opts)
		if err == nil {
			return res, nil
		}
		var statusErr commandCodeStatusError
		if errors.As(err, &statusErr) && statusErr.code == http.StatusForbidden && strings.Contains(statusErr.msg, "upgrade_required") {
			if apiKey, errKey := e.resolveAPIKey(a); errKey == nil && apiKey != "" {
				registry.SetCommandCodeTier(apiKey, registry.CommandCodeTierGo)
			}
			log.Warnf("commandcode: native responses stream 403 upgrade_required, falling back to harness translation for model %s", req.Model)
		} else {
			return nil, err
		}
	}

	endpoint := e.resolveBaseURL(a)

	payload := req.Payload
	if opts.SourceFormat == sdktranslator.FormatOpenAI || opts.SourceFormat == "" {
		// Same upstream spelling rewrite as the non-streaming path.
		upstreamModel := registry.CommandCodeUpstreamModelID(req.Model)
		payload = helps.ConvertOpenAIToCommandCodeRequest(upstreamModel, req.Payload, true)
	} else if opts.SourceFormat == sdktranslator.FormatOpenAIResponse {
		// Same two-stage Responses-API request repair as the non-streaming
		// path: Responses → Chat Completions → harness envelope.
		chatPayload := responses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(registry.CommandCodeUpstreamModelID(req.Model), req.Payload, true)
		payload = helps.ConvertOpenAIToCommandCodeRequest(registry.CommandCodeUpstreamModelID(req.Model), chatPayload, true)
	}

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if errNew != nil {
		return nil, fmt.Errorf("commandcode execute_stream: failed to create request: %w", errNew)
	}

	if errPrep := e.PrepareRequest(httpReq, a); errPrep != nil {
		return nil, errPrep
	}

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return nil, fmt.Errorf("commandcode execute_stream: network error: %w", errDo)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, CommandCodeMaxErrorBodySize))
		code := normalizeCommandCodeStatusError(resp.StatusCode, bodyBytes)
		return nil, commandCodeStatusError{code: code, msg: fmt.Sprintf("commandcode upstream error (status %d): %s", code, string(bodyBytes))}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer func() {
			_ = resp.Body.Close()
			close(chunks)
		}()

		reader := helps.NewUnboundedNDJSONReader(resp.Body)
		var param any
		var responsesParam any
		sawTerminal := false
		emit := func(chunk []byte) bool {
			select {
			case <-ctx.Done():
				chunks <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
				return false
			case chunks <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				return true
			}
		}

		for {
			select {
			case <-ctx.Done():
				chunks <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
				return
			default:
			}

			line, err := reader.ReadNextLine(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					if !sawTerminal {
						chunks <- cliproxyexecutor.StreamChunk{Err: errors.New("commandcode: stream ended before terminal event")}
					}
					return
				}
				chunks <- cliproxyexecutor.StreamChunk{Err: err}
				return
			}

			// Validate line
			event, errParse := helps.ParseRawJSONLine(line)
			if errParse != nil {
				chunks <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("malformed NDJSON line: %w", errParse)}
				return
			}

			if event.Type == "error" {
				errMsg := event.Message
				if errMsg == "" && event.Error != nil {
					errMsg = fmt.Sprintf("%v", event.Error)
				}
				if errMsg == "" {
					errMsg = "Command Code upstream stream error"
				}
				chunks <- cliproxyexecutor.StreamChunk{Err: errors.New(errMsg)}
				return
			}

			if event.Type == "finish-step" || event.Type == "finish" {
				sawTerminal = true
			}

			sseChunks := helps.ConvertCommandCodeStreamToOpenAI(ctx, req.Model, opts.OriginalRequest, req.Payload, line, &param)
			if opts.ResponseFormat == sdktranslator.FormatOpenAIResponse {
				// Downstream is a /v1/responses client: translate each Chat
				// Completions chunk into Responses SSE events. Emit a terminal
				// [DONE] frame after finish-step/finish because the ndjson
				// converter never produces one, and the Responses converter
				// relies on it to flush its completed event.
				done := false
				for _, chunk := range sseChunks {
					for _, respChunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, req.Model, opts.OriginalRequest, req.Payload, chunk, &responsesParam) {
						if !emit(respChunk) {
							done = true
							break
						}
					}
					if done {
						break
					}
				}
				if !done && (event.Type == "finish-step" || event.Type == "finish") {
					// Feed [DONE] through the converter first: the Responses
					// converter defers response.completed until it sees the
					// terminal marker, then emit the literal [DONE] frame.
					for _, respChunk := range sdktranslator.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, req.Model, opts.OriginalRequest, req.Payload, []byte("[DONE]"), &responsesParam) {
						if !emit(respChunk) {
							done = true
							break
						}
					}
					if !done {
						emit([]byte("[DONE]"))
					}
				}
				continue
			}
			for _, chunk := range sseChunks {
				select {
				case <-ctx.Done():
					chunks <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
					return
				case chunks <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				}
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: resp.Header,
		Chunks:  chunks,
	}, nil
}

func (e *CommandCodeExecutor) executeNativeResponsesStream(ctx context.Context, a *cliproxyauth.Auth, client *http.Client, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	endpoint := e.resolveProviderResponsesURL(a)
	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(req.Payload))
	if errNew != nil {
		return nil, fmt.Errorf("commandcode execute_native_responses_stream: failed to create request: %w", errNew)
	}
	if errPrep := e.PrepareNativeRequest(httpReq, a, true); errPrep != nil {
		return nil, errPrep
	}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return nil, fmt.Errorf("commandcode execute_native_responses_stream: network error: %w", errDo)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, CommandCodeMaxErrorBodySize))
		code := normalizeCommandCodeStatusError(resp.StatusCode, bodyBytes)
		return nil, commandCodeStatusError{code: code, msg: fmt.Sprintf("commandcode upstream error (status %d): %s", code, string(bodyBytes))}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)
	go func() {
		defer func() {
			_ = resp.Body.Close()
			close(chunks)
		}()
		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			// Native SSE line passthrough
			payload := make([]byte, len(line)+1)
			copy(payload, line)
			payload[len(line)] = '\n'

			select {
			case <-ctx.Done():
				chunks <- cliproxyexecutor.StreamChunk{Err: ctx.Err()}
				return
			case chunks <- cliproxyexecutor.StreamChunk{Payload: payload}:
			}
		}
		if errScan := scanner.Err(); errScan != nil && !errors.Is(errScan, io.EOF) {
			select {
			case <-ctx.Done():
			case chunks <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("commandcode native stream read: %w", errScan)}:
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{
		Headers: resp.Header,
		Chunks:  chunks,
	}, nil
}

// HttpRequest injects provider credentials into an arbitrary HTTP request and executes it.
func (e *CommandCodeExecutor) HttpRequest(ctx context.Context, a *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("commandcode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrep := e.PrepareRequest(httpReq, a); errPrep != nil {
		return nil, errPrep
	}
	client := e.buildHTTPClient(a)
	return client.Do(httpReq)
}

// CountTokens is currently not natively supported by Command Code /alpha/generate.
func (e *CommandCodeExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("commandcode: count tokens not implemented")
}

// Refresh returns the auth unchanged as Command Code uses static API keys.
func (e *CommandCodeExecutor) Refresh(_ context.Context, a *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return a, nil
}
