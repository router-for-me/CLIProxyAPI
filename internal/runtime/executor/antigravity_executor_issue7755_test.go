package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestConvertStreamChunksToNonStream_PreservesNonThoughtTextSignature(t *testing.T) {
	exec := &AntigravityExecutor{}

	t.Run("single signed non-thought text part", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello","thoughtSignature":"sig1"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if got := parts[0].Get("text").String(); got != "hello" {
			t.Fatalf("parts[0].text = %q, want %q; out=%s", got, "hello", out)
		}
		if got := parts[0].Get("thoughtSignature").String(); got != "sig1" {
			t.Fatalf("parts[0].thoughtSignature = %q, want %q; out=%s", got, "sig1", out)
		}
		if parts[0].Get("thought").Exists() {
			t.Fatalf("parts[0] unexpectedly marked as thought; out=%s", out)
		}
	})

	t.Run("snake_case thought_signature normalized on text part", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello","thought_signature":"sig_snake"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if got := parts[0].Get("thoughtSignature").String(); got != "sig_snake" {
			t.Fatalf("parts[0].thoughtSignature = %q, want %q; out=%s", got, "sig_snake", out)
		}
		if parts[0].Get("thought_signature").Exists() {
			t.Fatalf("parts[0] still has snake_case thought_signature; out=%s", out)
		}
	})

	t.Run("streamed text chunks ending with signed text chunk", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"lo","thoughtSignature":"sig1"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if got := parts[0].Get("text").String(); got != "hello" {
			t.Fatalf("parts[0].text = %q, want %q; out=%s", got, "hello", out)
		}
		if got := parts[0].Get("thoughtSignature").String(); got != "sig1" {
			t.Fatalf("parts[0].thoughtSignature = %q, want %q; out=%s", got, "sig1", out)
		}
	})

	t.Run("whitespace-only text with thoughtSignature is preserved", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"   ","thoughtSignature":"sig_ws"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if got := parts[0].Get("text").String(); got != "   " {
			t.Fatalf("parts[0].text = %q, want %q; out=%s", got, "   ", out)
		}
		if got := parts[0].Get("thoughtSignature").String(); got != "sig_ws" {
			t.Fatalf("parts[0].thoughtSignature = %q, want %q; out=%s", got, "sig_ws", out)
		}
	})
}

func TestConvertStreamChunksToNonStream_PreservesEmptyTextSignatureCarrier(t *testing.T) {
	exec := &AntigravityExecutor{}

	t.Run("standalone empty-text carrier", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig2"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if !parts[0].Get("text").Exists() || parts[0].Get("text").String() != "" {
			t.Fatalf("parts[0].text = %q (exists=%v), want empty string; out=%s", parts[0].Get("text").String(), parts[0].Get("text").Exists(), out)
		}
		if got := parts[0].Get("thoughtSignature").String(); got != "sig2" {
			t.Fatalf("parts[0].thoughtSignature = %q, want %q; out=%s", got, "sig2", out)
		}
	})

	t.Run("standalone empty-text snake_case carrier", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thought_signature":"sig_snake2"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if got := parts[0].Get("thoughtSignature").String(); got != "sig_snake2" {
			t.Fatalf("parts[0].thoughtSignature = %q, want %q; out=%s", got, "sig_snake2", out)
		}
	})

	t.Run("trailing carrier after functionCall", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"run","args":{"cmd":"ls"}}}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig_fc"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if !parts[0].Get("functionCall").Exists() {
			t.Fatalf("parts[0] missing functionCall; out=%s", out)
		}
		if !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" {
			t.Fatalf("parts[1].text = %q (exists=%v), want empty string; out=%s", parts[1].Get("text").String(), parts[1].Get("text").Exists(), out)
		}
		if got := parts[1].Get("thoughtSignature").String(); got != "sig_fc" {
			t.Fatalf("parts[1].thoughtSignature = %q, want %q; out=%s", got, "sig_fc", out)
		}
	})

	t.Run("trailing carriers after visible text keep carrier boundary and distinct signatures", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"visible answer"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig1"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig2"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 3 {
			t.Fatalf("parts len = %d, want 3; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "visible answer" || parts[0].Get("thoughtSignature").Exists() {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != "sig1" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
		if parts[2].Get("text").String() != "" || parts[2].Get("thoughtSignature").String() != "sig2" {
			t.Fatalf("parts[2] unexpected: %s", parts[2].Raw)
		}
	})

	t.Run("trailing non-thought carrier after thought part", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"analyzing"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig_after_thought"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "analyzing" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if parts[1].Get("thought").Bool() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != "sig_after_thought" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("unsigned empty text terminal frame is still dropped", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello","thoughtSignature":"sig1"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 1 {
			t.Fatalf("parts len = %d, want 1; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "hello" || parts[0].Get("thoughtSignature").String() != "sig1" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
	})
}

func TestConvertStreamChunksToNonStream_PreservesDistinctSignedTextBoundaries(t *testing.T) {
	exec := &AntigravityExecutor{}

	t.Run("consecutive signed non-thought text parts do not overwrite earlier signature", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"A","thoughtSignature":"sig1"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"B","thoughtSignature":"sig2"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "A" || parts[0].Get("thoughtSignature").String() != "sig1" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != "B" || parts[1].Get("thoughtSignature").String() != "sig2" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("signed text followed by unsigned text preserves boundary", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"A","thoughtSignature":"sig1"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"B"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "A" || parts[0].Get("thoughtSignature").String() != "sig1" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != "B" || parts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("consecutive signed thought parts do not overwrite earlier signature", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"t1","thoughtSignature":"sig_t1"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"t2","thoughtSignature":"sig_t2"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "t1" || parts[0].Get("thoughtSignature").String() != "sig_t1" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if !parts[1].Get("thought").Bool() || parts[1].Get("text").String() != "t2" || parts[1].Get("thoughtSignature").String() != "sig_t2" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("same-chunk unsigned text before signed text preserves part boundary", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"A"},{"text":"B","thoughtSignature":"sig_b"}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 2 {
			t.Fatalf("parts len = %d, want 2; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "A" || parts[0].Get("thoughtSignature").Exists() {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != "B" || parts[1].Get("thoughtSignature").String() != "sig_b" {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
	})

	t.Run("same-chunk thought followed by signed thought text preserves boundary while thought carrier merges", func(t *testing.T) {
		distinctChunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"t1"},{"thought":true,"text":"t2","thoughtSignature":"sig_t2"}]},"finishReason":"STOP"}]}}`),
		}
		distinctOut := exec.convertStreamChunksToNonStream(distinctChunks)
		distinctParts := ojson.GetBytes(distinctOut, "response.candidates.0.content.parts").Array()
		if len(distinctParts) != 2 {
			t.Fatalf("distinct thought parts len = %d, want 2; out=%s", len(distinctParts), distinctOut)
		}
		if !distinctParts[0].Get("thought").Bool() || distinctParts[0].Get("text").String() != "t1" || distinctParts[0].Get("thoughtSignature").Exists() {
			t.Fatalf("distinctParts[0] unexpected: %s", distinctParts[0].Raw)
		}
		if !distinctParts[1].Get("thought").Bool() || distinctParts[1].Get("text").String() != "t2" || distinctParts[1].Get("thoughtSignature").String() != "sig_t2" {
			t.Fatalf("distinctParts[1] unexpected: %s", distinctParts[1].Raw)
		}

		carrierChunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"analyzing"},{"thought":true,"text":"","thoughtSignature":"sig_t"}]},"finishReason":"STOP"}]}}`),
		}
		carrierOut := exec.convertStreamChunksToNonStream(carrierChunks)
		carrierParts := ojson.GetBytes(carrierOut, "response.candidates.0.content.parts").Array()
		if len(carrierParts) != 1 {
			t.Fatalf("thought carrier parts len = %d, want 1; out=%s", len(carrierParts), carrierOut)
		}
		if !carrierParts[0].Get("thought").Bool() || carrierParts[0].Get("text").String() != "analyzing" || carrierParts[0].Get("thoughtSignature").String() != "sig_t" {
			t.Fatalf("carrierParts[0] unexpected: %s", carrierParts[0].Raw)
		}
	})

	t.Run("whitespace separator isolated between signed text and empty-text carrier is preserved", func(t *testing.T) {
		chunks := [][]byte{
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello","thoughtSignature":"sig1"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":" "}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig2"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"world"}]}}]}}`),
			[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}}`),
		}
		out := exec.convertStreamChunksToNonStream(chunks)
		parts := ojson.GetBytes(out, "response.candidates.0.content.parts").Array()
		if len(parts) != 4 {
			t.Fatalf("parts len = %d, want 4; out=%s", len(parts), out)
		}
		if parts[0].Get("text").String() != "Hello" || parts[0].Get("thoughtSignature").String() != "sig1" {
			t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
		}
		if parts[1].Get("text").String() != " " || parts[1].Get("thoughtSignature").Exists() {
			t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
		}
		if !parts[2].Get("text").Exists() || parts[2].Get("text").String() != "" || parts[2].Get("thoughtSignature").String() != "sig2" {
			t.Fatalf("parts[2] unexpected: %s", parts[2].Raw)
		}
		if parts[3].Get("text").String() != "world" || parts[3].Get("thoughtSignature").Exists() {
			t.Fatalf("parts[3] unexpected: %s", parts[3].Raw)
		}
		combined := parts[0].Get("text").String() + parts[1].Get("text").String() + parts[2].Get("text").String() + parts[3].Get("text").String()
		if combined != "Hello world" {
			t.Fatalf("combined text = %q, want %q; out=%s", combined, "Hello world", out)
		}
	})
}

func TestExecuteClaudeNonStream_PreservesTextAndCarrierThoughtSignaturesEndToEnd(t *testing.T) {
	sseBody := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\",\"thoughtSignature\":\"sig1\"}]}}],\"modelVersion\":\"gemini-3-pro\",\"responseId\":\"resp-7755\"}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\",\"thoughtSignature\":\"sig2\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7},\"modelVersion\":\"gemini-3-pro\",\"responseId\":\"resp-7755\"}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseBody)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		ID: "auth-7755",
		Metadata: map[string]any{
			"access_token": "token-7755",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-7755",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}

	resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3-pro",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         false,
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	parts := ojson.GetBytes(resp.Payload, "candidates.0.content.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2; payload=%s", len(parts), resp.Payload)
	}
	if parts[0].Get("text").String() != "hello" || parts[0].Get("thoughtSignature").String() != "sig1" {
		t.Fatalf("parts[0] unexpected: %s", parts[0].Raw)
	}
	if !parts[1].Get("text").Exists() || parts[1].Get("text").String() != "" || parts[1].Get("thoughtSignature").String() != "sig2" {
		t.Fatalf("parts[1] unexpected: %s", parts[1].Raw)
	}
}
