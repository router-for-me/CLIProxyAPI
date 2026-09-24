package main

import "testing"

// TestObserveCompleteResponseBindsConversation verifies one non-streamed reply
// binds to the conversation its originating request belongs to, so a following
// continuation turn recovers that conversation instead of naming a second one.
func TestObserveCompleteResponseBindsConversation(t *testing.T) {
	runtime := newTestRuntime(t)
	cfg := runtime.loadedConfig()
	opening := responsesRoute(t, "complete-conv", "", 1)
	_, identity := runtime.conversationState(identityInput{
		SourceFormat: opening.SourceFormat,
		Body:         opening.Body,
		Metadata:     opening.Metadata,
	}, cfg)
	if identity == "" {
		t.Fatal("the opening request named no conversation")
	}

	observeCompleteReply(runtime, opening, "resp-complete")

	bound := runtime.chains.lookupResponse(continuationResponseKey{
		Generation: cfg.Generation,
		ResponseID: "resp-complete",
	})
	if bound != identity {
		t.Fatalf("bound identity = %q, want %q", bound, identity)
	}
}

// TestResponseIDFromChunkReadsEveryFraming verifies the provider response
// identifier is read from a raw event object, from server-sent event data lines,
// and from a complete response object, while a chunk naming none reads as absent.
func TestResponseIDFromChunkReadsEveryFraming(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
		want  string
	}{
		{name: "raw event", chunk: `{"type":"response.completed","response":{"id":"resp-1"}}`, want: "resp-1"},
		{name: "event stream", chunk: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-2\"}}\n\n", want: "resp-2"},
		{name: "response object", chunk: `{"object":"response","id":"resp-3"}`, want: "resp-3"},
		{name: "text delta", chunk: `{"type":"response.output_text.delta","delta":"hello"}`, want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := responseIDFromPayloads(responsePayloads([]byte(testCase.chunk))); got != testCase.want {
				t.Fatalf("responseIDFromPayloads() = %q, want %q", got, testCase.want)
			}
		})
	}
}
