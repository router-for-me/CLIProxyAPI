package loguploader

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestExampleConfigLoadsWithHourlyProductionDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "log-uploader.example.yaml")
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("load example config: %v", errLoad)
	}
	if cfg.Schedule.Interval != time.Hour || cfg.Schedule.SettleDelay != 5*time.Minute || cfg.Schedule.CatchUpDelay != 5*time.Minute {
		t.Errorf("schedule = interval %s, settle %s, catch-up %s", cfg.Schedule.Interval, cfg.Schedule.SettleDelay, cfg.Schedule.CatchUpDelay)
	}
	if cfg.Upload.Enabled {
		t.Errorf("example unexpectedly enables upload")
	}
	if !cfg.Schedule.RunOnStart {
		t.Errorf("example should scan completed historical hours on startup")
	}
	if !cfg.Retention.DeleteSourceAfterUpload || cfg.Retention.KeepLocalArchives {
		t.Errorf("unexpected production retention settings: delete_source=%t keep_archives=%t", cfg.Retention.DeleteSourceAfterUpload, cfg.Retention.KeepLocalArchives)
	}
	if cfg.Upload.Endpoint != "https://tos-cn-beijing.volces.com" || cfg.Upload.Bucket != "llm-d1" {
		t.Errorf("unexpected TOS target: endpoint=%q bucket=%q", cfg.Upload.Endpoint, cfg.Upload.Bucket)
	}
	if cfg.Supabase.Enabled {
		t.Errorf("example unexpectedly enables Supabase sync")
	}
	if cfg.Supabase.IngestURL != "https://ldhknedocspavyunogwt.supabase.co/functions/v1/ingest-log-usage" {
		t.Errorf("unexpected Supabase ingest URL: %q", cfg.Supabase.IngestURL)
	}
	if cfg.Supabase.IngestTokenEnv != "LOG_STATS_INGEST_TOKEN" {
		t.Errorf("unexpected Supabase token environment name: %q", cfg.Supabase.IngestTokenEnv)
	}
	if !filepath.IsAbs(cfg.LogsRoot) || !filepath.IsAbs(cfg.WorkDir) {
		t.Errorf("config paths were not resolved relative to the config file: logs=%q work=%q", cfg.LogsRoot, cfg.WorkDir)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "log-uploader.yaml")
	if errWrite := os.WriteFile(path, []byte("unknown-setting: true\n"), 0o600); errWrite != nil {
		t.Fatalf("write invalid config: %v", errWrite)
	}
	_, errLoad := LoadConfig(path)
	if errLoad == nil || !strings.Contains(errLoad.Error(), "unknown-setting") {
		t.Fatalf("unknown config field error = %v", errLoad)
	}
}

func TestLoadConfigAcceptsLegacyModelAliases(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "log-uploader.yaml")
	if errWrite := os.WriteFile(path, []byte("model-aliases:\n  gpt-5.6-sol: codex56sol\n"), 0o600); errWrite != nil {
		t.Fatalf("write legacy config: %v", errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("load config with legacy model aliases: %v", errLoad)
	}
	if cfg.Models["gpt-5.6-sol"] != "codex56sol" {
		t.Errorf("legacy model alias was not retained for config compatibility")
	}
}

func TestLoadConfigDefaultsSupabaseConfig(t *testing.T) {
	t.Parallel()

	cfg, errLoad := loadConfigYAML(t, "{}\n")
	if errLoad != nil {
		t.Fatalf("load default config: %v", errLoad)
	}
	if cfg.Supabase.Enabled {
		t.Errorf("Supabase upload is enabled by default")
	}
	if cfg.Supabase.IngestTokenEnv != "LOG_STATS_INGEST_TOKEN" {
		t.Errorf("supabase.ingest-token-env = %q, want LOG_STATS_INGEST_TOKEN", cfg.Supabase.IngestTokenEnv)
	}
}

func TestLoadConfigAcceptsEnabledSupabaseConfig(t *testing.T) {
	t.Parallel()

	cfg, errLoad := loadConfigYAML(t, `
supabase:
  enabled: true
  ingest-url: https://project-ref.supabase.co/functions/v1/log-stats-ingest
  ingest-token-env: CUSTOM_LOG_STATS_TOKEN
`)
	if errLoad != nil {
		t.Fatalf("load enabled Supabase config: %v", errLoad)
	}
	if !cfg.Supabase.Enabled {
		t.Errorf("Supabase upload is disabled")
	}
	if cfg.Supabase.IngestURL != "https://project-ref.supabase.co/functions/v1/log-stats-ingest" {
		t.Errorf("supabase.ingest-url = %q", cfg.Supabase.IngestURL)
	}
	if cfg.Supabase.IngestTokenEnv != "CUSTOM_LOG_STATS_TOKEN" {
		t.Errorf("supabase.ingest-token-env = %q", cfg.Supabase.IngestTokenEnv)
	}
}

func TestLoadConfigNormalizesSupabaseConfigWhitespace(t *testing.T) {
	t.Parallel()

	cfg, errLoad := loadConfigYAML(t, `
supabase:
  enabled: true
  ingest-url: "  https://project-ref.supabase.co/functions/v1/log-stats-ingest  "
  ingest-token-env: "  CUSTOM_LOG_STATS_TOKEN  "
`)
	if errLoad != nil {
		t.Fatalf("load Supabase config with surrounding whitespace: %v", errLoad)
	}
	if cfg.Supabase.IngestURL != "https://project-ref.supabase.co/functions/v1/log-stats-ingest" {
		t.Errorf("supabase.ingest-url = %q", cfg.Supabase.IngestURL)
	}
	if cfg.Supabase.IngestTokenEnv != "CUSTOM_LOG_STATS_TOKEN" {
		t.Errorf("supabase.ingest-token-env = %q", cfg.Supabase.IngestTokenEnv)
	}
}

func TestLoadConfigRejectsUnsafeSupabaseConfigIngestURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ingestURL string
	}{
		{name: "HTTP scheme", ingestURL: "http://project-ref.supabase.co/functions/v1/log-stats-ingest"},
		{name: "relative URL", ingestURL: "project-ref.supabase.co/functions/v1/log-stats-ingest"},
		{name: "missing host", ingestURL: "https:///functions/v1/log-stats-ingest"},
		{name: "credentials", ingestURL: "https://user:password@project-ref.supabase.co/functions/v1/log-stats-ingest"},
		{name: "query", ingestURL: "https://project-ref.supabase.co/functions/v1/log-stats-ingest?source=uploader"},
		{name: "fragment", ingestURL: "https://project-ref.supabase.co/functions/v1/log-stats-ingest#token"},
		{name: "non-function path", ingestURL: "https://project-ref.supabase.co/rest/v1/log-stats-ingest"},
		{name: "missing function name", ingestURL: "https://project-ref.supabase.co/functions/v1/"},
		{name: "nested function path", ingestURL: "https://project-ref.supabase.co/functions/v1/log-stats-ingest/admin"},
		{name: "encoded path separator", ingestURL: "https://project-ref.supabase.co/functions/v1/log%2Fstats-ingest"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, errLoad := loadConfigYAML(t, fmt.Sprintf(`
supabase:
  enabled: true
  ingest-url: %q
`, test.ingestURL))
			if errLoad == nil || !strings.Contains(errLoad.Error(), "supabase.ingest-url") {
				t.Fatalf("unsafe ingest URL error = %v", errLoad)
			}
		})
	}
}

func TestLoadConfigRejectsSupabaseConfigIngestURLWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ingestURL string
		secrets   []string
	}{
		{
			name:      "credentials",
			ingestURL: "https://leaky-user:leaky-password@project-ref.supabase.co/functions/v1/log-stats-ingest",
			secrets:   []string{"leaky-user", "leaky-password"},
		},
		{
			name:      "query",
			ingestURL: "https://project-ref.supabase.co/functions/v1/log-stats-ingest?token=leaky-query-token",
			secrets:   []string{"leaky-query-token"},
		},
		{
			name:      "fragment",
			ingestURL: "https://project-ref.supabase.co/functions/v1/log-stats-ingest#leaky-fragment-token",
			secrets:   []string{"leaky-fragment-token"},
		},
		{
			name:      "parse error",
			ingestURL: "https://project-ref.supabase.co/functions/v1/log%zz?token=leaky-parse-token",
			secrets:   []string{"leaky-parse-token"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, errLoad := loadConfigYAML(t, fmt.Sprintf(`
supabase:
  enabled: true
  ingest-url: %q
`, test.ingestURL))
			if errLoad == nil || !strings.Contains(errLoad.Error(), "supabase.ingest-url") {
				t.Fatalf("unsafe ingest URL error = %v", errLoad)
			}
			for _, secret := range test.secrets {
				if strings.Contains(errLoad.Error(), secret) {
					t.Errorf("error leaked sensitive URL content %q: %v", secret, errLoad)
				}
			}
		})
	}
}

func TestConfigValidateRejectsEmptySupabaseConfigTokenEnv(t *testing.T) {
	t.Parallel()

	cfg, errLoad := loadConfigYAML(t, `
supabase:
  enabled: true
  ingest-url: https://project-ref.supabase.co/functions/v1/log-stats-ingest
`)
	if errLoad != nil {
		t.Fatalf("load enabled Supabase config: %v", errLoad)
	}
	cfg.Supabase.IngestTokenEnv = " \t"

	errValidate := cfg.Validate()
	if errValidate == nil || !strings.Contains(errValidate.Error(), "supabase.ingest-token-env") {
		t.Fatalf("empty token environment name error = %v", errValidate)
	}
}

func TestSupabaseConfigContainsOnlySafeFields(t *testing.T) {
	t.Parallel()

	typeOfConfig := reflect.TypeOf(SupabaseConfig{})
	wantFields := map[string]string{
		"Enabled":        "enabled",
		"IngestURL":      "ingest-url",
		"IngestTokenEnv": "ingest-token-env",
	}
	if typeOfConfig.NumField() != len(wantFields) {
		t.Fatalf("SupabaseConfig has %d fields, want exactly %d safe fields", typeOfConfig.NumField(), len(wantFields))
	}
	for index := 0; index < typeOfConfig.NumField(); index++ {
		field := typeOfConfig.Field(index)
		wantYAMLTag, ok := wantFields[field.Name]
		if !ok {
			t.Errorf("SupabaseConfig exposes unexpected field %q", field.Name)
			continue
		}
		if field.Tag.Get("yaml") != wantYAMLTag {
			t.Errorf("SupabaseConfig.%s YAML tag = %q, want %q", field.Name, field.Tag.Get("yaml"), wantYAMLTag)
		}
	}
}

func TestLoadConfigRejectsInlineSupabaseConfigToken(t *testing.T) {
	t.Parallel()

	_, errLoad := loadConfigYAML(t, `
supabase:
  enabled: false
  ingest-token: must-not-be-configurable
`)
	if errLoad == nil || !strings.Contains(errLoad.Error(), "ingest-token") {
		t.Fatalf("inline Supabase token error = %v", errLoad)
	}
}

func TestSupabaseConfigDoesNotLoadTokenValue(t *testing.T) {
	const tokenValue = "top-secret-ingest-token"
	t.Setenv("LOG_STATS_INGEST_TOKEN", tokenValue)

	cfg, errLoad := loadConfigYAML(t, `
supabase:
  enabled: true
  ingest-url: https://project-ref.supabase.co/functions/v1/log-stats-ingest
`)
	if errLoad != nil {
		t.Fatalf("load enabled Supabase config: %v", errLoad)
	}
	if strings.Contains(cfg.Supabase.IngestURL, tokenValue) || strings.Contains(cfg.Supabase.IngestTokenEnv, tokenValue) {
		t.Fatalf("Supabase config contains the token value")
	}

	raw, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("marshal config: %v", errMarshal)
	}
	if strings.Contains(string(raw), tokenValue) {
		t.Fatalf("marshaled YAML contains the token value")
	}
}

func loadConfigYAML(t *testing.T, raw string) (Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "log-uploader.yaml")
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	return LoadConfig(path)
}
