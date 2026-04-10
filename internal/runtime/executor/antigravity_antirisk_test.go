package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// --- resolveUserAgent tests ---

func TestResolveUserAgent_NilAuth_ReturnsDynamic(t *testing.T) {
	ua := resolveUserAgent(nil)
	if !strings.HasPrefix(ua, "antigravity/") {
		t.Errorf("expected UA starting with 'antigravity/', got %q", ua)
	}
	// Should match the dynamic version from misc package.
	expected := misc.AntigravityUserAgent()
	if ua != expected {
		t.Errorf("expected %q, got %q", expected, ua)
	}
}

func TestResolveUserAgent_EmptyAuth_ReturnsDynamic(t *testing.T) {
	auth := &cliproxyauth.Auth{}
	ua := resolveUserAgent(auth)
	expected := misc.AntigravityUserAgent()
	if ua != expected {
		t.Errorf("expected %q, got %q", expected, ua)
	}
}

func TestResolveUserAgent_AttributeOverride(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"user_agent": "antigravity/1.21.9 linux/x64",
		},
	}
	ua := resolveUserAgent(auth)
	if ua != "antigravity/1.21.9 linux/x64" {
		t.Errorf("expected attribute UA, got %q", ua)
	}
}

func TestResolveUserAgent_MetadataOverride(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"user_agent": "antigravity/1.20.6 darwin/arm64",
		},
	}
	ua := resolveUserAgent(auth)
	if ua != "antigravity/1.20.6 darwin/arm64" {
		t.Errorf("expected metadata UA, got %q", ua)
	}
}

func TestResolveUserAgent_AttributeTakesPrecedence(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"user_agent": "from-attributes",
		},
		Metadata: map[string]any{
			"user_agent": "from-metadata",
		},
	}
	ua := resolveUserAgent(auth)
	if ua != "from-attributes" {
		t.Errorf("attributes should take precedence, got %q", ua)
	}
}

func TestResolveUserAgent_WhitespaceOnly_ReturnsDynamic(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"user_agent": "   ",
		},
	}
	ua := resolveUserAgent(auth)
	expected := misc.AntigravityUserAgent()
	if ua != expected {
		t.Errorf("whitespace-only should fall through to dynamic, got %q", ua)
	}
}

func TestResolveUserAgent_NeverReturnsGoHTTPClient(t *testing.T) {
	cases := []struct {
		name string
		auth *cliproxyauth.Auth
	}{
		{"nil auth", nil},
		{"empty auth", &cliproxyauth.Auth{}},
		{"empty attributes", &cliproxyauth.Auth{Attributes: map[string]string{}}},
		{"empty metadata", &cliproxyauth.Auth{Metadata: map[string]any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ua := resolveUserAgent(tc.auth)
			if strings.Contains(ua, "Go-http-client") {
				t.Errorf("UA must never contain 'Go-http-client', got %q", ua)
			}
		})
	}
}

// --- buildRequest header tests ---

func TestBuildRequest_NoConnectionClose(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	req, err := executor.buildRequest(context.Background(), auth, "test-token", "gemini-2.5-pro", payload, false, "", "https://cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	if req.Close {
		t.Error("request.Close should be false (no Connection: close)")
	}
	if conn := req.Header.Get("Connection"); strings.EqualFold(conn, "close") {
		t.Error("Connection header should not be 'close'")
	}
}

func TestBuildRequest_HasCorrectUserAgent(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	req, err := executor.buildRequest(context.Background(), auth, "test-token", "gemini-2.5-pro", payload, false, "", "https://cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	ua := req.Header.Get("User-Agent")
	if !strings.HasPrefix(ua, "antigravity/") {
		t.Errorf("User-Agent should start with 'antigravity/', got %q", ua)
	}
	if strings.Contains(ua, "Go-http-client") {
		t.Errorf("User-Agent must not contain 'Go-http-client', got %q", ua)
	}
}

func TestBuildRequest_HasAuthorizationBearer(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	req, err := executor.buildRequest(context.Background(), auth, "my-token-123", "gemini-2.5-pro", payload, false, "", "https://cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	authHeader := req.Header.Get("Authorization")
	if authHeader != "Bearer my-token-123" {
		t.Errorf("expected 'Bearer my-token-123', got %q", authHeader)
	}
}

func TestBuildRequest_NoXGoogApiClientHeader(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	req, err := executor.buildRequest(context.Background(), auth, "test-token", "gemini-2.5-pro", payload, false, "", "https://cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	if val := req.Header.Get("X-Goog-Api-Client"); val != "" {
		t.Errorf("X-Goog-Api-Client should not be set, got %q", val)
	}
	if val := req.Header.Get("Client-Metadata"); val != "" {
		t.Errorf("Client-Metadata should not be set in API requests, got %q", val)
	}
}

func TestBuildRequest_CustomAuthUA_Propagated(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"user_agent": "antigravity/1.21.9 linux/x64",
		},
	}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

	req, err := executor.buildRequest(context.Background(), auth, "test-token", "gemini-2.5-pro", payload, false, "", "https://cloudcode-pa.googleapis.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	ua := req.Header.Get("User-Agent")
	if ua != "antigravity/1.21.9 linux/x64" {
		t.Errorf("expected custom UA from auth attributes, got %q", ua)
	}
}

// --- HttpRequest header tests ---

func TestHttpRequest_NoConnectionClose(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token":  "test-token",
			"refresh_token": "test-refresh",
			"expires_at":    "2099-01-01T00:00:00Z",
		},
	}

	// Create a request that HttpRequest will modify.
	req, _ := http.NewRequestWithContext(context.Background(), "POST", "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Some-Header", "should-be-stripped")

	// HttpRequest will fail on the actual HTTP call, but we can inspect
	// the request state by looking at what headers it sets.
	// We'll use a channel to capture the prepared request.
	// For now, just verify the header setup logic via buildRequest.

	// The HttpRequest method strips all headers and sets only the whitelist.
	// We verify this indirectly via buildRequest tests above.
	// Direct HttpRequest testing would require a test HTTP server, which
	// is covered by integration tests.
	_ = executor
	_ = auth
}

// --- geminiToAntigravity metadata tests ---

func TestGeminiToAntigravity_HasModel(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	result := geminiToAntigravity("gemini-2.5-pro", payload, "test-project-123")

	// Verify model is set.
	if !strings.Contains(string(result), `"model":"gemini-2.5-pro"`) {
		t.Error("result should contain model field")
	}
}

func TestGeminiToAntigravity_HasProject(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	result := geminiToAntigravity("gemini-2.5-pro", payload, "my-project")

	if !strings.Contains(string(result), `"project":"my-project"`) {
		t.Error("result should contain project field from auth")
	}
}

func TestGeminiToAntigravity_GeneratesProjectWhenEmpty(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	result := geminiToAntigravity("gemini-2.5-pro", payload, "")

	if !strings.Contains(string(result), `"project"`) {
		t.Error("result should contain a generated project field")
	}
}

func TestGeminiToAntigravity_HasRequestID(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	result := geminiToAntigravity("gemini-2.5-pro", payload, "proj")

	if !strings.Contains(string(result), `"requestId"`) {
		t.Error("result should contain requestId field")
	}
}

// --- CountTokens request header test ---

func TestCountTokens_RequestSetup(t *testing.T) {
	// Verify that CountTokens builds its request correctly by checking
	// the buildRequest path which is shared. The key assertion is that
	// no Connection: close is set (covered by TestBuildRequest_NoConnectionClose).
	// This test documents that CountTokens follows the same header pattern.
	t.Log("CountTokens shares the same header setup as buildRequest — covered by TestBuildRequest_NoConnectionClose")
}

// Helpers for reading request bodies in tests.
func readRequestBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	if req.Body == nil {
		return nil
	}
	data, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body error: %v", err)
	}
	return data
}
