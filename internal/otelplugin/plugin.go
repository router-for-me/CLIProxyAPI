package otelplugin

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Tracer name for spans this package emits. Operators querying by tracer get
// only proxy-generated spans, not anything traced inside the OTel SDK itself.
const tracerName = "github.com/router-for-me/CLIProxyAPI/internal/otelplugin"

// init() registers the OTLP plugin with the usage manager. Registration is
// always performed; the plugin's HandleUsage is a no-op while Enabled() is
// false (default until SetConfig flips it on).
func init() {
	coreusage.RegisterPlugin(NewPlugin())
}

// NewPlugin returns a usage.Plugin that emits one OTel span per upstream
// LLM call. Safe to construct directly in tests.
func NewPlugin() *Plugin { return &Plugin{} }

// Plugin implements coreusage.Plugin. Stateless on the receiver — all mutable
// state lives in package-level atomics (config, tracer provider) so multiple
// plugin instances are safe.
type Plugin struct{}

// HandleUsage implements coreusage.Plugin. Emits a single span describing the
// completed upstream call, with attribute namespaces:
//
//   - gen_ai.*           OpenTelemetry GenAI semantic conventions (model,
//                        token counts, system identifier).
//   - cost.*             local convention; cost.usd plus per-component
//                        breakdown when CostConfig.Enabled is true.
//   - agent.* / workload.* per-request baggage keys forwarded from the
//                        request context (set by the baggage middleware).
//
// Lock-free fast path: a single atomic load gates the call entirely when the
// exporter is off. When on, span construction is contention-free per call.
func (p *Plugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || !Enabled() {
		return
	}
	tracer := tracerFor(loadConfig())
	if tracer == nil {
		return
	}
	cfg := loadConfig()
	startTime := startTimeFor(record)
	_, span := tracer.Start(ctx, cfg.OTLP.Span.Name,
		trace.WithTimestamp(startTime),
		trace.WithSpanKind(trace.SpanKindClient),
	)
	span.SetAttributes(usageAttributes(record, cfg)...)
	span.SetAttributes(baggageAttributes(BaggageFromContext(ctx), cfg)...)
	if cfg.Cost.Enabled {
		span.SetAttributes(costAttributes(record, cfg)...)
	}
	if record.Failed {
		span.SetStatus(codeError, failureMessage(record.Fail))
	} else {
		span.SetStatus(codeOk, "")
	}
	endTime := startTime.Add(record.Latency)
	if record.Latency <= 0 {
		endTime = time.Now()
	}
	span.End(trace.WithTimestamp(endTime))
}

// ---- tracer wiring ----------------------------------------------------------

var (
	tracerMu     sync.Mutex
	tracerActive trace.Tracer
	tracerEndpt  string
	tracerProto  string
)

// tracerFor lazily constructs the global tracer + exporter on first use. The
// guard mutex serialises construction; subsequent reads are lock-free because
// trace.Tracer is itself a goroutine-safe handle.
func tracerFor(cfg *Config) trace.Tracer {
	tracerMu.Lock()
	defer tracerMu.Unlock()
	if tracerActive != nil &&
		tracerEndpt == cfg.OTLP.Endpoint &&
		tracerProto == cfg.OTLP.Protocol {
		return tracerActive
	}
	tracer, shutdown, err := buildTracer(cfg)
	if err != nil {
		return nil
	}
	sharedExporter.reset(shutdown)
	tracerActive = tracer
	tracerEndpt = cfg.OTLP.Endpoint
	tracerProto = cfg.OTLP.Protocol
	return tracerActive
}

func buildTracer(cfg *Config) (trace.Tracer, func(time.Duration) error, error) {
	exporter, err := newOtlpExporter(cfg)
	if err != nil {
		return nil, nil, err
	}
	processor := sdktrace.NewBatchSpanProcessor(exporter,
		sdktrace.WithMaxQueueSize(2048),
		sdktrace.WithMaxExportBatchSize(128),
		sdktrace.WithBatchTimeout(5*time.Second),
	)
	res, err := resourceFor(cfg)
	if err != nil {
		_ = processor.Shutdown(context.Background())
		return nil, nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(processor),
	)
	// Replace the global provider so the rest of the proxy (if it adopts
	// otel.GetTracerProvider() anywhere) sees the same configured emitter.
	otel.SetTracerProvider(provider)
	tracer := provider.Tracer(tracerName)
	shutdown := func(timeout time.Duration) error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return provider.Shutdown(shutdownCtx)
	}
	return tracer, shutdown, nil
}

func newOtlpExporter(cfg *Config) (sdktrace.SpanExporter, error) {
	// HTTP/protobuf is the only protocol wired in the initial cut. gRPC support
	// is one extra import + parallel switch; deferred until an operator asks.
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.OTLP.Endpoint),
	}
	if len(cfg.OTLP.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.OTLP.Headers))
	}
	return otlptracehttp.New(context.Background(), opts...)
}

func resourceFor(cfg *Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName(cfg)),
	}
	if ns := strings.TrimSpace(cfg.OTLP.Service.Namespace); ns != "" {
		attrs = append(attrs, semconv.ServiceNamespace(ns))
	}
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, attrs...),
	)
}

func serviceName(cfg *Config) string {
	name := strings.TrimSpace(cfg.OTLP.Service.Name)
	if name == "" {
		name = "cli-proxy-api"
	}
	return name
}

// ---- attribute construction -------------------------------------------------

func usageAttributes(record coreusage.Record, cfg *Config) []attribute.KeyValue {
	out := []attribute.KeyValue{
		attribute.String("gen_ai.system", record.Provider),
		attribute.String("gen_ai.request.model", record.Model),
	}
	if alias := strings.TrimSpace(record.Alias); alias != "" {
		out = append(out, attribute.String("gen_ai.request.model_alias", alias))
	}
	if effort := strings.TrimSpace(record.ReasoningEffort); effort != "" {
		out = append(out, attribute.String("gen_ai.request.reasoning_effort", effort))
	}
	includeUsage := cfg.OTLP.Span.IncludeUsage == nil || *cfg.OTLP.Span.IncludeUsage
	if includeUsage {
		out = append(out,
			attribute.Int64("gen_ai.usage.input_tokens", record.Detail.InputTokens),
			attribute.Int64("gen_ai.usage.output_tokens", record.Detail.OutputTokens),
		)
		if record.Detail.CacheReadTokens > 0 {
			out = append(out, attribute.Int64("gen_ai.usage.cache_read_input_tokens", record.Detail.CacheReadTokens))
		}
		if record.Detail.CacheCreationTokens > 0 {
			out = append(out, attribute.Int64("gen_ai.usage.cache_creation_input_tokens", record.Detail.CacheCreationTokens))
		}
		if record.Detail.ReasoningTokens > 0 {
			out = append(out, attribute.Int64("gen_ai.usage.reasoning_tokens", record.Detail.ReasoningTokens))
		}
		if record.Detail.TotalTokens > 0 {
			out = append(out, attribute.Int64("gen_ai.usage.total_tokens", record.Detail.TotalTokens))
		}
	}
	if record.Failed {
		out = append(out, attribute.Int("gen_ai.response.status_code", record.Fail.StatusCode))
	}
	if record.Source != "" {
		out = append(out, attribute.String("gen_ai.request.source", record.Source))
	}
	return out
}

func baggageAttributes(b Baggage, cfg *Config) []attribute.KeyValue {
	if len(b) == 0 {
		return nil
	}
	allowed := cfg.OTLP.Span.IncludeBaggageKeys
	if len(allowed) == 0 {
		out := make([]attribute.KeyValue, 0, len(b))
		for k, v := range b {
			out = append(out, attribute.String(k, v))
		}
		return out
	}
	out := make([]attribute.KeyValue, 0, len(allowed))
	for _, k := range allowed {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if v, ok := b[k]; ok {
			out = append(out, attribute.String(k, v))
		}
	}
	return out
}

func costAttributes(record coreusage.Record, cfg *Config) []attribute.KeyValue {
	pricing, ok := cfg.Cost.Pricing[record.Model]
	if !ok {
		// Try a prefix match — operators frequently use one entry per model
		// family (e.g. "claude-sonnet") rather than every variant suffix.
		for model, p := range cfg.Cost.Pricing {
			if strings.HasPrefix(record.Model, model) {
				pricing = p
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil
	}
	const million = 1_000_000.0
	input := float64(record.Detail.InputTokens) * pricing.InputPerMillion / million
	output := float64(record.Detail.OutputTokens) * pricing.OutputPerMillion / million
	cacheRead := float64(record.Detail.CacheReadTokens) * pricing.CacheReadPerMillion / million
	cacheCreation := float64(record.Detail.CacheCreationTokens) * pricing.CacheCreationPerMillion / million
	total := input + output + cacheRead + cacheCreation
	includeCost := cfg.OTLP.Span.IncludeCost == nil || *cfg.OTLP.Span.IncludeCost
	if !includeCost {
		return nil
	}
	return []attribute.KeyValue{
		attribute.Float64("cost.usd", total),
		attribute.Float64("cost.input_usd", input),
		attribute.Float64("cost.output_usd", output),
		attribute.Float64("cost.cache_read_usd", cacheRead),
		attribute.Float64("cost.cache_creation_usd", cacheCreation),
	}
}

// ---- helpers ----------------------------------------------------------------

func startTimeFor(record coreusage.Record) time.Time {
	if !record.RequestedAt.IsZero() {
		return record.RequestedAt
	}
	if record.Latency > 0 {
		return time.Now().Add(-record.Latency)
	}
	return time.Now()
}

func failureMessage(f coreusage.Failure) string {
	if f.Body == "" {
		return ""
	}
	// Cap body length so we never ship an unbounded payload into a span. The
	// span has the status code already; the body is a hint for humans only.
	if len(f.Body) > 512 {
		return f.Body[:512]
	}
	return f.Body
}
