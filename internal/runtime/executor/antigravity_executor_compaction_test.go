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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityCompactionTriggerStreamGemini(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		contents := gjson.GetBytes(body, "request.contents").Array()
		if len(contents) > 0 {
			lastRole := contents[len(contents)-1].Get("role").String()
			if lastRole == "model" || lastRole == "assistant" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Requests ending with a model turn are not supported."}}`))
				return
			}
		}

		// When summary is requested with user turn at the end:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Summary of previous conversation"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-auth-test",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "test-proj",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	payload := []byte(`{
		"model": "gemini-3.7-flash",
		"stream": true,
		"input": [
			{"type": "message", "role": "user", "content": "synthetic context"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "synthetic previous answer"}]},
			{"type": "compaction_trigger"}
		]
	}`)

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}

	var buf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		buf.Write(chunk.Payload)
	}

	output := buf.String()
	for _, event := range []string{"response.created", "response.in_progress", "response.output_item.added", "response.output_item.done", "response.completed"} {
		if !strings.Contains(output, "event: "+event+"\n") {
			t.Fatalf("missing event %q in stream: %s", event, output)
		}
	}
	if !strings.Contains(output, `"type":"compaction"`) {
		t.Fatalf("expected stream to contain compaction item, got: %s", output)
	}
	if !strings.Contains(output, `"encrypted_content":"cpa-ag-compact-v1:`) {
		t.Fatalf("expected stream to contain sealed capsule, got: %s", output)
	}
}

func TestAntigravityCompactionTriggerStreamClaude(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		contents := gjson.GetBytes(body, "request.contents").Array()
		if len(contents) > 0 {
			lastRole := contents[len(contents)-1].Get("role").String()
			if lastRole == "assistant" || lastRole == "model" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"This model does not support assistant message prefill. The conversation must end with a user message."}}`))
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Claude summary of previous conversation\"}],\"role\":\"model\"}}],\"usageMetadata\":{\"promptTokenCount\":20,\"candidatesTokenCount\":10,\"totalTokenCount\":30}}}\n\n"))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-auth-test",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "test-proj",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	payload := []byte(`{
		"model": "claude-sonnet-4-6",
		"stream": true,
		"input": [
			{"type": "message", "role": "user", "content": "context"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "answer"}]},
			{"type": "compaction_trigger"}
		]
	}`)

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream Claude failed: %v", err)
	}

	var buf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		buf.Write(chunk.Payload)
	}

	output := buf.String()
	if !strings.Contains(output, `"type":"compaction"`) {
		t.Fatalf("expected Claude compaction output in stream, got: %s", output)
	}
	if !strings.Contains(output, "event: response.completed\n") {
		t.Fatalf("expected response.completed in stream, got: %s", output)
	}
}

func TestAntigravityCompactionAltResponsesCompact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		contents := gjson.GetBytes(body, "request.contents").Array()
		if len(contents) > 0 {
			lastRole := contents[len(contents)-1].Get("role").String()
			if lastRole == "model" || lastRole == "assistant" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Requests ending with a model turn are not supported."}}`))
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Summary of previous conversation"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-auth-test",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "test-proj",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	payload := []byte(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "message", "role": "user", "content": "synthetic context"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "synthetic previous answer"}]}
		]
	}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Alt:            "responses/compact",
		Stream:         false,
	})
	if err != nil {
		t.Fatalf("Execute with Alt responses/compact failed: %v", err)
	}

	if !gjson.GetBytes(resp.Payload, "output.0.type").Exists() || gjson.GetBytes(resp.Payload, "output.0.type").String() != "compaction" {
		t.Fatalf("expected output.0.type to be compaction, got: %s", string(resp.Payload))
	}
	if !strings.HasPrefix(gjson.GetBytes(resp.Payload, "output.0.encrypted_content").String(), "cpa-ag-compact-v1:") {
		t.Fatalf("expected sealed encrypted_content, got: %s", string(resp.Payload))
	}
}

func TestAntigravityCompactionReplayNextTurn(t *testing.T) {
	var gotContents []gjson.Result
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "generateContent") {
			gotBody = body
			gotContents = gjson.GetBytes(body, "request.contents").Array()
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Turn completed"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-auth-test",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "test-proj",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	// 1. Trigger compaction to get capsule
	compactPayload := []byte(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "hello"}]},
			{"type": "compaction_trigger"}
		]
	}`)
	compactResp, errCompact := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: compactPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         false,
	})
	if errCompact != nil {
		t.Fatalf("compaction failed: %v", errCompact)
	}

	capsule := gjson.GetBytes(compactResp.Payload, "output.0.encrypted_content").String()
	if capsule == "" {
		t.Fatalf("empty capsule: %s", string(compactResp.Payload))
	}

	// 2. Next turn: send compaction capsule in input
	nextPayload := []byte(fmt.Sprintf(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "compaction", "id": "cmp-1", "encrypted_content": %q},
			{"type": "message", "role": "user", "content": "next task"}
		]
	}`, capsule))

	nextResp, errNext := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: nextPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         false,
	})
	if errNext != nil {
		t.Fatalf("next turn failed: %v", errNext)
	}
	if len(nextResp.Payload) == 0 {
		t.Fatal("empty response on next turn")
	}

	// Verify that the capsule was expanded to developer context in upstream contents or system instruction
	foundContext := false
	for _, content := range gotContents {
		for _, part := range content.Get("parts").Array() {
			if strings.Contains(part.Get("text").String(), "Context summary from previous turns") {
				foundContext = true
			}
		}
	}
	if !foundContext {
		systemText := gjson.GetBytes(gotBody, "request.systemInstruction.parts.0.text").String()
		if strings.Contains(systemText, "Context summary from previous turns") {
			foundContext = true
		}
	}
	if !foundContext {
		t.Fatalf("expected context summary in upstream request, got body: %s", string(gotBody))
	}

	// 3. Foreign or corrupted capsule rejection
	corruptedPayload := []byte(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "compaction", "id": "cmp-1", "encrypted_content": "cpa-ag-compact-v1:bad-ciphertext"},
			{"type": "message", "role": "user", "content": "next task"}
		]
	}`)
	_, errCorrupted := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: corruptedPayload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         false,
	})
	if errCorrupted == nil {
		t.Fatal("expected error on corrupted capsule, got nil")
	}
}

func TestAntigravityCompactionSequentialCompactionPreservesContext(t *testing.T) {
	var summaryRequestBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "generateContent") {
			summaryRequestBodies = append(summaryRequestBodies, body)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Updated summary including earlier context"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":8,"totalTokenCount":23}}}`))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-auth-test",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "test-token",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "test-proj",
		},
		Attributes: map[string]string{
			"base_url": server.URL,
		},
	}

	// 1. Initial compaction
	compactPayload1 := []byte(`{
		"model": "gemini-3.7-flash",
		"input": [
			{"type": "message", "role": "user", "content": "initial conversation"},
			{"type": "compaction_trigger"}
		]
	}`)
	resp1, err1 := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: compactPayload1,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         false,
	})
	if err1 != nil {
		t.Fatalf("first compaction: %v", err1)
	}
	capsule1 := gjson.GetBytes(resp1.Payload, "output.0.encrypted_content").String()

	// 2. Second compaction triggered when input contains: old capsule + new conversation + compaction_trigger
	compactPayload2 := []byte(fmt.Sprintf(`{
		"model": "gemini-3.7-flash",
		"stream": true,
		"input": [
			{"type": "compaction", "id": "cmp-1", "encrypted_content": %q},
			{"type": "message", "role": "user", "content": "subsequent work"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "subsequent answer"}]},
			{"type": "compaction_trigger"}
		]
	}`, capsule1))

	streamResult, err2 := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: compactPayload2,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if err2 != nil {
		t.Fatalf("second compaction stream: %v", err2)
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}

	// Verify that the second summary request to upstream retained the context from capsule1
	if len(summaryRequestBodies) < 2 {
		t.Fatalf("expected at least 2 summary requests, got %d", len(summaryRequestBodies))
	}
	secondReqBody := string(summaryRequestBodies[1])
	if !strings.Contains(secondReqBody, "Context summary from previous turns") {
		t.Fatalf("second summary request to upstream did not preserve first compaction summary: %s", secondReqBody)
	}

	// 3. Verify corrupted capsule accompanied by compaction_trigger is NOT silently ignored
	corruptedWithTrigger := []byte(`{
		"model": "gemini-3.7-flash",
		"stream": true,
		"input": [
			{"type": "compaction", "id": "cmp-1", "encrypted_content": "cpa-ag-compact-v1:corrupted-data"},
			{"type": "compaction_trigger"}
		]
	}`)
	_, errCorruptedTrigger := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: corruptedWithTrigger,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errCorruptedTrigger == nil {
		t.Fatal("expected error on corrupted capsule with compaction trigger, got nil")
	}
}
