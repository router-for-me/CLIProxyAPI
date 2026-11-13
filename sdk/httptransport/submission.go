package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
)

type submissionKey struct{}
type submissionState struct {
	accepted atomic.Bool
	terminal atomic.Pointer[OutcomeUnknownError]
}

func (s *submissionState) fail(err error) error {
	unknown := &OutcomeUnknownError{Cause: err}
	s.terminal.CompareAndSwap(nil, unknown)
	return s.terminal.Load()
}

// WithSubmissionTracking starts one provider attempt. Explicit HTTP refusals do
// not make later attempts ambiguous; a successful response does.
func WithSubmissionTracking(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, submissionKey{}, &submissionState{})
}

// OutcomeUnknownError means the provider may have executed the request. Its
// transport cause is available to operators, never included in the public body.
type OutcomeUnknownError struct{ Cause error }

func (*OutcomeUnknownError) Error() string {
	return `{"error":{"message":"Upstream disconnected after submission. The request was not replayed and its outcome is unknown.","type":"server_error","code":"upstream_outcome_unknown","retryable":false}}`
}
func (e *OutcomeUnknownError) Unwrap() error       { return e.Cause }
func (*OutcomeUnknownError) StatusCode() int       { return http.StatusBadGateway }
func (*OutcomeUnknownError) IsRequestScoped() bool { return true }

// IsOutcomeUnknown reports an attempt that must never be replayed, even when a
// configured error rule would otherwise select another model or credential.
func IsOutcomeUnknown(err error) bool {
	var unknown *OutcomeUnknownError
	return errors.As(err, &unknown)
}

// ClassifySubmissionError stops failures detected after an accepted HTTP response.
// Explicit HTTP refusals never set the accepted flag.
func ClassifySubmissionError(ctx context.Context, err error) error {
	if err == nil || IsOutcomeUnknown(err) || ctx == nil {
		return err
	}
	state, _ := ctx.Value(submissionKey{}).(*submissionState)
	if state == nil {
		return err
	}
	if terminal := state.terminal.Load(); terminal != nil {
		return terminal
	}
	if !state.accepted.Load() {
		return err
	}
	return state.fail(err)
}

type submissionTransport struct {
	next  http.RoundTripper
	state *submissionState
}

func (s *submissionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if terminal := s.state.terminal.Load(); terminal != nil {
		return nil, terminal
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return s.next.RoundTrip(req)
	}
	var written, responseStarted atomic.Bool
	trace := &httptrace.ClientTrace{
		// Even a partial body can be accepted by an upstream that replies early.
		WroteHeaders: func() { written.Store(true) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				written.Store(true)
			}
		},
		GotFirstResponseByte: func() { responseStarted.Store(true) },
	}
	cloned := req.Clone(httptrace.WithClientTrace(req.Context(), trace))
	// A replayable body lets net/http repeat a POST with an Idempotency-Key inside
	// RoundTrip, before the manager can inspect the failed attempt.
	cloned.GetBody = nil
	response, err := s.next.RoundTrip(cloned)
	if err != nil {
		if written.Load() || responseStarted.Load() {
			return nil, s.state.fail(err)
		}
		return response, err
	}
	if response != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		s.state.accepted.Store(true)
		if response.Body != nil {
			response.Body = &submissionBody{ReadCloser: response.Body, state: s.state}
		}
	}
	return response, nil
}

type submissionBody struct {
	io.ReadCloser
	state *submissionState
}

func (b *submissionBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = b.state.fail(err)
	}
	return n, err
}
