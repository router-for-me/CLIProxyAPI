// Package logqa implements a standalone, read-only quality checker for
// unuploaded request logs. It never acquires log-uploader locks and never
// mutates source logs or uploader state.
package logqa

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config defines the standalone log QA service.
type Config struct {
	LogsRoot    string            `yaml:"logs-root"`
	WorkDir     string            `yaml:"work-dir"`
	Timezone    string            `yaml:"timezone"`
	Schedule    ScheduleConfig    `yaml:"schedule"`
	Scan        ScanConfig        `yaml:"scan"`
	Rules       RulesConfig       `yaml:"rules"`
	Aggregation AggregationConfig `yaml:"aggregation"`
	Report      ReportConfig      `yaml:"report"`
	Location    *time.Location    `yaml:"-"`
	ConfigDir   string            `yaml:"-"`
	ConfigPath  string            `yaml:"-"`
}

type ScheduleConfig struct {
	Interval        time.Duration `yaml:"-"`
	IntervalRaw     string        `yaml:"interval"`
	RunOnStart      bool          `yaml:"run-on-start"`
	InitialDelay    time.Duration `yaml:"-"`
	InitialDelayRaw string        `yaml:"initial-delay"`
}

type ScanConfig struct {
	MinFileAge         time.Duration `yaml:"-"`
	MinFileAgeRaw      string        `yaml:"min-file-age"`
	SkipCurrentHour    bool          `yaml:"skip-current-hour"`
	MaxFilesPerRun     int           `yaml:"max-files-per-run"`
	MaxBytesPerRun     int64         `yaml:"-"`
	MaxBytesPerRunRaw  string        `yaml:"max-bytes-per-run"`
	MaxFileConcurrency int           `yaml:"max-file-concurrency"`
	MaxFileSize        int64         `yaml:"-"`
	MaxFileSizeRaw     string        `yaml:"max-file-size"`
}

type RulesConfig struct {
	MinPromptRounds            int  `yaml:"min-prompt-rounds"`
	RequireToolCall            bool `yaml:"require-tool-call"`
	RequireSessionID           bool `yaml:"require-session-id"`
	RequireEndsWithoutToolCall bool `yaml:"require-ends-without-tool-call"`
	RejectUnpairedToolCalls    bool `yaml:"reject-unpaired-tool-calls"`
	RejectDuplicateAssistant   bool `yaml:"reject-duplicate-assistant"`
	ExcludeIDEContext          bool `yaml:"exclude-ide-context"`
	ExcludeEnvContext          bool `yaml:"exclude-env-context"`
	ExcludeTitleSummary        bool `yaml:"exclude-title-summary"`
}

type AggregationConfig struct {
	Key      string `yaml:"key"`      // session_id
	Snapshot string `yaml:"snapshot"` // max_input_len
}

type ReportConfig struct {
	KeepRuns int `yaml:"keep-runs"`
}

// ConfigFromPaths builds a validated Config using defaults for the given paths.
// Prefer LoadConfig when a log-qa.yaml is available so scan/rules match production.
func ConfigFromPaths(logsRoot, workDir, timezone string) (Config, error) {
	cfg := Config{
		LogsRoot: logsRoot,
		WorkDir:  workDir,
		Timezone: timezone,
	}
	applyDefaults(&cfg)
	// Paths are already expected absolute (or will be used as-is by callers).
	if errValidate := cfg.Validate(); errValidate != nil {
		return Config{}, errValidate
	}
	return cfg, nil
}

// LoadConfig reads and validates a log-qa YAML configuration file.
func LoadConfig(path string) (Config, error) {
	absolutePath, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		return Config{}, fmt.Errorf("resolve log qa config path: %w", errAbsolute)
	}
	raw, errRead := os.ReadFile(absolutePath)
	if errRead != nil {
		return Config{}, fmt.Errorf("read log qa config: %w", errRead)
	}

	cfg := Config{}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if errDecode := decoder.Decode(&cfg); errDecode != nil {
		return Config{}, fmt.Errorf("parse log qa config: %w", errDecode)
	}
	cfg.ConfigPath = absolutePath
	cfg.ConfigDir = filepath.Dir(absolutePath)
	applyDefaults(&cfg)
	resolvePaths(&cfg, cfg.ConfigDir)
	if errValidate := cfg.Validate(); errValidate != nil {
		return Config{}, errValidate
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.LogsRoot) == "" {
		cfg.LogsRoot = filepath.Join("auths", "logs", "keys")
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		cfg.WorkDir = filepath.Join("auths", "log-qa")
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	if strings.TrimSpace(cfg.Schedule.IntervalRaw) == "" {
		cfg.Schedule.IntervalRaw = "30m"
	}
	if strings.TrimSpace(cfg.Schedule.InitialDelayRaw) == "" {
		cfg.Schedule.InitialDelayRaw = "12m"
	}
	if strings.TrimSpace(cfg.Scan.MinFileAgeRaw) == "" {
		cfg.Scan.MinFileAgeRaw = "10m"
	}
	// MaxFilesPerRun: 0 means unlimited (full scan). Do not rewrite 0 to a positive default.
	if strings.TrimSpace(cfg.Scan.MaxBytesPerRunRaw) == "" {
		// 0 means unlimited; empty string defaults to unlimited for full-scan deployments.
		cfg.Scan.MaxBytesPerRunRaw = "0"
	}
	if cfg.Scan.MaxFileConcurrency <= 0 {
		cfg.Scan.MaxFileConcurrency = 2
	}
	if strings.TrimSpace(cfg.Scan.MaxFileSizeRaw) == "" {
		// 0 means unlimited single-file size.
		cfg.Scan.MaxFileSizeRaw = "0"
	}
	if cfg.Rules.MinPromptRounds <= 0 {
		cfg.Rules.MinPromptRounds = 4
	}
	// Rule toggles default to the recommended production values.
	// Example YAML and tests should set them explicitly when they need false.
	if !cfg.Rules.RequireToolCall && !cfg.Rules.RequireSessionID && !cfg.Rules.RequireEndsWithoutToolCall &&
		!cfg.Rules.RejectUnpairedToolCalls && !cfg.Rules.RejectDuplicateAssistant &&
		!cfg.Rules.ExcludeIDEContext && !cfg.Rules.ExcludeEnvContext && !cfg.Rules.ExcludeTitleSummary {
		cfg.Rules.RequireToolCall = true
		cfg.Rules.RequireSessionID = true
		cfg.Rules.RequireEndsWithoutToolCall = true
		cfg.Rules.RejectUnpairedToolCalls = true
		cfg.Rules.RejectDuplicateAssistant = true
		cfg.Rules.ExcludeIDEContext = true
		cfg.Rules.ExcludeEnvContext = true
		cfg.Rules.ExcludeTitleSummary = true
	}
	// skip-current-hour: default true when all scan flags are zero-ish is hard;
	// production example sets true. Tests set as needed.

	if strings.TrimSpace(cfg.Aggregation.Key) == "" {
		cfg.Aggregation.Key = "session_id"
	}
	if strings.TrimSpace(cfg.Aggregation.Snapshot) == "" {
		cfg.Aggregation.Snapshot = "max_input_len"
	}
	if cfg.Report.KeepRuns <= 0 {
		cfg.Report.KeepRuns = 48
	}
}

func resolvePaths(cfg *Config, baseDir string) {
	if !filepath.IsAbs(cfg.LogsRoot) {
		cfg.LogsRoot = filepath.Join(baseDir, cfg.LogsRoot)
	}
	if !filepath.IsAbs(cfg.WorkDir) {
		cfg.WorkDir = filepath.Join(baseDir, cfg.WorkDir)
	}
}

// Validate parses durations and checks configuration consistency.
func (cfg *Config) Validate() error {
	interval, errInterval := time.ParseDuration(cfg.Schedule.IntervalRaw)
	if errInterval != nil || interval <= 0 {
		return fmt.Errorf("invalid schedule.interval %q", cfg.Schedule.IntervalRaw)
	}
	cfg.Schedule.Interval = interval

	initialDelay, errInitial := time.ParseDuration(cfg.Schedule.InitialDelayRaw)
	if errInitial != nil || initialDelay < 0 {
		return fmt.Errorf("invalid schedule.initial-delay %q", cfg.Schedule.InitialDelayRaw)
	}
	cfg.Schedule.InitialDelay = initialDelay

	minAge, errAge := time.ParseDuration(cfg.Scan.MinFileAgeRaw)
	if errAge != nil || minAge < 0 {
		return fmt.Errorf("invalid scan.min-file-age %q", cfg.Scan.MinFileAgeRaw)
	}
	cfg.Scan.MinFileAge = minAge

	maxBytes, errBytes := parseByteSize(cfg.Scan.MaxBytesPerRunRaw)
	if errBytes != nil || maxBytes < 0 {
		return fmt.Errorf("invalid scan.max-bytes-per-run %q: %w", cfg.Scan.MaxBytesPerRunRaw, errBytes)
	}
	// 0 = unlimited bytes per run
	cfg.Scan.MaxBytesPerRun = maxBytes

	maxFileSize, errFileSize := parseByteSize(cfg.Scan.MaxFileSizeRaw)
	if errFileSize != nil || maxFileSize < 0 {
		return fmt.Errorf("invalid scan.max-file-size %q: %w", cfg.Scan.MaxFileSizeRaw, errFileSize)
	}
	// 0 = unlimited single file size
	cfg.Scan.MaxFileSize = maxFileSize

	// MaxFilesPerRun: 0 = unlimited files per run (full scan)
	if cfg.Scan.MaxFilesPerRun < 0 {
		return fmt.Errorf("scan.max-files-per-run must be >= 0 (0 means unlimited)")
	}
	if cfg.Scan.MaxFileConcurrency <= 0 {
		return fmt.Errorf("scan.max-file-concurrency must be positive")
	}
	if cfg.Rules.MinPromptRounds <= 0 {
		return fmt.Errorf("rules.min-prompt-rounds must be positive")
	}
	if cfg.Aggregation.Key != "session_id" {
		return fmt.Errorf("aggregation.key must be session_id (got %q)", cfg.Aggregation.Key)
	}
	if cfg.Aggregation.Snapshot != "max_input_len" {
		return fmt.Errorf("aggregation.snapshot must be max_input_len (got %q)", cfg.Aggregation.Snapshot)
	}

	location, errLocation := time.LoadLocation(cfg.Timezone)
	if errLocation != nil {
		return fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, errLocation)
	}
	cfg.Location = location
	return nil
}

func parseByteSize(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("empty size")
	}
	// plain integer bytes
	var n int64
	if _, err := fmt.Sscanf(value, "%d", &n); err == nil && fmt.Sprintf("%d", n) == value {
		return n, nil
	}
	multipliers := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30},
		{"GB", 1_000_000_000},
		{"MiB", 1 << 20},
		{"MB", 1_000_000},
		{"KiB", 1 << 10},
		{"KB", 1000},
		{"B", 1},
		{"G", 1 << 30},
		{"M", 1 << 20},
		{"K", 1 << 10},
	}
	for _, item := range multipliers {
		if strings.HasSuffix(value, item.suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(value, item.suffix))
			var f float64
			if _, err := fmt.Sscanf(num, "%f", &f); err != nil {
				return 0, err
			}
			if f <= 0 {
				return 0, fmt.Errorf("size must be positive")
			}
			return int64(f * float64(item.mult)), nil
		}
	}
	return 0, fmt.Errorf("unrecognized size %q", value)
}
