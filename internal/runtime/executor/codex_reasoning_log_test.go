package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

func TestLogCodexFinalReasoningEffort(t *testing.T) {
	logger := log.StandardLogger()
	prevLevel := logger.GetLevel()
	hook := test.NewLocal(logger)
	logger.SetLevel(log.DebugLevel)
	defer func() {
		hook.Reset()
		logger.SetLevel(prevLevel)
	}()

	logCodexFinalReasoningEffort([]byte(`{"reasoning":{"effort":"low"}}`), "gpt-5.4-mini")

	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Message != "codex: final reasoning effort after payload config" {
		t.Fatalf("log message = %q, want final reasoning effort message", entry.Message)
	}
	if got := entry.Data["effort"]; got != "low" {
		t.Fatalf("effort field = %v, want low", got)
	}
}

func TestCodexExecutorLogsPayloadOverrideReasoningEffort(t *testing.T) {
	logger := log.StandardLogger()
	prevLevel := logger.GetLevel()
	hook := test.NewLocal(logger)
	logger.SetLevel(log.DebugLevel)
	defer func() {
		hook.Reset()
		logger.SetLevel(prevLevel)
	}()

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1777300000,\"status\":\"completed\",\"model\":\"gpt-5.4-mini\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5.4-mini"}},
					Params: map[string]any{"reasoning.effort": "low"},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Hello!"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning.effort").String(); got != "low" {
		t.Fatalf("upstream reasoning.effort = %q, want low; body=%s", got, string(gotBody))
	}

	for _, entry := range hook.AllEntries() {
		if entry.Message == "codex: final reasoning effort after payload config" && entry.Data["effort"] == "low" {
			return
		}
	}
	t.Fatalf("expected final reasoning effort log with effort=low, got entries=%v", hook.AllEntries())
}
