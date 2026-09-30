package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	wsPoisonedSearchCallID = "fc_call_function_olq4gx3ow6cs_1"
	wsRepairedSearchCallID = "tsc_call_function_olq4gx3ow6cs_1"
)

func wsPollutedCreateFrame(extra string) []byte {
	return []byte(`{"type":"response.create","model":"gpt-6-astra"` + extra + `,"input":[` +
		`{"type":"tool_search_call","id":"` + wsPoisonedSearchCallID + `","call_id":"s1","arguments":{"query":"x"},"execution":"client"},` +
		`{"type":"tool_search_output","id":"tso_out_1","call_id":"s1","execution":"client","tools":[]}]}`)
}

// duplexSteeringConfig enables the successor-frame path a duplex connection
// uses for every create after the first response.
func duplexSteeringConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	return cfg
}

// The prepared body is what the executor would actually write, so the repair
// has to be visible there and not only in the request object.
func TestCodexWebsocketPreparedItemIDRepair(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		enabled bool
		want    string
	}{
		{"enabled", true, wsRepairedSearchCallID},
		{"disabled", false, wsPoisonedSearchCallID},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &config.Config{}
			if !testCase.enabled {
				disabled := false
				cfg.ResponsesTools.Enabled = &disabled
			}
			exec := NewCodexWebsocketsExecutor(cfg)
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			auth := &cliproxyauth.Auth{ID: "A", Provider: "codex", Attributes: map[string]string{
				"api_key": "test", "base_url": "https://example.invalid", "websockets": "true",
			}}
			payload := []byte(`{"model":"gpt-6-astra","input":[` +
				`{"type":"tool_search_call","id":"` + wsPoisonedSearchCallID + `","call_id":"s1","arguments":{"query":"x"},"execution":"client"}]}`)
			prepared, err := exec.prepareCodexWebsocketStream(context.Background(), auth,
				cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if got := gjson.GetBytes(prepared.clientBody, "input.0.id").String(); got != testCase.want {
				t.Fatalf("prepared id = %q, want %q", got, testCase.want)
			}
			if got := gjson.GetBytes(prepared.originalPayload, "input.0.id").String(); got != testCase.want {
				t.Fatalf("original payload id = %q, want %q", got, testCase.want)
			}
			if !testCase.enabled {
				// The gate must not have rewritten the caller's slice either.
				if got := gjson.GetBytes(payload, "input.0.id").String(); got != wsPoisonedSearchCallID {
					t.Fatalf("caller payload was mutated: %s", payload)
				}
			}
		})
	}
}

// A healthy first frame followed by a polluted successor proves the repair
// runs on the second write, not only on the initial request.
func TestCodexDuplexCreateRepairsSuccessorItemIDs(t *testing.T) {
	successorBody := make(chan string, 1)
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		read := func() []byte {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("upstream read: %v", errRead)
				return nil
			}
			return payload
		}
		first := read()
		if gjson.GetBytes(first, "type").String() != "response.create" {
			t.Errorf("initial frame: %s", first)
		}
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		// The connection accepts a successor create only once the previous
		// response is terminal, so the frame under test is the second write.
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		successor := read()
		successorBody <- string(successor)
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.completed","response":{"id":"r2","output":[]}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	input := make(chan cliproxyexecutor.WebsocketInput, 4)
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
	exec := NewCodexWebsocketsExecutor(duplexSteeringConfig())
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	credential := &cliproxyauth.Auth{ID: "A", Provider: "codex", Attributes: map[string]string{
		"api_key": "test", "base_url": server.URL, "websockets": "true",
	}}
	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()},
	}
	result, err := exec.ExecuteStream(ctx, credential, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	initialDone := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		kind := gjson.GetBytes(chunk.Payload, "type").String()
		id := gjson.GetBytes(chunk.Payload, "response.id").String()
		if kind == "response.completed" && id == "r1" && !initialDone {
			initialDone = true
			input <- cliproxyexecutor.WebsocketInput{Payload: wsPollutedCreateFrame("")}
		}
		if kind == "response.completed" && id == "r2" {
			cancel()
			break
		}
	}
	if !initialDone {
		t.Fatal("the initial response never completed")
	}
	body := <-successorBody
	if got := gjson.Get(body, "input.0.id").String(); got != wsRepairedSearchCallID {
		t.Fatalf("successor frame was not repaired: %s", body)
	}
	if bytes.Contains([]byte(body), []byte(wsPoisonedSearchCallID)) {
		t.Fatalf("successor frame still carries the polluted id: %s", body)
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not close")
	}
}

// An append inherits its parent response, which makes the history opaque. A
// frame that would need repair is refused locally and never sent, because the
// proxy cannot know which stored item the parent holds.
func TestCodexDuplexAppendRejectsOpaqueItemIDRepair(t *testing.T) {
	var upstreamFrames atomic.Int32
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, first, _ := conn.ReadMessage()
		upstreamFrames.Add(1)
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		// Only a healthy follow-up frame may reach the upstream.
		_, second, errRead := conn.ReadMessage()
		if errRead == nil {
			upstreamFrames.Add(1)
			if gjson.GetBytes(second, "type").String() != "response.create" ||
				gjson.GetBytes(second, "input.0.id").String() != "msg_1" {
				t.Errorf("unexpected follow-up frame: %s", second)
			}
			_ = conn.WriteMessage(websocket.TextMessage,
				[]byte(`{"type":"response.created","response":{"id":"r2","output":[]}}`))
			_ = conn.WriteMessage(websocket.TextMessage,
				[]byte(`{"type":"response.completed","response":{"id":"r2","output":[]}}`))
		}
		_, _, _ = conn.ReadMessage()
		_ = first
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	input := make(chan cliproxyexecutor.WebsocketInput, 4)
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
	exec := NewCodexWebsocketsExecutor(duplexSteeringConfig())
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	credential := &cliproxyauth.Auth{ID: "A", Provider: "codex", Attributes: map[string]string{
		"api_key": "test", "base_url": server.URL, "websockets": "true",
	}}
	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()},
	}
	result, err := exec.ExecuteStream(ctx, credential, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	sawRejection := false
	initialDone := false
	loopEnded := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		kind := gjson.GetBytes(chunk.Payload, "type").String()
		id := gjson.GetBytes(chunk.Payload, "response.id").String()
		switch {
		case kind == "response.completed" && id == "r1" && !initialDone:
			initialDone = true
			// An append inherits its parent response, which makes the replayed
			// history opaque, so this frame must be refused locally.
			input <- cliproxyexecutor.WebsocketInput{
				Payload: []byte(`{"type":"response.append","model":"gpt-6-astra","input":[` +
					`{"type":"tool_search_call","id":"` + wsPoisonedSearchCallID + `","call_id":"s1","arguments":{},"execution":"client"}]}`),
			}
			// A well-formed frame right after proves the refusal did not wedge
			// the connection or leave the frame in the pending queue.
			input <- cliproxyexecutor.WebsocketInput{
				Payload: []byte(`{"type":"response.create","model":"gpt-6-astra","input":[{"type":"message","id":"msg_1","role":"user","content":"hi"}]}`),
			}
		case kind == "error":
			if got := gjson.GetBytes(chunk.Payload, "status").Int(); got != 422 {
				t.Fatalf("local rejection status = %d, want 422: %s", got, chunk.Payload)
			}
			sawRejection = true
		case kind == "response.completed" && id == "r2":
			cancel()
			loopEnded = true
		}
		if loopEnded {
			break
		}
	}
	if !initialDone || !sawRejection {
		t.Fatalf("initialDone=%t rejection=%t", initialDone, sawRejection)
	}
	if got := upstreamFrames.Load(); got != 2 {
		t.Fatalf("upstream saw %d frames, want the initial create and the healthy follow-up only", got)
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not close")
	}
}

// A steer frame keeps its exact bytes when it needs no repair, and a polluted
// one is refused before it becomes steering state.
func TestCodexDuplexSteerItemIDRepairPreservesState(t *testing.T) {
	steerSeen := make(chan string, 2)
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		read := func() []byte {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("upstream read: %v", errRead)
				return nil
			}
			return payload
		}
		_ = read()
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.created","response":{"id":"r1","output":[]}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		healthy := read()
		steerSeen <- string(healthy)
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		// The refused steer must never arrive. This read therefore blocks until
		// the client closes the connection after seeing the local rejection.
		if _, extra, errRead := conn.ReadMessage(); errRead == nil {
			steerSeen <- string(extra)
		}
	}))
	defer server.Close()

	healthySteer := `{"type":"response.steer","previous_response_id":"r1","input":[{"role":"user","content":[{"type":"input_text","text":"go on"}]}]}`
	pollutedSteer := `{"type":"response.steer","previous_response_id":"r1","input":[{"type":"tool_search_call","id":"` +
		wsPoisonedSearchCallID + `","call_id":"s1","arguments":{},"execution":"client"}]}`

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	input := make(chan cliproxyexecutor.WebsocketInput, 4)
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
	exec := NewCodexWebsocketsExecutor(duplexSteeringConfig())
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	credential := &cliproxyauth.Auth{ID: "A", Provider: "codex", Attributes: map[string]string{
		"api_key": "test", "base_url": server.URL, "websockets": "true",
	}}
	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"model":"gpt-6-astra","input":[]}`)}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Metadata:     map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()},
	}
	result, err := exec.ExecuteStream(ctx, credential, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	accepted, rejected := false, false
	for !rejected {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream ended before the polluted steer was refused")
			}
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			switch gjson.GetBytes(chunk.Payload, "type").String() {
			case "response.created":
				if !accepted {
					input <- cliproxyexecutor.WebsocketInput{Payload: []byte(healthySteer)}
				}
			case "response.steer.accepted":
				accepted = true
				input <- cliproxyexecutor.WebsocketInput{Payload: []byte(pollutedSteer)}
			case "error":
				if gjson.GetBytes(chunk.Payload, "status").Int() != 422 {
					t.Fatalf("polluted steer status = %s", chunk.Payload)
				}
				rejected = true
			}
		case <-ctx.Done():
			t.Fatal("the polluted steer was never refused")
		}
	}
	if !accepted || !rejected {
		t.Fatalf("accepted=%t rejected=%t", accepted, rejected)
	}
	if got := <-steerSeen; got != healthySteer {
		t.Fatalf("healthy steer was rewritten:\n got %s\nwant %s", got, healthySteer)
	}
	select {
	case extra := <-steerSeen:
		t.Fatalf("the refused steer reached the upstream: %s", extra)
	default:
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not close")
	}
}
