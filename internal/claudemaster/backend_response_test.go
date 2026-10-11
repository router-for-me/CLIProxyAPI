package claudemaster

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type backendMarkedDirectError struct {
	status  int
	headers http.Header
	body    []byte
}

func (e *backendMarkedDirectError) Error() string                { return "direct upstream response" }
func (e *backendMarkedDirectError) StatusCode() int              { return e.status }
func (e *backendMarkedDirectError) DirectResponse() bool         { return true }
func (e *backendMarkedDirectError) ResponseHeaders() http.Header { return e.headers.Clone() }
func (e *backendMarkedDirectError) ResponseBody() []byte         { return append([]byte(nil), e.body...) }

func TestWriteBackendUpstreamErrorPreservesExplicitDirectResponse(t *testing.T) {
	body := gzipBackendTestBody(t, []byte("first line\r\nsecond line\n"))
	w := httptest.NewRecorder()
	writeBackendNativeUpstreamError(context.Background(), w, &interfaces.ErrorMessage{
		StatusCode:     http.StatusUnprocessableEntity,
		DirectResponse: true,
		Body:           body,
		Headers: http.Header{
			"Content-Type":              {"application/problem+json"},
			"X-Anthropic-Future-Header": {"one", "two"},
			"Set-Cookie":                {"cookie-a", "cookie-b"},
			"Authorization":             {"upstream-response-value"},
			"X-Litellm-Future":          {"preserved"},
			"Connection":                {"X-Hop"},
			"X-Hop":                     {"drop"},
			"Content-Encoding":          {"gzip"},
			"Content-Length":            {fmt.Sprint(len(body))},
		},
	})

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
	if got := w.Body.Bytes(); !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want exact %q", got, body)
	}
	if got := w.Header().Values("X-Anthropic-Future-Header"); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("future header = %v", got)
	}
	if got := w.Header().Values("Set-Cookie"); !reflect.DeepEqual(got, []string{"cookie-a", "cookie-b"}) {
		t.Fatalf("Set-Cookie = %v", got)
	}
	if w.Header().Get("Authorization") != "upstream-response-value" || w.Header().Get("X-Litellm-Future") != "preserved" {
		t.Fatalf("end-to-end headers changed: %v", w.Header())
	}
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Length") != fmt.Sprint(len(body)) {
		t.Fatalf("representation headers changed: %v", w.Header())
	}
	for _, key := range []string{"Connection", "X-Hop"} {
		if got := w.Header().Values(key); len(got) != 0 {
			t.Errorf("stale or hop-by-hop header %s = %v", key, got)
		}
	}
}

func TestWriteBackendNativeResultPreservesRawRepresentation(t *testing.T) {
	body := gzipBackendTestBody(t, []byte(`{"type":"message","content":[]}`))
	w := httptest.NewRecorder()
	writeBackendNativeHeaders(w.Header(), http.Header{
		"Content-Type":     {"application/json"},
		"Content-Encoding": {"gzip"},
		"Content-Length":   {fmt.Sprint(len(body))},
	})
	writeBackendNativeResult(context.Background(), w, body, nil)

	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("native response body changed: got %x want %x", w.Body.Bytes(), body)
	}
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Length") != fmt.Sprint(len(body)) {
		t.Fatalf("native representation headers changed: %v", w.Header())
	}
}

func gzipBackendTestBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestWriteBackendUpstreamErrorFindsWrappedRequestTermination(t *testing.T) {
	body := []byte(`{"type":"error","future_field":true}`)
	direct := &coreexecutor.RequestTerminatedError{
		HTTPStatus: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type":                 {"application/json"},
			"Anthropic-Ratelimit-Requests": {"future-value"},
		},
		Body: body,
	}
	w := httptest.NewRecorder()
	writeBackendNativeUpstreamError(context.Background(), w, &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      fmt.Errorf("wrapped: %w", direct),
	})

	if w.Code != http.StatusTooManyRequests || string(w.Body.Bytes()) != string(body) {
		t.Fatalf("direct response changed: status=%d body=%q", w.Code, w.Body.Bytes())
	}
	if got := w.Header().Get("Anthropic-Ratelimit-Requests"); got != "future-value" {
		t.Fatalf("rate-limit header = %q", got)
	}
}

func TestWriteBackendUpstreamErrorFindsMarkedDirectResponse(t *testing.T) {
	body := []byte("opaque upstream body")
	direct := &backendMarkedDirectError{
		status: http.StatusForbidden,
		headers: http.Header{
			"Content-Type": {"text/plain"},
			"X-Future":     {"preserved"},
		},
		body: body,
	}
	w := httptest.NewRecorder()
	writeBackendNativeUpstreamError(context.Background(), w, &interfaces.ErrorMessage{
		StatusCode: http.StatusInternalServerError,
		Error:      errors.Join(errors.New("outer"), direct),
	})

	if w.Code != http.StatusForbidden || string(w.Body.Bytes()) != string(body) || w.Header().Get("X-Future") != "preserved" {
		t.Fatalf("marked direct response changed: status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.Bytes())
	}
}
