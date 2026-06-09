package usage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	testAPIKey     = "sk-test-secret-value-123456"
	testAPIKeyHash = "5bfe2fcb866dba4b0eaea7173dfdfbaea58874001c6f909745fd729a830365f0"
)

func TestUsageEventRedactsAPIKeyAndNormalisesTokens(t *testing.T) {
	record := coreusage.Record{
		Provider:    " codex ",
		Model:       "",
		APIKey:      "  " + testAPIKey + "  ",
		Source:      " proxy ",
		AuthIndex:   " 7 ",
		RequestedAt: time.Date(2026, 6, 9, 12, 30, 45, 123456789, time.UTC),
		Latency:     1250 * time.Millisecond,
		Failed:      false,
		Detail: coreusage.Detail{
			InputTokens:     10,
			OutputTokens:    20,
			ReasoningTokens: 3,
			CachedTokens:    -99,
			TotalTokens:     0,
		},
	}

	event := newUsageEvent(context.Background(), record)
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal usage event: %v", err)
	}
	payloadText := string(payload)

	if strings.Contains(payloadText, testAPIKey) {
		t.Fatal("marshaled event contains full API key")
	}
	if event.APIKeyHash == "" {
		t.Fatal("expected non-empty API key hash")
	}
	if event.APIKeyHash != testAPIKeyHash {
		t.Fatalf("expected stable API key hash, got %q want %q", event.APIKeyHash, testAPIKeyHash)
	}
	if event.APIKeyPreview == "" {
		t.Fatal("expected non-empty API key preview")
	}
	if event.APIKeyPreview == testAPIKey {
		t.Fatal("expected API key preview to differ from full API key")
	}
	if event.Model != "unknown" {
		t.Fatalf("expected unknown model for empty model, got %q", event.Model)
	}
	if event.CachedTokens != 0 {
		t.Fatalf("expected negative cached tokens to become 0, got %d", event.CachedTokens)
	}
	if event.TotalTokens != 33 {
		t.Fatalf("expected total tokens fallback to input + output + reasoning, got %d", event.TotalTokens)
	}
	if event.Provider != "codex" {
		t.Fatalf("expected trimmed provider, got %q", event.Provider)
	}
	if event.Source != "proxy" {
		t.Fatalf("expected trimmed source, got %q", event.Source)
	}
	if event.AuthIndex != "7" {
		t.Fatalf("expected trimmed auth index, got %q", event.AuthIndex)
	}
	if event.Success != true || event.Failed != false {
		t.Fatalf("expected success=true failed=false, got success=%v failed=%v", event.Success, event.Failed)
	}
	if event.LatencyMs != 1250 {
		t.Fatalf("expected latency in milliseconds, got %d", event.LatencyMs)
	}
	if event.RequestedAt != record.RequestedAt.Format(time.RFC3339Nano) {
		t.Fatalf("expected requested_at from record, got %q", event.RequestedAt)
	}
}

func TestUsageEventNegativeTokensBecomeZero(t *testing.T) {
	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey: testAPIKey,
		Detail: coreusage.Detail{
			InputTokens:     -1,
			OutputTokens:    -2,
			ReasoningTokens: -3,
			CachedTokens:    -4,
			TotalTokens:     -5,
		},
	})

	if event.InputTokens != 0 {
		t.Fatalf("expected input tokens to become 0, got %d", event.InputTokens)
	}
	if event.OutputTokens != 0 {
		t.Fatalf("expected output tokens to become 0, got %d", event.OutputTokens)
	}
	if event.ReasoningTokens != 0 {
		t.Fatalf("expected reasoning tokens to become 0, got %d", event.ReasoningTokens)
	}
	if event.CachedTokens != 0 {
		t.Fatalf("expected cached tokens to become 0, got %d", event.CachedTokens)
	}
	if event.TotalTokens != 0 {
		t.Fatalf("expected total tokens to become 0, got %d", event.TotalTokens)
	}
}

func TestUsageEventRedactsSourceWhenItContainsAPIKey(t *testing.T) {
	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey: testAPIKey,
		Source: "  " + testAPIKey + "  ",
	})
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal usage event: %v", err)
	}

	if event.Source == testAPIKey {
		t.Fatal("expected source credential to be redacted")
	}
	if strings.Contains(string(payload), testAPIKey) {
		t.Fatal("marshaled event contains full source credential")
	}
}

func TestUsageEventRedactsEmbeddedSourceSecrets(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		secret     string
		wantPrefix string
	}{
		{
			name:       "bearer api key",
			source:     "Bearer " + testAPIKey,
			secret:     testAPIKey,
			wantPrefix: "Bearer ",
		},
		{
			name:       "source parameter token",
			source:     "source=sk-test-secret-value-abcdef",
			secret:     "sk-test-secret-value-abcdef",
			wantPrefix: "source=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := newUsageEvent(context.Background(), coreusage.Record{
				APIKey: testAPIKey,
				Source: tt.source,
			})
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("marshal usage event: %v", err)
			}

			if strings.Contains(event.Source, tt.secret) {
				t.Fatal("source contains full embedded credential")
			}
			if strings.Contains(string(payload), tt.secret) {
				t.Fatal("marshaled event contains full embedded source credential")
			}
			if !strings.HasPrefix(event.Source, tt.wantPrefix) {
				t.Fatalf("expected source to preserve non-secret prefix for %s", tt.name)
			}
		})
	}
}

func TestUsageEventHashAPIKeyTrimsAndUsesStableSHA256(t *testing.T) {
	if got := hashAPIKey("  " + testAPIKey + "  "); got != testAPIKeyHash {
		t.Fatalf("hashAPIKey() = %q, want %q", got, testAPIKeyHash)
	}
}

func TestUsageEventRequestIDFromContext(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "req-context")

	if got := resolveEventRequestID(ctx); got != "req-context" {
		t.Fatalf("resolveEventRequestID() = %q, want %q", got, "req-context")
	}
}

func TestUsageEventEndpointUsesGinRequestPathFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions?stream=true", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	if got := resolveEventEndpoint(ctx); got != "/v1/chat/completions" {
		t.Fatalf("resolveEventEndpoint() = %q, want %q", got, "/v1/chat/completions")
	}
}

func TestUsageEventWriterAppendsMonthlyJSONL(t *testing.T) {
	dir := t.TempDir()
	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  1,
			OutputTokens: 2,
		},
	})
	event.RequestID = "req-jsonl"

	writer := newUsageEventWriter(dir, 0)
	if err := writer.write(event); err != nil {
		t.Fatalf("write usage event: %v", err)
	}

	path := filepath.Join(dir, "usage-events-2026-06.jsonl")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usage event ledger: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one JSONL line, got %d", len(lines))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("expected valid JSONL payload: %v", err)
	}
	if payload["request_id"] != "req-jsonl" {
		t.Fatalf("expected request_id in payload, got %q", payload["request_id"])
	}
	if strings.Contains(lines[0], testAPIKey) {
		t.Fatal("JSONL payload contains full API key")
	}
}

func TestUsageEventWriterAppendsWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	writer := newUsageEventWriter(dir, 0)

	first := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	first.RequestID = "req-jsonl-1"
	second := first
	second.RequestID = "req-jsonl-2"

	if err := writer.write(first); err != nil {
		t.Fatalf("write first usage event: %v", err)
	}
	if err := writer.write(second); err != nil {
		t.Fatalf("write second usage event: %v", err)
	}

	path := filepath.Join(dir, "usage-events-2026-06.jsonl")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usage event ledger: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two JSONL lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "req-jsonl-1") || !strings.Contains(lines[1], "req-jsonl-2") {
		t.Fatal("expected appended request IDs in order")
	}
}

func TestUsageEventWriterCleanupOnlyUsageJSONL(t *testing.T) {
	dir := t.TempDir()
	oldJSONL := filepath.Join(dir, "usage-events-2026-01.jsonl")
	oldRequestLog := filepath.Join(dir, "request.log")
	oldTextLedger := filepath.Join(dir, "usage-events-2026-01.txt")
	oldSubDir := filepath.Join(dir, "usage-events-2026-02.jsonl")

	for _, path := range []string{oldJSONL, oldRequestLog, oldTextLedger} {
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
	}
	if err := os.Mkdir(oldSubDir, 0o700); err != nil {
		t.Fatalf("create fixture subdir: %v", err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{oldJSONL, oldRequestLog, oldTextLedger, oldSubDir} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("age fixture %s: %v", path, err)
		}
	}

	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	event.RequestID = "req-jsonl"

	writer := newUsageEventWriter(dir, 1)
	if err := writer.write(event); err != nil {
		t.Fatalf("write usage event: %v", err)
	}

	if _, err := os.Stat(oldJSONL); !os.IsNotExist(err) {
		t.Fatalf("expected old JSONL ledger to be deleted, stat err=%v", err)
	}
	for _, path := range []string{oldRequestLog, oldTextLedger, oldSubDir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected non-JSONL fixture to remain at %s: %v", path, err)
		}
	}
}

func TestUsageEventWriterCleanupHandlesGlobSpecialCharsInDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "usage[events]")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create usage event dir fixture: %v", err)
	}

	oldJSONL := filepath.Join(dir, "usage-events-2026-01.jsonl")
	if err := os.WriteFile(oldJSONL, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write old usage event ledger: %v", err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldJSONL, oldTime, oldTime); err != nil {
		t.Fatalf("age old usage event ledger: %v", err)
	}

	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	event.RequestID = "req-jsonl"

	writer := newUsageEventWriter(dir, 1)
	if err := writer.write(event); err != nil {
		t.Fatalf("write usage event: %v", err)
	}

	if _, err := os.Stat(oldJSONL); !os.IsNotExist(err) {
		t.Fatalf("expected old JSONL ledger in literal glob dir to be deleted, stat err=%v", err)
	}
}

func TestUsageEventWriterCleanupSkipsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target-ledger.jsonl")
	link := filepath.Join(dir, "usage-events-2026-03.jsonl")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(target, oldTime, oldTime); err != nil {
		t.Fatalf("age symlink target: %v", err)
	}

	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	event.RequestID = "req-jsonl"

	writer := newUsageEventWriter(dir, 1)
	if err := writer.write(event); err != nil {
		t.Fatalf("write usage event: %v", err)
	}

	if info, err := os.Lstat(link); err != nil {
		t.Fatalf("expected symlink ledger to remain: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected ledger fixture to remain a symlink")
	}
}

func TestUsageEventWriterCleanupSkipsBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "usage-events-2026-04.jsonl")
	if err := os.Symlink(filepath.Join(dir, "missing-target.jsonl"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	event.RequestID = "req-jsonl"

	writer := newUsageEventWriter(dir, 1)
	if err := writer.write(event); err != nil {
		t.Fatalf("write usage event with broken symlink fixture: %v", err)
	}

	if info, err := os.Lstat(link); err != nil {
		t.Fatalf("expected broken symlink ledger to remain: %v", err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected broken ledger fixture to remain a symlink")
	}
}

func TestUsageEventWriterTightensExistingDirectoryPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("relax temp dir permissions: %v", err)
	}

	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	event.RequestID = "req-jsonl"

	writer := newUsageEventWriter(dir, 0)
	if err := writer.write(event); err != nil {
		t.Fatalf("write usage event: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat usage event dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("expected usage event dir permissions 0700, got %o", got)
	}
}

func TestUsageEventSyncClientSignsRequest(t *testing.T) {
	event := newUsageEvent(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})
	event.RequestID = "req-sync"

	const internalToken = "internal-token"
	const hmacSecret = "hmac-secret"
	var sawRequest bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("x-internal-token"); got != internalToken {
			t.Fatalf("x-internal-token = %q, want %q", got, internalToken)
		}
		timestamp := r.Header.Get("x-usage-timestamp")
		if timestamp == "" {
			t.Fatal("missing x-usage-timestamp")
		}
		if _, err := strconv.ParseInt(timestamp, 10, 64); err != nil {
			t.Fatalf("invalid timestamp header: %v", err)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if strings.Contains(string(body), testAPIKey) {
			t.Fatal("sync request body contains full API key")
		}
		expectedSignature := signUsageEventBody(hmacSecret, timestamp, body)
		if got := r.Header.Get("x-usage-signature"); got != expectedSignature {
			t.Fatalf("x-usage-signature = %q, want %q", got, expectedSignature)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inserted":1,"skipped":0}`))
	}))
	defer server.Close()

	client := newUsageEventSyncClient(server.URL, internalToken, hmacSecret)
	if err := client.sync(context.Background(), event); err != nil {
		t.Fatalf("sync usage event: %v", err)
	}
	if !sawRequest {
		t.Fatal("expected sync request")
	}
}

func TestUsageEventSyncClientReportsServerFailure(t *testing.T) {
	event := UsageEvent{Version: 1, RequestID: "req-sync-failure", RequestedAt: "2026-06-09T12:00:00Z"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed"}`))
	}))
	defer server.Close()

	client := newUsageEventSyncClient(server.URL, "internal-token", "hmac-secret")
	if err := client.sync(context.Background(), event); err == nil {
		t.Fatal("expected server failure error")
	}
}

func signUsageEventBody(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestUsageEventPluginWritesJSONLAndSyncs(t *testing.T) {
	dir := t.TempDir()
	syncer := &recordingUsageEventSyncer{}
	plugin := newUsageEventPlugin(newUsageEventWriter(dir, 0), syncer)

	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		Detail:      coreusage.Detail{InputTokens: 1, OutputTokens: 2},
	})

	content, err := os.ReadFile(filepath.Join(dir, "usage-events-2026-06.jsonl"))
	if err != nil {
		t.Fatalf("read usage event ledger: %v", err)
	}
	if strings.Contains(string(content), testAPIKey) {
		t.Fatal("usage event ledger contains full API key")
	}
	if len(syncer.events) != 1 {
		t.Fatalf("synced event count = %d, want 1", len(syncer.events))
	}
	if syncer.events[0].Model != "gpt-5.4" {
		t.Fatalf("synced model = %q, want gpt-5.4", syncer.events[0].Model)
	}
}

func TestUsageEventPluginContinuesWhenSyncFails(t *testing.T) {
	dir := t.TempDir()
	plugin := newUsageEventPlugin(newUsageEventWriter(dir, 0), failingUsageEventSyncer{})

	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})

	if _, err := os.Stat(filepath.Join(dir, "usage-events-2026-06.jsonl")); err != nil {
		t.Fatalf("expected JSONL write despite sync failure: %v", err)
	}
}

func TestUsageEventPluginSkipsRecordWithoutAPIKey(t *testing.T) {
	dir := t.TempDir()
	syncer := &recordingUsageEventSyncer{}
	plugin := newUsageEventPlugin(newUsageEventWriter(dir, 0), syncer)

	plugin.HandleUsage(context.Background(), coreusage.Record{
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		Detail:      coreusage.Detail{InputTokens: 1, OutputTokens: 2},
	})

	if _, err := os.Stat(filepath.Join(dir, "usage-events-2026-06.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected no JSONL write for missing API key, stat err=%v", err)
	}
	if len(syncer.events) != 0 {
		t.Fatalf("expected no sync for missing API key, got %d event(s)", len(syncer.events))
	}
}

func TestUsageEventPluginFromEnvDisabled(t *testing.T) {
	t.Setenv("USAGE_EVENTS_ENABLED", "false")
	t.Setenv("USAGE_EVENTS_LOG_DIR", t.TempDir())

	plugin, enabled := newUsageEventPluginFromEnv()
	if enabled {
		t.Fatal("expected usage event plugin to be disabled")
	}
	if plugin != nil {
		t.Fatal("expected disabled usage event plugin to be nil")
	}
}

func TestUsageEventPluginFromEnvEnabledWritesJSONLWithoutSyncConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USAGE_EVENTS_ENABLED", "true")
	t.Setenv("USAGE_EVENTS_LOG_DIR", dir)
	t.Setenv("USAGE_EVENTS_RETENTION_DAYS", "0")
	t.Setenv("YUI_USAGE_EVENT_URL", "")
	t.Setenv("YUI_USAGE_EVENT_TOKEN", "")
	t.Setenv("YUI_USAGE_EVENT_HMAC_SECRET", "")

	plugin, enabled := newUsageEventPluginFromEnv()
	if !enabled {
		t.Fatal("expected usage event plugin to be enabled")
	}
	if plugin == nil {
		t.Fatal("expected usage event plugin")
	}
	plugin.HandleUsage(context.Background(), coreusage.Record{
		APIKey:      testAPIKey,
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
	})

	if _, err := os.Stat(filepath.Join(dir, "usage-events-2026-06.jsonl")); err != nil {
		t.Fatalf("expected env-enabled plugin to write JSONL: %v", err)
	}
}

type recordingUsageEventSyncer struct {
	events []UsageEvent
}

func (s *recordingUsageEventSyncer) sync(ctx context.Context, event UsageEvent) error {
	s.events = append(s.events, event)
	return nil
}

type failingUsageEventSyncer struct{}

func (failingUsageEventSyncer) sync(ctx context.Context, event UsageEvent) error {
	return errors.New("sync failed")
}
