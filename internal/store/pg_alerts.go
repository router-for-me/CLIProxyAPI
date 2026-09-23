package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Alert severity labels. Surfaced on the dashboard as badge variants and used
// by the feed/KPI grouping.
const (
	AlertSeverityWarning  = "warning"  // degraded / non-blocking condition
	AlertSeverityCritical = "critical" // budget exceeded / model unavailable
)

// Alert type labels, one per detector. These key both the feed filter dropdown
// and the per-category settings toggles.
const (
	AlertTypeUserBudget        = "user_budget"
	AlertTypeAPIKeyBudget      = "api_key_budget"
	AlertTypeErrorRate         = "error_rate"
	AlertTypeProviderCooldown  = "provider_cooldown"
	AlertTypeModelSubstitution = "model_substitution"
)

// AlertFeedCategories lists every alert type in display order. Used by the
// settings page to render one toggle per category.
var AlertFeedCategories = []string{
	AlertTypeUserBudget,
	AlertTypeAPIKeyBudget,
	AlertTypeErrorRate,
	AlertTypeProviderCooldown,
	AlertTypeModelSubstitution,
}

// alertSuppressionDefault is the default suppression window applied when the
// operator has not configured one (or set a non-positive value). Within this
// window a recurring condition with the same fingerprint bumps occurrences on
// the existing row instead of creating a new alert.
const alertSuppressionDefault = 60 * time.Minute

// alertSettingsDefaultInterval is the sweep cadence applied when the operator
// has not yet configured an interval (or set a non-positive value).
const alertSettingsDefaultInterval = 60 * time.Second

// alertSettingsMinInterval is the floor for the operator-configured interval.
// A sweeper that runs faster than this would hammer PG on large deployments.
const alertSettingsMinInterval = 15 * time.Second

// alertSettingsMaxInterval is the ceiling for the operator-configured interval
// so a typo cannot disable detection for effectively forever.
const alertSettingsMaxInterval = 24 * time.Hour

// alertSettingsMinErrorWindow is the floor for the error-rate window.
const alertSettingsMinErrorWindow = 1

// alertSettingsMaxErrorWindow is the ceiling for the error-rate window.
const alertSettingsMaxErrorWindow = 60

// defaultAlertRetention is the default time-based retention for the alerts
// feed: rows older than this window are purged by the retention sweep.
const defaultAlertRetention = 30 * 24 * time.Hour

// maxAlertMessageBytes caps the stored message text so a single verbose alert
// cannot bloat the feed row unbounded. The final sealing happens in the store.
const maxAlertMessageBytes = 8 * 1024

// Alert is the persisted representation of one notification-feed entry produced
// by an alert detector. A single condition (e.g. "user X over budget") creates
// one row; while the condition persists within the suppression window, later
// detections bump Occurrences and refresh LastSeenAt / value instead of
// inserting duplicates.
//
// Message and Data are AES-GCM-sealed at rest when PGSTORE_ENCRYPTION_KEY is
// configured (see Sealer); otherwise they are persisted in plaintext.
type Alert struct {
	ID              int64          `json:"id"`
	AlertType       string         `json:"alert_type"`
	Severity        string         `json:"severity"`
	Title           string         `json:"title"`
	Message         string         `json:"message,omitempty"`
	EntityID        string         `json:"entity_id,omitempty"`
	EntityName      string         `json:"entity_name,omitempty"`
	Model           string         `json:"model,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Value           float64        `json:"value"`
	LimitValue      float64        `json:"limit_value"`
	Data            map[string]any `json:"data,omitempty"`
	Occurrences     int            `json:"occurrences"`
	LastSeenAt      time.Time      `json:"last_seen_at"`
	SuppressedUntil *time.Time     `json:"suppressed_until,omitempty"`
	Read            bool           `json:"read"`
	Dismissed       bool           `json:"dismissed"`
	CreatedAt       time.Time      `json:"created_at"`
	// Fingerprint is the dedup/suppression key for a condition (e.g.
	// "user_budget:<user_id>"). Never exposed to the dashboard.
	Fingerprint string `json:"-"`
}

// AlertFilter captures the optional filter dimensions accepted by ListPaged.
// Zero values (and empty strings / nil pointers) are ignored.
type AlertFilter struct {
	AlertType string
	Severity  string
	// Dismissed filters by whether the alert has been dismissed. nil = both.
	Dismissed *bool
	// Read filters by whether the alert has been read. nil = both.
	Read *bool
	From time.Time
	To   time.Time
}

// AlertSettings mirrors the singleton operator configuration row for the alert
// sweep. Each category toggle independently enables/disables one detector;
// ErrorRateThreshold / ErrorWindowMinutes tune the error-rate detector; the
// interval / suppression apply globally.
type AlertSettings struct {
	Enabled                 bool    `json:"enabled"`
	IntervalSeconds         int     `json:"interval_seconds"`
	SuppressionMinutes      int     `json:"suppression_minutes"`
	EnableUserBudget        bool    `json:"enable_user_budget"`
	EnableAPIKeyBudget      bool    `json:"enable_api_key_budget"`
	EnableErrorRate         bool    `json:"enable_error_rate"`
	EnableProviderCooldown  bool    `json:"enable_provider_cooldown"`
	EnableModelSubstitution bool    `json:"enable_model_substitution"`
	ErrorRateThreshold      float64 `json:"error_rate_threshold"`
	ErrorWindowMinutes      int     `json:"error_window_minutes"`
	UpdatedAt               time.Time
}

// Suppression returns the configured suppression window as a duration.
func (s AlertSettings) Suppression() time.Duration {
	if s.SuppressionMinutes <= 0 {
		return alertSuppressionDefault
	}
	return time.Duration(s.SuppressionMinutes) * time.Minute
}

// CategoryEnabled reports whether a given alert type's detector is enabled.
func (s AlertSettings) CategoryEnabled(alertType string) bool {
	if !s.Enabled {
		return false
	}
	switch alertType {
	case AlertTypeUserBudget:
		return s.EnableUserBudget
	case AlertTypeAPIKeyBudget:
		return s.EnableAPIKeyBudget
	case AlertTypeErrorRate:
		return s.EnableErrorRate
	case AlertTypeProviderCooldown:
		return s.EnableProviderCooldown
	case AlertTypeModelSubstitution:
		return s.EnableModelSubstitution
	default:
		return true
	}
}

// AlertStore provides insert/list/update for the alerts feed table and the
// alert_settings singleton. It is backed by the same *sql.DB connection as
// PostgresStore and reuses the shared Sealer so message/data are sealed at rest
// when configured.
type AlertStore struct {
	db            *sql.DB
	alertsTable   string
	settingsTable string
	sealer        *Sealer
}

// NewAlertStore builds an AlertStore that reuses the PostgresStore connection
// and table names. Returns nil if the parent store is nil so callers can
// feature-detect the absence of the PG backend with a nil check (mirrors
// NewModelHealthStore).
func NewAlertStore(parent *PostgresStore) *AlertStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.WithError(err).Warn("postgres store: alert encryption disabled due to key error")
		sealer = nil
	}
	return &AlertStore{
		db:            parent.DB(),
		alertsTable:   parent.AlertsTable(),
		settingsTable: parent.AlertSettingsTable(),
		sealer:        sealer,
	}
}

// SetSealer overrides the in-memory sealer. Used by tests that construct an
// AlertStore directly without going through NewPostgresStore.
func (s *AlertStore) SetSealer(sealer *Sealer) {
	if s == nil {
		return
	}
	s.sealer = sealer
}

// sealAlertText seals a string column when a Sealer is enabled, truncating to
// maxAlertMessageBytes first to bound row size.
func (s *AlertStore) sealAlertText(value, label string) string {
	if value == "" {
		return ""
	}
	if s.sealer != nil && s.sealer.Enabled() {
		if sealed, err := s.sealer.Seal(truncateTo(value, maxAlertMessageBytes)); err == nil {
			return sealed
		} else {
			log.WithError(err).Debug("postgres store: seal alert " + label + " failed; storing plaintext")
		}
	}
	return truncateTo(value, maxAlertMessageBytes)
}

// unsealAlertText unseals a persisted sealed column when a Sealer is
// configured; legacy plaintext rows pass through unchanged.
func (s *AlertStore) unsealAlertText(value string) string {
	return unsealAuditBody(s.sealer, value)
}

// RecordAlert upserts an alert for a fingerprint. When an existing
// non-dismissed row for the same fingerprint is still within its suppression
// window, the occurrence is merged (bump occurrences, refresh value/info)
// instead of inserting a duplicate. Otherwise a new alert row is created with
// SuppressedUntil = now + suppression (so a recurring condition does not fire
// every sweep until dismissed or the operator resolves it).
//
// The suppression window read here is the operator's current settings value so
// a live change applies to the next detection.
func (s *AlertStore) RecordAlert(ctx context.Context, a Alert, suppression time.Duration) (Alert, bool, error) {
	if s == nil || s.db == nil {
		return Alert{}, false, fmt.Errorf("postgres store: alert store not initialized")
	}
	if strings.TrimSpace(a.AlertType) == "" {
		return Alert{}, false, fmt.Errorf("postgres store: alert_type is required")
	}
	if strings.TrimSpace(a.Fingerprint) == "" {
		return Alert{}, false, fmt.Errorf("postgres store: fingerprint is required")
	}
	if a.Severity == "" {
		a.Severity = AlertSeverityWarning
	}
	if a.Severity != AlertSeverityWarning {
		a.Severity = AlertSeverityCritical
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.LastSeenAt.IsZero() {
		a.LastSeenAt = a.CreatedAt
	}
	if suppression <= 0 {
		suppression = alertSuppressionDefault
	}
	now := a.LastSeenAt
	var (
		msg  sql.NullString
		data []byte
	)
	msgStr := s.sealAlertText(a.Message, "message")
	if msgStr != "" {
		msg = sql.NullString{String: msgStr, Valid: true}
	}
	if a.Data == nil {
		data = []byte("{}")
	} else if raw, err := json.Marshal(a.Data); err == nil {
		data = raw
	} else {
		data = []byte("{}")
	}

	// Try to merge into an existing non-dismissed row for the same fingerprint
	// that is still within its suppression window first.
	var existingID int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s
		WHERE fingerprint = $1 AND dismissed = FALSE
		  AND (suppressed_until IS NULL OR suppressed_until > $2)
		ORDER BY id DESC LIMIT 1`, s.alertsTable),
		a.Fingerprint, now,
	).Scan(&existingID)
	if err == nil {
		// Merge: bump occurrences, refresh value/info, re-arm suppression.
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s SET
				occurrences     = occurrences + 1,
				value           = $2,
				limit_value     = $3,
				data            = $4,
				last_seen_at    = $5,
				suppressed_until = $6
			WHERE id = $1`, s.alertsTable),
			existingID, a.Value, a.LimitValue, data, now, now.Add(suppression),
		); err != nil {
			return Alert{}, false, fmt.Errorf("postgres store: merge alert by fingerprint: %w", err)
		}
		merged, err := s.GetAlert(ctx, existingID)
		return merged, true, err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Alert{}, false, fmt.Errorf("postgres store: find existing alert by fingerprint: %w", err)
	}

	suppressedUntil := now.Add(suppression)
	err = s.db.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (alert_type, severity, title, message, entity_id, entity_name,
			model, provider, value, limit_value, data, fingerprint, occurrences,
			last_seen_at, suppressed_until, read, dismissed, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 1, $13, $14, FALSE, FALSE, $15)
		RETURNING id`, s.alertsTable),
		a.AlertType, a.Severity, a.Title, msg,
		a.EntityID, a.EntityName, a.Model, a.Provider,
		a.Value, a.LimitValue, data, a.Fingerprint,
		now, suppressedUntil, now,
	).Scan(&existingID)
	if err != nil {
		return Alert{}, false, fmt.Errorf("postgres store: insert alert: %w", err)
	}
	created, err := s.GetAlert(ctx, existingID)
	return created, false, err
}

// alertColumnList is the canonical column list for SELECT; order matches
// scanAlert. Message and data are unsealed on read.
const alertColumnList = `
	id, alert_type, severity, title, message, entity_id, entity_name, model, provider,
	value, limit_value, data, fingerprint, occurrences, last_seen_at, suppressed_until,
	read, dismissed, created_at`

// scanAlert scans one row from alertColumnList order into an Alert, unsealing
// message and data.
func (s *AlertStore) scanAlert(scan func(dest ...any) error) (Alert, error) {
	var (
		a     Alert
		msg   sql.NullString
		data  []byte
		fp    string
		supp  sql.NullTime
		model sql.NullString
		prov  sql.NullString
	)
	if err := scan(&a.ID, &a.AlertType, &a.Severity, &a.Title, &msg, &a.EntityID, &a.EntityName, &model, &prov,
		&a.Value, &a.LimitValue, &data, &fp, &a.Occurrences, &a.LastSeenAt, &supp,
		&a.Read, &a.Dismissed, &a.CreatedAt); err != nil {
		return Alert{}, err
	}
	a.Fingerprint = fp
	if msg.Valid {
		a.Message = s.unsealAlertText(msg.String)
	}
	if model.Valid {
		a.Model = model.String
	}
	if prov.Valid {
		a.Provider = prov.String
	}
	if supp.Valid {
		st := supp.Time
		a.SuppressedUntil = &st
	}
	if len(data) > 0 {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err == nil {
			a.Data = m
		}
	}
	return a, nil
}

// GetAlert returns a single alert row by ID with message/data unsealed.
func (s *AlertStore) GetAlert(ctx context.Context, id int64) (Alert, error) {
	if s == nil || s.db == nil {
		return Alert{}, fmt.Errorf("postgres store: alert store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1`, alertColumnList, s.alertsTable), id)
	a, err := s.scanAlert(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Alert{}, ErrAlertNotFound
		}
		return Alert{}, fmt.Errorf("postgres store: get alert: %w", err)
	}
	return a, nil
}

// ErrAlertNotFound is returned by GetAlert when no alert row matches the id.
var ErrAlertNotFound = errors.New("postgres store: alert not found")

// ListPaged returns a page of alerts matching the filter, newest first, plus
// the total row count. Message and data are unsealed.
func (s *AlertStore) ListPaged(ctx context.Context, f AlertFilter, page, pageSize int) ([]Alert, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: alert store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var (
		where []string
		args  []any
	)
	addFilter := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if strings.TrimSpace(f.AlertType) != "" {
		addFilter("alert_type = $%d", strings.TrimSpace(f.AlertType))
	}
	if strings.TrimSpace(f.Severity) != "" {
		addFilter("severity = $%d", strings.TrimSpace(f.Severity))
	}
	if f.Dismissed != nil {
		addFilter("dismissed = $%d", *f.Dismissed)
	}
	if f.Read != nil {
		addFilter("read = $%d", *f.Read)
	}
	if !f.From.IsZero() {
		addFilter("created_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		addFilter("created_at <= $%d", f.To)
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, s.alertsTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count alerts (paged): %w", err)
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT %s FROM %s%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, alertColumnList, s.alertsTable, whereClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list alerts (paged): %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close alerts rows failed")
		}
	}()

	out := make([]Alert, 0, pageSize)
	for rows.Next() {
		a, errScan := s.scanAlert(rows.Scan)
		if errScan != nil {
			return nil, 0, fmt.Errorf("postgres store: scan alert row (paged): %w", errScan)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate alerts (paged): %w", err)
	}
	return out, total, nil
}

// ListActive returns every non-dismissed alert whose suppression window has
// not yet elapsed, newest first. Used to render the "Active alerts" feed on the
// dashboard (a live view of currently-firing conditions). An alert whose
// condition has since cleared stays visible until dismissed or it ages out, but
// once its suppression window passes and the condition is gone it no longer
// counts as "active".
func (s *AlertStore) ListActive(ctx context.Context) ([]Alert, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: alert store not initialized")
	}
	now := time.Now()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT %s FROM %s
		WHERE dismissed = FALSE AND (suppressed_until IS NULL OR suppressed_until > $1)
		ORDER BY created_at DESC`, alertColumnList, s.alertsTable), now)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list active alerts: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close active alerts rows failed")
		}
	}()
	out := make([]Alert, 0)
	for rows.Next() {
		a, errScan := s.scanAlert(rows.Scan)
		if errScan != nil {
			return nil, fmt.Errorf("postgres store: scan active alert: %w", errScan)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountUnread returns the number of alerts that are neither dismissed nor read.
func (s *AlertStore) CountUnread(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: alert store not initialized")
	}
	var n int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE dismissed = FALSE AND read = FALSE`, s.alertsTable),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres store: count unread alerts: %w", err)
	}
	return n, nil
}

// MarkRead marks a single alert (by id) as read. Returns whether a row was
// affected (false when the id does not exist). Dismissed alerts can also be
// marked read without side effects.
func (s *AlertStore) MarkRead(ctx context.Context, id int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("postgres store: alert store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET read = TRUE WHERE id = $1`, s.alertsTable), id)
	if err != nil {
		return false, fmt.Errorf("postgres store: mark alert read: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkAllRead marks every non-dismissed alert as read. Returns the number
// affected so the dashboard can reflect the change immediately.
func (s *AlertStore) MarkAllRead(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: alert store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET read = TRUE WHERE dismissed = FALSE AND read = FALSE`, s.alertsTable))
	if err != nil {
		return 0, fmt.Errorf("postgres store: mark all alerts read: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Dismiss marks a single alert (by id) as dismissed, which removes it from the
// active feed and unread count. Returns whether a row was affected.
func (s *AlertStore) Dismiss(ctx context.Context, id int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("postgres store: alert store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET dismissed = TRUE, read = TRUE WHERE id = $1`, s.alertsTable), id)
	if err != nil {
		return false, fmt.Errorf("postgres store: dismiss alert: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClearAll deletes every alert row (history + feed). Used by the operator-facing
// DELETE endpoint. Returns the number of rows deleted.
func (s *AlertStore) ClearAll(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: alert store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, s.alertsTable))
	if err != nil {
		return 0, fmt.Errorf("postgres store: clear alerts: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeBefore deletes alert rows older than the cutoff, returning the number
// deleted. Invoked by the StartAlertRetentionSweep goroutine.
func (s *AlertStore) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: alert store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE created_at < $1`, s.alertsTable), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: purge alerts: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// getAlertSettingsRow reads the singleton settings row. Mirrors
// GetAlertSettings but returns the parsed row (used internally by clamps).
func (s *AlertStore) getAlertSettingsRow(ctx context.Context) (AlertSettings, error) {
	set := AlertSettings{}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT enabled, interval_seconds, suppression_minutes,
			enable_user_budget, enable_api_key_budget, enable_error_rate,
			enable_provider_cooldown, enable_model_substitution,
			error_rate_threshold, error_window_minutes, updated_at
		FROM %s WHERE id = 1`, s.settingsTable))
	err := row.Scan(
		&set.Enabled, &set.IntervalSeconds, &set.SuppressionMinutes,
		&set.EnableUserBudget, &set.EnableAPIKeyBudget, &set.EnableErrorRate,
		&set.EnableProviderCooldown, &set.EnableModelSubstitution,
		&set.ErrorRateThreshold, &set.ErrorWindowMinutes, &set.UpdatedAt,
	)
	return set, err
}

// GetAlertSettings returns the singleton operator configuration row. If the
// row is missing (e.g. a freshly created table where the seed INSERT has not
// yet run), the default settings are returned with no error so the sweep can
// run with safe defaults.
func (s *AlertStore) GetAlertSettings(ctx context.Context) (AlertSettings, error) {
	if s == nil || s.db == nil {
		return defaultAlertSettings(), fmt.Errorf("postgres store: alert store not initialized")
	}
	set, err := s.getAlertSettingsRow(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return defaultAlertSettings(), nil
		}
		return defaultAlertSettings(), fmt.Errorf("postgres store: get alert_settings: %w", err)
	}
	return clampAlertSettings(set), nil
}

// UpsertAlertSettings replaces the singleton operator configuration row.
// IntervalSeconds is clamped to [min, max]; SuppressionMinutes, thresholds and
// per-category toggles are normalized to safe defaults when invalid.
func (s *AlertStore) UpsertAlertSettings(ctx context.Context, set AlertSettings) (AlertSettings, error) {
	if s == nil || s.db == nil {
		return defaultAlertSettings(), fmt.Errorf("postgres store: alert store not initialized")
	}
	set = clampAlertSettings(set)
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, enabled, interval_seconds, suppression_minutes,
			enable_user_budget, enable_api_key_budget, enable_error_rate,
			enable_provider_cooldown, enable_model_substitution,
			error_rate_threshold, error_window_minutes, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled                   = EXCLUDED.enabled,
			interval_seconds          = EXCLUDED.interval_seconds,
			suppression_minutes       = EXCLUDED.suppression_minutes,
			enable_user_budget        = EXCLUDED.enable_user_budget,
			enable_api_key_budget     = EXCLUDED.enable_api_key_budget,
			enable_error_rate         = EXCLUDED.enable_error_rate,
			enable_provider_cooldown  = EXCLUDED.enable_provider_cooldown,
			enable_model_substitution = EXCLUDED.enable_model_substitution,
			error_rate_threshold      = EXCLUDED.error_rate_threshold,
			error_window_minutes      = EXCLUDED.error_window_minutes,
			updated_at                = EXCLUDED.updated_at
		`, s.settingsTable),
		set.Enabled, set.IntervalSeconds, set.SuppressionMinutes,
		set.EnableUserBudget, set.EnableAPIKeyBudget, set.EnableErrorRate,
		set.EnableProviderCooldown, set.EnableModelSubstitution,
		set.ErrorRateThreshold, set.ErrorWindowMinutes,
	)
	if err != nil {
		return set, fmt.Errorf("postgres store: upsert alert_settings: %w", err)
	}
	return set, nil
}

func defaultAlertSettings() AlertSettings {
	return AlertSettings{
		Enabled:                 true,
		IntervalSeconds:         int(alertSettingsDefaultInterval.Seconds()),
		SuppressionMinutes:      int(alertSuppressionDefault.Minutes()),
		EnableUserBudget:        true,
		EnableAPIKeyBudget:      true,
		EnableErrorRate:         true,
		EnableProviderCooldown:  true,
		EnableModelSubstitution: true,
		ErrorRateThreshold:      0.5,
		ErrorWindowMinutes:      5,
	}
}

func clampAlertSettings(set AlertSettings) AlertSettings {
	if set.IntervalSeconds < int(alertSettingsMinInterval.Seconds()) {
		set.IntervalSeconds = int(alertSettingsDefaultInterval.Seconds())
	}
	if set.IntervalSeconds > int(alertSettingsMaxInterval.Seconds()) {
		set.IntervalSeconds = int(alertSettingsMaxInterval.Seconds())
	}
	if set.SuppressionMinutes <= 0 {
		set.SuppressionMinutes = int(alertSuppressionDefault.Minutes())
	}
	if set.ErrorRateThreshold < 0 {
		set.ErrorRateThreshold = 0.5
	}
	if set.ErrorWindowMinutes < alertSettingsMinErrorWindow {
		set.ErrorWindowMinutes = 5
	}
	if set.ErrorWindowMinutes > alertSettingsMaxErrorWindow {
		set.ErrorWindowMinutes = alertSettingsMaxErrorWindow
	}
	return set
}

// Interval returns the configured sweep cadence as a duration.
func (s *AlertStore) Interval(ctx context.Context) time.Duration {
	if s == nil {
		return alertSettingsDefaultInterval
	}
	set, err := s.GetAlertSettings(ctx)
	if err != nil {
		return alertSettingsDefaultInterval
	}
	return time.Duration(set.IntervalSeconds) * time.Second
}

// StartAlertRetentionSweep launches a background goroutine that periodically
// deletes alert rows older than the retention window so the feed does not grow
// unbounded. No-op (returns immediately) when the store is nil so callers can
// unconditionally invoke it. Mirrors StartSyncLogSweep.
func StartAlertRetentionSweep(store *AlertStore) {
	if store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-defaultAlertRetention)
			deleted, err := store.PurgeBefore(context.Background(), cutoff)
			if err != nil {
				log.WithError(err).Warn("postgres store: alerts retention sweep failed")
			} else if deleted > 0 {
				log.WithField("deleted", deleted).WithField("cutoff", cutoff).Debug("alerts retention sweep purged aged rows")
			}
		}
	}()
}
