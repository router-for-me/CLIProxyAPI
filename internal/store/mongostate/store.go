package mongostate

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// StoreConfig captures configuration for the MongoDB state store.
type StoreConfig struct {
	// URI is the MongoDB connection URI (including credentials).
	URI string
	// Database is the target database name.
	Database string
	// Collection is the snapshots collection name.
	Collection string
	// ConnectTimeoutSec is the connection establishment timeout in seconds.
	ConnectTimeoutSec int
	// OperationTimeoutSec is the per-operation timeout in seconds.
	OperationTimeoutSec int
	// FlushIntervalSec is the background flush interval in seconds.
	FlushIntervalSec int
	// InstanceID is a unique identifier for this running instance (used in logging).
	InstanceID string
}

// NewStoreConfig builds a StoreConfig from the generic mongo config section.
func NewStoreConfig(uri, database, collection string, connectTimeoutSec, operationTimeoutSec, flushIntervalSec int, instanceID string) StoreConfig {
	if connectTimeoutSec <= 0 {
		connectTimeoutSec = 10
	}
	if operationTimeoutSec <= 0 {
		operationTimeoutSec = 5
	}
	if flushIntervalSec <= 0 {
		flushIntervalSec = 30
	}
	return StoreConfig{
		URI:                 uri,
		Database:            database,
		Collection:          collection,
		ConnectTimeoutSec:   connectTimeoutSec,
		OperationTimeoutSec: operationTimeoutSec,
		FlushIntervalSec:    flushIntervalSec,
		InstanceID:          instanceID,
	}
}

// MongoStore persists and loads runtime state snapshots via MongoDB.
type MongoStore struct {
	client     *mongo.Client
	collection *mongo.Collection
	cfg        StoreConfig
}

// NewMongoStore creates a new MongoDB-backed state store.
func NewMongoStore(ctx context.Context, cfg StoreConfig) (*MongoStore, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("mongostate: URI is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("mongostate: database is required")
	}
	if cfg.Collection == "" {
		cfg.Collection = "service_state_snapshots"
	}

	connectCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.ConnectTimeoutSec)*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(cfg.URI)
	client, err := mongo.Connect(connectCtx, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("mongostate: connect: %w", err)
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, time.Duration(cfg.OperationTimeoutSec)*time.Second)
	defer pingCancel()
	if err = client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongostate: ping: %w", err)
	}

	coll := client.Database(cfg.Database).Collection(cfg.Collection)

	return &MongoStore{
		client:     client,
		collection: coll,
		cfg:        cfg,
	}, nil
}

// Close disconnects the MongoDB client.
func (s *MongoStore) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}

// LoadState retrieves the latest persisted runtime state document.
// Returns (nil, nil) if no document exists.
func (s *MongoStore) LoadState(ctx context.Context) (*RuntimeStateDoc, error) {
	if s == nil || s.collection == nil {
		return nil, fmt.Errorf("mongostate: store not initialized")
	}

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.OperationTimeoutSec)*time.Second)
	defer cancel()

	var doc RuntimeStateDoc
	err := s.collection.FindOne(opCtx, bson.M{"_id": "default"}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil // no document yet — not an error
		}
		return nil, fmt.Errorf("mongostate: load: %w", err)
	}

	// Basic schema validation.
	if doc.SchemaVersion == 0 {
		// Pre-versioned document — treat as incompatible and skip.
		return nil, nil
	}
	if doc.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("mongostate: schema version %d is newer than supported %d", doc.SchemaVersion, SchemaVersion)
	}

	return &doc, nil
}

// SaveState persists the given runtime state document using an upsert.
// The document ID is always "default" (single-instance pattern).
func (s *MongoStore) SaveState(ctx context.Context, doc *RuntimeStateDoc) error {
	if s == nil || s.collection == nil {
		return fmt.Errorf("mongostate: store not initialized")
	}
	if doc == nil {
		return fmt.Errorf("mongostate: doc is nil")
	}

	doc.ID = "default"
	doc.SchemaVersion = SchemaVersion
	doc.UpdatedAt = time.Now().UTC()

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.OperationTimeoutSec)*time.Second)
	defer cancel()

	_, err := s.collection.ReplaceOne(
		opCtx,
		bson.M{"_id": "default"},
		doc,
		options.Replace().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("mongostate: save: %w", err)
	}
	return nil
}
