package helps

import (
	"net/http"
	"time"
)

// UpstreamResponseError retains an upstream error body while participating in auth retries.
type UpstreamResponseError struct {
	Code       int
	Header     http.Header
	Body       []byte
	RetryDelay *time.Duration
}

func (e *UpstreamResponseError) Error() string              { return string(e.Body) }
func (e *UpstreamResponseError) StatusCode() int            { return e.Code }
func (e *UpstreamResponseError) RetryAfter() *time.Duration { return e.RetryDelay }
func (e *UpstreamResponseError) Headers() http.Header       { return e.Header.Clone() }
