package home

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

const (
	clusterLatencyCapability = "cpa_latency_swap_v1"
	clusterLatencyTTL        = 2 * time.Minute
	clusterLatencyTimeout    = 3 * time.Second
)

type clusterLatencySample struct {
	IP         string    `json:"ip"`
	Port       int       `json:"port"`
	Sequence   int64     `json:"sequence"`
	ObservedAt time.Time `json:"observed_at"`
	AgeMS      int64     `json:"age_ms"`
	RTTUS      int64     `json:"rtt_us"`
	Valid      bool      `json:"valid"`
}

type clusterLatencyEntry struct {
	sample         clusterLatencySample
	measuredAt     time.Time
	nextProbeCycle int64
	nextProbeAt    time.Time
	probeBackoff   time.Duration
}

// Shared across replacement lifetimes; pending PINGs belong only to a subscription sampler.
type clusterLatencyHistory struct {
	mu            sync.Mutex
	sequence      int64
	entries       map[string]clusterLatencyEntry
	probeBaseline time.Time
}

func (h *clusterLatencyHistory) observe(node clusterNode, rtt time.Duration, valid bool, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entries == nil {
		h.entries = make(map[string]clusterLatencyEntry)
	}
	key := clusterNodeAddress(node)
	previous := h.entries[key]
	if len(h.entries) >= 64 && previous.measuredAt.IsZero() {
		oldestKey := ""
		var oldest time.Time
		for address, entry := range h.entries {
			if oldestKey == "" || entry.measuredAt.Before(oldest) {
				oldestKey, oldest = address, entry.measuredAt
			}
		}
		delete(h.entries, oldestKey)
	}
	h.sequence++
	if valid && rtt > 0 && previous.sample.Valid && now.Sub(previous.measuredAt) <= clusterLatencyTTL {
		rtt = (time.Duration(previous.sample.RTTUS)*time.Microsecond*3 + rtt) / 4
	}
	previous.sample = clusterLatencySample{IP: node.IP, Port: node.Port, Sequence: h.sequence, ObservedAt: now.UTC(), RTTUS: max(1, rtt.Microseconds()), Valid: valid && rtt > 0}
	previous.measuredAt = now
	h.entries[key] = previous
}

func (h *clusterLatencyHistory) probeDue(node clusterNode, reportAt time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[clusterNodeAddress(node)]
	if reportAt.Before(entry.nextProbeAt) {
		return false
	}
	// Round against one fixed baseline so ticker jitter cannot skip the next report cycle.
	return h.probeBaseline.IsZero() || int64(reportAt.Sub(h.probeBaseline).Round(time.Minute)/time.Minute) >= entry.nextProbeCycle
}

func (h *clusterLatencyHistory) observeProbe(node clusterNode, rtt time.Duration, valid bool, reportAt, now time.Time) {
	h.observe(node, rtt, valid, now)
	h.mu.Lock()
	defer h.mu.Unlock()
	address := clusterNodeAddress(node)
	entry := h.entries[address]
	if h.probeBaseline.IsZero() {
		h.probeBaseline = reportAt
	}
	entry.nextProbeCycle = int64(reportAt.Sub(h.probeBaseline).Round(time.Minute)/time.Minute) + 1
	entry.nextProbeAt = time.Time{}
	if !valid {
		entry.probeBackoff = min(max(time.Minute, entry.probeBackoff*2), 10*time.Minute)
		entry.nextProbeAt = now.Add(entry.probeBackoff)
	} else {
		entry.probeBackoff = 0
	}
	h.entries[address] = entry
}

func (h *clusterLatencyHistory) report(now time.Time) []clusterLatencySample {
	h.mu.Lock()
	defer h.mu.Unlock()
	var samples []clusterLatencySample
	for _, entry := range h.entries {
		age := now.Sub(entry.measuredAt)
		if age < 0 || age > clusterLatencyTTL {
			continue
		}
		sample := entry.sample
		sample.AgeMS = age.Milliseconds()
		samples = append(samples, sample)
	}
	return samples
}

type clusterRTTSampler struct {
	mu      sync.Mutex
	history *clusterLatencyHistory
	node    clusterNode
	token   string
	sentAt  time.Time
}

func (s *clusterRTTSampler) begin(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" {
		s.history.observe(s.node, 0, false, now)
	}
	s.token, s.sentAt = "home-rtt/"+uuid.NewString(), now
	return s.token
}

func (s *clusterRTTSampler) pong(token string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" || token != s.token {
		return false
	}
	rtt := now.Sub(s.sentAt)
	s.history.observe(s.node, max(time.Microsecond, rtt), rtt >= 0 && rtt <= clusterLatencyTimeout, now)
	s.token = ""
	return true
}

func (s *clusterRTTSampler) expire(now time.Time, force bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && (force || now.Sub(s.sentAt) > clusterLatencyTimeout) {
		s.history.observe(s.node, 0, false, now)
		s.token = ""
	}
}

// runClusterLatency returns subscription transport failures; report and probe failures remain telemetry-only.
func (c *Client) runClusterLatency(ctx context.Context, pubsub *redis.PubSub, sampler *clusterRTTSampler) error {
	sampleTimer := time.NewTimer(clusterLatencySampleDelay())
	defer sampleTimer.Stop()
	// Spread simultaneously started clients across a minute, then keep a fixed report cadence.
	reportTicker := time.NewTicker(clusterLatencyReportDelay())
	defer reportTicker.Stop()
	firstReport := true
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sampleTimer.C:
			c.mu.Lock()
			enabled := c.balance.latencySupported && c.balance.registered
			c.mu.Unlock()
			if enabled {
				token := sampler.begin(time.Now())
				if errPing := pubsub.Ping(ctx, token); errPing != nil {
					sampler.expire(time.Now(), true)
					// go-redis may already have resubscribed; only the lifecycle manager may replace membership.
					return fmt.Errorf("Home subscription telemetry PING: %w", errPing)
				}
			}
			sampleTimer.Reset(clusterLatencySampleDelay())
		case reportAt := <-reportTicker.C:
			if firstReport {
				// Reset before the RPC so report duration never adds to the recurring interval.
				reportTicker.Reset(time.Minute)
				firstReport = false
			}
			sampler.expire(time.Now(), false)
			if errReport := c.reportClusterLatency(ctx, sampler.history, reportAt); errReport != nil {
				// Telemetry failure must not end a healthy subscription or reclaim its membership.
				log.Warnf("Home latency telemetry failed: %v", errReport)
			}
		}
	}
}

func clusterLatencySampleDelay() time.Duration {
	return 27*time.Second + time.Duration(rand.Int64N(int64(6*time.Second)))
}

func clusterLatencyReportDelay() time.Duration {
	return time.Minute + time.Duration(1+rand.Int64N(int64(time.Minute)))
}

func (c *Client) reportClusterLatency(ctx context.Context, history *clusterLatencyHistory, reportAt time.Time) error {
	c.mu.Lock()
	eligible := c.canExecuteRebalanceLocked() && c.balance.registered && c.balance.latencySupported
	processID := c.instanceID
	c.mu.Unlock()
	if !eligible {
		return nil
	}
	payload, errJSON := json.Marshal(struct {
		ProcessID string                 `json:"process_id"`
		Samples   []clusterLatencySample `json:"samples"`
	}{ProcessID: processID, Samples: history.report(time.Now())})
	if errJSON != nil {
		return fmt.Errorf("encode Home latency report: %w", errJSON)
	}
	command, errClient := c.commandClient()
	if errClient != nil {
		return fmt.Errorf("get Home latency command client: %w", errClient)
	}
	// Reuse the command pool; telemetry must never close dispatch transports on cancellation.
	commandCtx, cancelCommand := context.WithTimeout(ctx, clusterLatencyTimeout)
	raw, errReport := command.Do(commandCtx, "CLUSTER", "BALANCE", "LATENCY", string(payload)).Text()
	cancelCommand()
	if errReport != nil {
		return fmt.Errorf("report Home latency: %w", errReport)
	}
	var response struct {
		OK    bool         `json:"ok"`
		Probe *clusterNode `json:"probe"`
	}
	if errDecode := json.Unmarshal([]byte(raw), &response); errDecode != nil {
		return fmt.Errorf("decode Home latency response: %w", errDecode)
	}
	if !response.OK {
		return fmt.Errorf("Home latency report rejected")
	}
	if response.Probe == nil {
		return nil
	}
	target := *response.Probe
	c.mu.Lock()
	known := false
	for _, node := range c.clusterNodes {
		known = known || (node.IP == target.IP && node.Port == target.Port)
	}
	current := target.IP == c.homeCfg.Host && target.Port == c.homeCfg.Port
	c.mu.Unlock()
	if !known || current || target.IP == "" || target.Port <= 0 || target.Port > 65535 {
		return nil
	}
	// One worker, at most one probe per report cycle, with per-target failure backoff.
	if !history.probeDue(target, reportAt) {
		return nil
	}
	address := clusterNodeAddress(target)
	probeCtx, cancelProbe := context.WithTimeout(ctx, clusterLatencyTimeout)
	rtt, errProbe := c.probeClusterLatency(probeCtx, address)
	cancelProbe()
	history.observeProbe(target, rtt, errProbe == nil, reportAt, time.Now())
	if errProbe != nil {
		return fmt.Errorf("probe Home latency: %w", errProbe)
	}
	return nil
}

// The first PING establishes mTLS; only the second round trip is timed. Neither subscribes.
func (c *Client) probeClusterLatency(ctx context.Context, address string) (time.Duration, error) {
	c.mu.Lock()
	options, tlsSettings := c.redisOptionsSnapshotLocked(address)
	c.mu.Unlock()
	// Loading and parsing certificate files must not block dispatch on the client's mutex.
	tlsConfig, errTLS := newHomeTLSConfig(tlsSettings, "")
	if errTLS != nil {
		return 0, errTLS
	}
	options.TLSConfig = tlsConfig
	options.ReadTimeout, options.WriteTimeout, options.MaxRetries = -1, -1, -1
	options.PoolSize = 1
	baseDialer := redis.NewDialer(options)
	options.Dialer = func(dialCtx context.Context, network, dialAddress string) (net.Conn, error) {
		conn, errDial := baseDialer(dialCtx, network, dialAddress)
		if errDial != nil {
			return nil, errDial
		}
		if ctx.Err() != nil {
			_ = closeUnderlyingTransport(conn)
			return nil, context.Cause(ctx)
		}
		return newPluginSyncCancelableConn(ctx, conn), nil
	}
	probe := redis.NewClient(options)
	stopCancel := context.AfterFunc(ctx, func() { _ = probe.Close() })
	defer func() {
		stopCancel()
		_ = probe.Close()
	}()
	if errWarmup := probe.Ping(ctx).Err(); errWarmup != nil {
		return 0, errWarmup
	}
	token := "home-probe/" + uuid.NewString()
	started := time.Now()
	response, errPing := probe.Do(ctx, "PING", token).Text()
	rtt := max(time.Microsecond, time.Since(started))
	if errPing == nil && response != token {
		return 0, ErrEmptyResponse
	}
	return rtt, errPing
}
