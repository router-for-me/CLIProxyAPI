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
