package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var _ cliproxyauth.CooldownStateStoreProvider = (*PostgresStore)(nil)
var _ cliproxyauth.CooldownStateStore = (*postgresCooldownStateStore)(nil)

type postgresCooldownStateKey struct {
	authID string
	model  string
}

type postgresCooldownStateRecord struct {
	key       postgresCooldownStateKey
	content   []byte
	updatedAt time.Time
}

type postgresCooldownStateVersion struct {
	updatedAt time.Time
}

type postgresCooldownStateStore struct {
	store    *PostgresStore
	mu       sync.Mutex
	previous map[postgresCooldownStateKey]postgresCooldownStateVersion
}

// CooldownStateStore returns the PostgreSQL-backed runtime cooldown store.
func (s *PostgresStore) CooldownStateStore() cliproxyauth.CooldownStateStore {
	if s == nil {
		return nil
	}
	return s.cooldownStore
}

func (s *postgresCooldownStateStore) Load(ctx context.Context) (records []cliproxyauth.CooldownStateRecord, err error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, fmt.Errorf("postgres cooldown store: not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	table := s.store.fullTableName(s.store.cfg.CooldownTable)
	query := fmt.Sprintf("SELECT content, updated_at FROM %s WHERE deleted = FALSE", table)
	rows, errQuery := s.store.db.QueryContext(ctx, query)
	if errQuery != nil {
		return nil, fmt.Errorf("postgres cooldown store: load state: %w", errQuery)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			err = errors.Join(err, fmt.Errorf("postgres cooldown store: close state rows: %w", errClose))
		}
	}()

	records = make([]cliproxyauth.CooldownStateRecord, 0)
	previous := make(map[postgresCooldownStateKey]postgresCooldownStateVersion)
	for rows.Next() {
		var content []byte
		var updatedAt time.Time
		if errScan := rows.Scan(&content, &updatedAt); errScan != nil {
			return nil, fmt.Errorf("postgres cooldown store: scan state: %w", errScan)
		}
		var record cliproxyauth.CooldownStateRecord
		if errUnmarshal := json.Unmarshal(content, &record); errUnmarshal != nil {
			return nil, fmt.Errorf("postgres cooldown store: decode state: %w", errUnmarshal)
		}
		key := cooldownStateKey(record)
		if key.authID == "" {
			return nil, fmt.Errorf("postgres cooldown store: decoded state has empty auth ID")
		}
		records = append(records, record)
		previous[key] = postgresCooldownStateVersion{updatedAt: updatedAt}
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("postgres cooldown store: iterate state: %w", errRows)
	}
	s.previous = previous
	return records, nil
}

func (s *postgresCooldownStateStore) Save(ctx context.Context, records []cliproxyauth.CooldownStateRecord) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return fmt.Errorf("postgres cooldown store: not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := normalizePostgresCooldownTime(time.Now(), time.Time{})
	current := make(map[postgresCooldownStateKey]postgresCooldownStateVersion, len(records))
	encoded := make([]postgresCooldownStateRecord, 0, len(records))
	for i := range records {
		record := records[i]
		key := cooldownStateKey(record)
		if key.authID == "" {
			return fmt.Errorf("postgres cooldown store: state has empty auth ID")
		}
		record.UpdatedAt = normalizePostgresCooldownTime(record.UpdatedAt, now)
		content, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			return fmt.Errorf("postgres cooldown store: encode state for %q: %w", key.authID, errMarshal)
		}
		current[key] = postgresCooldownStateVersion{updatedAt: record.UpdatedAt}
		encoded = append(encoded, postgresCooldownStateRecord{key: key, content: content, updatedAt: record.UpdatedAt})
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, errBegin := s.store.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("postgres cooldown store: begin save: %w", errBegin)
	}
	table := s.store.fullTableName(s.store.cfg.CooldownTable)
	upsertQuery := fmt.Sprintf(`
		INSERT INTO %s AS target (auth_id, model, content, deleted, created_at, updated_at)
		VALUES ($1, $2, $3, FALSE, NOW(), $4)
		ON CONFLICT (auth_id, model) DO UPDATE SET
			content = EXCLUDED.content,
			deleted = FALSE,
			updated_at = EXCLUDED.updated_at
		WHERE target.updated_at <= EXCLUDED.updated_at
	`, table)
	for i := range encoded {
		record := encoded[i]
		if _, errExec := tx.ExecContext(ctx, upsertQuery, record.key.authID, record.key.model, record.content, record.updatedAt); errExec != nil {
			return rollbackPostgresCooldownTransaction(tx, fmt.Errorf("postgres cooldown store: save state for %q: %w", record.key.authID, errExec))
		}
	}
	deleteQuery := fmt.Sprintf(`
		INSERT INTO %s AS target (auth_id, model, content, deleted, created_at, updated_at)
		VALUES ($1, $2, $3, TRUE, NOW(), $4)
		ON CONFLICT (auth_id, model) DO UPDATE SET
			content = EXCLUDED.content,
			deleted = TRUE,
			updated_at = EXCLUDED.updated_at
		WHERE NOT target.deleted AND target.updated_at <= $5
	`, table)
	for key, previous := range s.previous {
		if _, ok := current[key]; ok {
			continue
		}
		deletedAt := now
		if !deletedAt.After(previous.updatedAt) {
			deletedAt = previous.updatedAt.Add(time.Microsecond)
		}
		if _, errExec := tx.ExecContext(ctx, deleteQuery, key.authID, key.model, []byte(`{}`), deletedAt, previous.updatedAt); errExec != nil {
			return rollbackPostgresCooldownTransaction(tx, fmt.Errorf("postgres cooldown store: clear state for %q: %w", key.authID, errExec))
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("postgres cooldown store: commit save: %w", errCommit)
	}
	s.previous = current
	return nil
}

func cooldownStateKey(record cliproxyauth.CooldownStateRecord) postgresCooldownStateKey {
	return postgresCooldownStateKey{
		authID: strings.TrimSpace(record.AuthID),
		model:  strings.TrimSpace(record.Model),
	}
}

func normalizePostgresCooldownTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		value = fallback
	}
	return value.UTC().Truncate(time.Microsecond)
}

func rollbackPostgresCooldownTransaction(tx *sql.Tx, operationErr error) error {
	if errRollback := tx.Rollback(); errRollback != nil && !errors.Is(errRollback, sql.ErrTxDone) {
		return errors.Join(operationErr, fmt.Errorf("postgres cooldown store: rollback save: %w", errRollback))
	}
	return operationErr
}
