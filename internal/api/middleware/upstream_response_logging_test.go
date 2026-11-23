package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
)

func TestLocalErrorsAreSeparateFromUpstreamResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"memory", "file", "deferred"} {
		for _, streaming := range []bool{false, true} {
			for _, dialFailure := range []bool{false, true} {
				name := mode
				if streaming {
					name += "/streaming"
				}
				if dialFailure {
					name += "/dial_failure"
				}
				t.Run(name, func(t *testing.T) {
					logsDir := t.TempDir()
					fullLog := mode != "deferred"
					logger := logging.NewFileRequestLogger(fullLog, logsDir, "", 10)
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					if mode == "file" {
						source, errSource := logging.NewFileBodySourceInDir(t.TempDir(), "api-response")
						if errSource != nil {
							t.Fatal(errSource)
						}
						c.Set(logging.APIResponseSourceContextKey, source)
						t.Cleanup(func() {
							if errCleanup := source.Cleanup(); errCleanup != nil {
								t.Error(errCleanup)
							}
						})
					}
					wrapper := &ResponseWriterWrapper{
						ResponseWriter: c.Writer,
						logger:         logger,
						logOnErrorOnly: !fullLog,
						ginContext:     c,
						body:           &bytes.Buffer{},
						requestInfo:    &RequestInfo{URL: "/v1/chat/completions", Method: http.MethodPost, Timestamp: time.Now()},
					}
					ctx := context.WithValue(context.Background(), "gin", c)
					cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: fullLog}}
					helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://upstream.example/generate", Method: http.MethodPost})
					first := `data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"signature","text":""}]}}]}}`
					last := `data: {"response":{"usageMetadata":{"promptTokenCount":42,"totalTokenCount":42}}}`
					if !dialFailure {
						helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}})
						helps.AppendAPIResponseChunk(ctx, cfg, []byte(first))
					}
					localErr := errors.New("upstream stream closed with zero output")
					helps.RecordAPIResponseError(ctx, cfg, localErr)
					if !dialFailure {
						// Local failures must not affect subsequent upstream capture.
						helps.AppendAPIResponseChunk(ctx, cfg, []byte(last))
					}
					c.Writer = wrapper
					if streaming {
						wrapper.Header().Set("Content-Type", "text/event-stream")
						wrapper.WriteHeader(http.StatusOK)
					}
					handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil).WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("Failed to read upstream response")})
					if strings.Contains(recorder.Body.String(), localErr.Error()) {
						t.Fatalf("local diagnostics leaked into client response: %s", recorder.Body.String())
					}
					if errFinalize := wrapper.Finalize(c); errFinalize != nil {
						t.Fatal(errFinalize)
					}
					paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*.log"))
					if errGlob != nil || len(paths) != 1 {
						t.Fatalf("log files = %v, error = %v", paths, errGlob)
					}
					data, errRead := os.ReadFile(paths[0])
					if errRead != nil {
						t.Fatal(errRead)
					}
					text := string(data)
					if strings.Contains(text, "=== CPA ERROR") {
						t.Fatalf("unexpected redundant CPA error section:\n%s", text)
					}
					downstreamStart := strings.Index(text, "=== RESPONSE ===")
					if downstreamStart < 0 || !strings.Contains(text[downstreamStart:], "Error: "+localErr.Error()+"\n\nHeaders:\n") {
						t.Fatalf("downstream response log lost the local failure:\n%s", text)
					}
					if strings.Contains(text[:downstreamStart], localErr.Error()) {
						t.Fatalf("local error leaked outside downstream response section:\n%s", text)
					}
					if dialFailure {
						if strings.Contains(text, "=== API RESPONSE") {
							t.Fatalf("dial failure fabricated an upstream response:\n%s", text)
						}
						return
					}
					responseStart := strings.Index(text, "=== API RESPONSE 1 ===")
					if responseStart < 0 {
						t.Fatalf("missing upstream response:\n%s", text)
					}
					response := strings.SplitN(text[responseStart:], "\n===", 2)[0]
					if !strings.Contains(response, "Status: 200") || !strings.Contains(response, "Body:\n"+first+"\n\n"+last) || strings.Contains(response, localErr.Error()) || strings.Contains(response, "Error:") {
						t.Fatalf("upstream response was polluted:\n%s", response)
					}
				})
			}
		}
	}
}

func TestLocalDiagnosticsDoNotChangeRecoveredRequestStatus(t *testing.T) {
	for _, fullLog := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			name := "deferred"
			if fullLog {
				name = "full"
			}
			if streaming {
				name += "/streaming"
			}
			t.Run(name, func(t *testing.T) {
				logsDir := t.TempDir()
				cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: fullLog}}
				router := gin.New()
				router.Use(RequestLoggingMiddleware(logging.NewFileRequestLogger(fullLog, logsDir, "", 10)))
				router.POST("/v1/responses", func(c *gin.Context) {
					ctx := context.WithValue(c.Request.Context(), "gin", c)
					helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://upstream.example/first"})
					helps.RecordAPIResponseError(ctx, cfg, errors.New("first attempt disconnected"))
					helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://upstream.example/retry"})
					helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
					helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"success":true}`))
					if streaming {
						c.Data(http.StatusOK, "text/event-stream", []byte("data: [DONE]\n\n"))
					} else {
						c.Data(http.StatusOK, "application/json", []byte(`{"success":true}`))
					}
				})
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
				if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "disconnected") {
					t.Fatalf("diagnostics changed client response: %d %s", recorder.Code, recorder.Body.String())
				}
				paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*.log"))
				if errGlob != nil {
					t.Fatal(errGlob)
				}
				if !fullLog {
					if len(paths) != 0 {
						t.Fatalf("recovered request generated error logs: %v", paths)
					}
					return
				}
				if len(paths) != 1 || strings.HasPrefix(filepath.Base(paths[0]), "error-") {
					t.Fatalf("recovered request misclassified: %v", paths)
				}
				data, errRead := os.ReadFile(paths[0])
				if errRead != nil {
					t.Fatal(errRead)
				}
				_, response, found := strings.Cut(string(data), "=== RESPONSE ===\n")
				if !found || !strings.Contains(response, "Status: 200\nError: first attempt disconnected\n\nHeaders:\n") || !strings.Contains(response, "\nBody:\n") {
					t.Fatalf("recovered request log lost status or diagnostics:\n%s", data)
				}
			})
		}
	}
}

func TestHandlerFallbackDoesNotPolluteExecutorCapture(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	baseHandler := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil)
	claudeHandler := claude.NewClaudeCodeAPIHandler(baseHandler)
	for name, writeError := range map[string]func(*gin.Context, *interfaces.ErrorMessage){
		"base": baseHandler.WriteErrorResponse, "claude": claudeHandler.WriteErrorResponse,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx := context.WithValue(context.Background(), "gin", c)
			helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
			helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"upstream":"original"}`))
			previous, _ := c.Get("API_RESPONSE")
			writeError(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("local validation failure")})
			after, _ := c.Get("API_RESPONSE")
			if !bytes.Equal(previous.([]byte), after.([]byte)) {
				t.Fatalf("handler modified upstream capture: %s", after)
			}
		})
	}
}

type memoryCaptureLogRecord struct {
	apiResponse []byte
	errors      []*interfaces.ErrorMessage
}

type memoryCaptureStreamingWriter struct {
	mu          sync.Mutex
	apiResponse bytes.Buffer
	errors      []*interfaces.ErrorMessage
	onClose     func(memoryCaptureLogRecord)
}

func (*memoryCaptureStreamingWriter) WriteChunkAsync([]byte)                     {}
func (*memoryCaptureStreamingWriter) SetFirstChunkTimestamp(time.Time)           {}
func (*memoryCaptureStreamingWriter) WriteStatus(int, map[string][]string) error { return nil }
func (*memoryCaptureStreamingWriter) WriteAPIRequest([]byte) error               { return nil }
func (*memoryCaptureStreamingWriter) WriteAPIWebsocketTimeline([]byte) error     { return nil }
func (w *memoryCaptureStreamingWriter) WriteAPIResponse(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.apiResponse.Write(data)
	return nil
}
func (w *memoryCaptureStreamingWriter) WriteAPIResponseErrors(errs []*interfaces.ErrorMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.errors = append(w.errors, errs...)
	return nil
}
func (w *memoryCaptureStreamingWriter) Close() error {
	w.mu.Lock()
	record := memoryCaptureLogRecord{
		apiResponse: bytes.Clone(w.apiResponse.Bytes()),
		errors:      append([]*interfaces.ErrorMessage(nil), w.errors...),
	}
	w.mu.Unlock()
	if w.onClose != nil {
		w.onClose(record)
	}
	return nil
}

type memoryCaptureRequestLogger struct {
	mu      sync.Mutex
	records []memoryCaptureLogRecord
}

func (*memoryCaptureRequestLogger) IsEnabled() bool { return true }
func (l *memoryCaptureRequestLogger) record(rec memoryCaptureLogRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, rec)
}
func (l *memoryCaptureRequestLogger) LogRequest(_, _ string, _ map[string][]string, _ []byte, _ int, _ map[string][]string, _, _, _, apiResponse, _ []byte, apiResponseErrors []*interfaces.ErrorMessage, _ string, _, _ time.Time) error {
	l.record(memoryCaptureLogRecord{
		apiResponse: bytes.Clone(apiResponse),
		errors:      append([]*interfaces.ErrorMessage(nil), apiResponseErrors...),
	})
	return nil
}
func (l *memoryCaptureRequestLogger) LogRequestWithOptions(_, _ string, _ map[string][]string, _ []byte, _ int, _ map[string][]string, _, _, _, apiResponse, _ []byte, apiResponseErrors []*interfaces.ErrorMessage, _ bool, _ string, _, _ time.Time) error {
	l.record(memoryCaptureLogRecord{
		apiResponse: bytes.Clone(apiResponse),
		errors:      append([]*interfaces.ErrorMessage(nil), apiResponseErrors...),
	})
	return nil
}
func (l *memoryCaptureRequestLogger) LogStreamingRequest(_, _ string, _ map[string][]string, _ []byte, _ string) (logging.StreamingLogWriter, error) {
	return &memoryCaptureStreamingWriter{onClose: l.record}, nil
}

func TestMemoryStreamSnapshotterPublishedByMiddlewareAndIsolatedAcrossGinReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	t.Run("concurrent_mid_callback_gin_reuse", func(t *testing.T) {
		logger := &memoryCaptureRequestLogger{}
		var wg sync.WaitGroup
		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), "gin", c)
			reqTag := c.GetHeader("X-Test-Req")
			helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
				URL:    "https://api.individual.githubcopilot.com/chat/completions",
				Method: http.MethodPost,
				Body:   []byte(reqTag),
			})
			helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": []string{"text/event-stream"}})
			streamLogger := helps.BindAPIResponseStreamLogger(ctx, cfg)
			streamLogger.AppendChunk([]byte("data: " + reqTag + "-initial"))
			if reqTag == "req1" {
				// Keep writing concurrently from an orphaned goroutine across Finalize and Gin pool reuse.
				started := make(chan struct{})
				wg.Add(1)
				go func() {
					defer wg.Done()
					close(started)
					for range 200 {
						streamLogger.AppendChunk([]byte("data: STALE_REQ1_CONCURRENT"))
					}
					streamLogger.RecordError(errors.New("stale req1 concurrent error"))
				}()
				<-started
			}
			c.Data(http.StatusOK, "text/event-stream", []byte("data: "+reqTag+"\n\n"))
		})

		req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req1.Header.Set("X-Test-Req", "req1")
		router.ServeHTTP(httptest.NewRecorder(), req1)

		req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req2.Header.Set("X-Test-Req", "req2")
		router.ServeHTTP(httptest.NewRecorder(), req2)
		wg.Wait()

		logger.mu.Lock()
		defer logger.mu.Unlock()
		if len(logger.records) != 2 {
			t.Fatalf("logged records = %d, want 2", len(logger.records))
		}
		if !strings.Contains(string(logger.records[0].apiResponse), "data: req1-initial") {
			t.Fatalf("req1 API response missing initial stream chunk:\n%s", string(logger.records[0].apiResponse))
		}
		req2Resp := string(logger.records[1].apiResponse)
		if !strings.Contains(req2Resp, "data: req2-initial") {
			t.Fatalf("req2 API response missing req2 chunk:\n%s", req2Resp)
		}
		if strings.Contains(req2Resp, "STALE_REQ1_CONCURRENT") {
			t.Fatalf("req2 API response polluted by req1 concurrent callback:\n%s", req2Resp)
		}
		if len(logger.records[1].errors) != 0 {
			t.Fatalf("req2 errors polluted by req1 concurrent callback: %#v", logger.records[1].errors)
		}
	})

	t.Run("retry_after_bound_stream_preserves_both_attempts", func(t *testing.T) {
		logger := &memoryCaptureRequestLogger{}
		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/responses", func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), "gin", c)
			helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://api.individual.githubcopilot.com/attempt1", Method: http.MethodPost})
			helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
			streamLogger := helps.BindAPIResponseStreamLogger(ctx, cfg)
			streamLogger.AppendChunk([]byte(`data: {"type":"error","attempt":1}`))
			streamLogger.RecordError(errors.New("attempt 1 failed before payload"))

			helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://api.individual.githubcopilot.com/attempt2", Method: http.MethodPost})
			helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
			helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"attempt":2,"ok":true}`))
			c.Data(http.StatusOK, "application/json", []byte(`{"ok":true}`))
		})

		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

		logger.mu.Lock()
		defer logger.mu.Unlock()
		if len(logger.records) != 1 {
			t.Fatalf("logged records = %d, want 1", len(logger.records))
		}
		got := string(logger.records[0].apiResponse)
		for _, want := range []string{"=== API RESPONSE 1 ===", `"attempt":1`, "=== API RESPONSE 2 ===", `"attempt":2`} {
			if !strings.Contains(got, want) {
				t.Fatalf("retry API response missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("file_backed_retry_while_attempt1_stream_draining", func(t *testing.T) {
		logsDir := t.TempDir()
		fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 10)
		var wg sync.WaitGroup
		router := gin.New()
		router.Use(RequestLoggingMiddleware(fileLogger))
		router.POST("/v1/responses", func(c *gin.Context) {
			ctx := context.WithValue(c.Request.Context(), "gin", c)
			helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://api.individual.githubcopilot.com/attempt1", Method: http.MethodPost})
			helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
			streamLogger := helps.BindAPIResponseStreamLogger(ctx, cfg)
			started := make(chan struct{})
			wg.Add(1)
			go func() {
				defer wg.Done()
				close(started)
				for range 50 {
					streamLogger.AppendChunk([]byte(`data: {"attempt":1}`))
				}
			}()
			<-started

			helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://api.individual.githubcopilot.com/attempt2", Method: http.MethodPost})
			helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, nil)
			helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"attempt":2,"ok":true}`))
			c.Data(http.StatusOK, "application/json", []byte(`{"ok":true}`))
		})

		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		wg.Wait()
	})
}
