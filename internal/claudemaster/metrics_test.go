package claudemaster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	testAccountUUID = "11111111-2222-3333-4444-555555555555"
	testSSE         = ": native heartbeat\r\nevent: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"
)

func startMetrics(t *testing.T, labels map[string]string) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	stop, err := StartTelemetry(TelemetryOptions{Reader: reader, Instance: "test", AccountLabels: labels})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	return reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	return rm
}

func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, scope := range rm.ScopeMetrics {
		for i := range scope.Metrics {
			if scope.Metrics[i].Name == name {
				return &scope.Metrics[i]
			}
		}
	}
	return nil
}

// attrsMatch reports whether a data point carries every wanted attribute.
func attrsMatch(point map[string]string, want map[string]string) bool {
	for k, v := range want {
		if point[k] != v {
			return false
		}
	}
	return true
}

// attributesOf flattens a data point's attributes into strings for comparison.
func attributesOf(set attribute.Set) map[string]string {
	out := make(map[string]string, set.Len())
	for iter := set.Iter(); iter.Next(); {
		kv := iter.Attribute()
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

// sumOf adds up an integer counter's points that match.
func sumOf(rm metricdata.ResourceMetrics, name string, want map[string]string) int64 {
	m := findMetric(rm, name)
	if m == nil {
		return 0
	}
	data, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0
	}
	var total int64
	for _, p := range data.DataPoints {
		if attrsMatch(attributesOf(p.Attributes), want) {
			total += p.Value
		}
	}
	return total
}

// gaugeOf returns an observable gauge's value for the matching point.
func gaugeOf(rm metricdata.ResourceMetrics, name string, want map[string]string) (float64, bool) {
	m := findMetric(rm, name)
	if m == nil {
		return 0, false
	}
	switch data := m.Data.(type) {
	case metricdata.Gauge[float64]:
		for _, p := range data.DataPoints {
			if attrsMatch(attributesOf(p.Attributes), want) {
				return p.Value, true
			}
		}
	case metricdata.Gauge[int64]:
		for _, p := range data.DataPoints {
			if attrsMatch(attributesOf(p.Attributes), want) {
				return float64(p.Value), true
			}
		}
	}
	return 0, false
}

// histCount is how many observations a histogram holds for the matching point.
func histCount(rm metricdata.ResourceMetrics, name string, want map[string]string) uint64 {
	m := findMetric(rm, name)
	if m == nil {
		return 0
	}
	var total uint64
	switch data := m.Data.(type) {
	case metricdata.Histogram[float64]:
		for _, p := range data.DataPoints {
			if attrsMatch(attributesOf(p.Attributes), want) {
				total += p.Count
			}
		}
	case metricdata.Histogram[int64]:
		for _, p := range data.DataPoints {
			if attrsMatch(attributesOf(p.Attributes), want) {
				total += p.Count
			}
		}
	}
	return total
}

// upstream is a fake Anthropic: configurable status and headers, a small delay, an SSE body.
type upstream struct {
	status  int
	headers http.Header
	delay   time.Duration
	calls   atomic.Int32
}

func (u *upstream) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return u }

func (u *upstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.calls.Add(1)
	time.Sleep(u.delay)
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	header := http.Header{"Content-Type": {"text/event-stream"}, "Request-Id": {"req_test"}}
	for k, v := range u.headers {
		header[k] = v
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(testSSE)), Request: r}, nil
}

func inferenceRequest(account string, stream bool) *http.Request {
	userID := fmt.Sprintf(`{"device_id":"dev","account_uuid":%q,"session_id":"sess"}`, account)
	body := fmt.Sprintf(`{"model":"claude-opus-5","stream":%t,"metadata":{"user_id":%q},"messages":[{"role":"user","content":"hello, my email is ejc3@example.com"}]}`, stream, userID)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "RAW-SESSION-ID-do-not-export")
	return req.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(req.Context(), req.Header))
}

func serve(t *testing.T, backend *Backend, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	backend.Handler().ServeHTTP(recorder, req)
	return recorder
}

func TestAnInferenceRequestIsMeasuredByProfileAccountAndTiming(t *testing.T) {
	reader := startMetrics(t, map[string]string{testAccountUUID: "colton"})
	backend := newBackendNativeResponseFixture(t)
	backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-connor"}
	reset := strconv.FormatInt(time.Now().Add(36*time.Hour).Unix(), 10)
	fake := &upstream{delay: 30 * time.Millisecond, headers: http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.31"}, "Anthropic-Ratelimit-Unified-7d-Utilization": {"0.62"},
		"Anthropic-Ratelimit-Unified-7d-Reset": {reset}, "Anthropic-Ratelimit-Unified-Status": {"allowed_warning"},
		"Anthropic-Ratelimit-Requests-Remaining": {"4990"},
	}}
	backend.manager.SetRoundTripperProvider(fake)
	if rec := serve(t, backend, inferenceRequest(testAccountUUID, true)); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	rm := collect(t, reader)
	// One projection per axis, never their product (see TestNoMetricCrossesTheAxes).
	for name, shape := range map[string]map[string]string{
		"claude_master.inference.requests":           {"profile": "claude-connor", "status_class": "2xx", "client_account": "colton"},
		"claude_master.inference.requests.by_model":  {"model": "claude-opus-5", "status_class": "2xx"},
		"claude_master.inference.requests.by_client": {"client": "unknown", "client_account": "colton"},
		"claude_master.requests.by_client":           {"route": "inference", "client": "unknown"},
	} {
		if got := sumOf(rm, name, shape); got != 1 {
			t.Fatalf("%s = %d for %v", name, got, shape)
		}
	}
	for _, name := range []string{"claude_master.inference.duration.by_model", "claude_master.inference.ttfb.by_model"} {
		if got := histCount(rm, name, map[string]string{"model": "claude-opus-5"}); got != 1 {
			t.Errorf("%s has %d observations for the model, want 1", name, got)
		}
	}
	if got := sumOf(rm, "claude_master.requests", map[string]string{"route": "inference", "client_account": "colton"}); got != 1 {
		t.Fatalf("requests = %d", got)
	}
	for _, name := range []string{"claude_master.inference.duration", "claude_master.inference.ttfb", "claude_master.inference.upstream_ttfb"} {
		if got := histCount(rm, name, map[string]string{"profile": "claude-connor", "status_class": "2xx"}); got != 1 {
			t.Errorf("%s has %d observations, want 1", name, got)
		}
	}
	if histCount(rm, "claude_master.proxy.overhead", map[string]string{"profile": "claude-connor"}) != 1 {
		t.Error("claude-master's own overhead was not measured")
	}
	if histCount(rm, "claude_master.inference.request_bytes", map[string]string{"profile": "claude-connor"}) != 1 {
		t.Error("request size was not measured")
	}
	// Anthropic's own account and rate-limit state, by window.
	for _, tc := range []struct {
		window, measure string
		want            float64
	}{{"5h", "utilization", 0.31}, {"7d", "utilization", 0.62}, {"api", "requests_remaining", 4990}} {
		got, ok := gaugeOf(rm, "claude_master.anthropic.ratelimit", map[string]string{"profile": "claude-connor", "window": tc.window, "measure": tc.measure})
		if !ok || got != tc.want {
			t.Errorf("ratelimit %s %s = %v (%v), want %v", tc.window, tc.measure, got, ok, tc.want)
		}
	}
	if resets, ok := gaugeOf(rm, "claude_master.anthropic.ratelimit", map[string]string{"window": "7d", "measure": "resets_in_seconds"}); !ok || resets < 35*3600 || resets > 36*3600+5 {
		t.Errorf("the weekly reset is %v seconds away (%v), want about 36 hours", resets, ok)
	}
	if got := sumOf(rm, "claude_master.anthropic.ratelimit.state", map[string]string{"profile": "claude-connor", "window": "all", "measure": "status", "value": "allowed_warning"}); got != 1 {
		t.Errorf("the rate-limit status word was not counted (%d)", got)
	}
	for _, q := range []string{"0.5", "0.95", "0.99"} {
		if v, ok := gaugeOf(rm, "claude_master.inference.duration_quantile", map[string]string{"profile": "claude-connor", "quantile": q}); !ok || v < 25 {
			t.Errorf("p%s latency = %v (%v), want at least the 30 ms the fake upstream took", q, v, ok)
		}
	}
}

func TestAnErrorFromAnthropicIsCountedByStatus(t *testing.T) {
	reader := startMetrics(t, nil)
	backend := newBackendNativeResponseFixture(t)
	backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-ejc3"}
	backend.manager.SetRoundTripperProvider(&upstream{status: 529})
	_ = serve(t, backend, inferenceRequest(testAccountUUID, true))
	rm := collect(t, reader)
	if got := sumOf(rm, "claude_master.inference.errors", map[string]string{"profile": "claude-ejc3", "status": "529"}); got != 1 {
		t.Fatalf("inference.errors{status=529} = %d", got)
	}
	if got := sumOf(rm, "claude_master.inference.requests", map[string]string{"status_class": "5xx"}); got != 1 {
		t.Fatalf("a 529 was not classed 5xx (%d)", got)
	}
}

func TestTheIncomingAccountIsAKeyNeverTheId(t *testing.T) {
	startMetrics(t, map[string]string{testAccountUUID: "Colton's Laptop!"})
	legacy := `{"metadata":{"user_id":"user_abc123_account_` + testAccountUUID + `_session_9f8e7d"}}`
	modern := `{"metadata":{"user_id":"{\"device_id\":\"d\",\"account_uuid\":\"` + testAccountUUID + `\",\"session_id\":\"s\"}"}}`
	for name, body := range map[string]string{"legacy": legacy, "modern": modern} {
		if got := clientAccountKey([]byte(body)); got != "Colton-s-Laptop-" {
			t.Errorf("%s user_id gave %q, want the sanitised label", name, got)
		}
	}
	other := `{"metadata":{"user_id":"user_x_account_99999999-8888-7777-6666-555555555555_session_z"}}`
	key := clientAccountKey([]byte(other))
	if !strings.HasPrefix(key, "acct-") || len(key) != 13 || strings.Contains(key, "9999") {
		t.Errorf("an unlabelled account gave %q, want acct- and 8 hex of a hash", key)
	}
	if key != AccountKeyFor("99999999-8888-7777-6666-555555555555") {
		t.Error("account-key does not agree with what the dashboards use")
	}
	for _, body := range []string{`{}`, `{"metadata":{}}`, `{"metadata":{"user_id":"user_x_account__session_y"}}`, `not json`, ``} {
		if got := clientAccountKey([]byte(body)); got != "unknown" {
			t.Errorf("%q gave %q, want unknown", body, got)
		}
	}
}

func TestRoutingEventsBecomeMetrics(t *testing.T) {
	reader := startMetrics(t, nil)
	selector, auths, reset := namedSelector(t)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.5, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(24 * time.Hour)})
	requireBackendSeriesPickWith(t, selector, plainRequest("m-1"), auths, "profile-a")
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.95, resetsAt: reset})
	requireBackendSeriesPickWith(t, selector, plainRequest("m-1"), auths, "profile-b")
	selector.OnResult(backendSeriesQuotaResult("profile-b"))
	rm := collect(t, reader)
	if sumOf(rm, "claude_master.routing.picks", map[string]string{"profile": "claude-connor"}) != 1 ||
		sumOf(rm, "claude_master.routing.picks", map[string]string{"profile": "claude-ejc3"}) != 1 {
		t.Error("picks were not counted per profile")
	}
	if got := sumOf(rm, "claude_master.routing.switches", map[string]string{"from": "claude-connor", "to": "claude-ejc3", "reason": "reserve_reached"}); got != 1 {
		t.Errorf("the switch was counted %d times", got)
	}
	if sumOf(rm, "claude_master.quota.rate_limited", map[string]string{"profile": "claude-ejc3"}) != 1 {
		t.Error("the rate limit was not counted")
	}
	if histCount(rm, "claude_master.routing.pick_duration", nil) != 2 {
		t.Error("pick durations were not recorded")
	}
}

type fixedSource struct{ snap stateSnapshot }

func (f *fixedSource) metricsState() stateSnapshot { return f.snap }

func TestQuotaGaugesShowTheLiveStateOfEveryProfile(t *testing.T) {
	reader := startMetrics(t, nil)
	selector, _, reset := namedSelector(t)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.93, resetsAt: reset})
	selector.OnResult(backendSeriesQuotaResult("profile-b"))
	src := &fixedSource{snap: selector.metricsSnapshot(nil)} // a pointer, like the real sources
	registerStateSource(src)
	t.Cleanup(func() { unregisterStateSource(src) })
	rm := collect(t, reader)
	a := map[string]string{"profile": "claude-connor"}
	if v, ok := gaugeOf(rm, "claude_master.quota.used_fraction", a); !ok || v != 0.93 {
		t.Errorf("used_fraction = %v (%v)", v, ok)
	}
	if v, _ := gaugeOf(rm, "claude_master.quota.band", a); v != 1 {
		t.Errorf("band = %v, want 1 (reserve)", v)
	}
	if v, ok := gaugeOf(rm, "claude_master.quota.resets_in_seconds", a); !ok || v != 3600 {
		t.Errorf("resets_in_seconds = %v (%v), want 3600", v, ok)
	}
	b := map[string]string{"profile": "claude-ejc3"}
	if v, _ := gaugeOf(rm, "claude_master.quota.rate_limited_for_seconds", b); v <= 0 {
		t.Errorf("a rate-limited profile shows %v seconds of cooldown", v)
	}
	if v, _ := gaugeOf(rm, "claude_master.quota.band", b); v != -1 {
		t.Errorf("an unmeasured profile's band = %v, want -1 (unknown)", v)
	}
	for _, name := range []string{"claude_master.process.uptime_seconds", "claude_master.process.goroutines", "claude_master.process.heap_bytes"} {
		if findMetric(rm, name) == nil {
			t.Errorf("%s is missing", name)
		}
	}
}

func TestSplittingAnthropicRateLimitHeaders(t *testing.T) {
	for header, want := range map[string][2]string{
		"unified-5h-utilization":       {"5h", "utilization"},
		"unified-7d-reset":             {"7d", "reset"},
		"unified-status":               {"all", "status"},
		"unified-representative-claim": {"all", "representative_claim"},
		"unified-overage-status":       {"all", "overage_status"},
		"requests-remaining":           {"api", "requests_remaining"},
		"input-tokens-limit":           {"api", "input_tokens_limit"},
	} {
		if window, measure := splitLimitHeader(header); window != want[0] || measure != want[1] {
			t.Errorf("%s -> %s/%s, want %s/%s", header, window, measure, want[0], want[1])
		}
	}
}

// Everything that is exported is scanned: no id, token, address or text of a conversation may be in it.
func TestNothingSecretOrIdentifyingIsEverExported(t *testing.T) {
	reader := startMetrics(t, nil)
	backend := newBackendNativeResponseFixture(t)
	backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-connor"}
	backend.manager.SetRoundTripperProvider(&upstream{headers: http.Header{
		"Anthropic-Ratelimit-Unified-Status": {"allowed"}, "Anthropic-Ratelimit-Unified-5h-Utilization": {"0.1"},
		"Authorization": {"Bearer abcdefghijklmnop"}, "Set-Cookie": {"session=SECRETCOOKIE"},
	}})
	_ = serve(t, backend, inferenceRequest(testAccountUUID, true))
	rm := collect(t, reader)
	encoded, err := json.Marshal(rm)
	if err != nil {
		t.Fatal(err)
	}
	out := string(encoded)
	for _, secret := range []string{testAccountUUID, "RAW-SESSION-ID", "ejc3@example.com", "my email", "abcdefghijklmnop", "SECRETCOOKIE", "sk-ant-", "hello"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%q was exported:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "acct-") {
		t.Fatalf("the hashed account key is not there:\n%s", out)
	}
}

func TestMetricsReallyLeaveOverOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []int
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, len(data))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	stop, err := StartTelemetry(TelemetryOptions{Endpoint: collector.URL, Interval: 50 * time.Millisecond, Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	observeSwitch("claude-connor", "claude-ejc3", "reserve_reached")
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := len(paths)
		mu.Unlock()
		if got > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("nothing reached the OTLP collector")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths[0] != "/v1/metrics" || bodies[0] == 0 {
		t.Fatalf("the export went to %q with %d bytes, want /v1/metrics with a body", paths[0], bodies[0])
	}
}

func TestTelemetryOffCostsNothingAndNeverFails(t *testing.T) {
	stop, err := StartTelemetry(TelemetryOptions{})
	if err != nil || stop(context.Background()) != nil {
		t.Fatalf("an empty configuration must be a quiet no-op: %v", err)
	}
	// Recording with nothing started is safe: it goes to no-op instruments.
	observeRequest(requestObservation{Route: "inference", Profile: "p", Status: 200, Duration: time.Millisecond})
	observeSwitch("a", "b", "c")
}

func TestTheInstanceIsOnTheResourceOnlyWhenSet(t *testing.T) {
	for instance, want := range map[string]bool{"example-host": true, "": false} {
		reader := sdkmetric.NewManualReader()
		stop, err := StartTelemetry(TelemetryOptions{Reader: reader, Instance: instance})
		if err != nil {
			t.Fatal(err)
		}
		rm := collect(t, reader)
		_ = stop(context.Background())
		got, ok := rm.Resource.Set().Value("service.instance.id")
		if ok != want || (want && got.AsString() != instance) {
			t.Errorf("Instance %q: service.instance.id = %q (%v)", instance, got.AsString(), ok)
		}
		if name, _ := rm.Resource.Set().Value("service.name"); name.AsString() != "claude-master" {
			t.Errorf("service.name = %q", name.AsString())
		}
	}
}
