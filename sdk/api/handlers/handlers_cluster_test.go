package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestRequestExecutionMetadata_PropagatesClusterAndRequestDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest("POST", "/v1/completions?alt=sse&foo=bar", nil)
	req.Header.Set("Idempotency-Key", "idem-1")
	req.Header.Set(cluster.HeaderHop, "1")
	req.Header.Set(cluster.HeaderForwardedBy, "node-b")
	req.Header.Set("X-Custom", "value")
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = WithRequestBodyOverride(ctx, []byte(`{"prompt":"hi"}`))

	meta := requestExecutionMetadata(ctx)

	if got := meta[coreexecutor.RequestPathMetadataKey]; got != "/v1/completions" {
		t.Fatalf("request path = %v", got)
	}
	if got := meta[coreexecutor.RequestMethodMetadataKey]; got != "POST" {
		t.Fatalf("request method = %v", got)
	}
	if got := meta[coreexecutor.RequestRawQueryMetadataKey]; got != "alt=sse&foo=bar" {
		t.Fatalf("request raw query = %v", got)
	}
	if got := meta[coreexecutor.ClusterForwardedMetadataKey]; got != true {
		t.Fatalf("cluster forwarded flag = %v", got)
	}
	if got := meta[coreexecutor.ClusterLocalOnlyMetadataKey]; got != true {
		t.Fatalf("cluster local-only flag = %v", got)
	}
	if got := meta[coreexecutor.ClusterForwardedByMetadataKey]; got != "node-b" {
		t.Fatalf("cluster forwarded by = %v", got)
	}
	bodyOverride, ok := meta[coreexecutor.RequestBodyOverrideMetadataKey].([]byte)
	if !ok || string(bodyOverride) != `{"prompt":"hi"}` {
		t.Fatalf("request body override = %#v", meta[coreexecutor.RequestBodyOverrideMetadataKey])
	}
	headers, ok := meta[coreexecutor.RequestHeadersMetadataKey].(map[string][]string)
	if ok {
		if headers["X-Custom"][0] != "value" {
			t.Fatalf("request headers = %#v", headers)
		}
		return
	}
	httpHeaders, ok := meta[coreexecutor.RequestHeadersMetadataKey].(interface{ Get(string) string })
	if !ok || httpHeaders.Get("X-Custom") != "value" {
		t.Fatalf("request headers = %#v", meta[coreexecutor.RequestHeadersMetadataKey])
	}
}
