package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A user turn that only holds a file Antigravity cannot receive must be refused
// before any HTTP call, for every entry point and for both execution paths
// (Gemini models and Claude-on-Antigravity models), even after earlier turns.
func TestAntigravityExecutorRefusesAFileOnlyTurnBeforeCallingUpstream(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	const (
		claudeFile = `{"max_tokens":8,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"container_upload","file_id":"file_not_stored"}]}]}`
		openAIFile = `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"file","file":{"file_id":"file-not-stored"}}]}]}`
		// The attachment-only user turn is followed by a developer step that must not hide it.
		interactionsDeveloper = `{"input":[{"type":"user_input","content":[{"type":"text","text":"hello"}]},{"type":"model_output","content":[{"type":"text","text":"hi"}]},{"type":"user_input","content":[{"type":"document","uri":"gs://b/a.pdf"}]},{"type":"user_input","role":"developer","content":[{"type":"text","text":"note"}]}]}`
		// The only new user turn is a remote image URL, so the earlier turn must not be answered in its place.
		openAIImageURL = `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x.test/a.png"}}]}]}`
	)
	cases := []struct {
		name     string
		model    string
		source   string
		payload  string
		wantPart string
	}{
		{"claude source to gemini model", "gemini-3.7-flash", "claude", claudeFile, "container_upload"},
		{"claude source to claude model", "claude-sonnet-4-5", "claude", claudeFile, "container_upload"},
		{"openai source to gemini model", "gemini-3.7-flash", "openai", openAIFile, "file"},
		{"openai image_url source to gemini model", "gemini-3.7-flash", "openai", openAIImageURL, "image_url"},
		{"interactions developer step to gemini model", "gemini-3.7-flash", "interactions", interactionsDeveloper, "document"},
		{"openai source to claude model", "claude-sonnet-4-5", "openai", openAIFile, "file"},
	}
	for _, tc := range cases {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"access_token": "token-123",
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-1",
			},
			Attributes: map[string]string{"base_url": server.URL},
		}
		exec := NewAntigravityExecutor(&config.Config{})
		req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload)}
		opts := func(stream bool) cliproxyexecutor.Options {
			return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(tc.source), Stream: stream}
		}
		calls := map[string]func() error{
			"Execute": func() error {
				_, err := exec.Execute(context.Background(), auth, req, opts(false))
				return err
			},
			"ExecuteStream": func() error {
				_, err := exec.ExecuteStream(context.Background(), auth, req, opts(true))
				return err
			},
			"CountTokens": func() error {
				_, err := exec.CountTokens(context.Background(), auth, req, opts(false))
				return err
			},
		}
		for method, call := range calls {
			err := call()
			var part *translatorcommon.UnsupportedPartError
			if !errors.As(err, &part) || part.Type != tc.wantPart {
				t.Errorf("%s %s: err = %v, want unsupported %s", tc.name, method, err, tc.wantPart)
			}
		}
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream was called %d times, want 0", got)
	}
}
