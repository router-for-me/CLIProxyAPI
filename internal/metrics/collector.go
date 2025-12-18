package metrics

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

// Collector handles thread-safe metrics collection with batch writes.
type Collector struct {
	store       *Store
	buffer      []RequestRecord
	bufferMu    sync.Mutex
	flushSize   int
	flushTicker *time.Ticker
	stopCh      chan struct{}
	wg          sync.WaitGroup
	enabled     atomic.Bool
}

// CollectorConfig configures the metrics collector.
type CollectorConfig struct {
	MetricsDir    string
	FlushSize     int
	FlushInterval time.Duration
}

// DefaultCollectorConfig returns sensible defaults for the collector.
func DefaultCollectorConfig(metricsDir string) CollectorConfig {
	return CollectorConfig{
		MetricsDir:    metricsDir,
		FlushSize:     100,
		FlushInterval: 5 * time.Second,
	}
}

var (
	globalCollector     *Collector
	globalCollectorOnce sync.Once
)

// GetCollector returns the global metrics collector singleton.
func GetCollector() *Collector {
	return globalCollector
}

// InitCollector initializes the global metrics collector.
func InitCollector(cfg CollectorConfig) (*Collector, error) {
	var initErr error
	globalCollectorOnce.Do(func() {
		store, err := NewStore(cfg.MetricsDir)
		if err != nil {
			initErr = err
			return
		}

		c := &Collector{
			store:       store,
			buffer:      make([]RequestRecord, 0, cfg.FlushSize*2),
			flushSize:   cfg.FlushSize,
			flushTicker: time.NewTicker(cfg.FlushInterval),
			stopCh:      make(chan struct{}),
		}
		c.enabled.Store(true)

		c.wg.Add(1)
		go c.backgroundFlush()

		globalCollector = c
	})
	return globalCollector, initErr
}

// RecordRequest records a single API request metric.
// This method is designed for minimal overhead (<1ms).
func (c *Collector) RecordRequest(
	provider string,
	model string,
	profile string,
	requestType routing.RequestType,
	latencyMs int64,
	errorType string,
) {
	if !c.enabled.Load() {
		return
	}

	record := RequestRecord{
		Timestamp:   time.Now().UTC(),
		Provider:    provider,
		Model:       model,
		Profile:     profile,
		RequestType: requestType,
		LatencyMs:   latencyMs,
		ErrorType:   errorType,
		Success:     errorType == "",
	}

	c.bufferMu.Lock()
	c.buffer = append(c.buffer, record)
	shouldFlush := len(c.buffer) >= c.flushSize
	c.bufferMu.Unlock()

	if shouldFlush {
		go c.flush()
	}
}

// RecordSuccess is a convenience method for successful requests.
func (c *Collector) RecordSuccess(provider, model, profile string, requestType routing.RequestType, latencyMs int64) {
	c.RecordRequest(provider, model, profile, requestType, latencyMs, "")
}

// RecordError is a convenience method for failed requests.
func (c *Collector) RecordError(provider, model, profile string, requestType routing.RequestType, latencyMs int64, errorType string) {
	c.RecordRequest(provider, model, profile, requestType, latencyMs, errorType)
}

// backgroundFlush periodically flushes the buffer.
func (c *Collector) backgroundFlush() {
	defer c.wg.Done()
	for {
		select {
		case <-c.flushTicker.C:
			c.flush()
		case <-c.stopCh:
			c.flush()
			return
		}
	}
}

// flush writes buffered records to storage.
func (c *Collector) flush() {
	c.bufferMu.Lock()
	if len(c.buffer) == 0 {
		c.bufferMu.Unlock()
		return
	}
	records := c.buffer
	c.buffer = make([]RequestRecord, 0, c.flushSize*2)
	c.bufferMu.Unlock()

	if err := c.store.WriteRecords(records); err != nil {
	}
}

// Flush forces an immediate flush of all buffered records.
func (c *Collector) Flush() {
	c.flush()
}

// Stop gracefully shuts down the collector.
func (c *Collector) Stop() {
	c.enabled.Store(false)
	close(c.stopCh)
	c.flushTicker.Stop()
	c.wg.Wait()
}

// Enable enables metrics collection.
func (c *Collector) Enable() {
	c.enabled.Store(true)
}

// Disable disables metrics collection.
func (c *Collector) Disable() {
	c.enabled.Store(false)
}

// IsEnabled returns whether collection is currently enabled.
func (c *Collector) IsEnabled() bool {
	return c.enabled.Load()
}

// GetStore returns the underlying metrics store.
func (c *Collector) GetStore() *Store {
	return c.store
}
