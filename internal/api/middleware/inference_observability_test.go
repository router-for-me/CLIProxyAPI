package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestInferenceObservabilityCapturesPreExecutorFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name          string
		path          string
		body          string
		status        int
		wantModel     string
		wantOperation string
	}{
		{
			name:          "auth unavailable",
			path:          "/v1/messages",
			body:          `{"model":"claude-fable-5","messages":[],"api_key":"must-not-be-logged"}`,
			status:        http.StatusServiceUnavailable,
			wantModel:     "claude-fable-5",
			wantOperation: "inference",
		},
		{
			name:          "validation failure",
			path:          "/v1/responses",
			body:          `{"model":"gpt-5.6-sol","input":null,"secret":"must-not-be-logged"}`,
			status:        http.StatusBadRequest,
			wantModel:     "gpt-5.6-sol",
			wantOperation: "inference",
		},
		{
			name:          "gemini route model",
			path:          "/v1beta/models/gemini-2.5-pro:countTokens",
			body:          `{"contents":[]}`,
			status:        http.StatusBadRequest,
			wantModel:     "gemini-2.5-pro",
			wantOperation: "count_tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var records []usage.Record
			engine := gin.New()
			engine.Use(inferenceObservabilityMiddleware(func(_ context.Context, record usage.Record) {
				records = append(records, record)
			}))
			engine.POST(tt.path, func(c *gin.Context) {
				body, errRead := io.ReadAll(c.Request.Body)
				if errRead != nil {
					t.Fatalf("read restored request body: %v", errRead)
				}
				if string(body) != tt.body {
					t.Fatalf("restored request body = %q, want %q", string(body), tt.body)
				}
				c.AbortWithStatusJSON(tt.status, gin.H{"error": "response-secret-must-not-be-logged"})
			})

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			engine.ServeHTTP(rr, req)

			if len(records) != 1 {
				t.Fatalf("synthetic records = %d, want 1", len(records))
			}
			record := records[0]
			if record.Provider != "proxy" || !record.Failed || record.Fail.StatusCode != tt.status {
				t.Fatalf("synthetic record = %+v", record)
			}
			if record.Model != tt.wantModel || record.Alias != tt.wantModel {
				t.Fatalf("model/alias = %q/%q, want %q", record.Model, record.Alias, tt.wantModel)
			}
			if record.Operation != tt.wantOperation {
				t.Fatalf("operation = %q, want %q", record.Operation, tt.wantOperation)
			}
			if strings.Contains(record.Fail.Body, "must-not-be-logged") {
				t.Fatalf("failure metadata leaked request or response content: %q", record.Fail.Body)
			}
			if record.RequestedAt.IsZero() || record.Latency < 0 {
				t.Fatalf("invalid timing metadata: requested=%v latency=%v", record.RequestedAt, record.Latency)
			}
		})
	}
}

func TestInferenceObservabilityDoesNotDuplicateExecutorFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var synthetic []usage.Record
	engine := gin.New()
	engine.Use(inferenceObservabilityMiddleware(func(_ context.Context, record usage.Record) {
		synthetic = append(synthetic, record)
	}))
	engine.POST("/v1/messages", func(c *gin.Context) {
		usage.PublishRecord(c.Request.Context(), usage.Record{
			Provider: "claude",
			Model:    "claude-fable-5",
			Failed:   true,
			Fail:     usage.Failure{StatusCode: http.StatusServiceUnavailable},
		})
		c.Status(http.StatusServiceUnavailable)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-fable-5"}`))
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if len(synthetic) != 0 {
		t.Fatalf("synthetic duplicate records = %d, want 0", len(synthetic))
	}
}

func TestInferenceObservabilitySkipsNonInferenceRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var records []usage.Record
	engine := gin.New()
	engine.Use(inferenceObservabilityMiddleware(func(_ context.Context, record usage.Record) {
		records = append(records, record)
	}))
	engine.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })
	engine.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusServiceUnavailable) })

	for _, path := range []string{"/healthz", "/v1/models"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
	}
	if len(records) != 0 {
		t.Fatalf("non-inference records = %d, want 0", len(records))
	}
}

func TestPeekAndRestoreRequestBodyUsesBoundedProbe(t *testing.T) {
	body := append([]byte(`{"model":"gpt-5.6-terra","input":"`), bytes.Repeat([]byte("x"), maxInferenceModelProbeBytes+1024)...)
	body = append(body, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	probe := peekAndRestoreRequestBody(req, maxInferenceModelProbeBytes)
	if len(probe) != maxInferenceModelProbeBytes {
		t.Fatalf("probe length = %d, want %d", len(probe), maxInferenceModelProbeBytes)
	}
	if got := inferenceRequestModel(probe, "", ""); got != "gpt-5.6-terra" {
		t.Fatalf("model from bounded probe = %q", got)
	}
	restored, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		t.Fatalf("read restored body: %v", errRead)
	}
	if !bytes.Equal(restored, body) {
		t.Fatal("bounded model probe changed the request body")
	}
}

func TestClassifyInferenceRoute(t *testing.T) {
	tests := []struct {
		method    string
		path      string
		wantOp    string
		wantModel string
		wantOK    bool
	}{
		{http.MethodPost, "/v1/chat/completions", "inference", "", true},
		{http.MethodPost, "/v1/messages/count_tokens", "count_tokens", "", true},
		{http.MethodPost, "/v1/responses/compact", "compaction", "", true},
		{http.MethodPost, "/backend-api/codex/responses", "inference", "", true},
		{http.MethodGet, "/backend-api/codex/responses", "inference", "", true},
		{http.MethodPost, "/v1beta/interactions", "inference", "", true},
		{http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", "inference", "gemini-2.5-flash", true},
		{http.MethodPost, "/v1beta/models/gemini-2.5-flash:countTokens", "count_tokens", "gemini-2.5-flash", true},
		{http.MethodGet, "/v1/models", "", "", false},
		{http.MethodPost, "/v1/images/generations", "inference", "", true},
		{http.MethodPost, "/v1beta/models/gemini-2.5-flash:unknown", "", "", false},
	}
	for _, tt := range tests {
		op, model, ok := classifyInferenceRoute(tt.method, tt.path)
		if op != tt.wantOp || model != tt.wantModel || ok != tt.wantOK {
			t.Errorf("classifyInferenceRoute(%q, %q) = %q, %q, %v; want %q, %q, %v", tt.method, tt.path, op, model, ok, tt.wantOp, tt.wantModel, tt.wantOK)
		}
	}
}
