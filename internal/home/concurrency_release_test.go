package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
)

func concurrencyReleaseFrameFromFixture(t *testing.T) ConcurrencyReleaseFrame {
	t.Helper()
	raw, errRead := os.ReadFile(filepath.Join("testdata", "concurrency_release.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}

	var frame ConcurrencyReleaseFrame
	if errUnmarshal := json.Unmarshal(raw, &frame); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return frame
}

func TestConcurrencyReleaseFrameFixture(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("testdata", "concurrency_release.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	frame := concurrencyReleaseFrameFromFixture(t)
	if frame != (ConcurrencyReleaseFrame{CredentialID: "cred-1", Model: "gpt", ReleaseSeq: 1}) {
		t.Fatalf("fixture frame = %#v", frame)
	}
	marshaled, errMarshal := json.Marshal(frame)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if !bytes.Equal(marshaled, bytes.TrimSpace(raw)) {
		t.Fatalf("marshaled frame = %q, want fixture %q", marshaled, bytes.TrimSpace(raw))
	}
}

type recordingReleaseSender struct {
	mu       sync.Mutex
	failures int
	frames   []ConcurrencyReleaseFrame
	acked    []ConcurrencyReleaseFrame
	sent     chan struct{}
}

func (s *recordingReleaseSender) Send(_ context.Context, frame ConcurrencyReleaseFrame) error {
	s.mu.Lock()
	s.frames = append(s.frames, frame)
	failed := s.failures > 0
	if failed {
		s.failures--
	} else {
		s.acked = append(s.acked, frame)
	}
	s.mu.Unlock()
	select {
	case s.sent <- struct{}{}:
	default:
	}
	if failed {
		return errors.New("temporary Home failure")
	}
	return nil
}

func (s *recordingReleaseSender) LastSequence() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.acked) == 0 {
		return 0
	}
	return s.acked[len(s.acked)-1].ReleaseSeq
}

func (s *recordingReleaseSender) WaitForSequence(sequence int64, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if s.LastSequence() == sequence {
			return true
		}
		select {
		case <-timer.C:
			return false
		case <-s.sent:
		}
	}
}

func TestReleaseFlusherRetriesLatestCumulativeSequence(t *testing.T) {
	sender := &recordingReleaseSender{failures: 1, sent: make(chan struct{}, 8)}
	flusher := newReleaseFlusher(10*time.Millisecond, 40*time.Millisecond, sender.Send)
	group := executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "gpt"}
	flusher.MarkDirty(group, 1)
	flusher.MarkDirty(group, 3)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go flusher.Run(ctx)

	if !sender.WaitForSequence(3, 500*time.Millisecond) {
		t.Fatalf("last sequence = %d, want 3", sender.LastSequence())
	}
	if sender.LastSequence() != 3 {
		t.Fatalf("last sequence = %d, want 3", sender.LastSequence())
	}
}

type blockingReleaseSender struct {
	started chan struct{}
	release chan struct{}
	frames  chan ConcurrencyReleaseFrame
	once    sync.Once
}

func (s *blockingReleaseSender) Send(_ context.Context, frame ConcurrencyReleaseFrame) error {
	s.once.Do(func() { close(s.started) })
	select {
	case s.frames <- frame:
	default:
	}
	<-s.release
	return nil
}

func TestReleaseFlusherDoesNotLoseASequenceMarkedDuringSend(t *testing.T) {
	sender := &blockingReleaseSender{
		started: make(chan struct{}),
		release: make(chan struct{}),
		frames:  make(chan ConcurrencyReleaseFrame, 4),
	}
	flusher := newReleaseFlusher(time.Millisecond, 10*time.Millisecond, sender.Send)
	group := executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "gpt"}
	flusher.MarkDirty(group, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go flusher.Run(ctx)

	select {
	case <-sender.started:
	case <-time.After(time.Second):
		t.Fatal("release flusher did not begin sending")
	}
	flusher.MarkDirty(group, 2)
	close(sender.release)

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame := <-sender.frames:
			if frame.ReleaseSeq == 2 {
				return
			}
		case <-deadline.C:
			t.Fatal("release flusher did not send the latest sequence")
		}
	}
}

func TestReleaseFlusherUsesCurrentLimiterConfig(t *testing.T) {
	flusher := newReleaseFlusher(time.Hour, 2*time.Hour, func(context.Context, ConcurrencyReleaseFrame) error { return nil })
	flusher.SetConfigProvider(func() internalconfig.CredentialConcurrencyConfig {
		return internalconfig.CredentialConcurrencyConfig{
			ReleaseFlushInterval: 5 * time.Millisecond,
			ReleaseMaxBackoff:    25 * time.Millisecond,
		}
	})
	if got := flusher.timings(); got.flushInterval != 5*time.Millisecond || got.maxBackoff != 25*time.Millisecond {
		t.Fatalf("timings = %#v", got)
	}
}

func TestReleaseFlusherStopsWithLifetime(t *testing.T) {
	sender := &recordingReleaseSender{sent: make(chan struct{}, 1)}
	flusher := newReleaseFlusher(time.Hour, time.Hour, sender.Send)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		flusher.Run(ctx)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release flusher did not stop with its lifetime")
	}
}

type timedReleaseAttempt struct {
	at     time.Time
	frame  ConcurrencyReleaseFrame
	failed bool
}

type outageReleaseSender struct {
	mu       sync.Mutex
	outage   bool
	attempts []timedReleaseAttempt
	sent     chan struct{}
}

func (s *outageReleaseSender) Send(_ context.Context, frame ConcurrencyReleaseFrame) error {
	s.mu.Lock()
	failed := s.outage
	s.attempts = append(s.attempts, timedReleaseAttempt{at: time.Now(), frame: frame, failed: failed})
	s.mu.Unlock()
	select {
	case s.sent <- struct{}{}:
	default:
	}
	if failed {
		return errors.New("temporary Home outage")
	}
	return nil
}

func (s *outageReleaseSender) SetOutage(outage bool) {
	s.mu.Lock()
	s.outage = outage
	s.mu.Unlock()
}

func (s *outageReleaseSender) WaitForAttempts(count int, timeout time.Duration) []timedReleaseAttempt {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		attempts := append([]timedReleaseAttempt(nil), s.attempts...)
		s.mu.Unlock()
		if len(attempts) >= count {
			return attempts
		}
		select {
		case <-timer.C:
			return attempts
		case <-s.sent:
		}
	}
}

func TestReleaseFlusherCoalescesDirtyWakesDuringFailureBackoff(t *testing.T) {
	const (
		flushInterval = 20 * time.Millisecond
		maxBackoff    = 80 * time.Millisecond
		tolerance     = 10 * time.Millisecond
	)

	sender := &outageReleaseSender{outage: true, sent: make(chan struct{}, 32)}
	flusher := newReleaseFlusher(flushInterval, maxBackoff, sender.Send)
	group := executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "gpt"}
	flusher.MarkDirty(group, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		flusher.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	stopReleases := make(chan struct{})
	producerDone := make(chan struct{})
	latest := int64(1)
	go func() {
		defer close(producerDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopReleases:
				return
			case <-ticker.C:
				latest++
				flusher.MarkDirty(group, latest)
			}
		}
	}()

	attempts := sender.WaitForAttempts(3, time.Second)
	close(stopReleases)
	<-producerDone
	if len(attempts) < 3 {
		t.Fatalf("attempt count = %d, want at least 3", len(attempts))
	}
	for _, attempt := range attempts[:3] {
		if !attempt.failed {
			t.Fatal("release unexpectedly succeeded during outage")
		}
	}
	if got := attempts[1].at.Sub(attempts[0].at); got < 2*flushInterval-tolerance {
		t.Fatalf("first retry delay = %s, want at least %s", got, 2*flushInterval-tolerance)
	}
	if got := attempts[2].at.Sub(attempts[1].at); got < maxBackoff-tolerance {
		t.Fatalf("second retry delay = %s, want at least %s", got, maxBackoff-tolerance)
	}

	latest++
	recoverySequence := latest
	recoveryStart := attempts[2].at
	sender.SetOutage(false)
	flusher.MarkDirty(group, recoverySequence)

	attempts = sender.WaitForAttempts(4, time.Second)
	if len(attempts) < 4 {
		t.Fatalf("attempt count after recovery = %d, want at least 4", len(attempts))
	}
	recovered := attempts[3]
	if recovered.failed || recovered.frame.ReleaseSeq != recoverySequence {
		t.Fatalf("recovery attempt = %#v, want successful sequence %d", recovered, recoverySequence)
	}
	if got := recovered.at.Sub(recoveryStart); got < maxBackoff-tolerance {
		t.Fatalf("recovery retry delay = %s, want at least %s", got, maxBackoff-tolerance)
	}
}

type boundedForceReleaseSender struct {
	attempts chan context.Context
	calls    int
}

func (s *boundedForceReleaseSender) Send(ctx context.Context, _ ConcurrencyReleaseFrame) error {
	s.calls++
	select {
	case s.attempts <- ctx:
	default:
	}
	if s.calls == 1 {
		return errors.New("temporary Home failure")
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestReleaseFlusherFlushForceUsesBoundedContext(t *testing.T) {
	sender := &boundedForceReleaseSender{attempts: make(chan context.Context, 2)}
	flusher := newReleaseFlusher(time.Second, time.Second, sender.Send)
	group := executionregistry.ReleaseGroup{CredentialID: "cred-1", Model: "gpt"}
	flusher.MarkDirty(group, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		flusher.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-sender.attempts:
	case <-time.After(time.Second):
		t.Fatal("release flusher did not make the initial failed attempt")
	}

	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelFlush()
	if errFlush := flusher.Flush(flushCtx); !errors.Is(errFlush, context.DeadlineExceeded) {
		t.Fatalf("Flush() error = %v, want deadline exceeded", errFlush)
	}

	select {
	case forceCtx := <-sender.attempts:
		if _, ok := forceCtx.Deadline(); !ok {
			t.Fatal("forced release attempt did not receive the bounded Flush context")
		}
	case <-time.After(time.Second):
		t.Fatal("Flush() did not bypass the normal retry interval")
	}
}

func TestScopeEndBlocksDrainUntilReleaseSinkFlushesFinalSequence(t *testing.T) {
	sender := &recordingReleaseSender{sent: make(chan struct{}, 2)}
	flusher := newReleaseFlusher(time.Hour, time.Hour, sender.Send)
	releaseCtx, cancelRelease := context.WithCancel(context.Background())
	releaseDone := make(chan struct{})
	go func() {
		defer close(releaseDone)
		flusher.Run(releaseCtx)
	}()
	defer func() {
		cancelRelease()
		<-releaseDone
	}()

	registry := executionregistry.New()
	sinkStarted := make(chan struct{})
	unblockSink := make(chan struct{})
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) {
		close(sinkStarted)
		<-unblockSink
		flusher.MarkDirty(group, sequence)
	})
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{CredentialID: "cred-1", Model: "gpt", Accounted: true})
	if errInstall != nil {
		t.Fatal(errInstall)
	}

	endDone := make(chan struct{})
	go func() {
		defer close(endDone)
		scope.End("complete")
	}()
	select {
	case <-sinkStarted:
	case <-time.After(time.Second):
		t.Fatal("Scope.End() did not call the release sink")
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	drainDone := make(chan error, 1)
	go func() { drainDone <- registry.Drain(drainCtx) }()

	select {
	case errDrain := <-drainDone:
		t.Fatalf("Drain() returned before the release sink completed: %v", errDrain)
	case <-time.After(20 * time.Millisecond):
	}

	mutexAvailable := make(chan struct{})
	go func() {
		registry.SetReleaseSink(nil)
		close(mutexAvailable)
	}()
	select {
	case <-mutexAvailable:
	case <-time.After(time.Second):
		t.Fatal("release sink blocked the registry mutex")
	}
	if _, errBegin := registry.BeginDispatch(); !errors.Is(errBegin, executionregistry.ErrRegistryNotAccepting) {
		t.Fatalf("BeginDispatch() error = %v, want ErrRegistryNotAccepting", errBegin)
	}

	close(unblockSink)
	select {
	case <-endDone:
	case <-time.After(time.Second):
		t.Fatal("Scope.End() did not complete after the release sink unblocked")
	}
	if errDrain := <-drainDone; errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}

	flushCtx, cancelFlush := context.WithTimeout(context.Background(), time.Second)
	defer cancelFlush()
	if errFlush := flusher.Flush(flushCtx); errFlush != nil {
		t.Fatalf("Flush() error = %v", errFlush)
	}
	if got := sender.LastSequence(); got != 1 {
		t.Fatalf("final flushed sequence = %d, want 1", got)
	}
}
