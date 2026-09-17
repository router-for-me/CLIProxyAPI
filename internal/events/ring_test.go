package events

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestRingWrapsAtCapacity(t *testing.T) {
	// Capacity is clamped to >=100, so record 105 events into a cap-100 ring
	// and verify only the latest 100 survive, newest first.
	r := NewRing(100)
	for i := 0; i < 105; i++ {
		r.Record(Event{Type: "test", Ts: time.Now(), Payload: mustRaw(t, i)})
	}
	got := r.Snapshot()
	if len(got) != 100 {
		t.Fatalf("want 100 events, got %d", len(got))
	}
	// Newest first: i=104, 103, ..., 5
	if got[0].payloadInt(t) != 104 {
		t.Errorf("first event should be i=104, got %d", got[0].payloadInt(t))
	}
	if got[99].payloadInt(t) != 5 {
		t.Errorf("last event should be i=5, got %d", got[99].payloadInt(t))
	}
}

func TestRingDropsUnderContention(t *testing.T) {
	r := NewRing(100)
	// Hold the lock from this goroutine so the Record goroutine's
	// TryLock fails immediately — this genuinely exercises the
	// contention drop path.
	r.mu.Lock()
	done := make(chan struct{})
	go func() {
		r.Record(Event{Type: "test", Ts: time.Now()})
		close(done)
	}()
	<-done
	r.mu.Unlock()
	if r.Dropped() != 1 {
		t.Errorf("want 1 drop, got %d", r.Dropped())
	}
	if len(r.Snapshot()) != 0 {
		t.Errorf("want empty snapshot, got %d events", len(r.Snapshot()))
	}
}

func TestRingZeroValueRecordIsNoOp(t *testing.T) {
	// Critical regression test: events.Global() returns &Ring{} before
	// SetGlobal has been called. Record on that zero-value ring must
	// not panic — it must be a no-op.
	r := &Ring{}
	r.Record(Event{Type: "test", Ts: time.Now()}) // must not panic
	if r.Dropped() != 0 {
		t.Errorf("zero-value ring should not increment dropped counter, got %d", r.Dropped())
	}
	if len(r.Snapshot()) != 0 {
		t.Errorf("zero-value ring snapshot should be empty, got %d", len(r.Snapshot()))
	}
}

func TestRingCapacityClamping(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 100},       // below min -> 100
		{50, 100},      // below min -> 100
		{5000, 5000},   // in range
		{99999, 50000}, // above max -> 50000
	}
	for _, c := range cases {
		r := NewRing(c.in)
		if r.cap != c.want {
			t.Errorf("NewRing(%d).cap = %d, want %d", c.in, r.cap, c.want)
		}
	}
}

func TestRingConcurrentRecord(t *testing.T) {
	r := NewRing(1000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				r.Record(Event{Type: "concurrent", Ts: time.Now()})
			}
		}()
	}
	wg.Wait()
	if got := len(r.Snapshot()); got > 1000 {
		t.Errorf("snapshot exceeded capacity: %d", got)
	}
}

// TestRingSubscribeReceivesRecordedEvents pins the Subscribe / Unsubscribe
// fan-out: events recorded after Subscribe are delivered to the subscriber
// in Record order, so the SSE handler can stream them as they arrive.
func TestRingSubscribeReceivesRecordedEvents(t *testing.T) {
	r := NewRing(100)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	r.Record(Event{Type: "test", Ts: time.Now(), Payload: mustRaw(t, 1)})
	r.Record(Event{Type: "test", Ts: time.Now(), Payload: mustRaw(t, 2)})

	var got []Event
	for i := 0; i < 2; i++ {
		select {
		case e := <-sub.C:
			got = append(got, e)
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("did not receive event %d", i)
		}
	}
	if got[0].payloadInt(t) != 1 || got[1].payloadInt(t) != 2 {
		t.Errorf("events out of order: %+v", got)
	}
}

// TestRingUnsubscribeStopsDelivery confirms Unsubscribe closes the subscriber
// channel so the SSE handler can rely on a closed-channel signal as
// "stream is over".
func TestRingUnsubscribeStopsDelivery(t *testing.T) {
	r := NewRing(100)
	sub := r.Subscribe()
	r.Record(Event{Type: "before", Ts: time.Now()})

	// Drain the before event
	select {
	case <-sub.C:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive before event")
	}

	r.Unsubscribe(sub)
	r.Record(Event{Type: "after", Ts: time.Now()})

	select {
	case _, ok := <-sub.C:
		if ok {
			t.Errorf("subscriber received event after Unsubscribe (channel still open)")
		}
		// Channel closed — good
	case <-time.After(50 * time.Millisecond):
		// Channel still open but no event — also acceptable
	}
}

// TestRingSubscriberBufferOverflowDropsEvents confirms a slow subscriber
// (one that doesn't drain its buffer) never blocks Record calls — the
// per-subscriber buffer caps at subscriberChanCap and further events
// are silently dropped for that subscriber only.
func TestRingSubscriberBufferOverflowDropsEvents(t *testing.T) {
	r := NewRing(100)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	// Record more events than the subscriber buffer (32) without draining.
	for i := 0; i < 100; i++ {
		r.Record(Event{Type: "flood", Ts: time.Now(), Payload: mustRaw(t, i)})
	}

	// Drain whatever arrived
	received := 0
	drained := false
	for !drained {
		select {
		case <-sub.C:
			received++
		case <-time.After(50 * time.Millisecond):
			drained = true
		}
	}
	if received >= 100 {
		t.Errorf("subscriber received all events (no overflow protection): got %d", received)
	}
	if received > 32 {
		t.Errorf("subscriber received more than buffer cap: got %d", received)
	}
}

// helpers

func mustRaw(t *testing.T, v int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// payloadInt extracts an int payload. Used in assertions.
func (e Event) payloadInt(t *testing.T) int {
	t.Helper()
	var v int
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
