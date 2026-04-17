package mongostate

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// SchemaVersion is the current version of the persisted state document schema.
const SchemaVersion = 1

// RuntimeStateDoc represents the root document stored in MongoDB.
// It holds a snapshot of both circuit-breaker state and usage statistics.
type RuntimeStateDoc struct {
	// ID is the document identifier, always "default" (single-instance pattern).
	ID                     string                                  `bson:"_id"`
	SchemaVersion          int                                     `bson:"schema_version"`
	UpdatedAt              time.Time                               `bson:"updated_at"`
	CircuitBreakerSnapshot map[string]map[string]registry.CircuitBreakerPersistStatus `bson:"circuit_breaker_snapshot"`
	UsageSnapshot          usage.StatisticsSnapshot                 `bson:"usage_snapshot"`
}

// StateMetaDoc records schema migration metadata for the state store.
type StateMetaDoc struct {
	ID              string    `bson:"_id"`
	SchemaVersion   int       `bson:"schema_version"`
	LastMigrationAt time.Time `bson:"last_migration_at"`
	LastWriter      string    `bson:"last_writer"`
	LastWriteAt     time.Time `bson:"last_write_at"`
}
