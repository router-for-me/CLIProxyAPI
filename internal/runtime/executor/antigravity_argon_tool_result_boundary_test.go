package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityArgonTerminalToolResultBoundaryAndPayloadOverride(t *testing.T) {
	rawPayload := []byte(`{"request":{"contents":[` +
		`{"role":"user","parts":[{"text":"check status"}]},` +
		`{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"TaskList","args":{}},"thoughtSignature":"preserved"}]},` +
		`{"role":"model","parts":[{"functionResponse":{"id":"call-1","name":"TaskList","response":{"result":"#1 [in_progress]"}}}]}]` +
		`}}`)

	got := ensureAntigravityGeminiBoundaryUserContent("gemini-4-argon-high", rawPayload)
	if role := gjson.GetBytes(got, "request.contents.2.role").String(); role != "user" {
		t.Fatalf("terminal turn role = %q, want user", role)
	}
	if label := gjson.GetBytes(got, "request.contents.2.parts.1.text").String(); label != "TaskList" {
		t.Fatalf("terminal turn label = %q, want TaskList", label)
	}
	for _, path := range []string{"request.contents.0", "request.contents.1", "request.contents.2.parts.0"} {
		if gjson.GetBytes(got, path).Raw != gjson.GetBytes(rawPayload, path).Raw {
			t.Fatalf("unexpected mutation at %s", path)
		}
	}
	if twice := ensureAntigravityGeminiBoundaryUserContent("gemini-4-argon-high", got); !bytes.Equal(twice, got) {
		t.Fatal("ensureAntigravityGeminiBoundaryUserContent is not idempotent")
	}

	for _, useOverride := range []bool{false, true} {
		name := "default"
		if useOverride {
			name = "payload_override"
		}
		t.Run(name, func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read body: %v", errRead)
					return
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
			}))
			defer server.Close()

			cfg := &config.Config{RequestRetry: 1}
			if useOverride {
				cfg.Payload.Override = []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "gemini-4-argon*"}},
					Params: map[string]any{
						"contents.2.role":         "model",
						"contents.2.parts.1.text": "custom_override",
					},
				}}
			}

			claudePayload := []byte(`{` +
				`"model":"gemini-4-argon-high",` +
				`"max_tokens":1024,` +
				`"messages":[` +
				`{"role":"user","content":"check status"},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"TaskList","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"#1 [in_progress]"}]}` +
				`],` +
				`"tools":[{"name":"TaskList","description":"list tasks","input_schema":{"type":"object","properties":{}}}]` +
				`}`)

			exec := NewAntigravityExecutor(cfg)
			auth := testAntigravityAuth(server.URL)
			auth.Metadata["project_id"] = "project-1"

			streamRes, errExec := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gemini-4-argon-high",
				Payload: claudePayload,
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errExec != nil {
				t.Fatalf("ExecuteStream() error = %v", errExec)
			}
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
			}

			wire := <-captured
			wantRole, wantLabel := "user", "TaskList"
			if useOverride {
				wantRole, wantLabel = "model", "custom_override"
			}
			if gotRole := gjson.GetBytes(wire, "request.contents.2.role").String(); gotRole != wantRole {
				t.Fatalf("wire contents.2.role = %q, want %q; wire=%s", gotRole, wantRole, wire)
			}
			if gotLabel := gjson.GetBytes(wire, "request.contents.2.parts.1.text").String(); gotLabel != wantLabel {
				t.Fatalf("wire contents.2.parts.1.text = %q, want %q; wire=%s", gotLabel, wantLabel, wire)
			}
		})
	}
}

func TestAntigravityArgonTerminalToolResultPreservesReasoningReplayAcrossTurns(t *testing.T) {
	cache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(cache.ClearAntigravityReasoningReplayCache)

	// Valid Gemini field-2 -> field-1 protobuf envelope wrapping a 36-byte UUID.
	rawProto := append([]byte{0x12, 0x26, 0x0a, 0x24}, []byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3")...)
	validSig := base64.StdEncoding.EncodeToString(rawProto)

	var wireBodies [][]byte
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			return
		}
		wireBodies = append(wireBodies, body)
		turn++
		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"native-call-2\",\"name\":\"exec_command\",\"args\":{\"cmd\":\"echo hi\"}},\"thoughtSignature\":\""+validSig+"\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"all done\"}]},\"finishReason\":\"STOP\"}]}}\n\n")
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := testAntigravityAuth(server.URL)
	auth.Metadata["project_id"] = "project-1"

	// Turn 1: Responses request ending in a pure function_call_output on gemini-4-argon-high.
	turn1Payload := []byte(`{` +
		`"model":"gemini-4-argon-high",` +
		`"session_id":"argon-replay-continuity",` +
		`"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"TaskList","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"running"}` +
		`],` +
		`"tools":[` +
		`{"type":"function","name":"TaskList","parameters":{"type":"object","properties":{}}},` +
		`{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}` +
		`]` +
		`}`)

	res1, err1 := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-argon-high",
		Payload: turn1Payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if err1 != nil {
		t.Fatalf("turn 1 ExecuteStream() error = %v", err1)
	}
	for chunk := range res1.Chunks {
		if chunk.Err != nil {
			t.Fatalf("turn 1 chunk error = %v", chunk.Err)
		}
	}

	// Turn 2: Client sends Turn 1's function_call WITHOUT signature, plus its output,
	// so Turn 1's functionResponse at contents[2] is now historical (role="model", no synthetic label).
	// Reasoning replay must still match contextHash at contents[3] and restore validSig!
	turn2Payload := []byte(`{` +
		`"model":"gemini-4-argon-high",` +
		`"session_id":"argon-replay-continuity",` +
		`"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"TaskList","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"running"},` +
		`{"type":"function_call","call_id":"native-call-2","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}"},` +
		`{"type":"function_call_output","call_id":"native-call-2","output":"hi"}` +
		`],` +
		`"tools":[` +
		`{"type":"function","name":"TaskList","parameters":{"type":"object","properties":{}}},` +
		`{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}` +
		`]` +
		`}`)

	res2, err2 := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-argon-high",
		Payload: turn2Payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if err2 != nil {
		t.Fatalf("turn 2 ExecuteStream() error = %v", err2)
	}
	for chunk := range res2.Chunks {
		if chunk.Err != nil {
			t.Fatalf("turn 2 chunk error = %v", chunk.Err)
		}
	}

	if len(wireBodies) != 2 {
		t.Fatalf("captured %d upstream requests, want 2", len(wireBodies))
	}
	// Turn 1 terminal tool-result turn was normalized to user + label.
	if got := gjson.GetBytes(wireBodies[0], "request.contents.2.role").String(); got != "user" {
		t.Fatalf("turn 1 contents.2.role = %q, want user", got)
	}
	if got := gjson.GetBytes(wireBodies[0], "request.contents.2.parts.1.text").String(); got != "TaskList" {
		t.Fatalf("turn 1 contents.2.parts.1.text = %q, want TaskList", got)
	}
	// Turn 2 historical contents[2] stayed untouched, Turn 1's cached thoughtSignature
	// replayed onto contents[3].parts[0] without contextHash drift, and the new
	// terminal contents[4] was normalized to user + exec_command!
	if got := gjson.GetBytes(wireBodies[1], "request.contents.2.role").String(); got != "model" {
		t.Fatalf("turn 2 historical contents.2.role = %q, want model", got)
	}
	if gjson.GetBytes(wireBodies[1], "request.contents.2.parts.1").Exists() {
		t.Fatalf("turn 2 historical contents.2 unexpectedly retained synthetic label: %s", gjson.GetBytes(wireBodies[1], "request.contents.2").Raw)
	}
	if gotSig := gjson.GetBytes(wireBodies[1], "request.contents.3.parts.0.thoughtSignature").String(); gotSig != validSig {
		t.Fatalf("turn 2 replayed thoughtSignature = %q, want %q (contextHash drift broke replay!)", gotSig, validSig)
	}
	if got := gjson.GetBytes(wireBodies[1], "request.contents.4.role").String(); got != "user" {
		t.Fatalf("turn 2 terminal contents.4.role = %q, want user", got)
	}
	if got := gjson.GetBytes(wireBodies[1], "request.contents.4.parts.1.text").String(); got != "exec_command" {
		t.Fatalf("turn 2 terminal contents.4.parts.1.text = %q, want exec_command", got)
	}
}

func TestAntigravityArgonNonStreamPreservesReasoningReplayAcrossTurns(t *testing.T) {
	cache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(cache.ClearAntigravityReasoningReplayCache)

	rawProto := append([]byte{0x12, 0x26, 0x0a, 0x24}, []byte("e24830a7-5cd6-42fe-998b-ee539e72b9c3")...)
	validSig := base64.StdEncoding.EncodeToString(rawProto)

	var wireBodies [][]byte
	turn := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			return
		}
		wireBodies = append(wireBodies, body)
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-call-ns","name":"exec_command","args":{"cmd":"pwd"}},"thoughtSignature":"`+validSig+`"}]},"finishReason":"STOP"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}}`)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := testAntigravityAuth(server.URL)
	auth.Metadata["project_id"] = "project-1"

	turn1Payload := []byte(`{` +
		`"model":"gemini-4-argon-high",` +
		`"session_id":"argon-nonstream-replay-continuity",` +
		`"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"TaskList","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"running"}` +
		`],` +
		`"tools":[` +
		`{"type":"function","name":"TaskList","parameters":{"type":"object","properties":{}}},` +
		`{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}` +
		`]` +
		`}`)
	if _, err1 := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-argon-high",
		Payload: turn1Payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}); err1 != nil {
		t.Fatalf("turn 1 Execute() error = %v", err1)
	}

	turn2Payload := []byte(`{` +
		`"model":"gemini-4-argon-high",` +
		`"session_id":"argon-nonstream-replay-continuity",` +
		`"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},` +
		`{"type":"function_call","call_id":"call_1","name":"TaskList","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"running"},` +
		`{"type":"function_call","call_id":"native-call-ns","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},` +
		`{"type":"function_call_output","call_id":"native-call-ns","output":"/tmp"}` +
		`],` +
		`"tools":[` +
		`{"type":"function","name":"TaskList","parameters":{"type":"object","properties":{}}},` +
		`{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}` +
		`]` +
		`}`)
	if _, err2 := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-argon-high",
		Payload: turn2Payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}); err2 != nil {
		t.Fatalf("turn 2 Execute() error = %v", err2)
	}

	if gotSig := gjson.GetBytes(wireBodies[1], "request.contents.3.parts.0.thoughtSignature").String(); gotSig != validSig {
		t.Fatalf("non-stream turn 2 replayed thoughtSignature = %q, want %q", gotSig, validSig)
	}
}

func TestAntigravityCountTokensLabelsArgonTerminalToolResult(t *testing.T) {
	var wireBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
			return
		}
		wireBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"totalTokens":99}`)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	payload := []byte(`{` +
		`"model":"gemini-4-argon-high",` +
		`"messages":[` +
		`{"role":"user","content":"check"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"TaskList","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}` +
		`],` +
		`"tools":[{"name":"TaskList","input_schema":{"type":"object","properties":{}}}]` +
		`}`)
	_, errCount := exec.CountTokens(context.Background(), testAntigravityAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-4-argon-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got := gjson.GetBytes(wireBody, "request.contents.2.role").String(); got != "user" {
		t.Fatalf("countTokens contents.2.role = %q, want user; body=%s", got, wireBody)
	}
	if got := gjson.GetBytes(wireBody, "request.contents.2.parts.1.text").String(); got != "TaskList" {
		t.Fatalf("countTokens contents.2.parts.1.text = %q, want TaskList; body=%s", got, wireBody)
	}
}
