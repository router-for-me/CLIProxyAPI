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
