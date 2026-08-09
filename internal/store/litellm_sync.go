package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Sync status constants recorded on the singleton settings row after a sync.
const (
	LiteLLMSyncStatusOK     = "success"
	LiteLLMSyncStatusFailed = "error"
	LiteLLMSyncStatusIdle   = ""
)

// LiteLLMSyncMinInterval is the lowest allowed auto-sync cadence (seconds).
const LiteLLMSyncMinInterval = 60

// LiteLLMSyncDefaultInterval is the default auto-sync cadence (seconds).
const LiteLLMSyncDefaultInterval = 300

// LiteLLMSyncSettings is the singleton operator configuration for the
// Manage-LiteLLM external sync. The plaintext master API key is never
// represented here — only MasterKeySet + a masked MasterKeyPrefix — so GET
// responses can never leak it. MasterKey is sealed at rest via the Sealer.
type LiteLLMSyncSettings struct {
	Enabled         bool       `json:"enabled"`
	IntervalSeconds int        `json:"interval_seconds"`
	BaseURL         string     `json:"base_url"`
	MasterKeySet    bool       `json:"master_key_set"`
	MasterKeyPrefix string     `json:"master_key_prefix,omitempty"`
	LastSyncAt      *time.Time `json:"last_sync_at,omitempty"`
	LastSyncStatus  string     `json:"last_sync_status,omitempty"`
	LastSyncError   string     `json:"last_sync_error,omitempty"`
	LastSyncUsers   int        `json:"last_sync_users"`
	LastSyncKeys    int        `json:"last_sync_keys"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// defaultLiteLLMSyncSettings returns the fallback settings used when the
// singleton row is missing. Sync is disabled by default so no external traffic
// is ever attempted until an operator configures base_url + master key.
func defaultLiteLLMSyncSettings() LiteLLMSyncSettings {
	return LiteLLMSyncSettings{
		Enabled:         false,
		IntervalSeconds: LiteLLMSyncDefaultInterval,
	}
}

// clampLiteLLMSyncSettings normalizes IntervalSeconds to a safe floor.
func clampLiteLLMSyncSettings(s LiteLLMSyncSettings) LiteLLMSyncSettings {
	if s.IntervalSeconds < LiteLLMSyncMinInterval {
		s.IntervalSeconds = LiteLLMSyncMinInterval
	}
	return s
}

// LiteLLMSyncStore provides the singleton external-sync settings + last-sync
// outcome. It is backed by the same *sql.DB connection as PostgresStore and
// seals the master API key at rest via the shared Sealer.
type LiteLLMSyncStore struct {
	db          *sql.DB
	table       string
	usersTable  string
	keysTable   string
	policyTable string
	sealer      *Sealer
}

// NewLiteLLMSyncStore builds a LiteLLMSyncStore that reuses the PostgresStore
// connection and table names. Returns nil when the parent store is nil so
// feature-detection is a single nil check. The master-key sealer is derived
// from PGSTORE_ENCRYPTION_KEY; when unset the sealer is nil and the key is
// stored as plaintext (the legacy-tolerant path shared with other stores).
func NewLiteLLMSyncStore(parent *PostgresStore) *LiteLLMSyncStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.WithError(err).Warn("postgres store: litellm sync master-key encryption disabled due to key error")
		sealer = nil
	}
	return &LiteLLMSyncStore{
		db:          parent.DB(),
		table:       parent.LiteLLMSyncSettingsTable(),
		usersTable:  parent.LiteLLMUsersTable(),
		keysTable:   parent.LiteLLMKeysTable(),
		policyTable: parent.LiteLLMKeyPoliciesTable(),
		sealer:      sealer,
	}
}

// Get returns the singleton external-sync settings. When the row is missing
// (e.g. a freshly created table where the seed INSERT has not yet run), the
// safe defaults are returned with no error. The plaintext master key is never
// returned; only MasterKeySet + MasterKeyPrefix.
func (s *LiteLLMSyncStore) Get(ctx context.Context) (LiteLLMSyncSettings, error) {
	if s == nil || s.db == nil {
		return defaultLiteLLMSyncSettings(), fmt.Errorf("postgres store: litellm sync store not initialized")
	}
	set := LiteLLMSyncSettings{}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT enabled, interval_seconds, COALESCE(base_url, ''),
		       COALESCE(master_key_sealed, '') <> '' AS master_key_set,
		       COALESCE(master_key_prefix, ''),
		       last_sync_at, COALESCE(last_sync_status, ''), COALESCE(last_sync_error, ''),
		       last_sync_users, last_sync_keys, updated_at
		FROM %s WHERE id = 1`, s.table))
	err := row.Scan(
		&set.Enabled, &set.IntervalSeconds, &set.BaseURL,
		&set.MasterKeySet, &set.MasterKeyPrefix,
		&set.LastSyncAt, &set.LastSyncStatus, &set.LastSyncError,
		&set.LastSyncUsers, &set.LastSyncKeys, &set.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return defaultLiteLLMSyncSettings(), nil
		}
		return defaultLiteLLMSyncSettings(), fmt.Errorf("postgres store: get litellm_sync_settings: %w", err)
	}
	return clampLiteLLMSyncSettings(set), nil
}

// Upsert replaces the singleton settings row. The master key is updated only
// when masterKey is non-nil: a non-empty value is sealed+stored (rotating the
// key and prefix), an empty value clears it. When masterKey is nil the stored
// key is left unchanged, so callers can update the other fields without
// erasing the credential.
func (s *LiteLLMSyncStore) Upsert(ctx context.Context, set LiteLLMSyncSettings, masterKey *string) (LiteLLMSyncSettings, error) {
	if s == nil || s.db == nil {
		return defaultLiteLLMSyncSettings(), fmt.Errorf("postgres store: litellm sync store not initialized")
	}
	set = clampLiteLLMSyncSettings(set)
	set.BaseURL = strings.TrimSpace(set.BaseURL)

	// Resolve the sealed key + prefix for this write.
	sealedArg := sql.NullString{}
	prefixArg := sql.NullString{}
	switch {
	case masterKey == nil:
		// Keep the existing sealed key + prefix untouched; the UPDATE below
		// must not overwrite them. We read the current row and carry forward.
		current, err := s.Get(ctx)
		if err == nil {
			// Re-read the sealed column so an unchanged key is preserved even
			// though Get() masks it.
			var sealed, prefix *string
			sq := s.db.QueryRowContext(ctx, fmt.Sprintf(
				`SELECT master_key_sealed, master_key_prefix FROM %s WHERE id = 1`, s.table))
			if scanErr := sq.Scan(&sealed, &prefix); scanErr == nil {
				if sealed != nil && *sealed != "" {
					sealedArg = sql.NullString{String: *sealed, Valid: true}
				}
				if prefix != nil && *prefix != "" {
					prefixArg = sql.NullString{String: *prefix, Valid: true}
				}
				_ = current
			}
		}
	case *masterKey == "":
		// Explicitly clear the key.
		sealedArg = sql.NullString{}
		prefixArg = sql.NullString{}
	default:
		key := strings.TrimSpace(*masterKey)
		sealed := key
		if s.sealer != nil && s.sealer.Enabled() {
			if s2, errSeal := s.sealer.Seal(key); errSeal == nil {
				sealed = s2
			} else {
				log.WithError(errSeal).Warn("postgres store: litellm sync master-key seal failed; storing plaintext")
			}
		}
		sealedArg = sql.NullString{String: sealed, Valid: true}
		prefixArg = sql.NullString{String: prefixOf(key), Valid: true}
		set.MasterKeySet = sealedArg.Valid && sealedArg.String != ""
		set.MasterKeyPrefix = prefixArg.String
	}

	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, enabled, interval_seconds, base_url,
			master_key_sealed, master_key_prefix, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled            = EXCLUDED.enabled,
			interval_seconds   = EXCLUDED.interval_seconds,
			base_url           = EXCLUDED.base_url,
			master_key_sealed  = EXCLUDED.master_key_sealed,
			master_key_prefix  = EXCLUDED.master_key_prefix,
			updated_at         = NOW()
	`, s.table),
		set.Enabled, set.IntervalSeconds, nullableString(set.BaseURL),
		sealedArg, prefixArg,
	)
	if err != nil {
		return set, fmt.Errorf("postgres store: upsert litellm_sync_settings: %w", err)
	}
	return s.Get(ctx)
}

// MasterKey returns the unsealed plaintext master API key, or "" when none is
// configured. Used exclusively by the sync runner; never exposed over HTTP.
func (s *LiteLLMSyncStore) MasterKey(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: litellm sync store not initialized")
	}
	var sealed sql.NullString
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT master_key_sealed FROM %s WHERE id = 1`, s.table))
	if err := row.Scan(&sealed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("postgres store: get litellm master key: %w", err)
	}
	if !sealed.Valid || sealed.String == "" {
		return "", nil
	}
	if s.sealer == nil || !s.sealer.Enabled() {
		return sealed.String, nil // legacy plaintext
	}
	plain, err := s.sealer.Open(sealed.String)
	if err != nil {
		return "", fmt.Errorf("postgres store: unseal litellm master key: %w", err)
	}
	return plain, nil
}

// RecordSync persists the outcome of a sync pass (status + error + counts) on
// the singleton row, stamping LastSyncAt = now.
func (s *LiteLLMSyncStore) RecordSync(ctx context.Context, status, errMsg string, users, keys int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: litellm sync store not initialized")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET
			last_sync_at     = NOW(),
			last_sync_status = $1,
			last_sync_error  = $2,
			last_sync_users  = $3,
			last_sync_keys   = $4,
			updated_at       = NOW()
		WHERE id = 1`, s.table),
		status, nullableString(errMsg), users, keys,
	)
	if err != nil {
		return fmt.Errorf("postgres store: record litellm sync outcome: %w", err)
	}
	return nil
}

// Interval returns the current auto-sync cadence, re-read from the store so
// operator changes take effect without a restart. The sweep calls this each
// tick. Returns 0 when the store is unavailable (callers fall back to a
// default).
func (s *LiteLLMSyncStore) Interval(ctx context.Context) time.Duration {
	if s == nil || s.db == nil {
		return time.Duration(LiteLLMSyncDefaultInterval) * time.Second
	}
	set, err := s.Get(ctx)
	if err != nil || !set.Enabled {
		return time.Duration(LiteLLMSyncDefaultInterval) * time.Second
	}
	return time.Duration(set.IntervalSeconds) * time.Second
}
