package e2e_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestZhipuE2E_Stream_OpenAICompatible(t *testing.T) {
	apiKey := os.Getenv("E2E_ZHIPU_API_KEY")
	baseURL := os.Getenv("E2E_ZHIPU_BASE_URL")
	if apiKey == "" || baseURL == "" {
		t.Skip("E2E_ZHIPU_API_KEY or E2E_ZHIPU_BASE_URL not set; skipping e2e stream test")
	}

	exec := executor.NewZhipuExecutor(&config.Config{})
	auth := &coreauth.Auth{Attributes: map[string]string{
		"api_key":  apiKey,
		"base_url": baseURL,
	}}

	payload := []byte(`{
		"model": "glm-4.6",
		"messages": [
			{"role": "user", "content": "stream ping"}
		],
		"stream": true
	}`)
	opts := sdkexec.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: payload,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := exec.ExecuteStream(ctx, auth, sdkexec.Request{Model: "glm-4.6", Payload: payload}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	gotChunk := false
	select {
	case <-ctx.Done():
		t.Fatalf("timeout waiting for stream chunks: %v", ctx.Err())
	case chunk, ok := <-ch:
		if !ok {
			t.Fatalf("stream channel closed before any chunk received")
		}
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if len(chunk.Payload) == 0 {
			t.Fatalf("empty first stream chunk payload")
		}
		gotChunk = true
	}
	if !gotChunk {
		t.Fatalf("no stream chunks received")
	}
}
