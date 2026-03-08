package db

import "embed"

//go:embed migrations/*.sql
var MigrationFS embed.FS

// MigrationDialect 区分 SQL 方言
type MigrationDialect string

const (
	DialectSQLite   MigrationDialect = "sqlite"
	DialectPostgres MigrationDialect = "postgres"
)
