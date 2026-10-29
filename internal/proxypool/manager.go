// Package proxypool resolves account-level proxy pools into concrete proxy
// node URLs and tracks node health with periodic probes.
package proxypool

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// Strategy selects how nodes are assigned to accounts.
type Strategy string

const (
	// StrategySticky keeps an account on the same healthy node.
	StrategySticky Strategy = "sticky"
	// StrategyRoundRobin rotates accounts across healthy nodes.
	StrategyRoundRobin Strategy = "round-robin"
)

const (
	// DefaultHealthCheckInterval is the probe cadence used when a pool does
	// not configure interval-seconds.
	DefaultHealthCheckInterval = 60 * time.Second
	// DefaultHealthCheckTimeout bounds a single probe attempt.
	DefaultHealthCheckTimeout = 5 * time.Second
	// DefaultHealthCheckTestURL is the probe target used when enabled without
	// an explicit test-url.
	DefaultHealthCheckTestURL = "https://www.google.com"
	// DefaultFailureThreshold is the number of consecutive failures required
	// before a node is removed from assignment.
	DefaultFailureThreshold = 3
	// DefaultRecoveryThreshold is the number of consecutive successes required
	// before a failed node returns to assignment.
	DefaultRecoveryThreshold = 1
)

// HealthCheck describes periodic node probing for a pool.
type HealthCheck struct {
	Enabled  bool
	TestURL  string
	Interval time.Duration
	Timeout  time.Duration
}

// Config is a validated proxy pool definition.
type Config struct {
	ID          string
	Strategy    Strategy
	Nodes       []string
	HealthCheck HealthCheck
}

// ProbeFunc performs one health probe against a proxy node.
type ProbeFunc func(ctx context.Context, nodeURL, testURL string, timeout time.Duration) error

// Ticker is the probe cadence source. Tests inject a controllable
// implementation instead of waiting on wall-clock timers.
type Ticker interface {
	// Chan returns the channel that fires on each probe round.
	Chan() <-chan time.Time
	// Stop releases the ticker resources.
	Stop()
}

// IntervalFunc builds a Ticker for the given interval.
type IntervalFunc func(interval time.Duration) Ticker

// Option customizes a Manager. Options exist primarily to make probe timing
// and probe results deterministic in tests.
type Option func(*Manager)

// WithProbeFunc overrides the network probe implementation.
func WithProbeFunc(fn ProbeFunc) Option {
	return func(m *Manager) {
		if fn != nil {
			m.probe = fn
		}
	}
}

// WithIntervalFunc overrides the probe ticker source.
func WithIntervalFunc(fn IntervalFunc) Option {
	return func(m *Manager) {
		if fn != nil {
			m.interval = fn
		}
	}
}

// WithNowFunc overrides the manager clock.
func WithNowFunc(fn func() time.Time) Option {
	return func(m *Manager) {
		if fn != nil {
			m.now = fn
		}
	}
}

// WithFailureThreshold overrides how many consecutive failures remove a node.
func WithFailureThreshold(threshold int) Option {
	return func(m *Manager) {
		if threshold > 0 {
			m.failureThreshold = threshold
		}
	}
}

// WithRecoveryThreshold overrides how many consecutive successes restore a node.
func WithRecoveryThreshold(threshold int) Option {
	return func(m *Manager) {
		if threshold > 0 {
			m.recoveryThreshold = threshold
		}
	}
}

type nodeState struct {
	url         string
	healthy     bool
	failures    int
	successes   int
	lastChecked time.Time
}

type poolState struct {
	cfg    Config
	nodes  []*nodeState
	byURL  map[string]*nodeState
	cursor int
}

// Manager owns proxy pool state. It is safe for concurrent use.
type Manager struct {
	mu       sync.RWMutex
	pools    map[string]*poolState
	order    []string
	probe    ProbeFunc
	interval IntervalFunc
	now      func() time.Time

	failureThreshold  int
	recoveryThreshold int

	started bool
	closed  bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// ConfigsFromConfig converts validated application config into pool configs,
// applying defaults for strategy, health-check interval, timeout, and test URL.
func ConfigsFromConfig(pools []internalconfig.ProxyPoolConfig) ([]Config, error) {
	out := make([]Config, 0, len(pools))
	for index, pool := range pools {
		id := strings.TrimSpace(pool.ID)
		if id == "" {
			return nil, fmt.Errorf("proxy pool[%d]: id is required", index)
		}
		strategy := Strategy(internalconfig.NormalizeProxyPoolStrategy(pool.Strategy))
		if strategy != StrategySticky && strategy != StrategyRoundRobin {
			return nil, fmt.Errorf("proxy pool %q: unsupported strategy %q", id, pool.Strategy)
		}
		nodes := make([]string, 0, len(pool.Nodes))
		for nodeIndex, node := range pool.Nodes {
			trimmed := strings.TrimSpace(node)
			if trimmed == "" {
				return nil, fmt.Errorf("proxy pool %q: nodes[%d] is empty", id, nodeIndex)
			}
			nodes = append(nodes, trimmed)
		}
		if len(nodes) == 0 {
			return nil, fmt.Errorf("proxy pool %q: nodes must not be empty", id)
		}
		healthCheck := HealthCheck{
			Enabled:  pool.HealthCheck.Enabled,
			TestURL:  strings.TrimSpace(pool.HealthCheck.TestURL),
			Interval: time.Duration(pool.HealthCheck.IntervalSeconds) * time.Second,
			Timeout:  time.Duration(pool.HealthCheck.TimeoutSeconds) * time.Second,
		}
		if healthCheck.Interval <= 0 {
			healthCheck.Interval = DefaultHealthCheckInterval
		}
		if healthCheck.Timeout <= 0 {
			healthCheck.Timeout = DefaultHealthCheckTimeout
		}
		if healthCheck.TestURL == "" {
			healthCheck.TestURL = DefaultHealthCheckTestURL
		}
		out = append(out, Config{ID: id, Strategy: strategy, Nodes: nodes, HealthCheck: healthCheck})
	}
	return out, nil
}

// NewManager validates pool configs and builds a manager without starting any
// background work. Call Start to begin periodic health probes; calling Start is
// optional when health checks are disabled.
func NewManager(configs []Config, opts ...Option) (*Manager, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	manager := &Manager{
		pools:             make(map[string]*poolState, len(configs)),
		probe:             defaultProbe,
		interval:          defaultInterval,
		now:               time.Now,
		failureThreshold:  DefaultFailureThreshold,
		recoveryThreshold: DefaultRecoveryThreshold,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	for index, cfg := range configs {
		id := strings.TrimSpace(cfg.ID)
		if id == "" {
			return nil, fmt.Errorf("proxy pool[%d]: id is required", index)
		}
		if _, exists := manager.pools[id]; exists {
			return nil, fmt.Errorf("proxy pool %q: duplicate id", id)
		}
		strategy := Strategy(strings.ToLower(strings.TrimSpace(string(cfg.Strategy))))
		if strategy == "" {
			strategy = StrategySticky
		}
		if strategy != StrategySticky && strategy != StrategyRoundRobin {
			return nil, fmt.Errorf("proxy pool %q: unsupported strategy %q", id, cfg.Strategy)
		}
		if len(cfg.Nodes) == 0 {
			return nil, fmt.Errorf("proxy pool %q: nodes must not be empty", id)
		}
		state := &poolState{cfg: cfg, byURL: make(map[string]*nodeState, len(cfg.Nodes))}
		state.cfg.Strategy = strategy
		for nodeIndex, node := range cfg.Nodes {
			trimmed := strings.TrimSpace(node)
			if trimmed == "" {
				return nil, fmt.Errorf("proxy pool %q: nodes[%d] is empty", id, nodeIndex)
			}
			if _, exists := state.byURL[trimmed]; exists {
				continue
			}
			setting, errParse := proxyutil.Parse(trimmed)
			if errParse != nil {
				return nil, fmt.Errorf("proxy pool %q: nodes[%d] is invalid: %w", id, nodeIndex, errParse)
			}
			if setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
				return nil, fmt.Errorf("proxy pool %q: nodes[%d] is not a concrete proxy", id, nodeIndex)
			}
			entry := &nodeState{url: trimmed, healthy: true}
			state.nodes = append(state.nodes, entry)
			state.byURL[trimmed] = entry
		}
		if len(state.nodes) == 0 {
			return nil, fmt.Errorf("proxy pool %q: nodes must not be empty", id)
		}
		if state.cfg.HealthCheck.Interval <= 0 {
			state.cfg.HealthCheck.Interval = DefaultHealthCheckInterval
		}
		if state.cfg.HealthCheck.Timeout <= 0 {
			state.cfg.HealthCheck.Timeout = DefaultHealthCheckTimeout
		}
		if state.cfg.HealthCheck.TestURL == "" {
			state.cfg.HealthCheck.TestURL = DefaultHealthCheckTestURL
		}
		manager.pools[id] = state
		manager.order = append(manager.order, id)
	}
	return manager, nil
}

type timeTicker struct {
	ticker *time.Ticker
}

func (t timeTicker) Chan() <-chan time.Time { return t.ticker.C }
func (t timeTicker) Stop()                  { t.ticker.Stop() }

func defaultInterval(interval time.Duration) Ticker {
	return timeTicker{ticker: time.NewTicker(interval)}
}

// Strategy returns the assignment strategy of poolID.
func (m *Manager) Strategy(poolID string) (Strategy, error) {
	if m == nil {
		return "", fmt.Errorf("proxy pool manager is not configured")
	}
	poolID = strings.TrimSpace(poolID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.pools[poolID]
	if state == nil {
		return "", fmt.Errorf("proxy pool %q not found", poolID)
	}
	return state.cfg.Strategy, nil
}

// Pools returns the configured pool IDs in declaration order.
func (m *Manager) Pools() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// Start launches one probe goroutine per pool with health checking enabled.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.started || m.closed {
		m.mu.Unlock()
		return
	}
	m.started = true
	probeCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	ids := make([]string, 0, len(m.order))
	for _, id := range m.order {
		if m.pools[id] != nil && m.pools[id].cfg.HealthCheck.Enabled {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.wg.Add(1)
		go m.probeLoop(probeCtx, id)
	}
}

// Close stops probe goroutines and waits for them to exit.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

func (m *Manager) probeLoop(ctx context.Context, poolID string) {
	defer m.wg.Done()
	interval := m.poolInterval(poolID)
	if interval <= 0 {
		return
	}
	ticker := m.interval(interval)
	if ticker == nil {
		return
	}
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticker.Chan():
			if !ok {
				return
			}
			m.ProbePool(ctx, poolID)
		}
	}
}

func (m *Manager) poolInterval(poolID string) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.pools[poolID]
	if state == nil {
		return 0
	}
	return state.cfg.HealthCheck.Interval
}

func (m *Manager) poolTimeout(poolID string) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.pools[poolID]
	if state == nil {
		return DefaultHealthCheckTimeout
	}
	return state.cfg.HealthCheck.Timeout
}

// ProbePool probes every node of poolID once and records the results.
// It returns the number of probed nodes. Unknown pools report zero.
func (m *Manager) ProbePool(ctx context.Context, poolID string) int {
	if m == nil {
		return 0
	}
	poolID = strings.TrimSpace(poolID)
	m.mu.RLock()
	state := m.pools[poolID]
	if state == nil {
		m.mu.RUnlock()
		return 0
	}
	nodes := make([]*nodeState, len(state.nodes))
	copy(nodes, state.nodes)
	testURL := state.cfg.HealthCheck.TestURL
	timeout := state.cfg.HealthCheck.Timeout
	m.mu.RUnlock()

	for _, node := range nodes {
		errProbe := m.probe(ctx, node.url, testURL, timeout)
		m.recordProbeResult(poolID, node.url, errProbe)
	}
	return len(nodes)
}

// ReportDialResult feeds a request-path dial outcome into the node circuit
// breaker. A nil error counts as a success.
func (m *Manager) ReportDialResult(poolID, nodeURL string, err error) {
	if m == nil {
		return
	}
	m.recordProbeResult(strings.TrimSpace(poolID), strings.TrimSpace(nodeURL), err)
}

func (m *Manager) recordProbeResult(poolID, nodeURL string, errProbe error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.pools[poolID]
	if state == nil {
		return
	}
	node := state.byURL[nodeURL]
	if node == nil {
		return
	}
	node.lastChecked = m.now()
	if errProbe == nil {
		node.failures = 0
		node.successes++
		if !node.healthy && node.successes >= m.recoveryThreshold {
			node.healthy = true
			log.WithFields(log.Fields{
				"pool_id": poolID,
				"node":    proxyutil.Redact(node.url),
			}).Info("proxy pool node recovered")
		}
		return
	}
	node.successes = 0
	node.failures++
	if node.healthy && node.failures >= m.failureThreshold {
		node.healthy = false
		log.WithFields(log.Fields{
			"pool_id":  poolID,
			"node":     proxyutil.Redact(node.url),
			"failures": node.failures,
			"error":    errProbe.Error(),
		}).Warn("proxy pool node marked unhealthy")
	}
}

// Resolve returns a concrete proxy URL for an account. Repeated calls for the
// same sticky account return the same node while it remains healthy.
func (m *Manager) Resolve(provider, authID, poolID string) (string, error) {
	return m.ResolveNode(provider, authID, poolID, "")
}

// ResolveNode resolves a proxy URL for an account, preferring boundNode when it
// belongs to a sticky pool and is still healthy. The returned value is the
// concrete node URL that callers should persist as the account binding.
func (m *Manager) ResolveNode(provider, authID, poolID, boundNode string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("proxy pool manager is not configured")
	}
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return "", fmt.Errorf("proxy pool id is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.pools[poolID]
	if state == nil {
		return "", fmt.Errorf("proxy pool %q not found", poolID)
	}

	if state.cfg.Strategy == StrategyRoundRobin {
		node := state.nextAvailable()
		if node == nil {
			return "", fmt.Errorf("proxy pool %q has no healthy nodes", poolID)
		}
		return node.url, nil
	}

	bound := strings.TrimSpace(boundNode)
	start := stickyIndex(provider, authID, len(state.nodes))
	if bound != "" {
		if node := state.byURL[bound]; node != nil {
			if node.healthy {
				return node.url, nil
			}
			start = state.indexOf(node)
		}
	}
	node := state.firstAvailableFrom(start)
	if node == nil {
		return "", fmt.Errorf("proxy pool %q has no healthy nodes", poolID)
	}
	return node.url, nil
}

// NodeStatus is a point-in-time view of one node's health.
type NodeStatus struct {
	URL                  string
	Healthy              bool
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastChecked          time.Time
}

// Status returns the current health snapshot for poolID.
func (m *Manager) Status(poolID string) ([]NodeStatus, error) {
	if m == nil {
		return nil, fmt.Errorf("proxy pool manager is not configured")
	}
	poolID = strings.TrimSpace(poolID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	state := m.pools[poolID]
	if state == nil {
		return nil, fmt.Errorf("proxy pool %q not found", poolID)
	}
	out := make([]NodeStatus, 0, len(state.nodes))
	for _, node := range state.nodes {
		out = append(out, NodeStatus{
			URL:                  node.url,
			Healthy:              node.healthy,
			ConsecutiveFailures:  node.failures,
			ConsecutiveSuccesses: node.successes,
			LastChecked:          node.lastChecked,
		})
	}
	return out, nil
}

// PoolIDs returns sorted pool IDs, useful for diagnostics.
func (m *Manager) PoolIDs() []string {
	ids := m.Pools()
	sort.Strings(ids)
	return ids
}

func (p *poolState) indexOf(node *nodeState) int {
	for index, candidate := range p.nodes {
		if candidate == node {
			return index
		}
	}
	return 0
}

func (p *poolState) firstAvailableFrom(start int) *nodeState {
	if len(p.nodes) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	for offset := 0; offset < len(p.nodes); offset++ {
		node := p.nodes[(start+offset)%len(p.nodes)]
		if node.healthy {
			return node
		}
	}
	return nil
}

func (p *poolState) nextAvailable() *nodeState {
	if len(p.nodes) == 0 {
		return nil
	}
	for offset := 0; offset < len(p.nodes); offset++ {
		index := (p.cursor + offset) % len(p.nodes)
		node := p.nodes[index]
		if node.healthy {
			p.cursor = (index + 1) % len(p.nodes)
			return node
		}
	}
	return nil
}

func stickyIndex(provider, authID string, count int) int {
	if count <= 0 {
		return 0
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(authID)))
	return int(binary.BigEndian.Uint64(digest[:8]) % uint64(count))
}
