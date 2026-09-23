package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// The upstream deliberately cannot finish the initial response until it reads
// steering. A serial request/response proxy deadlocks this test.
func TestCodexDuplexSteeringLifecycle(t *testing.T) {
	for _, boundary := range []string{"response.incomplete", "response.completed"} {
		t.Run(boundary, func(t *testing.T) {
			var connections atomic.Int32
			serverErrors := make(chan error, 1)
			serverDone := make(chan struct{})
			steer := []byte(`{"type":"response.steer","previous_response_id":"r1","input":[{"role":"user","content":[{"type":"input_text","text":"STEER_OK"}]}]}`)
			accepted := []byte(`{"type":"response.steer.accepted","sequence_number":2,"steer":{"id":"s1","previous_response_id":"r1"}}`)
			pending := []byte(`{"type":"response.steer.pending","sequence_number":8,"steer":{"id":"s2","previous_response_id":"r2"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"c1","name":"lookup"}]}`)
			failed := []byte(`{"type":"response.steer.failed","sequence_number":14,"steer":{"id":"s3","previous_response_id":"missing","input":"recover me"},"error":{"code":"response_not_found","message":"missing response"}}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				connections.Add(1)
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					serverErrors <- err
					return
				}
				defer func() { _ = conn.Close() }()
				// Timeouts are test failure bounds only; no production network deadline is added.
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				read := func() []byte {
					_, b, err := conn.ReadMessage()
					if err != nil {
						t.Errorf("upstream read: %v", err)
					}
					return b
				}
				write := func(b []byte) {
					if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
						t.Errorf("upstream write: %v", err)
					}
				}
				first := read()
				if gjson.GetBytes(first, "type").String() != "response.create" {
					t.Errorf("initial create: %s", first)
				}
				write([]byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))
				if got := read(); !bytes.Equal(got, steer) {
					t.Errorf("steer transformed: %s", got)
				}
				write(accepted)
				write([]byte(fmt.Sprintf(`{"type":%q,"response":{"id":"r1","output":[],"incomplete_details":{"reason":"steered"}}}`, boundary)))
				write([]byte(`{"type":"response.created","response":{"id":"r2","output":[]}}`))
				got := read()
				if gjson.GetBytes(got, "type").String() != "response.steer" {
					t.Errorf("second control: %s", got)
				}
				write([]byte(`{"type":"response.steer.accepted","steer":{"id":"s2","previous_response_id":"r2"}}`))
				write([]byte(`{"type":"response.completed","response":{"id":"r2","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]}}`))
				write(pending)
				got = read()
				if gjson.GetBytes(got, "type").String() != "response.create" || gjson.GetBytes(got, "previous_response_id").String() != "r2" || gjson.GetBytes(got, "input.0.call_id").String() != "c1" {
					t.Errorf("tool continuation: %s", got)
				}
				if gjson.GetBytes(got, "instructions").String() != "New settings" {
					t.Errorf("explicit settings not applied: %s", got)
				}
				write([]byte(`{"type":"response.created","response":{"id":"r3","output":[]}}`))
				write([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"STEER_OK"}]}}`))
				write([]byte(`{"type":"response.completed","response":{"id":"r3","output":[]}}`))
				// Idle steering must also reach the same live upstream connection.
				got = read()
				if gjson.GetBytes(got, "previous_response_id").String() != "missing" {
					t.Errorf("idle control missing: %s", got)
				}
				write(failed)
				// Keep the socket alive until the client closes it; no automatic replay.
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			input := make(chan core.WebsocketInput, 8)
			ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)
			exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{ID: "A", Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "websockets": "true"}}
			req := core.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[],"instructions":"Initial settings"}`)}
			opts := core.Options{SourceFormat: sdktranslator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "duplex-test"}}
			result, err := exec.ExecuteStream(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for !seen["failed"] {
				select {
				case chunk, ok := <-result.Chunks:
					if !ok {
						t.Fatal("stream ended before all responses")
					}
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					event := gjson.GetBytes(chunk.Payload, "type").String()
					id := gjson.GetBytes(chunk.Payload, "response.id").String()
					switch {
					case event == "response.created" && id == "r1":
						input <- core.WebsocketInput{Payload: steer}
					case event == "response.steer.accepted" && gjson.GetBytes(chunk.Payload, "steer.id").String() == "s1":
						if !bytes.Equal(chunk.Payload, accepted) {
							t.Fatal("accepted event changed")
						}
						seen["accepted"] = true
					case event == boundary && id == "r1":
						seen["boundary"] = true
					case event == "response.created" && id == "r2":
						input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"r2","input":"Use tool result"}`)}
					case event == "response.steer.pending":
						if !bytes.Equal(chunk.Payload, pending) {
							t.Fatal("pending event changed")
						}
						seen["pending"] = true
						input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","previous_response_id":"r2","instructions":"New settings","input":[{"type":"function_call_output","call_id":"c1","output":"ok"}]}`)}
					case event == "response.completed" && id == "r3":
						if gjson.GetBytes(chunk.Payload, "response.output.0.content.0.text").String() != "STEER_OK" {
							t.Fatal("successor output missing or contaminated by preceding response")
						}
						seen["successor"] = true
						input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"missing","input":"recover me"}`)}
					case event == "response.steer.failed":
						if !bytes.Equal(chunk.Payload, failed) {
							t.Fatal("failed input changed")
						}
						seen["failed"] = true
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			cancel()
			select {
			case <-serverDone:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream did not close")
			}
			for range result.Chunks {
			}
			if connections.Load() != 1 {
				t.Fatalf("connections=%d", connections.Load())
			}
			for _, key := range []string{"accepted", "boundary", "pending", "successor", "failed"} {
				if !seen[key] {
					t.Fatalf("missing %s", key)
				}
			}
			select {
			case err := <-serverErrors:
				t.Fatal(err)
			default:
			}
		})
	}
}

func TestCodexDuplexAppendInheritsContextAndInstructions(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		// 1. Read initial create
		_, first, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read first create: %v", err)
			return
		}
		if gjson.GetBytes(first, "instructions").String() != "Initial system instructions" {
			t.Errorf("first instructions = %q", gjson.GetBytes(first, "instructions").String())
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-1","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`))

		// 2. Read subsequent append (which client sent omitting previous_response_id, model, and instructions)
		_, second, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read second create (from append): %v", err)
			return
		}
		if got := gjson.GetBytes(second, "type").String(); got != "response.create" {
			t.Errorf("second type = %q, want response.create", got)
		}
		if got := gjson.GetBytes(second, "previous_response_id").String(); got != "resp-1" {
			t.Errorf("second previous_response_id = %q, want resp-1", got)
		}
		if got := gjson.GetBytes(second, "model").String(); got != "gpt-6-astra" {
			t.Errorf("second model = %q, want gpt-6-astra", got)
		}
		if got := gjson.GetBytes(second, "instructions").String(); got != "Initial system instructions" {
			t.Errorf("second instructions = %q, want 'Initial system instructions'", got)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-2","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[]}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := make(chan core.WebsocketInput, 4)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

	exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := &cliproxyauth.Auth{ID: "append-test-auth", Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "websockets": "true"}}
	req := core.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"turn 1"}],"instructions":"Initial system instructions"}`),
	}
	opts := core.Options{SourceFormat: sdktranslator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "append-test"}}

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		eventType := gjson.GetBytes(chunk.Payload, "type").String()
		eventID := gjson.GetBytes(chunk.Payload, "response.id").String()
		if eventType == "response.completed" && eventID == "resp-1" {
			// Client sends response.append omitting previous_response_id, model, instructions
			appendPayload := []byte(`{"type":"response.append","input":[{"role":"user","content":"turn 2"}]}`)
			input <- core.WebsocketInput{Payload: appendPayload}
		}
		if eventType == "response.completed" && eventID == "resp-2" {
			cancel()
			break
		}
	}

	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish")
	}
}

func TestCodexDuplexStandaloneCreateDoesNotInheritParentID(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		// 1. Read first create
		_, _, err = conn.ReadMessage()
		if err != nil {
			t.Errorf("read first create: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-1","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[]}}`))

		// 2. Read second create (standalone / full history replacement without previous_response_id)
		_, second, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read second create: %v", err)
			return
		}
		if got := gjson.GetBytes(second, "type").String(); got != "response.create" {
			t.Errorf("second type = %q, want response.create", got)
		}
		if gjson.GetBytes(second, "previous_response_id").Exists() {
			t.Errorf("standalone create must not gain previous_response_id: %s", second)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp-2","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[]}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := make(chan core.WebsocketInput, 4)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

	exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := &cliproxyauth.Auth{ID: "standalone-create-test", Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "websockets": "true"}}
	req := core.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"turn 1"}]}`),
	}
	opts := core.Options{SourceFormat: sdktranslator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "standalone-test"}}

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		eventType := gjson.GetBytes(chunk.Payload, "type").String()
		eventID := gjson.GetBytes(chunk.Payload, "response.id").String()
		if eventType == "response.completed" && eventID == "resp-1" {
			// Client sends standalone response.create without previous_response_id
			standalonePayload := []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"role":"user","content":"turn 2 standalone"}]}`)
			input <- core.WebsocketInput{Payload: standalonePayload}
		}
		if eventType == "response.completed" && eventID == "resp-2" {
			cancel()
			break
		}
	}

	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish")
	}
}

func TestCodexDuplexQueuedCreateDoesNotBlockSubsequentSteer(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		// 1. Read first create
		_, first, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read first create: %v", err)
			return
		}
		if gjson.GetBytes(first, "type").String() != "response.create" {
			t.Errorf("expected response.create: %s", first)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`))

		// Read first steer for r1
		_, steer1, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read steer1: %v", err)
			return
		}
		if gjson.GetBytes(steer1, "type").String() != "response.steer" {
			t.Errorf("expected response.steer: %s", steer1)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[],"incomplete_details":{"reason":"steered"}}}`))

		// Emit automatic successor (sets automaticActive = true)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"auto-1","previous_response_id":"r1","output":[]}}`))

		// Client will send next create (blocked by automaticActive) followed by steer for auto-1.
		// Upstream expects steering to arrive NEXT, proving create did not block the steer!
		_, steerFrame, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		if got := gjson.GetBytes(steerFrame, "type").String(); got != "response.steer" {
			t.Errorf("expected response.steer to arrive before blocked create, got: %s", steerFrame)
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer.accepted","steer":{"id":"s2","previous_response_id":"auto-1"}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"auto-1","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"auto-2","previous_response_id":"auto-1","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"auto-2","output":[]}}`))

		// Now that automatic successor is completed, the queued create is delivered to upstream
		_, nextCreate, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read queued create: %v", err)
			return
		}
		if got := gjson.GetBytes(nextCreate, "type").String(); got != "response.create" {
			t.Errorf("queued create type = %q, want response.create", got)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r2","output":[]}}`))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r2","output":[]}}`))
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	input := make(chan core.WebsocketInput, 8)
	ctx = core.WithWebsocketInput(core.WithDownstreamWebsocket(ctx), input)

	exec := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{ResponseSteering: true}})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	auth := &cliproxyauth.Auth{ID: "steer-blocking-test", Provider: "codex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "websockets": "true"}}
	req := core.Request{
		Model:   "gpt-6-astra",
		Payload: []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"start"}]}`),
	}
	opts := core.Options{SourceFormat: sdktranslator.FromString("codex"), Metadata: map[string]any{core.ExecutionSessionMetadataKey: "steer-block-test"}}

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		eventType := gjson.GetBytes(chunk.Payload, "type").String()
		eventID := gjson.GetBytes(chunk.Payload, "response.id").String()
		if eventType == "response.created" && eventID == "r1" {
			input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"r1","input":"steer 1"}`)}
		}
		if eventType == "response.created" && eventID == "auto-1" {
			// During automatic successor generation, send a create first, followed by steer:
			input <- core.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"auto-1","input":[{"role":"user","content":"queued next"}]}`)}
			input <- core.WebsocketInput{Payload: []byte(`{"type":"response.steer","previous_response_id":"auto-1","input":"steer 2 in flight"}`)}
		}
		if eventType == "response.completed" && eventID == "r2" {
			cancel()
			break
		}
	}

	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish")
	}
}
