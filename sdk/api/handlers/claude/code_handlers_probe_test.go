package claude

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestClaudeMessagesShortCircuitNonStreamingSample(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-4-6","max_tokens":1,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"x"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler := &ClaudeCodeAPIHandler{}
	handler.ClaudeMessages(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	body := recorder.Body.Bytes()
	if got := gjson.GetBytes(body, "content").Raw; got != "[]" {
		t.Fatalf("content = %q, want []; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "usage.output_tokens").Int(); got != 0 {
		t.Fatalf("output_tokens = %d, want 0; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "claude-sonnet-4-6" {
		t.Fatalf("model = %q, want claude-sonnet-4-6; body=%s", got, body)
	}
}

func TestClaudeMessagesShortCircuitStreamingSample(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-4-6","max_tokens":1,"stream":true,"messages":[{"role":"user","content":"."},{"role":"assistant","content":"x"}]}`))
	c.Request.Header.Set("Content-Type", "application/json")

	handler := &ClaudeCodeAPIHandler{}
	handler.ClaudeMessages(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		"event: message_start",
		`"stop_reason":"max_tokens"`,
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q; body=%s", want, body)
		}
	}
}

func TestShouldShortCircuitClaudeSample(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "single user message not shorted",
			body: `{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			name: "probe shape shorts",
			body: `{"max_tokens":1,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}]}`,
			want: true,
		},
		{
			name: "normal multi-turn request not shorted",
			body: `{"max_tokens":64,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"}]}`,
			want: false,
		},
		{
			name: "single-token classifier (user only) not shorted",
			body: `{"max_tokens":1,"messages":[{"role":"user","content":"Is this spam? Reply yes or no."}]}`,
			want: false,
		},
		{
			name: "single-token tool-gated classifier not shorted",
			body: `{"max_tokens":1,"tools":[{"name":"check","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"."},{"role":"assistant","content":"x"}]}`,
			want: false,
		},
		{
			name: "single-token tool_choice forced not shorted",
			body: `{"max_tokens":1,"tool_choice":{"type":"any"},"messages":[{"role":"user","content":"."},{"role":"assistant","content":"x"}]}`,
			want: false,
		},
		{
			name: "multi-turn ending on user not shorted",
			body: `{"max_tokens":1,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"world"},{"role":"user","content":"next"}]}`,
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldShortCircuitClaudeSample([]byte(tc.body)); got != tc.want {
				t.Fatalf("shouldShortCircuitClaudeSample() = %v, want %v", got, tc.want)
			}
		})
	}
}
