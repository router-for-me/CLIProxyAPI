package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigBytesRequestLogSummaryPolicy(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("request-log: true\nrequest-log-success-summary: true\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.RequestLogSuccessSummary {
		t.Fatal("RequestLogSuccessSummary = false, want true")
	}
	if cfg.RequestLogSummaryRotationHours != DefaultRequestLogSummaryRotationHours {
		t.Fatalf("RequestLogSummaryRotationHours = %d, want %d", cfg.RequestLogSummaryRotationHours, DefaultRequestLogSummaryRotationHours)
	}
	if cfg.RequestLogSummaryMaxFiles != DefaultRequestLogSummaryMaxFiles {
		t.Fatalf("RequestLogSummaryMaxFiles = %d, want %d", cfg.RequestLogSummaryMaxFiles, DefaultRequestLogSummaryMaxFiles)
	}
}

func TestParseConfigBytesRequestLogSummaryPolicyOverrides(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("request-log-summary-rotation-hours: 2\nrequest-log-summary-max-files: 0\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.RequestLogSummaryRotationHours != 2 {
		t.Fatalf("RequestLogSummaryRotationHours = %d, want 2", cfg.RequestLogSummaryRotationHours)
	}
	if cfg.RequestLogSummaryMaxFiles != 0 {
		t.Fatalf("RequestLogSummaryMaxFiles = %d, want 0", cfg.RequestLogSummaryMaxFiles)
	}
}

func TestSaveConfigPreserveCommentsDoesNotAddRequestLogSummaryDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	rendered, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	for _, key := range []string{"request-log-summary-rotation-hours:", "request-log-summary-max-files:"} {
		if strings.Contains(string(rendered), key) {
			t.Fatalf("saved config unexpectedly contains default key %q:\n%s", key, rendered)
		}
	}
}

func TestParseConfigBytesCodexClaudeCacheWriteEstimateIsOptIn(t *testing.T) {
	defaults, errParse := ParseConfigBytes([]byte("{}\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes(defaults) error = %v", errParse)
	}
	if defaults.CodexClaudeEstimateCacheWriteUsage {
		t.Fatal("CodexClaudeEstimateCacheWriteUsage default = true, want false")
	}

	enabled, errParse := ParseConfigBytes([]byte("codex-claude-estimate-cache-write-usage: true\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes(enabled) error = %v", errParse)
	}
	if !enabled.CodexClaudeEstimateCacheWriteUsage {
		t.Fatal("CodexClaudeEstimateCacheWriteUsage = false, want true")
	}
}
