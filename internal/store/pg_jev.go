package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// JevDefaultModel is the default classifier model. It is pinned to an explicit
// version rather than the "jev-latest" alias: an alias moves when a release
// ships, and a moved model invalidates confidence thresholds an operator has
// already tuned.
const JevDefaultModel = "jev-1.13.0"

// JevSettings is the singleton operator configuration for Jev AI
// classification. The plaintext API key is never represented here — only
// APIKeySet and a masked APIKeyPrefix — so a GET response cannot leak it. The
// key itself is sealed at rest via the shared Sealer.
type JevSettings struct {
	Enabled      bool      `json:"enabled"`
	APIKeySet    bool      `json:"api_key_set"`
	APIKeyPrefix string    `json:"api_key_prefix,omitempty"`
	Model        string    `json:"model"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// defaultJevSettings returns the fallback settings used when the singleton row
// is missing. Classification is disabled by default, so no external traffic is
// attempted until an operator turns it on and supplies a key.
func defaultJevSettings() JevSettings {
	return JevSettings{Enabled: false, Model: JevDefaultModel}
}

// JevStore provides the singleton classifier configuration. It is backed by the
// same *sql.DB connection as PostgresStore and seals the API key at rest via the
// shared Sealer.
//
// Get is served from an in-memory cache invalidated on every write. The
// configuration is read on the request path, so caching keeps reads free of
// database round-trips while a dashboard change still takes effect on the next
// read rather than on restart.
type JevStore struct {
	db     *sql.DB
	table  string
	sealer *Sealer

	mu     sync.RWMutex
	cached *JevSettings
}

// NewJevStore builds a JevStore reusing the PostgresStore connection and table
// name. Returns nil when the parent is nil, so feature-detection is a nil check.
func NewJevStore(parent *PostgresStore) *JevStore {
	if parent == nil {
		return nil
	}
	sealer, errSealer := NewSealer(parent.cfg.UsageEncryptionKey)
	if errSealer != nil {
		log.WithError(errSealer).Warn("postgres store: jev api-key encryption disabled due to key error")
		sealer = nil
	}
	return &JevStore{db: parent.DB(), table: parent.JevSettingsTable(), sealer: sealer}
}

// Get returns the singleton settings. When the row is missing the safe defaults
// are returned with no error. The plaintext key is never returned.
func (s *JevStore) Get(ctx context.Context) (JevSettings, error) {
	if s == nil || s.db == nil {
		return defaultJevSettings(), fmt.Errorf("postgres store: jev store not initialized")
	}
	s.mu.RLock()
	if s.cached != nil {
		cached := *s.cached
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	set := JevSettings{}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT enabled,
		       COALESCE(api_key_sealed, '') <> '' AS api_key_set,
		       COALESCE(api_key_prefix, ''),
		       model,
		       updated_at
		FROM %s WHERE id = 1`, s.table))
	errScan := row.Scan(&set.Enabled, &set.APIKeySet, &set.APIKeyPrefix, &set.Model, &set.UpdatedAt)
	if errScan != nil {
		if errors.Is(errScan, sql.ErrNoRows) {
			return defaultJevSettings(), nil
		}
		return defaultJevSettings(), fmt.Errorf("postgres store: get jev_settings: %w", errScan)
	}
	set = clampJevSettings(set)
	s.mu.Lock()
	s.cached = &set
	s.mu.Unlock()
	return set, nil
}

// clampJevSettings normalizes a settings row: a blank model falls back to the
// pinned default.
func clampJevSettings(s JevSettings) JevSettings {
	if strings.TrimSpace(s.Model) == "" {
		s.Model = JevDefaultModel
	}
	return s
}

// Upsert replaces the singleton settings row. The API key is updated only when
// apiKey is non-nil: a non-empty value is sealed and stored (rotating the key
// and its prefix), an empty value clears it. When apiKey is nil the stored key
// is left unchanged, so callers can toggle the feature or change the model
// without erasing the credential.
func (s *JevStore) Upsert(ctx context.Context, set JevSettings, apiKey *string) (JevSettings, error) {
	if s == nil || s.db == nil {
		return defaultJevSettings(), fmt.Errorf("postgres store: jev store not initialized")
	}
	set = clampJevSettings(set)

	sealedArg := sql.NullString{}
	prefixArg := sql.NullString{}
	switch {
	case apiKey == nil:
		// Carry the existing sealed key + prefix forward: the UPDATE below must
		// not erase a credential the caller did not mention.
		var sealed, prefix *string
		row := s.db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT api_key_sealed, api_key_prefix FROM %s WHERE id = 1`, s.table))
		if errScan := row.Scan(&sealed, &prefix); errScan == nil {
			if sealed != nil && *sealed != "" {
				sealedArg = sql.NullString{String: *sealed, Valid: true}
			}
			if prefix != nil && *prefix != "" {
				prefixArg = sql.NullString{String: *prefix, Valid: true}
			}
		}
	case *apiKey == "":
		// Explicitly clear the key: both args stay invalid, writing NULL.
	default:
		key := strings.TrimSpace(*apiKey)
		sealed := key
		if s.sealer != nil && s.sealer.Enabled() {
			if sealedVal, errSeal := s.sealer.Seal(key); errSeal == nil {
				sealed = sealedVal
			} else {
				log.WithError(errSeal).Warn("postgres store: jev api-key seal failed; storing plaintext")
			}
		}
		sealedArg = sql.NullString{String: sealed, Valid: true}
		prefixArg = sql.NullString{String: prefixOf(key), Valid: true}
	}

	if _, errExec := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, enabled, api_key_sealed, api_key_prefix, model, updated_at)
		VALUES (1, $1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE SET
			enabled        = EXCLUDED.enabled,
			api_key_sealed = EXCLUDED.api_key_sealed,
			api_key_prefix = EXCLUDED.api_key_prefix,
			model          = EXCLUDED.model,
			updated_at     = NOW()
	`, s.table), set.Enabled, sealedArg, prefixArg, set.Model); errExec != nil {
		return set, fmt.Errorf("postgres store: upsert jev_settings: %w", errExec)
	}

	s.mu.Lock()
	s.cached = nil // force the next Get to re-read
	s.mu.Unlock()
	return s.Get(ctx)
}

// JevSettings returns the two global switches the classifier gate needs: the
// master toggle and whether an API key is configured. It satisfies the
// handlers.JevSettingsProvider interface and is served from the in-memory cache,
// so it is safe to call once per auto-routed request. A nil store (PG not
// configured) reports "off", which keeps the gate disabled.
func (s *JevStore) JevSettings(ctx context.Context) (enabled bool, apiKeySet bool) {
	if s == nil {
		return false, false
	}
	set, errGet := s.Get(ctx)
	if errGet != nil {
		// Fail closed: a settings read error must not enable an external call.
		return false, false
	}
	return set.Enabled, set.APIKeySet
}

// APIKey returns the unsealed plaintext API key, or "" when none is configured.
// Used exclusively by the classifier client; never exposed over HTTP.
func (s *JevStore) APIKey(ctx context.Context) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: jev store not initialized")
	}
	var sealed sql.NullString
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT api_key_sealed FROM %s WHERE id = 1`, s.table))
	if errScan := row.Scan(&sealed); errScan != nil {
		if errors.Is(errScan, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("postgres store: get jev api key: %w", errScan)
	}
	if !sealed.Valid || sealed.String == "" {
		return "", nil
	}
	if s.sealer == nil || !s.sealer.Enabled() {
		return sealed.String, nil // legacy plaintext
	}
	plain, errOpen := s.sealer.Open(sealed.String)
	if errOpen != nil {
		return "", fmt.Errorf("postgres store: unseal jev api key: %w", errOpen)
	}
	return plain, nil
}
