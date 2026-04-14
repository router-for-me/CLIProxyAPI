package common

import "testing"

func TestSSEEventData_HasSSEFrameDelimiter(t *testing.T) {
	payload := []byte(`{"type":"response.created"}`)
	chunk := SSEEventData("response.created", payload)
	want := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	if string(chunk) != want {
		t.Fatalf("unexpected chunk: %q", string(chunk))
	}
}
