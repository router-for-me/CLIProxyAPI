// Package otelplugin implements a CLIProxyAPI usage.Plugin that exports
// one OpenTelemetry span per upstream LLM call. It complements the
// in-memory LoggerPlugin and the Redis queue plugin: the records flow
// through the same coreusage.Plugin pipeline, no proxy internals change.
//
// Two surfaces ship with this package:
//
//  1. Plugin (HandleUsage) — emits a `gen_ai.request` span per record.
//     Span attributes follow the emerging OpenTelemetry GenAI semantic
//     conventions and carry caller-supplied baggage identity when a Gin
//     middleware extracts a W3C `baggage:` header upstream.
//
//  2. Baggage middleware — Gin middleware that parses the inbound
//     `baggage:` header, stores it on the request context, and (when
//     propagation is enabled) forwards an allowlisted subset to the
//     upstream provider.
//
// Config is sourced from the existing yaml/env config surface; toggles
// mirror the redisqueue plugin pattern so operators have a consistent
// enable/disable + management-API contract.
package otelplugin

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config bundles the optional configuration block plugin authors may attach
// to the existing CLIProxyAPI config. Embedding it into a top-level
// `telemetry` key keeps the change small for operators who do not opt in.
//
// Example (config.yaml):
//
//	telemetry:
//	  otlp:
//	    enabled: true
//	    endpoint: "http://127.0.0.1:4318"
//	    protocol: "http/protobuf"
//	    service:
//	      name: "cli-proxy-api"
//	      namespace: "agent-platform"
//	    span:
//	      name: "gen_ai.request"
//	      include_baggage_keys: ["agent.id", "workload.kind"]
//	  baggage:
//	    propagation: "allowlist"   # off | propagate | allowlist
//	    allowed_keys: ["agent.id", "workload.kind"]
//	  cost:
//	    enabled: false             # optional pricing-table companion
type Config struct {
	OTLP    OTLPConfig    `yaml:"otlp" json:"otlp"`
	Baggage BaggageConfig `yaml:"baggage" json:"baggage"`
	Cost    CostConfig    `yaml:"cost" json:"cost"`
}

// OTLPConfig configures the trace exporter.
type OTLPConfig struct {
	Enabled  bool              `yaml:"enabled" json:"enabled"`
	Endpoint string            `yaml:"endpoint" json:"endpoint"`
	Protocol string            `yaml:"protocol" json:"protocol"` // http/protobuf or grpc
	Headers  map[string]string `yaml:"headers" json:"headers"`
	Service  ServiceConfig     `yaml:"service" json:"service"`
	Span     SpanConfig        `yaml:"span" json:"span"`
}

// ServiceConfig describes the OpenTelemetry resource attributes attached to
// every span. service.name / service.namespace land on the Resource so all
// downstream backends group records by emitter without per-span attributes.
type ServiceConfig struct {
	Name      string `yaml:"name" json:"name"`
	Namespace string `yaml:"namespace" json:"namespace"`
}

// SpanConfig controls per-span attribute selection.
//
//   - Name defaults to "gen_ai.request" (OpenTelemetry GenAI semconv).
//     Operators preferring the local "llm.request" convention can set it.
//   - IncludeBaggageKeys is the allowlist of baggage keys that are copied
//     verbatim onto span attributes. Empty means "all baggage keys".
//   - IncludeUsage / IncludeCost default to true; setting them false produces
//     an attribution-only span useful when token counts come from a separate
//     emitter (rare).
type SpanConfig struct {
	Name               string   `yaml:"name" json:"name"`
	IncludeBaggageKeys []string `yaml:"include_baggage_keys" json:"include_baggage_keys"`
	IncludeUsage       *bool    `yaml:"include_usage,omitempty" json:"include_usage,omitempty"`
	IncludeCost        *bool    `yaml:"include_cost,omitempty" json:"include_cost,omitempty"`
}

// BaggagePropagationMode mirrors the W3C Baggage propagation modes the SDK
// supports — off is the safe default for a trusted-boundary proxy.
type BaggagePropagationMode string

const (
	BaggageOff       BaggagePropagationMode = "off"
	BaggagePropagate BaggagePropagationMode = "propagate"
	BaggageAllowlist BaggagePropagationMode = "allowlist"
)

// BaggageConfig configures W3C Baggage propagation policy. Always-on parsing
// (so plugin attributes still see the inbound keys) — propagation controls
// the *outbound* re-emission only.
type BaggageConfig struct {
	Propagation BaggagePropagationMode `yaml:"propagation" json:"propagation"`
	AllowedKeys []string               `yaml:"allowed_keys" json:"allowed_keys"`
}

// CostConfig holds an optional per-million-token pricing table. When
// disabled, the plugin emits gen_ai.usage.* attributes but no cost.usd.
type CostConfig struct {
	Enabled bool                       `yaml:"enabled" json:"enabled"`
	Pricing map[string]ModelPricingUSD `yaml:"pricing" json:"pricing"`
}

// ModelPricingUSD is the per-million-token pricing table for one model id.
type ModelPricingUSD struct {
	InputPerMillion         float64 `yaml:"input_per_million" json:"input_per_million"`
	OutputPerMillion        float64 `yaml:"output_per_million" json:"output_per_million"`
	CacheReadPerMillion     float64 `yaml:"cache_read_per_million" json:"cache_read_per_million"`
	CacheCreationPerMillion float64 `yaml:"cache_creation_per_million" json:"cache_creation_per_million"`
}

// ---- Runtime config snapshot ------------------------------------------------
//
// The plugin reads the active config from an atomic.Value rather than from a
// global mutex so HandleUsage stays lock-free on the hot path. SetConfig is
// the only writer; it is called from the config loader at startup and from
// the watcher on hot-reload (consistent with redisqueue.SetEnabled).

var (
	activeConfig atomic.Value // stores *Config
	enabled      atomic.Bool
)

func init() {
	// Safe default: feature flag off until SetConfig is called.
	enabled.Store(false)
	activeConfig.Store(defaultConfig())
}

// SetConfig replaces the active configuration. The plugin's HandleUsage
// hot-path reads via atomic.Value.Load so this can be called concurrently
// without coordination.
//
// Returns the previous configuration so callers can diff for reload logging.
func SetConfig(cfg Config) Config {
	prev := loadConfig()
	cfg = applyDefaults(cfg)
	activeConfig.Store(&cfg)
	enabled.Store(cfg.OTLP.Enabled)
	return *prev
}

// CurrentConfig returns a copy of the active configuration. The copy is
// shallow on slices/maps to keep the call cheap; callers must treat the
// returned value as read-only.
func CurrentConfig() Config {
	cfg := loadConfig()
	return *cfg
}

// Enabled reports whether the OTLP exporter is on. Callers (HandleUsage,
// middleware) use this as the cheap early-return guard.
func Enabled() bool { return enabled.Load() }

// SetEnabled toggles the OTLP exporter without rewriting the entire config.
// Used by the management API and tests.
func SetEnabled(on bool) {
	enabled.Store(on)
	cfg := loadConfig()
	updated := *cfg
	updated.OTLP.Enabled = on
	activeConfig.Store(&updated)
}

// loadConfig returns the active config pointer; never nil after init().
func loadConfig() *Config {
	raw := activeConfig.Load()
	if raw == nil {
		return defaultConfig()
	}
	cfg, ok := raw.(*Config)
	if !ok || cfg == nil {
		return defaultConfig()
	}
	return cfg
}

// defaultConfig returns the package's built-in defaults. The exporter is
// off until SetConfig is called.
func defaultConfig() *Config {
	cfg := applyDefaults(Config{})
	return &cfg
}

func applyDefaults(cfg Config) Config {
	if strings.TrimSpace(cfg.OTLP.Endpoint) == "" {
		cfg.OTLP.Endpoint = "http://127.0.0.1:4318"
	}
	if strings.TrimSpace(cfg.OTLP.Protocol) == "" {
		cfg.OTLP.Protocol = "http/protobuf"
	}
	if strings.TrimSpace(cfg.OTLP.Service.Name) == "" {
		cfg.OTLP.Service.Name = "cli-proxy-api"
	}
	if strings.TrimSpace(cfg.OTLP.Span.Name) == "" {
		cfg.OTLP.Span.Name = "gen_ai.request"
	}
	if cfg.Baggage.Propagation == "" {
		cfg.Baggage.Propagation = BaggageOff
	}
	return cfg
}

// ---- Exporter lifetime ------------------------------------------------------
//
// The OTel exporter is the only piece that has lifecycle state (HTTP client,
// batch span processor goroutine). We hold it behind a mutex and reset it
// whenever SetConfig changes the OTLP endpoint or protocol.

type exporterRef struct {
	mu       sync.Mutex
	shutdown func(timeout time.Duration) error
}

var sharedExporter = &exporterRef{}

func (e *exporterRef) reset(shutdown func(timeout time.Duration) error) {
	if e == nil {
		return
	}
	e.mu.Lock()
	prev := e.shutdown
	e.shutdown = shutdown
	e.mu.Unlock()
	if prev != nil {
		_ = prev(5 * time.Second)
	}
}

func (e *exporterRef) close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	prev := e.shutdown
	e.shutdown = nil
	e.mu.Unlock()
	if prev != nil {
		_ = prev(5 * time.Second)
	}
}

// Shutdown flushes pending spans and releases the exporter. Wire this to the
// proxy runtime's shutdown signal handler so deployments lose no telemetry on
// SIGTERM/SIGINT.
func Shutdown() { sharedExporter.close() }
