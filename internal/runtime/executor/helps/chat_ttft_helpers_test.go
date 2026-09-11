package helps

import (
	"testing"
	"time"
)

func TestIsChatTokenEventRejectsMultilineFragments(t *testing.T) {
	// Split across a JSON array element boundary so joining with "\n" stays valid JSON,
	// but neither fragment alone is a recognizable chat token event.
	frag1 := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[`)
	frag2 := []byte(`data: {"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
	assembled := []byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[\n{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}")

	if IsChatTokenEvent(frag1) {
		t.Fatalf("fragment 1 must not be treated as a token event")
	}
	if IsChatTokenEvent(frag2) {
		t.Fatalf("fragment 2 must not be treated as a token event")
	}
	if !IsChatTokenEvent(assembled) {
		t.Fatalf("assembled multiline SSE frame must be a token event")
	}
}

func TestObserveChatTokenEventUsesAssembledFrameNotFragments(t *testing.T) {
	reporter := &UsageReporter{}
	reporter.StartResponseTTFT()
	time.Sleep(5 * time.Millisecond)

	frag1 := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[`)
	frag2 := []byte(`data: {"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
	assembled := []byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[\n{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}")

	ObserveChatTokenEvent(reporter, frag1)
	ObserveChatTokenEvent(reporter, frag2)
	if reporter.IsTTFTSet() {
		t.Fatalf("fragment observation must not set effective TTFT")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("fragment observation should record first-packet fallback")
	}
	fallback := reporter.ttftDuration()

	time.Sleep(10 * time.Millisecond)
	ObserveChatTokenEvent(reporter, assembled)
	if !reporter.IsTTFTSet() {
		t.Fatalf("assembled frame must set effective TTFT")
	}
	tokenTTFT := reporter.ttftDuration()
	if tokenTTFT <= fallback {
		t.Fatalf("token TTFT %v should be later than first-packet fallback %v", tokenTTFT, fallback)
	}
}
