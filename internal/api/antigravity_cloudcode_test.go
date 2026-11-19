package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type antigravityCloudCodeCapture struct {
	mu        sync.Mutex
	calls     int
	path      string
	query     string
	authz     string
	userAgent string
	body      []byte
}

func newAntigravityCloudCodeTestServer(t *testing.T, enabled bool) (*Server, *antigravityCloudCodeCapture) {
	t.Helper()
	capture := &antigravityCloudCodeCapture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.mu.Lock()
		capture.calls++
		capture.path = r.URL.Path
		capture.query = r.URL.RawQuery
		capture.authz = r.Header.Get("Authorization")
		capture.userAgent = r.Header.Get("User-Agent")
		capture.body = body
		capture.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"r1\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"r2\"}}\n\n")
	}))
	t.Cleanup(upstream.Close)

	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}}}
	cfg.Antigravity.CloudCodePassthrough = enabled
	server := newTestServerWithConfig(t, cfg)
	server.handlers.AuthManager.RegisterExecutor(executor.NewAntigravityExecutor(server.getConfig()))
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
		ID:       "antigravity-pool",
		Provider: "antigravity",
		Status:   auth.StatusActive,
		Metadata: map[string]any{
			"access_token": "pool-token",
			"project_id":   "pool-project",
			"base_url":     upstream.URL,
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	return server, capture
}

func serveAntigravityCloudCode(server *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer google-client-token")
	req.Header.Set("User-Agent", "antigravity/cli/1.5.12 (aidev_client; os_type=darwin)")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	return recorder
}

func TestAntigravityCloudCodePassthroughUsesPooledCredential(t *testing.T) {
	server, capture := newAntigravityCloudCodeTestServer(t, true)

	recorder := serveAntigravityCloudCode(server,
		"/antigravity-cloudcode/test-key/v1internal:streamGenerateContent?alt=sse",
		`{"project":"client-project","model":"gemini-x","requestType":"agent","request":{"contents":[]}}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	if got := recorder.Body.String(); !strings.Contains(got, `"r1"`) || !strings.Contains(got, `"r2"`) {
		t.Fatalf("response body = %q", got)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.path != "/v1internal:streamGenerateContent" || capture.query != "alt=sse" {
		t.Fatalf("upstream path = %q query = %q", capture.path, capture.query)
	}
	if capture.authz != "Bearer pool-token" {
		t.Fatalf("upstream Authorization = %q, want pooled token", capture.authz)
	}
	if !strings.HasPrefix(capture.userAgent, "antigravity/cli/1.5.12") {
		t.Fatalf("upstream User-Agent = %q, want client UA", capture.userAgent)
	}
	var sent map[string]any
	if errUnmarshal := json.Unmarshal(capture.body, &sent); errUnmarshal != nil {
		t.Fatalf("upstream body %q: %v", capture.body, errUnmarshal)
	}
	if sent["project"] != "pool-project" || sent["model"] != "gemini-x" || sent["requestType"] != "agent" {
		t.Fatalf("upstream body = %s", capture.body)
	}
}

func TestAntigravityCloudCodePassthroughRejectsRequests(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		path       string
		wantStatus int
	}{
		{name: "disabled", enabled: false, path: "/antigravity-cloudcode/test-key/v1internal:loadCodeAssist", wantStatus: http.StatusNotFound},
		{name: "wrong key", enabled: true, path: "/antigravity-cloudcode/bad-key/v1internal:loadCodeAssist", wantStatus: http.StatusUnauthorized},
		{name: "not v1internal", enabled: true, path: "/antigravity-cloudcode/test-key/v1/models", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, capture := newAntigravityCloudCodeTestServer(t, test.enabled)
			recorder := serveAntigravityCloudCode(server, test.path, `{"metadata":{}}`)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if capture.calls != 0 {
				t.Fatalf("upstream called %d times", capture.calls)
			}
		})
	}
}

func TestAntigravityCloudCodeKeyMiddlewareMasksPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	var seenPath, seenAuthz, seenParam string
	engine.Use(antigravityCloudCodeKeyMiddleware())
	engine.POST(antigravityCloudCodePrefix+":key/*method", func(c *gin.Context) {
		seenPath = c.Request.URL.Path
		seenAuthz = c.GetHeader("Authorization")
		seenParam = c.Param("method")
	})
	req := httptest.NewRequest(http.MethodPost, "/antigravity-cloudcode/secret-key/v1internal:fetchUserInfo", nil)
	req.Header.Set("Authorization", "Bearer google-client-token")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(seenPath, "secret-key") || seenPath != "/antigravity-cloudcode/-/v1internal:fetchUserInfo" {
		t.Fatalf("path = %q, want masked key", seenPath)
	}
	if seenAuthz != "Bearer secret-key" {
		t.Fatalf("Authorization = %q", seenAuthz)
	}
	if seenParam != "/v1internal:fetchUserInfo" {
		t.Fatalf("method param = %q", seenParam)
	}
}

func TestRewriteAntigravityCloudCodeProject(t *testing.T) {
	tests := []struct {
		name, body, project, want string
	}{
		{name: "replaces project", body: `{"project":"a","x":1}`, project: "b", want: `{"project":"b","x":1}`},
		{name: "no project field", body: `{"metadata":{"ideType":"ANTIGRAVITY"}}`, project: "b", want: `{"metadata":{"ideType":"ANTIGRAVITY"}}`},
		{name: "empty auth project", body: `{"project":"a"}`, project: "", want: `{"project":"a"}`},
		{name: "empty body", body: ``, project: "b", want: ``},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(rewriteAntigravityCloudCodeProject([]byte(test.body), test.project)); got != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}
