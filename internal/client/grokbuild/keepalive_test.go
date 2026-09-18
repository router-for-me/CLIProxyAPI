package grokbuild

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIsGrokClientUserAgent(t *testing.T) {
	tests := []struct {
		ua   string
		want bool
	}{
		{"grok-shell/0.2.119 (macos; aarch64)", true},
		{"grok-pager/1.0.5 grok-shell/1.0.5 (linux; x86_64)", true},
		{"grok-pager/1.0.5", true},
		{"GROK-PAGER/1.0", true},
		{"GROK-SHELL/1.0", true},
		{"curl/8.7.1", false},
		{"openai-python/1.0.0", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsGrokClientUserAgent(tc.ua); got != tc.want {
			t.Errorf("IsGrokClientUserAgent(%q) = %v, want %v", tc.ua, got, tc.want)
		}
	}
}

func TestIsGrokClientHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    bool
	}{
		{
			name:    "User-Agent with grok-pager",
			headers: http.Header{"User-Agent": []string{"grok-pager/1.0.5"}},
			want:    true,
		},
		{
			name:    "case insensitive header name",
			headers: http.Header{"user-agent": []string{"grok-shell/0.2"}},
			want:    true,
		},
		{
			name:    "unrelated user agent",
			headers: http.Header{"User-Agent": []string{"curl/8.7.1"}},
			want:    false,
		},
		{
			name:    "nil headers",
			headers: nil,
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsGrokClientHeaders(tc.headers); got != tc.want {
				t.Errorf("IsGrokClientHeaders() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsGrokClientContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "grok-pager/1.0.5 grok-shell/1.0.5")

	ctx := context.WithValue(context.Background(), "gin", c)
	if !IsGrokClientContext(ctx, nil) {
		t.Error("expected IsGrokClientContext to detect gin context user agent")
	}

	plainCtx := context.Background()
	headers := http.Header{"User-Agent": []string{"grok-shell/1.0"}}
	if !IsGrokClientContext(plainCtx, headers) {
		t.Error("expected IsGrokClientContext to detect headers when gin context is absent")
	}
}

func TestIsKeepalivePayload(t *testing.T) {
	tests := []struct {
		payload []byte
		want    bool
	}{
		{[]byte(`{"type":"keepalive","sequence_number":3}`), true},
		{[]byte(`{"type":"keepalive"}`), true},
		{[]byte(`{"type":"response.created"}`), false},
		{[]byte(`{"type":"response.reasoning.delta"}`), false},
		{[]byte(``), false},
	}
	for _, tc := range tests {
		if got := IsKeepalivePayload(tc.payload); got != tc.want {
			t.Errorf("IsKeepalivePayload(%s) = %v, want %v", string(tc.payload), got, tc.want)
		}
	}
}

func TestIsKeepaliveSSELine(t *testing.T) {
	tests := []struct {
		line []byte
		want bool
	}{
		{[]byte("event: keepalive"), true},
		{[]byte("event: keepalive\n"), true},
		{[]byte("  event: keepalive  "), true},
		{[]byte(`data: {"type":"keepalive","sequence_number":3}`), true},
		{[]byte(`data: {"type":"keepalive"}`), true},
		{[]byte("event: response.created"), false},
		{[]byte("event: keepalive-other"), false},
		{[]byte(`data: {"type":"response.created"}`), false},
		{[]byte(""), false},
	}
	for _, tc := range tests {
		if got := IsKeepaliveSSELine(tc.line); got != tc.want {
			t.Errorf("IsKeepaliveSSELine(%s) = %v, want %v", string(tc.line), got, tc.want)
		}
	}
}

func TestTransformKeepaliveSSELine(t *testing.T) {
	comment := KeepaliveSSEComment()

	// Grok client: keepalive line is transformed
	got, ok := TransformKeepaliveSSELine([]byte("event: keepalive"), true)
	if !ok || !bytes.Equal(got, comment) {
		t.Errorf("TransformKeepaliveSSELine(event: keepalive, true) = %q, %v, want %q, true", string(got), ok, string(comment))
	}

	got, ok = TransformKeepaliveSSELine([]byte(`data: {"type":"keepalive","sequence_number":3}`), true)
	if !ok || !bytes.Equal(got, comment) {
		t.Errorf("TransformKeepaliveSSELine(data: keepalive, true) = %q, %v, want %q, true", string(got), ok, string(comment))
	}

	// Grok client: normal line is untouched
	normalLine := []byte(`data: {"type":"response.created"}`)
	got, ok = TransformKeepaliveSSELine(normalLine, true)
	if ok || !bytes.Equal(got, normalLine) {
		t.Errorf("TransformKeepaliveSSELine(normalLine, true) = %q, %v, want unchanged, false", string(got), ok)
	}

	// Non-Grok client: keepalive line is untouched
	keepaliveLine := []byte("event: keepalive")
	got, ok = TransformKeepaliveSSELine(keepaliveLine, false)
	if ok || !bytes.Equal(got, keepaliveLine) {
		t.Errorf("TransformKeepaliveSSELine(event: keepalive, false) = %q, %v, want unchanged, false", string(got), ok)
	}
}
