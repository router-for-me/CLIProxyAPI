package claudemaster

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// An address for a collector may carry credentials (user information, a token in the query). Only its host
// may reach the log.
func TestTheCollectorAddressIsNeverLoggedOnlyItsHost(t *testing.T) {
	logs := captureLogs(t, "debug")
	startMetricsAt(t, "https://exporter:hunter2-PASSWORD@collector.internal:4318/base?token=QUERY-SECRET")
	out := logs.String()
	if !strings.Contains(out, "collector.internal:4318") {
		t.Fatalf("the host should be logged:\n%s", out)
	}
	for _, secret := range []string{"hunter2", "PASSWORD", "exporter", "QUERY-SECRET", "token=", "/base"} {
		if strings.Contains(out, secret) {
			t.Fatalf("%q reached the log:\n%s", secret, out)
		}
	}
	if endpointHost("::not a url::") != "invalid" || endpointHost("") != "invalid" {
		t.Fatal("an unparsable address must log as invalid, never as itself")
	}
}

func startMetricsAt(t *testing.T, endpoint string) {
	t.Helper()
	stop, err := StartTelemetry(TelemetryOptions{Endpoint: endpoint, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop(t.Context()) })
}

// Both the backend and the proxy are state sources. The proxy owns no sessions, so it must not report a zero
// that can win against the backend's real count.
func TestSessionsAreReportedOnlyByASourceThatOwnsThem(t *testing.T) {
	reader := startMetrics(t, nil)
	backend := &fixedSource{snap: stateSnapshot{Sessions: 7, HasSessions: true}}
	registerStateSource(backend)
	t.Cleanup(func() { unregisterStateSource(backend) })
	// Iteration order over the sources is arbitrary, and the last value observed wins. Many session-less
	// sources (the proxy's shape) make it certain that some are visited after the backend.
	for i := 0; i < 64; i++ {
		proxy := &fixedSource{snap: stateSnapshot{ActiveConns: 2, HasActiveConns: true}}
		registerStateSource(proxy)
		t.Cleanup(func() { unregisterStateSource(proxy) })
	}
	for i := 0; i < 20; i++ { // map order is random: it must hold on every pass
		rm := collect(t, reader)
		if v, ok := gaugeOf(rm, "claude_master.sessions.tracked", nil); !ok || v != 7 {
			t.Fatalf("pass %d: sessions.tracked = %v (%v), want 7", i, v, ok)
		}
	}
}

// A reset time is kept as a moment, and the countdown is worked out when the gauge is read: a profile that
// is idle must still count down.
func TestAResetCountsDownBetweenRequests(t *testing.T) {
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := start
	restore := limitNow
	limitNow = func() time.Time { return now }
	t.Cleanup(func() { limitNow = restore })
	store := newLimitGaugeStore()
	store.setReset("claude-connor", "7d", "resets_in_seconds", start.Add(100*time.Second))
	read := func() float64 {
		var got float64 = -1
		store.each(func(_, _, _ string, v float64) { got = v })
		return got
	}
	for _, step := range []struct {
		after time.Duration
		want  float64
	}{{0, 100}, {40 * time.Second, 60}, {100 * time.Second, 0}, {500 * time.Second, 0}} {
		now = start.Add(step.after)
		if got := read(); got != step.want {
			t.Errorf("after %s the countdown reads %v, want %v", step.after, got, step.want)
		}
	}
}

// Remote Control and the like go straight to the client's own account; they still belong in the total.
func TestControlRequestsAreCountedInTheProxyTotal(t *testing.T) {
	reader := startMetrics(t, nil)
	_, client, _ := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{}`)
	}), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		return proxyTestResponse(200, `{}`), nil
	}))
	proxyTestRequest(t, client, http.MethodPost, "/v1/code/sessions", `{}`, http.Header{})
	rm := collect(t, reader)
	if got := sumOf(rm, "claude_master.requests", map[string]string{"route": "control"}); got != 1 {
		t.Fatalf("requests{route=control} = %d, want 1", got)
	}
	if got := sumOf(rm, "claude_master.inference.requests", nil); got != 0 {
		t.Fatalf("a control request was counted as inference (%d)", got)
	}
}
