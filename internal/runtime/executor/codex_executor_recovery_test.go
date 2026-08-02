package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalsse "github.com/router-for-me/CLIProxyAPI/v7/internal/sse"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexSSEScannerSplitsSafelyGluedFrames(t *testing.T) {
	scanner := internalsse.NewLineScanner(strings.NewReader(`data: {"type":"response.created"}data: {"type":"response.completed"}`), 1<<20)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, string(scanner.Bytes()))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "response.created") || !strings.Contains(lines[1], "response.completed") {
		t.Fatalf("lines = %q, want two safely split frames", lines)
	}
}

func TestCodexStreamCommitmentLifecycleClassification(t *testing.T) {
	for eventType, want := range map[string]cliproxyexecutor.StreamCommitment{
		"response.created":           cliproxyexecutor.StreamCommitmentProvisional,
		"response.in_progress":       cliproxyexecutor.StreamCommitmentProvisional,
		"response.queued":            cliproxyexecutor.StreamCommitmentProvisional,
		"response.output_text.delta": cliproxyexecutor.StreamCommitmentSemantic,
		"response.completed":         cliproxyexecutor.StreamCommitmentTerminal,
		"response.incomplete":        cliproxyexecutor.StreamCommitmentTerminal,
	} {
		if got := codexStreamCommitment(eventType); got != want {
			t.Errorf("codexStreamCommitment(%q) = %d, want %d", eventType, got, want)
		}
	}
}

func TestCodexExecutorStreamCommitmentClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-test\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\",\"item_id\":\"item_1\",\"output_index\":0,\"content_index\":0}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}, cliproxyexecutor.Request{
		Model:   "gpt-test",
		Payload: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), ResponseFormat: sdktranslator.FromString("claude"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	seen := map[cliproxyexecutor.StreamCommitment]bool{}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			seen[chunk.Commitment] = true
		}
	}
	for _, commitment := range []cliproxyexecutor.StreamCommitment{cliproxyexecutor.StreamCommitmentProvisional, cliproxyexecutor.StreamCommitmentSemantic, cliproxyexecutor.StreamCommitmentTerminal} {
		if !seen[commitment] {
			t.Fatalf("missing commitment %d; seen=%v", commitment, seen)
		}
	}
}

func TestCodexTerminalGenericServerErrorsRemainBadGateway(t *testing.T) {
	for name, event := range map[string][]byte{
		"error":           []byte(`{"type":"error","error":{"type":"server_error","message":"try again"}}`),
		"response.failed": []byte(`{"type":"response.failed","response":{"error":{"type":"server_error","message":"try again"}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			err, _, ok := codexTerminalFailureErr(event)
			if !ok {
				t.Fatal("event was not classified as terminal failure")
			}
			if err.StatusCode() != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", err.StatusCode())
			}
		})
	}
}
