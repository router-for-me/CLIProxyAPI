package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

var issue6478Calls = []struct {
	id   string
	name string
}{
	{id: "Write:1#e1f0da81143649f3a378d24af6fc3ec9", name: "Write"},
	{id: "PowerShell:66#cd50350578c34275a26a09a477a81ffb", name: "PowerShell"},
	// A lossy replacement would collide with the first call's already legal ID.
	{id: "Write_1_e1f0da81143649f3a378d24af6fc3ec9", name: "Read"},
	{id: "call_8jAZYBK5veMdr1DD2DqObARK", name: "Bash"},
}

func issue6478History(ids []string) []byte {
	var uses, results, tools []string
	for i, call := range issue6478Calls {
		uses = append(uses, fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":{"literal_id":%q}}`, ids[i], call.name, call.id))
		results = append(results, fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"content":%q}`, ids[i], "result for "+call.id))
		tools = append(tools, fmt.Sprintf(`{"name":%q,"input_schema":{"type":"object","properties":{"literal_id":{"type":"string"}}}}`, call.name))
	}
	return []byte(fmt.Sprintf(`{"model":"claude-opus-5","max_tokens":64,"tools":[%s],"messages":[{"role":"user","content":"run tools"},{"role":"assistant","content":[%s]},{"role":"user","content":[%s]}]}`, strings.Join(tools, ","), strings.Join(uses, ","), strings.Join(results, ",")))
}

func issue6478AssertPortableIDs(t *testing.T, ids []string) {
	t.Helper()
	if len(ids) != len(issue6478Calls) {
		t.Fatalf("tool IDs = %q, want %d calls", ids, len(issue6478Calls))
	}
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	seen := make(map[string]bool)
	for i, id := range ids {
		if !valid.MatchString(id) {
			t.Errorf("tool_use.id[%d] = %q violates Anthropic pattern ^[a-zA-Z0-9_-]+$", i, id)
		}
		if seen[id] {
			t.Errorf("distinct upstream calls collided at tool_use.id[%d] = %q", i, id)
		}
		seen[id] = true
		if i >= 2 && id != issue6478Calls[i].id {
			t.Errorf("already legal tool_use.id[%d] = %q, want unchanged %q", i, id, issue6478Calls[i].id)
		}
	}
}

// Exercise both public execution paths, then replay the client-visible IDs back
// through the real Claude-to-Devin request builder and inspect the wire payload.
func TestIssue6478_DevinClaudeOutputAndToolResultRoundTrip(t *testing.T) {
	var frames []byte
	for i, call := range issue6478Calls {
		tool := appendDevinFieldBytes(nil, 1, []byte(call.id))
		tool = appendDevinFieldBytes(tool, 2, []byte(call.name))
		tool = appendDevinFieldBytes(tool, 3, []byte(`{}`))
		tool = appendVarintField(tool, 4, uint64(i))
		frame := appendDevinFieldBytes(nil, 6, tool)
		frames = append(frames, helps.WrapConnectEnvelope(frame)...)
	}
	frames = append(frames, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)

	var idsByMode [][]string
	for _, stream := range []bool{false, true} {
		name := "Execute"
		if stream {
			name = "ExecuteStream"
		}
		t.Run(name, func(t *testing.T) {
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, wire, errRead := helps.ReadConnectFrame(r.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- wire
				w.Header().Set("Content-Type", "application/connect+proto")
				if _, errWrite := w.Write(frames); errWrite != nil {
					t.Errorf("write upstream response: %v", errWrite)
				}
			}))
			defer server.Close()

			cfg := &config.Config{}
			executor := NewDevinExecutor(cfg)
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "devin", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
			payload := []byte(`{"model":"devin/swe-2","max_tokens":64,"messages":[{"role":"user","content":"run tools"}]}`)
			req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Stream: stream}
			var ids []string
			if stream {
				result, errStream := executor.ExecuteStream(t.Context(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk: %v", chunk.Err)
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						event := gjson.Parse(strings.TrimPrefix(line, "data: "))
						if event.Get("type").String() == "content_block_start" && event.Get("content_block.type").String() == "tool_use" {
							ids = append(ids, event.Get("content_block.id").String())
						}
					}
				}
			} else {
				result, errExecute := executor.Execute(t.Context(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}
				for _, block := range gjson.GetBytes(result.Payload, "content").Array() {
					if block.Get("type").String() == "tool_use" {
						ids = append(ids, block.Get("id").String())
					}
				}
			}
			<-requests
			issue6478AssertPortableIDs(t, ids)
			idsByMode = append(idsByMode, ids)

			cfg.Payload = config.PayloadConfig{Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{
					"prompts.1.tool_calls.0.id": issue6478Calls[0].id,
					"prompts.2.tool_call_id":    issue6478Calls[0].id,
				}}}},
				Params: map[string]any{"system_prompt": "observed original Devin IDs"},
			}}}
			req.Payload = issue6478History(ids)
			opts.OriginalRequest = req.Payload
			opts.Stream = false
			if _, errReplay := executor.Execute(t.Context(), auth, req, opts); errReplay != nil {
				t.Fatalf("replay tool results: %v", errReplay)
			}
			_, native, errDecode := helps.FinalizeDevinPayload(<-requests, func(body []byte) []byte { return body })
			if errDecode != nil {
				t.Fatalf("decode sent Devin payload: %v", errDecode)
			}
			if gjson.GetBytes(native, "system_prompt").String() != "observed original Devin IDs" {
				t.Errorf("Devin payload condition did not observe original call/result IDs before final barrier")
			}
			var sentCalls, sentResults []string
			for _, prompt := range gjson.GetBytes(native, "prompts").Array() {
				for _, call := range prompt.Get("tool_calls").Array() {
					sentCalls = append(sentCalls, call.Get("id").String())
				}
				if prompt.Get("source").Int() == 4 {
					sentResults = append(sentResults, prompt.Get("tool_call_id").String())
				}
			}
			for i, call := range issue6478Calls {
				if i >= len(sentCalls) || i >= len(sentResults) || sentCalls[i] != call.id || sentResults[i] != call.id {
					t.Errorf("Devin replay[%d] must restore original ID %q for call and result; calls=%q results=%q", i, call.id, sentCalls, sentResults)
				}
			}
		})
	}
	if len(idsByMode) == 2 && !slices.Equal(idsByMode[0], idsByMode[1]) {
		t.Errorf("stream/non-stream IDs differ: %q vs %q", idsByMode[0], idsByMode[1])
	}
}

func TestIssue6478_ClaudeReplaysLegacyToolIDs(t *testing.T) {
	var legacyIDs []string
	for _, call := range issue6478Calls {
		legacyIDs = append(legacyIDs, call.id)
	}
	var idsByMode [][]string
	for _, stream := range []bool{false, true} {
		name := "Execute"
		if stream {
			name = "ExecuteStream"
		}
		t.Run(name, func(t *testing.T) {
			body := executeClaudeContextManagementRequest(t, &config.Config{}, issue6478History(legacyIDs), stream)
			var useIDs, resultIDs []string
			for _, message := range gjson.GetBytes(body, "messages").Array() {
				for _, block := range message.Get("content").Array() {
					switch block.Get("type").String() {
					case "tool_use":
						i := len(useIDs)
						useIDs = append(useIDs, block.Get("id").String())
						if i < len(issue6478Calls) && block.Get("input.literal_id").String() != issue6478Calls[i].id {
							t.Errorf("tool input was rewritten along with ID: %s", block.Raw)
						}
					case "tool_result":
						i := len(resultIDs)
						id := block.Get("tool_use_id").String()
						resultIDs = append(resultIDs, id)
						if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) {
							t.Errorf("sent tool_result.tool_use_id[%d] = %q violates Anthropic pattern ^[a-zA-Z0-9_-]+$", i, id)
						}
						if i < len(issue6478Calls) && block.Get("content").String() != "result for "+issue6478Calls[i].id {
							t.Errorf("tool result content was rewritten along with ID: %s", block.Raw)
						}
					}
				}
			}
			issue6478AssertPortableIDs(t, useIDs)
			if !slices.Equal(useIDs, resultIDs) {
				t.Errorf("sent tool_use and tool_result IDs must pair: uses=%q results=%q", useIDs, resultIDs)
			}
			idsByMode = append(idsByMode, useIDs)
		})
	}
	if len(idsByMode) == 2 && !slices.Equal(idsByMode[0], idsByMode[1]) {
		t.Errorf("legacy history repair differs across stream modes: %q vs %q", idsByMode[0], idsByMode[1])
	}
}

func TestIssue6478_ClaudeToolIDRepairPrecedesPayloadRules(t *testing.T) {
	var legacyIDs []string
	for _, call := range issue6478Calls {
		legacyIDs = append(legacyIDs, call.id)
	}
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "*", Exist: []string{"messages.1.content.0.id"}, NotMatch: []map[string]any{{"messages.1.content.0.id": issue6478Calls[0].id}}}},
			Params: map[string]any{"temperature": 0.25},
		}, {
			Models: []config.PayloadModelRule{{Name: "*"}},
			Params: map[string]any{"messages.1.content.0.id": "configured:id#override", "messages.2.content.0.tool_use_id": "configured:id#override"},
		}},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"messages.1.content.1.id", "messages.2.content.1.tool_use_id"}}},
	}}
	for _, stream := range []bool{false, true} {
		name := "Execute"
		if stream {
			name = "ExecuteStream"
		}
		t.Run(name, func(t *testing.T) {
			body := executeClaudeContextManagementRequest(t, cfg, issue6478History(legacyIDs), stream)
			if gjson.GetBytes(body, "temperature").Float() != 0.25 {
				t.Errorf("payload condition did not observe repaired tool ID before final barrier")
			}
			for _, path := range []string{"messages.1.content.0.id", "messages.2.content.0.tool_use_id"} {
				if got := gjson.GetBytes(body, path).String(); got != "configured:id#override" {
					t.Errorf("final user override at %s was changed: got %q", path, got)
				}
			}
			for _, path := range []string{"messages.1.content.1.id", "messages.2.content.1.tool_use_id"} {
				if gjson.GetBytes(body, path).Exists() {
					t.Errorf("final user filter at %s was undone", path)
				}
			}
		})
	}
}
