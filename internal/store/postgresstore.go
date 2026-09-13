package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultConfigTable   = "config_store"
	defaultAuthTable     = "auth_store"
	defaultCooldownTable = "cooldown_store"
	defaultConfigKey     = "config"
)

// PostgresStoreConfig captures configuration required to initialize a Postgres-backed store.
type PostgresStoreConfig struct {
	DSN           string
	Schema        string
	ConfigTable   string
	AuthTable     string
	CooldownTable string
	SpoolDir      string
}

// PostgresStore persists configuration and authentication metadata using PostgreSQL as backend
// while mirroring data to a local workspace so existing file-based workflows continue to operate.
type PostgresStore struct {
	db            *sql.DB
	cfg           PostgresStoreConfig
	spoolRoot     string
	configPath    string
	authDir       string
	cooldownStore *postgresCooldownStateStore
	mu            sync.Mutex
	renameFile    func(string, string) error
}

// NewPostgresStore establishes a connection to PostgreSQL and prepares the local workspace.
func NewPostgresStore(ctx context.Context, cfg PostgresStoreConfig) (*PostgresStore, error) {
	trimmedDSN := strings.TrimSpace(cfg.DSN)
	if trimmedDSN == "" {
		return nil, fmt.Errorf("postgres store: DSN is required")
	}
	cfg.DSN = trimmedDSN
	if cfg.ConfigTable == "" {
		cfg.ConfigTable = defaultConfigTable
	}
	if cfg.AuthTable == "" {
		cfg.AuthTable = defaultAuthTable
	}
	if cfg.CooldownTable == "" {
		cfg.CooldownTable = defaultCooldownTable
	}

	spoolRoot := strings.TrimSpace(cfg.SpoolDir)
	if spoolRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			spoolRoot = filepath.Join(cwd, "pgstore")
		} else {
			spoolRoot = filepath.Join(os.TempDir(), "pgstore")
		}
	}
	absSpool, err := filepath.Abs(spoolRoot)
	if err != nil {
		return nil, fmt.Errorf("postgres store: resolve spool directory: %w", err)
	}
	configDir := filepath.Join(absSpool, "config")
	authDir := filepath.Join(absSpool, "auths")
	if err = os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("postgres store: create config directory: %w", err)
	}
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return nil, fmt.Errorf("postgres store: create auth directory: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres store: open database connection: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres store: ping database: %w", err)
	}

	store := &PostgresStore{
		db:         db,
		cfg:        cfg,
		spoolRoot:  absSpool,
		configPath: filepath.Join(configDir, "config.yaml"),
		authDir:    authDir,
	}
	store.cooldownStore = &postgresCooldownStateStore{store: store}
	return store, nil
}

// Close releases the underlying database connection.
func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// EnsureSchema creates the required tables (and schema when provided).
func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("postgres store: create schema: %w", err)
		}
	}
	configTable := s.fullTableName(s.cfg.ConfigTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, configTable)); err != nil {
		return fmt.Errorf("postgres store: create config table: %w", err)
	}
	authTable := s.fullTableName(s.cfg.AuthTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			content JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, authTable)); err != nil {
		return fmt.Errorf("postgres store: create auth table: %w", err)
	}
	cooldownTable := s.fullTableName(s.cfg.CooldownTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			auth_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			content JSONB NOT NULL,
			deleted BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (auth_id, model)
		)
	`, cooldownTable)); err != nil {
		return fmt.Errorf("postgres store: create cooldown table: %w", err)
	}
	return nil
}

// Bootstrap synchronizes configuration and auth records between PostgreSQL and the local workspace.
func (s *PostgresStore) Bootstrap(ctx context.Context, exampleConfigPath string) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if err := s.syncConfigFromDatabase(ctx, exampleConfigPath); err != nil {
		return err
	}
	if err := s.syncAuthFromDatabase(ctx); err != nil {
		return err
	}
	return nil
}

// ConfigPath returns the managed configuration file path inside the spool directory.
func (s *PostgresStore) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

// AuthDir returns the local directory containing mirrored auth files.
func (s *PostgresStore) AuthDir() string {
	if s == nil {
		return ""
	}
	return s.authDir
}

// WorkDir exposes the root spool directory used for mirroring.
func (s *PostgresStore) WorkDir() string {
	if s == nil {
		return ""
	}
	return s.spoolRoot
}

// SetBaseDir implements the optional interface used by authenticators; it is a no-op because
// the Postgres-backed store controls its own workspace.
func (s *PostgresStore) SetBaseDir(string) {}

// Save persists authentication metadata to disk and PostgreSQL.
func (s *PostgresStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}
	cliproxyauth.NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := cliproxyauth.ValidateAuthWeight(auth); errWeight != nil {
		return "", fmt.Errorf("postgres store: %w", errWeight)
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("postgres store: missing file path attribute for %s", auth.ID)
	}

	// Runtime updates must not recreate a disabled credential whose source file
	// was deliberately removed. Login and migration callers explicitly mark the
	// save when creating a missing disabled credential is intentional.
	if auth.Disabled && !cliproxyauth.HasAuthCreationIntent(ctx) {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			return "", nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("postgres store: create auth directory: %w", err)
	}

	relID, err := s.relativeAuthID(path)
	if err != nil {
		return "", err
	}
	stagedDir, errStage := os.MkdirTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if errStage != nil {
		return "", fmt.Errorf("postgres store: create temp auth directory: %w", errStage)
	}
	tmp := filepath.Join(stagedDir, "auth")
	defer func() {
		if errRemove := os.RemoveAll(stagedDir); errRemove != nil {
			log.WithError(errRemove).Warn("postgres store: remove temporary auth directory")
		}
	}()

	switch {
	case auth.Storage != nil:
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disabled"] = auth.Disabled
		if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if err = auth.Storage.SaveTokenToFile(tmp); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("postgres store: marshal metadata: %w", errMarshal)
		}
		if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("postgres store: write temp auth file: %w", errWrite)
		}
	default:
		return "", fmt.Errorf("postgres store: nothing to persist for %s", auth.ID)
	}
	if errChmod := os.Chmod(tmp, 0o600); errChmod != nil && !errors.Is(errChmod, fs.ErrNotExist) {
		return "", fmt.Errorf("postgres store: secure temp auth file: %w", errChmod)
	}

	candidate, errReadCandidate := os.ReadFile(tmp)
	if errReadCandidate != nil {
		return "", fmt.Errorf("postgres store: read temp auth file: %w", errReadCandidate)
	}

	err = s.withAuthLock(ctx, relID, func(conn *sql.Conn) error {
		localPrevious, errReadPrevious := os.ReadFile(path)
		localExists := errReadPrevious == nil
		if errReadPrevious != nil && !errors.Is(errReadPrevious, fs.ErrNotExist) {
			return fmt.Errorf("postgres store: read existing metadata: %w", errReadPrevious)
		}
		var (
			durablePrevious       postgresAuthRecord
			durablePreviousExists bool
			durableChanged        bool
		)
		if len(candidate) == 0 {
			durablePrevious, durablePreviousExists, err = s.deleteAuthRecordReturning(ctx, conn, relID)
			durableChanged = durablePreviousExists
		} else {
			durablePrevious, durablePreviousExists, err = s.replaceAuthRecord(ctx, conn, relID, candidate)
			durableChanged = true
		}
		if err != nil {
			return err
		}
		if localExists && jsonEqual(localPrevious, candidate) {
			return nil
		}
		if errRename := s.renameAuthFile(tmp, path); errRename != nil {
			if durableChanged {
				errRollback := s.rollbackAuthRecord(context.WithoutCancel(ctx), conn, relID, candidate, durablePrevious, durablePreviousExists)
				if errRollback != nil {
					return errors.Join(
						fmt.Errorf("postgres store: publish auth file: %w", errRename),
						fmt.Errorf("postgres store: database rollback failed: %w", errRollback),
					)
				}
			}
			return fmt.Errorf("postgres store: publish auth file: %w", errRename)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	normalizeSavedPostgresAuth(auth, path)
	return path, nil
}

func (s *PostgresStore) renameAuthFile(oldPath, newPath string) error {
	if s.renameFile != nil {
		return s.renameFile(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func normalizeSavedPostgresAuth(auth *cliproxyauth.Auth, path string) {
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[cliproxyauth.AttributePath] = path
	auth.Attributes[cliproxyauth.AttributeSourceBackend] = cliproxyauth.AuthSourcePostgres
	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}
}

// List enumerates all auth records stored in PostgreSQL.
func (s *PostgresStore) List(ctx context.Context) ([]*cliproxyauth.Auth, error) {
	query := fmt.Sprintf("SELECT id, content, created_at, updated_at FROM %s ORDER BY id", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list auth: %w", err)
	}
	defer rows.Close()

	auths := make([]*cliproxyauth.Auth, 0, 32)
	for rows.Next() {
		var (
			id        string
			payload   string
			createdAt time.Time
			updatedAt time.Time
		)
		if err = rows.Scan(&id, &payload, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan auth row: %w", err)
		}
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: skipping auth %s outside spool", id)
			continue
		}
		metadata := make(map[string]any)
		if err = json.Unmarshal([]byte(payload), &metadata); err != nil {
			log.WithError(err).Warnf("postgres store: skipping auth %s with invalid json", id)
			continue
		}
		cliproxyauth.NormalizeCredentialMetadata(metadata)
		if errWeight := cliproxyauth.ValidateAuthWeight(&cliproxyauth.Auth{Metadata: metadata}); errWeight != nil {
			log.WithError(errWeight).Warnf("postgres store: skipping auth %s with invalid weight", id)
			continue
		}
		provider := strings.TrimSpace(valueAsString(metadata["type"]))
		if provider == "" {
			provider = "unknown"
		}
		attr := map[string]string{
			cliproxyauth.AttributePath:          path,
			cliproxyauth.AttributeSourceBackend: cliproxyauth.AuthSourcePostgres,
		}
		if email := strings.TrimSpace(valueAsString(metadata["email"])); email != "" {
			attr["email"] = email
		}
		auth := &cliproxyauth.Auth{
			ID:               normalizeAuthID(id),
			Provider:         provider,
			FileName:         normalizeAuthID(id),
			Label:            labelFor(metadata),
			Status:           cliproxyauth.StatusActive,
			Attributes:       attr,
			Metadata:         metadata,
			CreatedAt:        createdAt,
			UpdatedAt:        updatedAt,
			LastRefreshedAt:  time.Time{},
			NextRefreshAfter: time.Time{},
		}
		cliproxyauth.ApplyCustomHeadersFromMetadata(auth)
		if disabled, ok := metadata["disabled"].(bool); ok && disabled {
			auth.Disabled = true
			auth.Status = cliproxyauth.StatusDisabled
		}
		auths = append(auths, auth)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate auth rows: %w", err)
	}
	return auths, nil
}

// Delete removes an auth file and the corresponding database record.
func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("postgres store: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("postgres store: delete auth file: %w", err)
	}
	relID, err := s.relativeAuthID(path)
	if err != nil {
		return err
	}
	return s.deleteAuthRecord(ctx, relID)
}

// PersistAuthFiles stores the provided auth file changes in PostgreSQL.
func (s *PostgresStore) PersistAuthFiles(ctx context.Context, _ string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		relID, err := s.relativeAuthID(trimmed)
		if err != nil {
			// Attempt to resolve absolute path under authDir.
			abs := trimmed
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(s.authDir, trimmed)
			}
			relID, err = s.relativeAuthID(abs)
			if err != nil {
				log.WithError(err).Warnf("postgres store: ignoring auth path %s", trimmed)
				continue
			}
			trimmed = abs
		}
		if err = s.syncAuthFile(ctx, relID, trimmed); err != nil {
			return err
		}
	}
	return nil
}

// PersistConfig mirrors the local configuration file to PostgreSQL.
func (s *PostgresStore) PersistConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteConfigRecord(ctx)
		}
		return fmt.Errorf("postgres store: read config file: %w", err)
	}
	return s.persistConfig(ctx, data)
}

// syncConfigFromDatabase writes the database-stored config to disk or seeds the database from template.
func (s *PostgresStore) syncConfigFromDatabase(ctx context.Context, exampleConfigPath string) error {
	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	var content string
	err := s.db.QueryRowContext(ctx, query, defaultConfigKey).Scan(&content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, errStat := os.Stat(s.configPath); errors.Is(errStat, fs.ErrNotExist) {
			if exampleConfigPath != "" {
				if errCopy := misc.CopyConfigTemplate(exampleConfigPath, s.configPath); errCopy != nil {
					return fmt.Errorf("postgres store: copy example config: %w", errCopy)
				}
			} else {
				if errCreate := os.MkdirAll(filepath.Dir(s.configPath), 0o700); errCreate != nil {
					return fmt.Errorf("postgres store: prepare config directory: %w", errCreate)
				}
				if errWrite := os.WriteFile(s.configPath, []byte{}, 0o600); errWrite != nil {
					return fmt.Errorf("postgres store: create empty config: %w", errWrite)
				}
			}
		}
		data, errRead := os.ReadFile(s.configPath)
		if errRead != nil {
			return fmt.Errorf("postgres store: read local config: %w", errRead)
		}
		if errPersist := s.persistConfig(ctx, data); errPersist != nil {
			return errPersist
		}
	case err != nil:
		return fmt.Errorf("postgres store: load config from database: %w", err)
	default:
		if err = os.MkdirAll(filepath.Dir(s.configPath), 0o700); err != nil {
			return fmt.Errorf("postgres store: prepare config directory: %w", err)
		}
		normalized := normalizeLineEndings(content)
		if err = os.WriteFile(s.configPath, []byte(normalized), 0o600); err != nil {
			return fmt.Errorf("postgres store: write config to spool: %w", err)
		}
	}
	return nil
}

// syncAuthFromDatabase populates the local auth directory from PostgreSQL data.
func (s *PostgresStore) syncAuthFromDatabase(ctx context.Context) error {
	query := fmt.Sprintf("SELECT id, content FROM %s", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("postgres store: load auth from database: %w", err)
	}
	defer rows.Close()

	if err = os.RemoveAll(s.authDir); err != nil {
		return fmt.Errorf("postgres store: reset auth directory: %w", err)
	}
	if err = os.MkdirAll(s.authDir, 0o700); err != nil {
		return fmt.Errorf("postgres store: recreate auth directory: %w", err)
	}

	for rows.Next() {
		var (
			id      string
			payload string
		)
		if err = rows.Scan(&id, &payload); err != nil {
			return fmt.Errorf("postgres store: scan auth row: %w", err)
		}
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: skipping auth %s outside spool", id)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("postgres store: create auth subdir: %w", err)
		}
		if err = os.WriteFile(path, []byte(payload), 0o600); err != nil {
			return fmt.Errorf("postgres store: write auth file: %w", err)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate auth rows: %w", err)
	}
	return nil
}

func (s *PostgresStore) syncAuthFile(ctx context.Context, relID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteAuthRecord(ctx, relID)
		}
		return fmt.Errorf("postgres store: read auth file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteAuthRecord(ctx, relID)
	}
	return s.persistAuth(ctx, relID, data)
}

func (s *PostgresStore) persistAuth(ctx context.Context, relID string, data []byte) error {
	return s.withAuthLock(ctx, relID, func(conn *sql.Conn) error {
		jsonPayload := json.RawMessage(data)
		query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.AuthTable))
		if _, err := conn.ExecContext(ctx, query, relID, jsonPayload); err != nil {
			return fmt.Errorf("postgres store: upsert auth record: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) deleteAuthRecord(ctx context.Context, relID string) error {
	return s.withAuthLock(ctx, relID, func(conn *sql.Conn) error {
		query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.AuthTable))
		if _, err := conn.ExecContext(ctx, query, relID); err != nil {
			return fmt.Errorf("postgres store: delete auth record: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) withAuthLock(ctx context.Context, relID string, save func(*sql.Conn) error) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("postgres store: acquire auth connection: %w", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, sql.ErrConnDone) {
			err = errors.Join(err, fmt.Errorf("postgres store: close auth connection: %w", errClose))
		}
	}()
	var key int64
	if errKey := conn.QueryRowContext(ctx, "SELECT hashtextextended($1::regclass::oid::text || ':' || $2::text, 0)", s.fullTableName(s.cfg.AuthTable), relID).Scan(&key); errKey != nil {
		return fmt.Errorf("postgres store: resolve auth lock: %w", errKey)
	}
	locked := false
	// The same session must own the lock through commit, publication, and compensation.
	defer func() {
		var unlocked bool
		errUnlock := conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", key).Scan(&unlocked)
		if errUnlock == nil && locked && !unlocked {
			errUnlock = errors.New("auth lock ownership lost")
		}
		if errUnlock != nil {
			err = errors.Join(err, fmt.Errorf("postgres store: unlock auth record: %w", errUnlock))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, errLock := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key); errLock != nil {
		return fmt.Errorf("postgres store: lock auth record: %w", errLock)
	}
	locked = true
	return save(conn)
}

type postgresAuthRecord struct {
	content   []byte
	createdAt time.Time
	updatedAt time.Time
}

func (s *PostgresStore) replaceAuthRecord(ctx context.Context, conn *sql.Conn, relID string, candidate []byte) (previous postgresAuthRecord, previousExists bool, err error) {
	// READ COMMITTED lets the retry observe a row that won ON CONFLICT.
	tx, errBegin := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if errBegin != nil {
		return previous, false, fmt.Errorf("postgres store: begin auth replacement: %w", errBegin)
	}
	defer func() {
		if err == nil {
			return
		}
		if errRollback := tx.Rollback(); errRollback != nil && !errors.Is(errRollback, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("postgres store: rollback auth replacement: %w", errRollback))
		}
	}()

	table := s.fullTableName(s.cfg.AuthTable)
	selectQuery := fmt.Sprintf("SELECT content, created_at, updated_at FROM %s WHERE id = $1 FOR UPDATE", table)
	updateQuery := fmt.Sprintf("UPDATE %s SET content = $2, updated_at = CASE WHEN content = $2 THEN updated_at ELSE NOW() END WHERE id = $1", table)
	insertQuery := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id) DO NOTHING
	`, table)
	for {
		var content string
		errScan := tx.QueryRowContext(ctx, selectQuery, relID).Scan(&content, &previous.createdAt, &previous.updatedAt)
		switch {
		case errScan == nil:
			previous.content = []byte(content)
			result, errUpdate := tx.ExecContext(ctx, updateQuery, relID, json.RawMessage(candidate))
			if errUpdate != nil {
				err = fmt.Errorf("postgres store: replace auth record: %w", errUpdate)
				return previous, false, err
			}
			rows, errRows := result.RowsAffected()
			if errRows != nil {
				err = fmt.Errorf("postgres store: inspect auth replacement: %w", errRows)
				return previous, false, err
			}
			if rows != 1 {
				err = fmt.Errorf("postgres store: locked auth record disappeared before replacement")
				return previous, false, err
			}
			if errCommit := tx.Commit(); errCommit != nil {
				err = fmt.Errorf("postgres store: commit auth replacement: %w", errCommit)
				return previous, false, err
			}
			return previous, true, nil
		case !errors.Is(errScan, sql.ErrNoRows):
			err = fmt.Errorf("postgres store: lock auth record: %w", errScan)
			return previous, false, err
		}

		result, errInsert := tx.ExecContext(ctx, insertQuery, relID, json.RawMessage(candidate))
		if errInsert != nil {
			err = fmt.Errorf("postgres store: insert auth record: %w", errInsert)
			return previous, false, err
		}
		rows, errRows := result.RowsAffected()
		if errRows != nil {
			err = fmt.Errorf("postgres store: inspect auth insert: %w", errRows)
			return previous, false, err
		}
		if rows == 0 {
			continue
		}
		if rows != 1 {
			err = fmt.Errorf("postgres store: inserted %d auth records, want 1", rows)
			return previous, false, err
		}
		if errCommit := tx.Commit(); errCommit != nil {
			err = fmt.Errorf("postgres store: commit auth insert: %w", errCommit)
			return previous, false, err
		}
		return previous, false, nil
	}
}

func (s *PostgresStore) deleteAuthRecordReturning(ctx context.Context, conn *sql.Conn, relID string) (postgresAuthRecord, bool, error) {
	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1 RETURNING content, created_at, updated_at", s.fullTableName(s.cfg.AuthTable))
	var previous postgresAuthRecord
	var content string
	if err := conn.QueryRowContext(ctx, query, relID).Scan(&content, &previous.createdAt, &previous.updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return previous, false, nil
		}
		return previous, false, fmt.Errorf("postgres store: delete auth record: %w", err)
	}
	previous.content = []byte(content)
	return previous, true, nil
}

func (s *PostgresStore) rollbackAuthRecord(ctx context.Context, conn *sql.Conn, relID string, candidate []byte, previous postgresAuthRecord, previousExists bool) error {
	var (
		result sql.Result
		err    error
	)
	if previousExists && len(candidate) == 0 {
		query := fmt.Sprintf(`
			INSERT INTO %s (id, content, created_at, updated_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO NOTHING
		`, s.fullTableName(s.cfg.AuthTable))
		result, err = conn.ExecContext(ctx, query, relID, json.RawMessage(previous.content), previous.createdAt, previous.updatedAt)
	} else if previousExists {
		query := fmt.Sprintf("UPDATE %s SET content = $2, created_at = $4, updated_at = $5 WHERE id = $1 AND content = $3", s.fullTableName(s.cfg.AuthTable))
		result, err = conn.ExecContext(ctx, query, relID, json.RawMessage(previous.content), json.RawMessage(candidate), previous.createdAt, previous.updatedAt)
	} else {
		query := fmt.Sprintf("DELETE FROM %s WHERE id = $1 AND content = $2", s.fullTableName(s.cfg.AuthTable))
		result, err = conn.ExecContext(ctx, query, relID, json.RawMessage(candidate))
	}
	if err != nil {
		return fmt.Errorf("postgres store: rollback auth record: %w", err)
	}
	rows, errRows := result.RowsAffected()
	if errRows != nil {
		return fmt.Errorf("postgres store: inspect auth rollback: %w", errRows)
	}
	if rows != 1 {
		return fmt.Errorf("postgres store: auth record changed before rollback")
	}
	return nil
}

func (s *PostgresStore) persistConfig(ctx context.Context, data []byte) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.ConfigTable))
	normalized := normalizeLineEndings(string(data))
	if _, err := s.db.ExecContext(ctx, query, defaultConfigKey, normalized); err != nil {
		return fmt.Errorf("postgres store: upsert config: %w", err)
	}
	return nil
}

func (s *PostgresStore) deleteConfigRecord(ctx context.Context) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	if _, err := s.db.ExecContext(ctx, query, defaultConfigKey); err != nil {
		return fmt.Errorf("postgres store: delete config: %w", err)
	}
	return nil
}

func (s *PostgresStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		return filepath.Join(s.authDir, fileName), nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("postgres store: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	return filepath.Join(s.authDir, filepath.FromSlash(auth.ID)), nil
}

func (s *PostgresStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	return filepath.Join(s.authDir, filepath.FromSlash(id)), nil
}

func (s *PostgresStore) relativeAuthID(path string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("postgres store: store not initialized")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.authDir, path)
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(s.authDir, clean)
	if err != nil {
		return "", fmt.Errorf("postgres store: compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("postgres store: path %s outside managed directory", path)
	}
	return filepath.ToSlash(rel), nil
}

func (s *PostgresStore) absoluteAuthPath(id string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("postgres store: store not initialized")
	}
	clean := filepath.Clean(filepath.FromSlash(id))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("postgres store: invalid auth identifier %s", id)
	}
	path := filepath.Join(s.authDir, clean)
	rel, err := filepath.Rel(s.authDir, path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("postgres store: resolved auth path escapes auth directory")
	}
	return path, nil
}

func (s *PostgresStore) fullTableName(name string) string {
	if strings.TrimSpace(s.cfg.Schema) == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(s.cfg.Schema) + "." + quoteIdentifier(name)
}

func quoteIdentifier(identifier string) string {
	replaced := strings.ReplaceAll(identifier, "\"", "\"\"")
	return "\"" + replaced + "\""
}

func valueAsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v := strings.TrimSpace(valueAsString(metadata["label"])); v != "" {
		return v
	}
	if v := strings.TrimSpace(valueAsString(metadata["email"])); v != "" {
		return v
	}
	if v := strings.TrimSpace(valueAsString(metadata["project_id"])); v != "" {
		return v
	}
	return ""
}

func normalizeAuthID(id string) string {
	return filepath.ToSlash(filepath.Clean(id))
}

func normalizeLineEndings(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}
