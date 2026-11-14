package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

var claudeIssue7389PropertyKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)

const claudeIssue7389Schema = `{"type":"object","properties":{"not[labels]":{"type":"string","description":"Labels to exclude"},"state":{"type":"string"}},"required":["not[labels]"]}`

const claudeIssue7389ProjectSchema = `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`

var claudeIssue7389ResponsesPayload = `{"model":"claude-opus-5-5","input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"list"}]},` +
	`{"type":"function_call","call_id":"call_1","name":"list_merge_requests","arguments":"{\"not[labels]\":\"bug\",\"state\":\"opened\"}"},` +
	`{"type":"function_call_output","call_id":"call_1","output":"[]"},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}],` +
	`"tools":[{"type":"function","name":"list_merge_requests","description":"List merge requests.","parameters":` + claudeIssue7389Schema + `},` +
	`{"type":"function","name":"get_project","description":"Get a project.","parameters":` + claudeIssue7389ProjectSchema + `}]}`

var claudeIssue7389ClaudePayload = `{"model":"claude-opus-5-5","max_tokens":256,"messages":[` +
	`{"role":"user","content":"list"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"list_merge_requests","input":{"not[labels]":"bug","state":"opened"}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"[]"},{"type":"text","text":"again"}]}],` +
	`"tools":[{"name":"list_merge_requests","description":"List merge requests.","strict":true,"input_schema":` + claudeIssue7389Schema + `},` +
	`{"name":"get_project","description":"Get a project.","input_schema":` + claudeIssue7389ProjectSchema + `}]}`

func TestClaudeIssue7389RejectedPropertyNamesDoNotBreakRequests(t *testing.T) {
	sources := []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{name: "openai responses", format: sdktranslator.FormatOpenAIResponse, payload: claudeIssue7389ResponsesPayload},
		{name: "claude", format: sdktranslator.FormatClaude, payload: claudeIssue7389ClaudePayload},
	}
	auths := []struct {
		name  string
		attrs map[string]string
	}{
		{name: "api key", attrs: map[string]string{"api_key": "key-7389"}},
		{name: "claude code mcp alias", attrs: map[string]string{"api_key": "key-7389-cli", "fingerprint_profile": "claude-code-cli", "cloak_mode": "always"}},
	}
	for _, authCase := range auths {
		for _, source := range sources {
			for _, stream := range []bool{false, true} {
				name := authCase.name + " " + source.name
				if stream {
					name += " stream"
				}
				t.Run(name, func(t *testing.T) {
					claudeIssue7389RunRoundTrip(t, authCase.attrs, source.format, source.payload, stream)
				})
			}
		}
	}
}

func claudeIssue7389RunRoundTrip(t *testing.T, attrs map[string]string, format sdktranslator.Format, sourcePayload string, stream bool) {
	t.Helper()
	var upstreamBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(req.Body)
		toolName := claudeIssue7389Tool(upstreamBody, "List merge requests.").Get("name").String()
		contentType := "application/json"
		body := claudeIssue7389JSONResponse(toolName)
		if ojson.GetBytes(upstreamBody, "stream").Bool() {
			contentType = "text/event-stream"
			body = claudeIssue7389SSEResponse(toolName)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: attrs}
	payload := sourcePayload
	if stream && format == sdktranslator.FormatClaude {
		payload = strings.Replace(payload, `"max_tokens":256,`, `"max_tokens":256,"stream":true,`, 1)
	}
	request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(payload)}
	options := cliproxyexecutor.Options{SourceFormat: format, Stream: stream}

	var output []byte
	if stream {
		result, err := executor.ExecuteStream(ctx, auth, request, options)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
			output = append(output, chunk.Payload...)
			output = append(output, '\n')
		}
	} else {
		resp, err := executor.Execute(ctx, auth, request, options)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		output = resp.Payload
	}

	claudeIssue7389AssertUpstream(t, upstreamBody)
	if attrs["fingerprint_profile"] != "" && claudeIssue7389Tool(upstreamBody, "List merge requests.").Get("name").String() == "list_merge_requests" {
		t.Fatalf("expected an MCP tool alias upstream: %s", ojson.GetBytes(upstreamBody, "tools").Raw)
	}
	args := claudeIssue7389ClientArguments(t, format, stream, output)
	fields := claudeIssue7389ObjectFields(args)
	if fields["not[labels]"] != `"feature"` || fields["state"] != `"closed"` || len(fields) != 2 {
		t.Fatalf("client tool arguments = %s, want the original property names; output=%s", args, output)
	}
}

func TestClaudeIssue7389CountTokensDoesNotSendRejectedPropertyNames(t *testing.T) {
	var upstreamBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamBody, _ = io.ReadAll(req.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":12}`)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-7389"}}
	request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(claudeIssue7389ClaudePayload)}
	if _, err := executor.CountTokens(ctx, auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	claudeIssue7389AssertUpstream(t, upstreamBody)
}

// User payload rules are the final semantic barrier: values they configure
// must reach upstream exactly as configured.
func TestClaudeIssue7389PayloadRulesRunAfterSchemaFallback(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var upstreamBody []byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamBody, _ = io.ReadAll(req.Body)
				body := claudeIssue7389SSEResponse("list_merge_requests")
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			models := []config.PayloadModelRule{{Name: "claude-opus-5-5"}}
			cfg := &config.Config{Payload: config.PayloadConfig{
				Override:    []config.PayloadRule{{Models: models, Params: map[string]any{"tools.0.description": "configured"}}},
				OverrideRaw: []config.PayloadRule{{Models: models, Params: map[string]any{"tools.0.input_schema": `{"type":"object","properties":{"configured[x]":{"type":"string"}}}`}}},
			}}
			executor := NewClaudeExecutor(cfg)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-7389"}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(claudeIssue7389ResponsesPayload)}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				result, err := executor.ExecuteStream(ctx, auth, request, options)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, err := executor.Execute(ctx, auth, request, options); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := ojson.GetBytes(upstreamBody, "tools.0.input_schema").Raw; got != `{"type":"object","properties":{"configured[x]":{"type":"string"}}}` {
				t.Fatalf("configured input_schema = %s, want the payload rule value", got)
			}
			if got := ojson.GetBytes(upstreamBody, "tools.0.description").String(); got != "configured" {
				t.Fatalf("configured description = %q, want the payload rule value", got)
			}
		})
	}
}

// claudeIssue7389Tool returns the upstream declaration whose description
// starts with prefix, whatever name aliasing the request path applied.
func claudeIssue7389Tool(body []byte, prefix string) ojson.Result {
	var found ojson.Result
	ojson.GetBytes(body, "tools").ForEach(func(_, tool ojson.Result) bool {
		if strings.HasPrefix(tool.Get("description").String(), prefix) {
			found = tool
			return false
		}
		return true
	})
	return found
}

// claudeIssue7389AssertPropertyKeys checks every properties map in a schema.
func claudeIssue7389AssertPropertyKeys(t *testing.T, node ojson.Result, propertyMap bool) {
	t.Helper()
	if node.Type != ojson.JSON {
		return
	}
	object := node.IsObject()
	node.ForEach(func(key, value ojson.Result) bool {
		if object && propertyMap && !claudeIssue7389PropertyKeyPattern.MatchString(key.String()) {
			t.Fatalf("upstream property key %q does not match Claude pattern", key.String())
		}
		claudeIssue7389AssertPropertyKeys(t, value, object && !propertyMap && key.String() == "properties")
		return true
	})
}

func claudeIssue7389AssertUpstream(t *testing.T, body []byte) {
	t.Helper()
	if len(body) == 0 {
		t.Fatal("upstream request was not captured")
	}
	ojson.GetBytes(body, "tools").ForEach(func(_, tool ojson.Result) bool {
		claudeIssue7389AssertPropertyKeys(t, tool.Get("input_schema"), false)
		return true
	})

	reported := claudeIssue7389Tool(body, "List merge requests.")
	if got := reported.Get("input_schema").Raw; got != `{"type":"object"}` {
		t.Fatalf("reported tool input_schema = %s, want an open object", got)
	}
	if reported.Get("strict").Exists() {
		t.Fatalf("reported tool keeps strict: %s", reported.Raw)
	}
	description := reported.Get("description").String()
	if !strings.Contains(description, `"not[labels]":{"type":"string","description":"Labels to exclude"}`) || !strings.Contains(description, `"required":["not[labels]"]`) {
		t.Fatalf("reported tool description does not carry the original schema: %q", description)
	}

	project := claudeIssue7389Tool(body, "Get a project.")
	if got := project.Get("description").String(); got != "Get a project." {
		t.Fatalf("valid tool description = %q, want unchanged", got)
	}
	if !project.Get("input_schema.properties.id").Exists() || project.Get("input_schema.required.0").String() != "id" {
		t.Fatalf("valid tool input_schema changed: %s", project.Get("input_schema").Raw)
	}

	foundHistory := false
	ojson.GetBytes(body, "messages").ForEach(func(_, message ojson.Result) bool {
		message.Get("content").ForEach(func(_, block ojson.Result) bool {
			if block.Get("type").String() != "tool_use" {
				return true
			}
			foundHistory = true
			fields := claudeIssue7389ObjectFields([]byte(block.Get("input").Raw))
			if fields["not[labels]"] != `"bug"` || fields["state"] != `"opened"` || len(fields) != 2 {
				t.Fatalf("upstream history tool_use input = %s, want the original arguments", block.Get("input").Raw)
			}
			return true
		})
		return true
	})
	if !foundHistory {
		t.Fatalf("upstream history tool_use not found: %s", body)
	}
}

func claudeIssue7389ObjectFields(raw []byte) map[string]string {
	fields := make(map[string]string)
	ojson.ParseBytes(raw).ForEach(func(k, v ojson.Result) bool {
		fields[strings.Clone(k.String())] = strings.Clone(v.Raw)
		return true
	})
	return fields
}

func claudeIssue7389JSONResponse(toolName string) string {
	return `{"id":"msg_7389","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_2","name":` +
		string(ojson.AppendJSONString(nil, toolName)) + `,"input":{"not[labels]":"feature","state":"closed"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
}

func claudeIssue7389SSEResponse(toolName string) string {
	var b strings.Builder
	writeEvent := func(event, data string) {
		b.WriteString("event: " + event + "\ndata: " + data + "\n\n")
	}
	writeEvent("message_start", `{"type":"message_start","message":{"id":"msg_7389","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
	writeEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":`+string(ojson.AppendJSONString(nil, toolName))+`,"input":{}}}`)
	for _, fragment := range []string{"", `{"not[la`, `bels]": "fea`, `ture", "state"`, `: "closed"}`} {
		writeEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(ojson.AppendJSONString(nil, fragment))+`}}`)
	}
	writeEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
	writeEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":1}}`)
	writeEvent("message_stop", `{"type":"message_stop"}`)
	return b.String()
}

// claudeIssue7389ClientArguments extracts the final tool arguments the client sees.
func claudeIssue7389ClientArguments(t *testing.T, format sdktranslator.Format, stream bool, output []byte) []byte {
	t.Helper()
	switch {
	case format == sdktranslator.FormatClaude && !stream:
		return []byte(ojson.GetBytes(output, `content.#(type=="tool_use").input`).Raw)
	case format == sdktranslator.FormatClaude && stream:
		var args strings.Builder
		for _, line := range bytes.Split(output, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			event := ojson.ParseBytes(bytes.TrimSpace(line[len("data:"):]))
			if event.Get("delta.type").String() == "input_json_delta" {
				args.WriteString(event.Get("delta.partial_json").String())
			}
		}
		return []byte(args.String())
	case !stream:
		return []byte(ojson.GetBytes(output, `output.#(type=="function_call").arguments`).String())
	default:
		for _, line := range bytes.Split(output, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			event := ojson.ParseBytes(bytes.TrimSpace(line[len("data:"):]))
			if event.Get("type").String() == "response.function_call_arguments.done" {
				return []byte(event.Get("arguments").String())
			}
		}
	}
	t.Fatalf("tool arguments not found in client output: %s", output)
	return nil
}
