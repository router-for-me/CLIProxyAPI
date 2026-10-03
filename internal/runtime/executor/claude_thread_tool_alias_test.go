package executor

import (
	"fmt"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClaudeThreadContinuationRestoresTools(t *testing.T) {
	options := claudeMCPAliasOptions{secret: "isolated-caller"}
	create := []byte(`{"thread":{"type":"create"},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"inspect"}]}`)
	state, _, _ := helps.NewClaudeThreadTools(create, options.secret)
	upstream, createMap := prepareClaudeOAuthToolNamesForUpstream(create, options)
	alias := gjson.GetBytes(upstream, "tools.0.name").String()
	state.StoreResponse([]byte(`{"id":"msg-one"}`), createMap)
	continuation := []byte(`{"thread":{"type":"continue","previous_message_id":"msg-one"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-one","content":"ok"}]}]}`)
	_, inherited, found := helps.NewClaudeThreadTools(continuation, options.secret)
	if !found {
		t.Fatal("lost thread state")
	}
	options.inherited = inherited
	_, reverseMap := prepareClaudeOAuthToolNamesForUpstream(continuation, options)
	response := []byte(`{"content":[{"type":"tool_use","id":"tool-two","name":"` + alias + `","input":{}}]}`)
	restored, err := restoreClaudeOAuthToolNamesFromResponse(response, reverseMap)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(restored, "content.0.name").String(); got != "Read" {
		t.Fatalf("continued thread returned unavailable tool %q, want Read", got)
	}
}

func TestClaudeExecutorThreadToolAliases(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "stream"}[stream], func(t *testing.T) {
			var alias string
			messageID := "msg-" + uuid.NewString()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if gjson.GetBytes(body, "thread.type").String() == "create" {
					alias = gjson.GetBytes(body, "tools.0.name").String()
					if alias == "Read" || alias == "" {
						t.Errorf("tool not aliased: %q", alias)
					}
				} else if gjson.GetBytes(body, "tools").Exists() {
					t.Error("continuation unexpectedly sent declarations")
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", messageID)
					fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool-two\",\"name\":%q,\"input\":{}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", alias)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"id":%q,"type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"tool-two","name":%q,"input":{}}],"usage":{"input_tokens":1,"output_tokens":1}}`, messageID, alias)
				}
			}))
			defer server.Close()
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: uuid.NewString(), Attributes: map[string]string{
				"api_key": "sk-ant-oat01-synthetic", "base_url": server.URL,
			}, Metadata: claudeOAuthTestMetadata()}
			payloads := [][]byte{
				[]byte(`{"model":"claude-opus-5-5","max_tokens":32,"thread":{"type":"create"},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"inspect"}]}`),
				[]byte(fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":32,"thread":{"type":"continue","previous_message_id":%q},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-two","content":"ok"}]}]}`, messageID)),
			}
			for _, payload := range payloads {
				result := runClaudeThreadToolRequest(t, executor, auth, payload, stream)
				if !strings.Contains(string(result), `"name":"Read"`) {
					t.Fatalf("client received unavailable tool: %s", result)
				}
				if strings.Contains(string(result), alias) {
					t.Fatal("upstream alias leaked")
				}
			}
		})
	}
}

func runClaudeThreadToolRequest(t *testing.T, executor *ClaudeExecutor,
	auth *cliproxyauth.Auth, payload []byte, stream bool,
) []byte {
	t.Helper()
	req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	if !stream {
		result, err := executor.Execute(t.Context(), auth, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		return result.Payload
	}
	result, err := executor.ExecuteStream(t.Context(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var output []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		output = append(output, chunk.Payload...)
	}
	return output
}
