package openai

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestShouldRetryResponsesWebsocketAfterMissingToolOutput(t *testing.T) {
	functionCallErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf(`{"status":400,"error":{"type":"invalid_request_error","message":"No tool output found for function call call-1.","param":"input"}}`),
	}
	customToolCallErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf(`{"status":400,"error":{"type":"invalid_request_error","message":"No tool output found for custom tool call call-2.","param":"input"}}`),
	}
	payload := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"continue"}]}`)
	fullTranscriptPayload := []byte(`{"type":"response.create","input":[{"type":"message","role":"assistant","content":"delegating"},{"type":"function_call","call_id":"call-1","name":"spawn_agent","arguments":"{}"},{"type":"message","role":"user","content":"continue"}]}`)
	lastRequest := []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"start"}]}`)

	for _, errMsg := range []*interfaces.ErrorMessage{functionCallErr, customToolCallErr} {
		if !shouldRetryResponsesWebsocketAfterUpstreamStateLoss(errMsg, payload, lastRequest, false) {
			t.Fatalf("missing tool output error was not retryable: %v", errMsg.Error)
		}
		if !shouldRetryResponsesWebsocketAfterUpstreamStateLoss(errMsg, fullTranscriptPayload, lastRequest, false) {
			t.Fatalf("missing tool output error from a full transcript was not retryable: %v", errMsg.Error)
		}
		if shouldRetryResponsesWebsocketAfterUpstreamStateLoss(errMsg, payload, lastRequest, true) {
			t.Fatalf("missing tool output error retried more than once: %v", errMsg.Error)
		}
		if shouldRetryResponsesWebsocketAfterUpstreamStateLoss(errMsg, payload, nil, false) {
			t.Fatalf("missing tool output error retried without transcript state: %v", errMsg.Error)
		}
	}
}

func TestShouldNotRetryResponsesWebsocketAfterUnrelatedBadRequest(t *testing.T) {
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf(`{"status":400,"error":{"type":"invalid_request_error","message":"Invalid input.","param":"input"}}`),
	}
	payload := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[]}`)
	lastRequest := []byte(`{"model":"gpt-5-codex","input":[]}`)

	if shouldRetryResponsesWebsocketAfterUpstreamStateLoss(errMsg, payload, lastRequest, false) {
		t.Fatal("unrelated bad request must not trigger transcript replay")
	}
}

func TestResponsesWebsocketAttemptUsesSSEAfterStateLossReplay(t *testing.T) {
	if !responsesWebsocketAttemptUsesDownstreamWebsocket(false) {
		t.Fatal("initial attempt must retain downstream websocket preference")
	}
	if responsesWebsocketAttemptUsesDownstreamWebsocket(true) {
		t.Fatal("state-loss replay must prefer HTTP/SSE upstream")
	}
}
