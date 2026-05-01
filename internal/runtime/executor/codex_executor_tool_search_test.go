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
	"github.com/tidwall/gjson"
)

func TestNormalizeCodexServerToolSearchStripsServerOnlyFields(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type":"tool_search","description":"search local tools","parameters":{"type":"object"}},
			{"type":"tool_search","execution":"server","description":"search server tools","parameters":{"type":"object"}},
			{"type":"tool_search","execution":"client","description":"search client tools","parameters":{"type":"object"}},
			{"type":"function","name":"lookup","description":"keep me","parameters":{"type":"object"}},
			{"type":"tool_search","execution":"server","description":null,"parameters":null},
			{"type":"tool_search","execution":"server","name":"metadata-only"}
		],
		"tool_choice": {
			"type": "allowed_tools",
			"tools": [
				{"type":"tool_search","description":"search allowed tools","parameters":{"type":"object"}}
			]
		}
	}`)

	out := normalizeCodexServerToolSearch(body)

	if gjson.GetBytes(out, "tools.0.description").Exists() {
		t.Fatalf("tools.0.description should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.0.parameters").Exists() {
		t.Fatalf("tools.0.parameters should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.1.description").Exists() {
		t.Fatalf("tools.1.description should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.1.parameters").Exists() {
		t.Fatalf("tools.1.parameters should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.2.description").String(); got != "search client tools" {
		t.Fatalf("client tool_search description = %q, want preserved: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "tools.2.parameters").Exists() {
		t.Fatalf("client tool_search parameters should be preserved: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.3.description").String(); got != "keep me" {
		t.Fatalf("function description = %q, want preserved: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "tools.3.parameters").Exists() {
		t.Fatalf("function parameters should be preserved: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.4.description").Exists() {
		t.Fatalf("null server tool_search description should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.4.parameters").Exists() {
		t.Fatalf("null server tool_search parameters should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.5.name").String(); got != "metadata-only" {
		t.Fatalf("server tool_search without removable fields name = %q, want preserved: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice.tools.0.description").Exists() {
		t.Fatalf("tool_choice.tools.0.description should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "tool_choice.tools.0.parameters").Exists() {
		t.Fatalf("tool_choice.tools.0.parameters should be removed: %s", string(out))
	}
}

func TestCodexExecutorExecuteNormalizesServerToolSearch(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		SDKConfig: config.SDKConfig{
			DisableImageGeneration: config.DisableImageGenerationAll,
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"input":"hello",
			"tools":[
				{"type":"tool_search","description":"server search","parameters":{"type":"object"}},
				{"type":"tool_search","execution":"client","description":"client search","parameters":{"type":"object"}}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gjson.GetBytes(gotBody, "tools.0.description").Exists() {
		t.Fatalf("server tool_search description should be removed: %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "tools.0.parameters").Exists() {
		t.Fatalf("server tool_search parameters should be removed: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.1.description").String(); got != "client search" {
		t.Fatalf("client tool_search description = %q, want preserved: %s", got, string(gotBody))
	}
	if !gjson.GetBytes(gotBody, "tools.1.parameters").Exists() {
		t.Fatalf("client tool_search parameters should be preserved: %s", string(gotBody))
	}
}
