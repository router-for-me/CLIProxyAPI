package executor

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// claudeFastRequestError marks a Fast request failure as request-scoped. Fast
// errors must stop at the caller: they do not justify retrying another
// credential or changing the selected credential's availability, unless the failure
// is a genuine credential-level rate limit.
type claudeFastRequestError struct {
	cause      error
	status     int
	retryAfter *time.Duration
}

func (e *claudeFastRequestError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *claudeFastRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *claudeFastRequestError) StatusCode() int {
	if e == nil || (e.status >= http.StatusOK && e.status < http.StatusMultipleChoices) {
		return 0
	}
	return e.status
}

func (e *claudeFastRequestError) IsRequestScoped() bool {
	if e == nil {
		return false
	}
	if e.IsCredentialScoped() {
		return false
	}
	return true
}

func (e *claudeFastRequestError) IsCredentialScoped() bool {
	if e == nil {
		return false
	}
	type credentialScopedProvider interface {
		IsCredentialScoped() bool
	}
	var csp credentialScopedProvider
	if errors.As(e.cause, &csp) && csp != nil {
		return csp.IsCredentialScoped()
	}
	return false
}

func (e *claudeFastRequestError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	return e.retryAfter
}

// claudeFastDirectResponseError carries an upstream HTTP error response through
// the auth manager and protocol handlers without retrying or rebuilding its
// status and JSON body.
type claudeFastDirectResponseError struct {
	response         *cliproxyexecutor.RequestTerminatedError
	retryAfter       *time.Duration
	credentialScoped bool
}

func (e *claudeFastDirectResponseError) Error() string {
	if e == nil || e.response == nil {
		return ""
	}
	return fmt.Sprintf("claude Fast upstream request failed with status %d", e.response.HTTPStatus)
}

func (e *claudeFastDirectResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.response
}

func (e *claudeFastDirectResponseError) IsRequestScoped() bool {
	if e == nil {
		return false
	}
	if e.credentialScoped {
		return false
	}
	return true
}

func (e *claudeFastDirectResponseError) IsCredentialScoped() bool {
	if e == nil {
		return false
	}
	return e.credentialScoped
}

func (e *claudeFastDirectResponseError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	return e.retryAfter
}

func wrapClaudeFastRequestError(fastRequest bool, status int, err error) error {
	if err == nil || !fastRequest {
		return err
	}
	var retryAfter *time.Duration
	if rap, ok := err.(interface{ RetryAfter() *time.Duration }); ok && rap != nil {
		retryAfter = rap.RetryAfter()
	}
	return &claudeFastRequestError{cause: err, status: status, retryAfter: retryAfter}
}

func newClaudeFastDirectResponseError(resp *http.Response, body []byte) error {
	if resp == nil {
		return nil
	}
	headers := resp.Header.Clone()
	// body has already been decoded. Do not forward stale representation or
	// length headers that describe the compressed upstream bytes.
	headers.Del("Content-Encoding")
	headers.Del("Content-Length")

	var retryAfter *time.Duration
	credentialScoped := false
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = helps.ParseClaudeRateLimitReset(resp.Header, time.Now())
		if helps.ClaudeHeadersIndicateUnifiedRateLimitRejection(resp.Header) {
			credentialScoped = true
		}
	}

	return &claudeFastDirectResponseError{
		response: &cliproxyexecutor.RequestTerminatedError{
			HTTPStatus: resp.StatusCode,
			Header:     headers,
			Body:       bytes.Clone(body),
		},
		retryAfter:       retryAfter,
		credentialScoped: credentialScoped,
	}
}

func claudeRequestIsFast(req *http.Request, body []byte) bool {
	if req == nil {
		return false
	}
	betas := strings.Join(req.Header.Values("Anthropic-Beta"), ",")
	return claudeRequestUsesFastMode(body, claudeRequestedBetas(betas, nil))
}

func claudeAuthLogIdentity(auth *cliproxyauth.Auth) (id, label, authType, authValue string) {
	if auth == nil {
		return "", "", "", ""
	}
	authType, authValue = auth.AccountInfo()
	return auth.ID, auth.Label, authType, authValue
}
