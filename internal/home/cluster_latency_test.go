package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestClusterLatencySamplerMatchesOnlyItsSubscription(t *testing.T) {
	now := time.Unix(1000, 0)
	history := &clusterLatencyHistory{}
	node := clusterNode{IP: "home-a", Port: 8327}
	sampler := &clusterRTTSampler{history: history, node: node}
	token := sampler.begin(now)
	if sampler.pong("", now.Add(time.Millisecond)) || sampler.pong("other", now.Add(time.Millisecond)) {
		t.Fatal("heartbeat or unrelated PONG became RTT evidence")
	}
	if !sampler.pong(token, now.Add(10*time.Millisecond)) {
		t.Fatal("numbered PONG not matched")
	}
	if sampler.pong(token, now.Add(time.Second)) {
		t.Fatal("duplicate PONG matched twice")
	}
	// Reporting happens much later than reception; consumer queue time must not enter RTT.
	samples := history.report(now.Add(time.Minute))
	if len(samples) != 1 || samples[0].RTTUS != 10000 || samples[0].AgeMS != 59990 || !samples[0].Valid {
		t.Fatalf("unexpected RTT report: %+v", samples)
	}
	token = sampler.begin(now.Add(time.Minute))
	sampler.expire(now.Add(time.Minute+time.Second), true)
	next := &clusterRTTSampler{history: history, node: node}
	if next.pong(token, now.Add(time.Minute+2*time.Second)) {
		t.Fatal("new lifetime matched an old subscription PONG")
	}
	if samples = history.report(now.Add(time.Minute + time.Second)); len(samples) != 1 || samples[0].Valid {
		t.Fatal("disconnect did not invalidate the unanswered sample")
	}
}

func TestClusterLatencyFreshnessSmoothingAndBounds(t *testing.T) {
	history := &clusterLatencyHistory{}
	now := time.Unix(2000, 0)
	node := clusterNode{IP: "home", Port: 8327}
	history.observe(node, 100*time.Millisecond, true, now)
	history.observe(node, 20*time.Millisecond, true, now.Add(time.Second))
	if samples := history.report(now.Add(time.Second)); len(samples) != 1 || samples[0].RTTUS != 80000 || samples[0].Sequence != 2 {
		t.Fatalf("EWMA or sequence incorrect: %+v", samples)
	}
	if samples := history.report(now.Add(clusterLatencyTTL + 2*time.Second)); len(samples) != 0 {
		t.Fatal("stale RTT was reported")
	}
	history.observe(node, 10*time.Millisecond, true, now.Add(3*time.Minute))
	if samples := history.report(now.Add(3 * time.Minute)); len(samples) != 1 || samples[0].RTTUS != 10000 {
		t.Fatal("stale EWMA contaminated a new sample")
	}
	for index := range 100 {
		history.observe(clusterNode{IP: fmt.Sprintf("home-%d", index), Port: 8327}, time.Millisecond, true, now.Add(time.Duration(index)*time.Second))
	}
	if len(history.entries) != 64 {
		t.Fatalf("history unbounded: %d", len(history.entries))
	}
	for range 100 {
		delay := clusterLatencySampleDelay()
		if delay < 27*time.Second || delay >= 33*time.Second {
			t.Fatalf("sample jitter out of bounds: %v", delay)
		}
	}
}

func TestClusterLatencyTimeoutAndNewLifetime(t *testing.T) {
	client := New(config.HomeConfig{})
	client.latency = &clusterLatencyHistory{}
	client.balance.latencySupported = true
	sampler := &clusterRTTSampler{history: client.latency, node: clusterNode{IP: "home", Port: 8327}}
	now := time.Unix(3000, 0)
	token := sampler.begin(now)
	if !sampler.pong(token, now.Add(clusterLatencyTimeout+time.Millisecond)) {
		t.Fatal("late matching PONG not consumed")
	}
	if samples := client.latency.report(now.Add(clusterLatencyTimeout + time.Millisecond)); len(samples) != 1 || samples[0].Valid {
		t.Fatal("late sample was accepted as low RTT")
	}
	next := client.NewLifetime()
	if next.latency != client.latency || next.balance.latencySupported {
		t.Fatal("history was lost or Home capability was incorrectly inherited")
	}
}

func TestClusterLatencyProbeOnlyUsesPing(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "PING") && len(args) == 2 {
			return fmt.Sprintf("$%d\r\n%s\r\n", len(args[1]), args[1])
		}
		return balanceTestRedisReply(args)
	})
	before := client.homeCfg
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rtt, errProbe := client.probeClusterLatency(ctx, clusterNodeAddress(clusterNode{IP: before.Host, Port: before.Port}))
	if errProbe != nil || rtt <= 0 {
		t.Fatalf("probe = %v, %v", rtt, errProbe)
	}
	pings := 0
	for _, args := range commands.All() {
		if strings.EqualFold(args[0], "SUBSCRIBE") || strings.EqualFold(args[0], "CLUSTER") {
			t.Fatalf("probe altered subscription ownership: %v", args)
		}
		if strings.EqualFold(args[0], "PING") {
			pings++
		}
	}
	if pings != 2 || client.balance.moving || before != client.homeCfg {
		t.Fatal("probe did not isolate warmup and RTT or changed client state")
	}
}

func TestClusterLatencyProbeBudgetAndBackoff(t *testing.T) {
	target, targetCommands := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "PING") {
			return "-ERR probe failed\r\n"
		}
		return balanceTestRedisReply(args)
	})
	node := clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port}
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "CLUSTER") {
			raw := fmt.Sprintf(`{"ok":true,"probe":{"ip":%q,"port":%d}}`, node.IP, node.Port)
			return fmt.Sprintf("$%d\r\n%s\r\n", len(raw), raw)
		}
		return balanceTestRedisReply(args)
	})
	enableBalanceTestClient(client)
	client.balance.latencySupported = true
	history := &clusterLatencyHistory{}
	history.observe(clusterNode{IP: client.homeCfg.Host, Port: client.homeCfg.Port}, 100*time.Millisecond, true, time.Now())
	// Unadvertised targets are never contacted.
	if errReport := client.reportClusterLatency(context.Background(), history, time.Now()); errReport != nil {
		t.Fatal(errReport)
	}
	if len(targetCommands.All()) != 0 {
		t.Fatal("unadvertised target was probed")
	}
	client.clusterNodes = []clusterNode{node}
	if errReport := client.reportClusterLatency(context.Background(), history, time.Now()); errReport == nil {
		t.Fatal("probe failure was swallowed")
	}
	if errReport := client.reportClusterLatency(context.Background(), history, time.Now()); errReport != nil {
		t.Fatal(errReport)
	}
	pings := 0
	for _, args := range targetCommands.All() {
		if strings.EqualFold(args[0], "PING") {
			pings++
		}
	}
	entry := history.entries[clusterNodeAddress(node)]
	if pings != 1 || entry.sample.Valid || entry.probeBackoff != time.Minute || !entry.nextProbeAt.After(time.Now()) {
		t.Fatalf("probe budget/backoff incorrect: pings=%d entry=%+v", pings, entry)
	}
	entry.nextProbeAt = time.Time{}
	entry.nextProbeCycle = 0
	history.entries[clusterNodeAddress(node)] = entry
	if errReport := client.reportClusterLatency(context.Background(), history, time.Now()); errReport == nil {
		t.Fatal("repeated probe failure was swallowed")
	}
	if entry = history.entries[clusterNodeAddress(node)]; entry.probeBackoff != 2*time.Minute {
		t.Fatalf("failure backoff did not increase: %+v", entry)
	}
	if !client.HeartbeatOK() || client.dispatchFenced.Load() || client.balance.moving {
		t.Fatal("failed probes changed healthy membership")
	}
}

func TestClusterLatencyCapabilityNegotiationAndIsolatedFailure(t *testing.T) {
	for _, mode := range []string{"old", "new", "report failure", "malformed response", "wrong response type", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				if strings.EqualFold(args[0], "CLUSTER") {
					if args[2] == "ENABLE" {
						raw := `{"ok":true}`
						if mode != "old" {
							raw = `{"ok":true,"capabilities":["cpa_latency_swap_v1"]}`
						}
						return fmt.Sprintf("$%d\r\n%s\r\n", len(raw), raw)
					}
					switch mode {
					case "report failure":
						return "-ERR unavailable\r\n"
					case "malformed response":
						return "$1\r\n{\r\n"
					case "wrong response type":
						return "$13\r\n{\"ok\":\"true\"}\r\n"
					case "rejected":
						return "$12\r\n{\"ok\":false}\r\n"
					}
					return "$11\r\n{\"ok\":true}\r\n"
				}
				return balanceTestRedisReply(args)
			})
			enableBalanceTestClient(client)
			client.balance.registered = false
			if errSync := client.syncClusterBalance(context.Background()); errSync != nil {
				t.Fatal(errSync)
			}
			if client.balance.latencySupported != (mode != "old") {
				t.Fatal("incorrect latency capability negotiation")
			}
			history := &clusterLatencyHistory{}
			history.observe(clusterNode{IP: client.homeCfg.Host, Port: client.homeCfg.Port}, time.Millisecond, true, time.Now())
			errReport := client.reportClusterLatency(context.Background(), history, time.Now())
			if (errReport != nil) != (mode != "old" && mode != "new") {
				t.Fatalf("report error = %v, mode = %s", errReport, mode)
			}
			if mode == "malformed response" {
				var syntaxError *json.SyntaxError
				if !errors.As(errReport, &syntaxError) {
					t.Fatalf("report error = %v, want JSON syntax error", errReport)
				}
			}
			if mode == "wrong response type" {
				var typeError *json.UnmarshalTypeError
				if !errors.As(errReport, &typeError) {
					t.Fatalf("report error = %v, want JSON type error", errReport)
				}
			}
			reports := 0
			for _, args := range commands.All() {
				if len(args) > 2 && args[2] == "LATENCY" {
					reports++
				}
			}
			if (mode == "old" && reports != 0) || (mode != "old" && reports != 1) || !client.HeartbeatOK() || client.dispatchFenced.Load() || client.balance.moving {
				t.Fatalf("telemetry changed liveness or failed to downgrade: reports=%d", reports)
			}
		})
	}
}
