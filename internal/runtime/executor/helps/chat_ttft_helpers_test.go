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
	// Controllable clock avoids wall-clock Sleep / CI timer flakiness (AGENTS.md).
	now := time.Unix(1_700_000_000, 0)
	reporter := &UsageReporter{
		nowFunc: func() time.Time { return now },
	}
	reporter.StartResponseTTFT()

	frag1 := []byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[`)
	frag2 := []byte(`data: {"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`)
	assembled := []byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[\n{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}")

	now = now.Add(5 * time.Millisecond)
	ObserveChatTokenEvent(reporter, frag1)
	ObserveChatTokenEvent(reporter, frag2)
	if reporter.IsTTFTSet() {
		t.Fatalf("fragment observation must not set effective TTFT")
	}
	if !reporter.IsFirstPacketSet() {
		t.Fatalf("fragment observation should record first-packet fallback")
	}
	fallback := reporter.ttftDuration()
	if fallback != 5*time.Millisecond {
		t.Fatalf("first-packet fallback = %v, want 5ms", fallback)
	}

	now = now.Add(10 * time.Millisecond)
	ObserveChatTokenEvent(reporter, assembled)
	if !reporter.IsTTFTSet() {
		t.Fatalf("assembled frame must set effective TTFT")
	}
	tokenTTFT := reporter.ttftDuration()
	if tokenTTFT != 15*time.Millisecond {
		t.Fatalf("token TTFT = %v, want 15ms", tokenTTFT)
	}
	if tokenTTFT <= fallback {
		t.Fatalf("token TTFT %v should be later than first-packet fallback %v", tokenTTFT, fallback)
	}
}

func TestIsChatTokenEventRecognizesAudioOutput(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "delta audio data",
			payload: `{"choices":[{"delta":{"audio":{"data":"UklGRi..."}}}]}`,
			want:    true,
		},
		{
			name:    "delta audio transcript",
			payload: `{"choices":[{"delta":{"audio":{"transcript":"Hello"}}}]}`,
			want:    true,
		},
		{
			name:    "delta audio id",
			payload: `{"choices":[{"delta":{"audio":{"id":"audio_1"}}}]}`,
			want:    true,
		},
		{
			name:    "delta output_audio string",
			payload: `{"choices":[{"delta":{"output_audio":"UklGRi..."}}]}`,
			want:    true,
		},
		{
			name:    "empty audio object is not a token",
			payload: `{"choices":[{"delta":{"audio":{}}}]}`,
			want:    false,
		},
		{
			name:    "role-only delta is not a token",
			payload: `{"choices":[{"delta":{"role":"assistant"}}]}`,
			want:    false,
		},
		{
			name:    "message audio data",
			payload: `{"choices":[{"message":{"audio":{"data":"UklGRi...","transcript":"Hi"}}}]}`,
			want:    true,
		},
		{
			name:    "content part output_audio",
			payload: `{"choices":[{"delta":{"content":[{"type":"output_audio","audio":"AAAA"}]}}]}`,
			want:    true,
		},
		{
			name:    "content part audio",
			payload: `{"choices":[{"delta":{"content":[{"type":"audio","data":"AAAA"}]}}]}`,
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsChatTokenEvent([]byte(tc.payload)); got != tc.want {
				t.Fatalf("IsChatTokenEvent(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
