package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func newUpstreamTimingTestReporter(t *testing.T) *UsageReporter {
	t.Helper()
	reporter := NewUsageReporter(context.Background(), "test-provider", "test-model", nil)
	if reporter == nil {
		t.Fatal("NewUsageReporter returned nil")
	}
	return reporter
}

func upstreamTimingTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(server.Close)
	return server
}

func drainAndClose(t *testing.T, resp *http.Response) {
	t.Helper()
	if _, errRead := io.Copy(io.Discard, resp.Body); errRead != nil {
		t.Fatalf("read upstream body: %v", errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("close upstream body: %v", errClose)
	}
}

func TestRoundTripRecordsUpstreamTiming(t *testing.T) {
	server := upstreamTimingTestServer(t)
	reporter := newUpstreamTimingTestReporter(t)
	client := reporter.TrackHTTPClient(server.Client())

	resp, errGet := client.Get(server.URL)
	if errGet != nil {
		t.Fatalf("get upstream: %v", errGet)
	}
	drainAndClose(t, resp)

	record := reporter.buildRecord(usage.Detail{}, false)
	if record.UpstreamTTFB <= 0 {
		t.Fatalf("upstream ttfb = %v, want > 0", record.UpstreamTTFB)
	}
	if record.FirstPacket < record.UpstreamTTFB {
		t.Fatalf("first packet %v is smaller than upstream ttfb %v", record.FirstPacket, record.UpstreamTTFB)
	}
	if record.ConnReused {
		t.Fatal("conn reused = true, want false for the first request")
	}
	if record.ConnSetup < 0 {
		t.Fatalf("conn setup = %v, want >= 0", record.ConnSetup)
	}
	if record.TTFT <= 0 {
		t.Fatalf("ttft = %v, want > 0", record.TTFT)
	}
}

func TestRoundTripRecordsConnReuse(t *testing.T) {
	server := upstreamTimingTestServer(t)
	client := server.Client()

	firstReporter := newUpstreamTimingTestReporter(t)
	firstResp, errFirst := firstReporter.TrackHTTPClient(client).Get(server.URL)
	if errFirst != nil {
		t.Fatalf("first get: %v", errFirst)
	}
	drainAndClose(t, firstResp)

	secondReporter := newUpstreamTimingTestReporter(t)
	secondResp, errSecond := secondReporter.TrackHTTPClient(client).Get(server.URL)
	if errSecond != nil {
		t.Fatalf("second get: %v", errSecond)
	}
	drainAndClose(t, secondResp)

	record := secondReporter.buildRecord(usage.Detail{}, false)
	if !record.ConnReused {
		t.Fatal("conn reused = false, want true for the pooled second request")
	}
	if record.ConnSetup != 0 {
		t.Fatalf("conn setup = %v, want 0 when the connection is reused", record.ConnSetup)
	}
}

func TestObserveUpstreamAttemptFirstWins(t *testing.T) {
	reporter := newUpstreamTimingTestReporter(t)
	reporter.ObserveUpstreamAttempt(upstreamAttemptTiming{upstreamTTFB: 100, connSetup: 20, connReused: true})
	reporter.ObserveUpstreamAttempt(upstreamAttemptTiming{upstreamTTFB: 999, connSetup: 77, connReused: false})

	record := reporter.buildRecord(usage.Detail{}, false)
	if record.UpstreamTTFB != 100 {
		t.Fatalf("upstream ttfb = %v, want first observed 100", record.UpstreamTTFB)
	}
	if record.ConnSetup != 20 {
		t.Fatalf("conn setup = %v, want first observed 20", record.ConnSetup)
	}
	if !record.ConnReused {
		t.Fatal("conn reused = false, want first observed true")
	}
}

func TestMarkFirstResponseByteRecordsFirstPacket(t *testing.T) {
	server := upstreamTimingTestServer(t)
	reporter := newUpstreamTimingTestReporter(t)
	client := reporter.TrackHTTPClient(server.Client())

	resp, errGet := client.Get(server.URL)
	if errGet != nil {
		t.Fatalf("get upstream: %v", errGet)
	}
	drainAndClose(t, resp)

	record := reporter.buildRecord(usage.Detail{}, false)
	if record.FirstPacket != record.TTFT {
		t.Fatalf("first packet = %v, ttft = %v, want equal on the tracked http client path", record.FirstPacket, record.TTFT)
	}
}
