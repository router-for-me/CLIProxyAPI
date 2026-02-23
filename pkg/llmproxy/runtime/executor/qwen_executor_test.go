package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "qwen-max", "qwen-max", ""},
		{"with level suffix", "qwen-max(high)", "qwen-max", "high"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max", "16384"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestQwenExecutorExecuteStreamSplitsFinishWithUsage(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}` + "\n"))
	}))
	defer server.Close()

	executor := NewQwenExecutor(&config.Config{})
	streamResult, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": server.URL + "/v1",
			"api_key":  "test-api-key",
		},
	}, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"ping"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}

	var chunks [][]byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk.Payload)
	}

	var chunksWithUsage int
	var chunkWithFinish int
	var chunksWithContent int
	for _, chunk := range chunks {
		if gjson.ParseBytes(chunk).Get("usage").Exists() {
			chunksWithUsage++
		}
		if gjson.ParseBytes(chunk).Get("choices.0.finish_reason").Exists() {
			chunkWithFinish++
		}
		if gjson.ParseBytes(chunk).Get("choices.0.delta.content").Exists() {
			chunksWithContent++
		}
	}
	if chunksWithUsage != 1 {
		t.Fatalf("expected 1 usage chunk, got %d", chunksWithUsage)
	}
	if chunkWithFinish != 1 {
		t.Fatalf("expected 1 finish-reason chunk, got %d", chunkWithFinish)
	}
	if chunksWithContent != 1 {
		t.Fatalf("expected 1 content chunk, got %d", chunksWithContent)
	}
}
