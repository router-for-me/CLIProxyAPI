package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRecordAPIResponseAggregatesIncrementally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{RequestLog: true}}

	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:    "https://example.com/v1/test",
		Method: http.MethodPost,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: []byte(`{"hello":"world"}`),
	})
	recordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{
		"Content-Type": []string{"application/json"},
	})
	appendAPIResponseChunk(ctx, cfg, []byte(`{"first":1}`))
	appendAPIResponseChunk(ctx, cfg, []byte(`{"second":2}`))

	rawValue, exists := ginCtx.Get(apiResponseKey)
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
