package middleware

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
)

// mockPolicyService is a test double for policy.PolicyService.
type mockPolicyService struct {
	active        bool
	checkDecision policy.Decision
	checkErr      error
	consumeErr    error
	lastPrincipal string
	lastModel     string
	lastConsume   *policy.TokenCounts
	invalidateKey string
	invalidateAll bool
}

func (m *mockPolicyService) Active() bool { return m.active }

func (m *mockPolicyService) Check(_ context.Context, principal, model string) (policy.Decision, error) {
	m.lastPrincipal = principal
	m.lastModel = model
	if m.checkErr != nil {
		return policy.Decision{}, m.checkErr
	}
	return m.checkDecision, nil
}

func (m *mockPolicyService) Consume(_ context.Context, principal, model string, tokens policy.TokenCounts) error {
	m.lastPrincipal = principal
	m.lastModel = model
	tc := tokens
	m.lastConsume = &tc
	return m.consumeErr
}

func (m *mockPolicyService) InvalidateKey(_ context.Context, principal string) error {
	m.invalidateKey = principal
	return nil
}

func (m *mockPolicyService) InvalidateAll() { m.invalidateAll = true }

func newTestRouter(svc policy.PolicyService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		// Simulate AuthMiddleware setting the principal.
		c.Set("userApiKey", "test-principal")
		c.Next()
	})
	r.Use(PolicyMiddleware(svc))
	r.POST("/v1/chat", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return r
}

func TestPolicyMiddlewarePassThroughWhenInactive(t *testing.T) {
	svc := &mockPolicyService{active: false}
	r := newTestRouter(svc)
	w := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"model":"gpt-4o"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", body)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when inactive; got %d", w.Code)
	}
}

func TestPolicyMiddlewareAllowsWhenCheckSucceeds(t *testing.T) {
	svc := &mockPolicyService{active: true, checkDecision: policy.Decision{Allow: true}}
	r := newTestRouter(svc)
	w := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"model":"gpt-4o"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", body)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	if svc.lastModel != "gpt-4o" {
		t.Errorf("model = %q; want gpt-4o", svc.lastModel)
	}
	if svc.lastPrincipal != "test-principal" {
		t.Errorf("principal = %q; want test-principal", svc.lastPrincipal)
	}
}

func TestPolicyMiddlewareRestoresBodyForHandler(t *testing.T) {
	// The handler should be able to read the original body, even though the
	// middleware buffered it to extract the model field.
	svc := &mockPolicyService{active: true, checkDecision: policy.Decision{Allow: true}}
	r := gin.New()
	gin.SetMode(gin.TestMode)
	r.Use(func(c *gin.Context) { c.Set("userApiKey", "p"); c.Next() })
	r.Use(PolicyMiddleware(svc))
	r.POST("/x", func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		c.JSON(http.StatusOK, gin.H{"body": string(raw)})
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(`{"model":"claude-3","messages":[]}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"body":"{\"model\":\"claude-3\",\"messages\":[]}"`)) {
		t.Errorf("handler did not see the original body: %s", w.Body.String())
	}
}

func TestPolicyMiddlewareRejectsWithStatusCode(t *testing.T) {
	svc := &mockPolicyService{active: true, checkDecision: policy.Decision{
		Allow: false, Reason: "rate limit exceeded", StatusCode: 429,
	}}
	r := newTestRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewBufferString(`{"model":"gpt-4o"}`))
	r.ServeHTTP(w, req)
	if w.Code != 429 {
		t.Fatalf("expected 429; got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("rate_limit_exceeded")) {
		t.Errorf("error body missing rate_limit_exceeded type: %s", w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("rate limit exceeded")) {
		t.Errorf("error body missing reason: %s", w.Body.String())
	}
}

func TestPolicyMiddlewareDefaultsToForbiddenWhenStatusCodeZero(t *testing.T) {
	svc := &mockPolicyService{active: true, checkDecision: policy.Decision{Allow: false, Reason: "blocked"}}
	r := newTestRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewBufferString(`{"model":"x"}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403; got %d", w.Code)
	}
}

func TestPolicyMiddlewareFailsOpenOnCheckError(t *testing.T) {
	svc := &mockPolicyService{active: true, checkErr: context.DeadlineExceeded}
	r := newTestRouter(svc)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewBufferString(`{"model":"gpt-4o"}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (fail-open); got %d", w.Code)
	}
}

func TestPolicyMiddlewareSkipsWhenNoPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PolicyMiddleware(&mockPolicyService{active: true, checkDecision: policy.Decision{Allow: false, StatusCode: 403}}))
	r.POST("/v1/chat", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewBufferString(`{"model":"x"}`))
	r.ServeHTTP(w, req)
	// No principal set → middleware defers (open mode).
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (no principal); got %d", w.Code)
	}
}

func TestParseModelFieldHandlesAnthropicEnvelope(t *testing.T) {
	body := []byte(`{"request":{"model":"claude-3"}}`)
	if got := parseModelField(body); got != "claude-3" {
		t.Errorf("envelope model = %q; want claude-3", got)
	}
}

func TestParseModelFieldHandlesTopLevel(t *testing.T) {
	if got := parseModelField([]byte(`{"model":"gpt-4o","messages":[]}`)); got != "gpt-4o" {
		t.Errorf("model = %q; want gpt-4o", got)
	}
}

func TestParseModelFieldHandlesObjectModel(t *testing.T) {
	if got := parseModelField([]byte(`{"model":{"name":"gemini-pro"}}`)); got != "gemini-pro" {
		t.Errorf("object model = %q; want gemini-pro", got)
	}
}

func TestParseModelFieldEmptyBody(t *testing.T) {
	if got := parseModelField(nil); got != "" {
		t.Errorf("nil body should return empty; got %q", got)
	}
	if got := parseModelField([]byte("")); got != "" {
		t.Errorf("empty body should return empty; got %q", got)
	}
}

func TestParseModelFieldNonJSON(t *testing.T) {
	// Empty bodies (e.g. "data: ..." SSE requests) should not crash.
	if got := parseModelField([]byte("plain text")); got != "" {
		t.Errorf("non-JSON body should return empty; got %q", got)
	}
}
