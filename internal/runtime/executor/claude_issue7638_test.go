package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func claudeIssue7638OAuthAuth(id, token, accountUUID string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "claude",
		Status:   cliproxyauth.StatusActive,
		Metadata: map[string]any{
			"access_token": token,
			"account_uuid": accountUUID,
		},
	}
}

func claudeIssue7638JSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func claudeIssue7638SSEResponse(req *http.Request, events string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(events)),
		Request:    req,
	}
}

func claudeIssue7638AdvisorMintJSON(id, text string) string {
	return fmt.Sprintf(`{
		"id": "msg_%s",
		"type": "message",
		"role": "assistant",
		"model": "claude-opus-5-5",
		"content": [
			{"type": "server_tool_use", "id": %q, "name": "advisor", "input": {}},
			{"type": "advisor_tool_result", "tool_use_id": %q, "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_%s"}},
			{"type": "text", "text": %q}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 20}
	}`, id, id, id, id, text)
}

func claudeIssue7638AdvisorMintSSE(id, text string) string {
	return fmt.Sprintf(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%s\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"server_tool_use\",\"id\":%q,\"name\":\"advisor\",\"input\":{}}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"advisor_tool_result\",\"tool_use_id\":%q,\"content\":{\"type\":\"advisor_redacted_result\",\"encrypted_content\":\"enc_%s\"}}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":20}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		id, id, id, id, text,
	)
}

func TestClaudeIssue7638_ExecuteStripsForeignAdvisorBlocksAndPreservesOwn(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var captured [][]byte
	callIdx := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured = append(captured, body)
		callIdx++
		switch callIdx {
		case 1:
			return claudeIssue7638JSONResponse(req, claudeIssue7638AdvisorMintJSON("srvtoolu_adv_a", "from A")), nil
		case 2:
			return claudeIssue7638JSONResponse(req, claudeIssue7638AdvisorMintJSON("srvtoolu_adv_b", "from B")), nil
		default:
			return claudeIssue7638JSONResponse(req, `{"id":"msg_3","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
		}
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	exec := NewClaudeExecutor(&config.Config{})
	authA := claudeIssue7638OAuthAuth("cred-a", "sk-ant-oat-a", "11111111-1111-4111-8111-111111111111")
	authB := claudeIssue7638OAuthAuth("cred-b", "sk-ant-oat-b", "22222222-2222-4222-8222-222222222222")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	// Turn 1 on A: mints srvtoolu_adv_a.
	turn1 := []byte(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"turn 1"}]}`)
	if _, err := exec.Execute(ctx, authA, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: turn1}, opts); err != nil {
		t.Fatalf("turn 1 Execute() error = %v", err)
	}

	// Turn 2 on B: replays A's advisor blocks (must be stripped) and mints srvtoolu_adv_b.
	turn2 := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_adv_a", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_adv_a", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_a"}},
					{"type": "text", "text": "from A"}
				]
			},
			{"role": "user", "content": "turn 2"}
		]
	}`)
	if _, err := exec.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: turn2}, opts); err != nil {
		t.Fatalf("turn 2 Execute() error = %v", err)
	}

	if strings.Contains(string(captured[1]), "srvtoolu_adv_a") {
		t.Fatalf("turn 2 sent foreign advisor block srvtoolu_adv_a to cred-b: %s", captured[1])
	}
	if !strings.Contains(string(captured[1]), "from A") {
		t.Fatalf("turn 2 dropped non-advisor text block: %s", captured[1])
	}

	// Turn 3 on B: replays both A's (foreign) and B's (own) advisor blocks.
	turn3 := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_adv_a", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_adv_a", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_a"}},
					{"type": "text", "text": "from A"}
				]
			},
			{"role": "user", "content": "turn 2"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_adv_b", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_adv_b", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_b"}},
					{"type": "text", "text": "from B"}
				]
			},
			{"role": "user", "content": "turn 3"}
		]
	}`)
	if _, err := exec.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: turn3}, opts); err != nil {
		t.Fatalf("turn 3 Execute() error = %v", err)
	}

	if strings.Contains(string(captured[2]), "srvtoolu_adv_a") {
		t.Fatalf("turn 3 sent foreign advisor block srvtoolu_adv_a to cred-b: %s", captured[2])
	}
	if !strings.Contains(string(captured[2]), "srvtoolu_adv_b") {
		t.Fatalf("turn 3 stripped cred-b's own advisor block srvtoolu_adv_b: %s", captured[2])
	}
}

func TestClaudeIssue7638_ExecuteStreamTracksMintedAdvisorAndStripsOnRebind(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var captured [][]byte
	callIdx := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured = append(captured, body)
		callIdx++
		if callIdx == 1 {
			return claudeIssue7638SSEResponse(req, claudeIssue7638AdvisorMintSSE("srvtoolu_stream_a", "stream from A")), nil
		}
		return claudeIssue7638SSEResponse(req,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		), nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	exec := NewClaudeExecutor(&config.Config{})
	authA := claudeIssue7638OAuthAuth("cred-stream-a", "sk-ant-oat-sa", "11111111-1111-4111-8111-111111111112")
	authB := claudeIssue7638OAuthAuth("cred-stream-b", "sk-ant-oat-sb", "22222222-2222-4222-8222-222222222223")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true}

	// Turn 1 streaming on A: mints srvtoolu_stream_a.
	res1, err := exec.ExecuteStream(ctx, authA, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"turn 1"}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("turn 1 ExecuteStream() error = %v", err)
	}
	for chunk := range res1.Chunks {
		if chunk.Err != nil {
			t.Fatalf("turn 1 chunk error = %v", chunk.Err)
		}
	}

	// Turn 2 streaming on B: foreign advisor turns (containing ONLY advisor blocks) must be dropped completely.
	turn2 := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"stream": true,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_stream_a", "name": "advisor", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_stream_a", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_sa"}}
				]
			},
			{"role": "assistant", "content": "stream from A"},
			{"role": "user", "content": "turn 2"}
		]
	}`)
	res2, err := exec.ExecuteStream(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: turn2}, opts)
	if err != nil {
		t.Fatalf("turn 2 ExecuteStream() error = %v", err)
	}
	for chunk := range res2.Chunks {
		if chunk.Err != nil {
			t.Fatalf("turn 2 chunk error = %v", chunk.Err)
		}
	}

	if strings.Contains(string(captured[1]), "srvtoolu_stream_a") {
		t.Fatalf("turn 2 stream sent foreign advisor block to cred-stream-b: %s", captured[1])
	}
	// The two advisor-only turns must be dropped rather than leaving empty content arrays.
	for _, msg := range ojson.GetBytes(captured[1], "messages").Array() {
		content := msg.Get("content")
		if content.IsArray() && len(content.Array()) == 0 {
			t.Fatalf("turn 2 stream left empty content array in messages: %s", captured[1])
		}
	}
}

func TestClaudeIssue7638_InRequestFailover429StripsForeignAdvisorOnRetry(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var mu sync.Mutex
	var attempts []struct {
		authHeader string
		body       []byte
	}
	turn := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		authHeader := req.Header.Get("Authorization")
		mu.Lock()
		attempts = append(attempts, struct {
			authHeader string
			body       []byte
		}{authHeader: authHeader, body: body})
		turn++
		cur := turn
		mu.Unlock()
		switch cur {
		case 1:
			// Turn 1 on A mints srvtoolu_failover.
			return claudeIssue7638JSONResponse(req, claudeIssue7638AdvisorMintJSON("srvtoolu_failover", "advice")), nil
		case 2:
			// Turn 2 first lands on A, which returns 429 to trigger in-request failover to B.
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)),
				Request:    req,
			}, nil
		default:
			// Retry on B must receive cleaned body without srvtoolu_failover.
			if strings.Contains(string(body), "srvtoolu_failover") {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"Advisor tool result content could not be processed."}}`)),
					Request:    req,
				}, nil
			}
			return claudeIssue7638JSONResponse(req, `{"id":"msg_ok","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"recovered"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
		}
	})

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.SetRetryConfig(2, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
	authA := claudeIssue7638OAuthAuth("cred-issue7638-a", "sk-ant-oat-a", "11111111-1111-4111-8111-111111111111")
	authB := claudeIssue7638OAuthAuth("cred-issue7638-b", "sk-ant-oat-b", "22222222-2222-4222-8222-222222222222")
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authA.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
	reg.RegisterClient(authB.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5"}})
	t.Cleanup(func() {
		reg.UnregisterClient(authA.ID)
		reg.UnregisterClient(authB.ID)
	})
	if _, err := manager.Register(context.Background(), authA); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(context.Background(), authB); err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	// Turn 1: A serves and mints srvtoolu_failover.
	if _, err := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"turn 1"}]}`),
	}, opts); err != nil {
		t.Fatalf("turn 1 manager.Execute() error = %v", err)
	}

	// Turn 2: A returns 429 -> Manager fails over to B -> B must succeed with stripped body.
	turn2 := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_failover", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_failover", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_f"}},
					{"type": "text", "text": "advice"}
				]
			},
			{"role": "user", "content": "turn 2"}
		]
	}`)
	if _, err := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: turn2}, opts); err != nil {
		t.Fatalf("turn 2 failover manager.Execute() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("expected 3 upstream attempts (turn1-A, turn2-A-429, turn2-B-200), got %d", len(attempts))
	}
	if !strings.Contains(string(attempts[1].body), "srvtoolu_failover") {
		t.Fatalf("attempt on original owner A unexpectedly stripped its own advisor block: %s", attempts[1].body)
	}
	if strings.Contains(string(attempts[2].body), "srvtoolu_failover") {
		t.Fatalf("failover attempt on B still contained foreign advisor block: %s", attempts[2].body)
	}
}

func TestClaudeIssue7638_CountTokensAndKVPersistenceAcrossRestart(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	kvPath := filepath.Join(t.TempDir(), "kv.db")
	if errCfg := kvstore.ConfigureLocal(true, kvPath); errCfg != nil {
		t.Fatal(errCfg)
	}
	t.Cleanup(func() {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})

	var captured [][]byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured = append(captured, body)
		if strings.Contains(req.URL.Path, "count_tokens") {
			return claudeIssue7638JSONResponse(req, `{"input_tokens":42}`), nil
		}
		return claudeIssue7638JSONResponse(req, claudeIssue7638AdvisorMintJSON("srvtoolu_persist", "persisted")), nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	authA := claudeIssue7638OAuthAuth("cred-persist-a", "sk-ant-oat-pa", "11111111-1111-4111-8111-111111111113")
	authB := claudeIssue7638OAuthAuth("cred-persist-b", "sk-ant-oat-pb", "22222222-2222-4222-8222-222222222224")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	exec1 := NewClaudeExecutor(&config.Config{})
	if _, err := exec1.Execute(ctx, authA, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"turn 1"}]}`),
	}, opts); err != nil {
		t.Fatalf("exec1.Execute() error = %v", err)
	}

	// Simulate proxy restart: close & reopen KV store and allocate a brand-new ClaudeExecutor.
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	if errCfg := kvstore.ConfigureLocal(true, kvPath); errCfg != nil {
		t.Fatal(errCfg)
	}
	exec2 := NewClaudeExecutor(&config.Config{})

	replayPayload := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_persist", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_persist", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_p"}},
					{"type": "text", "text": "persisted"}
				]
			},
			{"role": "user", "content": "turn 2"}
		]
	}`)

	if _, err := exec2.CountTokens(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: replayPayload}, opts); err != nil {
		t.Fatalf("exec2.CountTokens() error = %v", err)
	}
	if strings.Contains(string(captured[1]), "srvtoolu_persist") {
		t.Fatalf("post-restart CountTokens on cred-persist-b still contained foreign advisor block: %s", captured[1])
	}

	if _, err := exec2.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: replayPayload}, opts); err != nil {
		t.Fatalf("exec2.Execute() error = %v", err)
	}
	if strings.Contains(string(captured[2]), "srvtoolu_persist") {
		t.Fatalf("post-restart Execute on cred-persist-b still contained foreign advisor block: %s", captured[2])
	}
}

func TestClaudeIssue7638_AllAdvisorBlockShapesAndRequestFallbackLearning(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var captured [][]byte
	callIdx := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured = append(captured, body)
		callIdx++
		if callIdx == 1 {
			// Failed 400 on B must NOT record B as the owner of srvtoolu_pre.
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"Advisor tool result content could not be processed."}}`)),
				Request:    req,
			}, nil
		}
		return claudeIssue7638JSONResponse(req, `{"id":"msg_ok","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	exec := NewClaudeExecutor(&config.Config{})
	authA := claudeIssue7638OAuthAuth("cred-shape-a", "sk-ant-oat-sha", "11111111-1111-4111-8111-111111111114")
	authB := claudeIssue7638OAuthAuth("cred-shape-b", "sk-ant-oat-shb", "22222222-2222-4222-8222-222222222225")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	payload := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "start"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_pre", "name": "advisor", "input": {}},
					{"type": "server_tool_use", "id": "srvtoolu_ws", "name": "web_search", "input": {"query": "go"}},
					{"type": "tool_use", "id": "toolu_mix", "name": "custom_tool", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "advisor_redacted_result", "tool_use_id": "srvtoolu_pre", "encrypted_content": "enc_redacted"},
					{"type": "tool_result", "tool_use_id": "srvtoolu_pre", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_obj"}},
					{
						"type": "tool_result",
						"tool_use_id": "toolu_mix",
						"content": [
							{"type": "text", "text": "keep tool output"},
							{"type": "advisor_redacted_result", "encrypted_content": "enc_mixed"}
						]
					}
				]
			},
			{"role": "user", "content": "next"}
		]
	}`)

	// Call 1: B fails with 400 -> must NOT claim ownership of srvtoolu_pre or toolu_mix.
	if _, err := exec.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); err == nil {
		t.Fatal("expected 400 error on first attempt")
	}

	// Call 2: A succeeds with 200 -> A learns ownership of srvtoolu_pre and toolu_mix via 2xx request fallback.
	if _, err := exec.Execute(ctx, authA, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); err != nil {
		t.Fatalf("A Execute() error = %v", err)
	}

	// Call 3: B retries -> foreign advisor blocks stripped; web_search and non-advisor tool_result text kept!
	if _, err := exec.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); err != nil {
		t.Fatalf("B Execute() error = %v", err)
	}

	bodyB := string(captured[2])
	if strings.Contains(bodyB, "srvtoolu_pre") || strings.Contains(bodyB, "enc_redacted") || strings.Contains(bodyB, "enc_obj") || strings.Contains(bodyB, "enc_mixed") {
		t.Fatalf("B still received foreign advisor blocks: %s", bodyB)
	}
	if !strings.Contains(bodyB, "srvtoolu_ws") {
		t.Fatalf("non-advisor web_search server_tool_use was wrongly stripped: %s", bodyB)
	}
	if !strings.Contains(bodyB, "toolu_mix") || !strings.Contains(bodyB, "keep tool output") {
		t.Fatalf("mixed tool_result non-advisor text was wrongly stripped: %s", bodyB)
	}
}
func TestClaudeIssue7638_ForAPIKeyAndWithConfigShareAdvisorOwnerStore(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var captured [][]byte
	callIdx := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured = append(captured, body)
		callIdx++
		if callIdx == 1 {
			return claudeIssue7638JSONResponse(req, claudeIssue7638AdvisorMintJSON("srvtoolu_shared", "from A")), nil
		}
		return claudeIssue7638JSONResponse(req, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	base := NewClaudeExecutor(&config.Config{})
	copyA := base.ForAPIKey().(*ClaudeExecutor)
	copyB := base.WithConfig(&config.Config{})
	authA := claudeIssue7638OAuthAuth("cred-share-a", "sk-ant-oat-sha1", "11111111-1111-4111-8111-111111111115")
	authB := claudeIssue7638OAuthAuth("cred-share-b", "sk-ant-oat-shb1", "22222222-2222-4222-8222-222222222226")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	if _, err := copyA.Execute(ctx, authA, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"turn 1"}]}`),
	}, opts); err != nil {
		t.Fatalf("copyA.Execute() error = %v", err)
	}

	replay := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_shared", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_shared", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_s"}},
					{"type": "text", "text": "from A"}
				]
			},
			{"role": "user", "content": "turn 2"}
		]
	}`)
	if _, err := copyB.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: replay}, opts); err != nil {
		t.Fatalf("copyB.Execute() error = %v", err)
	}
	if strings.Contains(string(captured[1]), "srvtoolu_shared") {
		t.Fatalf("WithConfig/ForAPIKey copies did not share advisor owner store: %s", captured[1])
	}
}

func TestClaudeIssue7638_PayloadRuleRemainsFinalSemanticBarrier(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	var captured [][]byte
	callIdx := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured = append(captured, body)
		callIdx++
		if callIdx == 1 {
			return claudeIssue7638JSONResponse(req, claudeIssue7638AdvisorMintJSON("srvtoolu_barrier", "from A")), nil
		}
		return claudeIssue7638JSONResponse(req, `{"id":"msg_2","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	})

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-opus-5-5", Protocol: "claude"}},
					Params: map[string]any{"max_tokens": 999},
				},
			},
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	exec := NewClaudeExecutor(cfg)
	authA := claudeIssue7638OAuthAuth("cred-bar-a", "sk-ant-oat-ba", "11111111-1111-4111-8111-111111111116")
	authB := claudeIssue7638OAuthAuth("cred-bar-b", "sk-ant-oat-bb", "22222222-2222-4222-8222-222222222227")
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	if _, err := exec.Execute(ctx, authA, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"turn 1"}]}`),
	}, opts); err != nil {
		t.Fatalf("turn 1 error = %v", err)
	}

	replay := []byte(`{
		"model": "claude-opus-5-5",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "turn 1"},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_barrier", "name": "advisor", "input": {}},
					{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_barrier", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_b"}},
					{"type": "text", "text": "from A"}
				]
			},
			{"role": "user", "content": "turn 2"}
		]
	}`)
	if _, err := exec.Execute(ctx, authB, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: replay}, opts); err != nil {
		t.Fatalf("turn 2 error = %v", err)
	}
	if strings.Contains(string(captured[1]), "srvtoolu_barrier") {
		t.Fatalf("foreign advisor block was not stripped: %s", captured[1])
	}
	if got := ojson.GetBytes(captured[1], "max_tokens").Int(); got != 999 {
		t.Fatalf("payload override max_tokens = %d, want 999: %s", got, captured[1])
	}
}

func TestClaudeIssue7638_PayloadRuleRemovingUnknownAdvisorDoesNotClaimOwner(t *testing.T) {
	for _, stream := range []bool{false, true} {
		mode := "Execute"
		if stream {
			mode = "ExecuteStream"
		}
		t.Run(mode, func(t *testing.T) {
			if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
				t.Fatal(errShutdown)
			}
			var captured [][]byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(req.Body)
				captured = append(captured, body)
				if stream {
					return claudeIssue7638SSEResponse(req,
						"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_f\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"+
							"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
							"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"+
							"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
							"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"+
							"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
					), nil
				}
				return claudeIssue7638JSONResponse(req, `{"id":"msg_f","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
			})

			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			authA := claudeIssue7638OAuthAuth("cred-filt-a", "sk-ant-oat-fa", "11111111-1111-4111-8111-111111111117")
			authB := claudeIssue7638OAuthAuth("cred-filt-b", "sk-ant-oat-fb", "22222222-2222-4222-8222-222222222228")

			// Override replaces messages so srvtoolu_filtered never reaches upstream B.
			overrideCfg := &config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{
						{
							Models: []config.PayloadModelRule{{Name: "claude-opus-5-5", Protocol: "claude"}},
							Params: map[string]any{
								"messages": []any{map[string]any{"role": "user", "content": "sanitized by rule"}},
							},
						},
					},
				},
			}
			exec := NewClaudeExecutor(overrideCfg)
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}

			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			payload := []byte(fmt.Sprintf(`{
				"model": "claude-opus-5-5",
				"max_tokens": 256,
				%s
				"messages": [
					{"role": "user", "content": "turn 1"},
					{
						"role": "assistant",
						"content": [
							{"type": "server_tool_use", "id": "srvtoolu_filtered", "name": "advisor", "input": {}},
							{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_filtered", "content": {"type": "advisor_redacted_result", "encrypted_content": "enc_filt"}},
							{"type": "text", "text": "from A"}
						]
					},
					{"role": "user", "content": "turn 2"}
				]
			}`, streamField))

			runReq := func(e *ClaudeExecutor, auth *cliproxyauth.Auth) {
				if stream {
					res, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts)
					if err != nil {
						t.Fatalf("ExecuteStream() error = %v", err)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
						}
					}
					return
				}
				if _, err := e.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
			}

			// Call 1 on B with payload rule removing srvtoolu_filtered: B succeeds,
			// but MUST NOT claim ownership of srvtoolu_filtered.
			runReq(exec, authB)
			if strings.Contains(string(captured[0]), "srvtoolu_filtered") {
				t.Fatalf("expected payload rule to remove srvtoolu_filtered before upstream B: %s", captured[0])
			}

			// Call 2 on A without the payload rule (sharing the same store via WithConfig):
			// srvtoolu_filtered must NOT be stripped for A!
			plainExec := exec.WithConfig(&config.Config{})
			runReq(plainExec, authA)
			if !strings.Contains(string(captured[1]), "srvtoolu_filtered") {
				t.Fatalf("cred-filt-a had srvtoolu_filtered stripped because cred-filt-b falsely claimed ownership after payload removal: %s", captured[1])
			}
		})
	}
}
