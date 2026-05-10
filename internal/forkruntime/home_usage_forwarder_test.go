package forkruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
)

func TestStartHomeUsageForwarderForwardsQueuedPayload(t *testing.T) {
	withEnabledHomeUsageQueue(t)

	payload := []byte(`{"id":1}`)
	redisqueue.Enqueue(payload)

	sink := newRecordingHomeUsageSink(0)
	startHomeUsageForwarderForTest(t, sink)

	got := sink.waitForPayload(t, time.Second)
	if string(got) != string(payload) {
		t.Fatalf("forwarded payload = %q, want %q", string(got), string(payload))
	}
	if got := sink.callCount(); got != 1 {
		t.Fatalf("LPushUsage calls = %d, want 1", got)
	}
}

func TestStartHomeUsageForwarderReenqueuesFailedPayloadAndRetries(t *testing.T) {
	withEnabledHomeUsageQueue(t)

	payload := []byte(`{"id":2}`)
	redisqueue.Enqueue(payload)

	sink := newRecordingHomeUsageSink(1)
	startHomeUsageForwarderForTest(t, sink)

	got := sink.waitForPayload(t, 3*time.Second)
	if string(got) != string(payload) {
		t.Fatalf("retried payload = %q, want %q", string(got), string(payload))
	}
	if got := sink.callCount(); got != 2 {
		t.Fatalf("LPushUsage calls = %d, want 2", got)
	}
}

func TestStartHomeUsageForwarderWaitsForHealthyHeartbeatBeforeDrainingQueue(t *testing.T) {
	withEnabledHomeUsageQueue(t)

	payload := []byte(`{"id":3}`)
	redisqueue.Enqueue(payload)

	sink := newRecordingHomeUsageSink(0)
	sink.setHeartbeatOK(false)
	startHomeUsageForwarderForTest(t, sink)

	sink.waitForHeartbeatCheck(t, time.Second)
	if got := sink.callCount(); got != 0 {
		t.Fatalf("LPushUsage calls while heartbeat is unhealthy = %d, want 0", got)
	}

	sink.setHeartbeatOK(true)
	got := sink.waitForPayload(t, 3*time.Second)
	if string(got) != string(payload) {
		t.Fatalf("forwarded payload = %q, want %q", string(got), string(payload))
	}
	if got := sink.callCount(); got != 1 {
		t.Fatalf("LPushUsage calls = %d, want 1", got)
	}
}

func startHomeUsageForwarderForTest(t *testing.T, sink HomeUsageSink) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHomeUsageForwarder(ctx, sink)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for home usage forwarder to stop")
		}
	})
}

func withEnabledHomeUsageQueue(t *testing.T) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})
}

type recordingHomeUsageSink struct {
	mu              sync.Mutex
	failCount       int
	calls           int
	heartbeatOK     bool
	heartbeatChecks chan struct{}
	received        chan []byte
}

func newRecordingHomeUsageSink(failCount int) *recordingHomeUsageSink {
	return &recordingHomeUsageSink{
		failCount:       failCount,
		heartbeatOK:     true,
		heartbeatChecks: make(chan struct{}, 16),
		received:        make(chan []byte, 8),
	}
}

func (s *recordingHomeUsageSink) HeartbeatOK() bool {
	s.mu.Lock()
	ok := s.heartbeatOK
	s.mu.Unlock()

	select {
	case s.heartbeatChecks <- struct{}{}:
	default:
	}
	return ok
}

func (s *recordingHomeUsageSink) setHeartbeatOK(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatOK = ok
}

func (s *recordingHomeUsageSink) LPushUsage(_ context.Context, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	if s.calls <= s.failCount {
		return errors.New("temporary push failure")
	}

	s.received <- append([]byte(nil), payload...)
	return nil
}

func (s *recordingHomeUsageSink) waitForPayload(t *testing.T, timeout time.Duration) []byte {
	t.Helper()

	select {
	case payload := <-s.received:
		return payload
	case <-time.After(timeout):
		t.Fatal("timeout waiting for forwarded payload")
		return nil
	}
}

func (s *recordingHomeUsageSink) waitForHeartbeatCheck(t *testing.T, timeout time.Duration) {
	t.Helper()

	select {
	case <-s.heartbeatChecks:
	case payload := <-s.received:
		t.Fatalf("payload forwarded before healthy heartbeat: %q", string(payload))
	case <-time.After(timeout):
		t.Fatal("timeout waiting for heartbeat check")
	}
}

func (s *recordingHomeUsageSink) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
