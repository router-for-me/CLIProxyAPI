package test

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Text-only replies for the three live harness checks. The fixture upstream is local, so
// these checks cover each CLI's protocol handling of this gateway, not any provider.
var (
	liveChatStream = sse(
		`{"id":"chatcmpl-live","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{"id":"chatcmpl-live","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{"content":"pong from fixture"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-live","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-live","object":"chat.completion.chunk","created":1700000000,"model":"grok-fixture-chat","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`,
		`[DONE]`)

	liveResponsesStream = sse(
		`{"type":"response.created","response":{"id":"resp_live","object":"response","status":"in_progress","model":"gpt-5.4"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"pong from fixture"}`,
		`{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"pong from fixture"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong from fixture"}]}}`,
		`{"type":"response.completed","response":{"id":"resp_live","object":"response","status":"completed","model":"gpt-5.4","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"pong from fixture"}]}],"usage":{"input_tokens":20,"output_tokens":7,"total_tokens":27}}}`)

	liveMessagesStream = namedSSE(
		"message_start", `{"type":"message_start","message":{"id":"msg_live","type":"message","role":"assistant","model":"claude-sonnet-fixture","content":[],"stop_reason":null,"usage":{"input_tokens":11,"output_tokens":1}}}`,
		"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong from fixture"}}`,
		"content_block_stop", `{"type":"content_block_stop","index":0}`,
		"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":6}}`,
		"message_stop", `{"type":"message_stop"}`)
	liveMessagesJSON = `{"id":"msg_live","type":"message","role":"assistant","model":"claude-sonnet-fixture","content":[{"type":"text","text":"pong from fixture"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":6}}`
)

// TestLiveHarnessCLIs runs the installed Claude Code, Codex and Grok CLIs against the gateway.
// It is opt-in because it depends on locally installed, version-specific clients.
func TestLiveHarnessCLIs(t *testing.T) {
	if os.Getenv("AI_PROXY_LIVE_HARNESS") != "1" {
		t.Skip("set AI_PROXY_LIVE_HARNESS=1 to run the installed harness CLIs against the gateway")
	}

	type harness struct {
		binary   string
		contract contract
		reply    func(w http.ResponseWriter, body string)
		command  func(ctx context.Context, gatewayURL, home string) (*exec.Cmd, error)
	}
	sseReply := func(stream string) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, _ string) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, stream)
		}
	}
	harnesses := map[string]harness{
		"claude": {binary: "claude", contract: contracts[2], reply: func(w http.ResponseWriter, body string) {
			if strings.Contains(body, `"stream":true`) {
				sseReply(liveMessagesStream)(w, body)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, liveMessagesJSON)
		}, command: func(ctx context.Context, gatewayURL, home string) (*exec.Cmd, error) {
			cmd := exec.CommandContext(ctx, "claude", "-p", "say pong", "--model", "claude-sonnet-fixture")
			cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+gatewayURL, "ANTHROPIC_API_KEY="+gatewayClientKey, "DISABLE_TELEMETRY=1", "HOME="+home)
			return cmd, nil
		}},
		"codex": {binary: "codex", contract: contracts[1], reply: func(w http.ResponseWriter, body string) { sseReply(liveResponsesStream)(w, body) },
			command: func(ctx context.Context, gatewayURL, home string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, "codex", "exec", "--skip-git-repo-check", "--ephemeral",
					"-c", `model_provider="fx"`, "-c", `model="gpt-5.4"`,
					"-c", `model_providers.fx.name="fx"`, "-c", `model_providers.fx.base_url="`+gatewayURL+`/v1"`,
					"-c", `model_providers.fx.env_key="FX_KEY"`, "-c", `model_providers.fx.wire_api="responses"`, "say pong")
				cmd.Env = append(os.Environ(), "FX_KEY="+gatewayClientKey, "CODEX_HOME="+home)
				return cmd, nil
			}},
		"grok": {binary: "grok", contract: contracts[0], reply: func(w http.ResponseWriter, body string) { sseReply(liveChatStream)(w, body) },
			command: func(ctx context.Context, gatewayURL, home string) (*exec.Cmd, error) {
				config := filepath.Join(home, "config.toml")
				toml := "[model.fx]\nmodel = \"grok-fixture-chat\"\nname = \"fx\"\nbase_url = \"" + gatewayURL + "/v1\"\napi_backend = \"chat_completions\"\nenv_key = \"FX_KEY\"\n"
				if err := os.WriteFile(config, []byte(toml), 0o600); err != nil {
					return nil, err
				}
				cmd := exec.CommandContext(ctx, "grok", "-p", "say pong", "-m", "fx")
				cmd.Env = append(os.Environ(), "FX_KEY="+gatewayClientKey, "GROK_CONFIG_PATH="+config, "GROK_HOME="+home)
				return cmd, nil
			}},
	}

	for name, h := range harnesses {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(h.binary); err != nil {
				t.Skipf("%s is not installed", h.binary)
			}
			version, _ := exec.Command(h.binary, "--version").CombinedOutput()
			t.Logf("%s version: %s", name, strings.TrimSpace(string(version)))

			upstream := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, call upstreamCall) bool {
				if !strings.HasSuffix(call.Path, h.contract.upstream) {
					return false
				}
				h.reply(w, call.Body)
				return true
			})
			recorder := recordAccounting(t)
			g := newGateway(t, gatewayOptions{mode: accountingEnabled, upstreamURL: upstream.server.URL, accounts: contractAccounts("live-"+name, h.contract)})

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd, err := h.command(ctx, g.URL, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "pong from fixture") {
				t.Fatalf("%s did not complete a turn through the gateway: %v\n%s", name, err, out)
			}
			if events := recorder.waitEvents(t, 1); events[0].Status != "succeeded" {
				t.Errorf("%s accounting event = %+v", name, events[0])
			}
		})
	}
}
