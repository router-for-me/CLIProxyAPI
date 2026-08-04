package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestWriteErrorResponse_AddonHeadersDisabledByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":  {"30"},
			"X-Request-Id": {"req-1"},
		},
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After should be empty when passthrough is disabled, got %q", got)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "" {
		t.Fatalf("X-Request-Id should be empty when passthrough is disabled, got %q", got)
	}
}

func TestWriteErrorResponseDirectResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.Header().Set("X-Cpa-Trace-Id", "local-trace")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "https://trusted.example")

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode:     http.StatusForbidden,
		DirectResponse: true,
		Body:           []byte(`{"error":"blocked"}`),
		Headers: http.Header{
			"Content-Type":                {"application/problem+json"},
			"X-Plugin-Policy":             {"blocked"},
			"X-Cpa-Trace-Id":              {"plugin-trace"},
			"Access-Control-Allow-Origin": {"https://untrusted.example"},
		},
	})

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if got := recorder.Body.String(); got != `{"error":"blocked"}` {
		t.Fatalf("body = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-Plugin-Policy"); got != "blocked" {
		t.Fatalf("X-Plugin-Policy = %q", got)
	}
	if got := recorder.Header().Get("X-Cpa-Trace-Id"); got != "local-trace" {
		t.Fatalf("X-Cpa-Trace-Id = %q, want local value", got)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "https://trusted.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want trusted origin", got)
	}
}

func TestInternalConcurrencyBusyWritesRetryAfterWithoutPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	handler := NewBaseAPIHandlers(nil, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewHomeConcurrencyBusyError("busy", 750*time.Millisecond),
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

func TestWriteErrorResponseHomeBusyNormalAndStreamHeaders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stream"}[stream], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if stream {
				c.Request.Header.Set("Accept", "text/event-stream")
			}

			handler := NewBaseAPIHandlers(nil, nil)
			handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      coreauth.NewHomeConcurrencyBusyError("busy", 750*time.Millisecond),
			})
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
			}
			if got := recorder.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		})
	}
}

func TestWriteErrorResponse_AddonHeadersEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Writer.Header().Set("X-Request-Id", "old-value")
	c.Writer.Header().Set("x-cpa-trace-id", "local-trace")
	c.Writer.Header().Set("Access-Control-Expose-Headers", "x-cpa-trace-id")

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil)
	handler.WriteErrorResponse(c, &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New("rate limit"),
		Addon: http.Header{
			"Retry-After":                   {"30"},
			"X-Request-Id":                  {"new-1", "new-2"},
			"x-cpa-trace-id":                {"upstream-trace"},
			"Access-Control-Expose-Headers": {"upstream-header"},
		},
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want %q", got, "30")
	}
	if got := recorder.Header().Values("X-Request-Id"); !reflect.DeepEqual(got, []string{"new-1", "new-2"}) {
		t.Fatalf("X-Request-Id = %#v, want %#v", got, []string{"new-1", "new-2"})
	}
	if got := recorder.Header().Get("x-cpa-trace-id"); got != "local-trace" {
		t.Fatalf("x-cpa-trace-id = %q, want local trace", got)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != "x-cpa-trace-id" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want CPA value", got)
	}
}

func TestEnrichAuthSelectionError_DefaultsTo503WithContext(t *testing.T) {
	in := &coreauth.Error{Code: "auth_not_found", Message: "no auth available"}
	out := enrichAuthSelectionError(nil, context.Background(), in, []string{"claude"}, "claude-sonnet-4-6")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusServiceUnavailable)
	}
	if !strings.Contains(got.Message, "providers=claude") {
		t.Fatalf("message missing provider context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "model=claude-sonnet-4-6") {
		t.Fatalf("message missing model context: %q", got.Message)
	}
	if !strings.Contains(got.Message, "/v0/management/auth-files") {
		t.Fatalf("message missing management hint: %q", got.Message)
	}
}

func TestEnrichAuthSelectionError_PreservesExplicitStatus(t *testing.T) {
	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available", HTTPStatus: http.StatusTooManyRequests}
	out := enrichAuthSelectionError(nil, context.Background(), in, []string{"gemini"}, "gemini-2.5-pro")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if got.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", got.StatusCode(), http.StatusTooManyRequests)
	}
}

func TestEnrichAuthSelectionError_IgnoresOtherErrors(t *testing.T) {
	in := errors.New("boom")
	out := enrichAuthSelectionError(nil, context.Background(), in, []string{"claude"}, "claude-sonnet-4-6")
	if out != in {
		t.Fatalf("expected original error to be returned unchanged")
	}
}

// TestEnrichAuthSelectionError_ShowsOfficialProviderName verifies that the
// providers list surfaced in the error message is rendered using the Official
// Provider name rather than the internal "openai-compatible-..." key.
func TestEnrichAuthSelectionError_ShowsOfficialProviderName(t *testing.T) {
	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"}
	out := enrichAuthSelectionError(nil, context.Background(), in, []string{"openai-compatible-opencode"}, "some-custom-model")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if strings.Contains(got.Message, "openai-compatible-opencode") {
		t.Fatalf("message should not expose the internal key, got %q", got.Message)
	}
	if !strings.Contains(got.Message, "providers=opencode") {
		t.Fatalf("message should show Official Provider name, got %q", got.Message)
	}
}

// TestEnrichAuthSelectionError_ResolvesOfficialNameFromRegistry verifies that
// a built-in provider key resolves to its Official Provider name (OwnedBy)
// taken from the model registry, rather than being emitted as the raw key.
func TestEnrichAuthSelectionError_ResolvesOfficialNameFromRegistry(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "auth-claude",
		Provider: "claude",
		Status:   coreauth.StatusActive,
	}
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider,
		[]*registry.ModelInfo{{ID: "claude-test-model", OwnedBy: "anthropic", Type: "claude"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	handler := NewBaseAPIHandlers(nil, manager)
	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"}
	out := enrichAuthSelectionError(handler, context.Background(), in, []string{"claude"}, "claude-test-model")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if strings.Contains(got.Message, "providers=claude") {
		t.Fatalf("message should not expose the raw key, got %q", got.Message)
	}
	if !strings.Contains(got.Message, "providers=anthropic") {
		t.Fatalf("message should show Official Provider name, got %q", got.Message)
	}
}

// TestEnrichAuthSelectionError_ResolvesCompatNameFromAuthManager verifies that
// an OpenAI-compatible provider key resolves to the official compat name
// carried on the auth record, independent of model registration.
func TestEnrichAuthSelectionError_ResolvesCompatNameFromAuthManager(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "auth-opencode",
		Provider: "openai-compatibility",
		Label:    "opencode",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"provider_key": "openai-compatible-opencode",
			"compat_name":  "opencode",
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	handler := NewBaseAPIHandlers(nil, manager)
	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"}
	out := enrichAuthSelectionError(handler, context.Background(), in, []string{"openai-compatible-opencode"}, "some-unregistered-model")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if strings.Contains(got.Message, "openai-compatible-opencode") {
		t.Fatalf("message should not expose the internal key, got %q", got.Message)
	}
	if !strings.Contains(got.Message, "providers=opencode") {
		t.Fatalf("message should show Official Provider name, got %q", got.Message)
	}
}

// fakeCatalogResolver is a test double for ModelsCatalogResolver returning a
// canned official_provider value regardless of input.
type fakeCatalogResolver struct {
	official  string
	calls     int
	lastKey   string
	lastModel string
}

func (f *fakeCatalogResolver) OfficialProvider(_ context.Context, providerKey, model string) string {
	f.calls++
	f.lastKey = providerKey
	f.lastModel = model
	return f.official
}

// TestEnrichAuthSelectionError_PrefersModelsCatalog verifies that the persisted
// models catalog (official_provider column) takes precedence over the registry
// and auth-manager fallbacks when a resolver is wired.
func TestEnrichAuthSelectionError_PrefersModelsCatalog(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	handler := NewBaseAPIHandlers(nil, manager)
	handler.SetModelsCatalogStore(&fakeCatalogResolver{official: "Anthropic PBC"})

	in := &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"}
	out := enrichAuthSelectionError(handler, context.Background(), in, []string{"claude"}, "claude-sonnet-4-6")

	var got *coreauth.Error
	if !errors.As(out, &got) || got == nil {
		t.Fatalf("expected coreauth.Error, got %T", out)
	}
	if !strings.Contains(got.Message, "providers=Anthropic PBC") {
		t.Fatalf("message should show catalog official_provider, got %q", got.Message)
	}
	// Claude hint should still fire (keyed on the internal "claude" key).
	if !strings.Contains(got.Message, "/v0/management/auth-files") {
		t.Fatalf("message missing management hint: %q", got.Message)
	}
}
