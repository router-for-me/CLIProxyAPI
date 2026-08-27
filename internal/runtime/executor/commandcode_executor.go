package executor

import (
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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
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
	if a != nil && a.Attributes != nil {
		if ep := strings.TrimSpace(a.Attributes["endpoint"]); ep != "" {
			return ep
		}
		if bu := strings.TrimSpace(a.Attributes["base_url"]); bu != "" {
			return strings.TrimRight(bu, "/") + CommandCodeGenerateEndpoint
		}
	}
	if env := strings.TrimSpace(os.Getenv("COMMANDCODE_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/") + CommandCodeGenerateEndpoint
	}
	if env := strings.TrimSpace(os.Getenv("COMMAND_CODE_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/") + CommandCodeGenerateEndpoint
	}
	if e.BaseURL != "" {
		if strings.HasSuffix(e.BaseURL, CommandCodeGenerateEndpoint) {
			return e.BaseURL
		}
		return strings.TrimRight(e.BaseURL, "/") + CommandCodeGenerateEndpoint
	}
	return CommandCodeDefaultBaseURL + CommandCodeGenerateEndpoint
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
	if req == nil {
		return errors.New("commandcode: request is nil")
	}

	apiKey, errKey := e.resolveAPIKey(a)
	if errKey != nil {
		return errKey
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
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
	endpoint := e.resolveBaseURL(a)

	payload := req.Payload
	if opts.SourceFormat == sdktranslator.FormatOpenAI || opts.SourceFormat == "" {
		// The gateway matches model ids strictly against the official catalog
		// spelling, while local registrations are lowercase; rewrite before
		// building the upstream envelope.
		upstreamModel := registry.CommandCodeUpstreamModelID(req.Model)
		payload = helps.ConvertOpenAIToCommandCodeRequest(upstreamModel, req.Payload, false)
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
		return cliproxyexecutor.Response{
			Payload: bodyBytes,
			Headers: resp.Header,
		}, commandCodeStatusError{code: resp.StatusCode, msg: fmt.Sprintf("commandcode upstream error (status %d): %s", resp.StatusCode, string(bodyBytes))}
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

	return cliproxyexecutor.Response{
		Payload: openaiJSON,
		Headers: resp.Header,
	}, nil
}

// ExecuteStream handles streaming client requests (stream: true).
// It streams NDJSON from upstream and emits OpenAI SSE chunks (`chat.completion.chunk`).
func (e *CommandCodeExecutor) ExecuteStream(ctx context.Context, a *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	client := e.buildHTTPClient(a)
	endpoint := e.resolveBaseURL(a)

	payload := req.Payload
	if opts.SourceFormat == sdktranslator.FormatOpenAI || opts.SourceFormat == "" {
		// Same upstream spelling rewrite as the non-streaming path.
		upstreamModel := registry.CommandCodeUpstreamModelID(req.Model)
		payload = helps.ConvertOpenAIToCommandCodeRequest(upstreamModel, req.Payload, true)
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
		return nil, commandCodeStatusError{code: resp.StatusCode, msg: fmt.Sprintf("commandcode upstream error (status %d): %s", resp.StatusCode, string(bodyBytes))}
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 64)

	go func() {
		defer func() {
			_ = resp.Body.Close()
			close(chunks)
		}()

		reader := helps.NewUnboundedNDJSONReader(resp.Body)
		var param any
		sawTerminal := false

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
