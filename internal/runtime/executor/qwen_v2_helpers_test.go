package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQwenChatIDPrefersClaudeSessionID(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("x-claude-code-session-id", "claude-session")
	req.Header.Set("x-client-request-id", "client-request")

	got := qwenChatID(req)

	if got != "claude-session" {
		t.Fatalf("qwenChatID() = %q, want %q", got, "claude-session")
	}
}

func TestQwenChatIDFallsBackToClientRequestID(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("x-client-request-id", "client-request")

	got := qwenChatID(req)

	if got != "client-request" {
		t.Fatalf("qwenChatID() = %q, want %q", got, "client-request")
	}
}

func TestQwenBuildQueryFlattensMessages(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"assistant","content":"prev"},{"role":"user","content":"ask"}]}`)

	got, err := qwenBuildQuery(payload)
	if err != nil {
		t.Fatalf("qwenBuildQuery() error = %v", err)
	}
	if !strings.Contains(got, "sys") || !strings.Contains(got, "prev") || !strings.HasSuffix(got, "ask") {
		t.Fatalf("query = %q, want flattened context ending with latest user message", got)
	}
}

func TestQwenBuildQueryDoesNotDuplicateTrailingUser(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"ask"},{"role":"assistant","content":"reply"}]}`)

	got, err := qwenBuildQuery(payload)
	if err != nil {
		t.Fatalf("qwenBuildQuery() error = %v", err)
	}
	parts := strings.Split(got, "\n")
	if last := parts[len(parts)-1]; last != "ask" {
		t.Fatalf("last segment = %q, want %q", last, "ask")
	}
	count := 0
	for _, part := range parts {
		if part == "ask" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("user segment count = %d, want 1", count)
	}
}

func TestQwenBuildQueryHandlesStructuredContent(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"text":"part1"},{"text":"part2"}]},{"role":"user","content":{"text":"query"}}]}`)

	got, err := qwenBuildQuery(payload)
	if err != nil {
		t.Fatalf("qwenBuildQuery() error = %v", err)
	}
	if !strings.Contains(got, "part1 part2") || !strings.HasSuffix(got, "query") {
		t.Fatalf("query = %q, expect structured content to be flattened", got)
	}
}

func TestQwenBuildQueryErrorsWhenMessagesMissing(t *testing.T) {
	if _, err := qwenBuildQuery([]byte(`{}`)); err == nil {
		t.Fatal("expected error when messages missing")
	}
	if _, err := qwenBuildQuery([]byte(`{"messages":[]}`)); err == nil {
		t.Fatal("expected error when messages empty")
	}
}

func TestQwenBuildQueryErrorsWhenMessagesNotArray(t *testing.T) {
	if _, err := qwenBuildQuery([]byte(`{"messages":{}}`)); err == nil {
		t.Fatal("expected error when messages not array")
	}
}

func TestQwenBuildQueryErrorsWhenContentEmpty(t *testing.T) {
	if _, err := qwenBuildQuery([]byte(`{"messages":[{"role":"system","content":" \t"}]}`)); err == nil {
		t.Fatal("expected error when all content empty")
	}
}

func TestQwenBuildQueryIgnoresObjectWithoutText(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":{"tool":"image","url":"https://example.com"}},{"role":"user","content":"query"}]}`)

	got, err := qwenBuildQuery(payload)
	if err != nil {
		t.Fatalf("qwenBuildQuery() error = %v", err)
	}
	if strings.Contains(got, "image") {
		t.Fatalf("query = %q, object noise should be dropped", got)
	}
	if !strings.HasSuffix(got, "query") {
		t.Fatalf("query = %q, last segment should be user", got)
	}
}

func TestQwenBuildQueryTruncatesAfterLastUser(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"first"},{"role":"assistant","content":"reply"},{"role":"user","content":"second"},{"role":"assistant","content":"ignored"}]}`)

	got, err := qwenBuildQuery(payload)
	if err != nil {
		t.Fatalf("qwenBuildQuery() error = %v", err)
	}
	if strings.Contains(got, "ignored") {
		t.Fatalf("query = %q, assistant after user should be trimmed", got)
	}
	parts := strings.Split(got, "\n")
	if parts[len(parts)-1] != "second" {
		t.Fatalf("last = %q, want %q", parts[len(parts)-1], "second")
	}
}

func TestQwenBuildQueryErrorsWhenLastUserEmpty(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":"context"},{"role":"user","content":{"tool":"image"}},{"role":"assistant","content":"ok"}]}`)

	if _, err := qwenBuildQuery(payload); err == nil {
		t.Fatal("expected error when last user content empty")
	}
}

func TestQwenChatIDHandlesNilRequest(t *testing.T) {
	got := qwenChatID(nil)
	if got == "" {
		t.Fatal("expected non-empty ID for nil request")
	}
}

func TestQwenChatIDIgnoresBlankHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("x-claude-code-session-id", "   ")
	req.Header.Set("x-client-request-id", "\t")
	got := qwenChatID(req)
	if got == "" {
		t.Fatal("expected fallback ID when headers blank")
	}
}

func TestQwenExecutionBaseURLDefaultsToChatV2(t *testing.T) {
	got := qwenExecutionBaseURL("")
	want := "https://chat.qwen.ai/api/v2"
	if got != want {
		t.Fatalf("qwenExecutionBaseURL(\"\") = %q, want %q", got, want)
	}
}

func TestQwenCookieHeaderIncludesTokenAndSessionCookies(t *testing.T) {
	got := qwenCookieHeader("token-cookie", map[string]string{
		"refresh_token": "refresh",
		"session_id":    "session",
	})
	want := "token=token-cookie; refresh_token=refresh; session_id=session"
	if got != want {
		t.Fatalf("cookie header = %q, want %q", got, want)
	}
}

func TestQwenParseModelsResponseBuildsRegistryModels(t *testing.T) {
	payload := []byte(`{"success":true,"data":{"data":[{"id":"qwen3.6-plus","name":"Qwen3.6-Plus","object":"model","owned_by":"qwen","info":{"meta":{"description":"desc","max_context_length":1000000}}}]}}`)

	models, err := qwenParseModelsResponse(payload)
	if err != nil {
		t.Fatalf("qwenParseModelsResponse() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen3.6-plus" {
		t.Fatalf("models = %#v, want qwen3.6-plus", models)
	}
	if models[0].ContextLength != 1000000 {
		t.Fatalf("context length = %d, want %d", models[0].ContextLength, 1000000)
	}
	if models[0].Name != "Qwen3.6-Plus" {
		t.Fatalf("name = %q, want %q", models[0].Name, "Qwen3.6-Plus")
	}
	if models[0].Object != "model" || models[0].OwnedBy != "qwen" {
		t.Fatalf("object/owned_by = %q/%q", models[0].Object, models[0].OwnedBy)
	}
	if models[0].Description != "desc" {
		t.Fatalf("description = %q, want %q", models[0].Description, "desc")
	}
}

func TestWrapQwenErrorMapsQuotaTo429(t *testing.T) {
	code, retryAfter := wrapQwenError(context.Background(), http.StatusForbidden, []byte(`{"error":{"code":"insufficient_quota","message":"quota exceeded"}}`))
	if code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want %d", code, http.StatusTooManyRequests)
	}
	if retryAfter == nil {
		t.Fatal("retryAfter = nil, want cooldown duration")
	}
}

func TestWrapQwenErrorMapsQuota429ByMessageTo429(t *testing.T) {
	code, retryAfter := wrapQwenError(context.Background(), http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_error","message":"free allocated quota exceeded for today"}}`))
	if code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want %d", code, http.StatusTooManyRequests)
	}
	if retryAfter == nil {
		t.Fatal("retryAfter = nil, want cooldown duration")
	}
}

func TestWrapQwenErrorMapsSessionExpiredTo401(t *testing.T) {
	code, retryAfter := wrapQwenError(context.Background(), http.StatusForbidden, []byte(`{"error":{"code":"session_expired","message":"session expired or invalid","type":"auth_error"}}`))
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want %d", code, http.StatusUnauthorized)
	}
	if retryAfter != nil {
		t.Fatalf("retryAfter = %v, want nil for session errors", retryAfter)
	}
}

func TestWrapQwenErrorMapsSessionInvalidTo401(t *testing.T) {
	code, retryAfter := wrapQwenError(context.Background(), http.StatusForbidden, []byte(`{"error":{"code":"session_invalid","message":"session invalid","type":"auth_error"}}`))
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want %d", code, http.StatusUnauthorized)
	}
	if retryAfter != nil {
		t.Fatalf("retryAfter = %v, want nil for session errors", retryAfter)
	}
}

func TestWrapQwenErrorMapsNeedLoginHeuristicTo401(t *testing.T) {
	code, retryAfter := wrapQwenError(context.Background(), http.StatusForbidden, []byte(`{"error":{"type":"auth_error","message":"need login because token expired"}}`))
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want %d", code, http.StatusUnauthorized)
	}
	if retryAfter != nil {
		t.Fatalf("retryAfter = %v, want nil for session errors", retryAfter)
	}
}

func TestWrapQwenErrorDoesNotMapGenericForbiddenTo401(t *testing.T) {
	code, retryAfter := wrapQwenError(context.Background(), http.StatusForbidden, []byte(`{"error":{"type":"forbidden_error","message":"access denied by policy"}}`))
	if code != http.StatusForbidden {
		t.Fatalf("code = %d, want %d", code, http.StatusForbidden)
	}
	if retryAfter != nil {
		t.Fatalf("retryAfter = %v, want nil for non-session errors", retryAfter)
	}
}
