package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestStudioCorrelationMiddlewareAcceptsAuthenticatedStudioHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	ctx.Request.Header.Set(coreusage.InferenceSessionHeader, "studio-session")
	ctx.Request.Header.Set(coreusage.TraceParentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx.Set("accessProvider", "studio")

	StudioCorrelationMiddleware()(ctx)
	value, exists := ctx.Get(studioInferenceSessionContextKey)
	if !exists || value != "studio-session" {
		t.Fatalf("stored Studio session = %v, want studio-session", value)
	}
	correlation := coreusage.CorrelationFromContext(ctx.Request.Context())
	if correlation.InferenceSessionID != "studio-session" || correlation.GatewayRequestID == "" || correlation.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("request context correlation = %+v", correlation)
	}
}

func TestStudioCorrelationMiddlewareRejectsMissingSessionForStudio(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	ctx.Set("accessProvider", "studio")

	StudioCorrelationMiddleware()(ctx)
	if !ctx.IsAborted() {
		t.Fatal("missing Studio session was not rejected")
	}
	if ctx.Writer.Status() != 400 {
		t.Fatalf("status = %d, want 400", ctx.Writer.Status())
	}
}

func TestStudioCorrelationMiddlewareAllowsStudioModelDiscoveryWithoutSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/models", "/v1beta/models"} {
		t.Run(path, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("GET", "http://example.com"+path, nil)
			ctx.Set("accessProvider", "studio")

			StudioCorrelationMiddleware()(ctx)
			if ctx.IsAborted() {
				t.Fatal("model discovery without a session was rejected")
			}
			correlation := coreusage.CorrelationFromContext(ctx.Request.Context())
			if correlation.InferenceSessionID != "" || correlation.GatewayRequestID == "" {
				t.Fatalf("model discovery correlation = %+v", correlation)
			}
		})
	}
}

func TestStudioCorrelationMiddlewareIgnoresUntrustedHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	requestContext := coreusage.WithInferenceSessionID(ctx.Request.Context(), "inherited-session")
	ctx.Request = ctx.Request.WithContext(requestContext)
	ctx.Request.Header.Set(coreusage.InferenceSessionHeader, "untrusted-session")
	ctx.Set(studioInferenceSessionContextKey, "stale-session")

	StudioCorrelationMiddleware()(ctx)
	if ctx.IsAborted() {
		t.Fatal("untrusted header was rejected instead of ignored")
	}
	if _, exists := ctx.Get(studioInferenceSessionContextKey); exists {
		t.Fatal("untrusted header or stale Studio state was promoted to correlation")
	}
	if sessionID := coreusage.InferenceSessionIDFromContext(ctx.Request.Context()); sessionID != "" {
		t.Fatalf("inherited session ID = %q, want empty", sessionID)
	}
}

func TestStudioCorrelationMiddlewareRejectsMalformedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	ctx.Request.Header.Set(coreusage.InferenceSessionHeader, "bad session")
	ctx.Set("accessProvider", "studio")

	StudioCorrelationMiddleware()(ctx)
	if !ctx.IsAborted() || ctx.Writer.Status() != 400 {
		t.Fatalf("malformed session status=%d aborted=%v", ctx.Writer.Status(), ctx.IsAborted())
	}
}

func TestStudioCorrelationMiddlewareIgnoresDuplicateHeaderForUntrustedCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	ctx.Request.Header.Add(coreusage.InferenceSessionHeader, "one")
	ctx.Request.Header.Add(coreusage.InferenceSessionHeader, "two")

	StudioCorrelationMiddleware()(ctx)
	if ctx.IsAborted() {
		t.Fatal("duplicate untrusted header was rejected")
	}
}

func TestStudioCorrelationMiddlewareRejectsDuplicateHeaderForStudio(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	ctx.Request.Header.Add(coreusage.InferenceSessionHeader, "one")
	ctx.Request.Header.Add(coreusage.InferenceSessionHeader, "two")
	ctx.Set("accessProvider", "studio")

	StudioCorrelationMiddleware()(ctx)
	if !ctx.IsAborted() || ctx.Writer.Status() != 400 {
		t.Fatalf("duplicate Studio header status=%d aborted=%v", ctx.Writer.Status(), ctx.IsAborted())
	}
}

func TestStudioCorrelationMiddlewareRecognizesStudioMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "http://example.com/v1/chat/completions", nil)
	ctx.Request.Header.Set(coreusage.InferenceSessionHeader, "studio-session")
	ctx.Set("accessMetadata", map[string]string{"integration": "studio"})

	StudioCorrelationMiddleware()(ctx)
	if value, exists := ctx.Get(studioInferenceSessionContextKey); !exists || value != "studio-session" {
		t.Fatalf("stored Studio session = %v, want studio-session", value)
	}
}
