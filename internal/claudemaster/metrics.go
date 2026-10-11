package claudemaster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// OpenTelemetry metrics, exported over OTLP/HTTP to whatever collects them (a CloudWatch agent, an OTel
// Collector). Nothing here is specific to any cloud: claude-master only speaks OTLP.
//
// DIMENSIONS (attributes) are kept low-cardinality and never identifying:
//   profile         the subscription profile's own name (claude-connor), or api-backup
//   client          the connecting box: its certificate name, "tunnel" (the open listener) or "local"
//   client_account  the INCOMING user's Anthropic account: your label for it (--account-label), else
//                   acct-<8 hex> derived from a hash. The account id itself is never emitted.
//   model, status_class (2xx..5xx), status, route, stream, reason, result, window, measure
//
// WHAT IS NEVER EMITTED: tokens, bodies, URLs, account ids, upstream error text.

// ---------------------------------------------------------------- options and lifecycle

// TelemetryOptions configures metric export.
type TelemetryOptions struct {
	Endpoint      string        // OTLP/HTTP base URL, e.g. http://127.0.0.1:4318; empty disables export
	Interval      time.Duration // export interval (default 30 s)
	Instance      string        // service.instance.id; empty adds none (the command line defaults it to the hostname)
	AccountLabels map[string]string
	Reader        sdkmetric.Reader // tests supply a manual reader instead of the OTLP exporter
}

const defaultMetricInterval = 30 * time.Second

var (
	latencyBucketsMS = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000}
	sizeBuckets      = []float64{256, 1024, 4096, 16384, 65536, 262144, 1 << 20, 4 << 20, 16 << 20}
)

type instruments struct {
	connections       metric.Int64Counter
	handshakeErrors   metric.Int64Counter
	requests          metric.Int64Counter
	requestsByClient  metric.Int64Counter
	inference         metric.Int64Counter
	inferenceByModel  metric.Int64Counter
	inferenceByClient metric.Int64Counter
	inferenceErrors   metric.Int64Counter
	tokens            metric.Int64Counter
	tokensByAccount   metric.Int64Counter
	tokensByClient    metric.Int64Counter
	tokensByProject   metric.Int64Counter
	picks             metric.Int64Counter
	switches          metric.Int64Counter
	backup            metric.Int64Counter
	rateLimited       metric.Int64Counter
	refresh           metric.Int64Counter
	usagePolls        metric.Int64Counter
	limitState        metric.Int64Counter
	duration          metric.Float64Histogram
	ttfb              metric.Float64Histogram
	durationByModel   metric.Float64Histogram
	ttfbByModel       metric.Float64Histogram
	upstreamTTFB      metric.Float64Histogram
	overhead          metric.Float64Histogram
	pickDuration      metric.Float64Histogram
	requestBytes      metric.Int64Histogram
	responseBytes     metric.Int64Histogram
	registration      metric.Registration
	quantiles         *quantileSet
	limitGauges       *limitGaugeStore
	quotaUsed         metric.Float64ObservableGauge
	quotaResets       metric.Float64ObservableGauge
	quotaBlocked      metric.Float64ObservableGauge
	fiveHourUsed      metric.Float64ObservableGauge
	fiveHourResets    metric.Float64ObservableGauge
	tokenExpires      metric.Float64ObservableGauge
	quotaBand         metric.Int64ObservableGauge
	sessions          metric.Int64ObservableGauge
	activeConns       metric.Int64ObservableGauge
	upstreamLimit     metric.Float64ObservableGauge
	latencyQuantile   metric.Float64ObservableGauge
	uptime            metric.Float64ObservableGauge
	goroutines        metric.Int64ObservableGauge
	heap              metric.Int64ObservableGauge
	startedAt         time.Time
	accountLabelsMap  atomic.Pointer[map[string]string] // swapped whole by SetAccountLabels
}

var activeInstruments atomic.Pointer[instruments]

var noopInstruments = buildInstruments(noop.NewMeterProvider().Meter("claude-master"), nil)

func tm() *instruments {
	if i := activeInstruments.Load(); i != nil {
		return i
	}
	return noopInstruments
}

// StartTelemetry starts metric export and returns a function that flushes and stops it. With no endpoint
// and no reader it does nothing and returns a no-op.
func StartTelemetry(opts TelemetryOptions) (func(context.Context) error, error) {
	reader := opts.Reader
	if reader == nil {
		if opts.Endpoint == "" {
			return func(context.Context) error { return nil }, nil
		}
		interval := opts.Interval
		if interval <= 0 {
			interval = defaultMetricInterval
		}
		exporter, err := otlpmetrichttp.New(context.Background(),
			otlpmetrichttp.WithEndpointURL(strings.TrimRight(opts.Endpoint, "/")+"/v1/metrics"))
		if err != nil {
			return nil, errors.New("cannot create the OTLP metric exporter")
		}
		reader = sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(interval))
	}
	// Resource attributes can become dimensions downstream; the instance id keeps several servers' series
	// apart and is added only when there is one.
	resAttrs := []attribute.KeyValue{attribute.String("service.name", "claude-master")}
	if opts.Instance != "" {
		resAttrs = append(resAttrs, attribute.String("service.instance.id", opts.Instance))
	}
	res, err := resource.Merge(resource.Empty(), resource.NewSchemaless(resAttrs...))
	if err != nil {
		return nil, err
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
	labels := normalizeAccountLabels(opts.AccountLabels)
	inst := buildInstruments(provider.Meter("claude-master"), labels)
	activeInstruments.Store(inst)
	lg().Info("telemetry started", "endpoint_host", endpointHost(opts.Endpoint), "account_labels", len(labels))
	return func(ctx context.Context) error {
		activeInstruments.Store(nil)
		if inst.registration != nil {
			_ = inst.registration.Unregister()
		}
		return provider.Shutdown(ctx)
	}, nil
}

func buildInstruments(m metric.Meter, labels map[string]string) *instruments {
	i := &instruments{quantiles: newQuantileSet(512), limitGauges: newLimitGaugeStore(), startedAt: time.Now()}
	i.accountLabelsMap.Store(&labels)
	counter := func(name, desc string) metric.Int64Counter {
		c, _ := m.Int64Counter(name, metric.WithDescription(desc))
		return c
	}
	hist := func(name, unit, desc string) metric.Float64Histogram {
		h, _ := m.Float64Histogram(name, metric.WithUnit(unit), metric.WithDescription(desc), metric.WithExplicitBucketBoundaries(latencyBucketsMS...))
		return h
	}
	i.connections = counter("claude_master.proxy.connections", "Tunnel connections by listener and result")
	i.handshakeErrors = counter("claude_master.proxy.tls_handshake_errors", "Client TLS handshakes that failed")
	i.requests = counter("claude_master.requests", "Requests through the proxy by route and client account")
	i.requestsByClient = counter("claude_master.requests.by_client", "Requests through the proxy by route and client (the connecting box)")
	i.inference = counter("claude_master.inference.requests", "Inference requests by profile, client account and status class")
	i.inferenceByModel = counter("claude_master.inference.requests.by_model", "Inference requests by model and status class")
	i.inferenceByClient = counter("claude_master.inference.requests.by_client", "Inference requests by client (the connecting box) and client account")
	i.inferenceErrors = counter("claude_master.inference.errors", "Inference requests that ended in an error status, by profile, status and client account")
	i.tokens = counter("claude_master.inference.tokens", "Tokens by profile and type (input, output, cache_read, cache_creation), from the responses' usage")
	i.tokensByAccount = counter("claude_master.inference.tokens.by_client_account", "Tokens by client account and type")
	i.tokensByClient = counter("claude_master.inference.tokens.by_client", "Tokens by client (the connecting box) and type")
	i.tokensByProject = counter("claude_master.inference.tokens.by_project", "Tokens by project (the launch directory's repository name) and type")
	i.picks = counter("claude_master.routing.picks", "Routing decisions by chosen profile")
	i.switches = counter("claude_master.routing.switches", "Conversations moved to another account, by from, to and reason")
	i.backup = counter("claude_master.routing.backup_requests", "Requests served by the paid API-key backup")
	i.rateLimited = counter("claude_master.quota.rate_limited", "Times a profile was rate limited by Anthropic")
	i.refresh = counter("claude_master.auth.refresh", "Login refreshes by profile and result")
	i.usagePolls = counter("claude_master.usage.polls", "Subscription usage polls by profile and result")
	i.limitState = counter("claude_master.anthropic.ratelimit.state", "Anthropic rate-limit status words seen, by profile, window and value")
	i.duration = hist("claude_master.inference.duration", "ms", "Whole inference request, received to last byte")
	i.durationByModel = hist("claude_master.inference.duration.by_model", "ms", "Whole inference request by model")
	i.ttfb = hist("claude_master.inference.ttfb", "ms", "Request received to first response byte")
	i.ttfbByModel = hist("claude_master.inference.ttfb.by_model", "ms", "Request received to first response byte, by model")
	i.upstreamTTFB = hist("claude_master.inference.upstream_ttfb", "ms", "Account chosen to first response byte: Anthropic's own time to first byte")
	i.overhead = hist("claude_master.proxy.overhead", "ms", "Request received to account chosen: claude-master's own added time")
	i.pickDuration = hist("claude_master.routing.pick_duration", "ms", "Time the routing decision took")
	i.requestBytes, _ = m.Int64Histogram("claude_master.inference.request_bytes", metric.WithUnit("By"), metric.WithExplicitBucketBoundaries(sizeBuckets...))
	i.responseBytes, _ = m.Int64Histogram("claude_master.inference.response_bytes", metric.WithUnit("By"), metric.WithExplicitBucketBoundaries(sizeBuckets...))

	i.quotaUsed, _ = m.Float64ObservableGauge("claude_master.quota.used_fraction", metric.WithDescription("Weekly subscription allowance used, 0 to 1"))
	i.quotaResets, _ = m.Float64ObservableGauge("claude_master.quota.resets_in_seconds", metric.WithUnit("s"))
	i.quotaBlocked, _ = m.Float64ObservableGauge("claude_master.quota.rate_limited_for_seconds", metric.WithUnit("s"))
	i.fiveHourUsed, _ = m.Float64ObservableGauge("claude_master.quota.five_hour.used_fraction", metric.WithDescription("Five-hour subscription window used, 0 to 1, from the usage poll; 0 once the window has reset"))
	i.fiveHourResets, _ = m.Float64ObservableGauge("claude_master.quota.five_hour.resets_in_seconds", metric.WithUnit("s"), metric.WithDescription("Until the five-hour window resets; absent while no window is open"))
	i.quotaBand, _ = m.Int64ObservableGauge("claude_master.quota.band", metric.WithDescription("0 ok, 1 reserve (last tenth), 2 exhausted, -1 unknown"))
	i.tokenExpires, _ = m.Float64ObservableGauge("claude_master.auth.token_expires_in_seconds", metric.WithUnit("s"))
	i.sessions, _ = m.Int64ObservableGauge("claude_master.sessions.tracked")
	i.activeConns, _ = m.Int64ObservableGauge("claude_master.proxy.active_connections")
	i.upstreamLimit, _ = m.Float64ObservableGauge("claude_master.anthropic.ratelimit", metric.WithDescription("Anthropic rate-limit headers: utilization, remaining, limit, resets_in_seconds"))
	i.latencyQuantile, _ = m.Float64ObservableGauge("claude_master.inference.duration_quantile", metric.WithUnit("ms"), metric.WithDescription("Recent inference duration percentiles per profile (so CloudWatch can chart p50/p95/p99)"))
	i.uptime, _ = m.Float64ObservableGauge("claude_master.process.uptime_seconds", metric.WithUnit("s"))
	i.goroutines, _ = m.Int64ObservableGauge("claude_master.process.goroutines")
	i.heap, _ = m.Int64ObservableGauge("claude_master.process.heap_bytes", metric.WithUnit("By"))

	i.registration, _ = m.RegisterCallback(i.observe,
		i.quotaUsed, i.quotaResets, i.quotaBlocked, i.fiveHourUsed, i.fiveHourResets, i.quotaBand, i.tokenExpires, i.sessions, i.activeConns,
		i.upstreamLimit, i.latencyQuantile, i.uptime, i.goroutines, i.heap)
	return i
}

// ---------------------------------------------------------------- state sources

// profileState is one profile's live state, read at collection time.
type profileState struct {
	Name            string
	Used            float64
	UsedKnown       bool
	ResetsInSeconds float64
	ResetsKnown     bool
	BlockedSeconds  float64
	Band            int64
	TokenExpiresIn  float64
	TokenKnown      bool
	// The five-hour window from the usage poll.
	FiveHourUsed        float64
	FiveHourKnown       bool
	FiveHourResetsIn    float64
	FiveHourResetsKnown bool
}

type stateSnapshot struct {
	Profiles       []profileState
	Sessions       int64
	HasSessions    bool
	ActiveConns    int64
	HasActiveConns bool
}

type stateSource interface{ metricsState() stateSnapshot }

var stateSources sync.Map // stateSource -> struct{}

func registerStateSource(s stateSource)   { stateSources.Store(s, struct{}{}) }
func unregisterStateSource(s stateSource) { stateSources.Delete(s) }

func (i *instruments) observe(_ context.Context, o metric.Observer) error {
	stateSources.Range(func(key, _ any) bool {
		snap := key.(stateSource).metricsState()
		for _, p := range snap.Profiles {
			attrs := metric.WithAttributes(attribute.String("profile", p.Name))
			if p.UsedKnown {
				o.ObserveFloat64(i.quotaUsed, p.Used, attrs)
			}
			if p.ResetsKnown {
				o.ObserveFloat64(i.quotaResets, p.ResetsInSeconds, attrs)
			}
			o.ObserveFloat64(i.quotaBlocked, p.BlockedSeconds, attrs)
			if p.FiveHourKnown {
				o.ObserveFloat64(i.fiveHourUsed, p.FiveHourUsed, attrs)
			}
			if p.FiveHourResetsKnown {
				o.ObserveFloat64(i.fiveHourResets, p.FiveHourResetsIn, attrs)
			}
			o.ObserveInt64(i.quotaBand, p.Band, attrs)
			if p.TokenKnown {
				o.ObserveFloat64(i.tokenExpires, p.TokenExpiresIn, attrs)
			}
		}
		if snap.HasSessions {
			o.ObserveInt64(i.sessions, snap.Sessions)
		}
		if snap.HasActiveConns {
			o.ObserveInt64(i.activeConns, snap.ActiveConns)
		}
		return true
	})
	i.limitGauges.each(func(profile, window, measure string, v float64) {
		o.ObserveFloat64(i.upstreamLimit, v, metric.WithAttributes(
			attribute.String("profile", profile), attribute.String("window", window), attribute.String("measure", measure)))
	})
	i.quantiles.each(func(profile string, q float64, ms float64) {
		o.ObserveFloat64(i.latencyQuantile, ms, metric.WithAttributes(
			attribute.String("profile", profile), attribute.String("quantile", strconv.FormatFloat(q, 'f', -1, 64))))
	})
	o.ObserveFloat64(i.uptime, time.Since(i.startedAt).Seconds())
	o.ObserveInt64(i.goroutines, int64(runtime.NumGoroutine()))
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	o.ObserveInt64(i.heap, int64(mem.HeapAlloc))
	return nil
}

// ---------------------------------------------------------------- the incoming user's account

var (
	legacyUserID = regexp.MustCompile(`account_([0-9A-Fa-f-]{8,})`)
)

// clientAccountKey names the INCOMING user's Anthropic account for the dashboards, without ever emitting
// the account id. Claude Code puts it in metadata.user_id of each inference request, as JSON
// ({"account_uuid": ...}) or the older user_<hash>_account_<uuid>_session_<uuid> string. The key is your
// label for it (--account-label UUID=NAME) or acct-<8 hex of a hash>; "unknown" when the request carries
// none (an API-key client, an old Claude Code).
func clientAccountKey(raw []byte) string {
	uid := gjson.GetBytes(raw, "metadata.user_id").String()
	if uid == "" {
		return "unknown"
	}
	id := ""
	if strings.HasPrefix(strings.TrimSpace(uid), "{") {
		id = gjson.Get(uid, "account_uuid").String()
	} else if m := legacyUserID.FindStringSubmatch(uid); m != nil {
		id = m[1]
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return "unknown"
	}
	if labels := tm().accountLabelsMap.Load(); labels != nil {
		if label, ok := (*labels)[id]; ok && label != "" {
			return sanitizeDimension(label) // bounded by the labels file
		}
	}
	sum := sha256.Sum256([]byte(id))
	return accounts.admit("acct-"+hex.EncodeToString(sum[:4]), "other")
}

func normalizeAccountLabels(in map[string]string) map[string]string {
	labels := make(map[string]string, len(in))
	for id, label := range in {
		labels[strings.ToLower(strings.TrimSpace(id))] = strings.TrimSpace(label)
	}
	return labels
}

// SetAccountLabels replaces the running telemetry's account labels (ACCOUNT_UUID -> NAME) in one swap, so
// a changed labels file takes effect without a restart. It returns false when telemetry is not running.
func SetAccountLabels(labels map[string]string) bool {
	inst := activeInstruments.Load()
	if inst == nil {
		return false
	}
	normalized := normalizeAccountLabels(labels)
	inst.accountLabelsMap.Store(&normalized)
	return true
}

// AccountKeyFor is what the dashboards will call an account id: use it to build --account-label values
// ("which acct-xxxxxxxx is whose?").
func AccountKeyFor(accountID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(accountID))))
	return "acct-" + hex.EncodeToString(sum[:4])
}

var dimensionUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeDimension(s string) string {
	s = dimensionUnsafe.ReplaceAllString(strings.TrimSpace(s), "-")
	if len(s) > 48 {
		s = s[:48]
	}
	if s == "" {
		return "unknown"
	}
	return s
}

// ---------------------------------------------------------------- recording

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "none"
	}
	return strconv.Itoa(status/100) + "xx"
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// requestObservation is one finished inference request.
type requestObservation struct {
	Route      string // inference or count_tokens
	Profile    string
	Model      string
	Client     string
	Project    string // the project the client's tunnel named (projectHeader), or ""
	Account    string
	Stream     bool
	Status     int
	Duration   time.Duration
	TTFB       time.Duration // zero when nothing was written
	Overhead   time.Duration // zero when no account was chosen
	ReqBytes   int64
	RespBytes  int64
	RateLimits http.Header // the upstream response headers
	Tokens     tokenCounts // the usage the response reported, when HasTokens
	HasTokens  bool
}

// observeTokens counts one request's tokens. Like the request metrics, each axis (profile, user
// account, client, project) is its own projection with the token type.
func observeTokens(profile, client, account, project string, counts tokenCounts) {
	projectAttr := attribute.String("project", projectMetricLabel(project))
	t := tm()
	ctx := context.Background()
	for _, kind := range []struct {
		name  string
		count int64
	}{
		{"input", counts.input},
		{"output", counts.output},
		{"cache_read", counts.cacheRead},
		{"cache_creation", counts.cacheCreation},
	} {
		if kind.count <= 0 {
			continue
		}
		typ := attribute.String("type", kind.name)
		t.tokens.Add(ctx, kind.count, metric.WithAttributes(attribute.String("profile", profile), typ))
		t.tokensByAccount.Add(ctx, kind.count, metric.WithAttributes(attribute.String("client_account", account), typ))
		t.tokensByClient.Add(ctx, kind.count, metric.WithAttributes(attribute.String("client", client), typ))
		t.tokensByProject.Add(ctx, kind.count, metric.WithAttributes(projectAttr, typ))
	}
}

func observeRequest(o requestObservation) {
	t := tm()
	ctx := context.Background()
	if o.Profile == "" {
		o.Profile = "none"
	}
	// COST SHAPE. CloudWatch makes every distinct combination of a metric's attributes its own billable custom
	// metric, so the attributes are NOT crossed: each metric carries at most three, and each axis (profile,
	// account, client, model) gets its own projection. The series count is then a sum of small numbers, not
	// their product. TestNoMetricCrossesTheAxes keeps it that way.
	route := attribute.String("route", o.Route)
	account := attribute.String("client_account", o.Account)
	client := attribute.String("client", o.Client)
	t.requests.Add(ctx, 1, metric.WithAttributes(route, account))
	t.requestsByClient.Add(ctx, 1, metric.WithAttributes(route, client))
	if o.Route != "inference" {
		return
	}
	profile := attribute.String("profile", o.Profile)
	class := attribute.String("status_class", statusClass(o.Status))
	model := attribute.String("model", o.Model)
	t.inference.Add(ctx, 1, metric.WithAttributes(profile, account, class))
	t.inferenceByModel.Add(ctx, 1, metric.WithAttributes(model, class))
	t.inferenceByClient.Add(ctx, 1, metric.WithAttributes(client, account))
	if o.HasTokens {
		observeTokens(o.Profile, o.Client, o.Account, o.Project, o.Tokens)
	}
	if o.Status >= 400 || o.Status == 0 {
		t.inferenceErrors.Add(ctx, 1, metric.WithAttributes(profile, attribute.String("status", strconv.Itoa(o.Status)), account))
	}
	shape := metric.WithAttributes(profile, class)
	byModel := metric.WithAttributes(model)
	t.duration.Record(ctx, ms(o.Duration), shape)
	t.durationByModel.Record(ctx, ms(o.Duration), byModel)
	t.quantiles.add(o.Profile, ms(o.Duration))
	if o.TTFB > 0 {
		t.ttfb.Record(ctx, ms(o.TTFB), shape)
		t.ttfbByModel.Record(ctx, ms(o.TTFB), byModel)
		if o.Overhead > 0 && o.TTFB > o.Overhead {
			t.upstreamTTFB.Record(ctx, ms(o.TTFB-o.Overhead), shape)
		}
	}
	if o.Overhead > 0 {
		t.overhead.Record(ctx, ms(o.Overhead), metric.WithAttributes(attribute.String("profile", o.Profile)))
	}
	t.requestBytes.Record(ctx, o.ReqBytes, metric.WithAttributes(attribute.String("profile", o.Profile)))
	t.responseBytes.Record(ctx, o.RespBytes, metric.WithAttributes(attribute.String("profile", o.Profile)))
	if o.RateLimits != nil {
		t.recordRateLimitHeaders(o.Profile, o.RateLimits)
	}
	lg().Debug("request finished", "client", o.Client, "client_account", o.Account, "profile", o.Profile, "model", o.Model,
		"stream", o.Stream, "status", o.Status, "duration_ms", ms(o.Duration), "ttfb_ms", ms(o.TTFB), "overhead_ms", ms(o.Overhead),
		"request_bytes", o.ReqBytes, "response_bytes", o.RespBytes)
}

func observePick(profile string, took time.Duration, backup bool) {
	t := tm()
	t.picks.Add(context.Background(), 1, metric.WithAttributes(attribute.String("profile", profile)))
	t.pickDuration.Record(context.Background(), ms(took))
	if backup {
		t.backup.Add(context.Background(), 1)
	}
}

func observeSwitch(from, to, reason string) {
	tm().switches.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("from", from), attribute.String("to", to), attribute.String("reason", reason)))
}

func observeRateLimited(profile string) {
	tm().rateLimited.Add(context.Background(), 1, metric.WithAttributes(attribute.String("profile", profile)))
}

func observeRefresh(profile, result string) {
	tm().refresh.Add(context.Background(), 1, metric.WithAttributes(attribute.String("profile", profile), attribute.String("result", result)))
}

func observeUsagePoll(profile, result string) {
	tm().usagePolls.Add(context.Background(), 1, metric.WithAttributes(attribute.String("profile", profile), attribute.String("result", result)))
}

func observeConnection(listener, result string) {
	tm().connections.Add(context.Background(), 1, metric.WithAttributes(attribute.String("listener", listener), attribute.String("result", result)))
}

func observeHandshakeError() {
	tm().handshakeErrors.Add(context.Background(), 1)
}

// ---------------------------------------------------------------- Anthropic's own rate-limit headers

// limitNow is the clock the reset countdowns are read against; tests move it.
var limitNow = time.Now

type limitKey struct{ profile, window, measure string }

type limitGaugeStore struct {
	mu     sync.Mutex
	values map[limitKey]float64
	resets map[limitKey]time.Time // absolute: the countdown is computed when the gauge is read
}

func newLimitGaugeStore() *limitGaugeStore {
	return &limitGaugeStore{values: make(map[limitKey]float64), resets: make(map[limitKey]time.Time)}
}

func (s *limitGaugeStore) setReset(profile, window, measure string, at time.Time) {
	s.mu.Lock()
	s.resets[limitKey{profile, window, measure}] = at
	s.mu.Unlock()
}

func (s *limitGaugeStore) set(profile, window, measure string, v float64) {
	s.mu.Lock()
	s.values[limitKey{profile, window, measure}] = v
	s.mu.Unlock()
}

func (s *limitGaugeStore) each(fn func(profile, window, measure string, v float64)) {
	s.mu.Lock()
	snapshot := make(map[limitKey]float64, len(s.values)+len(s.resets))
	for k, v := range s.values {
		snapshot[k] = v
	}
	now := limitNow()
	for k, at := range s.resets {
		snapshot[k] = max(at.Sub(now).Seconds(), 0)
	}
	s.mu.Unlock()
	for k, v := range snapshot {
		fn(k.profile, k.window, k.measure, v)
	}
}

// A window is a duration (5h, 7d) with optional qualifiers joined by underscores (7d_oi).
var windowToken = regexp.MustCompile(`^\d+[smhdw](_[a-z0-9]+)*$`)
var wordValue = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)

// recordRateLimitHeaders keeps the latest value of every Anthropic-Ratelimit-* header per profile:
// numbers as a gauge (window = 5h, 7d ... or api; measure = utilization, remaining, limit,
// resets_in_seconds ...), words (allowed, allowed_warning, rejected ...) as a counter. The account
// utilization and reset times are exactly the "account quotas" the dashboards want.
func (i *instruments) recordRateLimitHeaders(profile string, headers http.Header) {
	for name, values := range headers {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "anthropic-ratelimit-") || len(values) == 0 {
			continue
		}
		window, measure := splitLimitHeader(strings.TrimPrefix(lower, "anthropic-ratelimit-"))
		raw := strings.TrimSpace(values[0])
		if strings.HasSuffix(measure, "reset") {
			if t, ok := parseQuotaTime(raw); ok {
				i.limitGauges.setReset(profile, window, measure+"s_in_seconds", t)
			}
			continue
		}
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			i.limitGauges.set(profile, window, measure, v)
			continue
		}
		if wordValue.MatchString(strings.ToLower(raw)) {
			i.limitState.Add(context.Background(), 1, metric.WithAttributes(
				attribute.String("profile", profile), attribute.String("window", window),
				attribute.String("measure", measure), attribute.String("value", strings.ToLower(raw))))
		}
	}
}

// splitLimitHeader separates "unified-5h-utilization" into window 5h and measure utilization, and the
// classic "requests-remaining" into window api and measure requests_remaining.
func splitLimitHeader(rest string) (window, measure string) {
	parts := strings.Split(rest, "-")
	if len(parts) > 0 && parts[0] == "unified" {
		parts = parts[1:]
		window = "all"
		if len(parts) > 0 && windowToken.MatchString(parts[0]) {
			window, parts = parts[0], parts[1:]
		}
		if len(parts) == 0 {
			return window, "status"
		}
		return window, strings.Join(parts, "_")
	}
	return "api", strings.Join(parts, "_")
}

// ---------------------------------------------------------------- recent-latency percentiles

// quantileSet keeps the last N durations per profile and reports p50, p95 and p99 at collection time.
type quantileSet struct {
	mu   sync.Mutex
	n    int
	ring map[string]*ringBuffer
}

type ringBuffer struct {
	values []float64
	next   int
	full   bool
}

func newQuantileSet(n int) *quantileSet {
	return &quantileSet{n: n, ring: make(map[string]*ringBuffer)}
}

func (q *quantileSet) add(profile string, v float64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	r := q.ring[profile]
	if r == nil {
		r = &ringBuffer{values: make([]float64, q.n)}
		q.ring[profile] = r
	}
	r.values[r.next] = v
	r.next = (r.next + 1) % q.n
	if r.next == 0 {
		r.full = true
	}
}

func (q *quantileSet) each(fn func(profile string, quantile, value float64)) {
	q.mu.Lock()
	type sample struct {
		profile string
		values  []float64
	}
	var samples []sample
	for profile, r := range q.ring {
		count := r.next
		if r.full {
			count = q.n
		}
		if count == 0 {
			continue
		}
		samples = append(samples, sample{profile, append([]float64(nil), r.values[:count]...)})
	}
	q.mu.Unlock()
	for _, s := range samples {
		sort.Float64s(s.values)
		for _, quantile := range []float64{0.5, 0.95, 0.99} {
			index := int(quantile*float64(len(s.values)-1) + 0.5)
			fn(s.profile, quantile, s.values[index])
		}
	}
}

// endpointHost is all of a collector address that is ever logged: an address may carry credentials in its
// user information or query, and those must never reach a log.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	return u.Host
}

// ---------------------------------------------------------------- untrusted values

// A model name and a user's account come from the CLIENT's request body, and a client can send anything:
// a secret, an email, a different value every time. Neither is ever used as written. A model must look like a
// model name; and only a bounded number of distinct models and unlabelled accounts are ever kept, so the
// number of series is bounded whatever a client sends. The rest is "other".
const (
	maxDistinctModels   = 64
	maxDistinctAccounts = 256
)

// Anthropic model names all contain "claude" (claude-opus-5, anthropic.claude-3-5-sonnet, us.anthropic.claude-...).
var modelNamePattern = regexp.MustCompile(`^(?:[a-z]{2}\.)?(?:anthropic\.)?claude[a-z0-9._:-]{0,56}$`)

type boundedValues struct {
	mu   sync.Mutex
	seen map[string]struct{}
	max  int
}

func newBoundedValues(max int) *boundedValues {
	return &boundedValues{seen: make(map[string]struct{}), max: max}
}

// admit returns v if it is already known or there is still room for it, otherwise overflow.
func (b *boundedValues) admit(v, overflow string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[v]; ok {
		return v
	}
	if len(b.seen) >= b.max {
		return overflow
	}
	b.seen[v] = struct{}{}
	return v
}

var (
	models   = newBoundedValues(maxDistinctModels)
	accounts = newBoundedValues(maxDistinctAccounts)
)

// modelLabel is the only form of a request's model that metrics and logs ever see.
func modelLabel(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "unknown"
	}
	if !modelNamePattern.MatchString(raw) {
		return "other"
	}
	return models.admit(raw, "other")
}
