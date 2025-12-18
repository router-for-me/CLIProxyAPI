package metrics

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func TestCollectorRecordRequest(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	c.RecordRequest("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150, "")
	c.RecordRequest("openai", "gpt-4", "work", routing.RequestTypeCompletion, 200, "rate_limit")

	c.bufferMu.Lock()
	count := len(c.buffer)
	c.bufferMu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 records in buffer, got %d", count)
	}
}

func TestCollectorRecordSuccess(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)

	c.bufferMu.Lock()
	if len(c.buffer) != 1 {
		t.Fatalf("expected 1 record")
	}
	rec := c.buffer[0]
	c.bufferMu.Unlock()

	if !rec.Success {
		t.Error("RecordSuccess should set Success=true")
	}
	if rec.ErrorType != "" {
		t.Error("RecordSuccess should have empty ErrorType")
	}
}

func TestCollectorRecordError(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	c.RecordError("openai", "gpt-4", "default", routing.RequestTypeChat, 100, "timeout")

	c.bufferMu.Lock()
	if len(c.buffer) != 1 {
		t.Fatalf("expected 1 record")
	}
	rec := c.buffer[0]
	c.bufferMu.Unlock()

	if rec.Success {
		t.Error("RecordError should set Success=false")
	}
	if rec.ErrorType != "timeout" {
		t.Errorf("expected ErrorType=timeout, got %s", rec.ErrorType)
	}
}

func TestCollectorFlush(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)
	c.Flush()

	c.bufferMu.Lock()
	count := len(c.buffer)
	c.bufferMu.Unlock()

	if count != 0 {
		t.Errorf("buffer should be empty after flush, got %d", count)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("expected at least one metrics file after flush")
	}
}

func TestCollectorDisabled(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(false)

	c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)

	c.bufferMu.Lock()
	count := len(c.buffer)
	c.bufferMu.Unlock()

	if count != 0 {
		t.Errorf("disabled collector should not record, got %d records", count)
	}
}

func TestCollectorEnableDisable(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	if !c.IsEnabled() {
		t.Error("collector should be enabled initially")
	}

	c.Disable()
	if c.IsEnabled() {
		t.Error("collector should be disabled after Disable()")
	}

	c.Enable()
	if !c.IsEnabled() {
		t.Error("collector should be enabled after Enable()")
	}
}

func TestCollectorConcurrency(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 2000),
		flushSize:   1000,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, int64(n*10))
		}(i)
	}
	wg.Wait()

	c.Flush()

	metrics, err := store.LoadMetrics(time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("failed to load metrics: %v", err)
	}
	if metrics.Summary.TotalRequests != 100 {
		t.Errorf("expected 100 requests, got %d", metrics.Summary.TotalRequests)
	}
}

func TestCollectorAutoFlush(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 20),
		flushSize:   5,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	for i := 0; i < 10; i++ {
		c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, int64(i*10))
	}

	time.Sleep(50 * time.Millisecond)

	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("expected auto-flush to create metrics file")
	}
}

func BenchmarkCollectorRecord(b *testing.B) {
	dir := b.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 10000),
		flushSize:   5000,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}
	c.enabled.Store(true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.RecordSuccess("claude", "claude-3-opus", "default", routing.RequestTypeChat, 150)
	}
}

func TestDefaultCollectorConfig(t *testing.T) {
	cfg := DefaultCollectorConfig("/tmp/metrics")
	if cfg.MetricsDir != "/tmp/metrics" {
		t.Errorf("expected MetricsDir=/tmp/metrics, got %s", cfg.MetricsDir)
	}
	if cfg.FlushSize != 100 {
		t.Errorf("expected FlushSize=100, got %d", cfg.FlushSize)
	}
	if cfg.FlushInterval != 5*time.Second {
		t.Errorf("expected FlushInterval=5s, got %v", cfg.FlushInterval)
	}
}

func TestCollectorGetStore(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	c := &Collector{
		store:       store,
		buffer:      make([]RequestRecord, 0, 200),
		flushSize:   100,
		flushTicker: time.NewTicker(1 * time.Hour),
		stopCh:      make(chan struct{}),
	}

	if c.GetStore() != store {
		t.Error("GetStore should return the underlying store")
	}
}

func TestInitCollector(t *testing.T) {
	globalCollector = nil
	globalCollectorOnce = sync.Once{}

	dir := filepath.Join(t.TempDir(), "metrics")
	cfg := CollectorConfig{
		MetricsDir:    dir,
		FlushSize:     50,
		FlushInterval: 1 * time.Second,
	}

	c, err := InitCollector(cfg)
	if err != nil {
		t.Fatalf("InitCollector failed: %v", err)
	}
	if c == nil {
		t.Fatal("InitCollector returned nil")
	}

	defer c.Stop()

	if GetCollector() != c {
		t.Error("GetCollector should return the initialized collector")
	}
}
