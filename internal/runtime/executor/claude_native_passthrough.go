package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const nativeClaudeMessagesURL = "https://api.anthropic.com/v1/messages?beta=true"

const nativeClaudeCountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens?beta=true"

type nativeClaudeIdentityMode uint8

const (
	nativeClaudeIdentityRequired nativeClaudeIdentityMode = iota
	nativeClaudeIdentityStrip
)

// executeNativeClaude is the transparent path used only by the trusted native
// Claude adapter. The native process owns the request protocol. The proxy owns
// only the selected credential, so this path replaces credential-bound identity
// and its CCH signature without applying any of the generic translation,
// thinking, sampling, cache, tool, beta, model, or stream normalization.
func (e *ClaudeExecutor) executeNativeClaude(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (cliproxyexecutor.Response, error) {
	httpResp, errRequest := e.sendNativeClaudeRequest(ctx, auth, req.Payload, nativeClaudeMessagesURL, nativeClaudeIdentityRequired)
	if errRequest != nil {
		return cliproxyexecutor.Response{}, errRequest
	}

	body, headers, errRead := readNativeClaudeResponse(httpResp)
	if errRead != nil {
		return cliproxyexecutor.Response{}, errRead
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return cliproxyexecutor.Response{}, newClaudeNativeDirectResponseError(httpResp.StatusCode, headers, body)
	}
	return cliproxyexecutor.Response{Payload: body, Headers: headers}, nil
}

func (e *ClaudeExecutor) countNativeClaudeTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (cliproxyexecutor.Response, error) {
	httpResp, errRequest := e.sendNativeClaudeRequest(ctx, auth, req.Payload, nativeClaudeCountTokensURL, nativeClaudeIdentityStrip)
	if errRequest != nil {
		return cliproxyexecutor.Response{}, errRequest
	}
	body, headers, errRead := readNativeClaudeResponse(httpResp)
	if errRead != nil {
		return cliproxyexecutor.Response{}, errRead
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return cliproxyexecutor.Response{}, newClaudeNativeDirectResponseError(httpResp.StatusCode, headers, body)
	}
	return cliproxyexecutor.Response{Payload: body, Headers: headers}, nil
}

// executeNativeClaudeStream connects before returning and then relays each read
// exactly as received. In particular, it does not scan, split, join, rebuild,
// classify, or terminate on SSE events. Only an HTTP error received before the
// body starts is eligible for another credential.
func (e *ClaudeExecutor) executeNativeClaudeStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (*cliproxyexecutor.StreamResult, error) {
	httpResp, errRequest := e.sendNativeClaudeRequest(ctx, auth, req.Payload, nativeClaudeMessagesURL, nativeClaudeIdentityRequired)
	if errRequest != nil {
		return nil, errRequest
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		body, headers, errRead := readNativeClaudeResponse(httpResp)
		if errRead != nil {
			return nil, errRead
		}
		return nil, newClaudeNativeDirectResponseError(httpResp.StatusCode, headers, body)
	}

	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go relayNativeClaudeStream(ctx, httpResp, out)
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *ClaudeExecutor) sendNativeClaudeRequest(ctx context.Context, auth *cliproxyauth.Auth, payload []byte, upstreamURL string, identityMode nativeClaudeIdentityMode) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	nativeHeaders, ok := cliproxyexecutor.NativeClaudeProtocolHeadersFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("native Claude request is missing its trusted protocol headers")
	}

	body := payload
	sessionID := helps.ExtractClaudeCodeSessionID(ctx, payload, nativeHeaders)
	if identityMode == nativeClaudeIdentityStrip {
		// Native Claude omits metadata on count_tokens, and Anthropic rejects the
		// ordinary identity-only shape there. Remove only the master-bound user_id.
		// Preserve any unknown sibling fields so a future native protocol addition
		// is not silently discarded; Anthropic remains the authority on whether it
		// accepts those fields.
		if gjson.GetBytes(payload, "metadata").Exists() {
			var errDelete error
			body, errDelete = sjson.DeleteBytes(payload, "metadata.user_id")
			if errDelete != nil {
				return nil, fmt.Errorf("remove native Claude count identity: %w", errDelete)
			}
			remaining := gjson.GetBytes(body, "metadata")
			if remaining.IsObject() && len(remaining.Map()) == 0 {
				body, errDelete = sjson.DeleteBytes(body, "metadata")
				if errDelete != nil {
					return nil, fmt.Errorf("remove empty native Claude count metadata: %w", errDelete)
				}
			}
		}
	} else {
		// The session is conversation state, not account identity. Preserve it
		// while replacing only the device and account fields tied to the selected
		// login.
		if sessionID == "" {
			sessionID = helps.ClaudeAgentSessionUUID(nativeHeaders, payload, payload)
		}
		var errIdentity error
		body, _, errIdentity = helps.ApplyClaudeCredentialMetadata(payload, auth, sessionID)
		if errIdentity != nil {
			return nil, errIdentity
		}
	}
	// Any credential-bound edit changes the CCH input. This is a no-op when the
	// body does not carry a native billing CCH (the measured count shape does not).
	body, errCCH := signAnthropicMessagesBody(body)
	if errCCH != nil {
		return nil, fmt.Errorf("re-sign native Claude CCH: %w", errCCH)
	}

	httpReq, errNewRequest := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if errNewRequest != nil {
		return nil, errNewRequest
	}
	httpReq.Header = nativeHeaders
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	// PrepareRequest is the credential authority, but native headers are the
	// protocol authority. Discard every other header it may have synthesized.
	preparedHeaders := httpReq.Header
	httpReq.Header = nativeHeaders.Clone()
	for _, name := range []string{"Authorization", "X-Api-Key"} {
		if values := preparedHeaders.Values(name); len(values) > 0 {
			httpReq.Header[name] = append([]string(nil), values...)
		}
	}
	if httpReq.Header.Get("X-Claude-Code-Session-Id") == "" && sessionID != "" {
		httpReq.Header.Set("X-Claude-Code-Session-Id", sessionID)
	}

	authID, authLabel, authType, authValue := claudeAuthLogIdentity(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       upstreamURL,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.upstreamRequestLogProvider(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	// Zero means no client timeout. After the upstream connection is established,
	// only caller cancellation controls the lifetime of a native stream.
	httpClient := helps.NewRawUtlsHTTPClient(ctx, e.cfg, auth, 0)
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	return httpResp, nil
}

func readNativeClaudeResponse(resp *http.Response) ([]byte, http.Header, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil, fmt.Errorf("native Claude upstream returned an empty response")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, nil, fmt.Errorf("read native Claude response: %w", errRead)
	}
	return body, resp.Header.Clone(), nil
}

func relayNativeClaudeStream(ctx context.Context, resp *http.Response, out chan<- cliproxyexecutor.StreamChunk) {
	defer close(out)
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	buffer := make([]byte, 32*1024)
	for {
		count, errRead := resp.Body.Read(buffer)
		if count > 0 {
			payload := bytes.Clone(buffer[:count])
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
			case <-ctx.Done():
				return
			}
		}
		if errRead != nil {
			if errRead != io.EOF {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
				case <-ctx.Done():
				}
			}
			return
		}
	}
}

// claudeNativeDirectResponseError preserves Anthropic's status, headers, and
// body through retries and the final downstream response. It deliberately does
// not wrap RequestTerminatedError: a credential-scoped 429 must remain eligible
// for another selected subscription before any response bytes are exposed.
type claudeNativeDirectResponseError struct {
	status           int
	headers          http.Header
	body             []byte
	retryAfter       *time.Duration
	credentialScoped bool
}

func newClaudeNativeDirectResponseError(status int, headers http.Header, body []byte) error {
	err := &claudeNativeDirectResponseError{
		status:  status,
		headers: headers.Clone(),
		body:    bytes.Clone(body),
	}
	if status == http.StatusTooManyRequests {
		err.retryAfter = helps.ParseClaudeRateLimitReset(headers, time.Now())
		err.credentialScoped = helps.ClaudeHeadersIndicateUnifiedRateLimitRejection(headers)
	}
	return err
}

func (e *claudeNativeDirectResponseError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("native Claude upstream request failed with status %d", e.status)
}

func (e *claudeNativeDirectResponseError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *claudeNativeDirectResponseError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return e.headers.Clone()
}

func (e *claudeNativeDirectResponseError) ResponseHeaders() http.Header { return e.Headers() }

func (e *claudeNativeDirectResponseError) ResponseBody() []byte {
	if e == nil {
		return nil
	}
	return bytes.Clone(e.body)
}

func (e *claudeNativeDirectResponseError) DirectResponse() bool { return e != nil }

func (e *claudeNativeDirectResponseError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter == nil {
		return nil
	}
	value := *e.retryAfter
	return &value
}

func (e *claudeNativeDirectResponseError) IsCredentialScoped() bool {
	return e != nil && e.credentialScoped
}
