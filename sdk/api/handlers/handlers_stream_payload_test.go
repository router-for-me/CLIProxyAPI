package handlers

import "testing"

func TestStreamPayloadForHandler_OpenAIResponsePreservesPayload(t *testing.T) {
	payload := []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	out := streamPayloadForHandler("openai-response", payload)
	if len(out) != len(payload) {
		t.Fatalf("len(out)=%d want %d", len(out), len(payload))
	}
	if len(out) > 0 && &out[0] != &payload[0] {
		t.Fatalf("expected openai-response payload to be preserved")
	}
}

func TestStreamPayloadForHandler_DefaultClonesPayload(t *testing.T) {
	payload := []byte("event: message\ndata: hello\n\n")
	out := streamPayloadForHandler("openai", payload)
	if len(out) != len(payload) {
		t.Fatalf("len(out)=%d want %d", len(out), len(payload))
	}
	if len(out) > 0 && &out[0] == &payload[0] {
		t.Fatalf("expected default handler payload to be cloned")
	}
	out[0] = 'E'
	if payload[0] == 'E' {
		t.Fatalf("mutating cloned payload should not mutate source")
	}
}
