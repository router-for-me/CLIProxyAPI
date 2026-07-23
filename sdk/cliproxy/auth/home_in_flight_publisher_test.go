package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestEncodeHomeInFlightFreezePreservesPartitionsAndBarrier(t *testing.T) {
	freeze := executionregistry.Freeze{
		Revision:        9,
		BarrierRevision: 14,
		Executions: []executionregistry.Observation{
			{RequestID: "req-a", CredentialID: "cred", Model: "gpt-5", RequestKind: "http", StartedAt: time.Unix(10, 0).UTC(), Accounted: true},
			{RequestID: "req-b", CredentialID: "cred", Model: "gpt-5", RequestKind: "sse", StartedAt: time.Unix(11, 0).UTC(), Accounted: false},
		},
	}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(12, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 64, MaxRevisionBytes: 16384,
		MaxAggregateGroups: 100000, MaxDetails: 1, MaxStringBytes: 256,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFramePart {
		t.Fatalf("frames = %#v", frames)
	}
	if frames[0].BarrierRevision != 14 || !frames[0].DetailsTruncated {
		t.Fatalf("metadata = %#v", frames[0])
	}
	if got := frames[0].Aggregates; len(got) != 2 || got[0].Count != 1 || got[1].Count != 1 {
		t.Fatalf("aggregates = %#v", got)
	}
}

func TestEncodeHomeInFlightFreezeUsesOverflowWithoutPartialAggregates(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 10, BarrierRevision: 15, Executions: []executionregistry.Observation{
		{CredentialID: "a", Model: "m1", RequestKind: "http", Accounted: false},
		{CredentialID: "b", Model: "m2", RequestKind: "http", Accounted: true},
	}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 256, MaxPartCount: 1, MaxRevisionBytes: 256,
		MaxAggregateGroups: 1, MaxDetails: 0, MaxStringBytes: 256,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFrameOverflow || frames[0].AggregateGroupCount != 2 {
		t.Fatalf("frames = %#v", frames)
	}
	if len(frames[0].Aggregates) != 0 || len(frames[0].Details) != 0 {
		t.Fatalf("overflow leaked partial data: %#v", frames[0])
	}
	if frames[0].PartIndex != nil || frames[0].PartCount != nil {
		t.Fatalf("overflow contains part metadata: %#v", frames[0])
	}
}

func TestEncodeHomeInFlightFreezeUsesDeterministicBoundedMultipartFrames(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 4, Executions: []executionregistry.Observation{
		{RequestID: "req-c", CredentialID: "cred", Model: "model", RequestKind: "http", StartedAt: time.Unix(12, 0).UTC()},
		{RequestID: "req-a", CredentialID: "cred", Model: "model", RequestKind: "http", StartedAt: time.Unix(10, 0).UTC()},
		{RequestID: "req-b", CredentialID: "cred", Model: "model", RequestKind: "http", StartedAt: time.Unix(11, 0).UTC()},
	}}
	cfg := HomeInFlightPublisherConfig{
		MaxPartBytes: 300, MaxPartCount: 8, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 8, MaxDetails: 3, MaxStringBytes: 256,
	}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), cfg)
	if len(frames) < 2 {
		t.Fatalf("frames = %#v, want multipart", frames)
	}
	for index, frame := range frames {
		raw, errMarshal := json.Marshal(frame)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if len(raw) > cfg.MaxPartBytes || frame.PartIndex == nil || frame.PartCount == nil || *frame.PartIndex != index || *frame.PartCount != len(frames) {
			t.Fatalf("frame %d = %s", index, raw)
		}
	}
	requestIDs := make([]string, 0, 3)
	for _, frame := range frames {
		for _, detail := range frame.Details {
			requestIDs = append(requestIDs, detail.RequestID)
		}
	}
	if strings.Join(requestIDs, ",") != "req-a,req-b,req-c" {
		t.Fatalf("details are not sorted: %#v", frames)
	}
}

func TestEncodeHomeInFlightFreezeOverflowsWhenFinalAggregatePartExceedsPartCount(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 13, Executions: []executionregistry.Observation{
		{CredentialID: strings.Repeat("a", 300), Model: strings.Repeat("a", 300), Accounted: true},
		{CredentialID: strings.Repeat("b", 300), Model: strings.Repeat("b", 300), Accounted: true},
		{CredentialID: strings.Repeat("c", 300), Model: strings.Repeat("c", 300), Accounted: true},
	}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 3, MaxDetails: 0, MaxStringBytes: 512,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFrameOverflow || frames[0].AggregateGroupCount != 3 {
		t.Fatalf("frames = %#v", frames)
	}
	if len(frames[0].Aggregates) != 0 || len(frames[0].Details) != 0 {
		t.Fatalf("overflow leaked aggregate prefix: %#v", frames[0])
	}
}

func TestEncodeHomeInFlightFreezeTruncatesDetailsBeforeTotalOverflow(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 12}
	for index := 0; index < 5; index++ {
		freeze.Executions = append(freeze.Executions, executionregistry.Observation{
			RequestID: strings.Repeat(string(rune('a'+index)), 60), CredentialID: "cred", Model: "model", RequestKind: "http",
			StartedAt: time.Unix(int64(index), 0).UTC(),
		})
	}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 512, MaxPartCount: 8, MaxRevisionBytes: 1000,
		MaxAggregateGroups: 8, MaxDetails: 5, MaxStringBytes: 128,
	})
	if len(frames) == 1 && frames[0].Kind == home.InFlightFrameOverflow {
		t.Fatalf("details overflowed complete aggregates: %#v", frames)
	}
	if !frames[0].DetailsTruncated || len(frames[0].Aggregates) != 1 {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestEncodeHomeInFlightFreezeBoundsStringsAndExcludesSensitiveFields(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 3, Executions: []executionregistry.Observation{{
		RequestID: strings.Repeat("request", 20), CredentialID: strings.Repeat("credential", 20),
		Model: strings.Repeat("model", 20), RequestKind: strings.Repeat("kind", 20),
	}}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 8, MaxDetails: 1, MaxStringBytes: 8,
	})
	raw, errMarshal := json.Marshal(frames)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if strings.Contains(string(raw), "credentialcredential") || strings.Contains(string(raw), "token") {
		t.Fatalf("snapshot leaked unbounded or sensitive data: %s", raw)
	}
}

func TestHomeInFlightPublisherConfigFromConfigValidatesAndUpdates(t *testing.T) {
	cfg := internalconfig.DefaultCredentialInFlightConfig()
	cfg.SnapshotInterval = "25ms"
	publisherCfg, errConfig := HomeInFlightPublisherConfigFromConfig(cfg)
	if errConfig != nil || publisherCfg.SnapshotInterval != 25*time.Millisecond {
		t.Fatalf("config = %#v, error = %v", publisherCfg, errConfig)
	}

	manager := NewManager(nil, nil, nil)
	manager.ApplyHomeInFlightPublisherConfig(publisherCfg)
	if got := manager.HomeInFlightPublisherConfig(); got.SnapshotInterval != 25*time.Millisecond {
		t.Fatalf("manager config = %#v", got)
	}
}

type homeInFlightTransportStub struct {
	heartbeat bool
	payloads  chan []byte
}

func (t *homeInFlightTransportStub) HeartbeatOK() bool { return t.heartbeat }
func (t *homeInFlightTransportStub) LPushInFlightSnapshot(_ context.Context, payload []byte) error {
	t.payloads <- append([]byte(nil), payload...)
	return nil
}

func TestHomeInFlightPublisherPinsLifetimeRegistry(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.ApplyHomeInFlightPublisherConfig(HomeInFlightPublisherConfig{SnapshotInterval: time.Hour, MaxPartBytes: 1024, MaxPartCount: 1, MaxRevisionBytes: 1024, MaxAggregateGroups: 1, MaxDetails: 0, MaxStringBytes: 8})
	registry := executionregistry.New()
	registry.ObserveBarrier(14)
	transport := &homeInFlightTransportStub{heartbeat: true, payloads: make(chan []byte, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.StartHomeInFlightPublisher(ctx, transport, registry)

	select {
	case raw := <-transport.payloads:
		var frame home.InFlightSnapshotFrame
		if errUnmarshal := json.Unmarshal(raw, &frame); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if frame.BarrierRevision != 14 {
			t.Fatalf("frame = %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("publisher did not send lifetime snapshot")
	}
}

type homeInFlightModelDispatcher struct{}

func (homeInFlightModelDispatcher) HeartbeatOK() bool { return true }
func (homeInFlightModelDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{
		Model: "final-upstream-model",
		Auth:  Auth{ID: "home-auth", Provider: "home-execution", Status: StatusActive},
	})
}
func (homeInFlightModelDispatcher) AbortAmbiguousDispatch() {}

func TestHomeInFlightObservationUsesFinalDispatchModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(homeInFlightModelDispatcher{}, registry, 1)
	manager.RegisterExecutor(&homeExecutionExecutor{})

	selection, errSelection := manager.pickHomeDispatchSelection(context.Background(), "requested-model", cliproxyexecutor.Options{})
	if errSelection != nil {
		t.Fatalf("pickHomeDispatchSelection() error = %v", errSelection)
	}
	defer selection.End("test_complete")

	freeze := registry.FreezeInFlight(time.Now())
	if len(freeze.Executions) != 1 || freeze.Executions[0].Model != "final-upstream-model" {
		t.Fatalf("observation = %#v", freeze.Executions)
	}
}

func TestEncodeHomeInFlightFreezeOverflowsForRawAggregateKey(t *testing.T) {
	freeze := executionregistry.Freeze{Executions: []executionregistry.Observation{{
		CredentialID: "credential-id-exceeds-limit", Model: "model", RequestKind: "http",
	}}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 2, MaxDetails: 1, MaxStringBytes: 8,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFrameOverflow || frames[0].AggregateGroupCount != 1 {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestEncodeHomeInFlightFreezeKeepsRawAggregateGroupsDistinct(t *testing.T) {
	freeze := executionregistry.Freeze{Executions: []executionregistry.Observation{
		{CredentialID: "credential-a", Model: "model", RequestKind: "http"},
		{CredentialID: "credential-b", Model: "model", RequestKind: "http"},
	}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 1, MaxDetails: 0, MaxStringBytes: 8,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFrameOverflow || frames[0].AggregateGroupCount != 2 {
		t.Fatalf("frames = %#v", frames)
	}
}

func TestEncodeHomeInFlightFreezeDropsInvalidDetailsWithoutDiscardingAggregates(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 21, Executions: []executionregistry.Observation{
		{RequestID: "", CredentialID: "cred-a", Model: "model-a", RequestKind: "http", StartedAt: time.Unix(1, 0).UTC()},
		{RequestID: "request-b", CredentialID: "cred-a", Model: "model-a", RequestKind: "http", StartedAt: time.Unix(2, 0).UTC()},
	}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 2, MaxDetails: 2, MaxStringBytes: 64,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFramePart {
		t.Fatalf("frames = %#v, want one part", frames)
	}
	if !frames[0].DetailsTruncated || len(frames[0].Aggregates) != 1 || frames[0].Aggregates[0].Count != 2 {
		t.Fatalf("frame = %#v, want preserved aggregate and truncated details", frames[0])
	}
	if len(frames[0].Details) != 1 || frames[0].Details[0].RequestID != "request-b" {
		t.Fatalf("details = %#v, want only valid request-b", frames[0].Details)
	}
}

func TestEncodeHomeInFlightFreezeCanonicalizesUnaccountedModelsWithFallback(t *testing.T) {
	freeze := executionregistry.Freeze{Revision: 22, Executions: []executionregistry.Observation{
		{RequestID: "request-a", CredentialID: "cred-a", Model: "GPT-5(HIGH)", RequestKind: "http", StartedAt: time.Unix(1, 0).UTC()},
		{RequestID: "request-b", CredentialID: "cred-b", Model: "   ", RequestKind: "http", StartedAt: time.Unix(2, 0).UTC()},
	}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 3, MaxDetails: 2, MaxStringBytes: 64,
	})
	if len(frames) != 1 || frames[0].Kind != home.InFlightFramePart {
		t.Fatalf("frames = %#v, want one part", frames)
	}
	models := make([]string, 0, len(frames[0].Aggregates))
	for _, aggregate := range frames[0].Aggregates {
		models = append(models, aggregate.Model)
	}
	if strings.Join(models, ",") != "gpt-5,unknown" {
		t.Fatalf("aggregate models = %v, want canonical valid models", models)
	}
	if frames[0].Details[0].Model != "gpt-5" || frames[0].Details[1].Model != "unknown" {
		t.Fatalf("detail models = %#v, want canonical valid models", frames[0].Details)
	}
}

func TestEncodeHomeInFlightFreezeSetsGlobalDetailTruncationMetadata(t *testing.T) {
	freeze := executionregistry.Freeze{Executions: []executionregistry.Observation{
		{RequestID: strings.Repeat("r", 32), CredentialID: "cred-a", Model: "model-a", RequestKind: "http", StartedAt: time.Unix(1, 0)},
		{RequestID: "request-b", CredentialID: "cred-b", Model: "model-b", RequestKind: "http", StartedAt: time.Unix(2, 0)},
		{RequestID: "request-c", CredentialID: "cred-c", Model: "model-c", RequestKind: "http", StartedAt: time.Unix(3, 0)},
	}}
	frames := encodeHomeInFlightFreeze(freeze, time.Unix(20, 0).UTC(), HomeInFlightPublisherConfig{
		MaxPartBytes: 300, MaxPartCount: 8, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 4, MaxDetails: 2, MaxStringBytes: 8,
	})
	if len(frames) < 2 {
		t.Fatalf("frames = %#v, want multipart", frames)
	}
	for index, frame := range frames {
		if !frame.DetailsTruncated {
			t.Fatalf("frame %d missing global truncation metadata: %#v", index, frame)
		}
	}
}

type homeInFlightPublisherPayload struct {
	observedAt time.Time
	raw        []byte
}

type homeInFlightLifecycleTransport struct {
	heartbeat atomic.Bool
	payloads  chan homeInFlightPublisherPayload
}

func newHomeInFlightLifecycleTransport(heartbeat bool) *homeInFlightLifecycleTransport {
	transport := &homeInFlightLifecycleTransport{payloads: make(chan homeInFlightPublisherPayload, 32)}
	transport.heartbeat.Store(heartbeat)
	return transport
}

func (t *homeInFlightLifecycleTransport) HeartbeatOK() bool { return t.heartbeat.Load() }
func (t *homeInFlightLifecycleTransport) LPushInFlightSnapshot(_ context.Context, raw []byte) error {
	t.payloads <- homeInFlightPublisherPayload{observedAt: time.Now(), raw: append([]byte(nil), raw...)}
	return nil
}

func homeInFlightPublisherTestConfig(interval time.Duration) HomeInFlightPublisherConfig {
	return HomeInFlightPublisherConfig{
		SnapshotInterval: interval, MaxPartBytes: 1024, MaxPartCount: 2, MaxRevisionBytes: 2048,
		MaxAggregateGroups: 2, MaxDetails: 1, MaxStringBytes: 32,
	}
}

func waitForHomeInFlightPublisherPayload(t *testing.T, payloads <-chan homeInFlightPublisherPayload) homeInFlightPublisherPayload {
	t.Helper()
	select {
	case payload := <-payloads:
		return payload
	case <-time.After(time.Second):
		t.Fatal("publisher did not send a payload")
		return homeInFlightPublisherPayload{}
	}
}

func TestHomeInFlightPublisherSkipsFreezeAndPublishWithoutHeartbeat(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.ApplyHomeInFlightPublisherConfig(homeInFlightPublisherTestConfig(10 * time.Millisecond))
	registry := executionregistry.New()
	transport := newHomeInFlightLifecycleTransport(false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.StartHomeInFlightPublisher(ctx, transport, registry)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not exit after cancellation")
	}
	select {
	case published := <-transport.payloads:
		t.Fatalf("publisher sent payload without heartbeat at %v", published.observedAt)
	default:
	}
	if freeze := registry.FreezeInFlight(time.Now()); freeze.Revision != 1 {
		t.Fatalf("publisher froze registry without heartbeat: %#v", freeze)
	}
}

func TestHomeInFlightPublisherCancellationExits(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.ApplyHomeInFlightPublisherConfig(homeInFlightPublisherTestConfig(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.StartHomeInFlightPublisher(ctx, newHomeInFlightLifecycleTransport(false), executionregistry.New())
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publisher did not exit after cancellation")
	}
}

func TestHomeInFlightPublisherReplacementStopsOldLifetimeAndPinsDependencies(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.ApplyHomeInFlightPublisherConfig(homeInFlightPublisherTestConfig(10 * time.Millisecond))
	oldRegistry := executionregistry.New()
	oldRegistry.ObserveBarrier(11)
	oldTransport := newHomeInFlightLifecycleTransport(true)
	oldCtx, cancelOld := context.WithCancel(context.Background())
	oldDone := make(chan struct{})
	go func() {
		manager.StartHomeInFlightPublisher(oldCtx, oldTransport, oldRegistry)
		close(oldDone)
	}()
	oldPayload := waitForHomeInFlightPublisherPayload(t, oldTransport.payloads)
	var oldFrame home.InFlightSnapshotFrame
	if errUnmarshal := json.Unmarshal(oldPayload.raw, &oldFrame); errUnmarshal != nil || oldFrame.BarrierRevision != 11 {
		t.Fatalf("old publisher frame = %#v, error = %v", oldFrame, errUnmarshal)
	}
	cancelOld()
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old publisher did not stop")
	}

	newRegistry := executionregistry.New()
	newRegistry.ObserveBarrier(22)
	newTransport := newHomeInFlightLifecycleTransport(true)
	newCtx, cancelNew := context.WithCancel(context.Background())
	defer cancelNew()
	go manager.StartHomeInFlightPublisher(newCtx, newTransport, newRegistry)
	newPayload := waitForHomeInFlightPublisherPayload(t, newTransport.payloads)
	var newFrame home.InFlightSnapshotFrame
	if errUnmarshal := json.Unmarshal(newPayload.raw, &newFrame); errUnmarshal != nil || newFrame.BarrierRevision != 22 {
		t.Fatalf("new publisher frame = %#v, error = %v", newFrame, errUnmarshal)
	}
	time.Sleep(30 * time.Millisecond)
	select {
	case published := <-oldTransport.payloads:
		t.Fatalf("replaced publisher sent payload at %v", published.observedAt)
	default:
	}

	freeze := newRegistry.FreezeInFlight(time.Now())
	if freeze.BarrierRevision != 22 {
		t.Fatalf("new publisher did not use replacement registry: %#v", freeze)
	}
}

func TestHomeInFlightPublisherAppliesConfigUpdateAtNextTimerCycle(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.ApplyHomeInFlightPublisherConfig(homeInFlightPublisherTestConfig(60 * time.Millisecond))
	transport := newHomeInFlightLifecycleTransport(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.StartHomeInFlightPublisher(ctx, transport, executionregistry.New())
	waitForHomeInFlightPublisherPayload(t, transport.payloads)

	manager.ApplyHomeInFlightPublisherConfig(homeInFlightPublisherTestConfig(10 * time.Millisecond))
	select {
	case published := <-transport.payloads:
		t.Fatalf("publisher applied hot interval before the next timer cycle at %v", published)
	case <-time.After(30 * time.Millisecond):
	}
	second := waitForHomeInFlightPublisherPayload(t, transport.payloads)
	third := waitForHomeInFlightPublisherPayload(t, transport.payloads)
	if elapsed := third.observedAt.Sub(second.observedAt); elapsed > 35*time.Millisecond {
		t.Fatalf("publisher interval after update = %v, want <= 35ms", elapsed)
	}
}
