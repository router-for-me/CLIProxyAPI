package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/tidwall/gjson"
)

func TestClaudeRoutesUseStructuredAuthErrors(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, key := range []string{"", "wrong-key"} {
			t.Run(path+"/"+key, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, path+"?beta=true", strings.NewReader(`{"model":"test-model","messages":[]}`))
				if key != "" {
					req.Header.Set("X-Api-Key", key)
				}
				recorder := httptest.NewRecorder()
				server.engine.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", recorder.Code)
				}
				body := recorder.Body.Bytes()
				if gjson.GetBytes(body, "type").String() != "error" || gjson.GetBytes(body, "error.type").String() != "authentication_error" {
					t.Fatalf("expected Anthropic authentication error, got %s", body)
				}
				wantMessage := "Missing API key"
				if key != "" {
					wantMessage = "Invalid API key"
				}
				if got := gjson.GetBytes(body, "error.message").String(); got != wantMessage {
					t.Errorf("error.message = %q, want %q", got, wantMessage)
				}
			})
		}
	}
}

type claudeAuthErrorProvider struct {
	err *sdkaccess.AuthError
}

func (p claudeAuthErrorProvider) Identifier() string { return "claude-auth-error-test" }

func (p claudeAuthErrorProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return nil, p.err
}

func TestClaudeAuthErrorsPreserveStatusAndSafeMessage(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      *sdkaccess.AuthError
		wantType string
	}{
		{name: "service failure", err: sdkaccess.NewInternalAuthError("Authentication service unavailable", errors.New("private cause")), wantType: "api_error"},
		{name: "permission denied", err: &sdkaccess.AuthError{StatusCode: http.StatusForbidden, Message: "Access denied"}, wantType: "permission_error"},
		{name: "rate limited", err: &sdkaccess.AuthError{StatusCode: http.StatusTooManyRequests, Message: "Too many authentication attempts"}, wantType: "rate_limit_error"},
	} {
		for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
			t.Run(tt.name+path, func(t *testing.T) {
				manager := sdkaccess.NewManager()
				manager.SetProviders([]sdkaccess.Provider{claudeAuthErrorProvider{err: tt.err}})
				router := gin.New()
				router.Use(AuthMiddleware(manager))
				router.POST(path, func(c *gin.Context) { t.Error("rejected request reached endpoint") })
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
				if recorder.Code != tt.err.HTTPStatusCode() {
					t.Fatalf("status = %d, want %d", recorder.Code, tt.err.HTTPStatusCode())
				}
				body := recorder.Body.Bytes()
				if gjson.GetBytes(body, "type").String() != "error" || gjson.GetBytes(body, "error.type").String() != tt.wantType {
					t.Fatalf("expected %s error, got %s", tt.wantType, body)
				}
				if got := gjson.GetBytes(body, "error.message").String(); got != tt.err.Message {
					t.Errorf("error.message = %q, want public message %q", got, tt.err.Message)
				}
				if strings.Contains(string(body), "private cause") {
					t.Error("private authentication cause was returned to client")
				}
			})
		}
	}
}

func TestNonClaudeAuthErrorsKeepExistingEnvelope(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1beta/models/test:generateContent"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.Header.Set("Anthropic-Version", "2023-06-01")
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized || gjson.GetBytes(recorder.Body.Bytes(), "error").String() != "Missing API key" {
				t.Fatalf("existing auth response changed: %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
