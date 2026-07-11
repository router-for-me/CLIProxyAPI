package executor

import (
	"context"
	"testing"
	"time"
)

func TestNormalizeToolCallID(t *testing.T) {
	input := "call-cce860e6-ab07-414d-812c-785db35b17ca-4\nfc_d2335004-a95f-93b4-977b-e9eee6316be7_0"
	want := "call-cce860e6-ab07-414d-812c-785db35b17ca-4fc_d2335004-a95f-93b4-977b-e9eee6316be7_0"

	if got := normalizeToolCallID(input); got != want {
		t.Fatalf("normalizeToolCallID() = %q, want %q", got, want)
	}
}

func TestCursorStreamCoalescerBatchesAdjacentDeltas(t *testing.T) {
	type emittedDelta struct {
		text       string
		isThinking bool
	}

	emitted := make(chan emittedDelta, 4)
	coalescer := newCursorStreamCoalescer(
		context.Background(),
		time.Hour,
		func(text string, isThinking bool) {
			emitted <- emittedDelta{text: text, isThinking: isThinking}
		},
	)

	coalescer.push("first", true)
	coalescer.push(" second", true)
	coalescer.push(" third", true)
	coalescer.push("answer", false)
	coalescer.close()
	close(emitted)

	got := make([]emittedDelta, 0, 3)
	for delta := range emitted {
		got = append(got, delta)
	}
	want := []emittedDelta{
		{text: "first", isThinking: true},
		{text: " second third", isThinking: true},
		{text: "answer", isThinking: false},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d emitted deltas, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("delta %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestCursorStreamCoalescerFlushesOnTimer(t *testing.T) {
	emitted := make(chan string, 2)
	coalescer := newCursorStreamCoalescer(
		context.Background(),
		5*time.Millisecond,
		func(text string, _ bool) {
			emitted <- text
		},
	)
	defer coalescer.close()

	coalescer.push("first", false)
	if got := <-emitted; got != "first" {
		t.Fatalf("first emitted delta = %q, want %q", got, "first")
	}

	coalescer.push(" second", false)
	select {
	case got := <-emitted:
		if got != " second" {
			t.Fatalf("timed emitted delta = %q, want %q", got, " second")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed delta was not emitted")
	}
}
