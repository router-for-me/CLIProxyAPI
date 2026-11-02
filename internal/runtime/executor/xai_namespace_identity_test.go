package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAINamespaceIdentityStableAcrossStream(t *testing.T) {
	refs := map[string]xaiNamespaceToolRef{
		"math_tools__add_numbers":   {namespace: "math_tools", name: "add_numbers"},
		"other_tools__add_numbers":  {namespace: "other_tools", name: "add_numbers"},
		"math__tools__add__numbers": {namespace: "math__tools", name: "add__numbers"},
	}
	for _, tc := range []struct {
		upstream  string
		name      string
		namespace string
	}{
		{"math_tools__add_numbers", "add_numbers", "math_tools"},
		{"other_tools__add_numbers", "add_numbers", "other_tools"},
		{"math__tools__add__numbers", "add__numbers", "math__tools"},
		{"flat__add_numbers", "flat__add_numbers", ""},
	} {
		t.Run(tc.upstream, func(t *testing.T) {
			restorer := newXAINamespaceRestorer(refs)
			for _, eventType := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
				arguments := `{"a":2,"b":3}`
				status := "completed"
				if eventType == "response.output_item.added" {
					arguments = ""
					status = "in_progress"
				}
				item := fmt.Sprintf(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":%q,"arguments":%q,"status":%q}`, tc.upstream, arguments, status)
				event := fmt.Sprintf(`{"type":%q,"output_index":0,"item":%s}`, eventType, item)
				path := "item"
				if eventType == "response.completed" {
					event = fmt.Sprintf(`{"type":%q,"response":{"output":[%s]}}`, eventType, item)
					path = "response.output.0"
				}
				restored := restorer.restore([]byte(event))
				got := gjson.GetBytes(restored, path)
				if got.Get("name").String() != tc.name || got.Get("namespace").String() != tc.namespace {
					t.Errorf("%s identity = (%q, %q), want (%q, %q)", eventType, got.Get("namespace").String(), got.Get("name").String(), tc.namespace, tc.name)
				}
				if tc.namespace == "" && got.Get("namespace").Exists() {
					t.Errorf("%s added a namespace to a flat tool: %s", eventType, got.Raw)
				}
				for _, field := range []string{"type", "id", "call_id", "arguments", "status"} {
					if got.Get(field).Raw != gjson.Get(item, field).Raw {
						t.Errorf("%s changed %s: got %s, want %s", eventType, field, got.Get(field).Raw, gjson.Get(item, field).Raw)
					}
				}
				if eventType != "response.completed" && gjson.GetBytes(restored, "output_index").Raw != "0" {
					t.Errorf("%s changed output_index: %s", eventType, restored)
				}
			}
		})
	}
}

func TestXAIExecutorStreamNamespaceIdentity(t *testing.T) {
	for _, tc := range []struct {
		label     string
		tools     string
		upstream  string
		name      string
		namespace string
	}{
		{
			label:     "namespace",
			tools:     `[{"type":"namespace","name":"math_tools","tools":[{"type":"function","name":"add_numbers","parameters":{"type":"object"}}]}]`,
			upstream:  "math_tools__add_numbers",
			name:      "add_numbers",
			namespace: "math_tools",
		},
		{
			label:    "flat",
			tools:    `[{"type":"function","name":"math_tools__add_numbers","parameters":{"type":"object"}}]`,
			upstream: "math_tools__add_numbers",
			name:     "math_tools__add_numbers",
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read upstream request: %v", err)
					return
				}
				if got := gjson.GetBytes(body, "tools.0.name").String(); got != tc.upstream {
					t.Errorf("upstream tool name = %q, want %q", got, tc.upstream)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				item := fmt.Sprintf(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":%q,"arguments":""}`, tc.upstream)
				fmt.Fprintf(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%s}\n\n", item)
				item = fmt.Sprintf(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":%q,"arguments":%q}`, tc.upstream, `{"a":2,"b":3}`)
				fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", item)
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[%s]}}\n\n", item)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			exec := NewXAIExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider:   "xai",
				Attributes: map[string]string{"base_url": server.URL},
				Metadata:   map[string]any{"access_token": "fixture-token"},
			}
			result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "grok-4.7",
				Payload: []byte(fmt.Sprintf(`{"model":"grok-4.7","tools":%s,"input":[{"role":"user","content":"call tool"}]}`, tc.tools)),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAIResponse,
				ResponseFormat: sdktranslator.FormatOpenAIResponse,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			seen := map[string]bool{}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
				for _, line := range strings.Split(string(chunk.Payload), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					event := gjson.Parse(strings.TrimPrefix(line, "data: "))
					eventType := event.Get("type").String()
					path := "item"
					if eventType == "response.completed" {
						path = "response.output.0"
					} else if eventType != "response.output_item.added" && eventType != "response.output_item.done" {
						continue
					}
					seen[eventType] = true
					item := event.Get(path)
					if item.Get("name").String() != tc.name || item.Get("namespace").String() != tc.namespace {
						t.Errorf("%s identity = %s, want namespace=%q name=%q", eventType, item.Raw, tc.namespace, tc.name)
					}
					if tc.namespace == "" && item.Get("namespace").Exists() {
						t.Errorf("%s added a namespace to a flat tool: %s", eventType, item.Raw)
					}
					wantArguments := `{"a":2,"b":3}`
					if eventType == "response.output_item.added" {
						wantArguments = ""
					}
					if item.Get("id").String() != "fc_1" || item.Get("call_id").String() != "call_1" || item.Get("arguments").String() != wantArguments {
						t.Errorf("%s changed call payload: %s", eventType, item.Raw)
					}
				}
			}
			for _, eventType := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
				if !seen[eventType] {
					t.Errorf("missing SSE event %s", eventType)
				}
			}
		})
	}
}
