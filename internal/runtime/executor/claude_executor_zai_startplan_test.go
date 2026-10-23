package executor

import (
	"context"
	"encoding/json"
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

func TestIsZaiStartPlanUpstreamURL(t *testing.T) {
	cases := map[string]bool{
		"https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages":           true,
		"https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages?beta=true": true,
		"https://api.z.ai/api/anthropic/v1/messages":                           false,
		"https://api.anthropic.com/v1/messages":                                false,
		"":                                                                     false,
	}
	for url, want := range cases {
		if got := isZaiStartPlanUpstreamURL(url); got != want {
			t.Errorf("isZaiStartPlanUpstreamURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestZaiStartPlanSystemBlocksAreNonEmpty(t *testing.T) {
	if strings.TrimSpace(zaiStartPlanSystemBlockOne) == "" {
		t.Fatal("block one is empty")
	}
	if len(zaiStartPlanSystemBlockTwo) < 1200 {
		t.Fatalf("block two too short: %d chars; the upstream risk gate requires the full official agent prompt", len(zaiStartPlanSystemBlockTwo))
	}
	if !strings.HasPrefix(zaiStartPlanSystemBlockOne, "You are ZCode") {
		t.Fatalf("block one does not match the official client prompt: %q", zaiStartPlanSystemBlockOne)
	}
	if !strings.HasPrefix(zaiStartPlanSystemBlockTwo, "\nYou are an interactive ZCode agent") {
		t.Fatalf("block two does not match the official client prompt: %q", zaiStartPlanSystemBlockTwo[:60])
	}
}

func TestInjectZaiStartPlanSystemWithoutSystemField(t *testing.T) {
	body := []byte(`{"model":"GLM-5.3-Flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	out := injectZaiStartPlanSystem(body)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Get("text").String() != zaiStartPlanSystemBlockOne || blocks[1].Get("text").String() != zaiStartPlanSystemBlockTwo {
		t.Fatal("injected blocks do not carry the official prompt")
	}
}

func TestInjectZaiStartPlanSystemPrependsBeforeStringSystem(t *testing.T) {
	body := []byte(`{"model":"GLM-5.3-Flash","system":"You are a helpful pirate.","messages":[]}`)
	out := injectZaiStartPlanSystem(body)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 3 {
		t.Fatalf("system blocks = %d, want 3", len(blocks))
	}
	if blocks[0].Get("text").String() != zaiStartPlanSystemBlockOne {
		t.Fatal("official block one must come first")
	}
	if blocks[2].Get("text").String() != "You are a helpful pirate." {
		t.Fatalf("client system lost: %q", blocks[2].Get("text").String())
	}
}

func TestInjectZaiStartPlanSystemPrependsBeforeBlockArray(t *testing.T) {
	body := []byte(`{"model":"GLM-5.3-Flash","system":[{"type":"text","text":"client rules"},{"type":"text","text":"more rules"}],"messages":[]}`)
	out := injectZaiStartPlanSystem(body)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 4 {
		t.Fatalf("system blocks = %d, want 4", len(blocks))
	}
	if blocks[0].Get("text").String() != zaiStartPlanSystemBlockOne || blocks[1].Get("text").String() != zaiStartPlanSystemBlockTwo {
		t.Fatal("official blocks must occupy positions 0 and 1")
	}
	if blocks[2].Get("text").String() != "client rules" || blocks[3].Get("text").String() != "more rules" {
		t.Fatal("client system blocks must be preserved after the official ones")
	}
}

func TestInjectZaiStartPlanSystemKeepsBodyValidJSON(t *testing.T) {
	body := []byte(`{"model":"GLM-5.3-Flash","messages":[{"role":"user","content":"hi"}]}`)
	out := injectZaiStartPlanSystem(body)
	if !gjson.ValidBytes(out) {
		t.Fatal("output is not valid JSON")
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// TestOpenAICompatExecutorStreamingDelegationInjectsStartPlanSystem is an
// end-to-end streaming regression test for the Start Plan risk gate: the
// upstream request body sent on the streaming path must carry the two
// official system blocks in positions 0 and 1, or the gateway rejects the
// request with HTTP 405 code 3012.
func TestOpenAICompatExecutorStreamingDelegationInjectsStartPlanSystem(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3-flash","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	bodyCh := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case bodyCh <- body:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer upstream.Close()

	exec := NewOpenAICompatExecutor("zai", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "zai", Attributes: map[string]string{
		"api_key":           "zcode-jwt",
		"base_url":          upstream.URL + "/zcode-plan/anthropic",
		"upstream_protocol": "anthropic",
	}}
	req := cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","max_tokens":16,"system":"You are a helpful pirate.","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true}
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range result.Chunks {
	}
	var body []byte
	select {
	case body = <-bodyCh:
	default:
	}
	if len(body) == 0 {
		t.Fatal("upstream request body was not captured")
	}
	blocks := gjson.GetBytes(body, "system").Array()
	if len(blocks) < 2 {
		t.Fatalf("system blocks = %d, want at least the two official ones", len(blocks))
	}
	if blocks[0].Get("text").String() != zaiStartPlanSystemBlockOne {
		t.Fatalf("system block 0 does not carry the official prompt: %q", blocks[0].Get("text").String())
	}
	if blocks[1].Get("text").String() != zaiStartPlanSystemBlockTwo {
		t.Fatalf("system block 1 does not carry the official prompt: %q", blocks[1].Get("text").String()[:60])
	}
	if blocks[len(blocks)-1].Get("text").String() != "You are a helpful pirate." {
		t.Fatalf("client system prompt must be preserved after the official blocks, got last block: %q", blocks[len(blocks)-1].Get("text").String())
	}
}
