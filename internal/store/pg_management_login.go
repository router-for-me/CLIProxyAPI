package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Management login outcome labels recorded in management_login_events.outcome.
const (
	LoginOutcomeSuccess        = "success"
	LoginOutcomeInvalidKey     = "invalid_key"
	LoginOutcomeMissingKey     = "missing_key"
	LoginOutcomeRemoteDisabled = "remote_disabled"
	LoginOutcomeBanned         = "banned"
	LoginOutcomeBanStarted     = "ban_started"
)

// Defaults and clamps for the management_login_settings singleton. The
// defaults mirror the constants that were previously hardcoded in the
// management handler (5 failures / 30m ban / 1h cleanup / 2h idle / 30d).
const (
	loginSecurityDefaultMaxFailures   = 5
	loginSecurityDefaultBanSeconds    = 1800
	loginSecurityDefaultCleanupSecs   = 3600
	loginSecurityDefaultIdleSeconds   = 7200
	loginSecurityDefaultRetentionDays = 30

	loginSecurityMaxMaxFailures    = 100
	loginSecurityMaxBanSeconds     = 604800 // 7d
	loginSecurityMaxWindowSeconds  = 86400  // 24h
	loginSecurityMinCleanupSeconds = 60
	loginSecurityMaxCleanupSeconds = 86400
	loginSecurityMinIdleSeconds    = 60
	loginSecurityMaxIdleSeconds    = 604800
	loginSecurityMaxRetentionDays  = 365
)

// LoginSecuritySettings mirrors the singleton management_login_settings row.
type LoginSecuritySettings struct {
	Enabled                bool      `json:"enabled"`
	MaxFailedAttempts      int       `json:"max_failed_attempts"`
	BanDurationSeconds     int       `json:"ban_duration_seconds"`
	FailureWindowSeconds   int       `json:"failure_window_seconds"`
	CleanupIntervalSeconds int       `json:"cleanup_interval_seconds"`
	IdleTimeoutSeconds     int       `json:"idle_timeout_seconds"`
	LogSuccesses           bool      `json:"log_successes"`
	RetentionDays          int       `json:"retention_days"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// BanDuration returns the configured ban window as a duration.
func (s LoginSecuritySettings) BanDuration() time.Duration {
	return time.Duration(s.BanDurationSeconds) * time.Second
}

// CleanupInterval returns the configured in-memory cleanup cadence.
func (s LoginSecuritySettings) CleanupInterval() time.Duration {
	return time.Duration(s.CleanupIntervalSeconds) * time.Second
}

// IdleTimeout returns how long an IP may be idle before cleanup.
func (s LoginSecuritySettings) IdleTimeout() time.Duration {
	return time.Duration(s.IdleTimeoutSeconds) * time.Second
}

// FailureWindow returns the tumbling failure-count window (0 = lifetime).
func (s LoginSecuritySettings) FailureWindow() time.Duration {
	return time.Duration(s.FailureWindowSeconds) * time.Second
}

// Retention returns the event-log retention window.
func (s LoginSecuritySettings) Retention() time.Duration {
	return time.Duration(s.RetentionDays) * 24 * time.Hour
}

// LoginEvent is one persisted management login attempt.
type LoginEvent struct {
	ID           int64     `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	IP           string    `json:"ip"`
	Outcome      string    `json:"outcome"`
	Reason       string    `json:"reason"`
	AttemptCount int       `json:"attempt_count"`
	Local        bool      `json:"local"`
	UserAgent    string    `json:"user_agent"`
}

// LoginEventFilter captures the optional dimensions accepted by ListLoginEvents.
type LoginEventFilter struct {
	IP      string
	Outcome string
	From    time.Time
	To      time.Time
}

// ManagementLoginStore provides read/write access to the management login
// settings singleton and the append-only login event log. It is a child of
// PostgresStore (borrows its *sql.DB and table names).
type ManagementLoginStore struct {
	db            *sql.DB
	settingsTable string
	eventsTable   string
}

// NewManagementLoginStore builds a ManagementLoginStore reusing the
// PostgresStore connection. Returns nil for a nil parent so callers can
// feature-detect the absence of the PG backend.
func NewManagementLoginStore(parent *PostgresStore) *ManagementLoginStore {
	if parent == nil {
		return nil
	}
	return &ManagementLoginStore{
		db:            parent.DB(),
		settingsTable: parent.ManagementLoginSettingsTable(),
		eventsTable:   parent.ManagementLoginEventsTable(),
	}
}

// DefaultLoginSecuritySettings returns the fail-open policy applied when the
// store is unavailable or has not been configured.
func DefaultLoginSecuritySettings() LoginSecuritySettings {
	return LoginSecuritySettings{
		Enabled:                true,
		MaxFailedAttempts:      loginSecurityDefaultMaxFailures,
		BanDurationSeconds:     loginSecurityDefaultBanSeconds,
		FailureWindowSeconds:   0,
		CleanupIntervalSeconds: loginSecurityDefaultCleanupSecs,
		IdleTimeoutSeconds:     loginSecurityDefaultIdleSeconds,
		LogSuccesses:           true,
		RetentionDays:          loginSecurityDefaultRetentionDays,
	}
}

// ClampLoginSecuritySettings normalizes an operator-supplied policy. Values
// below their floor fall back to the default (not the floor); values above
// their ceiling are capped.
func ClampLoginSecuritySettings(set LoginSecuritySettings) LoginSecuritySettings {
	if set.MaxFailedAttempts < 1 {
		set.MaxFailedAttempts = loginSecurityDefaultMaxFailures
	}
	if set.MaxFailedAttempts > loginSecurityMaxMaxFailures {
		set.MaxFailedAttempts = loginSecurityMaxMaxFailures
	}
	if set.BanDurationSeconds < 1 {
		set.BanDurationSeconds = loginSecurityDefaultBanSeconds
	}
	if set.BanDurationSeconds > loginSecurityMaxBanSeconds {
		set.BanDurationSeconds = loginSecurityMaxBanSeconds
	}
	if set.FailureWindowSeconds < 0 {
		set.FailureWindowSeconds = 0
	}
	if set.FailureWindowSeconds > loginSecurityMaxWindowSeconds {
		set.FailureWindowSeconds = loginSecurityMaxWindowSeconds
	}
	if set.CleanupIntervalSeconds < loginSecurityMinCleanupSeconds {
		set.CleanupIntervalSeconds = loginSecurityDefaultCleanupSecs
	}
	if set.CleanupIntervalSeconds > loginSecurityMaxCleanupSeconds {
		set.CleanupIntervalSeconds = loginSecurityMaxCleanupSeconds
	}
	if set.IdleTimeoutSeconds < loginSecurityMinIdleSeconds {
		set.IdleTimeoutSeconds = loginSecurityDefaultIdleSeconds
	}
	if set.IdleTimeoutSeconds > loginSecurityMaxIdleSeconds {
		set.IdleTimeoutSeconds = loginSecurityMaxIdleSeconds
	}
	if set.RetentionDays < 1 {
		set.RetentionDays = loginSecurityDefaultRetentionDays
	}
	if set.RetentionDays > loginSecurityMaxRetentionDays {
		set.RetentionDays = loginSecurityMaxRetentionDays
	}
	return set
}

func (s *ManagementLoginStore) getLoginSettingsRow(ctx context.Context) (LoginSecuritySettings, error) {
	set := LoginSecuritySettings{}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT enabled, max_failed_attempts, ban_duration_seconds,
			failure_window_seconds, cleanup_interval_seconds, idle_timeout_seconds,
			log_successes, retention_days, updated_at
		FROM %s WHERE id = 1`, s.settingsTable))
	err := row.Scan(
		&set.Enabled, &set.MaxFailedAttempts, &set.BanDurationSeconds,
		&set.FailureWindowSeconds, &set.CleanupIntervalSeconds, &set.IdleTimeoutSeconds,
		&set.LogSuccesses, &set.RetentionDays, &set.UpdatedAt,
	)
	return set, err
}

// GetLoginSettings returns the singleton policy, clamped. A missing row or an
// uninitialized (nil) store yields the default policy with no error so the auth
// path can fail open.
func (s *ManagementLoginStore) GetLoginSettings(ctx context.Context) (LoginSecuritySettings, error) {
	if s == nil || s.db == nil {
		return DefaultLoginSecuritySettings(), nil
	}
	set, err := s.getLoginSettingsRow(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DefaultLoginSecuritySettings(), nil
		}
		return DefaultLoginSecuritySettings(), fmt.Errorf("postgres store: get management_login_settings: %w", err)
	}
	return ClampLoginSecuritySettings(set), nil
}

// UpsertLoginSettings replaces the singleton policy row after clamping.
func (s *ManagementLoginStore) UpsertLoginSettings(ctx context.Context, set LoginSecuritySettings) (LoginSecuritySettings, error) {
	if s == nil || s.db == nil {
		return DefaultLoginSecuritySettings(), fmt.Errorf("postgres store: management login store not initialized")
	}
	set = ClampLoginSecuritySettings(set)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			id, enabled, max_failed_attempts, ban_duration_seconds,
			failure_window_seconds, cleanup_interval_seconds, idle_timeout_seconds,
			log_successes, retention_days, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled                  = EXCLUDED.enabled,
			max_failed_attempts      = EXCLUDED.max_failed_attempts,
			ban_duration_seconds     = EXCLUDED.ban_duration_seconds,
			failure_window_seconds   = EXCLUDED.failure_window_seconds,
			cleanup_interval_seconds = EXCLUDED.cleanup_interval_seconds,
			idle_timeout_seconds     = EXCLUDED.idle_timeout_seconds,
			log_successes            = EXCLUDED.log_successes,
			retention_days           = EXCLUDED.retention_days,
			updated_at               = EXCLUDED.updated_at
		`, s.settingsTable),
		set.Enabled, set.MaxFailedAttempts, set.BanDurationSeconds,
		set.FailureWindowSeconds, set.CleanupIntervalSeconds, set.IdleTimeoutSeconds,
		set.LogSuccesses, set.RetentionDays,
	)
	if err != nil {
		return set, fmt.Errorf("postgres store: upsert management_login_settings: %w", err)
	}
	return set, nil
}

// RecordLoginEvents inserts a batch of login events. Batches above 200 rows
// are split so a single SQL statement stays bounded.
func (s *ManagementLoginStore) RecordLoginEvents(ctx context.Context, events []LoginEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: management login store not initialized")
	}
	if len(events) == 0 {
		return nil
	}
	const maxBatch = 200
	if len(events) > maxBatch {
		if err := s.RecordLoginEvents(ctx, events[:maxBatch]); err != nil {
			return err
		}
		return s.RecordLoginEvents(ctx, events[maxBatch:])
	}
	var b strings.Builder
	fmt.Fprintf(&b, `INSERT INTO %s (created_at, ip, outcome, reason, attempt_count, local, user_agent) VALUES `, s.eventsTable)
	args := make([]any, 0, len(events)*7)
	for i, e := range events {
		if i > 0 {
			b.WriteString(", ")
		}
		base := i * 7
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6, base+7)
		created := e.CreatedAt
		if created.IsZero() {
			created = time.Now()
		}
		args = append(args, created, e.IP, e.Outcome, e.Reason, e.AttemptCount, e.Local, e.UserAgent)
	}
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("postgres store: record management login events: %w", err)
	}
	return nil
}

// ListLoginEvents returns a page of events (newest first) plus the total count.
func (s *ManagementLoginStore) ListLoginEvents(ctx context.Context, f LoginEventFilter, page, pageSize int) ([]LoginEvent, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: management login store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}

	where := []string{"1=1"}
	args := []any{}
	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if ip := strings.TrimSpace(f.IP); ip != "" {
		add("ip = $%d", ip)
	}
	if o := strings.TrimSpace(f.Outcome); o != "" {
		add("outcome = $%d", o)
	}
	if !f.From.IsZero() {
		add("created_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("created_at <= $%d", f.To)
	}
	clause := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, s.eventsTable, clause),
		args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count management login events: %w", err)
	}

	listArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, created_at, ip, outcome, reason, attempt_count, local, user_agent
		FROM %s WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d`,
		s.eventsTable, clause, len(args)+1, len(args)+2), listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list management login events: %w", err)
	}
	defer rows.Close()

	out := make([]LoginEvent, 0, pageSize)
	for rows.Next() {
		var e LoginEvent
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.IP, &e.Outcome, &e.Reason, &e.AttemptCount, &e.Local, &e.UserAgent); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan management login event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate management login events: %w", err)
	}
	return out, total, nil
}

// PurgeLoginEventsBefore deletes events created before cutoff.
func (s *ManagementLoginStore) PurgeLoginEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: management login store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE created_at < $1`, s.eventsTable), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: purge management login events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClearLoginEvents deletes every event row.
func (s *ManagementLoginStore) ClearLoginEvents(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: management login store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, s.eventsTable))
	if err != nil {
		return 0, fmt.Errorf("postgres store: clear management login events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
