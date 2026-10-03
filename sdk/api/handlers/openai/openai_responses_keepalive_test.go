package openai

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesKeepAlive_ClientMatrix(t *testing.T) {
	tests := []struct {
		name               string
		userAgent          string
		originator         string
		globalKeepAliveSec int
		disableAppKA       bool
		wantCodex          bool
		wantComment        bool
	}{
		{
			name:               "codex desktop UA with global keepalive 0",
			userAgent:          "Codex Desktop/1.0.0",
			globalKeepAliveSec: 0,
			wantCodex:          true,
			wantComment:        false,
		},
		{
			name:               "codex-tui UA with global keepalive 0",
			userAgent:          "codex-tui/0.1.0",
			globalKeepAliveSec: 0,
			wantCodex:          true,
			wantComment:        false,
		},
		{
			name:               "codex_cli_rs UA with global keepalive 0",
			userAgent:          "codex_cli_rs/0.1.0",
			globalKeepAliveSec: 0,
			wantCodex:          true,
			wantComment:        false,
		},
		{
			name:               "codex desktop originator with global keepalive 0",
			originator:         "codex desktop",
			globalKeepAliveSec: 0,
			wantCodex:          true,
			wantComment:        false,
		},
		{
			name:               "codex_cli_rs originator with global keepalive 0",
			originator:         "codex_cli_rs",
			globalKeepAliveSec: 0,
			wantCodex:          true,
			wantComment:        false,
		},
		{
			name:               "codex desktop UA with dual cadence (global keepalive 5s)",
			userAgent:          "Codex Desktop/1.0.0",
			globalKeepAliveSec: 5,
			wantCodex:          true,
			wantComment:        true,
		},
		{
			name:               "codex desktop with app keepalive explicitly disabled",
			userAgent:          "Codex Desktop/1.0.0",
			globalKeepAliveSec: 5,
			disableAppKA:       true,
			wantCodex:          false,
			wantComment:        true,
		},
		{
			name:               "generic curl UA with global keepalive 0",
			userAgent:          "curl/8.5.0",
			globalKeepAliveSec: 0,
			wantCodex:          false,
			wantComment:        false,
		},
		{
			name:               "generic curl UA with global keepalive 5s",
			userAgent:          "curl/8.5.0",
			globalKeepAliveSec: 5,
			wantCodex:          false,
			wantComment:        true,
		},
		{
			name:               "browser UA with global keepalive 0",
			userAgent:          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
			globalKeepAliveSec: 0,
			wantCodex:          false,
			wantComment:        false,
		},
		{
			name:               "arbitrary originator with global keepalive 5s",
			originator:         "custom-client",
			globalKeepAliveSec: 5,
			wantCodex:          false,
			wantComment:        true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &sdkconfig.SDKConfig{}
			cfg.Streaming.KeepAliveSeconds = tc.globalKeepAliveSec
			cfg.Streaming.DisableCodexAppKeepAlive = tc.disableAppKA

			base := handlers.NewBaseAPIHandlers(cfg, nil)
			h := NewOpenAIResponsesAPIHandler(base)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.userAgent != "" {
				c.Request.Header.Set("User-Agent", tc.userAgent)
			}
			if tc.originator != "" {
				c.Request.Header.Set("Originator", tc.originator)
			}

			var buf bytes.Buffer
			interval, writeKeepAlive, _ := h.configureResponsesKeepAlive(c, &buf, nil)

			if !tc.wantCodex {
				if interval != nil {
					t.Fatalf("expected nil interval for non-codex/disabled client, got %v", *interval)
				}
				if writeKeepAlive != nil {
					t.Fatalf("expected nil writeKeepAlive for non-codex/disabled client")
				}
				return
			}

			if interval == nil {
				t.Fatalf("expected non-nil interval for Codex client")
			}
			if writeKeepAlive == nil {
				t.Fatalf("expected non-nil writeKeepAlive for Codex client")
			}

			if tc.globalKeepAliveSec > 0 {
				if *interval != time.Duration(tc.globalKeepAliveSec)*time.Second {
					t.Errorf("ticker interval = %v, want %v", *interval, time.Duration(tc.globalKeepAliveSec)*time.Second)
				}
			} else {
				if *interval != 30*time.Second {
					t.Errorf("ticker interval = %v, want 30s", *interval)
				}
			}

			// Deterministic clock test for writeKeepAlive
			currentTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			origNow := codexAppKeepAliveNowFunc
			defer func() { codexAppKeepAliveNowFunc = origNow }()
			codexAppKeepAliveNowFunc = func() time.Time { return currentTime }

			// Reconfigure to bind start time to mock currentTime
			buf.Reset()
			_, writeKA, _ := h.configureResponsesKeepAlive(c, &buf, nil)

			// Tick 1 immediately: silence is 0, so app keepalive should not fire yet
			writeKA()
			output := buf.String()
			if strings.Contains(output, "codex.client.keepalive") {
				t.Fatalf("unexpected app keepalive at 0s: %q", output)
			}
			if tc.wantComment && !strings.Contains(output, ": keep-alive\n\n") {
				t.Fatalf("expected comment keepalive on tick, got %q", output)
			}
			if !tc.wantComment && strings.Contains(output, ": keep-alive") {
				t.Fatalf("unexpected comment keepalive when global keepalive=0: %q", output)
			}

			// Advance clock to 30s of silence
			currentTime = currentTime.Add(30 * time.Second)
			buf.Reset()
			writeKA()
			output = buf.String()
			if !strings.Contains(output, "data: {\"type\":\"codex.client.keepalive\"}\n\n") {
				t.Fatalf("expected app keepalive at 30s of silence, got %q", output)
			}
			if tc.wantComment && !strings.Contains(output, ": keep-alive\n\n") {
				t.Fatalf("expected comment keepalive alongside app keepalive, got %q", output)
			}
		})
	}
}

func TestResponsesKeepAlive_SilenceResetOnChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &sdkconfig.SDKConfig{}
	cfg.Streaming.KeepAliveSeconds = 0
	base := handlers.NewBaseAPIHandlers(cfg, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/1.0.0")

	currentTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	origNow := codexAppKeepAliveNowFunc
	defer func() { codexAppKeepAliveNowFunc = origNow }()
	codexAppKeepAliveNowFunc = func() time.Time { return currentTime }

	var buf bytes.Buffer
	var chunkReceived []byte
	onChunk := func(chunk []byte) {
		chunkReceived = chunk
	}

	_, writeKeepAlive, wrappedWriteChunk := h.configureResponsesKeepAlive(c, &buf, onChunk)

	// Advance 20 seconds, then upstream sends a chunk
	currentTime = currentTime.Add(20 * time.Second)
	wrappedWriteChunk([]byte("upstream-chunk"))
	if string(chunkReceived) != "upstream-chunk" {
		t.Fatalf("wrappedWriteChunk did not pass chunk to underlying handler")
	}

	// Advance 15 seconds (total 35s since start, but only 15s since last chunk)
	currentTime = currentTime.Add(15 * time.Second)
	buf.Reset()
	writeKeepAlive()
	if buf.Len() != 0 {
		t.Fatalf("keepalive emitted while upstream was not silent for 30s: %q", buf.String())
	}

	// Advance another 16 seconds (now 31s since chunk)
	currentTime = currentTime.Add(16 * time.Second)
	buf.Reset()
	writeKeepAlive()
	if !strings.Contains(buf.String(), "data: {\"type\":\"codex.client.keepalive\"}\n\n") {
		t.Fatalf("expected keepalive after 31s of silence since chunk, got %q", buf.String())
	}
}

func TestResponsesKeepAlive_NoContentDeltaInvention(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &sdkconfig.SDKConfig{}
	cfg.Streaming.KeepAliveSeconds = 0
	base := handlers.NewBaseAPIHandlers(cfg, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/1.0.0")

	currentTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	origNow := codexAppKeepAliveNowFunc
	defer func() { codexAppKeepAliveNowFunc = origNow }()
	codexAppKeepAliveNowFunc = func() time.Time { return currentTime }

	var buf bytes.Buffer
	_, writeKeepAlive, _ := h.configureResponsesKeepAlive(c, &buf, nil)

	currentTime = currentTime.Add(30 * time.Second)
	writeKeepAlive()

	raw := buf.String()
	if !strings.HasPrefix(raw, "data: ") || !strings.HasSuffix(raw, "\n\n") {
		t.Fatalf("malformed SSE data frame: %q", raw)
	}

	payload := strings.TrimPrefix(strings.TrimSpace(raw), "data: ")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("keepalive payload is not valid JSON: %v", err)
	}

	// Must ONLY contain {"type":"codex.client.keepalive"}
	if len(parsed) != 1 {
		t.Fatalf("keepalive payload has unexpected extra keys: %v", parsed)
	}
	if parsed["type"] != "codex.client.keepalive" {
		t.Fatalf("type = %v, want codex.client.keepalive", parsed["type"])
	}

	// Verify strictly no assistant content / deltas / tokens
	disallowed := []string{
		"delta", "text_delta", "output_text", "content", "role",
		"assistant", "response", "item", "usage", "completion_tokens",
	}
	for _, key := range disallowed {
		if gjson.Get(payload, key).Exists() {
			t.Fatalf("disallowed key %q found in keepalive payload: %s", key, payload)
		}
	}
}

func TestChatCompletionsKeepAlive_UnchangedForCodexAndNonCodex(t *testing.T) {
	// Ensure Chat Completions handler does not emit codex.client.keepalive
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/1.0.0")

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"hello"}}]}`)
	close(data)
	close(errs)

	cfg := &sdkconfig.SDKConfig{}
	cfg.Streaming.KeepAliveSeconds = 1
	base := handlers.NewBaseAPIHandlers(cfg, nil)
	h := NewOpenAIAPIHandler(base)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("recorder does not implement http.Flusher")
	}

	h.handleStreamResult(c, flusher, func(error) {}, data, errs)

	body := recorder.Body.String()
	if strings.Contains(body, "codex.client.keepalive") {
		t.Fatalf("Chat Completions must never emit codex.client.keepalive, got %q", body)
	}
	if !strings.Contains(body, "data: [DONE]\n\n") {
		t.Fatalf("Chat Completions stream missing [DONE] terminal chunk, got %q", body)
	}
}

func TestForwardResponsesStream_CodexKeepAliveIntegration(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	c.Request.Header.Set("User-Agent", "Codex Desktop/1.0.0")

	// Override interval for fast integration testing
	fastInterval := 15 * time.Millisecond
	codexAppKeepAliveIntervalOverrideForTest = &fastInterval
	defer func() { codexAppKeepAliveIntervalOverrideForTest = nil }()

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)

	done := make(chan struct{})
	go func() {
		h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
		close(done)
	}()

	// Wait for at least one keepalive to be written while silent
	deadline := time.After(300 * time.Millisecond)
	foundKeepAlive := false
	for !foundKeepAlive {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for codex keepalive; recorder body: %q", recorder.Body.String())
		default:
			if strings.Contains(recorder.Body.String(), "data: {\"type\":\"codex.client.keepalive\"}\n\n") {
				foundKeepAlive = true
			} else {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}

	// Complete stream cleanly
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)
	<-done

	body := recorder.Body.String()
	if !strings.Contains(body, "data: {\"type\":\"codex.client.keepalive\"}\n\n") {
		t.Fatalf("expected keepalive in body, got %q", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("expected response.completed in body, got %q", body)
	}
}
