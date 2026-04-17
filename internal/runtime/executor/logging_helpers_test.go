package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRecordAPIResponseAggregatesIncrementally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{RequestLog: true}}

	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:    "https://example.com/v1/test",
		Method: http.MethodPost,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: []byte(`{"hello":"world"}`),
	})
	helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{
		"Content-Type": []string{"application/json"},
	})
	helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"first":1}`))
	helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"second":2}`))

	rawValue, exists := ginCtx.Get("API_RESPONSE")
	if !exists {
		t.Fatal("expected aggregated api response to be stored")
	}
	builder, ok := rawValue.(*strings.Builder)
	if !ok {
		t.Fatalf("api response type = %T, want *strings.Builder", rawValue)
	}

	output := builder.String()
	if strings.Count(output, "=== API RESPONSE 1 ===") != 1 {
		t.Fatalf("response header count = %d, want 1", strings.Count(output, "=== API RESPONSE 1 ==="))
	}
	for _, fragment := range []string{
		"Status: 200",
		"Content-Type: application/json",
		`{"first":1}`,
		`{"second":2}`,
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("aggregated response missing %q in %q", fragment, output)
		}
	}
}

func TestRecordAPIHelpersReadHeadersImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{RequestLog: true}}

	requestHeaders := http.Header{
		"X-Request-Test": []string{"before"},
	}
	responseHeaders := http.Header{
		"X-Response-Test": []string{"before"},
	}

	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:     "https://example.com/v1/test",
		Method:  http.MethodPost,
		Headers: requestHeaders,
	})
	helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusCreated, responseHeaders)

	requestHeaders.Set("X-Request-Test", "after")
	responseHeaders.Set("X-Response-Test", "after")

	rawRequest, exists := ginCtx.Get("API_REQUEST")
	if !exists {
		t.Fatal("expected aggregated api request to be stored")
	}
	requestBytes, ok := rawRequest.([]byte)
	if !ok {
		t.Fatalf("api request type = %T, want []byte", rawRequest)
	}
	requestText := string(requestBytes)
	if !strings.Contains(requestText, "X-Request-Test: before") {
		t.Fatalf("request log missing original header value in %q", requestText)
	}
	if strings.Contains(requestText, "X-Request-Test: after") {
		t.Fatalf("request log retained mutated header value in %q", requestText)
	}

	rawResponse, exists := ginCtx.Get("API_RESPONSE")
	if !exists {
		t.Fatal("expected aggregated api response to be stored")
	}
	responseBuilder, ok := rawResponse.(*strings.Builder)
	if !ok {
		t.Fatalf("api response type = %T, want *strings.Builder", rawResponse)
	}
	responseText := responseBuilder.String()
	if !strings.Contains(responseText, "X-Response-Test: before") {
		t.Fatalf("response log missing original header value in %q", responseText)
	}
	if strings.Contains(responseText, "X-Response-Test: after") {
		t.Fatalf("response log retained mutated header value in %q", responseText)
	}
}
