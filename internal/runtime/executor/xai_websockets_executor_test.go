package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIWebsocketsExecuteStreamSendsResponseCreateWithPreviousResponseID(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xai-token" {
			t.Errorf("Authorization = %q, want Bearer xai-token", got)
		}
		if got := r.Header.Get("x-grok-conv-id"); got != "execution-session-1" {
			t.Errorf("x-grok-conv-id = %q, want execution-session-1", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp-xai-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	req := cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","stream":true,"previous_response_id":"resp-prev","instructions":"system prompt","input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session-1",
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("type = %q, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-prev" {
			t.Fatalf("previous_response_id = %q, want resp-prev; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "input.#").Int(); got != 1 {
			t.Fatalf("input count = %d, want 1; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "input.0.type").String(); got != "function_call_output" {
			t.Fatalf("input.0.type = %q, want function_call_output; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "input.0.call_id").String(); got != "call-1" {
			t.Fatalf("input.0.call_id = %q, want call-1; payload=%s", got, payload)
		}
		if gjson.GetBytes(payload, "stream").Exists() {
			t.Fatalf("stream must be omitted for xAI websocket payload: %s", payload)
		}
		if gjson.GetBytes(payload, "instructions").Exists() {
			t.Fatalf("instructions must be omitted when previous_response_id is set: %s", payload)
		}
		if got := gjson.GetBytes(payload, "prompt_cache_key").String(); got != "execution-session-1" {
			t.Fatalf("prompt_cache_key = %q, want execution-session-1; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "store").Bool(); !got {
			t.Fatalf("store = false, want true; payload=%s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before completed chunk")
		}
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v", chunk.Err)
		}
		if got := gjson.GetBytes(bytes.TrimSpace(chunk.Payload), "type").String(); got != "response.completed" {
			t.Fatalf("chunk type = %q, want response.completed; payload=%s", got, chunk.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for completed chunk")
	}
}

func TestXAIWebsocketsExecuteStreamNormalizesReasoningTextEvents(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		events := [][]byte{
			[]byte(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","summary":[]}}`),
			[]byte(`{"type":"response.content_part.added","sequence_number":2,"item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`),
			[]byte(`{"type":"response.reasoning_text.delta","sequence_number":3,"item_id":"rs_1","output_index":0,"content_index":0,"delta":"thinking"}`),
			[]byte(`{"type":"response.reasoning_text.done","sequence_number":4,"item_id":"rs_1","output_index":0,"content_index":0,"text":"thinking"}`),
			[]byte(`{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"thinking"}]}}`),
			[]byte(`{"type":"response.completed","sequence_number":6,"response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"grok-4.3","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`),
		}
		for _, event := range events {
			if errWrite := conn.WriteMessage(websocket.TextMessage, event); errWrite != nil {
				t.Errorf("write websocket event: %v", errWrite)
				return
			}
		}
	}))
	defer server.Close()

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatCodex,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var streamed bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	output := streamed.String()
	if strings.Contains(output, "reasoning_text") {
		t.Fatalf("stream contains xAI reasoning_text shape: %s", output)
	}
	for _, want := range []string{
		`"type":"response.reasoning_summary_part.added"`,
		`"type":"response.reasoning_summary_text.delta"`,
		`"type":"response.reasoning_summary_text.done"`,
		`"type":"response.reasoning_summary_part.done"`,
		`"part":{"type":"summary_text","text":"thinking"}`,
		`"summary_index":0`,
		`"summary":[{"type":"summary_text","text":"thinking"}]`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stream missing %q: %s", want, output)
		}
	}
	textDoneIndex := strings.Index(output, `"type":"response.reasoning_summary_text.done"`)
	partDoneIndex := strings.Index(output, `"type":"response.reasoning_summary_part.done"`)
	if textDoneIndex < 0 || partDoneIndex < 0 || textDoneIndex > partDoneIndex {
		t.Fatalf("reasoning done events are out of order: %s", output)
	}
}

func TestXAIWebsocketsExecuteStreamRewritesRepeatedResponseIDForDownstream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPreviousIDs := make(chan string, 3)
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		for i := 0; i < 3; i++ {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read upstream websocket message: %v", errRead)
				return
			}
			previousID := gjson.GetBytes(payload, "previous_response_id").String()
			capturedPreviousIDs <- previousID
			completed := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-real","previous_response_id":%q,"output":[{"id":"rs_resp-real","type":"reasoning","status":"completed"}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`, previousID))
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				t.Errorf("write completed websocket message: %v", errWrite)
				return
			}
		}
		<-releaseServer
	}))
	defer server.Close()
	defer close(releaseServer)

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-id-map",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "xai-id-map-session",
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	runRequest := func(previousID string) (string, string, string) {
		body := []byte(`{"model":"grok-4.3","input":[{"type":"message","role":"user","content":"hello"}]}`)
		if previousID != "" {
			body = []byte(fmt.Sprintf(`{"model":"grok-4.3","previous_response_id":%q,"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`, previousID))
		}
		result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4.3", Payload: body}, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed before completed chunk")
			}
			if chunk.Err != nil {
				t.Fatalf("chunk error = %v", chunk.Err)
			}
			payload := bytes.TrimSpace(chunk.Payload)
			return gjson.GetBytes(payload, "response.id").String(),
				gjson.GetBytes(payload, "response.output.0.id").String(),
				gjson.GetBytes(payload, "response.previous_response_id").String()
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for completed chunk")
		}
		return "", "", ""
	}

	firstDownstreamID, firstOutputID, firstResponsePrevious := runRequest("")
	if firstDownstreamID != "resp-real" {
		t.Fatalf("first downstream id = %q, want resp-real", firstDownstreamID)
	}
	if firstOutputID != "rs_resp-real" {
		t.Fatalf("first output item id = %q, want rs_resp-real", firstOutputID)
	}
	if firstResponsePrevious != "" {
		t.Fatalf("first response previous_response_id = %q, want empty", firstResponsePrevious)
	}
	firstUpstreamPrevious := <-capturedPreviousIDs
	if firstUpstreamPrevious != "" {
		t.Fatalf("first upstream previous_response_id = %q, want empty", firstUpstreamPrevious)
	}

	secondDownstreamID, secondOutputID, secondResponsePrevious := runRequest(firstDownstreamID)
	if secondDownstreamID == "" || secondDownstreamID == "resp-real" {
		t.Fatalf("second downstream id = %q, want synthetic id different from resp-real", secondDownstreamID)
	}
	if secondOutputID == "rs_resp-real" || !strings.Contains(secondOutputID, secondDownstreamID) {
		t.Fatalf("second output item id = %q, want rewritten id containing %q", secondOutputID, secondDownstreamID)
	}
	if secondResponsePrevious != firstDownstreamID {
		t.Fatalf("second response previous_response_id = %q, want %q", secondResponsePrevious, firstDownstreamID)
	}
	secondUpstreamPrevious := <-capturedPreviousIDs
	if secondUpstreamPrevious != "resp-real" {
		t.Fatalf("second upstream previous_response_id = %q, want resp-real", secondUpstreamPrevious)
	}

	thirdDownstreamID, thirdOutputID, thirdResponsePrevious := runRequest(secondDownstreamID)
	if thirdDownstreamID == "" || thirdDownstreamID == "resp-real" || thirdDownstreamID == secondDownstreamID {
		t.Fatalf("third downstream id = %q, want a new synthetic id", thirdDownstreamID)
	}
	if thirdOutputID == "rs_resp-real" || !strings.Contains(thirdOutputID, thirdDownstreamID) {
		t.Fatalf("third output item id = %q, want rewritten id containing %q", thirdOutputID, thirdDownstreamID)
	}
	if thirdResponsePrevious != secondDownstreamID {
		t.Fatalf("third response previous_response_id = %q, want %q", thirdResponsePrevious, secondDownstreamID)
	}
	thirdUpstreamPrevious := <-capturedPreviousIDs
	if thirdUpstreamPrevious != "resp-real" {
		t.Fatalf("third upstream previous_response_id = %q, want resp-real", thirdUpstreamPrevious)
	}
}

func TestXAIWebsocketsExecuteStreamRewritesRepeatedResponseIDWithoutPreviousResponseID(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPreviousIDs := make(chan string, 2)
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		for i := 0; i < 2; i++ {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read upstream websocket message: %v", errRead)
				return
			}
			capturedPreviousIDs <- gjson.GetBytes(payload, "previous_response_id").String()
			completed := []byte(`{"type":"response.completed","response":{"id":"resp-real","output":[{"id":"msg_resp-real","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				t.Errorf("write completed websocket message: %v", errWrite)
				return
			}
		}
		<-releaseServer
	}))
	defer server.Close()
	defer close(releaseServer)

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-id-map-no-prev",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "xai-id-map-no-prev-session",
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	runRequest := func(content string) (string, string) {
		body := []byte(fmt.Sprintf(`{"model":"grok-4.3","input":[{"type":"message","role":"user","content":%q}]}`, content))
		result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4.3", Payload: body}, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed before completed chunk")
			}
			if chunk.Err != nil {
				t.Fatalf("chunk error = %v", chunk.Err)
			}
			payload := bytes.TrimSpace(chunk.Payload)
			return gjson.GetBytes(payload, "response.id").String(),
				gjson.GetBytes(payload, "response.output.0.id").String()
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for completed chunk")
		}
		return "", ""
	}

	firstDownstreamID, firstOutputID := runRequest("first")
	if firstDownstreamID != "resp-real" {
		t.Fatalf("first downstream id = %q, want resp-real", firstDownstreamID)
	}
	if firstOutputID != "msg_resp-real" {
		t.Fatalf("first output item id = %q, want msg_resp-real", firstOutputID)
	}
	if firstUpstreamPrevious := <-capturedPreviousIDs; firstUpstreamPrevious != "" {
		t.Fatalf("first upstream previous_response_id = %q, want empty", firstUpstreamPrevious)
	}

	secondDownstreamID, secondOutputID := runRequest("second")
	if secondDownstreamID == "" || secondDownstreamID == "resp-real" {
		t.Fatalf("second downstream id = %q, want synthetic id different from resp-real", secondDownstreamID)
	}
	if secondOutputID == "msg_resp-real" || !strings.Contains(secondOutputID, secondDownstreamID) {
		t.Fatalf("second output item id = %q, want rewritten id containing %q", secondOutputID, secondDownstreamID)
	}
	if secondUpstreamPrevious := <-capturedPreviousIDs; secondUpstreamPrevious != "" {
		t.Fatalf("second upstream previous_response_id = %q, want empty", secondUpstreamPrevious)
	}
}

func TestXAIWebsocketsExecuteStreamCompactionTriggerUsesHTTPCompactWithRecordedContext(t *testing.T) {
	nativeEncryptedContent := testValidGrokEncryptedContent()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedWebsocketPayload := make(chan []byte, 1)
	capturedCompactPayload := make(chan []byte, 1)
	compactResponse := []byte(fmt.Sprintf(`{"id":"resp_compact","model":"grok-4.3","output":[{"type":"compaction","encrypted_content":%q}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`, nativeEncryptedContent))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade websocket: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()

			for i := 0; i < 2; i++ {
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Errorf("read upstream websocket message: %v", errRead)
					return
				}
				capturedWebsocketPayload <- bytes.Clone(payload)
				completed := []byte(`{"type":"response.completed","response":{"id":"resp-real","output":[{"type":"message","id":"out-1","role":"assistant","content":"first answer"}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
				if i == 1 {
					completed = []byte(`{"type":"response.completed","response":{"id":"resp-after-compact","output":[{"type":"message","id":"out-2","role":"assistant","content":"second answer"}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
				}
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					t.Errorf("write completed websocket message: %v", errWrite)
					return
				}
			}
		case "/responses/compact":
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Errorf("read compact body: %v", errRead)
				return
			}
			capturedCompactPayload <- bytes.Clone(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(compactResponse)
		default:
			t.Errorf("path = %q, want /responses", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-compaction",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "xai-compaction-session",
		},
	}

	result, err := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","stream":true,"input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream first turn error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-capturedWebsocketPayload:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("type = %q, want response.create; payload=%s", got, payload)
		}
		input := gjson.GetBytes(payload, "input")
		if !input.IsArray() || len(input.Array()) != 1 {
			t.Fatalf("input = %s, want one first-turn item", input.Raw)
		}
		if gjson.GetBytes(payload, "stream").Exists() {
			t.Fatalf("stream must be omitted for xAI websocket payload: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}

	compactResult, err := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","stream":true,"previous_response_id":"resp-real-xai-1","input":[{"type":"compaction_trigger"}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream compaction trigger error: %v", err)
	}
	for chunk := range compactResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("compact stream chunk error = %v", chunk.Err)
		}
	}

	select {
	case payload := <-capturedCompactPayload:
		if xaiInputHasItemType(payload, "compaction_trigger") {
			t.Fatalf("compaction_trigger reached xai compact body: %s", payload)
		}
		input := gjson.GetBytes(payload, "input")
		if !input.IsArray() || len(input.Array()) != 2 {
			t.Fatalf("compact input = %s, want first request input plus response output", input.Raw)
		}
		if got := input.Array()[0].Get("id").String(); got != "msg-1" {
			t.Fatalf("compact input[0].id = %q, want msg-1; payload=%s", got, payload)
		}
		if got := input.Array()[1].Get("id").String(); got != "out-1" {
			t.Fatalf("compact input[1].id = %q, want out-1; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "" {
			t.Fatalf("compact previous_response_id = %q, want empty; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for compact HTTP payload")
	}

	nextResult, err := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), auth, cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","stream":true,"previous_response_id":"resp_compact","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream post-compaction turn error: %v", err)
	}
	for chunk := range nextResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("post-compaction stream chunk error = %v", chunk.Err)
		}
	}
	select {
	case payload := <-capturedWebsocketPayload:
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "" {
			t.Fatalf("post-compaction previous_response_id = %q, want empty; payload=%s", got, payload)
		}
		input := gjson.GetBytes(payload, "input")
		if !input.IsArray() || len(input.Array()) != 2 {
			t.Fatalf("post-compaction input = %s, want compaction item plus new message", input.Raw)
		}
		if got := input.Array()[0].Get("type").String(); got != "compaction" {
			t.Fatalf("post-compaction input[0].type = %q, want compaction; payload=%s", got, payload)
		}
		if got := input.Array()[0].Get("encrypted_content").String(); got != nativeEncryptedContent {
			t.Fatalf("post-compaction input[0].encrypted_content = %q, want native sample; payload=%s", got, payload)
		}
		if got := input.Array()[1].Get("id").String(); got != "msg-2" {
			t.Fatalf("post-compaction input[1].id = %q, want msg-2; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for post-compaction websocket payload")
	}
}

func TestBuildXAIWebsocketRequestBodySetsStoreAndKeepsPromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","stream":true,"stream_options":{"include_usage":true},"background":true,"prompt_cache_key":"cache-1","previous_response_id":"resp-prev","instructions":"system prompt","input":[{"type":"message","role":"user","content":"hello"}]}`)

	payload := buildXAIWebsocketRequestBody(body)

	if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
		t.Fatalf("type = %q, want response.create; payload=%s", got, payload)
	}
	if gjson.GetBytes(payload, "stream").Exists() {
		t.Fatalf("stream must be omitted for xAI websocket payload: %s", payload)
	}
	if gjson.GetBytes(payload, "stream_options").Exists() {
		t.Fatalf("stream_options must be omitted for xAI websocket payload: %s", payload)
	}
	if gjson.GetBytes(payload, "background").Exists() {
		t.Fatalf("background must be omitted for xAI websocket payload: %s", payload)
	}
	if got := gjson.GetBytes(payload, "prompt_cache_key").String(); got != "cache-1" {
		t.Fatalf("prompt_cache_key = %q, want cache-1; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "store").Bool(); !got {
		t.Fatalf("store = false, want true; payload=%s", payload)
	}
	if gjson.GetBytes(payload, "instructions").Exists() {
		t.Fatalf("instructions must be omitted when previous_response_id is set: %s", payload)
	}
}

func TestXAIWebsocketsExecuteStreamCompletesGenerateFalseWarmup(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)
		created := []byte(`{"type":"response.created","response":{"id":"resp-warmup-1","object":"response","status":"in_progress","output":[]}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, created); errWrite != nil {
			t.Errorf("write created websocket message: %v", errWrite)
			return
		}
		<-releaseServer
	}))
	defer server.Close()
	defer close(releaseServer)

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-warmup",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	req := cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","generate":false,"input":[{"type":"message","role":"user","content":"warm up"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "generate").Bool(); got {
			t.Fatalf("generate = true, want false; payload=%s", payload)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("type = %q, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "store").Bool(); !got {
			t.Fatalf("store = false, want true; payload=%s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}

	var gotTypes []string
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				if len(gotTypes) != 2 {
					t.Fatalf("event types = %v, want response.created and response.completed", gotTypes)
				}
				return
			}
			if chunk.Err != nil {
				t.Fatalf("chunk error = %v", chunk.Err)
			}
			gotTypes = append(gotTypes, gjson.GetBytes(bytes.TrimSpace(chunk.Payload), "type").String())
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for warmup stream to close; event types so far: %v", gotTypes)
		}
	}
}

func TestXAIWebsocketsExecuteStreamHandshakeFreeUsageExhaustedSetsRetryAfter(t *testing.T) {
	body := []byte(`{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for now."}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		if _, errWrite := w.Write(body); errWrite != nil {
			t.Errorf("write handshake rejection: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-free-usage",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	req := cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}

	_, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want handshake rejection")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter for free-usage-exhausted handshake error: %#v", err)
	}
	if got := *retryable.RetryAfter(); got != 24*time.Hour {
		t.Fatalf("RetryAfter = %v, want 24h", got)
	}
	if got := err.Error(); got != string(body) {
		t.Fatalf("error payload = %q, want %q", got, body)
	}
}

func TestParseXAIWebsocketErrorFreeUsageExhaustedSetsRetryAfter(t *testing.T) {
	payload := []byte(`{"type":"error","status":429,"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage for now."}}`)
	err, ok := parseXAIWebsocketError(payload)
	if !ok {
		t.Fatal("expected xAI websocket error")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter for free-usage-exhausted websocket event: %#v", err)
	}
	if got := *retryable.RetryAfter(); got != 24*time.Hour {
		t.Fatalf("RetryAfter = %v, want 24h", got)
	}
	parsed := gjson.Parse(err.Error())
	if got := parsed.Get("status").Int(); got != http.StatusTooManyRequests {
		t.Fatalf("error status = %d, want 429; payload=%s", got, err)
	}
	if got := parsed.Get("error.code").String(); got != "subscription:free-usage-exhausted" {
		t.Fatalf("error code = %q, want free-usage-exhausted; payload=%s", got, err)
	}
}

func TestParseXAIWebsocketBareErrorFreeUsageExhaustedSetsRetryAfter(t *testing.T) {
	payload := []byte(`{"status":429,"error":{"code":"subscription:free-usage-exhausted","message":"You've used all the included free usage for now."}}`)
	err, ok := parseXAIWebsocketError(payload)
	if !ok {
		t.Fatal("expected bare xAI websocket error")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected RetryAfter for bare free-usage-exhausted websocket event: %#v", err)
	}
	if got := *retryable.RetryAfter(); got != 24*time.Hour {
		t.Fatalf("RetryAfter = %v, want 24h", got)
	}
	parsed := gjson.Parse(err.Error())
	if got := parsed.Get("type").String(); got != "error" {
		t.Fatalf("error type = %q, want error; payload=%s", got, err)
	}
	if got := parsed.Get("error.code").String(); got != "subscription:free-usage-exhausted" {
		t.Fatalf("error code = %q, want free-usage-exhausted; payload=%s", got, err)
	}
}

func TestXAIWebsocketsExecuteStreamStopsOnBareErrorPayload(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		payload := []byte(`{"error":{"message":"Request validation error: {\"code\":\"400\",\"error\":\"Argument not supported: instructions and previous_response_id together\"}","type":"api_error"}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, payload); errWrite != nil {
			t.Errorf("write error websocket message: %v", errWrite)
			return
		}
		<-releaseServer
	}))
	defer server.Close()
	defer close(releaseServer)

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-error",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	req := cliproxyexecutor.Request{
		Model:   "grok-4.3",
		Payload: []byte(`{"model":"grok-4.3","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before error chunk")
		}
		if chunk.Err == nil {
			t.Fatalf("chunk error = nil, want upstream error; payload=%s", chunk.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for bare upstream error")
	}
}

func TestXAIWebsocketsExecuteStreamSearchResultFilteringRequiresDualFlags(t *testing.T) {
	tests := []struct {
		name       string
		xaiConfig  config.XAIConfig
		wantHidden bool
	}{
		{
			name: "inject_and_hide",
			xaiConfig: config.XAIConfig{
				InjectBuildSearchTools:    true,
				HideInjectedSearchResults: true,
			},
			wantHidden: true,
		},
		{
			name: "inject_without_hide",
			xaiConfig: config.XAIConfig{
				InjectBuildSearchTools: true,
			},
		},
		{
			name: "hide_without_inject",
			xaiConfig: config.XAIConfig{
				HideInjectedSearchResults: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade websocket: %v", err)
					return
				}
				defer func() { _ = conn.Close() }()

				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Errorf("read upstream websocket message: %v", errRead)
					return
				}
				events := [][]byte{
					[]byte(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`),
					[]byte(`{"type":"response.web_search_call.in_progress","sequence_number":2,"output_index":0,"item_id":"ws_1"}`),
					[]byte(`{"type":"response.content_part.added","sequence_number":3,"output_index":0,"item_id":"ws_1","content_index":0,"part":{"type":"search_result","text":"hidden"}}`),
					[]byte(`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed"}}`),
					[]byte(`{"type":"response.output_item.added","sequence_number":5,"output_index":1,"item":{"id":"xs_1","type":"x_search_call","status":"in_progress"}}`),
					[]byte(`{"type":"response.x_search_call.in_progress","sequence_number":6,"output_index":1,"item_id":"xs_1"}`),
					[]byte(`{"type":"response.content_part.added","sequence_number":7,"output_index":1,"item_id":"xs_1","content_index":0,"part":{"type":"search_result","text":"hidden"}}`),
					[]byte(`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"id":"xs_1","type":"x_search_call","status":"completed"}}`),
					[]byte(`{"type":"response.output_item.added","sequence_number":9,"output_index":2,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`),
					[]byte(`{"type":"response.output_text.delta","sequence_number":10,"output_index":2,"item_id":"msg_1","content_index":0,"delta":"answer"}`),
					[]byte(`{"type":"response.output_item.done","sequence_number":11,"output_index":2,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}}`),
					[]byte(`{"type":"response.completed","sequence_number":12,"response":{"id":"resp_1","status":"completed","output":[{"id":"ws_1","type":"web_search_call","status":"completed"},{"id":"xs_1","type":"x_search_call","status":"completed"},{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`),
				}
				for _, event := range events {
					if errWrite := conn.WriteMessage(websocket.TextMessage, event); errWrite != nil {
						t.Errorf("write websocket event: %v", errWrite)
						return
					}
				}
			}))
			defer server.Close()

			exec := NewXAIWebsocketsExecutor(&config.Config{XAI: tt.xaiConfig})
			auth := &cliproxyauth.Auth{
				ID:       "xai-auth-search-filter",
				Provider: "xai",
				Attributes: map[string]string{
					"base_url":   server.URL,
					"websockets": "true",
				},
				Metadata: map[string]any{"access_token": "xai-token"},
			}
			result, err := exec.ExecuteStream(
				cliproxyexecutor.WithDownstreamWebsocket(context.Background()),
				auth,
				cliproxyexecutor.Request{
					Model:   "grok-4.3",
					Payload: []byte(`{"model":"grok-4.3","tools":[{"type":"web_search"}],"input":"hello"}`),
				},
				cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatOpenAIResponse,
					ResponseFormat: sdktranslator.FormatOpenAIResponse,
				},
			)
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}

			var events [][]byte
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
				events = append(events, bytes.Clone(bytes.TrimSpace(chunk.Payload)))
			}

			wantMessageIndex := int64(2)
			if tt.wantHidden {
				wantMessageIndex = 0
			}
			var sawSearchEvent, sawSearchResidual, sawCompleted bool
			messageIndexChecks := 0
			for _, event := range events {
				eventType := gjson.GetBytes(event, "type").String()
				itemType := gjson.GetBytes(event, "item.type").String()
				if strings.Contains(eventType, "web_search") || strings.Contains(eventType, "x_search") || itemType == "web_search_call" || itemType == "x_search_call" {
					sawSearchEvent = true
				}
				if itemID := gjson.GetBytes(event, "item_id").String(); itemID == "ws_1" || itemID == "xs_1" {
					sawSearchResidual = true
				}
				if gjson.GetBytes(event, "item.id").String() == "msg_1" || gjson.GetBytes(event, "item_id").String() == "msg_1" {
					messageIndexChecks++
					if got := gjson.GetBytes(event, "output_index").Int(); got != wantMessageIndex {
						t.Fatalf("message output_index = %d, want %d; event=%s", got, wantMessageIndex, event)
					}
				}
				if eventType != "response.completed" {
					continue
				}
				sawCompleted = true
				output := gjson.GetBytes(event, "response.output")
				wantOutputCount := int64(3)
				if tt.wantHidden {
					wantOutputCount = 1
				}
				if got := output.Get("#").Int(); got != wantOutputCount {
					t.Fatalf("completed output count = %d, want %d; event=%s", got, wantOutputCount, event)
				}
				if tt.wantHidden && output.Get("0.type").String() != "message" {
					t.Fatalf("completed output.0.type = %q, want message; event=%s", output.Get("0.type").String(), event)
				}
			}

			if !sawCompleted {
				t.Fatal("stream missing response.completed")
			}
			if messageIndexChecks != 3 {
				t.Fatalf("message output_index checks = %d, want 3", messageIndexChecks)
			}
			if tt.wantHidden {
				if sawSearchEvent || sawSearchResidual {
					t.Fatalf("hidden search events leaked: search=%v residual=%v events=%q", sawSearchEvent, sawSearchResidual, events)
				}
				return
			}
			if !sawSearchEvent || !sawSearchResidual {
				t.Fatalf("search events were filtered without dual flags: search=%v residual=%v events=%q", sawSearchEvent, sawSearchResidual, events)
			}
		})
	}
}

func TestXAIWebsocketsExecuteStreamRemapsCustomToolCalls(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		// Upstream xAI promotes custom tools to function_call + function_call_arguments.*.
		// Downstream Codex/Alma clients expect custom_tool_call + custom_tool_call_input.done.
		events := [][]byte{
			[]byte(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_custom","type":"function_call","name":"exec","call_id":"call_exec","arguments":"","status":"in_progress"}}`),
			[]byte(`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_custom","delta":"{\"input\":\"pwd\"}"}`),
			[]byte(`{"type":"response.function_call_arguments.done","sequence_number":3,"output_index":0,"item_id":"fc_custom","arguments":"{\"input\":\"pwd\"}"}`),
			[]byte(`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc_custom","type":"function_call","name":"exec","call_id":"call_exec","arguments":"{\"input\":\"pwd\"}","status":"completed"}}`),
			[]byte(`{"type":"response.output_item.added","sequence_number":5,"output_index":1,"item":{"id":"fc_lookup","type":"function_call","name":"lookup","call_id":"call_lookup","arguments":"","status":"in_progress"}}`),
			[]byte(`{"type":"response.function_call_arguments.delta","sequence_number":6,"output_index":1,"item_id":"fc_lookup","delta":"{\"q\":\"x\"}"}`),
			[]byte(`{"type":"response.function_call_arguments.done","sequence_number":7,"output_index":1,"item_id":"fc_lookup","arguments":"{\"q\":\"x\"}"}`),
			[]byte(`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"id":"fc_lookup","type":"function_call","name":"lookup","call_id":"call_lookup","arguments":"{\"q\":\"x\"}","status":"completed"}}`),
			[]byte(`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_custom","status":"completed","output":[{"id":"fc_custom","type":"function_call","name":"exec","call_id":"call_exec","arguments":"{\"input\":\"pwd\"}","status":"completed"},{"id":"fc_lookup","type":"function_call","name":"lookup","call_id":"call_lookup","arguments":"{\"q\":\"x\"}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`),
		}
		for _, event := range events {
			if errWrite := conn.WriteMessage(websocket.TextMessage, event); errWrite != nil {
				t.Errorf("write websocket event: %v", errWrite)
				return
			}
		}
	}))
	defer server.Close()

	exec := NewXAIWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "xai-auth-custom-remap",
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	// type=custom is promoted to function for xAI; response must remap only "exec".
	body := []byte(`{"model":"grok-4.3","tools":[{"type":"custom","name":"exec","description":"run shell"},{"type":"function","name":"lookup","description":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],"input":"run pwd"}`)
	result, err := exec.ExecuteStream(
		cliproxyexecutor.WithDownstreamWebsocket(context.Background()),
		auth,
		cliproxyexecutor.Request{Model: "grok-4.3", Payload: body},
		cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAIResponse,
			ResponseFormat: sdktranslator.FormatOpenAIResponse,
		},
	)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var events [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		events = append(events, bytes.Clone(bytes.TrimSpace(chunk.Payload)))
	}

	var (
		sawCustomAdded     bool
		sawCustomDoneItem  bool
		sawCustomInputDone bool
		sawFunctionDelta   bool
		sawFunctionDone    bool
		sawLookupFunction  bool
		sawCompleted       bool
	)
	for _, event := range events {
		eventType := gjson.GetBytes(event, "type").String()
		itemType := gjson.GetBytes(event, "item.type").String()
		itemName := gjson.GetBytes(event, "item.name").String()

		switch eventType {
		case "response.function_call_arguments.delta":
			// Custom tool deltas must be dropped; ordinary function deltas remain.
			if gjson.GetBytes(event, "output_index").Int() == 0 {
				t.Fatalf("custom tool function_call_arguments.delta leaked: %s", event)
			}
			sawFunctionDelta = true
		case "response.function_call_arguments.done":
			if gjson.GetBytes(event, "output_index").Int() == 0 {
				t.Fatalf("custom tool function_call_arguments.done leaked: %s", event)
			}
			sawFunctionDone = true
		case "response.custom_tool_call_input.done":
			if gjson.GetBytes(event, "output_index").Int() != 0 {
				t.Fatalf("unexpected custom_tool_call_input.done index: %s", event)
			}
			if got := gjson.GetBytes(event, "input").String(); got != "pwd" {
				t.Fatalf("custom_tool_call_input.done input = %q, want pwd; event=%s", got, event)
			}
			if gjson.GetBytes(event, "arguments").Exists() {
				t.Fatalf("custom_tool_call_input.done still has arguments: %s", event)
			}
			sawCustomInputDone = true
		case "response.output_item.added", "response.output_item.done":
			switch {
			case itemName == "exec":
				if itemType != "custom_tool_call" {
					t.Fatalf("exec item type = %q, want custom_tool_call; event=%s", itemType, event)
				}
				if eventType == "response.output_item.done" {
					if got := gjson.GetBytes(event, "item.input").String(); got != "pwd" {
						t.Fatalf("exec item.input = %q, want pwd; event=%s", got, event)
					}
					sawCustomDoneItem = true
				} else {
					sawCustomAdded = true
				}
			case itemName == "lookup":
				if itemType != "function_call" {
					t.Fatalf("lookup item type = %q, want function_call; event=%s", itemType, event)
				}
				sawLookupFunction = true
			}
		case "response.completed":
			sawCompleted = true
			out0 := gjson.GetBytes(event, "response.output.0")
			out1 := gjson.GetBytes(event, "response.output.1")
			if out0.Get("type").String() != "custom_tool_call" || out0.Get("name").String() != "exec" {
				t.Fatalf("completed.output.0 = %s, want custom_tool_call exec", out0.Raw)
			}
			if got := out0.Get("input").String(); got != "pwd" {
				t.Fatalf("completed.output.0.input = %q, want pwd", got)
			}
			if out1.Get("type").String() != "function_call" || out1.Get("name").String() != "lookup" {
				t.Fatalf("completed.output.1 = %s, want function_call lookup", out1.Raw)
			}
		}
	}

	if !sawCustomAdded || !sawCustomDoneItem {
		t.Fatalf("missing remapped custom item events: added=%v done=%v events=%q", sawCustomAdded, sawCustomDoneItem, events)
	}
	if !sawCustomInputDone {
		t.Fatalf("missing custom_tool_call_input.done; events=%q", events)
	}
	if !sawFunctionDelta || !sawFunctionDone {
		t.Fatalf("ordinary function argument stream missing: delta=%v done=%v events=%q", sawFunctionDelta, sawFunctionDone, events)
	}
	if !sawLookupFunction {
		t.Fatalf("lookup function_call missing; events=%q", events)
	}
	if !sawCompleted {
		t.Fatal("stream missing response.completed")
	}
}
