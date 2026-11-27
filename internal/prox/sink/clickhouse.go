// Package sink ships per-request usage rows to ClickHouse over its HTTP
// interface (no driver dependency). Rows are batched in memory and dropped,
// with a warning, when ClickHouse is unreachable for long enough to fill
// the buffer; request handling is never blocked.
package sink

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Config is the clickhouse block of plugins.configs.prox.
type Config struct {
	URL      string `yaml:"url" json:"url,omitempty"`
	Database string `yaml:"database" json:"database"`
	Table    string `yaml:"table" json:"table"`
	Username string `yaml:"username" json:"username,omitempty"`
	Password string `yaml:"password" json:"-"`
	// PasswordEnv names an environment variable holding the password.
	PasswordEnv   string        `yaml:"password_env" json:"password_env,omitempty"`
	FlushInterval time.Duration `yaml:"flush_interval" json:"flush_interval"`
	BatchSize     int           `yaml:"batch_size" json:"batch_size"`
	BufferSize    int           `yaml:"buffer_size" json:"buffer_size"`
	Timeout       time.Duration `yaml:"timeout" json:"timeout"`
}

// Default returns sink defaults; an empty URL disables the sink.
func Default() Config {
	return Config{Database: "prox", Table: "requests", FlushInterval: 5 * time.Second, BatchSize: 500, BufferSize: 20000, Timeout: 30 * time.Second}
}

// Decode reads the clickhouse block from the prox settings YAML.
func Decode(raw []byte) (Config, error) {
	var wrapper struct {
		ClickHouse yaml.Node `yaml:"clickhouse"`
	}
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := yaml.Unmarshal(raw, &wrapper); err != nil {
			return Config{}, fmt.Errorf("clickhouse: invalid YAML")
		}
		if wrapper.ClickHouse.Kind != 0 {
			if err := wrapper.ClickHouse.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("clickhouse: %w", err)
			}
		}
	}
	if cfg.URL == "" {
		return cfg, nil
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("clickhouse: url must be http(s)://host:port")
	}
	if !identifier.MatchString(cfg.Database) || !identifier.MatchString(cfg.Table) {
		return Config{}, fmt.Errorf("clickhouse: database and table must be plain identifiers")
	}
	if cfg.FlushInterval < time.Second || cfg.BatchSize < 1 || cfg.BufferSize < cfg.BatchSize || cfg.Timeout < time.Second {
		return Config{}, fmt.Errorf("clickhouse: flush_interval >= 1s, batch_size >= 1, buffer_size >= batch_size, timeout >= 1s")
	}
	if cfg.PasswordEnv != "" && cfg.Password == "" {
		cfg.Password = os.Getenv(cfg.PasswordEnv)
	}
	return cfg, nil
}

// Row is one upstream attempt.
type Row struct {
	TS               time.Time `json:"ts"`
	RequestID        string    `json:"request_id"`
	TraceID          string    `json:"trace_id"`
	Provider         string    `json:"provider"`
	AuthID           string    `json:"auth_id"`
	Model            string    `json:"model"`
	ResponseModel    string    `json:"response_model"`
	Alias            string    `json:"alias"`
	Client           string    `json:"client"`
	SessionKey       string    `json:"session_key"`
	ParentSessionKey string    `json:"parent_session_key"`
	Stream           uint8     `json:"stream"`
	Failed           uint8     `json:"failed"`
	Status           uint16    `json:"status"`
	FailCode         string    `json:"fail_code"`
	LatencyMS        uint32    `json:"latency_ms"`
	TTFTMS           uint32    `json:"ttft_ms"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	ReasoningTokens  int64     `json:"reasoning_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	ReasoningEffort  string    `json:"reasoning_effort"`
	ServiceTier      string    `json:"service_tier"`
	Executor         string    `json:"executor"`
}

const schema = `CREATE TABLE IF NOT EXISTS %s.%s (
  ts DateTime64(3, 'UTC'),
  request_id String,
  trace_id String,
  provider LowCardinality(String),
  auth_id String,
  model LowCardinality(String),
  response_model LowCardinality(String),
  alias String,
  client LowCardinality(String),
  session_key String,
  parent_session_key String,
  stream UInt8,
  failed UInt8,
  status UInt16,
  fail_code LowCardinality(String),
  latency_ms UInt32,
  ttft_ms UInt32,
  input_tokens UInt64,
  output_tokens UInt64,
  reasoning_tokens UInt64,
  cache_read_tokens UInt64,
  cache_write_tokens UInt64,
  cost_usd Float64,
  reasoning_effort LowCardinality(String),
  service_tier LowCardinality(String),
  executor LowCardinality(String)
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (provider, ts)`

// ClickHouse batches rows and inserts them.
type ClickHouse struct {
	Client *http.Client

	mu      sync.Mutex
	cfg     Config
	queue   chan Row
	stop    chan struct{}
	done    chan struct{}
	ready   bool
	dropped uint64
	sent    uint64
	lastErr string
}

// Status describes the sink for the dashboard.
type Status struct {
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
	Sent    uint64 `json:"sent"`
	Dropped uint64 `json:"dropped"`
	Queued  int    `json:"queued"`
	Error   string `json:"error,omitempty"`
}

// New starts a sink for cfg.
func New(cfg Config) *ClickHouse {
	c := &ClickHouse{cfg: cfg, queue: make(chan Row, cfg.BufferSize), stop: make(chan struct{}), done: make(chan struct{})}
	go c.run()
	return c
}

// Config returns the active settings.
func (c *ClickHouse) Config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// Add queues a row without blocking.
func (c *ClickHouse) Add(r Row) {
	select {
	case c.queue <- r:
	default:
		c.mu.Lock()
		c.dropped++
		n := c.dropped
		c.mu.Unlock()
		if n == 1 || n%1000 == 0 {
			log.Warnf("prox clickhouse: buffer full, dropped %d rows", n)
		}
	}
}

// Status reports counters.
func (c *ClickHouse) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Enabled: true, Ready: c.ready, Sent: c.sent, Dropped: c.dropped, Queued: len(c.queue), Error: c.lastErr}
}

// Close flushes what is queued (best effort) and stops.
func (c *ClickHouse) Close() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	<-c.done
}

func (c *ClickHouse) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func (c *ClickHouse) run() {
	defer close(c.done)
	cfg := c.Config()
	tick := time.NewTicker(cfg.FlushInterval)
	defer tick.Stop()
	batch := make([]Row, 0, cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if !c.isReady() {
			if err := c.ensureTable(); err != nil {
				c.setErr(err)
				// Keep the batch; retry on the next tick unless it would grow unbounded.
				if len(batch) >= cfg.BufferSize {
					c.mu.Lock()
					c.dropped += uint64(len(batch))
					c.mu.Unlock()
					batch = batch[:0]
				}
				return
			}
		}
		if err := c.insert(batch); err != nil {
			c.setErr(err)
			if len(batch) >= cfg.BufferSize {
				c.mu.Lock()
				c.dropped += uint64(len(batch))
				c.mu.Unlock()
				batch = batch[:0]
			}
			return
		}
		c.mu.Lock()
		c.sent += uint64(len(batch))
		c.lastErr = ""
		c.mu.Unlock()
		batch = batch[:0]
	}
	for {
		select {
		case <-c.stop:
			for {
				select {
				case r := <-c.queue:
					batch = append(batch, r)
				default:
					flush()
					return
				}
			}
		case r := <-c.queue:
			batch = append(batch, r)
			if len(batch) >= cfg.BatchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (c *ClickHouse) isReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *ClickHouse) setErr(err error) {
	c.mu.Lock()
	first := c.lastErr == ""
	c.lastErr = err.Error()
	c.mu.Unlock()
	if first {
		log.WithError(err).Warn("prox clickhouse: write failed; will retry")
	}
}

func (c *ClickHouse) ensureTable() error {
	cfg := c.Config()
	if err := c.exec(cfg, "CREATE DATABASE IF NOT EXISTS "+cfg.Database, nil); err != nil {
		return err
	}
	if err := c.exec(cfg, fmt.Sprintf(schema, cfg.Database, cfg.Table), nil); err != nil {
		return err
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	return nil
}

func (c *ClickHouse) insert(rows []Row) error {
	cfg := c.Config()
	var buf bytes.Buffer
	for _, r := range rows {
		r.TS = r.TS.UTC()
		raw, err := json.Marshal(r)
		if err != nil {
			continue
		}
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	return c.exec(cfg, fmt.Sprintf("INSERT INTO %s.%s FORMAT JSONEachRow", cfg.Database, cfg.Table), &buf)
}

func (c *ClickHouse) exec(cfg Config, query string, body io.Reader) error {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("query", query)
	// Accept RFC3339 timestamps in JSONEachRow.
	q.Set("date_time_input_format", "best_effort")
	u.RawQuery = q.Encode()
	if body == nil {
		body = http.NoBody
	}
	// ClickHouse is a local analytics side channel, not upstream model
	// traffic; bound each write so a hung server cannot stall the sink.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return err
	}
	if cfg.Username != "" {
		req.Header.Set("X-ClickHouse-User", cfg.Username)
		req.Header.Set("X-ClickHouse-Key", cfg.Password)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
