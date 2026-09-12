package redisqueue

import "testing"

func TestUsageIntegrityOverflowFallsBack(t *testing.T) {
	SetEnabled(false)
	SetEnabled(true)
	defer SetEnabled(false)
	_, cancel := SubscribeUsage()
	defer cancel()
	for i := 0; i < usageSubscriberBuffer-1; i++ {
		Enqueue([]byte(`{"request_id":"a"}`))
	}
	Enqueue([]byte(`{"request_id":"overflow"}`))
	if got := PopOldest(10); len(got) != 1 {
		t.Fatalf("lost overflow event: got %d", len(got))
	}
}

func TestUsageIntegrityMixedSubscriberOverflowFallsBack(t *testing.T) {
	withEnabledQueue(t, func() {
		slow, cancelSlow := SubscribeUsage()
		defer cancelSlow()
		fast, cancelFast := SubscribeUsage()
		defer cancelFast()
		requireUsageSubscriberPayload(t, fast, usageSupportRefreshPayload)
		// Keep one subscriber writable while filling the other's buffer.
		for i := 0; i < usageSubscriberBuffer-1; i++ {
			Enqueue([]byte(`{"event_id":"fill"}`))
			requireUsageSubscriberPayload(t, fast, `{"event_id":"fill"}`)
		}
		const overflow = `{"event_id":"overflow"}`
		Enqueue([]byte(overflow))
		requireUsageSubscriberPayload(t, fast, overflow)
		got := PopOldest(10)
		if len(got) != 1 || string(got[0]) != overflow {
			t.Fatalf("disconnected subscriber cannot recover overflow: %q", got)
		}
		for i := 0; i < usageSubscriberBuffer; i++ {
			select {
			case _, ok := <-slow:
				if !ok {
					t.Fatal("slow subscriber lost buffered records")
				}
			default:
				t.Fatal("slow subscriber buffer unexpectedly empty")
			}
		}
		select {
		case _, ok := <-slow:
			if ok {
				t.Fatal("slow subscriber remained open")
			}
		default:
			t.Fatal("slow subscriber was not disconnected")
		}
		Enqueue([]byte(`{"event_id":"next"}`))
		requireUsageSubscriberPayload(t, fast, `{"event_id":"next"}`)
		if got := PopOldest(10); len(got) != 0 {
			t.Fatalf("healthy subscriber delivery was unnecessarily queued: %q", got)
		}
	})
}
