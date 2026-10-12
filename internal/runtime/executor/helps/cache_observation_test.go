package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func resetCacheCapture(t *testing.T) {
	t.Helper()
	cacheCapture.Lock()
	previous := cacheCapture.active
	cacheCapture.active = nil
	cacheCapture.Unlock()
	t.Cleanup(func() {
		cacheCapture.Lock()
		cacheCapture.active = previous
		cacheCapture.Unlock()
	})
}

func captureConfig() *config.Config {
	return &config.Config{CacheObservation: &config.CacheObservationConfig{
		SessionID: "fixture-session", ExpiresAt: time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339), MaxAttempts: 100,
	}}
}

func observeFixture(t *testing.T, cfg *config.Config, body string) *UsageReporter {
	t.Helper()
	r := NewUsageReporter(context.Background(), "claude", "fixture-model", nil)
	r.authIndex = "fixture-credential"
	r.ObserveCacheRequest(util.WithSessionID(context.Background(), "fixture-session"), cfg, []byte(body), "https://upstream.test/messages")
	if r.cacheObservation == nil {
		t.Fatal("request was not captured")
	}
	return r
}

func TestCacheObservationFiniteScope(t *testing.T) {
	resetCacheCapture(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	c := config.CacheObservationConfig{SessionID: "session", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339), MaxAttempts: 2}
	if beginCacheCapture(c, "unrelated", now) != nil {
		t.Fatal("captured unrelated session")
	}
	s := beginCacheCapture(c, "session", now)
	if s == nil || beginCacheCapture(c, "session", now) != s {
		t.Fatal("reload did not retain capture identity")
	}
	if beginCacheCapture(c, "session", now) != nil || beginCacheCapture(c, "session", now.Add(time.Minute)) != nil {
		t.Fatal("budget or expiry was not enforced")
	}
	for _, invalid := range []config.CacheObservationConfig{
		{SessionID: "session", ExpiresAt: "invalid", MaxAttempts: 1},
		{SessionID: "session", ExpiresAt: now.Add(2 * time.Hour).Format(time.RFC3339), MaxAttempts: 1},
		{SessionID: "session", ExpiresAt: c.ExpiresAt, MaxAttempts: 0},
		{SessionID: "session", ExpiresAt: c.ExpiresAt, MaxAttempts: 101},
		{ExpiresAt: c.ExpiresAt, MaxAttempts: 1},
	} {
		if beginCacheCapture(invalid, "session", now) != nil {
			t.Fatal("invalid capture was admitted")
		}
	}
}

func TestCacheObservationConcurrentBudgetAndReplacement(t *testing.T) {
	resetCacheCapture(t)
	now := time.Now()
	c := *captureConfig().CacheObservation
	c.MaxAttempts = 3
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if beginCacheCapture(c, c.SessionID, now) != nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 3 {
		t.Fatalf("admitted %d, want 3", admitted)
	}
	different := c
	different.SessionID = "other"
	if beginCacheCapture(different, "other", now) != nil {
		t.Fatal("reload replaced unexpired capture")
	}
}

func TestCacheObservationGrowingHistoryAndBoundaries(t *testing.T) {
	resetCacheCapture(t)
	cfg := captureConfig()
	base := `{"model":"fixture","thinking":{"type":"adaptive"},"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"system":[{"type":"text","text":"stable"}],"messages":[{"role":"user","content":"first"}]}`
	growing := strings.Replace(base, `"first"}]`, `"first"},{"role":"assistant","content":[{"type":"tool_use","id":"fixed-id","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"fixed-id","content":"result"}]}]`, 1)
	a, b := observeFixture(t, cfg, base).cacheObservation, observeFixture(t, cfg, growing).cacheObservation
	if len(a.Components) != 3 || len(b.Components) != 5 || a.BodyDigest == b.BodyDigest {
		t.Fatal("growing sequence shape was not recorded")
	}
	for i := range a.Components {
		if a.Components[i] != b.Components[i] {
			t.Fatal("unchanged prefix reported as changed")
		}
	}
	for _, changed := range []string{
		strings.Replace(base, "stable", "changed-system", 1),
		strings.Replace(base, "first", "pruned-history", 1),
		strings.Replace(base, "lookup", "reloaded-tool", 1),
	} {
		o := observeFixture(t, cfg, changed).cacheObservation
		if o.Components[len(o.Components)-1].PrefixDigest == a.Components[len(a.Components)-1].PrefixDigest {
			t.Fatal("changed prefix was not detected")
		}
	}
	settings := observeFixture(t, cfg, strings.Replace(base, "adaptive", "disabled", 1)).cacheObservation
	if settings.Settings["thinking"] == a.Settings["thinking"] {
		t.Fatal("thinking setting change was not detected")
	}
	r := NewUsageReporter(context.Background(), "claude", "fixture-model", nil)
	r.authIndex = "failover-credential"
	r.ObserveCacheRequest(util.WithSessionID(context.Background(), cfg.CacheObservation.SessionID), cfg, []byte(base), "https://alternate.test/messages")
	if r.cacheObservation.CredentialDigest == a.CredentialDigest || r.cacheObservation.RouteDigest == a.RouteDigest {
		t.Fatal("failover scope change was not recorded")
	}
}

func TestCacheObservationPrivacyMarkersAndTTL(t *testing.T) {
	resetCacheCapture(t)
	cfg := captureConfig()
	body := `{"model":"secret-model","prompt_cache_key":"secret-cache-key","metadata":{"user_id":"secret-user"},"tools":[],"system":[{"text":"secret-system","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"text":"secret-prompt","cache_control":{"type":"ephemeral","ttl":"secret-ttl"}}]}]}`
	r := observeFixture(t, cfg, body)
	r.ObserveCacheUsage([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":0,"cache_read_input_tokens":20,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":10}}}}`))
	r.ObserveCacheUsage([]byte(`data: {"type":"message_delta","usage":{"output_tokens":2}}`))
	o := r.cacheObservation
	if o.CacheWrite5m == nil || *o.CacheWrite5m != 0 || o.CacheWrite1h == nil || *o.CacheWrite1h != 10 || len(o.UsageFields) != 4 {
		t.Fatal("split stream counters were lost")
	}
	if len(o.Markers) != 2 || o.Markers[0].TTL != "1h" || o.Markers[1].TTL != "unknown" {
		t.Fatal("marker TTLs were not allowlisted")
	}
	var output bytes.Buffer
	logger := log.StandardLogger()
	previous := logger.Out
	previousFormatter := logger.Formatter
	logger.SetOutput(&output)
	logger.SetFormatter(&logging.LogFormatter{})
	t.Cleanup(func() {
		logger.SetOutput(previous)
		logger.SetFormatter(previousFormatter)
	})
	record := usage.Record{
		RequestID: r.RequestID(), APIKey: "secret-api-key", SessionID: "secret-session", AuthID: "secret-account",
		ResponseModel: "secret-served-model", Fail: usage.Failure{StatusCode: 429, Body: "secret-failure"}, Failed: true,
		ServiceTier: "secret-tier", ResponseServiceTier: "secret-response-tier", Detail: ParseClaudeUsage([]byte(`{"usage":{"input_tokens":0,"cache_read_input_tokens":20,"cache_creation_input_tokens":10,"output_tokens":2}}`)),
	}
	r.publishCacheObservation(record)
	parts := strings.SplitN(output.String(), " cache_observation=", 2)
	if len(parts) != 2 {
		t.Fatal("native application formatter omitted the observation record")
	}
	var rendered cacheObservation
	if err := json.Unmarshal([]byte(strings.TrimSpace(parts[1])), &rendered); err != nil || rendered.RequestID != r.RequestID() || rendered.Phase != "completion" || rendered.Tokens == nil || rendered.Tokens.TotalTokens != 32 {
		t.Fatal("native application log did not preserve the per-execution usage record")
	}
	if strings.Contains(output.String(), "secret-") || strings.Contains(output.String(), cfg.CacheObservation.SessionID) {
		t.Fatal("raw sensitive value escaped observation")
	}
	encoded, err := json.Marshal(o)
	if err != nil || bytes.Contains(encoded, []byte("scope")) {
		t.Fatal("scope key or raw body escaped record")
	}
	if o.RequestID != r.RequestID() || o.Tokens == nil || o.Tokens.TotalTokens != 32 || o.StatusCode != 429 {
		t.Fatal("usage was not joined to the execution attempt")
	}
}

func TestCacheObservationMissingAndMalformedEvidence(t *testing.T) {
	resetCacheCapture(t)
	cfg := captureConfig()
	for _, body := range []string{`not-json`, strings.Repeat("x", (8<<20)+1)} {
		o := observeFixture(t, cfg, body).cacheObservation
		if o.ShapeStatus == "complete" || o.BodyDigest != "" || len(o.Components) != 0 {
			t.Fatal("unsupported evidence reported as complete")
		}
	}
	r := observeFixture(t, cfg, `{"messages":[]}`)
	r.ObserveCacheUsage([]byte(`{"usage":null}`))
	r.ObserveCacheUsage([]byte(`{"usage":{"cache_creation":{"ephemeral_1h_input_tokens":1.5,"ephemeral_5m_input_tokens":-1}}}`))
	r.publishCacheObservation(usage.Record{})
	if r.cacheObservation.UsageState != "absent" || r.cacheObservation.Tokens != nil || r.cacheObservation.CacheWrite1h != nil || r.cacheObservation.CacheWrite5m != nil {
		t.Fatal("missing or invalid usage became measured zero")
	}
	r2 := observeFixture(t, cfg, `{"messages":[]}`)
	r2.ObserveCacheUsage([]byte(`{"response":{"usage":{"input_tokens":0,"output_tokens":0}}}`))
	r2.publishCacheObservation(usage.Record{Detail: usage.EnsureTokenBreakdownForProvider(usage.Detail{}, "codex", "CodexExecutor")})
	if r2.cacheObservation.UsageState != "reported" || r2.cacheObservation.Tokens == nil {
		t.Fatal("reported zero usage was confused with missing usage")
	}
}

func TestCacheObservationDisabledAndBytePreserving(t *testing.T) {
	resetCacheCapture(t)
	body := []byte(`{"messages":[{"role":"user","content":"keep bytes and spacing"}]}`)
	before := bytes.Clone(body)
	r := NewUsageReporter(context.Background(), "claude", "fixture-model", nil)
	r.ObserveCacheRequest(context.Background(), &config.Config{}, body, "route")
	if r.cacheObservation != nil {
		t.Fatal("capture enabled by default")
	}
	cfg := captureConfig()
	r.ObserveCacheRequest(util.WithSessionID(context.Background(), cfg.CacheObservation.SessionID), cfg, body, "route")
	if !bytes.Equal(body, before) {
		t.Fatal("observation changed request bytes")
	}
}
