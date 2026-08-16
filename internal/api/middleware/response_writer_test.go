package middleware

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestExtractRequestBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{
		requestInfo: &RequestInfo{Body: []byte("original-body")},
	}

	body := wrapper.extractRequestBody(c)
	if string(body) != "original-body" {
		t.Fatalf("request body = %q, want %q", string(body), "original-body")
	}

	c.Set(requestBodyOverrideContextKey, []byte("override-body"))
	body = wrapper.extractRequestBody(c)
	if string(body) != "override-body" {
		t.Fatalf("request body = %q, want %q", string(body), "override-body")
	}
}

func TestExtractRequestBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	c.Set(requestBodyOverrideContextKey, "override-as-string")

	body := wrapper.extractRequestBody(c)
	if string(body) != "override-as-string" {
		t.Fatalf("request body = %q, want %q", string(body), "override-as-string")
	}
}

func TestExtractResponseBodyPrefersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{body: &bytes.Buffer{}}
	wrapper.body.WriteString("original-response")

	body := wrapper.extractResponseBody(c)
	if string(body) != "original-response" {
		t.Fatalf("response body = %q, want %q", string(body), "original-response")
	}

	c.Set(responseBodyOverrideContextKey, []byte("override-response"))
	body = wrapper.extractResponseBody(c)
	if string(body) != "override-response" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response")
	}

	body[0] = 'X'
	if got := wrapper.extractResponseBody(c); string(got) != "override-response" {
		t.Fatalf("response override should be cloned, got %q", string(got))
	}
}

func TestExtractResponseBodySupportsStringOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	c.Set(responseBodyOverrideContextKey, "override-response-as-string")

	body := wrapper.extractResponseBody(c)
	if string(body) != "override-response-as-string" {
		t.Fatalf("response body = %q, want %q", string(body), "override-response-as-string")
	}
}

func TestExtractBodyOverrideClonesBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	override := []byte("body-override")
	c.Set(requestBodyOverrideContextKey, override)

	body := extractBodyOverride(c, requestBodyOverrideContextKey)
	if !bytes.Equal(body, override) {
		t.Fatalf("body override = %q, want %q", string(body), string(override))
	}

	body[0] = 'X'
	if !bytes.Equal(override, []byte("body-override")) {
		t.Fatalf("override mutated: %q", string(override))
	}
}

func TestExtractWebsocketTimelineUsesOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	wrapper := &ResponseWriterWrapper{}
	if got := wrapper.extractWebsocketTimeline(c); got != nil {
		t.Fatalf("expected nil websocket timeline, got %q", string(got))
	}

	c.Set(websocketTimelineOverrideContextKey, []byte("timeline"))
	body := wrapper.extractWebsocketTimeline(c)
	if string(body) != "timeline" {
		t.Fatalf("websocket timeline = %q, want %q", string(body), "timeline")
	}
}

func TestFinalizeStreamingWritesAPIWebsocketTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	streamWriter := &testStreamingLogWriter{}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         &testRequestLogger{enabled: true},
		requestInfo: &RequestInfo{
			URL:       "/v1/responses",
			Method:    "POST",
			Headers:   map[string][]string{"Content-Type": {"application/json"}},
			RequestID: "req-1",
			Timestamp: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC),
		},
		isStreaming:  true,
		streamWriter: streamWriter,
	}

	c.Set("API_WEBSOCKET_TIMELINE", []byte("Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}"))

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if string(streamWriter.apiWebsocketTimeline) != "Timestamp: 2026-04-01T12:00:00Z\nEvent: api.websocket.request\n{}" {
		t.Fatalf("stream writer websocket timeline = %q", string(streamWriter.apiWebsocketTimeline))
	}
	if !streamWriter.closed {
		t.Fatal("expected stream writer to be closed")
	}
}

func TestResponseWriterFinalizeStreamingClosesWriterAfterSourceMergeError(t *testing.T) {
	closeFailure := errors.New("close streaming writer")
	tests := []struct {
		name     string
		closeErr error
	}{
		{name: "merge error"},
		{name: "merge and close errors", closeErr: closeFailure},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)

			source, errSource := logging.NewFileBodySourceInDir(t.TempDir(), "invalid-api-request")
			if errSource != nil {
				t.Fatalf("NewFileBodySourceInDir: %v", errSource)
			}
			part, errPart := source.CreatePart("part")
			if errPart != nil {
				t.Fatalf("CreatePart: %v", errPart)
			}
			partPath := part.Name()
			if errClose := part.Close(); errClose != nil {
				t.Fatalf("close part: %v", errClose)
			}
			if errRemove := os.Remove(partPath); errRemove != nil {
				t.Fatalf("remove part: %v", errRemove)
			}
			if errMkdir := os.Mkdir(partPath, 0o755); errMkdir != nil {
				t.Fatalf("replace part with directory: %v", errMkdir)
			}

			streamWriter := &testStreamingLogWriter{closeErr: test.closeErr}
			wrapper := &ResponseWriterWrapper{
				ResponseWriter: c.Writer,
				logger:         &testRequestLogger{enabled: true},
				requestInfo: &RequestInfo{
					URL:       "/v1/responses",
					Method:    "POST",
					RequestID: "req-merge-error",
					Timestamp: time.Now(),
				},
				isStreaming:  true,
				streamWriter: streamWriter,
			}
			c.Set(logging.APIRequestSourceContextKey, source)

			errFinalize := wrapper.Finalize(c)
			if errFinalize == nil {
				t.Fatal("Finalize error = nil, want source merge error")
			}
			var pathError *os.PathError
			if !errors.As(errFinalize, &pathError) {
				t.Fatalf("Finalize error = %v, want source path error", errFinalize)
			}
			if test.closeErr != nil && !errors.Is(errFinalize, test.closeErr) {
				t.Fatalf("Finalize error = %v, want joined close error %v", errFinalize, test.closeErr)
			}
			if streamWriter.closeCount != 1 {
				t.Fatalf("stream writer Close count = %d, want 1", streamWriter.closeCount)
			}
			if wrapper.streamWriter != nil {
				t.Fatal("stream writer state was not cleared")
			}
			if _, errStat := os.Stat(filepath.Dir(partPath)); !os.IsNotExist(errStat) {
				t.Fatalf("source directory still exists after Finalize: %v", errStat)
			}
		})
	}
}

type testRequestLogger struct {
	enabled bool
}

func (l *testRequestLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	return nil
}

func (l *testRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	return &testStreamingLogWriter{}, nil
}

func (l *testRequestLogger) IsEnabled() bool {
	return l.enabled
}

type testStreamingLogWriter struct {
	apiWebsocketTimeline []byte
	closed               bool
	closeCount           int
	closeErr             error
}

func (w *testStreamingLogWriter) WriteChunkAsync([]byte) {}

func (w *testStreamingLogWriter) WriteStatus(int, map[string][]string) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIRequest([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIResponse([]byte) error {
	return nil
}

func (w *testStreamingLogWriter) WriteAPIWebsocketTimeline(apiWebsocketTimeline []byte) error {
	w.apiWebsocketTimeline = bytes.Clone(apiWebsocketTimeline)
	return nil
}

func (w *testStreamingLogWriter) SetFirstChunkTimestamp(time.Time) {}

func (w *testStreamingLogWriter) Close() error {
	w.closed = true
	w.closeCount++
	return w.closeErr
}
