package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestGetRequestLogUsageAggregatesHistoryActiveAndPending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	logsRoot := filepath.Join(root, "runtime", "logs", "keys")
	workDir := filepath.Join(root, "runtime", "log-uploader")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: runtime/logs/keys\nwork-dir: runtime/log-uploader\ntimezone: Asia/Shanghai\n"))

	hour13 := time.Date(2026, 7, 14, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	hour14 := hour13.Add(time.Hour)
	hour15 := hour14.Add(time.Hour)
	hour16 := hour15.Add(time.Hour)

	historyLines := []string{
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded_cleanup_pending",
			Hour:   hour13,
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(1, 50, "gpt", 1, 50),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded",
			Hour:   hour13,
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(2, 100, "gpt", 2, 100),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded",
			Hour:   hour14,
			KeyNames: map[string]requestLogUsageAuditKey{
				"bob": requestLogUsageAuditKeyWithModel(4, 200, "mini", 4, 200),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "failed",
			Hour:   hour16,
			KeyNames: map[string]requestLogUsageAuditKey{
				"ignored": requestLogUsageAuditKeyWithModel(99, 999, "gpt", 99, 999),
			},
		}),
		`{"status":"uploaded","hour":`,
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "history", "2026-07.jsonl"), []byte(strings.Join(historyLines, "\n")+"\n"))

	activeLines := []string{
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded_cleanup_pending",
			Hour:   hour13,
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(3, 150, "gpt", 3, 150),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded_cleanup_pending",
			Hour:   hour14,
			KeyNames: map[string]requestLogUsageAuditKey{
				"bob": requestLogUsageAuditKeyWithModel(6, 300, "mini", 6, 300),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded",
			Hour:   hour14,
			KeyNames: map[string]requestLogUsageAuditKey{
				"bob": requestLogUsageAuditKeyWithModel(7, 350, "mini", 7, 350),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded_cleanup_pending",
			Hour:   hour15,
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(1, 25, "gpt", 1, 25),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{Status: "uploaded", Hour: hour16, KeyNames: map[string]requestLogUsageAuditKey{}}),
		`{"status":"uploaded"`,
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "audit.jsonl"), []byte(strings.Join(activeLines, "\n")))

	mustWriteRequestLogUsageFile(t, filepath.Join(logsRoot, "alice", "one.log"), []byte("123"))
	mustWriteRequestLogUsageFile(t, filepath.Join(logsRoot, "alice", "two.log"), []byte("12345"))
	mustWriteRequestLogUsageFile(t, filepath.Join(logsRoot, "alice", "empty.log"), nil)
	mustWriteRequestLogUsageFile(t, filepath.Join(logsRoot, "alice", "ignored.txt"), []byte("not a log"))
	mustWriteRequestLogUsageFile(t, filepath.Join(logsRoot, "bob", "pending.log"), []byte("1234567"))

	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(root, "fallback-auths"),
		SDKConfig: config.SDKConfig{
			APIKeys:     []string{"raw-secret-must-not-appear", "another-secret"},
			APIKeyNames: []string{"alice", "zero", "alice", ""},
		},
	}, configPath, nil)
	response, raw := performRequestLogUsage(t, handler)

	if response.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q", response.Timezone)
	}
	if response.Totals.SourceCount != 11 || response.Totals.SourceBytes != 525 || response.Totals.BatchCount != 3 {
		t.Fatalf("unexpected uploaded totals: %+v", response.Totals)
	}
	if response.Totals.PendingCount != 3 || response.Totals.PendingBytes != 15 || response.Totals.KeyCount != 3 {
		t.Fatalf("unexpected pending/key totals: %+v", response.Totals)
	}
	if strings.Contains(raw, "raw-secret-must-not-appear") || strings.Contains(raw, "another-secret") {
		t.Fatalf("response leaked an API key: %s", raw)
	}
	if len(response.ParseErrors) != 1 || !strings.Contains(response.ParseErrors[0], "history/2026-07.jsonl line 5") {
		t.Fatalf("parse errors = %#v", response.ParseErrors)
	}
	if len(response.Hours) != 3 || response.Hours[0].Hour != "2026-07-14T13:00:00+08:00" || response.Hours[2].Hour != "2026-07-14T15:00:00+08:00" {
		t.Fatalf("hours are not sorted and normalized: %+v", response.Hours)
	}
	if response.Hours[0].SourceCount != 3 {
		t.Fatalf("active cleanup-pending record did not override history: %+v", response.Hours[0])
	}
	if response.Hours[1].SourceCount != 7 {
		t.Fatalf("final uploaded record did not override cleanup-pending: %+v", response.Hours[1])
	}
	if len(response.Days) != 1 || response.Days[0].Date != "2026-07-14" || response.Days[0].SourceCount != 11 || response.Days[0].SourceBytes != 525 {
		t.Fatalf("daily totals = %+v", response.Days)
	}
	if len(response.Days[0].Keys) != 2 || response.Days[0].Keys[0].KeyName != "alice" || response.Days[0].Keys[0].SourceCount != 4 || response.Days[0].Keys[0].SourceBytes != 175 || response.Days[0].Keys[1].KeyName != "bob" || response.Days[0].Keys[1].SourceCount != 7 || response.Days[0].Keys[1].SourceBytes != 350 {
		t.Fatalf("daily key totals = %+v", response.Days[0].Keys)
	}

	if got := requestLogUsageKeyByName(t, response.Keys, "alice"); got.SourceCount != 4 || got.SourceBytes != 175 || got.BatchCount != 2 || got.PendingCount != 2 || got.PendingBytes != 8 || !got.Configured {
		t.Fatalf("alice = %+v", got)
	} else if len(got.Models) != 1 || got.Models[0].Model != "gpt" || got.Models[0].SourceCount != 4 {
		t.Fatalf("alice models = %+v", got.Models)
	}
	if got := requestLogUsageKeyByName(t, response.Keys, "bob"); got.SourceCount != 7 || got.SourceBytes != 350 || got.BatchCount != 1 || got.PendingCount != 1 || got.PendingBytes != 7 || got.Configured {
		t.Fatalf("bob = %+v", got)
	}
	if got := requestLogUsageKeyByName(t, response.Keys, "zero"); got.SourceCount != 0 || got.PendingCount != 0 || !got.Configured || got.FirstHour != "" || got.LastHour != "" {
		t.Fatalf("zero = %+v", got)
	}
	if response.Keys[0].KeyName != "alice" || response.Keys[1].KeyName != "bob" || response.Keys[2].KeyName != "zero" {
		t.Fatalf("keys are not sorted: %+v", response.Keys)
	}
}

func TestGetRequestLogUsageSeparatesClaudeAndCodexProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: logs/keys\nwork-dir: work\ntimezone: Asia/Shanghai\n"))

	hour := time.Date(2026, 7, 22, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	lines := []string{
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:   "uploaded",
			Hour:     hour,
			Provider: "codex",
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(2, 100, "gpt-5.6", 2, 100),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:   "uploaded",
			Hour:     hour,
			Provider: "fable5",
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(3, 300, "claude-fable-5", 3, 300),
				"bob":   requestLogUsageAuditKeyWithModel(1, 50, "claude-fable-5", 1, 50),
			},
		}),
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "audit.jsonl"), []byte(strings.Join(lines, "\n")+"\n"))

	handler := NewHandler(&config.Config{AuthDir: filepath.Join(root, "auths")}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)

	if response.Totals.SourceCount != 6 || response.Totals.SourceBytes != 450 || response.Totals.BatchCount != 2 {
		t.Fatalf("totals = %+v", response.Totals)
	}
	if len(response.Providers) != 2 {
		t.Fatalf("providers = %+v", response.Providers)
	}
	if response.Providers[0].Provider != "codex" || response.Providers[0].SourceCount != 2 || response.Providers[0].SourceBytes != 100 {
		t.Fatalf("codex provider summary = %+v", response.Providers[0])
	}
	if response.Providers[1].Provider != "fable5" || response.Providers[1].SourceCount != 4 || response.Providers[1].SourceBytes != 350 {
		t.Fatalf("claude provider summary = %+v", response.Providers[1])
	}

	if len(response.Hours) != 2 {
		t.Fatalf("hours = %+v", response.Hours)
	}
	if response.Hours[0].Provider != "codex" || response.Hours[1].Provider != "fable5" {
		t.Fatalf("hour providers = %q, %q", response.Hours[0].Provider, response.Hours[1].Provider)
	}

	if len(response.Days) != 1 {
		t.Fatalf("days = %+v", response.Days)
	}
	day := response.Days[0]
	if len(day.Providers) != 2 || day.Providers[0].Provider != "codex" || day.Providers[0].SourceBytes != 100 || day.Providers[1].Provider != "fable5" || day.Providers[1].SourceBytes != 350 {
		t.Fatalf("day providers = %+v", day.Providers)
	}
	if len(day.Keys) != 2 {
		t.Fatalf("day keys = %+v", day.Keys)
	}
	if len(day.Keys[0].Providers) != 2 || day.Keys[0].KeyName != "alice" || day.Keys[0].Providers[0].Provider != "codex" || day.Keys[0].Providers[0].SourceBytes != 100 || day.Keys[0].Providers[1].Provider != "fable5" || day.Keys[0].Providers[1].SourceBytes != 300 {
		t.Fatalf("alice day providers = %+v", day.Keys[0].Providers)
	}
	if len(day.Keys[1].Providers) != 1 || day.Keys[1].KeyName != "bob" || day.Keys[1].Providers[0].Provider != "fable5" || day.Keys[1].Providers[0].SourceBytes != 50 {
		t.Fatalf("bob day providers = %+v", day.Keys[1].Providers)
	}

	alice := requestLogUsageKeyByName(t, response.Keys, "alice")
	if len(alice.Providers) != 2 || alice.Providers[0].Provider != "codex" || alice.Providers[0].SourceCount != 2 || alice.Providers[1].Provider != "fable5" || alice.Providers[1].SourceCount != 3 {
		t.Fatalf("alice key providers = %+v", alice.Providers)
	}
	bob := requestLogUsageKeyByName(t, response.Keys, "bob")
	if len(bob.Providers) != 1 || bob.Providers[0].Provider != "fable5" || bob.Providers[0].SourceCount != 1 || bob.Providers[0].SourceBytes != 50 {
		t.Fatalf("bob key providers = %+v", bob.Providers)
	}
}

func TestGetRequestLogUsageSeparatesGrokProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: logs/keys\nwork-dir: work\ntimezone: Asia/Shanghai\n"))

	hour := time.Date(2026, 7, 22, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	lines := []string{
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:   "uploaded",
			Hour:     hour,
			Provider: "codex",
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(2, 100, "gpt-5.6", 2, 100),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:   "uploaded",
			Hour:     hour,
			Provider: "grok45",
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(5, 500, "grok-4.5", 5, 500),
			},
		}),
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "audit.jsonl"), []byte(strings.Join(lines, "\n")+"\n"))

	handler := NewHandler(&config.Config{AuthDir: filepath.Join(root, "auths")}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)

	if response.Totals.SourceCount != 7 || response.Totals.SourceBytes != 600 || response.Totals.BatchCount != 2 {
		t.Fatalf("totals = %+v", response.Totals)
	}
	if len(response.Providers) != 2 {
		t.Fatalf("providers = %+v", response.Providers)
	}
	if response.Providers[0].Provider != "codex" || response.Providers[0].SourceCount != 2 || response.Providers[0].SourceBytes != 100 {
		t.Fatalf("codex provider summary = %+v", response.Providers[0])
	}
	if response.Providers[1].Provider != "grok45" || response.Providers[1].SourceCount != 5 || response.Providers[1].SourceBytes != 500 {
		t.Fatalf("grok provider summary = %+v", response.Providers[1])
	}

	if len(response.Hours) != 2 {
		t.Fatalf("hours = %+v", response.Hours)
	}
	if response.Hours[0].Provider != "codex" || response.Hours[1].Provider != "grok45" {
		t.Fatalf("hour providers = %q, %q", response.Hours[0].Provider, response.Hours[1].Provider)
	}

	alice := requestLogUsageKeyByName(t, response.Keys, "alice")
	if len(alice.Providers) != 2 || alice.Providers[0].Provider != "codex" || alice.Providers[1].Provider != "grok45" {
		t.Fatalf("alice key providers = %+v", alice.Providers)
	}
	if alice.Providers[1].SourceCount != 5 || alice.Providers[1].SourceBytes != 500 {
		t.Fatalf("alice grok provider totals = %+v", alice.Providers[1])
	}
	if len(alice.Models) == 0 {
		t.Fatalf("alice models empty")
	}
	foundGrokModel := false
	for _, model := range alice.Models {
		if model.Model == "grok-4.5" && model.SourceCount == 5 && model.SourceBytes == 500 {
			foundGrokModel = true
			break
		}
	}
	if !foundGrokModel {
		t.Fatalf("alice models missing grok-4.5: %+v", alice.Models)
	}
}

func TestBuildRequestLogUsageResponseGroupsDaysInConfiguredTimezone(t *testing.T) {
	location, errLocation := time.LoadLocation("Asia/Shanghai")
	if errLocation != nil {
		t.Fatalf("load timezone: %v", errLocation)
	}
	firstHour := time.Date(2026, 7, 14, 15, 0, 0, 0, time.UTC)
	secondHour := firstHour.Add(time.Hour)
	batches := map[string]requestLogUsageBatch{
		firstHour.Format(time.RFC3339): {
			hour:        firstHour,
			sourceCount: 2,
			sourceBytes: 20,
			keyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(2, 20, "gpt", 2, 20),
			},
		},
		secondHour.Format(time.RFC3339): {
			hour:        secondHour,
			sourceCount: 3,
			sourceBytes: 30,
			keyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(3, 30, "gpt", 3, 30),
			},
		},
	}

	response := buildRequestLogUsageResponse(requestLogUsageSettings{
		timezone: "Asia/Shanghai",
		location: location,
	}, nil, batches, nil, nil)

	if len(response.Days) != 2 {
		t.Fatalf("days = %+v", response.Days)
	}
	if response.Days[0].Date != "2026-07-14" || response.Days[0].SourceCount != 2 || response.Days[0].SourceBytes != 20 {
		t.Fatalf("first day = %+v", response.Days[0])
	}
	if response.Days[1].Date != "2026-07-15" || response.Days[1].SourceCount != 3 || response.Days[1].SourceBytes != 30 {
		t.Fatalf("second day = %+v", response.Days[1])
	}
}

func TestGetRequestLogUsageFallsBackToAuthDirectory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	hour := time.Date(2026, 7, 15, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	record := requestLogUsageAuditRecord{
		Status: "uploaded",
		Hour:   hour,
		KeyNames: map[string]requestLogUsageAuditKey{
			"fallback-user": requestLogUsageAuditKeyWithModel(2, 80, "gpt", 2, 80),
		},
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(authDir, "log-uploader", "audit.jsonl"), []byte(marshalRequestLogUsageAudit(t, record)+"\n"))
	mustWriteRequestLogUsageFile(t, filepath.Join(authDir, "logs", "keys", "fallback-user", "pending.log"), []byte("1234"))

	handler := NewHandler(&config.Config{
		AuthDir: authDir,
		SDKConfig: config.SDKConfig{
			APIKeys:     []string{"fallback-secret", "configured-zero-secret"},
			APIKeyNames: []string{"fallback-user", "configured-zero"},
		},
	}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)
	if len(response.ParseErrors) != 0 {
		t.Fatalf("missing log-uploader.yaml should silently use fallback: %#v", response.ParseErrors)
	}
	if response.Totals.SourceCount != 2 || response.Totals.SourceBytes != 80 || response.Totals.PendingCount != 1 || response.Totals.PendingBytes != 4 {
		t.Fatalf("fallback totals = %+v", response.Totals)
	}
	if got := requestLogUsageKeyByName(t, response.Keys, "configured-zero"); !got.Configured || got.SourceCount != 0 {
		t.Fatalf("configured zero key = %+v", got)
	}
}

func TestGetRequestLogUsageMatchesSanitizedConfiguredName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	logsRoot := filepath.Join(root, "logs", "keys")
	workDir := filepath.Join(root, "work")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: logs/keys\nwork-dir: work\ntimezone: Asia/Shanghai\n"))
	hour := time.Date(2026, 7, 15, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	record := requestLogUsageAuditRecord{
		Status: "uploaded",
		Hour:   hour,
		KeyNames: map[string]requestLogUsageAuditKey{
			"张三-Mobile": requestLogUsageAuditKeyWithModel(2, 80, "gpt", 2, 80),
		},
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "audit.jsonl"), []byte(marshalRequestLogUsageAudit(t, record)+"\n"))
	mustWriteRequestLogUsageFile(t, filepath.Join(logsRoot, "张三-Mobile", "pending.log"), []byte("1234"))

	handler := NewHandler(&config.Config{
		AuthDir: filepath.Join(root, "auths"),
		SDKConfig: config.SDKConfig{
			APIKeys:     []string{"configured-secret"},
			APIKeyNames: []string{"张三 / Mobile"},
		},
	}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)
	if len(response.Keys) != 1 {
		t.Fatalf("sanitized configured name split into multiple rows: %+v", response.Keys)
	}
	key := response.Keys[0]
	if key.KeyName != "张三-Mobile" || key.DisplayName != "张三 / Mobile" || !key.Configured {
		t.Fatalf("configured name mapping = %+v", key)
	}
	if key.SourceCount != 2 || key.SourceBytes != 80 || key.PendingCount != 1 || key.PendingBytes != 4 {
		t.Fatalf("configured name totals = %+v", key)
	}
}

func TestGetRequestLogUsageMarksUnnamedConfiguredKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	apiKey := "unnamed-secret-must-not-appear"
	keyName := logging.APIKeyLogDirectory(apiKey)
	authDir := filepath.Join(root, "auths")
	hour := time.Date(2026, 7, 15, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	record := requestLogUsageAuditRecord{
		Status: "uploaded",
		Hour:   hour,
		KeyNames: map[string]requestLogUsageAuditKey{
			keyName: requestLogUsageAuditKeyWithModel(1, 40, "gpt", 1, 40),
		},
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(authDir, "log-uploader", "audit.jsonl"), []byte(marshalRequestLogUsageAudit(t, record)+"\n"))

	handler := NewHandler(&config.Config{
		AuthDir: authDir,
		SDKConfig: config.SDKConfig{
			APIKeys: []string{apiKey},
		},
	}, filepath.Join(root, "config.yaml"), nil)
	response, raw := performRequestLogUsage(t, handler)
	if strings.Contains(raw, apiKey) {
		t.Fatalf("response leaked unnamed API key: %s", raw)
	}
	if len(response.Keys) != 1 {
		t.Fatalf("unnamed configured key rows = %+v", response.Keys)
	}
	key := response.Keys[0]
	if key.KeyName != keyName || key.DisplayName != keyName || !key.Configured || key.SourceBytes != 40 {
		t.Fatalf("unnamed configured key = %+v", key)
	}
}

func TestGetRequestLogUsageInvalidFinalRecordRetainsCleanupPending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: logs\nwork-dir: work\ntimezone: Invalid/Timezone\n"))
	hour := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	lines := []string{
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded_cleanup_pending",
			Hour:   hour,
			KeyNames: map[string]requestLogUsageAuditKey{
				"safe": requestLogUsageAuditKeyWithModel(1, 10, "gpt", 1, 10),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "uploaded",
			Hour:   hour,
			KeyNames: map[string]requestLogUsageAuditKey{
				"safe": {SourceCount: -1, SourceBytes: 10},
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status: "dry_run",
			Hour:   hour.Add(time.Hour),
			KeyNames: map[string]requestLogUsageAuditKey{
				"ignored": requestLogUsageAuditKeyWithModel(50, 500, "gpt", 50, 500),
			},
		}),
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "audit.jsonl"), []byte(strings.Join(lines, "\n")+"\n"))

	handler := NewHandler(&config.Config{AuthDir: filepath.Join(root, "auths")}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)
	if response.Timezone != requestLogUsageDefaultTimezone {
		t.Fatalf("invalid timezone did not fall back: %q", response.Timezone)
	}
	if response.Totals.SourceCount != 1 || response.Totals.SourceBytes != 10 || response.Totals.BatchCount != 1 {
		t.Fatalf("cleanup-pending fallback was lost: %+v", response.Totals)
	}
	if len(response.ParseErrors) != 2 {
		t.Fatalf("expected timezone and invalid-final errors: %#v", response.ParseErrors)
	}
	if _, raw := performRequestLogUsage(t, handler); strings.Contains(raw, "ignored") {
		t.Fatalf("ignored status appeared in response: %s", raw)
	}
}

func TestGetRequestLogUsageMalformedUploaderConfigUsesFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: [unterminated\n"))
	hour := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	record := requestLogUsageAuditRecord{
		Status: "uploaded",
		Hour:   hour,
		KeyNames: map[string]requestLogUsageAuditKey{
			"fallback": requestLogUsageAuditKeyWithModel(1, 20, "gpt", 1, 20),
		},
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(authDir, "log-uploader", "audit.jsonl"), []byte(marshalRequestLogUsageAudit(t, record)+"\n"))

	handler := NewHandler(&config.Config{AuthDir: authDir}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)
	if response.Totals.SourceCount != 1 || len(response.ParseErrors) != 1 || !strings.Contains(response.ParseErrors[0], "parse log-uploader.yaml") {
		t.Fatalf("malformed uploader fallback failed: response=%+v errors=%#v", response.Totals, response.ParseErrors)
	}
}

func TestGetRequestLogUsageJSONLBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	mustWriteRequestLogUsageFile(t, filepath.Join(root, "log-uploader.yaml"), []byte("logs-root: logs/keys\nwork-dir: work\ntimezone: Asia/Shanghai\n"))

	hour1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	hour2 := hour1.Add(time.Hour)

	// Codex batch: source_bytes=500, jsonl_bytes=350 (normalization reduced size)
	// Fable5 batch: source_bytes=800, jsonl_bytes=800 (no normalization)
	lines := []string{
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:     "uploaded",
			Hour:       hour1,
			Provider:   "codex",
			JSONLBytes: 350,
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(5, 500, "gpt-5.6", 5, 500),
			},
		}),
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:     "uploaded",
			Hour:       hour1,
			Provider:   "fable5",
			JSONLBytes: 800,
			KeyNames: map[string]requestLogUsageAuditKey{
				"alice": requestLogUsageAuditKeyWithModel(8, 800, "claude-fable-5", 8, 800),
			},
		}),
		// Second codex batch in a different hour; jsonl_bytes=200
		marshalRequestLogUsageAudit(t, requestLogUsageAuditRecord{
			Status:     "uploaded",
			Hour:       hour2,
			Provider:   "codex",
			JSONLBytes: 200,
			KeyNames: map[string]requestLogUsageAuditKey{
				"bob": requestLogUsageAuditKeyWithModel(3, 300, "gpt-5.6", 3, 300),
			},
		}),
	}
	mustWriteRequestLogUsageFile(t, filepath.Join(workDir, "audit.jsonl"), []byte(strings.Join(lines, "\n")+"\n"))

	handler := NewHandler(&config.Config{AuthDir: filepath.Join(root, "auths")}, filepath.Join(root, "config.yaml"), nil)
	response, _ := performRequestLogUsage(t, handler)

	// Totals: source_bytes = 500+800+300 = 1600, jsonl_bytes = 350+800+200 = 1350
	if response.Totals.SourceBytes != 1600 {
		t.Fatalf("totals source_bytes = %d, want 1600", response.Totals.SourceBytes)
	}
	if response.Totals.JSONLBytes != 1350 {
		t.Fatalf("totals jsonl_bytes = %d, want 1350", response.Totals.JSONLBytes)
	}

	// Provider summaries
	if len(response.Providers) != 2 {
		t.Fatalf("providers count = %d, want 2", len(response.Providers))
	}
	codexProvider := response.Providers[0]
	if codexProvider.Provider != "codex" {
		t.Fatalf("first provider = %q, want codex", codexProvider.Provider)
	}
	if codexProvider.SourceBytes != 800 || codexProvider.JSONLBytes != 550 {
		t.Fatalf("codex provider: source=%d jsonl=%d, want 800/550", codexProvider.SourceBytes, codexProvider.JSONLBytes)
	}
	fable5Provider := response.Providers[1]
	if fable5Provider.Provider != "fable5" {
		t.Fatalf("second provider = %q, want fable5", fable5Provider.Provider)
	}
	if fable5Provider.SourceBytes != 800 || fable5Provider.JSONLBytes != 800 {
		t.Fatalf("fable5 provider: source=%d jsonl=%d, want 800/800", fable5Provider.SourceBytes, fable5Provider.JSONLBytes)
	}

	// Days: both hours are on the same day
	if len(response.Days) != 1 {
		t.Fatalf("days count = %d, want 1", len(response.Days))
	}
	if response.Days[0].SourceBytes != 1600 || response.Days[0].JSONLBytes != 1350 {
		t.Fatalf("day: source=%d jsonl=%d, want 1600/1350", response.Days[0].SourceBytes, response.Days[0].JSONLBytes)
	}

	// Hours
	if len(response.Hours) != 3 {
		t.Fatalf("hours count = %d, want 3", len(response.Hours))
	}
	for _, h := range response.Hours {
		if h.Provider == "codex" && h.Hour == "2026-08-01T10:00:00+08:00" {
			if h.JSONLBytes != 350 {
				t.Fatalf("hour1 codex jsonl_bytes = %d, want 350", h.JSONLBytes)
			}
		}
		if h.Provider == "codex" && h.Hour == "2026-08-01T11:00:00+08:00" {
			if h.JSONLBytes != 200 {
				t.Fatalf("hour2 codex jsonl_bytes = %d, want 200", h.JSONLBytes)
			}
		}
	}

	// JSONLBytesMeaning should be set
	if response.JSONLBytesMeaning == "" {
		t.Fatal("jsonl_bytes_meaning is empty")
	}
}

func performRequestLogUsage(t *testing.T, handler *Handler) (requestLogUsageResponse, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/request-log-usage", nil)
	handler.GetRequestLogUsage(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var response requestLogUsageResponse
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v body=%s", errUnmarshal, recorder.Body.String())
	}
	return response, recorder.Body.String()
}

func requestLogUsageAuditKeyWithModel(sourceCount, sourceBytes int64, model string, modelCount, modelBytes int64) requestLogUsageAuditKey {
	return requestLogUsageAuditKey{
		SourceCount: sourceCount,
		SourceBytes: sourceBytes,
		Models: map[string]requestLogUsageAuditModel{
			model: {SourceCount: modelCount, SourceBytes: modelBytes},
		},
	}
}

func marshalRequestLogUsageAudit(t *testing.T, record requestLogUsageAuditRecord) string {
	t.Helper()
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatalf("marshal audit fixture: %v", errMarshal)
	}
	return string(raw)
}

func mustWriteRequestLogUsageFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o750); errMkdir != nil {
		t.Fatalf("create fixture directory: %v", errMkdir)
	}
	if errWrite := os.WriteFile(path, data, 0o640); errWrite != nil {
		t.Fatalf("write fixture: %v", errWrite)
	}
}

func requestLogUsageKeyByName(t *testing.T, keys []requestLogUsageKey, name string) requestLogUsageKey {
	t.Helper()
	for _, key := range keys {
		if key.KeyName == name {
			return key
		}
	}
	t.Fatalf("missing key %q in %+v", name, keys)
	return requestLogUsageKey{}
}
