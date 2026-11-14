package home

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClusterLatencyProbeCadenceKeepsEvidenceFresh(t *testing.T) {
	for _, test := range []struct {
		name   string
		jitter []time.Duration
	}{
		{name: "regular ticks", jitter: []time.Duration{0}},
		{name: "progressively early ticks", jitter: []time.Duration{0, -time.Nanosecond, -2 * time.Nanosecond, -3 * time.Nanosecond}},
		{name: "alternating jitter", jitter: []time.Duration{200 * time.Nanosecond, -100 * time.Nanosecond, 50 * time.Nanosecond, -200 * time.Nanosecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			testClusterLatencyProbeCadence(t, test.jitter)
		})
	}
}

func testClusterLatencyProbeCadence(t *testing.T, jitter []time.Duration) {
	t.Helper()
	history := &clusterLatencyHistory{}
	node := clusterNode{IP: "candidate", Port: 8327}
	address := clusterNodeAddress(node)
	start := time.Unix(4000, 0)
	var uploadedExpiry time.Time
	var previousObservedAt time.Time
	for cycle := range 8 {
		reportAt := start.Add(time.Duration(cycle)*time.Minute + jitter[cycle%len(jitter)])
		// Home receives the previous probe on the next report, not when the probe completes.
		uploadAt := reportAt.Add(100 * time.Millisecond)
		samples := history.report(reportAt)
		if cycle == 0 {
			if len(samples) != 0 {
				t.Fatal("first report contained an unmeasured candidate")
			}
		} else {
			if len(samples) != 1 || !samples[0].Valid || samples[0].Sequence != int64(cycle) {
				t.Fatalf("cycle %d: missing fresh candidate evidence: %+v", cycle, samples)
			}
			sample := samples[0]
			if !sample.ObservedAt.Equal(previousObservedAt) || sample.AgeMS != reportAt.Sub(previousObservedAt).Milliseconds() {
				t.Fatalf("cycle %d: report did not preserve probe age: %+v", cycle, sample)
			}
			if !uploadedExpiry.IsZero() && !uploadAt.Before(uploadedExpiry) {
				t.Fatalf("cycle %d: Home evidence expired at %v before upload at %v", cycle, uploadedExpiry, uploadAt)
			}
			// Home uses the older of observed_at and receipt time minus age_ms.
			sampledAt := uploadAt.Add(-time.Duration(sample.AgeMS) * time.Millisecond)
			if sample.ObservedAt.Before(sampledAt) {
				sampledAt = sample.ObservedAt
			}
			uploadedExpiry = sampledAt.Add(clusterLatencyTTL)
		}
		if !history.probeDue(node, reportAt) {
			t.Fatalf("cycle %d: ticker jitter or probe duration skipped a report cycle", cycle)
		}
		// Vary report and probe latency without sleeping or relying on platform timers.
		reportDelay := time.Duration(1+cycle%3) * 100 * time.Millisecond
		probeDuration := time.Duration(1+cycle%2) * time.Second
		completedAt := reportAt.Add(reportDelay + probeDuration)
		history.observeProbe(node, 20*time.Millisecond, true, reportAt, completedAt)
		entry := history.entries[address]
		if entry.nextProbeCycle != int64(cycle+1) || !entry.nextProbeAt.IsZero() || entry.probeBackoff != 0 {
			t.Fatalf("cycle %d: incorrect successful probe schedule: %+v", cycle, entry)
		}
		if history.probeDue(node, completedAt) {
			t.Fatalf("cycle %d: another probe is allowed within the same report cycle", cycle)
		}
		previousObservedAt = completedAt
	}
}

func TestClusterLatencyProbeCyclesSurviveSkippedReports(t *testing.T) {
	history := &clusterLatencyHistory{}
	node := clusterNode{IP: "candidate", Port: 8327}
	start := time.Unix(4500, 0)
	history.observeProbe(node, time.Millisecond, true, start, start.Add(time.Second))
	// Dropped ticks advance to their absolute cycle instead of shifting the baseline.
	reportAt := start.Add(5*time.Minute - time.Nanosecond)
	if !history.probeDue(node, reportAt) {
		t.Fatal("skipped reports prevented a new probe")
	}
	history.observeProbe(node, time.Millisecond, true, reportAt, reportAt.Add(time.Second))
	entry := history.entries[clusterNodeAddress(node)]
	if entry.nextProbeCycle != 6 || !history.probeBaseline.Equal(start) {
		t.Fatalf("skipped reports shifted the cycle baseline: %+v", entry)
	}
	if history.probeDue(node, start.Add(5*time.Minute+time.Nanosecond)) {
		t.Fatal("the same cycle allowed another probe after a slightly early tick")
	}
	if !history.probeDue(node, start.Add(6*time.Minute-2*time.Nanosecond)) {
		t.Fatal("an early next tick skipped a probe after dropped reports")
	}
}

func TestClusterLatencyProbeFailureScheduleAndRecovery(t *testing.T) {
	history := &clusterLatencyHistory{}
	node := clusterNode{IP: "candidate", Port: 8327}
	address := clusterNodeAddress(node)
	reportAt := time.Unix(5000, 0)
	for _, backoff := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		completedAt := reportAt.Add(clusterLatencyTimeout)
		history.observeProbe(node, 0, false, reportAt, completedAt)
		entry := history.entries[address]
		if entry.sample.Valid || entry.probeBackoff != backoff || !entry.nextProbeAt.Equal(completedAt.Add(backoff)) {
			t.Fatalf("failure backoff changed: %+v, want %v after completion", entry, backoff)
		}
		if history.probeDue(node, entry.nextProbeAt.Add(-time.Nanosecond)) || !history.probeDue(node, entry.nextProbeAt) {
			t.Fatal("report cycle rounding bypassed or extended failure backoff")
		}
		reportAt = entry.nextProbeAt
	}
	history.observeProbe(node, time.Millisecond, true, reportAt, reportAt.Add(time.Second))
	entry := history.entries[address]
	if !entry.sample.Valid || entry.probeBackoff != 0 || !entry.nextProbeAt.IsZero() || history.probeDue(node, reportAt) || !history.probeDue(node, reportAt.Add(time.Minute-time.Nanosecond)) {
		t.Fatalf("successful probe did not restore report cadence: %+v", entry)
	}
}

func TestClusterLatencyReportsRespectScheduledProbeBudget(t *testing.T) {
	target, targetCommands := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "PING") && len(args) == 2 {
			return fmt.Sprintf("$%d\r\n%s\r\n", len(args[1]), args[1])
		}
		return balanceTestRedisReply(args)
	})
	node := clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port}
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "CLUSTER") {
			raw := fmt.Sprintf(`{"ok":true,"probe":{"ip":%q,"port":%d}}`, node.IP, node.Port)
			return fmt.Sprintf("$%d\r\n%s\r\n", len(raw), raw)
		}
		return balanceTestRedisReply(args)
	})
	enableBalanceTestClient(client)
	client.balance.latencySupported = true
	client.clusterNodes = []clusterNode{node}
	history := &clusterLatencyHistory{}
	// A slightly early next tick is a new cycle; repeated ticks in that cycle must not probe twice.
	start := time.Now().Add(-3 * time.Minute)
	for _, reportAt := range []time.Time{start, start.Add(time.Minute - time.Nanosecond), start.Add(time.Minute), start.Add(2*time.Minute - 2*time.Nanosecond)} {
		if errReport := client.reportClusterLatency(context.Background(), history, reportAt); errReport != nil {
			t.Fatal(errReport)
		}
	}
	pings := 0
	for _, args := range targetCommands.All() {
		if strings.EqualFold(args[0], "PING") {
			pings++
		}
	}
	if pings != 6 {
		t.Fatalf("PING count = %d, want exactly three probes (warmup plus measured PING)", pings)
	}
	var sequences []int64
	for _, args := range commands.All() {
		if len(args) != 4 || args[2] != "LATENCY" {
			continue
		}
		var report struct {
			Samples []clusterLatencySample `json:"samples"`
		}
		if errDecode := json.Unmarshal([]byte(args[3]), &report); errDecode != nil {
			t.Fatal(errDecode)
		}
		if len(report.Samples) == 0 {
			sequences = append(sequences, 0)
		} else {
			sequences = append(sequences, report.Samples[0].Sequence)
		}
	}
	if len(sequences) != 4 || sequences[0] != 0 || sequences[1] != 1 || sequences[2] != 2 || sequences[3] != 2 || history.entries[clusterNodeAddress(node)].sample.Sequence != 3 {
		t.Fatalf("reports did not upload the preceding probe before probing again: %v", sequences)
	}
}
