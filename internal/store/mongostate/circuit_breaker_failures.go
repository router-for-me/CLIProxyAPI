package mongostate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	DefaultCircuitBreakerFailureStateCollection = "circuit_breaker_failure_states"
	DefaultCircuitBreakerFailureEventCollection = "circuit_breaker_failure_events"
	DefaultCircuitBreakerFailureEventTTLDays    = 30
)

// CircuitBreakerFailureStore persists auth+model failure counts and audit events.
type CircuitBreakerFailureStore struct {
	client              *mongo.Client
	states              *mongo.Collection
	events              *mongo.Collection
	operationTimeoutSec int
}

// NewCircuitBreakerFailureStore creates the strong-consistency failure store.
func NewCircuitBreakerFailureStore(ctx context.Context, cfg StoreConfig, stateCollection string, eventCollection string, ttlDays int) (*CircuitBreakerFailureStore, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("mongostate: URI is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("mongostate: database is required")
	}
	if strings.TrimSpace(stateCollection) == "" {
		stateCollection = DefaultCircuitBreakerFailureStateCollection
	}
	if strings.TrimSpace(eventCollection) == "" {
		eventCollection = DefaultCircuitBreakerFailureEventCollection
	}
	if ttlDays <= 0 {
		ttlDays = DefaultCircuitBreakerFailureEventTTLDays
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
		return nil, fmt.Errorf("mongostate: connect circuit breaker failure store: %w", err)
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, time.Duration(cfg.OperationTimeoutSec)*time.Second)
	defer pingCancel()
	if err = client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongostate: ping circuit breaker failure store: %w", err)
	}

	db := client.Database(cfg.Database)
	store := &CircuitBreakerFailureStore{
		client:              client,
		states:              db.Collection(stateCollection),
		events:              db.Collection(eventCollection),
		operationTimeoutSec: cfg.OperationTimeoutSec,
	}
	if err = store.ensureIndexes(ctx, ttlDays); err != nil {
		_ = store.Close(context.Background())
		return nil, err
	}
	return store, nil
}

func (s *CircuitBreakerFailureStore) ensureIndexes(ctx context.Context, ttlDays int) error {
	if s == nil || s.states == nil || s.events == nil {
		return fmt.Errorf("mongostate: circuit breaker failure store not initialized")
	}
	if ttlDays <= 0 {
		ttlDays = DefaultCircuitBreakerFailureEventTTLDays
	}
	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	if _, err := s.states.Indexes().CreateMany(opCtx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "provider", Value: 1},
				{Key: "auth_id", Value: 1},
				{Key: "normalized_model", Value: 1},
			},
			Options: options.Index().SetName("unique_provider_auth_model").SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "normalized_model", Value: 1}, {Key: "consecutive_failures", Value: -1}},
			Options: options.Index().SetName("query_model_failures"),
		},
	}); err != nil {
		return fmt.Errorf("mongostate: ensure circuit breaker failure state indexes: %w", err)
	}

	ttlSeconds := int32(ttlDays * 24 * 60 * 60)
	if _, err := s.events.Indexes().CreateMany(opCtx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "created_at", Value: 1}},
			Options: options.Index().SetName("ttl_created_at").SetExpireAfterSeconds(ttlSeconds),
		},
		{
			Keys: bson.D{
				{Key: "provider", Value: 1},
				{Key: "auth_id", Value: 1},
				{Key: "normalized_model", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("query_provider_auth_model_created"),
		},
	}); err != nil {
		return fmt.Errorf("mongostate: ensure circuit breaker failure event indexes: %w", err)
	}
	return nil
}

// getFailureCountsWithRetry invokes readFunc and retries once on failure when the context is still active.
func getFailureCountsWithRetry(ctx context.Context, model string, readFunc func(context.Context, string) (map[string]int, error)) (map[string]int, error) {
	counts, err := readFunc(ctx, model)
	if err == nil {
		return counts, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	// First attempt failed; retry once.
	return readFunc(ctx, model)
}

// GetFailureCounts returns consecutive failure counts keyed by auth ID for a normalized model.
func (s *CircuitBreakerFailureStore) GetFailureCounts(ctx context.Context, model string) (map[string]int, error) {
	if s == nil || s.states == nil {
		return nil, fmt.Errorf("mongostate: circuit breaker failure store not initialized")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return map[string]int{}, nil
	}
	return getFailureCountsWithRetry(ctx, model, s.getFailureCountsOnce)
}

func (s *CircuitBreakerFailureStore) getFailureCountsOnce(ctx context.Context, model string) (map[string]int, error) {
	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	cursor, err := s.states.Find(opCtx, bson.M{"normalized_model": model, "consecutive_failures": bson.M{"$gt": 0}})
	if err != nil {
		return nil, fmt.Errorf("mongostate: query circuit breaker failure states: %w", err)
	}
	defer cursor.Close(opCtx)

	out := make(map[string]int)
	for cursor.Next(opCtx) {
		var doc struct {
			AuthID              string `bson:"auth_id"`
			ConsecutiveFailures int    `bson:"consecutive_failures"`
		}
		if errDecode := cursor.Decode(&doc); errDecode != nil {
			return nil, fmt.Errorf("mongostate: decode circuit breaker failure state: %w", errDecode)
		}
		if doc.AuthID != "" && doc.ConsecutiveFailures > 0 {
			out[doc.AuthID] = doc.ConsecutiveFailures
		}
	}
	if errCursor := cursor.Err(); errCursor != nil {
		return nil, fmt.Errorf("mongostate: iterate circuit breaker failure states: %w", errCursor)
	}
	return out, nil
}

// RecordFailure increments the auth+model failure state and appends one audit event.
func (s *CircuitBreakerFailureStore) RecordFailure(ctx context.Context, event coreauth.CircuitBreakerFailureEvent) (coreauth.CircuitBreakerFailureState, error) {
	if s == nil || s.states == nil || s.events == nil {
		return coreauth.CircuitBreakerFailureState{}, fmt.Errorf("mongostate: circuit breaker failure store not initialized")
	}
	event.Provider = strings.ToLower(strings.TrimSpace(event.Provider))
	event.AuthID = strings.TrimSpace(event.AuthID)
	event.Model = strings.TrimSpace(event.Model)
	event.NormalizedModel = strings.TrimSpace(event.NormalizedModel)
	if event.NormalizedModel == "" {
		event.NormalizedModel = event.Model
	}
	if event.Provider == "" || event.AuthID == "" || event.NormalizedModel == "" {
		return coreauth.CircuitBreakerFailureState{}, fmt.Errorf("mongostate: provider, auth_id, and normalized_model are required")
	}
	now := time.Now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}

	state, err := s.recordFailureOnce(ctx, event)
	if err == nil {
		return state, nil
	}
	if ctx.Err() != nil {
		return state, err
	}
	// First attempt failed; retry once.
	return s.recordFailureOnce(ctx, event)
}

func (s *CircuitBreakerFailureStore) recordFailureOnce(ctx context.Context, event coreauth.CircuitBreakerFailureEvent) (coreauth.CircuitBreakerFailureState, error) {
	now := time.Now().UTC()

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	filter := bson.M{
		"provider":         event.Provider,
		"auth_id":          event.AuthID,
		"normalized_model": event.NormalizedModel,
	}
	update := bson.M{
		"$inc": bson.M{"consecutive_failures": 1},
		"$set": bson.M{
			"provider":         event.Provider,
			"auth_id":          event.AuthID,
			"model":            event.Model,
			"normalized_model": event.NormalizedModel,
			"last_reason":      event.Reason,
			"last_http_status": event.HTTPStatus,
			"last_failed_at":   event.CreatedAt,
			"updated_at":       now,
		},
		"$setOnInsert": bson.M{"created_at": now},
	}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)

	var stateDoc struct {
		Provider            string    `bson:"provider"`
		AuthID              string    `bson:"auth_id"`
		Model               string    `bson:"model"`
		NormalizedModel     string    `bson:"normalized_model"`
		ConsecutiveFailures int       `bson:"consecutive_failures"`
		LastReason          string    `bson:"last_reason"`
		LastHTTPStatus      int       `bson:"last_http_status"`
		LastFailedAt        time.Time `bson:"last_failed_at"`
		UpdatedAt           time.Time `bson:"updated_at"`
	}
	if err := s.states.FindOneAndUpdate(opCtx, filter, update, opts).Decode(&stateDoc); err != nil {
		return coreauth.CircuitBreakerFailureState{}, fmt.Errorf("mongostate: update circuit breaker failure state: %w", err)
	}

	event.ConsecutiveFailures = stateDoc.ConsecutiveFailures
	event.CircuitOpened = stateDoc.ConsecutiveFailures >= registry.DefaultCircuitBreakerFailureThreshold
	if _, err := s.events.InsertOne(opCtx, event); err != nil {
		return coreauth.CircuitBreakerFailureState{}, fmt.Errorf("mongostate: insert circuit breaker failure event: %w", err)
	}

	return coreauth.CircuitBreakerFailureState{
		Provider:            stateDoc.Provider,
		AuthID:              stateDoc.AuthID,
		Model:               stateDoc.Model,
		NormalizedModel:     stateDoc.NormalizedModel,
		ConsecutiveFailures: stateDoc.ConsecutiveFailures,
		LastReason:          stateDoc.LastReason,
		LastHTTPStatus:      stateDoc.LastHTTPStatus,
		LastFailedAt:        stateDoc.LastFailedAt,
		UpdatedAt:           stateDoc.UpdatedAt,
	}, nil
}

// ResetFailure clears consecutive failure state for a successful auth+model request.
func (s *CircuitBreakerFailureStore) ResetFailure(ctx context.Context, provider, authID, model string) error {
	if s == nil || s.states == nil {
		return fmt.Errorf("mongostate: circuit breaker failure store not initialized")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	if provider == "" || authID == "" || model == "" {
		return nil
	}

	err := s.resetFailureOnce(ctx, provider, authID, model)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return err
	}
	// First attempt failed; retry once.
	return s.resetFailureOnce(ctx, provider, authID, model)
}

func (s *CircuitBreakerFailureStore) resetFailureOnce(ctx context.Context, provider, authID, model string) error {
	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()
	_, err := s.states.DeleteOne(opCtx, bson.M{
		"provider":         provider,
		"auth_id":          authID,
		"normalized_model": model,
	})
	if err != nil {
		return fmt.Errorf("mongostate: reset circuit breaker failure state: %w", err)
	}
	return nil
}

// Close disconnects the MongoDB client.
func (s *CircuitBreakerFailureStore) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}
