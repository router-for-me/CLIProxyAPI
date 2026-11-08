package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	log "github.com/sirupsen/logrus"
)

func (s *PostgresStore) refreshTable() string {
	name := s.cfg.AuthTable
	if name == "" {
		name = defaultAuthTable
	}
	return s.fullTableName(name + "_refresh_journal")
}

// RefreshClaim uses a unique database key, independent of disposable auth mirrors.
func (s *PostgresStore) RefreshClaim(ctx context.Context, key string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("postgres refresh journal is unavailable")
	}
	if errSchema := s.ensureRefreshSchema(ctx); errSchema != nil {
		return false, errSchema
	}
	table := s.refreshTable()
	result, errInsert := s.db.ExecContext(ctx, "INSERT INTO "+table+" (id) VALUES ($1) ON CONFLICT (id) DO NOTHING", key)
	if errInsert != nil {
		return false, errInsert
	}
	count, errCount := result.RowsAffected()
	return count == 1, errCount
}

// Bootstrap prepares the table before refresh workers start. The lazy path also
// supports embedded callers. A database-wide transaction lock serializes DDL
// across instances, including differently-spelled schema/search_path aliases.
// Only successful initialization is cached, so temporary failures can recover.
func (s *PostgresStore) ensureRefreshSchema(ctx context.Context) error {
	s.refreshSchemaMu.Lock()
	defer s.refreshSchemaMu.Unlock()
	if s.refreshSchemaReady {
		return nil
	}
	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("postgres refresh schema: begin: %w", errBegin)
	}
	defer func() {
		if errRollback := tx.Rollback(); errRollback != nil && !errors.Is(errRollback, sql.ErrTxDone) {
			log.Warnf("postgres refresh schema: rollback: %v", errRollback)
		}
	}()
	if _, errLock := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('cliproxyapi:codex-refresh-schema')::bigint)"); errLock != nil {
		return fmt.Errorf("postgres refresh schema: lock: %w", errLock)
	}
	if _, errCreate := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS "+s.refreshTable()+" (id TEXT PRIMARY KEY, receipt BYTEA)"); errCreate != nil {
		return fmt.Errorf("postgres refresh schema: create: %w", errCreate)
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("postgres refresh schema: commit: %w", errCommit)
	}
	s.refreshSchemaReady = true
	return nil
}

func (s *PostgresStore) RefreshRead(ctx context.Context, key string) ([]byte, error) {
	var receipt []byte
	errRead := s.db.QueryRowContext(ctx, "SELECT receipt FROM "+s.refreshTable()+" WHERE id = $1", key).Scan(&receipt)
	if errors.Is(errRead, sql.ErrNoRows) {
		return nil, nil
	}
	if errRead == nil && receipt == nil {
		return []byte(codex.PendingRefreshReceipt), nil
	}
	return receipt, errRead
}

func (s *PostgresStore) RefreshComplete(ctx context.Context, key string, receipt []byte) error {
	result, errUpdate := s.db.ExecContext(ctx, "UPDATE "+s.refreshTable()+" SET receipt = $2 WHERE id = $1 AND receipt IS NULL", key, receipt)
	if errUpdate != nil {
		return errUpdate
	}
	count, errCount := result.RowsAffected()
	if errCount != nil {
		return errCount
	}
	if count != 1 {
		return fmt.Errorf("refresh receipt already completed or claim missing")
	}
	return nil
}
