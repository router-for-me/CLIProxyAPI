package mongostate

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	DefaultErrorEventCollection = "error_events"
	DefaultErrorEventTTLDays    = 30
)

// ErrorEventRecord captures one failed upstream request in structured form.
type ErrorEventRecord struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty"`
	DedupeKey          string             `bson:"dedupe_key,omitempty"`
	CreatedAt          time.Time          `bson:"created_at"`
	OccurredAt         time.Time          `bson:"occurred_at"`
	Provider           string             `bson:"provider"`
	Model              string             `bson:"model"`
	NormalizedModel    string             `bson:"normalized_model"`
	Source             string             `bson:"source,omitempty"`
	AuthID             string             `bson:"auth_id,omitempty"`
	AuthIndex          string             `bson:"auth_index,omitempty"`
	RequestID          string             `bson:"request_id,omitempty"`
	RequestLogRef      string             `bson:"request_log_ref,omitempty"`
	AttemptCount       int                `bson:"attempt_count,omitempty"`
	UpstreamRequestIDs []string           `bson:"upstream_request_ids,omitempty"`
	Failed             bool               `bson:"failed"`
	FailureStage       string             `bson:"failure_stage,omitempty"`
	ErrorCode          string             `bson:"error_code,omitempty"`
	ErrorMessageMasked string             `bson:"error_message_masked,omitempty"`
	ErrorMessageHash   string             `bson:"error_message_hash,omitempty"`
	StatusCode         int                `bson:"status_code,omitempty"`
	CircuitCountable   bool               `bson:"circuit_countable"`
	CircuitSkipReason  string             `bson:"circuit_skip_reason,omitempty"`
}

// ErrorEventItem is the API-facing shape returned by management query endpoints.
type ErrorEventItem struct {
	ID                 string    `json:"id"`
	DedupeKey          string    `json:"dedupe_key,omitempty"`
	ProgressScopeKey   string    `json:"progress_scope_key,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	OccurredAt         time.Time `json:"occurred_at"`
	Provider           string    `json:"provider"`
	Model              string    `json:"model"`
	NormalizedModel    string    `json:"normalized_model"`
	Source             string    `json:"source,omitempty"`
	AuthID             string    `json:"auth_id,omitempty"`
	AuthIndex          string    `json:"auth_index,omitempty"`
	RequestID          string    `json:"request_id,omitempty"`
	RequestLogRef      string    `json:"request_log_ref,omitempty"`
	AttemptCount       int       `json:"attempt_count,omitempty"`
	UpstreamRequestIDs []string  `json:"upstream_request_ids,omitempty"`
	Failed             bool      `json:"failed"`
	FailureStage       string    `json:"failure_stage,omitempty"`
	ErrorCode          string    `json:"error_code,omitempty"`
	ErrorMessageMasked string    `json:"error_message_masked,omitempty"`
	ErrorMessageHash   string    `json:"error_message_hash,omitempty"`
	StatusCode         int       `json:"status_code,omitempty"`
	CircuitCountable   bool      `json:"circuit_countable"`
	CircuitSkipReason  string    `json:"circuit_skip_reason,omitempty"`
}

// ErrorEventQuery defines filters and pagination for error event search.
type ErrorEventQuery struct {
	Provider     string
	AuthID       string
	Model        string
	FailureStage string
	ErrorCode    string
	StatusCode   *int
	RequestID    string
	Start        time.Time
	End          time.Time
	Page         int
	PageSize     int
}

// ErrorEventQueryResult is the paged query result.
type ErrorEventQueryResult struct {
	Items    []ErrorEventItem    `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
	Meta     ErrorEventQueryMeta `json:"meta,omitempty"`
}

// ErrorEventQueryMeta contains extra computed metadata for UI consumers.
type ErrorEventQueryMeta struct {
	ProgressByScope map[string]ErrorEventProgressSnapshot `json:"progress_by_scope,omitempty"`
}

// ErrorEventProgressSnapshot describes breaker and deletion progress for one auth+model scope.
type ErrorEventProgressSnapshot struct {
	Breaker  ErrorEventBreakerProgressSnapshot  `json:"breaker"`
	Deletion ErrorEventDeletionProgressSnapshot `json:"deletion"`
}

// ErrorEventBreakerProgressSnapshot describes current breaker failure progress.
type ErrorEventBreakerProgressSnapshot struct {
	Current   int    `json:"current"`
	Threshold int    `json:"threshold"`
	State     string `json:"state,omitempty"`
}

// ErrorEventDeletionProgressSnapshot describes current auto-removal progress.
type ErrorEventDeletionProgressSnapshot struct {
	Enabled   bool   `json:"enabled"`
	Current   int    `json:"current"`
	Threshold int    `json:"threshold"`
	Status    string `json:"status,omitempty"`
}

// ErrorEventSummaryQuery defines filters and grouping dimensions for summary API.
type ErrorEventSummaryQuery struct {
	Provider     string
	AuthID       string
	Model        string
	FailureStage string
	ErrorCode    string
	StatusCode   *int
	Start        time.Time
	End          time.Time
	GroupBy      []string
	Limit        int
}

// ErrorEventSummaryItem is one aggregated bucket returned by summary API.
type ErrorEventSummaryItem struct {
	Provider              string     `json:"provider,omitempty"`
	Model                 string     `json:"model,omitempty"`
	NormalizedModel       string     `json:"normalized_model,omitempty"`
	AuthID                string     `json:"auth_id,omitempty"`
	ProgressScopeKey      string     `json:"progress_scope_key,omitempty"`
	ErrorCode             string     `json:"error_code,omitempty"`
	FailureStage          string     `json:"failure_stage,omitempty"`
	StatusCode            *int       `json:"status_code,omitempty"`
	Total                 int64      `json:"total"`
	CircuitCountableTotal int64      `json:"circuit_countable_total"`
	LatestOccurredAt      *time.Time `json:"latest_occurred_at,omitempty"`
}

// ErrorEventSummaryResult is the aggregate result payload.
type ErrorEventSummaryResult struct {
	Items   []ErrorEventSummaryItem `json:"items"`
	GroupBy []string                `json:"group_by"`
	Meta    ErrorEventQueryMeta     `json:"meta,omitempty"`
}

// ErrorEventQuerier describes the query capability required by management APIs.
type ErrorEventQuerier interface {
	Query(ctx context.Context, query ErrorEventQuery) (ErrorEventQueryResult, error)
	Insert(ctx context.Context, record *ErrorEventRecord) error
}

// ErrorEventStore persists structured failed-request events.
type ErrorEventStore struct {
	client              *mongo.Client
	collection          *mongo.Collection
	operationTimeoutSec int
}

var (
	globalErrorEventStore   ErrorEventQuerier
	globalErrorEventStoreMu sync.RWMutex
)

// SetGlobalErrorEventStore sets the global error-event store used by usage plugin and management handlers.
func SetGlobalErrorEventStore(store ErrorEventQuerier) {
	globalErrorEventStoreMu.Lock()
	defer globalErrorEventStoreMu.Unlock()
	globalErrorEventStore = store
}

// GetGlobalErrorEventStore returns the global error-event store.
func GetGlobalErrorEventStore() ErrorEventQuerier {
	globalErrorEventStoreMu.RLock()
	defer globalErrorEventStoreMu.RUnlock()
	return globalErrorEventStore
}

// ErrorEventItemFromRecord converts a persisted record to its API shape.
func ErrorEventItemFromRecord(doc ErrorEventRecord) ErrorEventItem {
	return ErrorEventItem{
		ID:                 doc.ID.Hex(),
		DedupeKey:          doc.DedupeKey,
		CreatedAt:          doc.CreatedAt,
		OccurredAt:         doc.OccurredAt,
		Provider:           doc.Provider,
		Model:              doc.Model,
		NormalizedModel:    doc.NormalizedModel,
		Source:             doc.Source,
		AuthID:             doc.AuthID,
		AuthIndex:          doc.AuthIndex,
		RequestID:          doc.RequestID,
		RequestLogRef:      doc.RequestLogRef,
		AttemptCount:       doc.AttemptCount,
		UpstreamRequestIDs: append([]string(nil), doc.UpstreamRequestIDs...),
		Failed:             doc.Failed,
		FailureStage:       doc.FailureStage,
		ErrorCode:          doc.ErrorCode,
		ErrorMessageMasked: doc.ErrorMessageMasked,
		ErrorMessageHash:   doc.ErrorMessageHash,
		StatusCode:         doc.StatusCode,
		CircuitCountable:   doc.CircuitCountable,
		CircuitSkipReason:  doc.CircuitSkipReason,
	}
}

// BuildErrorEventProgressScopeKey returns the shared auth+model scope key used by
// error-event progress insights and circuit-breaker deletion audits.
func BuildErrorEventProgressScopeKey(provider string, authID string, normalizedModel string) string {
	return BuildCircuitBreakerDeletionDedupeKey(provider, authID, normalizedModel)
}

// BuildErrorEventDedupeKey builds a deterministic dedupe key for one error event.
func BuildErrorEventDedupeKey(provider string, authID string, normalizedModel string, requestID string, failureStage string, errorCode string, statusCode int, occurredAt time.Time, errorMessageHash string, attemptCount int) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(provider)),
		strings.TrimSpace(authID),
		strings.ToLower(strings.TrimSpace(normalizedModel)),
		strings.TrimSpace(requestID),
		strings.ToLower(strings.TrimSpace(failureStage)),
		strings.ToLower(strings.TrimSpace(errorCode)),
		strconv.Itoa(statusCode),
	}
	if strings.TrimSpace(requestID) == "" {
		if occurredAt.IsZero() {
			occurredAt = time.Now().UTC()
		}
		parts = append(parts,
			strconv.FormatInt(occurredAt.UTC().UnixMilli(), 10),
			strings.ToLower(strings.TrimSpace(errorMessageHash)),
			strconv.Itoa(attemptCount),
		)
	}
	return strings.Join(parts, "|")
}

// NewErrorEventStore creates the store and ensures required indexes.
func NewErrorEventStore(ctx context.Context, cfg StoreConfig, collection string, ttlDays int) (*ErrorEventStore, error) {
	if cfg.URI == "" {
		return nil, fmt.Errorf("mongostate: URI is required")
	}
	if cfg.Database == "" {
		return nil, fmt.Errorf("mongostate: database is required")
	}
	if strings.TrimSpace(collection) == "" {
		collection = DefaultErrorEventCollection
	}
	if ttlDays <= 0 {
		ttlDays = DefaultErrorEventTTLDays
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
		return nil, fmt.Errorf("mongostate: connect error event store: %w", err)
	}

	pingCtx, pingCancel := context.WithTimeout(ctx, time.Duration(cfg.OperationTimeoutSec)*time.Second)
	defer pingCancel()
	if err = client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongostate: ping error event store: %w", err)
	}

	store := &ErrorEventStore{
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

func (s *ErrorEventStore) ensureIndexes(ctx context.Context, ttlDays int) error {
	if s == nil || s.collection == nil {
		return fmt.Errorf("mongostate: error event store not initialized")
	}
	if ttlDays <= 0 {
		ttlDays = DefaultErrorEventTTLDays
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
			Keys:    bson.D{{Key: "dedupe_key", Value: 1}},
			Options: options.Index().SetName("unique_dedupe_key").SetUnique(true).SetPartialFilterExpression(bson.M{"dedupe_key": bson.M{"$exists": true}}),
		},
		{
			Keys:    bson.D{{Key: "provider", Value: 1}, {Key: "auth_id", Value: 1}, {Key: "normalized_model", Value: 1}, {Key: "occurred_at", Value: -1}},
			Options: options.Index().SetName("query_provider_auth_model_occurred"),
		},
		{
			Keys:    bson.D{{Key: "failure_stage", Value: 1}, {Key: "status_code", Value: 1}, {Key: "occurred_at", Value: -1}},
			Options: options.Index().SetName("query_stage_status_occurred"),
		},
		{
			Keys:    bson.D{{Key: "request_id", Value: 1}, {Key: "occurred_at", Value: -1}},
			Options: options.Index().SetName("query_request_occurred"),
		},
	}
	if _, err := s.collection.Indexes().CreateMany(opCtx, models); err != nil {
		return fmt.Errorf("mongostate: ensure error event indexes: %w", err)
	}
	return nil
}

// Insert writes one structured error event record.
func (s *ErrorEventStore) Insert(ctx context.Context, record *ErrorEventRecord) error {
	if s == nil || s.collection == nil {
		return fmt.Errorf("mongostate: error event store not initialized")
	}
	if record == nil {
		return fmt.Errorf("mongostate: error event record is nil")
	}

	now := time.Now().UTC()
	record.Provider = strings.ToLower(strings.TrimSpace(record.Provider))
	record.Model = strings.TrimSpace(record.Model)
	record.NormalizedModel = strings.TrimSpace(record.NormalizedModel)
	record.Source = strings.TrimSpace(record.Source)
	record.AuthID = strings.TrimSpace(record.AuthID)
	record.AuthIndex = strings.TrimSpace(record.AuthIndex)
	record.RequestID = strings.TrimSpace(record.RequestID)
	record.RequestLogRef = strings.TrimSpace(record.RequestLogRef)
	record.FailureStage = strings.ToLower(strings.TrimSpace(record.FailureStage))
	record.ErrorCode = strings.ToLower(strings.TrimSpace(record.ErrorCode))
	record.ErrorMessageMasked = strings.TrimSpace(record.ErrorMessageMasked)
	record.ErrorMessageHash = strings.TrimSpace(record.ErrorMessageHash)
	record.CircuitSkipReason = strings.TrimSpace(record.CircuitSkipReason)

	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = record.CreatedAt
	}
	if strings.TrimSpace(record.DedupeKey) == "" {
		record.DedupeKey = BuildErrorEventDedupeKey(
			record.Provider,
			record.AuthID,
			record.NormalizedModel,
			record.RequestID,
			record.FailureStage,
			record.ErrorCode,
			record.StatusCode,
			record.OccurredAt,
			record.ErrorMessageHash,
			record.AttemptCount,
		)
	}

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	_, err := s.collection.InsertOne(opCtx, record)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil
		}
		return fmt.Errorf("mongostate: insert error event record: %w", err)
	}
	return nil
}

// Query lists error event records with filters and pagination.
func (s *ErrorEventStore) Query(ctx context.Context, query ErrorEventQuery) (ErrorEventQueryResult, error) {
	result := ErrorEventQueryResult{}
	if s == nil || s.collection == nil {
		return result, fmt.Errorf("mongostate: error event store not initialized")
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

	filter := buildErrorEventFilter(query)

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	total, err := s.collection.CountDocuments(opCtx, filter)
	if err != nil {
		return result, fmt.Errorf("mongostate: count error event records: %w", err)
	}

	skip := int64((query.Page - 1) * query.PageSize)
	findOpts := options.Find().
		SetSort(bson.D{{Key: "occurred_at", Value: -1}, {Key: "created_at", Value: -1}}).
		SetSkip(skip).
		SetLimit(int64(query.PageSize))

	cursor, err := s.collection.Find(opCtx, filter, findOpts)
	if err != nil {
		return result, fmt.Errorf("mongostate: find error event records: %w", err)
	}
	defer cursor.Close(opCtx)

	items := make([]ErrorEventItem, 0, query.PageSize)
	for cursor.Next(opCtx) {
		var doc ErrorEventRecord
		if err := cursor.Decode(&doc); err != nil {
			return result, fmt.Errorf("mongostate: decode error event record: %w", err)
		}
		items = append(items, ErrorEventItemFromRecord(doc))
	}
	if err := cursor.Err(); err != nil {
		return result, fmt.Errorf("mongostate: iterate error event records: %w", err)
	}

	result.Items = items
	result.Total = total
	result.Page = query.Page
	result.PageSize = query.PageSize
	return result, nil
}

// Summarize aggregates error events by selected dimensions.
func (s *ErrorEventStore) Summarize(ctx context.Context, query ErrorEventSummaryQuery) (ErrorEventSummaryResult, error) {
	result := ErrorEventSummaryResult{}
	if s == nil || s.collection == nil {
		return result, fmt.Errorf("mongostate: error event store not initialized")
	}

	groupBy := normalizeErrorEventSummaryGroupBy(query.GroupBy)
	result.GroupBy = groupBy

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	filter := buildErrorEventFilter(ErrorEventQuery{
		Provider:     query.Provider,
		AuthID:       query.AuthID,
		Model:        query.Model,
		FailureStage: query.FailureStage,
		ErrorCode:    query.ErrorCode,
		StatusCode:   query.StatusCode,
		Start:        query.Start,
		End:          query.End,
	})

	groupID := bson.D{}
	for _, field := range groupBy {
		groupID = append(groupID, bson.E{Key: field, Value: "$" + field})
	}

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: groupID},
			{Key: "total", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "circuit_countable_total", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{"$circuit_countable", 1, 0}}}}}},
			{Key: "latest_occurred_at", Value: bson.D{{Key: "$max", Value: "$occurred_at"}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "total", Value: -1}, {Key: "latest_occurred_at", Value: -1}}}},
		{{Key: "$limit", Value: limit}},
	}

	opCtx, cancel := context.WithTimeout(ctx, time.Duration(s.operationTimeoutSec)*time.Second)
	defer cancel()

	cursor, err := s.collection.Aggregate(opCtx, pipeline)
	if err != nil {
		return result, fmt.Errorf("mongostate: summarize error events: %w", err)
	}
	defer cursor.Close(opCtx)

	type summaryDoc struct {
		ID                    map[string]any `bson:"_id"`
		Total                 int64          `bson:"total"`
		CircuitCountableTotal int64          `bson:"circuit_countable_total"`
		LatestOccurredAt      time.Time      `bson:"latest_occurred_at"`
	}
	items := make([]ErrorEventSummaryItem, 0, limit)
	for cursor.Next(opCtx) {
		var doc summaryDoc
		if errDecode := cursor.Decode(&doc); errDecode != nil {
			return result, fmt.Errorf("mongostate: decode error event summary: %w", errDecode)
		}
		item := ErrorEventSummaryItem{
			Total:                 doc.Total,
			CircuitCountableTotal: doc.CircuitCountableTotal,
		}
		if !doc.LatestOccurredAt.IsZero() {
			ts := doc.LatestOccurredAt.UTC()
			item.LatestOccurredAt = &ts
		}
		item.Provider = summaryStringValue(doc.ID["provider"])
		item.Model = summaryStringValue(doc.ID["model"])
		item.NormalizedModel = summaryStringValue(doc.ID["normalized_model"])
		item.AuthID = summaryStringValue(doc.ID["auth_id"])
		item.ErrorCode = summaryStringValue(doc.ID["error_code"])
		item.FailureStage = summaryStringValue(doc.ID["failure_stage"])
		if status, ok := summaryIntValue(doc.ID["status_code"]); ok {
			item.StatusCode = &status
		}
		items = append(items, item)
	}
	if errCursor := cursor.Err(); errCursor != nil {
		return result, fmt.Errorf("mongostate: iterate error event summary: %w", errCursor)
	}

	result.Items = items
	return result, nil
}

func buildErrorEventFilter(query ErrorEventQuery) bson.M {
	filter := bson.M{}
	if v := strings.ToLower(strings.TrimSpace(query.Provider)); v != "" {
		filter["provider"] = v
	}
	if v := strings.TrimSpace(query.AuthID); v != "" {
		filter["auth_id"] = v
	}
	if v := strings.TrimSpace(query.Model); v != "" {
		filter["$or"] = bson.A{bson.M{"model": v}, bson.M{"normalized_model": v}}
	}
	if v := strings.ToLower(strings.TrimSpace(query.FailureStage)); v != "" {
		filter["failure_stage"] = v
	}
	if v := strings.ToLower(strings.TrimSpace(query.ErrorCode)); v != "" {
		filter["error_code"] = v
	}
	if query.StatusCode != nil {
		filter["status_code"] = *query.StatusCode
	}
	if v := strings.TrimSpace(query.RequestID); v != "" {
		filter["request_id"] = v
	}
	if !query.Start.IsZero() || !query.End.IsZero() {
		timeFilter := bson.M{}
		if !query.Start.IsZero() {
			timeFilter["$gte"] = query.Start.UTC()
		}
		if !query.End.IsZero() {
			timeFilter["$lte"] = query.End.UTC()
		}
		filter["occurred_at"] = timeFilter
	}
	return filter
}

func normalizeErrorEventSummaryGroupBy(raw []string) []string {
	defaultFields := []string{"provider", "model", "auth_id", "error_code", "failure_stage", "status_code"}
	if len(raw) == 0 {
		return defaultFields
	}
	allowed := map[string]struct{}{
		"provider":         {},
		"model":            {},
		"normalized_model": {},
		"auth_id":          {},
		"error_code":       {},
		"failure_stage":    {},
		"status_code":      {},
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, field := range raw {
		key := strings.ToLower(strings.TrimSpace(field))
		if key == "" {
			continue
		}
		if _, ok := allowed[key]; !ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return defaultFields
	}
	return out
}

func summaryStringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func summaryIntValue(value any) (int, bool) {
	if value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

// Close disconnects the MongoDB client.
func (s *ErrorEventStore) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}
