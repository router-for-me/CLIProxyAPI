package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/keepalive"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type countingProber struct{ calls int }

func (p *countingProber) Probe(context.Context, keepalive.ProbeRequest) (keepalive.ProbeResult, error) {
	p.calls++
	return keepalive.ProbeResult{}, nil
}

type stubTimer struct{}

func (stubTimer) Stop() bool { return true }

const keepaliveSession = "4463ede6-1111-2222-3333-444455556666"

func keepaliveBody(ttl string) []byte {
	return []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":100,` +
		`"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"` + ttl + `"}}],` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"metadata":{"user_id":"{\"session_id\":\"` + keepaliveSession + `\"}"}}`)
}

// installKeepaliveScheduler installs a scheduler whose timers never fire, so the
// test observes only whether a probe was scheduled.
func installKeepaliveScheduler(t *testing.T) *[]time.Duration {
	t.Helper()
	scheduler := keepalive.New(keepalive.Config{
		Enabled:              true,
		BeforeExpiry:         5 * time.Minute,
		OnlyWhenAgentsActive: true,
		MaxProbes:            6,
		MaxTokens:            1,
	})
	scheduler.SetProber(&countingProber{})
	var scheduled []time.Duration
	scheduler.SetTimerFactory(func(d time.Duration, _ func()) keepalive.Timer {
		scheduled = append(scheduled, d)
		return stubTimer{}
	})
	keepalive.SetDefault(scheduler)
	t.Cleanup(func() { keepalive.SetDefault(nil) })
	return &scheduled
}

func baseObservation(body []byte) ClaudeCacheKeepaliveObservation {
	return ClaudeCacheKeepaliveObservation{
		ConfirmedClaudeCode: true,
		AuthID:              "auth-a",
		AuthProvider:        "claude",
		Model:               "claude-haiku-4-5-20251001",
		OriginalPayload:     body,
		Headers:             http.Header{"Anthropic-Beta": []string{"claude-code-20250219"}},
		StartedAt:           time.Now(),
	}
}

func TestObserveClaudeCacheKeepaliveSchedulesOneHourRequest(t *testing.T) {
	scheduled := installKeepaliveScheduler(t)
	ObserveClaudeCacheKeepalive(context.Background(), baseObservation(keepaliveBody("1h")))
	if len(*scheduled) != 1 {
		t.Fatalf("scheduled %d probes for a 1h request, want 1", len(*scheduled))
	}
}

func TestObserveClaudeCacheKeepaliveIgnoresFiveMinuteRequest(t *testing.T) {
	scheduled := installKeepaliveScheduler(t)
	ObserveClaudeCacheKeepalive(context.Background(), baseObservation(keepaliveBody("5m")))
	if len(*scheduled) != 0 {
		t.Fatalf("scheduled %d probes for a 5m request, want 0", len(*scheduled))
	}
}

func TestObserveClaudeCacheKeepaliveIgnoresUnconfirmedClient(t *testing.T) {
	scheduled := installKeepaliveScheduler(t)
	observation := baseObservation(keepaliveBody("1h"))
	observation.ConfirmedClaudeCode = false
	ObserveClaudeCacheKeepalive(context.Background(), observation)
	if len(*scheduled) != 0 {
		t.Fatalf("scheduled %d probes for an unconfirmed client, want 0", len(*scheduled))
	}
}

func TestObserveClaudeCacheKeepaliveIgnoresRequestWithoutSession(t *testing.T) {
	scheduled := installKeepaliveScheduler(t)
	observation := baseObservation([]byte(`{"model":"m","messages":[],"system":[{"cache_control":{"ttl":"1h"}}]}`))
	ObserveClaudeCacheKeepalive(context.Background(), observation)
	if len(*scheduled) != 0 {
		t.Fatalf("scheduled %d probes without a session id, want 0", len(*scheduled))
	}
}

func TestObserveClaudeCacheKeepaliveNoSchedulerInstalled(t *testing.T) {
	keepalive.SetDefault(nil)
	ObserveClaudeCacheKeepalive(context.Background(), baseObservation(keepaliveBody("1h")))
}

func TestClaudeCacheKeepaliveAffinityKeyPrefersSelectionMetadata(t *testing.T) {
	observation := baseObservation(keepaliveBody("1h"))
	observation.Metadata = map[string]any{
		cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
		cliproxyexecutor.SessionAffinityModelMetadataKey:    "claude-sonnet-5",
	}
	provider, model := claudeCacheKeepaliveAffinityKey(observation)
	if provider != "mixed" || model != "claude-sonnet-5" {
		t.Fatalf("affinity key = (%q, %q), want the values selection published", provider, model)
	}

	observation.Metadata = nil
	provider, model = claudeCacheKeepaliveAffinityKey(observation)
	if provider != "claude" || model != "claude-haiku-4-5-20251001" {
		t.Fatalf("affinity key fallback = (%q, %q)", provider, model)
	}
}
