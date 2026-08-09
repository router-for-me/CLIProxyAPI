package loguploader

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config defines the standalone log uploader service.
type Config struct {
	LogsRoot    string            `yaml:"logs-root"`
	WorkDir     string            `yaml:"work-dir"`
	Timezone    string            `yaml:"timezone"`
	Schedule    ScheduleConfig    `yaml:"schedule"`
	Upload      UploadConfig      `yaml:"upload"`
	Supabase    SupabaseConfig    `yaml:"supabase"`
	Retention   RetentionConfig   `yaml:"retention"`
	SessionGate SessionGateConfig `yaml:"session-gate"`
	// Models is retained for backward-compatible config parsing.
	// Deprecated: Hourly archives always use the fixed archive name label.
	Models map[string]string `yaml:"model-aliases"`
}

// SessionGateConfig filters which settled request logs may enter an upload batch.
// When disabled, the uploader behaves as before (all settled logs are eligible).
type SessionGateConfig struct {
	Enabled                    bool          `yaml:"enabled"`
	MinPromptRounds            int           `yaml:"min-prompt-rounds"`
	RequireToolCall            bool          `yaml:"require-tool-call"`
	RequireSessionID           bool          `yaml:"require-session-id"`
	RequireEndsWithoutToolCall bool          `yaml:"require-ends-without-tool-call"`
	RejectUnpairedToolCalls    bool          `yaml:"reject-unpaired-tool-calls"`
	ExcludeTitleSummary        bool          `yaml:"exclude-title-summary"`
	ExcludeIDEContext          bool          `yaml:"exclude-ide-context"`
	ExcludeEnvContext          bool          `yaml:"exclude-env-context"`
	MaxHoldAge                 time.Duration `yaml:"-"`
	MaxHoldAgeRaw              string        `yaml:"max-hold-age"`
	MaxAbsoluteAge             time.Duration `yaml:"-"`
	MaxAbsoluteAgeRaw          string        `yaml:"max-absolute-age"`
	// DeferredPackaging is always eligibility-hour in MVP (kept for config clarity).
	DeferredPackaging string `yaml:"deferred-packaging"`
}

type ScheduleConfig struct {
	Interval     time.Duration `yaml:"-"`
	IntervalRaw  string        `yaml:"interval"`
	RunOnStart   bool          `yaml:"run-on-start"`
	SettleDelay  time.Duration `yaml:"-"`
	SettleRaw    string        `yaml:"settle-delay"`
	CatchUpDelay time.Duration `yaml:"-"`
	CatchUpRaw   string        `yaml:"catch-up-delay"`
}

type UploadConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Endpoint           string `yaml:"endpoint"`
	Region             string `yaml:"region"`
	Bucket             string `yaml:"bucket"`
	ObjectPrefix       string `yaml:"object-prefix"`
	AccessKeyIDEnv     string `yaml:"access-key-id-env"`
	SecretAccessKeyEnv string `yaml:"secret-access-key-env"`
	SessionTokenEnv    string `yaml:"session-token-env"`
}

type SupabaseConfig struct {
	Enabled        bool   `yaml:"enabled"`
	IngestURL      string `yaml:"ingest-url"`
	IngestTokenEnv string `yaml:"ingest-token-env"`
}

type RetentionConfig struct {
	DeleteSourceAfterUpload bool `yaml:"delete-source-after-upload"`
	KeepLocalArchives       bool `yaml:"keep-local-archives"`
}

func LoadConfig(path string) (Config, error) {
	absolutePath, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		return Config{}, fmt.Errorf("resolve log uploader config path: %w", errAbsolute)
	}
	raw, errRead := os.ReadFile(absolutePath)
	if errRead != nil {
		return Config{}, fmt.Errorf("read log uploader config: %w", errRead)
	}

	cfg := Config{}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if errUnmarshal := decoder.Decode(&cfg); errUnmarshal != nil {
		return Config{}, fmt.Errorf("parse log uploader config: %w", errUnmarshal)
	}
	applyConfigDefaults(&cfg)
	resolveConfigPaths(&cfg, filepath.Dir(absolutePath))
	if errValidate := cfg.Validate(); errValidate != nil {
		return Config{}, errValidate
	}
	return cfg, nil
}

func resolveConfigPaths(cfg *Config, baseDir string) {
	if !filepath.IsAbs(cfg.LogsRoot) {
		cfg.LogsRoot = filepath.Join(baseDir, cfg.LogsRoot)
	}
	if !filepath.IsAbs(cfg.WorkDir) {
		cfg.WorkDir = filepath.Join(baseDir, cfg.WorkDir)
	}
}

func applyConfigDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.LogsRoot) == "" {
		cfg.LogsRoot = filepath.Join("auths", "logs", "keys")
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		cfg.WorkDir = filepath.Join("auths", "log-uploader")
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	if strings.TrimSpace(cfg.Schedule.IntervalRaw) == "" {
		cfg.Schedule.IntervalRaw = "1h"
	}
	if strings.TrimSpace(cfg.Schedule.SettleRaw) == "" {
		cfg.Schedule.SettleRaw = "5m"
	}
	if strings.TrimSpace(cfg.Schedule.CatchUpRaw) == "" {
		cfg.Schedule.CatchUpRaw = "5m"
	}
	if strings.TrimSpace(cfg.Upload.Endpoint) == "" {
		cfg.Upload.Endpoint = "https://tos-cn-beijing.volces.com"
	}
	if strings.TrimSpace(cfg.Upload.Region) == "" {
		cfg.Upload.Region = "cn-beijing"
	}
	if strings.TrimSpace(cfg.Upload.ObjectPrefix) == "" {
		cfg.Upload.ObjectPrefix = "cliproxy-logs"
	}
	if strings.TrimSpace(cfg.Upload.AccessKeyIDEnv) == "" {
		cfg.Upload.AccessKeyIDEnv = "VOLC_TOS_ACCESS_KEY_ID"
	}
	if strings.TrimSpace(cfg.Upload.SecretAccessKeyEnv) == "" {
		cfg.Upload.SecretAccessKeyEnv = "VOLC_TOS_SECRET_ACCESS_KEY"
	}
	if strings.TrimSpace(cfg.Upload.SessionTokenEnv) == "" {
		cfg.Upload.SessionTokenEnv = "VOLC_TOS_SESSION_TOKEN"
	}
	if strings.TrimSpace(cfg.Supabase.IngestTokenEnv) == "" {
		cfg.Supabase.IngestTokenEnv = "LOG_STATS_INGEST_TOKEN"
	}
	applySessionGateDefaults(&cfg.SessionGate)
}

func applySessionGateDefaults(gate *SessionGateConfig) {
	if gate.MinPromptRounds <= 0 {
		gate.MinPromptRounds = 4
	}
	if strings.TrimSpace(gate.MaxHoldAgeRaw) == "" {
		gate.MaxHoldAgeRaw = "48h"
	}
	if strings.TrimSpace(gate.MaxAbsoluteAgeRaw) == "" {
		gate.MaxAbsoluteAgeRaw = "168h"
	}
	if strings.TrimSpace(gate.DeferredPackaging) == "" {
		gate.DeferredPackaging = "eligibility-hour"
	}
	// Recommended production toggles when every flag is still zero (omitted in YAML).
	// Explicit false requires at least one true flag elsewhere, or set after LoadConfig.
	// Example YAML always sets each flag explicitly.
	if !gate.RequireToolCall && !gate.RequireSessionID && !gate.RequireEndsWithoutToolCall &&
		!gate.RejectUnpairedToolCalls && !gate.ExcludeTitleSummary && !gate.ExcludeIDEContext && !gate.ExcludeEnvContext {
		gate.RequireToolCall = true
		gate.RequireSessionID = true
		gate.RequireEndsWithoutToolCall = true
		gate.RejectUnpairedToolCalls = true
		gate.ExcludeTitleSummary = true
		gate.ExcludeIDEContext = true
		gate.ExcludeEnvContext = true
	}
}

func (cfg *Config) Validate() error {
	interval, errInterval := time.ParseDuration(cfg.Schedule.IntervalRaw)
	if errInterval != nil || interval <= 0 {
		return fmt.Errorf("invalid schedule.interval %q", cfg.Schedule.IntervalRaw)
	}
	cfg.Schedule.Interval = interval

	settleDelay, errSettle := time.ParseDuration(cfg.Schedule.SettleRaw)
	if errSettle != nil || settleDelay < 0 {
		return fmt.Errorf("invalid schedule.settle-delay %q", cfg.Schedule.SettleRaw)
	}
	if settleDelay >= interval {
		return fmt.Errorf("schedule.settle-delay must be shorter than schedule.interval")
	}
	cfg.Schedule.SettleDelay = settleDelay

	catchUpDelay, errCatchUp := time.ParseDuration(cfg.Schedule.CatchUpRaw)
	if errCatchUp != nil || catchUpDelay <= 0 {
		return fmt.Errorf("invalid schedule.catch-up-delay %q", cfg.Schedule.CatchUpRaw)
	}
	cfg.Schedule.CatchUpDelay = catchUpDelay

	if _, errLocation := time.LoadLocation(cfg.Timezone); errLocation != nil {
		return fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, errLocation)
	}

	if errGate := cfg.SessionGate.validate(); errGate != nil {
		return errGate
	}
	if errSupabase := cfg.Supabase.validate(); errSupabase != nil {
		return errSupabase
	}

	if !cfg.Upload.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.Upload.Endpoint) == "" {
		return fmt.Errorf("upload.endpoint is required")
	}
	if strings.TrimSpace(cfg.Upload.Region) == "" {
		return fmt.Errorf("upload.region is required")
	}
	if strings.TrimSpace(cfg.Upload.Bucket) == "" {
		return fmt.Errorf("upload.bucket is required")
	}
	return nil
}

func (supabase *SupabaseConfig) validate() error {
	supabase.IngestURL = strings.TrimSpace(supabase.IngestURL)
	supabase.IngestTokenEnv = strings.TrimSpace(supabase.IngestTokenEnv)
	if !supabase.Enabled {
		return nil
	}
	if supabase.IngestTokenEnv == "" {
		return fmt.Errorf("supabase.ingest-token-env is required")
	}

	rawURL := supabase.IngestURL
	parsedURL, errParse := url.Parse(rawURL)
	if errParse != nil {
		return fmt.Errorf("invalid supabase.ingest-url: malformed URL")
	}
	if !parsedURL.IsAbs() || !strings.EqualFold(parsedURL.Scheme, "https") || parsedURL.Hostname() == "" {
		return fmt.Errorf("invalid supabase.ingest-url: must be an absolute HTTPS URL")
	}
	if parsedURL.User != nil {
		return fmt.Errorf("invalid supabase.ingest-url: credentials are not allowed")
	}
	if parsedURL.RawQuery != "" || parsedURL.ForceQuery {
		return fmt.Errorf("invalid supabase.ingest-url: query parameters are not allowed")
	}
	if parsedURL.Fragment != "" || strings.Contains(rawURL, "#") {
		return fmt.Errorf("invalid supabase.ingest-url: fragments are not allowed")
	}

	const edgeFunctionPrefix = "/functions/v1/"
	functionName := strings.TrimPrefix(parsedURL.Path, edgeFunctionPrefix)
	if parsedURL.RawPath != "" || functionName == parsedURL.Path || functionName == "" || strings.Contains(functionName, "/") {
		return fmt.Errorf("invalid supabase.ingest-url: path must be /functions/v1/<function-name>")
	}
	for _, character := range functionName {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return fmt.Errorf("invalid supabase.ingest-url: function name contains unsupported characters")
		}
	}
	return nil
}

func (gate *SessionGateConfig) validate() error {
	if strings.TrimSpace(gate.MaxHoldAgeRaw) == "" {
		gate.MaxHoldAgeRaw = "48h"
	}
	holdAge, errHold := time.ParseDuration(gate.MaxHoldAgeRaw)
	if errHold != nil || holdAge <= 0 {
		return fmt.Errorf("invalid session-gate.max-hold-age %q", gate.MaxHoldAgeRaw)
	}
	gate.MaxHoldAge = holdAge

	if strings.TrimSpace(gate.MaxAbsoluteAgeRaw) == "" {
		gate.MaxAbsoluteAgeRaw = "168h"
	}
	absAge, errAbs := time.ParseDuration(gate.MaxAbsoluteAgeRaw)
	if errAbs != nil || absAge <= 0 {
		return fmt.Errorf("invalid session-gate.max-absolute-age %q", gate.MaxAbsoluteAgeRaw)
	}
	gate.MaxAbsoluteAge = absAge

	if gate.MinPromptRounds <= 0 {
		gate.MinPromptRounds = 4
	}
	switch strings.TrimSpace(gate.DeferredPackaging) {
	case "", "eligibility-hour":
		gate.DeferredPackaging = "eligibility-hour"
	default:
		return fmt.Errorf("invalid session-gate.deferred-packaging %q (only eligibility-hour is supported)", gate.DeferredPackaging)
	}
	return nil
}
