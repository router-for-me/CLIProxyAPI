package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/keepalive"
)

type keepaliveTestProber struct{ result keepalive.ProbeResult }

func (p keepaliveTestProber) Probe(context.Context, keepalive.ProbeRequest) (keepalive.ProbeResult, error) {
	return p.result, nil
}

type keepaliveTestTimer struct{}

func (keepaliveTestTimer) Stop() bool { return true }

func getCacheKeepalive(t *testing.T) (int, keepalive.Snapshot) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/cache-keepalive", nil)
	(&Handler{}).GetCacheKeepalive(c)

	var snapshot keepalive.Snapshot
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &snapshot); errDecode != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", errDecode, recorder.Body.String())
	}
	return recorder.Code, snapshot
}

func TestGetCacheKeepaliveWithNoScheduler(t *testing.T) {
	keepalive.SetDefault(nil)
	status, snapshot := getCacheKeepalive(t)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if snapshot.Enabled {
		t.Fatalf("enabled = true with no scheduler installed")
	}
	if snapshot.Sessions == nil || snapshot.Counters.SkippedByReason == nil {
		t.Fatalf("empty snapshot must still be well formed: %+v", snapshot)
	}
}

func TestGetCacheKeepaliveReportsSessionsAndCounters(t *testing.T) {
	scheduler := keepalive.New(keepalive.Config{
		Enabled:              true,
		BeforeExpiry:         5 * time.Minute,
		OnlyWhenAgentsActive: false,
		MaxProbes:            6,
		MaxTokens:            1,
	})
	var fire func()
	scheduler.SetTimerFactory(func(_ time.Duration, run func()) keepalive.Timer {
		fire = run
		return keepaliveTestTimer{}
	})
	scheduler.SetProber(keepaliveTestProber{result: keepalive.ProbeResult{CacheReadInputTokens: 12468, CacheCreationInputTokens: 4}})
	keepalive.SetDefault(scheduler)
	t.Cleanup(func() { keepalive.SetDefault(nil) })

	scheduler.Observe(keepalive.ObserveInput{
		SessionID: "aa11bb22-cc33-dd44-ee55-ff6677889900",
		AuthID:    "claude-account.json",
		Provider:  "mixed",
		Model:     "claude-haiku-4-5-20251001",
		Body:      []byte(`{"system":[{"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`),
		TTL:       time.Hour,
		StartedAt: time.Now(),
	})
	if fire == nil {
		t.Fatalf("Observe did not schedule a probe")
	}
	fire()

	status, snapshot := getCacheKeepalive(t)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !snapshot.Enabled || snapshot.MaxProbes != 6 || snapshot.BeforeExpiry != "5m0s" {
		t.Fatalf("snapshot config = %+v", snapshot)
	}
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want 1", snapshot.Sessions)
	}
	session := snapshot.Sessions[0]
	if session.SessionID != "aa11bb22-cc33-dd44-ee55-ff6677889900" || session.AuthID != "claude-account.json" {
		t.Fatalf("session identity = %+v", session)
	}
	if session.ProbesSent != 1 || session.ConsecutiveProbes != 1 {
		t.Fatalf("probe counts = %+v", session)
	}
	if session.LastProbe == nil || session.LastProbe.Status != keepalive.ProbeStatusHit {
		t.Fatalf("last probe = %+v", session.LastProbe)
	}
	if session.LastProbe.CacheReadInputTokens != 12468 || session.LastProbe.CacheCreationInputTokens != 4 {
		t.Fatalf("last probe tokens = %+v", session.LastProbe)
	}
	if snapshot.Counters.Scheduled != 1 || snapshot.Counters.Fired != 1 || snapshot.Counters.Hits != 1 {
		t.Fatalf("counters = %+v", snapshot.Counters)
	}
}
