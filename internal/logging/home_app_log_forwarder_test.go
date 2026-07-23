package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	log "github.com/sirupsen/logrus"
)

type stubHomeAppLogClient struct {
	mu          sync.Mutex
	heartbeatOK bool
	err         error
	pushed      [][]byte
}

func (c *stubHomeAppLogClient) HeartbeatOK() bool { return c.heartbeatOK }

func (c *stubHomeAppLogClient) RPushAppLog(_ context.Context, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.pushed = append(c.pushed, bytes.Clone(payload))
	return nil
}

func (c *stubHomeAppLogClient) pushedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pushed)
}

func (c *stubHomeAppLogClient) pushedAt(index int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index >= len(c.pushed) {
		return nil
	}
	return bytes.Clone(c.pushed[index])
}

func TestHomeAppLogForwarder_ForwardsFormattedLogWhenBoundOwnerIsHealthy(t *testing.T) {
	stub := &stubHomeAppLogClient{heartbeatOK: true}
	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 4),
		stop:      make(chan struct{}),
	}
	forwarder.enabled.Store(true)
	forwarder.bind(stub)
	forwarder.wg.Add(1)
	go forwarder.run()
	defer forwarder.Stop()

	entry := log.NewEntry(log.StandardLogger())
	entry.Time = time.Date(2026, 5, 29, 8, 0, 0, 0, time.Local)
	entry.Level = log.DebugLevel
	entry.Message = "debug details"
	entry.Data["request_id"] = "req-app-1"

	if errFire := forwarder.Fire(entry); errFire != nil {
		t.Fatalf("Fire error: %v", errFire)
	}

	deadline := time.Now().Add(time.Second)
	for stub.pushedCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stub.pushedCount() != 1 {
		t.Fatalf("pushed records = %d, want 1", stub.pushedCount())
	}

	var got homeAppLogPayload
	if errUnmarshal := json.Unmarshal(stub.pushedAt(0), &got); errUnmarshal != nil {
		t.Fatalf("unmarshal payload: %v", errUnmarshal)
	}
	if got.Level != "debug" {
		t.Fatalf("level = %q, want debug", got.Level)
	}
	if got.RequestID != "req-app-1" {
		t.Fatalf("request_id = %q, want req-app-1", got.RequestID)
	}
	if !strings.Contains(got.Line, "debug details") {
		t.Fatalf("line %q missing log message", got.Line)
	}
	if !strings.Contains(got.Line, "[req-app-1]") {
		t.Fatalf("line %q missing matching request id", got.Line)
	}
	if strings.TrimSpace(got.Timestamp) == "" {
		t.Fatal("timestamp empty, want non-empty")
	}
}

func TestHomeAppLogForwarder_StopUnregistersMuxTarget(t *testing.T) {
	beforeHooks := homeAppLogForwarderHookCount()
	beforeTargets := homeAppLogForwarderTargetCount()
	forwarder := StartHomeAppLogForwarder(1)
	if got := homeAppLogForwarderHookCount(); got != beforeHooks {
		forwarder.Stop()
		t.Fatalf("direct Home log forwarder hooks = %d, want %d", got, beforeHooks)
	}
	if got := homeAppLogForwarderTargetCount(); got != beforeTargets+1 {
		forwarder.Stop()
		t.Fatalf("Home log forwarder targets = %d, want %d", got, beforeTargets+1)
	}
	forwarder.Stop()
	if got := homeAppLogForwarderTargetCount(); got != beforeTargets {
		t.Fatalf("Home log forwarder targets after Stop = %d, want %d", got, beforeTargets)
	}
}

func TestHomeAppLogForwardersUseOneProcessWideMuxHook(t *testing.T) {
	first := StartHomeAppLogForwarder(1)
	second := StartHomeAppLogForwarder(1)
	t.Cleanup(first.Stop)
	t.Cleanup(second.Stop)

	if got := homeAppLogForwarderHookCount(); got != 0 {
		t.Fatalf("direct Home log forwarder hooks = %d, want 0", got)
	}
	if got := homeAppLogMuxHookCount(); got != 1 {
		t.Fatalf("Home log mux hooks = %d, want 1", got)
	}
}

func homeAppLogForwarderHookCount() int {
	count := 0
	for _, hooks := range log.StandardLogger().Hooks {
		for _, hook := range hooks {
			if _, ok := hook.(*HomeAppLogForwarder); ok {
				count++
			}
		}
	}
	return count / len(log.AllLevels)
}

func homeAppLogMuxHookCount() int {
	count := 0
	for _, hooks := range log.StandardLogger().Hooks {
		for _, hook := range hooks {
			if _, ok := hook.(*homeAppLogMux); ok {
				count++
			}
		}
	}
	return count / len(log.AllLevels)
}

func homeAppLogForwarderTargetCount() int {
	homeAppLogMuxHook.mu.Lock()
	defer homeAppLogMuxHook.mu.Unlock()
	return len(homeAppLogMuxHook.targets)
}

func TestHomeAppLogForwarder_RebindsOnlyToCurrentOwner(t *testing.T) {
	first := &stubHomeAppLogClient{heartbeatOK: true}
	second := &stubHomeAppLogClient{heartbeatOK: true}
	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 4),
		stop:      make(chan struct{}),
	}
	forwarder.enabled.Store(true)
	forwarder.wg.Add(1)
	go forwarder.run()
	t.Cleanup(forwarder.Stop)

	forwarder.bind(first)
	if errFire := forwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("Fire() error = %v", errFire)
	}
	waitForHomeAppLogPush(t, first, 1)

	forwarder.bind(second)
	forwarder.deactivate(first)
	if errFire := forwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("Fire() error = %v", errFire)
	}
	waitForHomeAppLogPush(t, second, 1)
	if first.pushedCount() != 1 {
		t.Fatalf("stale owner received %d records, want 1", first.pushedCount())
	}

	forwarder.deactivate(first)
	if errFire := forwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("Fire() error = %v", errFire)
	}
	waitForHomeAppLogPush(t, second, 2)

	forwarder.deactivate(second)
	if errFire := forwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("Fire() error = %v", errFire)
	}
	time.Sleep(20 * time.Millisecond)
	if second.pushedCount() != 2 {
		t.Fatalf("detached owner received %d records, want 2", second.pushedCount())
	}
}

func waitForHomeAppLogPush(t *testing.T, client *stubHomeAppLogClient, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for client.pushedCount() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.pushedCount(); got != want {
		t.Fatalf("pushed records = %d, want %d", got, want)
	}
}

type delayedUnsupportedHomeAppLogClient struct {
	started     chan struct{}
	startedOnce sync.Once
	release     <-chan struct{}
}

func (c *delayedUnsupportedHomeAppLogClient) HeartbeatOK() bool { return true }

func (c *delayedUnsupportedHomeAppLogClient) RPushAppLog(_ context.Context, _ []byte) error {
	c.startedOnce.Do(func() { close(c.started) })
	<-c.release
	return errors.New("ERR unsupported key")
}

func TestHomeAppLogForwarder_DelayedOldOwnerUnsupportedDoesNotDisableNewOwner(t *testing.T) {
	release := make(chan struct{})
	oldOwner := &delayedUnsupportedHomeAppLogClient{started: make(chan struct{}), release: release}
	newOwner := &stubHomeAppLogClient{heartbeatOK: true}
	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 1),
		stop:      make(chan struct{}),
	}
	forwarder.enabled.Store(true)
	forwarder.wg.Add(1)
	go forwarder.run()
	t.Cleanup(forwarder.Stop)

	forwarder.bind(oldOwner)
	forwardDone := make(chan struct{})
	go func() {
		forwarder.forward(homeAppLogPayload{Line: "old owner", client: oldOwner})
		close(forwardDone)
	}()

	select {
	case <-oldOwner.started:
	case <-time.After(time.Second):
		t.Fatal("old owner did not start forwarding")
	}

	forwarder.bind(newOwner)
	close(release)
	select {
	case <-forwardDone:
	case <-time.After(time.Second):
		t.Fatal("old owner forwarding did not finish")
	}
	if !forwarder.enabled.Load() {
		t.Fatal("old owner unsupported response disabled the new owner")
	}

	forwarder.forward(homeAppLogPayload{Line: "new owner", client: newOwner})
	waitForHomeAppLogPush(t, newOwner, 1)
}

func TestHomeAppLogForwarder_UnboundNeverUsesGlobalFallbackClient(t *testing.T) {
	fallback := home.New(internalconfig.HomeConfig{Enabled: true})
	home.SetCurrent(fallback)
	t.Cleanup(home.ClearCurrent)

	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 1),
		stop:      make(chan struct{}),
	}
	forwarder.enabled.Store(true)

	if client := forwarder.client(); client != nil {
		t.Fatalf("unbound client = %v, want nil", client)
	}
	if errFire := forwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("Fire() error = %v", errFire)
	}
	if queued := len(forwarder.queue); queued != 0 {
		t.Fatalf("unbound queued records = %d, want 0", queued)
	}
}

func TestHomeAppLogForwarder_DropsPreACKAndReconnectGapLogs(t *testing.T) {
	oldClient := home.New(internalconfig.HomeConfig{Enabled: true})
	newClient := home.New(internalconfig.HomeConfig{Enabled: true})
	home.SetCurrent(oldClient)
	t.Cleanup(home.ClearCurrent)

	preACKForwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 1),
		stop:      make(chan struct{}),
	}
	preACKForwarder.enabled.Store(true)
	if client := preACKForwarder.client(); client != nil {
		t.Fatalf("pre-ACK client = %v, want nil", client)
	}
	if errFire := preACKForwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("pre-ACK Fire() error = %v", errFire)
	}

	preACKForwarder.bind(oldClient)
	preACKForwarder.deactivate(oldClient)
	home.SetCurrent(newClient)
	if errFire := preACKForwarder.Fire(log.NewEntry(log.StandardLogger())); errFire != nil {
		t.Fatalf("reconnect-gap Fire() error = %v", errFire)
	}

	if got := len(preACKForwarder.queue); got != 0 {
		t.Fatalf("pre-ACK/reconnect-gap queued records = %d, want 0", got)
	}
	if client := preACKForwarder.client(); client != nil {
		t.Fatalf("reconnect-gap client = %v, want nil", client)
	}
}

func TestHomeAppLogForwarder_OmitsPlaceholderRequestID(t *testing.T) {
	entry := log.NewEntry(log.StandardLogger())
	entry.Data["request_id"] = "--------"

	if got := appLogRequestID(entry); got != "" {
		t.Fatalf("request id = %q, want empty for placeholder", got)
	}
}

func TestHomeAppLogForwarder_SkipsWhenBoundOwnerHeartbeatIsDown(t *testing.T) {
	stub := &stubHomeAppLogClient{heartbeatOK: false}
	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 4),
		stop:      make(chan struct{}),
	}
	forwarder.enabled.Store(true)
	forwarder.bind(stub)

	entry := log.NewEntry(log.StandardLogger())
	entry.Time = time.Now()
	entry.Level = log.InfoLevel
	entry.Message = "should stay local"

	if errFire := forwarder.Fire(entry); errFire != nil {
		t.Fatalf("Fire error: %v", errFire)
	}
	if stub.pushedCount() != 0 {
		t.Fatalf("pushed records = %d, want 0", stub.pushedCount())
	}
}

func TestHomeAppLogForwarder_DisablesForwardingWhenBoundOwnerDoesNotSupportAppLog(t *testing.T) {
	stub := &stubHomeAppLogClient{
		heartbeatOK: true,
		err:         errors.New("ERR unsupported key"),
	}
	forwarder := &HomeAppLogForwarder{
		formatter: &LogFormatter{},
		queue:     make(chan homeAppLogPayload, 4),
		stop:      make(chan struct{}),
	}
	forwarder.enabled.Store(true)
	forwarder.bind(stub)

	forwarder.forward(homeAppLogPayload{Line: "legacy home cannot receive app logs"})
	if forwarder.enabled.Load() {
		t.Fatal("forwarder still enabled, want disabled after unsupported app-log response")
	}
}
