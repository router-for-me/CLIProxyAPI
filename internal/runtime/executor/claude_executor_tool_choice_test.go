package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestRelaxForcedToolChoiceForModel(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantType   string
		wantName   string
		wantParDis bool
	}{
		{
			name:     "opus-5-5 any",
			body:     `{"model":"claude-opus-5-5","tool_choice":{"type":"any"}}`,
			wantType: "auto",
		},
		{
			name:       "opus-5-5 named tool keeps disable_parallel_tool_use",
			body:       `{"model":"claude-opus-5-5","tool_choice":{"type":"tool","name":"get_weather","disable_parallel_tool_use":true}}`,
			wantType:   "auto",
			wantParDis: true,
		},
		{
			name:     "fable-5-1 variant any",
			body:     `{"model":"claude-fable-5-1-max","tool_choice":{"type":"any"}}`,
			wantType: "auto",
		},
		{
			name:     "prefixed model id",
			body:     `{"model":"anthropic/claude-opus-5-5","tool_choice":{"type":"any"}}`,
			wantType: "auto",
		},
		{
			name:     "opus-5 keeps forced tool",
			body:     `{"model":"claude-opus-5","tool_choice":{"type":"tool","name":"get_weather"}}`,
			wantType: "tool",
			wantName: "get_weather",
		},
		{
			name:     "opus-5-5 none untouched",
			body:     `{"model":"claude-opus-5-5","tool_choice":{"type":"none"}}`,
			wantType: "none",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := relaxForcedToolChoiceForModel([]byte(tc.body))
			if got := gjson.GetBytes(out, "tool_choice.type").String(); got != tc.wantType {
				t.Fatalf("tool_choice.type = %q, want %q: %s", got, tc.wantType, out)
			}
			if got := gjson.GetBytes(out, "tool_choice.name").String(); got != tc.wantName {
				t.Fatalf("tool_choice.name = %q, want %q: %s", got, tc.wantName, out)
			}
			if got := gjson.GetBytes(out, "tool_choice.disable_parallel_tool_use").Bool(); got != tc.wantParDis {
				t.Fatalf("disable_parallel_tool_use = %v, want %v: %s", got, tc.wantParDis, out)
			}
		})
	}
}

func TestRelaxForcedToolChoiceForModel_KeepsThinkingAfterForcedCheck(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"tool_choice":{"type":"any"}}`)
	out := disableThinkingIfToolChoiceForced(relaxForcedToolChoiceForModel(body))

	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive: %s", got, out)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("output_config.effort = %q, want high: %s", got, out)
	}
}

func TestClaudeExecutor_ExecuteRelaxesForcedToolChoiceForOpus55(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"model":"claude-opus-5-5",
		"max_tokens":1024,
		"tools":[{"name":"get_weather","description":"Get weather","input_schema":{"type":"object","properties":{}}}],
		"tool_choice":{"type":"tool","name":"get_weather"},
		"messages":[{"role":"user","content":[{"type":"text","text":"weather?"}]}]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(seenBody) == 0 {
		t.Fatal("expected request body to be captured")
	}
	if got := gjson.GetBytes(seenBody, "tool_choice.type").String(); got != "auto" {
		t.Fatalf("upstream tool_choice.type = %q, want auto: %s", got, seenBody)
	}
	if gjson.GetBytes(seenBody, "tool_choice.name").Exists() {
		t.Fatalf("upstream tool_choice.name should be removed: %s", seenBody)
	}
}
