package apikeyusage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	_ "modernc.org/sqlite"
)

const (
	periodWeek    = "week"
	periodMonth   = "month"
	pruneInterval = 24 * time.Hour
)

var ErrDisabled = errors.New("per-key usage accounting is disabled")

// UsageTotals contains the counters stored for one period.
type UsageTotals struct {
	Requests        int64 `json:"requests"`
	Successes       int64 `json:"successes"`
	Failures        int64 `json:"failures"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// ProfileUsage combines configured policy data with one period's counters.
type ProfileUsage struct {
	ID                string             `json:"id"`
	Name              string             `json:"name"`
	KeyFingerprint    string             `json:"key_fingerprint"`
	Disabled          bool               `json:"disabled"`
	AllowedModels     []string           `json:"allowed_models,omitempty"`
	Limit             config.APIKeyLimit `json:"limit"`
	Usage             UsageTotals        `json:"usage"`
	RemainingRequests int64              `json:"remaining_requests"`
	RemainingTokens   int64              `json:"remaining_tokens"`
}

// ModelUsage is an aggregate of persisted usage records for one model.
type ModelUsage struct {
	ProfileID    string `json:"profile_id"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Calls        int64  `json:"calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// Summary is returned to the management dashboard.
type Summary struct {
	Period   string         `json:"period"`
	Timezone string         `json:"timezone"`
	Start    time.Time      `json:"start"`
	End      time.Time      `json:"end"`
	Totals   UsageTotals    `json:"totals"`
	Profiles []ProfileUsage `json:"profiles"`
	Models   []ModelUsage   `json:"models"`
}

// Event is one completed upstream usage record. API key plaintext is never stored.
type Event struct {
	ID              int64     `json:"id"`
	ProfileID       string    `json:"profile_id"`
	KeyFingerprint  string    `json:"key_fingerprint"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	RequestedAt     time.Time `json:"requested_at"`
	Failed          bool      `json:"failed"`
	StatusCode      int       `json:"status_code"`
	LatencyMS       int64     `json:"latency_ms"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
	CachedTokens    int64     `json:"cached_tokens"`
	TotalTokens     int64     `json:"total_tokens"`
}

// EventsPage contains a bounded page of usage records.
type EventsPage struct {
	Events []Event `json:"events"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
	Total  int64   `json:"total"`
}

type periodRef struct {
	kind  string
	start int64
	end   time.Time
}

// Reservation represents an accepted downstream generation request.
type Reservation struct {
	keyHash     string
	profileID   string
	profileName string
	periods     [2]periodRef
	tracked     bool
}

// Decision describes whether a request may proceed and why it was denied.
type Decision struct {
	Allowed           bool
	Code              string
	Message           string
	Period            string
	Limit             int64
	Used              int64
	RemainingRequests int64
	RemainingTokens   int64
	ResetAt           time.Time
	Reservation       Reservation
}

// Service owns the persistent counters and the active profile snapshot.
type Service struct {
	configPath string

	mu            sync.RWMutex
	enabled       bool
	settings      config.APIKeyUsageConfig
	profilesByKey map[string]config.APIKeyProfile
	profilesByID  map[string]config.APIKeyProfile
	location      *time.Location

	dbMu sync.RWMutex
	db   *sql.DB
	path string

	pruneMu   sync.Mutex
	lastPrune time.Time
}

// New creates a per-key usage service and opens its database when enabled.
func New(configPath string, cfg *config.Config) (*Service, error) {
	s := &Service{configPath: strings.TrimSpace(configPath), location: time.UTC}
	if err := s.UpdateConfig(cfg); err != nil {
		return s, err
	}
	return s, nil
}

// UpdateConfig atomically replaces policies and reopens the database when its path changes.
func (s *Service) UpdateConfig(cfg *config.Config) error {
	if s == nil {
		return nil
	}
	settings := config.APIKeyUsageConfig{}
	profiles := []config.APIKeyProfile(nil)
	if cfg != nil {
		settings = cfg.APIKeyUsage
		profiles = append(profiles, cfg.APIKeyProfiles...)
	}
	if settings.RetentionDays <= 0 {
		settings.RetentionDays = config.DefaultAPIKeyUsageRetentionDays
	}
	if strings.TrimSpace(settings.Timezone) == "" {
		settings.Timezone = config.DefaultAPIKeyUsageTimezone
	}
	location, errLocation := time.LoadLocation(settings.Timezone)
	if errLocation != nil {
		location = time.UTC
		settings.Timezone = config.DefaultAPIKeyUsageTimezone
	}

	byKey := make(map[string]config.APIKeyProfile, len(profiles))
	byID := make(map[string]config.APIKeyProfile, len(profiles))
	for i := range profiles {
		profile := profiles[i]
		if strings.TrimSpace(profile.APIKey) == "" {
			continue
		}
		profile.AllowedModels = append([]string(nil), profile.AllowedModels...)
		byKey[profile.APIKey] = profile
		byID[profile.ID] = profile
	}

	databasePath := ""
	if settings.Enabled {
		databasePath = s.resolveDatabasePath(settings.DatabasePath)
	}

	s.mu.Lock()
	s.enabled = settings.Enabled
	s.settings = settings
	s.profilesByKey = byKey
	s.profilesByID = byID
	s.location = location
	s.mu.Unlock()

	if !settings.Enabled {
		s.swapDatabase(nil, "")
		return nil
	}

	s.dbMu.RLock()
	sameDatabase := s.db != nil && s.path == databasePath
	s.dbMu.RUnlock()
	if sameDatabase {
		return nil
	}

	db, errOpen := openDatabase(databasePath, settings.RetentionDays)
	if errOpen != nil {
		s.swapDatabase(nil, "")
		return errOpen
	}
	s.swapDatabase(db, databasePath)
	return nil
}

func (s *Service) resolveDatabasePath(configured string) string {
	configured = strings.TrimSpace(configured)
	base := "."
	if s.configPath != "" {
		base = filepath.Dir(s.configPath)
	}
	if configured == "" {
		return filepath.Join(base, "api-key-usage.db")
	}
	configured = filepath.Clean(configured)
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(base, configured)
}

func openDatabase(path string, retentionDays int) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("api-key usage database path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create api-key usage database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open api-key usage database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure api-key usage database: %w", err)
	}
	if _, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS period_usage (
			key_hash TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			period_kind TEXT NOT NULL,
			period_start INTEGER NOT NULL,
			requests INTEGER NOT NULL DEFAULT 0,
			successes INTEGER NOT NULL DEFAULT 0,
			failures INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (profile_id, period_kind, period_start)
		);
		CREATE INDEX IF NOT EXISTS idx_period_usage_period ON period_usage(period_kind, period_start);
		CREATE TABLE IF NOT EXISTS usage_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key_hash TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			requested_at INTEGER NOT NULL,
			failed INTEGER NOT NULL,
			status_code INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_usage_events_time ON usage_events(requested_at DESC);
		CREATE INDEX IF NOT EXISTS idx_usage_events_profile_time ON usage_events(profile_id, requested_at DESC);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize api-key usage database: %w", err)
	}
	if err = pruneExpired(context.Background(), db, retentionDays, time.Now()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prune api-key usage database: %w", err)
	}
	return db, nil
}

func pruneExpired(ctx context.Context, db *sql.DB, retentionDays int, now time.Time) error {
	if db == nil || retentionDays <= 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.AddDate(0, 0, -retentionDays).Unix()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DELETE FROM usage_events WHERE requested_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM period_usage WHERE period_start < ?`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) swapDatabase(next *sql.DB, path string) {
	s.dbMu.Lock()
	previous := s.db
	s.db = next
	s.path = path
	s.dbMu.Unlock()
	s.pruneMu.Lock()
	s.lastPrune = time.Time{}
	s.pruneMu.Unlock()
	if previous != nil && previous != next {
		_ = previous.Close()
	}
}

// Close releases the embedded database.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.dbMu.Lock()
	db := s.db
	s.db = nil
	s.path = ""
	s.dbMu.Unlock()
	if db != nil {
		return db.Close()
	}
	return nil
}

// Enabled reports whether persistent accounting is active.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	enabled := s.enabled
	s.mu.RUnlock()
	return enabled
}

// Reserve checks all applicable policies and atomically counts an accepted request.
func (s *Service) Reserve(ctx context.Context, apiKey, model string, now time.Time) (Decision, error) {
	if s == nil || strings.TrimSpace(apiKey) == "" {
		return Decision{Allowed: true}, nil
	}
	s.mu.RLock()
	if !s.enabled {
		s.mu.RUnlock()
		return Decision{Allowed: true}, nil
	}
	profile, managed := s.profilesByKey[apiKey]
	location := s.location
	s.mu.RUnlock()
	if location == nil {
		location = time.UTC
	}
	if managed && profile.Disabled {
		return Decision{Code: "api_key_disabled", Message: "This API key is disabled."}, nil
	}
	if managed && !modelAllowed(profile.AllowedModels, model) {
		return Decision{Code: "model_not_allowed", Message: "This API key is not allowed to use the requested model."}, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	keyHash := hashAPIKey(apiKey)
	profileID, profileName := profileIdentity(profile, managed, keyHash)
	weekStart, weekEnd := periodWindow(now, periodWeek, location)
	monthStart, monthEnd := periodWindow(now, periodMonth, location)
	reservation := Reservation{
		keyHash: keyHash, profileID: profileID, profileName: profileName, tracked: true,
		periods: [2]periodRef{
			{kind: periodWeek, start: weekStart.Unix(), end: weekEnd},
			{kind: periodMonth, start: monthStart.Unix(), end: monthEnd},
		},
	}

	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	if s.db == nil {
		if managed {
			return Decision{}, errors.New("api-key usage database is unavailable")
		}
		return Decision{Allowed: true}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, err
	}
	defer func() { _ = tx.Rollback() }()

	remainingRequests := int64(-1)
	remainingTokens := int64(-1)
	for _, period := range reservation.periods {
		if err = ensurePeriodRow(ctx, tx, reservation, period); err != nil {
			return Decision{}, err
		}
		var requests, totalTokens int64
		if err = tx.QueryRowContext(ctx, `SELECT requests, total_tokens FROM period_usage WHERE profile_id = ? AND period_kind = ? AND period_start = ?`, profileID, period.kind, period.start).Scan(&requests, &totalTokens); err != nil {
			return Decision{}, err
		}
		limit := limitForPeriod(profile, managed, period.kind)
		if limit.Requests > 0 && requests >= limit.Requests {
			return Decision{Code: period.kind + "_request_limit", Message: "The API key request limit has been reached.", Period: period.kind, Limit: limit.Requests, Used: requests, ResetAt: period.end}, nil
		}
		if limit.Tokens > 0 && totalTokens >= limit.Tokens {
			return Decision{Code: period.kind + "_token_limit", Message: "The API key token limit has been reached.", Period: period.kind, Limit: limit.Tokens, Used: totalTokens, ResetAt: period.end}, nil
		}
		remainingRequests = tighterRemaining(remainingRequests, limit.Requests, requests+1)
		remainingTokens = tighterRemaining(remainingTokens, limit.Tokens, totalTokens)
	}
	for _, period := range reservation.periods {
		if _, err = tx.ExecContext(ctx, `UPDATE period_usage SET requests = requests + 1, key_hash = ?, profile_name = ? WHERE profile_id = ? AND period_kind = ? AND period_start = ?`, keyHash, profileName, profileID, period.kind, period.start); err != nil {
			return Decision{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Decision{}, err
	}
	return Decision{Allowed: true, RemainingRequests: remainingRequests, RemainingTokens: remainingTokens, Reservation: reservation}, nil
}

func ensurePeriodRow(ctx context.Context, tx *sql.Tx, reservation Reservation, period periodRef) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO period_usage (key_hash, profile_id, profile_name, period_kind, period_start) VALUES (?, ?, ?, ?, ?) ON CONFLICT(profile_id, period_kind, period_start) DO UPDATE SET key_hash = excluded.key_hash, profile_name = excluded.profile_name`, reservation.keyHash, reservation.profileID, reservation.profileName, period.kind, period.start)
	return err
}

// Complete records the final downstream HTTP outcome for one reserved request.
func (s *Service) Complete(ctx context.Context, reservation Reservation, statusCode int) error {
	if s == nil || !reservation.tracked {
		return nil
	}
	column := "successes"
	if statusCode >= 400 {
		column = "failures"
	}
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	if s.db == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, period := range reservation.periods {
		query := `UPDATE period_usage SET ` + column + ` = ` + column + ` + 1 WHERE profile_id = ? AND period_kind = ? AND period_start = ?`
		if _, err = tx.ExecContext(ctx, query, reservation.profileID, period.kind, period.start); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HandleUsage implements usage.Plugin and persists completed token accounting.
func (s *Service) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil || strings.TrimSpace(record.APIKey) == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.RLock()
	if !s.enabled {
		s.mu.RUnlock()
		return
	}
	profile, managed := s.profilesByKey[record.APIKey]
	location := s.location
	s.mu.RUnlock()
	if location == nil {
		location = time.UTC
	}
	requestedAt := record.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = time.Now()
	}
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	breakdown := detail.TokenBreakdown
	inputTokens := breakdown.Input.TotalTokens
	outputTokens := breakdown.Output.TotalTokens
	reasoningTokens := breakdown.Output.ReasoningTokens
	cachedTokens := breakdown.Input.CacheReadTokens + breakdown.Input.CacheWriteTokens
	totalTokens := breakdown.TotalTokens
	keyHash := hashAPIKey(record.APIKey)
	profileID, profileName := profileIdentity(profile, managed, keyHash)
	weekStart, _ := periodWindow(requestedAt, periodWeek, location)
	monthStart, _ := periodWindow(requestedAt, periodMonth, location)
	periods := [2]periodRef{{kind: periodWeek, start: weekStart.Unix()}, {kind: periodMonth, start: monthStart.Unix()}}

	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	if s.db == nil {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	reservation := Reservation{keyHash: keyHash, profileID: profileID, profileName: profileName}
	for _, period := range periods {
		if err = ensurePeriodRow(ctx, tx, reservation, period); err != nil {
			return
		}
		if _, err = tx.ExecContext(ctx, `UPDATE period_usage SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?, reasoning_tokens = reasoning_tokens + ?, cached_tokens = cached_tokens + ?, total_tokens = total_tokens + ? WHERE profile_id = ? AND period_kind = ? AND period_start = ?`, inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens, profileID, period.kind, period.start); err != nil {
			return
		}
	}
	failed := 0
	if record.Failed {
		failed = 1
	}
	model := strings.TrimSpace(record.Alias)
	if model == "" {
		model = strings.TrimSpace(record.Model)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_events (key_hash, profile_id, provider, model, requested_at, failed, status_code, latency_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, keyHash, profileID, strings.TrimSpace(record.Provider), model, requestedAt.Unix(), failed, record.Fail.StatusCode, record.Latency.Milliseconds(), inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens)
	if err != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	s.pruneIfNeeded(context.WithoutCancel(ctx), s.db, requestedAt)
}

func (s *Service) pruneIfNeeded(ctx context.Context, db *sql.DB, now time.Time) {
	if s == nil || db == nil {
		return
	}
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < pruneInterval {
		return
	}
	s.mu.RLock()
	retentionDays := s.settings.RetentionDays
	s.mu.RUnlock()
	if err := pruneExpired(ctx, db, retentionDays, now); err == nil {
		s.lastPrune = now
	}
}

// SummaryForPeriod returns current-week or current-month aggregates.
func (s *Service) SummaryForPeriod(ctx context.Context, period string, now time.Time) (Summary, error) {
	period = strings.ToLower(strings.TrimSpace(period))
	if period != periodWeek && period != periodMonth {
		return Summary{}, fmt.Errorf("unsupported period %q", period)
	}
	s.mu.RLock()
	if !s.enabled {
		s.mu.RUnlock()
		return Summary{}, ErrDisabled
	}
	location := s.location
	timezone := s.settings.Timezone
	profiles := make([]config.APIKeyProfile, 0, len(s.profilesByID))
	for _, profile := range s.profilesByID {
		profile.AllowedModels = append([]string(nil), profile.AllowedModels...)
		profiles = append(profiles, profile)
	}
	s.mu.RUnlock()
	if location == nil {
		location = time.UTC
	}
	if now.IsZero() {
		now = time.Now()
	}
	start, end := periodWindow(now, period, location)
	summary := Summary{Period: period, Timezone: timezone, Start: start, End: end, Profiles: []ProfileUsage{}, Models: []ModelUsage{}}

	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	if s.db == nil {
		return Summary{}, errors.New("api-key usage database is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key_hash, profile_id, profile_name, requests, successes, failures, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens FROM period_usage WHERE period_kind = ? AND period_start = ?`, period, start.Unix())
	if err != nil {
		return Summary{}, err
	}
	usageByID := make(map[string]ProfileUsage)
	for rows.Next() {
		var hash, id, name string
		var totals UsageTotals
		if err = rows.Scan(&hash, &id, &name, &totals.Requests, &totals.Successes, &totals.Failures, &totals.InputTokens, &totals.OutputTokens, &totals.ReasoningTokens, &totals.CachedTokens, &totals.TotalTokens); err != nil {
			_ = rows.Close()
			return Summary{}, err
		}
		entry, exists := usageByID[id]
		if !exists {
			entry = ProfileUsage{ID: id, Name: name, KeyFingerprint: fingerprintFromHash(hash), RemainingRequests: -1, RemainingTokens: -1}
		}
		addTotals(&entry.Usage, totals)
		usageByID[id] = entry
	}
	if err = rows.Close(); err != nil {
		return Summary{}, err
	}
	for _, profile := range profiles {
		hash := hashAPIKey(profile.APIKey)
		row, exists := usageByID[profile.ID]
		if !exists {
			row = ProfileUsage{ID: profile.ID, Name: profile.Name, KeyFingerprint: fingerprintFromHash(hash), RemainingRequests: -1, RemainingTokens: -1}
		}
		row.ID = profile.ID
		row.Name = profile.Name
		row.Disabled = profile.Disabled
		row.AllowedModels = append([]string(nil), profile.AllowedModels...)
		row.Limit = limitForPeriod(profile, true, period)
		row.RemainingRequests = remaining(row.Limit.Requests, row.Usage.Requests)
		row.RemainingTokens = remaining(row.Limit.Tokens, row.Usage.TotalTokens)
		row.KeyFingerprint = fingerprintFromHash(hash)
		usageByID[profile.ID] = row
	}
	for _, row := range usageByID {
		summary.Profiles = append(summary.Profiles, row)
		addTotals(&summary.Totals, row.Usage)
	}
	sort.Slice(summary.Profiles, func(i, j int) bool {
		return strings.ToLower(summary.Profiles[i].Name) < strings.ToLower(summary.Profiles[j].Name)
	})

	modelRows, err := s.db.QueryContext(ctx, `SELECT profile_id, model, provider, COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(total_tokens), 0) FROM usage_events WHERE requested_at >= ? AND requested_at < ? GROUP BY profile_id, model, provider ORDER BY SUM(total_tokens) DESC, COUNT(*) DESC`, start.Unix(), end.Unix())
	if err != nil {
		return Summary{}, err
	}
	for modelRows.Next() {
		var row ModelUsage
		if err = modelRows.Scan(&row.ProfileID, &row.Model, &row.Provider, &row.Calls, &row.InputTokens, &row.OutputTokens, &row.TotalTokens); err != nil {
			_ = modelRows.Close()
			return Summary{}, err
		}
		summary.Models = append(summary.Models, row)
	}
	if err = modelRows.Close(); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

// Events returns recent persisted usage records with optional period and profile filters.
func (s *Service) Events(ctx context.Context, profileID string, start, end time.Time, limit, offset int) (EventsPage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	clauses := []string{"1 = 1"}
	args := make([]any, 0, 5)
	if strings.TrimSpace(profileID) != "" {
		clauses = append(clauses, "profile_id = ?")
		args = append(args, strings.TrimSpace(profileID))
	}
	if !start.IsZero() {
		clauses = append(clauses, "requested_at >= ?")
		args = append(args, start.Unix())
	}
	if !end.IsZero() {
		clauses = append(clauses, "requested_at < ?")
		args = append(args, end.Unix())
	}
	where := strings.Join(clauses, " AND ")

	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	if s.db == nil {
		return EventsPage{}, errors.New("api-key usage database is unavailable")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events WHERE `+where, args...).Scan(&total); err != nil {
		return EventsPage{}, err
	}
	queryArgs := append(append([]any(nil), args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT id, profile_id, key_hash, provider, model, requested_at, failed, status_code, latency_ms, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens FROM usage_events WHERE `+where+` ORDER BY requested_at DESC, id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return EventsPage{}, err
	}
	defer rows.Close()
	page := EventsPage{Events: []Event{}, Limit: limit, Offset: offset, Total: total}
	for rows.Next() {
		var event Event
		var keyHash string
		var requestedAt int64
		var failed int
		if err = rows.Scan(&event.ID, &event.ProfileID, &keyHash, &event.Provider, &event.Model, &requestedAt, &failed, &event.StatusCode, &event.LatencyMS, &event.InputTokens, &event.OutputTokens, &event.ReasoningTokens, &event.CachedTokens, &event.TotalTokens); err != nil {
			return EventsPage{}, err
		}
		event.KeyFingerprint = fingerprintFromHash(keyHash)
		event.RequestedAt = time.Unix(requestedAt, 0).UTC()
		event.Failed = failed != 0
		page.Events = append(page.Events, event)
	}
	return page, rows.Err()
}

func periodWindow(now time.Time, period string, location *time.Location) (time.Time, time.Time) {
	local := now.In(location)
	switch period {
	case periodMonth:
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 1, 0)
	default:
		daysSinceMonday := (int(local.Weekday()) + 6) % 7
		start := time.Date(local.Year(), local.Month(), local.Day()-daysSinceMonday, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 0, 7)
	}
}

func profileIdentity(profile config.APIKeyProfile, managed bool, keyHash string) (string, string) {
	if managed {
		return profile.ID, profile.Name
	}
	fingerprint := fingerprintFromHash(keyHash)
	return "legacy-" + fingerprint, "Legacy key " + fingerprint
}

func limitForPeriod(profile config.APIKeyProfile, managed bool, period string) config.APIKeyLimit {
	if !managed {
		return config.APIKeyLimit{}
	}
	if period == periodMonth {
		return profile.Monthly
	}
	return profile.Weekly
}

func modelAllowed(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, pattern := range patterns {
		if wildcardMatch(strings.ToLower(strings.TrimSpace(pattern)), model) {
			return true
		}
	}
	return false
}

func wildcardMatch(pattern, value string) bool {
	if pattern == "*" || pattern == value {
		return true
	}
	patternIndex, valueIndex, starIndex, matchIndex := 0, 0, -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			matchIndex = valueIndex
			patternIndex++
			continue
		}
		if starIndex >= 0 {
			patternIndex = starIndex + 1
			matchIndex++
			valueIndex = matchIndex
			continue
		}
		return false
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

func hashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// Fingerprint returns a short, non-secret identifier for an API key.
func Fingerprint(apiKey string) string {
	return fingerprintFromHash(hashAPIKey(apiKey))
}

func fingerprintFromHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func remaining(limit, used int64) int64 {
	if limit <= 0 {
		return -1
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

func tighterRemaining(current, limit, used int64) int64 {
	next := remaining(limit, used)
	if next < 0 {
		return current
	}
	if current < 0 || next < current {
		return next
	}
	return current
}

func addTotals(target *UsageTotals, value UsageTotals) {
	target.Requests += value.Requests
	target.Successes += value.Successes
	target.Failures += value.Failures
	target.InputTokens += value.InputTokens
	target.OutputTokens += value.OutputTokens
	target.ReasoningTokens += value.ReasoningTokens
	target.CachedTokens += value.CachedTokens
	target.TotalTokens += value.TotalTokens
}
