package auth

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestCapacityClaudeFableWindow(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		model, utilization, status string
		want                       float64
	}{
		{"claude-fable-5-1", "0.99", "allowed", 0.01},
		{"claude-fable-5", "0.1", "rejected", 0},
		{"claude-fable-5-1", "", "rejected", 0},
		{"claude-opus-5-5", "0.99", "rejected", 0.9},
		{"claude-sonnet-5-5", "0.99", "rejected", 0.9},
	} {
		t.Run(tc.model+tc.utilization+tc.status, func(t *testing.T) {
			signals := map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.1", "Anthropic-Ratelimit-Unified-7d_oi-Status": tc.status}
			if tc.utilization != "" {
				signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] = tc.utilization
			}
			auth := &Auth{Provider: "claude", Quota: QuotaState{ObservedAt: now, Signals: signals}}
			got, known := observedCapacity(auth, tc.model, now)
			if !known || math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("capacity=%v,%v want %v", got, known, tc.want)
			}
			auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Reset"] = "1800000000"
			if _, known = observedCapacity(auth, "claude-fable-5-1", now); known {
				t.Fatal("expired Fable window remained usable")
			}
		})
	}
}

func TestCapacityAgesCapturedHeadersBeforeStreamCompletion(t *testing.T) {
	captured := time.Unix(1800000000, 0)
	completed := captured.Add(2 * time.Minute)
	headers := http.Header{"X-Codex-Primary-Used-Percent": []string{"10"}, "X-Codex-Primary-Reset-After-Seconds": []string{"60"}, "X-Request-Id": []string{"later"}}
	times := map[string]time.Time{"X-Codex-Primary-Used-Percent": captured, "X-Codex-Primary-Reset-After-Seconds": captured, "X-Request-Id": completed}
	observed := quotaObservationTime("codex", headers, times, completed)
	auth := &Auth{Provider: "codex"}
	auth.Quota.ObserveResponseHeadersForProvider("codex", headers, observed)
	if !observed.Equal(captured) {
		t.Fatalf("observation=%v, want header capture %v", observed, captured)
	}
	if _, known := observedCapacity(auth, "gpt-6.1-sol", completed); known {
		t.Fatal("stream completion rejuvenated expired relative reset")
	}
	// A later quota event must not rejuvenate retained older windows.
	headers.Set("X-Codex-Secondary-Used-Percent", "20")
	times["X-Codex-Secondary-Used-Percent"] = completed
	if got := quotaObservationTime("codex", headers, times, completed); !got.Equal(captured) {
		t.Fatal("partial event rejuvenated earlier window")
	}
	// Conversely, handshake headers alone must not age a new quota event.
	delete(headers, "X-Codex-Primary-Used-Percent")
	delete(headers, "X-Codex-Primary-Reset-After-Seconds")
	times["X-Request-Id"] = captured
	if got := quotaObservationTime("codex", headers, times, completed); !got.Equal(completed) {
		t.Fatal("non-quota headers aged latest quota event")
	}
}

func TestMarkResultUsesHeaderCaptureTime(t *testing.T) {
	manager := NewManager(nil, &CapacitySelector{}, nil)
	auth := &Auth{ID: "captured-quota", Provider: "codex", Status: StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": []string{"10"}})
	_, times := internallogging.GetResponseHeadersWithCaptureTimes(ctx)
	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-6.1-sol", Success: true})
	got, _ := manager.GetByID(auth.ID)
	if !got.Quota.ObservedAt.Equal(times["X-Codex-Primary-Used-Percent"]) {
		t.Fatalf("MarkResult changed capture time: %v", got.Quota.ObservedAt)
	}
}

func TestQuotaObservationRejectsLateOlderStream(t *testing.T) {
	captured := time.Unix(1800000000, 0)
	var quota QuotaState
	quota.ObserveResponseHeadersForProvider("codex", http.Header{"X-Codex-Primary-Used-Percent": []string{"80"}}, captured.Add(time.Minute))
	if quota.ObserveResponseHeadersForProvider("codex", http.Header{"X-Codex-Primary-Used-Percent": []string{"10"}}, captured) {
		t.Fatal("older stream replaced newer quota")
	}
	if quota.Signals["X-Codex-Primary-Used-Percent"] != "80" || !quota.ObservedAt.Equal(captured.Add(time.Minute)) {
		t.Fatal("newer quota changed")
	}
}
