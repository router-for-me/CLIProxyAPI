// Package accountpool provides data access for the account pool management feature.
// It manages member accounts, leader accounts, proxies, and groups using PostgreSQL.
package accountpool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Store provides data access for account pool entities.
type Store struct {
	db     *sql.DB
	schema string
}

// New creates a new account pool Store.
func New(db *sql.DB, schema string) *Store {
	return &Store{db: db, schema: schema}
}

// EnsureSchema creates all required tables if they do not exist.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("account pool store: not initialized")
	}

	if schema := strings.TrimSpace(s.schema); schema != "" {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))); err != nil {
			return fmt.Errorf("account pool store: create schema: %w", err)
		}
	}

	tables := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id            BIGSERIAL PRIMARY KEY,
			email         TEXT NOT NULL UNIQUE,
			password      TEXT NOT NULL,
			recovery_email TEXT NOT NULL DEFAULT '',
			totp_secret   TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'available',
			nstbrowser_profile_id   TEXT NOT NULL DEFAULT '',
			nstbrowser_profile_name TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, s.tableName("account_pool_members")),

		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_apm_status ON %s(status)`,
			s.tableName("account_pool_members")),

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id            BIGSERIAL PRIMARY KEY,
			email         TEXT NOT NULL UNIQUE,
			password      TEXT NOT NULL,
			recovery_email TEXT NOT NULL DEFAULT '',
			totp_secret   TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'available',
			nstbrowser_profile_id   TEXT NOT NULL DEFAULT '',
			nstbrowser_profile_name TEXT NOT NULL DEFAULT '',
			ultra_subscription_expiry TIMESTAMPTZ,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, s.tableName("account_pool_leaders")),

		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_apl_status ON %s(status)`,
			s.tableName("account_pool_leaders")),

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id         BIGSERIAL PRIMARY KEY,
			proxy_url  TEXT NOT NULL UNIQUE,
			type       TEXT NOT NULL DEFAULT 'member',
			status     TEXT NOT NULL DEFAULT 'available',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, s.tableName("account_pool_proxies")),

		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_app_status_type ON %s(status, type)`,
			s.tableName("account_pool_proxies")),

		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id            BIGSERIAL PRIMARY KEY,
			group_id      TEXT NOT NULL,
			date          DATE NOT NULL DEFAULT CURRENT_DATE,
			leader_email  TEXT NOT NULL,
			member_email  TEXT NOT NULL DEFAULT '',
			family_status TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(group_id, member_email)
		)`, s.tableName("account_pool_groups")),
	}

	for _, ddl := range tables {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("account pool store: execute DDL: %w", err)
		}
	}
	return nil
}

// tableName returns the fully qualified table name.
func (s *Store) tableName(name string) string {
	if schema := strings.TrimSpace(s.schema); schema != "" {
		return quoteIdentifier(schema) + "." + quoteIdentifier(name)
	}
	return quoteIdentifier(name)
}

// quoteIdentifier quotes a SQL identifier to prevent injection.
func quoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
