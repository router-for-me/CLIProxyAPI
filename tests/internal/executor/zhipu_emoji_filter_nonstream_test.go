package executor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestZhipuExecutor_NonStream_EmojiFiltered(t *testing.T) {
	// Upstream returns OpenAI-style chat completion with emoji in message.content
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok 😀🚀🇺🇸👍🏻"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	exec := executor.NewZhipuExecutor(cfg)
	ctx := context.Background()
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "tok", "base_url": srv.URL}}
	req := sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

	resp, err := exec.Execute(ctx, auth, req, sdkexec.Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty payload")
	}
	// Parse and assert no emoji in choices[0].message.content
	var obj struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Payload, &obj); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(obj.Choices) == 0 {
		t.Fatalf("no choices")
	}
	if s := obj.Choices[0].Message.Content; s == "" {
		// allowed; filtered to empty is acceptable
	} else if strings.ContainsAny(s, "😀🚀👍") || strings.ContainsRune(s, '\U0001F1FA') {
		t.Fatalf("emoji not stripped in non-stream: %q", s)
	}
}
