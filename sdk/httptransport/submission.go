package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moego/ojson"
)

type submissionKey struct{}
type submissionState struct {
	accepted   atomic.Bool
	redirected atomic.Bool
	terminal   atomic.Pointer[OutcomeUnknownError]
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

// WithoutSubmissionTracking keeps side-effect-free operations replayable even
// when their context came from a generation attempt.
func WithoutSubmissionTracking(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, submissionKey{}, (*submissionState)(nil))
}

// OutcomeUnknownError means the provider may have executed the request. Its
// transport cause is available to operators, never included in the public body.
type OutcomeUnknownError struct{ Cause error }

func (*OutcomeUnknownError) Error() string {
	doc := ojson.NewDoc([]byte(`{}`))
	_ = doc.Set("error.message", "Upstream disconnected after submission. The request was not replayed and its outcome is unknown.")
	_ = doc.Set("error.type", "server_error")
	_ = doc.Set("error.code", "upstream_outcome_unknown")
	_ = doc.Set("error.retryable", false)
	return doc.String()
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

// ClassifySubmissionError treats every failure after acceptance as ambiguous
// unless the executor positively parsed a provider error payload.
func ClassifySubmissionError(ctx context.Context, err error) error {
	return classifySubmissionError(ctx, err, false)
}

// ClassifyIncompleteResponse marks a protocol failure that left an accepted
// request without a known outcome, such as a stream with no payload or terminal.
func ClassifyIncompleteResponse(ctx context.Context, err error) error {
	return classifySubmissionError(ctx, err, true)
}

func classifySubmissionError(ctx context.Context, err error, incomplete bool) error {
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
	var provider interface{ IsProviderErrorPayload() bool }
	if !incomplete && !state.redirected.Load() && errors.As(err, &provider) && provider.IsProviderErrorPayload() {
		return err
	}
	return state.fail(err)
}

// GenerationTransport guards a model send after credentials and payload preparation.
// Generic HTTP client construction must not track token or auxiliary requests.
func GenerationTransport(ctx context.Context, next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if ctx == nil {
		return next
	}
	state, _ := ctx.Value(submissionKey{}).(*submissionState)
	if state == nil {
		return next
	}
	return &submissionTransport{next: next, state: state}
}

type submissionTransport struct {
	next  http.RoundTripper
	state *submissionState
}

func (s *submissionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if terminal := s.state.terminal.Load(); terminal != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, terminal
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		if !s.state.accepted.Load() {
			return s.next.RoundTrip(req)
		}
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
	if cloned.Body == nil || cloned.Body == http.NoBody {
		// net/http treats canonical idempotency map keys as permission to replay an
		// empty mutation. HTTP field names are case-insensitive: recasing the cloned
		// keys preserves their wire values and Content-Length: 0 without that opt-in.
		for _, key := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
			if values, ok := cloned.Header[key]; ok {
				lower := strings.ToLower(key)
				cloned.Header[lower] = append(cloned.Header[lower], values...)
				delete(cloned.Header, key)
			}
		}
	}
	response, err := s.next.RoundTrip(cloned)
	if err != nil {
		if s.state.accepted.Load() || written.Load() || responseStarted.Load() {
			return nil, s.state.fail(err)
		}
		return response, err
	}
	if response != nil {
		switch response.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther:
			if req.Method != http.MethodGet && req.Method != http.MethodHead {
				s.state.redirected.Store(true)
				s.state.accepted.Store(true)
			}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			s.state.accepted.Store(true)
		}
		if s.state.accepted.Load() && response.Body != nil {
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
