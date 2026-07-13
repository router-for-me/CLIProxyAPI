package claude

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

func TestClaudeMessagesMaxTokensOneProbeReturnsOKContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"claude-sonnet-4-6","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"."}]}]}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})

	handler.ClaudeMessages(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	respBody := recorder.Body.Bytes()
	if got := gjson.GetBytes(respBody, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text; body=%s", got, respBody)
	}
	if got := gjson.GetBytes(respBody, "content.0.text").String(); got != "ok" {
		t.Fatalf("content.0.text = %q, want ok; body=%s", got, respBody)
	}
	if got := gjson.GetBytes(respBody, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens; body=%s", got, respBody)
	}
}

func TestClaudeMessagesMaxTokensOneProbeStreamsOKDelta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"claude-opus-4-6","max_tokens":1,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"."}]}]}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})

	handler.ClaudeMessages(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	respBody := recorder.Body.String()
	if !strings.Contains(respBody, `"text":"ok"`) {
		t.Fatalf("stream body missing ok delta: %s", respBody)
	}
	if !strings.Contains(respBody, `"stop_reason":"max_tokens"`) {
		t.Fatalf("stream body missing max_tokens stop reason: %s", respBody)
	}
}
