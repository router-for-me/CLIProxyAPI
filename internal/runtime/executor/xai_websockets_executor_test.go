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
		Payload: []byte(`{"model":"grok-4.3","stream":true,"previous_response_id":"resp-prev","instructions":"system prompt","input":[{"type":"message","role":"user","content":"hello"}]}`),
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
