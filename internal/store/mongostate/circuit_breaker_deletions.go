package mongostate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	DefaultCircuitBreakerDeletionCollection = "circuit_breaker_model_deletions"
	DefaultCircuitBreakerDeletionTTLDays    = 30
)

// CircuitBreakerDeletionRecord captures one automatic model-removal action
// triggered by circuit breaker OPEN cycles.
type CircuitBreakerDeletionRecord struct {
	ID                  primitive.ObjectID `bson:"_id,omitempty"`
	AuthID              string             `bson:"auth_id"`
	Provider            string             `bson:"provider"`
	Model               string             `bson:"model"`
	NormalizedModel     string             `bson:"normalized_model"`
	OpenCycles          int                `bson:"open_cycles"`
	FailureCount        int                `bson:"failure_count"`
	ConsecutiveFailures int                `bson:"consecutive_failures"`
	OpenedAt            time.Time          `bson:"opened_at"`
	RecoveryAt          *time.Time         `bson:"recovery_at,omitempty"`
	Persisted           bool               `bson:"persisted"`
	AlreadyRemoved      bool               `bson:"already_removed"`
	RuntimeSuspended    bool               `bson:"runtime_suspended"`
	PersistError        string             `bson:"persist_error,omitempty"`
	CreatedAt           time.Time          `bson:"created_at"`
}

// CircuitBreakerDeletionItem is the API-facing shape returned by management query endpoints.
type CircuitBreakerDeletionItem struct {
	ID                  string     `json:"id"`
	AuthID              string     `json:"auth_id"`
	Provider            string     `json:"provider"`
	Model               string     `json:"model"`
	NormalizedModel     string     `json:"normalized_model"`
	OpenCycles          int        `json:"open_cycles"`
	FailureCount        int        `json:"failure_count"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	OpenedAt            time.Time  `json:"opened_at"`
	RecoveryAt          *time.Time `json:"recovery_at,omitempty"`
	Persisted           bool       `json:"persisted"`
	AlreadyRemoved      bool       `json:"already_removed"`
	RuntimeSuspended    bool       `json:"runtime_suspended"`
	PersistError        string     `json:"persist_error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

// CircuitBreakerDeletionQuery defines filters and pagination for audit query.
type CircuitBreakerDeletionQuery struct {
	Provider string
	AuthID   string
	Model    string
	Start    time.Time
	End      time.Time
	Page     int
	PageSize int
}

// CircuitBreakerDeletionQueryResult is the paged query result.
type CircuitBreakerDeletionQueryResult struct {
	Items    []CircuitBreakerDeletionItem `json:"items"`
	Total    int64                        `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
}

// CircuitBreakerDeletionStore persists and queries circuit-breaker model deletion audits.
type CircuitBreakerDeletionStore struct {
	client              *mongo.Client
	collection          *mongo.Collection
	operationTimeoutSec int
}

// CircuitBreakerDeletionQuerier describes the query capability required by management APIs.
type CircuitBreakerDeletionQuerier interface {
	Query(ctx context.Context, query CircuitBreakerDeletionQuery) (CircuitBreakerDeletionQueryResult, error)
}

var (
	globalCircuitBreakerDeletionStore   CircuitBreakerDeletionQuerier
	globalCircuitBreakerDeletionStoreMu sync.RWMutex
)

// SetGlobalCircuitBreakerDeletionStore sets the global audit store used by service and management handlers.
func SetGlobalCircuitBreakerDeletionStore(store CircuitBreakerDeletionQuerier) {
	globalCircuitBreakerDeletionStoreMu.Lock()
	defer globalCircuitBreakerDeletionStoreMu.Unlock()
	globalCircuitBreakerDeletionStore = store
}

// GetGlobalCircuitBreakerDeletionStore returns the global audit store.
func GetGlobalCircuitBreakerDeletionStore() CircuitBreakerDeletionQuerier {
	globalCircuitBreakerDeletionStoreMu.RLock()
	defer globalCircuitBreakerDeletionStoreMu.RUnlock()
	return globalCircuitBreakerDeletionStore
}

// NewCircuitBreakerDeletionStore creates the audit store and ensures indexes.
func NewCircuitBreakerDeletionStore(ctx context.Context, cfg StoreConfig, collection string, ttlDays int) (*CircuitBreakerDeletionStore, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("mongostate: URI is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("mongostate: database is required")
	}
	if strings.TrimSpace(collection) == "" {
		collection = DefaultCircuitBreakerDeletionCollection
	}
	if ttlDays <= 0 {
		ttlDays = DefaultCircuitBreakerDeletionTTLDays
	}
	if cfg.ConnectTimeoutSec <= 0 {
		cfg.ConnectTimeoutSec = 10
	}
	if cfg.OperationTimeoutSec <= 0 {
		cfg.OperationTimeoutSec = 5
	}

	connectCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.ConnectTimeoutSec)*time.Second)
	defer cancel()
	client, err := mongo.Connect(connectCtx, options.Client().ApplyURI(cfg.URI))
	if err != nil {
		return nil, fmt.Errorf("mongostate: connect deletion store: %w", err)
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, time.Duration(cfg.OperationTimeoutSec)*time.Second)
	defer pingCancel()
	if err = client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongostate: ping deletion store: %w", err)
	}

	store := &CircuitBreakerDeletionStore{
		client:              client,
		collection:          client.Database(cfg.Database).Collection(collection),
		operationTimeoutSec: cfg.OperationTimeoutSec,
	}
	if err = store.ensureIndexes(ctx, ttlDays); err != nil {
		_ = store.Close(context.Background())
		return nil, err
	}
	return store, nil
}

func (s *CircuitBreakerDeletionStore) ensureIndexes(ctx context.Context, ttlDays int) error {
	if s == nil || s.collection == nil {
		return fmt.Errorf("mongostate: deletion store not initialized")
	}
	if ttlDays <= 0 {
		ttlDays = DefaultCircuitBreakerDeletionTTLDays
	}
	ttlSeconds := int32(ttlDays * 24 * 60 * 60)

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	models := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "created_at", Value: 1}},
			Options: options.Index().SetName("ttl_created_at").SetExpireAfterSeconds(ttlSeconds),
		},
		{
			Keys:    bson.D{{Key: "provider", Value: 1}, {Key: "auth_id", Value: 1}, {Key: "model", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("query_provider_auth_model_created"),
		},
	}
	if _, err := s.collection.Indexes().CreateMany(opCtx, models); err != nil {
		return fmt.Errorf("mongostate: ensure deletion indexes: %w", err)
	}
	return nil
}

// Insert writes one deletion audit record.
func (s *CircuitBreakerDeletionStore) Insert(ctx context.Context, record *CircuitBreakerDeletionRecord) error {
	if s == nil || s.collection == nil {
		return fmt.Errorf("mongostate: deletion store not initialized")
	}
	if record == nil {
		return fmt.Errorf("mongostate: deletion record is nil")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	_, err := s.collection.InsertOne(opCtx, record)
	if err != nil {
		return fmt.Errorf("mongostate: insert deletion record: %w", err)
	}
	return nil
}

// Query lists deletion audit records with filters and pagination.
func (s *CircuitBreakerDeletionStore) Query(ctx context.Context, query CircuitBreakerDeletionQuery) (CircuitBreakerDeletionQueryResult, error) {
	result := CircuitBreakerDeletionQueryResult{}
	if s == nil || s.collection == nil {
		return result, fmt.Errorf("mongostate: deletion store not initialized")
	}

	if query.Page <= 0 {
		query.Page = 1
	}
	if query.PageSize <= 0 {
		query.PageSize = 20
	}
	if query.PageSize > 200 {
		query.PageSize = 200
	}

	filter := bson.M{}
	if v := strings.TrimSpace(query.Provider); v != "" {
		filter["provider"] = strings.ToLower(v)
	}
	if v := strings.TrimSpace(query.AuthID); v != "" {
		filter["auth_id"] = v
	}
	if v := strings.TrimSpace(query.Model); v != "" {
		filter["model"] = v
	}
	if !query.Start.IsZero() || !query.End.IsZero() {
		timeFilter := bson.M{}
		if !query.Start.IsZero() {
			timeFilter["$gte"] = query.Start.UTC()
		}
		if !query.End.IsZero() {
			timeFilter["$lte"] = query.End.UTC()
		}
		filter["created_at"] = timeFilter
	}

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	total, err := s.collection.CountDocuments(opCtx, filter)
	if err != nil {
		return result, fmt.Errorf("mongostate: count deletion records: %w", err)
	}

	skip := int64((query.Page - 1) * query.PageSize)
	findOpts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(skip).
		SetLimit(int64(query.PageSize))

	cursor, err := s.collection.Find(opCtx, filter, findOpts)
	if err != nil {
		return result, fmt.Errorf("mongostate: find deletion records: %w", err)
	}
	defer cursor.Close(opCtx)

	items := make([]CircuitBreakerDeletionItem, 0, query.PageSize)
	for cursor.Next(opCtx) {
		var doc CircuitBreakerDeletionRecord
		if err := cursor.Decode(&doc); err != nil {
			return result, fmt.Errorf("mongostate: decode deletion record: %w", err)
		}
		item := CircuitBreakerDeletionItem{
			ID:                  doc.ID.Hex(),
			AuthID:              doc.AuthID,
			Provider:            doc.Provider,
			Model:               doc.Model,
			NormalizedModel:     doc.NormalizedModel,
			OpenCycles:          doc.OpenCycles,
			FailureCount:        doc.FailureCount,
			ConsecutiveFailures: doc.ConsecutiveFailures,
			OpenedAt:            doc.OpenedAt,
			RecoveryAt:          doc.RecoveryAt,
			Persisted:           doc.Persisted,
			AlreadyRemoved:      doc.AlreadyRemoved,
			RuntimeSuspended:    doc.RuntimeSuspended,
			PersistError:        doc.PersistError,
			CreatedAt:           doc.CreatedAt,
		}
		items = append(items, item)
	}
	if err := cursor.Err(); err != nil {
		return result, fmt.Errorf("mongostate: iterate deletion records: %w", err)
	}

	result.Items = items
	result.Total = total
	result.Page = query.Page
	result.PageSize = query.PageSize
	return result, nil
}

// Close disconnects the Mongo client.
func (s *CircuitBreakerDeletionStore) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}
