package helps

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestGinCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c
}

func getUsage(c *gin.Context) map[string]int64 {
	v, _ := c.Get(UpstreamRawUsageKey)
	m, _ := v.(map[string]int64)
	return m
}

// ---- TestExtractFirstUserText ------------------------------------------------

func TestExtractFirstUserText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "anthropic string content",
			body: `{"messages":[{"role":"user","content":"hello"}]}`,
			want: "hello",
		},
		{
			name: "anthropic array content blocks",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"world"}]}]}`,
			want: "world",
		},
		{
			name: "gemini contents",
			body: `{"contents":[{"role":"user","parts":[{"text":"gemini msg"}]}]}`,
			want: "gemini msg",
		},
		{
			name: "antigravity request.contents",
			body: `{"request":{"contents":[{"role":"user","parts":[{"text":"antgrav"}]}]}}`,
			want: "antgrav",
		},
		{
			name: "responses api input string",
			body: `{"input":"resp input"}`,
			want: "resp input",
		},
		{
			name: "responses api input array",
			body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"arr input"}]}]}`,
			want: "arr input",
		},
		{
			name: "empty body",
			body: `{}`,
			want: "",
		},
		{
			name: "malformed json",
			body: `{bad`,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFirstUserText([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("extractFirstUserText() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("truncation", func(t *testing.T) {
		long := strings.Repeat("x", 2500)
		body := `{"messages":[{"role":"user","content":"` + long + `"}]}`
		got := extractFirstUserText([]byte(body))
		if len(got) > 2003 {
			t.Fatalf("len(result) = %d, want <= 2003", len(got))
		}
	})
}

// ---- TestAccumulateResponseText ---------------------------------------------

func TestAccumulateResponseText(t *testing.T) {
	getResponseText := func(c *gin.Context) string {
		v, _ := c.Get(UpstreamResponseTextKey)
		s, _ := v.(string)
		return s
	}

	// Non-streaming subtests.
	nonStreaming := []struct {
		name string
		body string
		want string
	}{
		{
			name: "anthropic text block",
			body: `{"content":[{"type":"text","text":"ant resp"}]}`,
			want: "ant resp",
		},
		{
			name: "anthropic thinking+text",
			body: `{"content":[{"type":"thinking","thinking":"..."},{"type":"text","text":"answer"}]}`,
			want: "answer",
		},
		{
			name: "openai choices",
			body: `{"choices":[{"message":{"content":"oai resp"}}]}`,
			want: "oai resp",
		},
		{
			name: "gemini",
			body: `{"candidates":[{"content":{"parts":[{"text":"gem resp"}]}}]}`,
			want: "gem resp",
		},
		{
			name: "antigravity",
			body: `{"response":{"candidates":[{"content":{"parts":[{"text":"antgrav resp"}]}}]}}`,
			want: "antgrav resp",
		},
		{
			name: "codex output[]",
			body: `{"output":[{"content":[{"type":"output_text","text":"codex resp"}]}]}`,
			want: "codex resp",
		},
		{
			name: "codex response.output[]",
			body: `{"response":{"output":[{"content":[{"type":"output_text","text":"codex sse"}]}]}}`,
			want: "codex sse",
		},
		{
			name: "empty",
			body: `{}`,
			want: "",
		},
	}
	for _, tc := range nonStreaming {
		t.Run("non-streaming/"+tc.name, func(t *testing.T) {
			c := newTestGinCtx()
			accumulateResponseText(c, []byte(tc.body))
			got := getResponseText(c)
			if got != tc.want {
				t.Fatalf("accumulateResponseText() text = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("non-streaming/truncation", func(t *testing.T) {
		long := strings.Repeat("y", 5000)
		body := `{"content":[{"type":"text","text":"` + long + `"}]}`
		c := newTestGinCtx()
		accumulateResponseText(c, []byte(body))
		got := getResponseText(c)
		if len(got) > 4003 {
			t.Fatalf("len(result) = %d, want <= 4003", len(got))
		}
	})

	// Streaming SSE subtests.
	sse := func(jsonStr string) []byte {
		return []byte("data: " + jsonStr + "\n\n")
	}

	streamingCases := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "anthropic delta",
			body: sse(`{"delta":{"type":"text_delta","text":"hello"}}`),
			want: "hello",
		},
		{
			name: "openai delta",
			body: sse(`{"choices":[{"delta":{"content":"world"}}]}`),
			want: "world",
		},
		{
			name: "codex output_text.delta",
			body: sse(`{"type":"response.output_text.delta","delta":"codex d"}`),
			want: "codex d",
		},
		{
			name: "gemini native",
			body: sse(`{"candidates":[{"content":{"parts":[{"text":"gem d"}]}}]}`),
			want: "gem d",
		},
		{
			name: "gemini antigravity",
			body: sse(`{"response":{"candidates":[{"content":{"parts":[{"text":"ag d"}]}}]}}`),
			want: "ag d",
		},
	}
	for _, tc := range streamingCases {
		t.Run("streaming/"+tc.name, func(t *testing.T) {
			c := newTestGinCtx()
			accumulateResponseText(c, tc.body)
			got := getResponseText(c)
			if got != tc.want {
				t.Fatalf("accumulateResponseText() text = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("streaming/accumulation", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateResponseText(c, sse(`{"delta":{"type":"text_delta","text":"foo"}}`))
		accumulateResponseText(c, sse(`{"delta":{"type":"text_delta","text":"bar"}}`))
		got := getResponseText(c)
		if got != "foobar" {
			t.Fatalf("accumulateResponseText() text = %q, want %q", got, "foobar")
		}
	})

	t.Run("streaming/codex output_item.done no prior delta", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateResponseText(c, sse(`{"type":"response.output_item.done","item":{"content":[{"type":"output_text","text":"done"}]}}`))
		got := getResponseText(c)
		if got != "done" {
			t.Fatalf("accumulateResponseText() text = %q, want %q", got, "done")
		}
	})

	t.Run("streaming/codex output_item.done after delta", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateResponseText(c, sse(`{"type":"response.output_text.delta","delta":"x"}`))
		accumulateResponseText(c, sse(`{"type":"response.output_item.done","item":{"content":[{"type":"output_text","text":"full"}]}}`))
		got := getResponseText(c)
		if got != "x" {
			t.Fatalf("accumulateResponseText() text = %q, want %q", got, "x")
		}
	})
}

// ---- TestAccumulateUsage ----------------------------------------------------

func TestAccumulateUsage(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`))
		m := getUsage(c)
		if m["input_tokens"] != 10 {
			t.Fatalf("input_tokens = %d, want 10", m["input_tokens"])
		}
		if m["output_tokens"] != 20 {
			t.Fatalf("output_tokens = %d, want 20", m["output_tokens"])
		}
		if m["total_tokens"] != 30 {
			t.Fatalf("total_tokens = %d, want 30", m["total_tokens"])
		}
	})

	t.Run("openai", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":15,"total_tokens":20}}`))
		m := getUsage(c)
		if m["prompt_tokens"] != 5 {
			t.Fatalf("prompt_tokens = %d, want 5", m["prompt_tokens"])
		}
		if m["completion_tokens"] != 15 {
			t.Fatalf("completion_tokens = %d, want 15", m["completion_tokens"])
		}
		if m["total_tokens"] != 20 {
			t.Fatalf("total_tokens = %d, want 20", m["total_tokens"])
		}
	})

	t.Run("claude message.usage", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"message":{"usage":{"input_tokens":8,"output_tokens":12}}}`))
		m := getUsage(c)
		if m["input_tokens"] != 8 {
			t.Fatalf("input_tokens = %d, want 8", m["input_tokens"])
		}
		if m["output_tokens"] != 12 {
			t.Fatalf("output_tokens = %d, want 12", m["output_tokens"])
		}
		if m["total_tokens"] != 20 {
			t.Fatalf("total_tokens = %d, want 20", m["total_tokens"])
		}
	})

	t.Run("responses api", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"response":{"usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10}}}`))
		m := getUsage(c)
		if m["input_tokens"] != 3 {
			t.Fatalf("input_tokens = %d, want 3", m["input_tokens"])
		}
		if m["output_tokens"] != 7 {
			t.Fatalf("output_tokens = %d, want 7", m["output_tokens"])
		}
		if m["total_tokens"] != 10 {
			t.Fatalf("total_tokens = %d, want 10", m["total_tokens"])
		}
	})

	t.Run("gemini usageMetadata", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":6,"totalTokenCount":10}}`))
		m := getUsage(c)
		if m["prompt_tokens"] != 4 {
			t.Fatalf("prompt_tokens = %d, want 4", m["prompt_tokens"])
		}
		if m["completion_tokens"] != 6 {
			t.Fatalf("completion_tokens = %d, want 6", m["completion_tokens"])
		}
		if m["total_tokens"] != 10 {
			t.Fatalf("total_tokens = %d, want 10", m["total_tokens"])
		}
	})

	t.Run("antigravity response.usageMetadata", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"response":{"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":8,"totalTokenCount":10}}}`))
		m := getUsage(c)
		if m["prompt_tokens"] != 2 {
			t.Fatalf("prompt_tokens = %d, want 2", m["prompt_tokens"])
		}
		if m["completion_tokens"] != 8 {
			t.Fatalf("completion_tokens = %d, want 8", m["completion_tokens"])
		}
		if m["total_tokens"] != 10 {
			t.Fatalf("total_tokens = %d, want 10", m["total_tokens"])
		}
	})

	t.Run("nested details", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":3}}}`))
		m := getUsage(c)
		if m["cache_read_input_tokens"] != 5 {
			t.Fatalf("cache_read_input_tokens = %d, want 5", m["cache_read_input_tokens"])
		}
		if m["reasoning_tokens"] != 3 {
			t.Fatalf("reasoning_tokens = %d, want 3", m["reasoning_tokens"])
		}
	})

	t.Run("no double-count", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"prompt_tokens":10,"completion_tokens":20}}`))
		m := getUsage(c)
		if m["total_tokens"] != 30 {
			t.Fatalf("total_tokens = %d, want 30", m["total_tokens"])
		}
	})

	t.Run("setMax", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`))
		accumulateUsage(c, []byte(`{"usage":{"input_tokens":5,"output_tokens":5}}`))
		m := getUsage(c)
		if m["input_tokens"] != 10 {
			t.Fatalf("input_tokens = %d, want 10 (setMax should keep higher value)", m["input_tokens"])
		}
	})

	t.Run("empty body no panic", func(t *testing.T) {
		c := newTestGinCtx()
		accumulateUsage(c, []byte(`{}`))
		m := getUsage(c)
		if len(m) > 0 {
			// Acceptable: map may be nil or empty; non-zero values would be wrong.
			for k, v := range m {
				if v != 0 {
					t.Fatalf("expected zero usage for key %q, got %d", k, v)
				}
			}
		}
	})
}
