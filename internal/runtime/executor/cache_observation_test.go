package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

type observerUsageCapture struct {
	alias   string
	records chan usage.Record
}

func (c *observerUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Alias == c.alias {
		c.records <- record
	}
}

func TestProviderCacheObservationWireAndUsage(t *testing.T) {
	hook := logtest.NewGlobal()
	alias := t.Name()
	capture := &observerUsageCapture{alias: alias, records: make(chan usage.Record, 32)}
	usage.RegisterNamedPlugin(alias, capture)
	t.Cleanup(func() { usage.RegisterNamedPlugin(alias, codexResponseModelNoopUsagePlugin{}) })
	session := "cache-observer-executor-fixture"
	cfg := &config.Config{CacheObservation: &config.CacheObservationConfig{
		SessionID: session, ExpiresAt: time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339), MaxAttempts: 20,
	}}
	ctx := usage.WithRequestedModelAlias(util.WithSessionID(context.Background(), session), alias)
	claudeAuth := &cliproxyauth.Auth{ID: "cache-observation-oauth", Attributes: map[string]string{"api_key": "sk-ant-oat-fixture"}, Metadata: map[string]any{
		"account_uuid":      "11111111-2222-4333-8444-555555555555",
		"claude_device_ids": []string{strings.Repeat("1", 64)},
	}}
	codexAuth := &cliproxyauth.Auth{ID: "cache-observation-codex", Attributes: map[string]string{"api_key": "fixture-key", "base_url": "https://upstream.test"}}
	var bodies [][]byte
	var headers []http.Header
	stream := false
	provider := "claude"
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		bodies = append(bodies, body)
		headers = append(headers, req.Header.Clone())
		response := `{"id":"msg_fixture","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":20,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10}}}`
		if stream {
			response = "data: {\"type\":\"message_start\",\"message\":" + response + "}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		if provider == "codex" {
			response = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":31,\"output_tokens\":2,\"total_tokens\":33,\"input_tokens_details\":{\"cached_tokens\":20}}}}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Sensitive": {"secret-response-header"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
	claudePayload := `{"model":"claude-opus-5-5","system":[{"type":"text","text":"secret-prompt","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"lookup","description":"fixture tool","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"first"}],"max_tokens":100}`
	growing := strings.Replace(claudePayload, `"first"}]`, `"first"},{"role":"assistant","content":[{"type":"tool_use","id":"tool-id","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-id","content":"tool result"}]}]`, 1)
	codexPayload := `{"model":"gpt-5.5","instructions":"secret-prompt","input":[{"role":"user","content":"first"}]}`
	seenRequestIDs := make(map[string]bool)
	for _, turn := range []struct {
		provider string
		stream   bool
		payload  string
	}{
		{"claude", false, claudePayload},
		{"claude", false, growing},
		{"claude", true, claudePayload},
		{"codex", false, codexPayload},
		{"codex", true, codexPayload},
	} {
		provider, stream = turn.provider, turn.stream
		model, source, auth := "claude-opus-5-5", sdktranslator.FormatClaude, claudeAuth
		if provider == "codex" {
			model, source, auth = "gpt-5.5", sdktranslator.FormatOpenAIResponse, codexAuth
		}
		req := cliproxyexecutor.Request{Model: model, Payload: []byte(turn.payload)}
		opts := cliproxyexecutor.Options{SourceFormat: source, Headers: http.Header{"X-Session-Id": {session}}}
		for _, enabled := range []bool{false, true} {
			selectedCfg := *cfg
			if !enabled {
				selectedCfg.CacheObservation = nil
			}
			claude := NewClaudeExecutor(&selectedCfg)
			codex := NewCodexExecutor(&selectedCfg)
			var errExecute error
			if stream {
				var result *cliproxyexecutor.StreamResult
				if provider == "claude" {
					result, errExecute = claude.ExecuteStream(ctx, auth, req, opts)
				} else {
					result, errExecute = codex.ExecuteStream(ctx, auth, req, opts)
				}
				if errExecute == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
					}
				}
			} else if provider == "claude" {
				_, errExecute = claude.Execute(ctx, auth, req, opts)
			} else {
				_, errExecute = codex.Execute(ctx, auth, req, opts)
			}
			if errExecute != nil {
				t.Fatalf("%s stream=%t enabled=%t: %v", provider, stream, enabled, errExecute)
			}
			var record usage.Record
			select {
			case record = <-capture.records:
			case <-time.After(5 * time.Second):
				t.Fatal("usage not delivered")
			}
			if enabled {
				matched := false
				for _, entry := range hook.AllEntries() {
					raw, ok := entry.Data["cache_observation"].(string)
					if !ok {
						continue
					}
					if strings.Contains(raw, "secret-") || strings.Contains(raw, "sk-ant-oat") {
						t.Fatal("sensitive content escaped final-boundary measurement")
					}
					var observation map[string]any
					if errJSON := json.Unmarshal([]byte(raw), &observation); errJSON != nil {
						t.Fatal(errJSON)
					}
					if observation["request_id"] == record.RequestID && observation["phase"] == "completion" {
						matched = true
						if observation["usage_state"] != "reported" || observation["shape_status"] != "complete" || observation["provider"] != provider {
							t.Fatalf("invalid measurement: %v", observation)
						}
						if provider == "claude" && observation["cache_write_1h"] != float64(10) {
							t.Fatal("raw TTL split not retained")
						}
					}
				}
				if !matched || seenRequestIDs[record.RequestID] {
					t.Fatal("measurement missing or joined to the wrong execution")
				}
				seenRequestIDs[record.RequestID] = true
			}
		}
		last := len(bodies) - 1
		if !bytes.Equal(bodies[last-1], bodies[last]) {
			t.Fatalf("%s stream=%t observation changed wire body", provider, stream)
		}
		// Request IDs are legitimately regenerated. Other headers must stay equal.
		for _, h := range headers[last-1:] {
			h.Del("X-Client-Request-Id")
			delete(h, "x-client-request-id")
		}
		if !reflect.DeepEqual(headers[last-1], headers[last]) {
			t.Fatalf("%s stream=%t observation changed transport headers", provider, stream)
		}
	}
}
