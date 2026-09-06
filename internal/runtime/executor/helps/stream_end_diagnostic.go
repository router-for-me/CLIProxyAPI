package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/http2"
)

func streamEndKind(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "downstream_cancelled"
	}
	if err == nil {
		return "clean_eof_before_terminal"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) {
		return "http2_" + streamErr.Code.String()
	}
	return "read_error"
}

// LogProviderStreamFailure distinguishes a provider's explicit error event from
// a truncated HTTP body. A CONNECT proxy cannot fabricate TLS-protected events.
func LogProviderStreamFailure(ctx context.Context, response *http.Response, body []byte, startedAt time.Time) {
	LogWithRequestID(ctx).WithFields(logrus.Fields{
		"stream_end":            "upstream_error_event",
		"upstream_status":       response.StatusCode,
		"upstream_http_version": response.Proto,
		"upstream_request_id":   response.Header.Get("X-Request-ID"),
		"provider_error_code":   gjson.GetBytes(body, "error.code").String(),
		"provider_error_type":   gjson.GetBytes(body, "error.type").String(),
		"duration_ms":           time.Since(startedAt).Milliseconds(),
	}).Warn("codex provider sent a terminal error event")
}

// LogIncompleteResponseStream records transport evidence without response text,
// URLs, authorization headers or credentials. HTTP 200 alone is not success.
func LogIncompleteResponseStream(ctx context.Context, response *http.Response, err error, startedAt time.Time) {
	LogWithRequestID(ctx).WithFields(logrus.Fields{
		"stream_end":            streamEndKind(ctx, err),
		"upstream_status":       response.StatusCode,
		"upstream_http_version": response.Proto,
		"upstream_request_id":   response.Header.Get("X-Request-ID"),
		"duration_ms":           time.Since(startedAt).Milliseconds(),
	}).Warn("codex response stream ended without a terminal event")
}
