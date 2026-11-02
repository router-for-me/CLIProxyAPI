package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeMCPBareAliasResponsePaths(t *testing.T) {
	const alias, bare, original = "mcp__alpha_beta__word_shell", "word_shell", "ExactShell"
	reverse := map[string]string{alias: original}
	proof := map[string]string{bare: original}
	blocks := []string{
		`{"type":"tool_use","id":"call","name":"word_shell","input":{"name":"word_shell","number":1e2,"text":"mcp__alpha_beta__word_shell"}}`,
		`{"type":"tool_reference","tool_name":"word_shell","other":"word_shell"}`,
		`{"type":"tool_result","content":[{"type":"tool_reference","tool_name":"word_shell"}],"is_error":false}`,
		`{"type":"tool_search_tool_result","content":{"tool_references":[{"type":"tool_reference","tool_name":"word_shell"}]}}`,
	}
	for i, block := range blocks {
		t.Run(fmt.Sprintf("json-%d", i), func(t *testing.T) {
			body := []byte(` { "type":"message", "content" : [` + block + `], "text":"word_shell" } `)
			key := `"tool_name":"word_shell"`
			if i == 0 {
				key = `"name":"word_shell"`
			}
			want := bytes.Replace(body, []byte(key), []byte(strings.Replace(key, bare, original, 1)), 1)
			got, err := reverseRemapOAuthToolNames(body, reverse, proof)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("got %s, err %v; want exact name-only replacement %s", got, err, want)
			}
			without, err := reverseRemapOAuthToolNames(body, reverse, nil)
			if err != nil || !bytes.Equal(without, body) {
				t.Fatalf("no proof changed bytes: %s, %v", without, err)
			}
		})
		if i == 2 {
			continue
		} // The existing SSE walker does not visit tool_result.
		t.Run(fmt.Sprintf("sse-%d", i), func(t *testing.T) {
			line := []byte(" \tdata:\t" + `{"type":"content_block_start","index":0,"content_block":` + block + "}\r\n")
			key := `"tool_name":"word_shell"`
			if i == 0 {
				key = `"name":"word_shell"`
			}
			want := bytes.Replace(line, []byte(key), []byte(strings.Replace(key, bare, original, 1)), 1)
			got, err := reverseRemapOAuthToolNamesFromStreamLine(line, reverse, proof)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("got %q, err %v; want %q", got, err, want)
			}
		})
	}
	for _, name := range []string{"unknown_shell", "shell", "other_shell", "wordshell", "extra_word_shell", "word_shell_more", "word_shell<end>", "WORD_shell", " word_shell", "word_shell ", "mcp__word_shell", "mcp__unknown__word_shell", "prefix_word_shell", "word_shell_suffix"} {
		t.Run(name, func(t *testing.T) {
			block := fmt.Sprintf(`{"type":"tool_use","name":%q,"input":{"keep":1e2}}`, name)
			body := []byte(` { "type":"message", "content":[` + block + `] } `)
			got, err := reverseRemapOAuthToolNames(body, reverse, proof)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("non-match changed bytes: %s, %v", got, err)
			}
			line := []byte("data:\t" + `{"type":"content_block_start","index":0,"content_block":` + block + "}\r")
			got, err = reverseRemapOAuthToolNamesFromStreamLine(line, reverse, proof)
			if err != nil || !bytes.Equal(got, line) {
				t.Fatalf("non-match changed SSE bytes: %s, %v", got, err)
			}
		})
	}
}

func TestClaudeMCPBareAliasResponseBoundary(t *testing.T) {
	const bare, alias = "word_shell", "mcp__alpha_beta__word_shell"
	reverse := map[string]string{alias: "ExactShell"}
	proof := map[string]string{bare: "ExactShell"}
	blocks := []string{
		`{"type":"tool_use","name":"word_shell","name":"other"}`,
		`{"type":"tool_use","name":"other","name":"word_shell"}`,
		`{"type":"tool_use","name":"word_shell","na\u006de":"other"}`,
		`{"type":"tool_use","Name":"other","name":"word_shell"}`,
		`{"type":"tool_use","type":"text","name":"word_shell"}`,
		`{"type":"tool_use","Type":"tool_use","name":"word_shell"}`,
		`{"type":"tool_use","ty\u0070e":"tool_use","name":"word_shell"}`,
		`{"type":"tool_use","name":123}`,
		`{"type":"tool_use","name":null}`,
		`{"type":"tool_reference","tool_name":"word_shell","Tool_Name":"other"}`,
		`{"type":"tool_result","content":[],"content":[{"type":"tool_reference","tool_name":"word_shell"}]}`,
		`{"type":"tool_result","content":[{"type":"tool_reference","tool_name":"word_shell","tool_name":"other"}]}`,
		`{"type":"tool_search_tool_result","content":{"tool_references":[{"type":"tool_reference","tool_name":"word_shell"}],"Tool_References":[]}}`,
		`{"type":"tool_search_tool_result","content":{"tool_references":[{"type":"tool_reference","tool_name":"word_shell","Type":"tool_reference"}]}}`,
	}
	for i, block := range blocks {
		t.Run(fmt.Sprintf("block-%d", i), func(t *testing.T) {
			body := []byte(` {"type":"message","content":[` + block + `],"number":1e2} `)
			got, err := reverseRemapOAuthToolNames(body, reverse, proof)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("ambiguous body changed: %s, %v", got, err)
			}
			line := []byte("data:\t" + `{"type":"content_block_start","index":0,"content_block":` + block + "}\r")
			got, err = reverseRemapOAuthToolNamesFromStreamLine(line, reverse, proof)
			if err != nil || !bytes.Equal(got, line) {
				t.Fatalf("ambiguous SSE changed: %s, %v", got, err)
			}
		})
	}
	jsonBase := `{"type":"message","content":[{"type":"tool_use","name":"word_shell","input":{}}]}`
	sseBase := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"word_shell","input":{}}}`
	for _, stream := range []bool{false, true} {
		base, field := jsonBase, "content"
		if stream {
			base, field = sseBase, "content_block"
		}
		bad := []string{
			base[:len(base)-1],
			strings.Replace(base, `"type":`, `"Type":"wrong","type":`, 1),
			strings.Replace(base, `"type":`, `"ty\u0070e":"wrong","type":`, 1),
			strings.Replace(base, `"`+field+`":`, `"`+field+`":null,"`+field+`":`, 1),
			strings.Replace(base, `"`+field+`":`, `"`+strings.ToUpper(field)+`":null,"`+field+`":`, 1),
			strings.Replace(base, `"`+field+`":`, `"\u0063`+field[1:]+`":null,"`+field+`":`, 1),
		}
		if stream {
			for _, value := range []string{`"0"`, `-1`, `0.5`, `1e2`, `null`} {
				bad = append(bad, strings.Replace(base, `"index":0`, `"index":`+value, 1))
			}
			bad = append(bad, strings.Replace(base, `"index":0,`, ``, 1), strings.Replace(base, `"index":0`, `"index":0,"index":1`, 1))
		}
		for i, body := range bad {
			t.Run(fmt.Sprintf("envelope-%v-%d", stream, i), func(t *testing.T) {
				input := []byte(body)
				var got []byte
				var err error
				if stream {
					input = append([]byte("data: "), input...)
					got, err = reverseRemapOAuthToolNamesFromStreamLine(input, reverse, proof)
				} else {
					got, err = reverseRemapOAuthToolNames(input, reverse, proof)
				}
				if err != nil || !bytes.Equal(input, got) {
					t.Fatalf("bad envelope changed: %s, %v", got, err)
				}
			})
		}
	}
	// Additional bare guards must not tighten the pre-existing full-alias path.
	for _, body := range []string{
		`{"content":[{"type":"tool_use","name":"mcp__alpha_beta__word_shell"}]}`,
		`{"type":"message","content":[{"type":"tool_use","name":"mcp__alpha_beta__word_shell","Name":"other"}]}`,
	} {
		without, errOld := reverseRemapOAuthToolNames([]byte(body), reverse, nil)
		with, errNew := reverseRemapOAuthToolNames([]byte(body), reverse, proof)
		if errOld != nil || errNew != nil || !bytes.Equal(without, with) || gjson.GetBytes(with, "content.0.name").String() != "ExactShell" {
			t.Fatalf("legacy path changed: %s / %s, %v / %v", without, with, errOld, errNew)
		}
	}
}

func TestClaudeMCPBareAliasDeclinedProofBytes(t *testing.T) {
	const alias = "mcp__alpha_beta__word_shell"
	pre := []byte(`{"tools":[{"name":"ExactShell","input_schema":{}}],"messages":[{"role":"user","content":"hello"}]}`)
	final := bytes.Replace(pre, []byte(`"name":"ExactShell"`), []byte(`"name":"`+alias+`"`), 1)
	variants := [][]byte{
		[]byte(`{"tools":[],"messages":[]}`),
		[]byte(`{"tools":[{"name":"Other","input_schema":{}}],"messages":[]}`),
		[]byte(`{"tools":[{"name":"` + alias + `","input_schema":{}},{"name":"word_shell","input_schema":{}}],"messages":[]}`),
		[]byte(`{"tools":[{"name":"` + alias + `","input_schema":{}},{"name":"mcp__caller__word_shell","input_schema":{}}],"messages":[]}`),
		bytes.Replace(final, []byte(`"name":`), []byte(`"name":"other","name":`), 1),
		bytes.Replace(final, []byte(`"name":`), []byte(`"NaMe":"other","name":`), 1),
		bytes.Replace(final, []byte(`"name":`), []byte(`"na\u006de":"other","name":`), 1),
		bytes.Replace(final, []byte(`"name":`), []byte(`"defer_loading":true,"name":`), 1),
		bytes.Replace(final, []byte(`"name":`), []byte(`"type":"web_search_20250305","name":`), 1),
		bytes.Replace(final, []byte(`"tools":`), []byte(`"additional_tools":[],"tools":`), 1),
		bytes.Replace(final, []byte(`"tools":`), []byte(`"thread":{"type":"continue"},"tools":`), 1),
		bytes.Replace(final, []byte(`"content":"hello"`), []byte(`"content":[{"type":"tool_addition","tools":[]}]`), 1),
		bytes.Replace(final, []byte(`"content":"hello"`), []byte(`"content":[{"type":"tool_removal","tool_name":"`+alias+`"}]`), 1),
	}
	aliases := map[string]string{alias: "ExactShell"}
	for i, outgoing := range variants {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			proof := helps.BuildClaudeMCPBareAliasMap(pre, outgoing, aliases)
			if len(proof) != 0 {
				t.Fatalf("unexpected proof: %v", proof)
			}
			body := []byte(" \t" + `{"type":"message", "content":[{"type":"tool_use","name":"word_shell","input":{"n":1e2,"s":"\u0061"}}],"other":"word_shell"}` + "\r\n")
			got, err := reverseRemapOAuthToolNames(body, aliases, proof)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("declined proof changed raw JSON: %q, %v", got, err)
			}
			line := []byte("data:\t" + `{"type":"content_block_start","content_block":{"type":"tool_use","name":"word_shell","input":{"n":1e2,"s":"\u0061"}}}` + "\r\n")
			got, err = reverseRemapOAuthToolNamesFromStreamLine(line, aliases, proof)
			if err != nil || !bytes.Equal(got, line) {
				t.Fatalf("declined proof changed raw SSE: %q, %v", got, err)
			}
		})
	}
}

func TestClaudeMCPBareAliasExecutorFinalPayload(t *testing.T) {
	const original = "ExactShell"
	metadata := map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "fixture-only-bare-caller"}
	alias := helps.ClaudeMCPToolAlias(resolveClaudeMCPAliasOptions(context.Background(), metadata).secret, original, 0)
	_, component, _ := strings.Cut(strings.TrimPrefix(alias, "mcp__"), "__")
	tool := func(name string) map[string]any {
		return map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}}
	}
	for _, path := range []string{"native-json", "buffered-openai", "native-sse", "translated-sse"} {
		for _, scenario := range []string{"eligible", "collision", "removed", "replaced", "no-alias", "confirmed", "full-alias", "opaque", "continuation-injected"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				cfg := &config.Config{}
				params := map[string]any{}
				if scenario == "collision" {
					params["tools"] = []any{tool(alias), tool(component)}
				}
				if scenario == "replaced" {
					params["tools"] = []any{tool("Other")}
				}
				if scenario == "continuation-injected" {
					params["tools"] = []any{tool(alias)}
				}
				if scenario == "opaque" {
					params["mcp_servers"] = []any{map[string]any{"type": "url", "url": "https://never.invalid"}}
				}
				if len(params) > 0 {
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: params}}
				}
				if scenario == "removed" {
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"tools"}}}
				}
				var seen []byte
				calls := 0
				responseName := component
				if scenario == "full-alias" {
					responseName = alias
				}
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					// This transport never delegates: no provider or other network is reachable.
					if req.URL.Host != "fixture.invalid" || req.URL.Path != "/v1/messages" {
						return nil, fmt.Errorf("unexpected external request: %s", req.URL)
					}
					calls++
					var err error
					seen, err = io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					name := gjson.GetBytes(seen, "tools.0.name").String()
					if scenario == "eligible" && name != alias {
						return nil, fmt.Errorf("actual current alias = %q, want %q", name, alias)
					}
					if scenario == "eligible" {
						_, responseName, _ = strings.Cut(strings.TrimPrefix(name, "mcp__"), "__")
					}
					body := fmt.Sprintf(`{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"call_fixture","name":%q,"input":{"command":"fixture"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, responseName)
					contentType := "application/json"
					if path != "native-json" {
						contentType = "text/event-stream"
						body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
							fmt.Sprintf("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_fixture\",\"name\":%q,\"input\":{}}}\n\n", responseName) +
							"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"fixture\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				auth := &cliproxyauth.Auth{ID: "bare-fixture", Attributes: map[string]string{"api_key": "sk-ant-oat-fixture-only", "base_url": "https://fixture.invalid"}, Metadata: claudeOAuthTestMetadata()}
				if scenario == "no-alias" {
					auth.Attributes["api_key"] = "sk-ant-api-fixture-only"
				}
				payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"run fixture"}],"tools":[{"name":"ExactShell","input_schema":{"type":"object"}}]}`)
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Metadata: metadata}
				if scenario == "confirmed" {
					const userID = `{"device_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","account_uuid":"","session_id":"33333333-4444-4555-8666-777777777777"}`
					payload = bytes.Replace(payload, []byte(`{"model":`), []byte(fmt.Sprintf(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258.9cb; cc_entrypoint=claude-vscode; cch=00000;"}],"metadata":{"user_id":%q},"model":`, userID)), 1)
					opts.OriginalRequest = payload
					opts.Headers = http.Header{
						"User-Agent": {"claude-cli/2.1.280 (external, claude-vscode, agent-sdk/0.3.220)"},
						"X-App":      {"cli"}, "Anthropic-Beta": {"claude-code-20250219"},
						"X-Stainless-Package-Version": {"0.112.1"}, "X-Stainless-Runtime-Version": {"v26.3.0"},
					}
				}
				if path == "buffered-openai" || path == "translated-sse" {
					opts.ResponseFormat = sdktranslator.FormatOpenAI
				}
				executor := NewClaudeExecutor(cfg)
				if scenario == "continuation-injected" {
					executor.rememberClaudeOAuthToolAliases([]byte(`{"thread":{"type":"create"}}`), map[string]string{alias: original}, "msg_previous_fixture")
					payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_previous_fixture"},"messages":[{"role":"user","content":"continue fixture"}]}`)
				}
				req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
				var output []byte
				if path == "native-sse" || path == "translated-sse" {
					opts.Stream = true
					result, err := executor.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
				} else {
					result, err := executor.Execute(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = result.Payload
				}
				if calls != 1 {
					t.Fatalf("upstream calls = %d", calls)
				}
				want := component
				if scenario == "eligible" || scenario == "full-alias" {
					want = original
				}
				if !bytes.Contains(output, []byte(`"name":"`+want+`"`)) {
					t.Fatalf("want name %q, got %s; sent %s", want, output, seen)
				}
				if scenario == "collision" && gjson.GetBytes(seen, "tools.1.name").String() != component {
					t.Fatalf("proof repaired configured collision: %s", seen)
				}
				if scenario == "removed" && gjson.GetBytes(seen, "tools").Exists() {
					t.Fatalf("proof restored removed tools: %s", seen)
				}
				if scenario == "replaced" && gjson.GetBytes(seen, "tools.0.name").String() != "Other" {
					t.Fatalf("proof changed replacement: %s", seen)
				}
				if (scenario == "no-alias" || scenario == "confirmed") && gjson.GetBytes(seen, "tools.0.name").String() != original {
					t.Fatalf("native/API-key control unexpectedly aliased: %s", seen)
				}
				if scenario == "continuation-injected" && gjson.GetBytes(seen, "tools.0.name").String() != alias {
					t.Fatalf("continuation final declaration missing: %s", seen)
				}
			})
		}
	}
}
