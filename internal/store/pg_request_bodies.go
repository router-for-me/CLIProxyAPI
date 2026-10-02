package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRequestBodyNotFound is returned when no captured body row exists for a
// request id (capture disabled for the provider, not captured, or swept).
var ErrRequestBodyNotFound = errors.New("request body not found")

// RequestBody is one captured request/response pair. Body fields are AES-GCM
// sealed at rest when a sealer is configured; header fields are JSON text.
type RequestBody struct {
	RequestID             string
	Provider              string
	UpstreamProviderID    int64
	ClientRequestHeaders  string
	ClientRequestBody     string
	ClientResponseHeaders string
	ClientResponseBody    string
	UpstreamRequest       string
	UpstreamResponse      string
	Truncated             bool
	CreatedAt             time.Time
}

// InsertRequestBody upserts one captured pair, sealing the four body columns.
// A duplicate request_id replaces the prior row (retries capture once).
func (s *UsageStore) InsertRequestBody(ctx context.Context, rb RequestBody) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if rb.CreatedAt.IsZero() {
		rb.CreatedAt = time.Now().UTC()
	}
	seal := func(v string) string {
		if v == "" || s.sealer == nil {
			return v
		}
		out, err := s.sealer.Seal(v)
		if err != nil {
			return v
		}
		return out
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			request_id, provider, upstream_provider_id,
			client_request_headers, client_request_body,
			client_response_headers, client_response_body,
			upstream_request, upstream_response, truncated, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (request_id) DO UPDATE SET
			provider = EXCLUDED.provider,
			upstream_provider_id = EXCLUDED.upstream_provider_id,
			client_request_headers = EXCLUDED.client_request_headers,
			client_request_body = EXCLUDED.client_request_body,
			client_response_headers = EXCLUDED.client_response_headers,
			client_response_body = EXCLUDED.client_response_body,
			upstream_request = EXCLUDED.upstream_request,
			upstream_response = EXCLUDED.upstream_response,
			truncated = EXCLUDED.truncated,
			created_at = EXCLUDED.created_at
	`, s.requestBodiesTable),
		rb.RequestID, rb.Provider, nullableInt64(rb.UpstreamProviderID),
		nullableString(rb.ClientRequestHeaders), nullableString(seal(rb.ClientRequestBody)),
		nullableString(rb.ClientResponseHeaders), nullableString(seal(rb.ClientResponseBody)),
		nullableString(seal(rb.UpstreamRequest)), nullableString(seal(rb.UpstreamResponse)),
		rb.Truncated, rb.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert request body: %w", err)
	}
	return nil
}

// GetRequestBodyByRequestID loads and decrypts one captured pair.
func (s *UsageStore) GetRequestBodyByRequestID(ctx context.Context, requestID string) (RequestBody, error) {
	var rb RequestBody
	if s == nil || s.db == nil {
		return rb, fmt.Errorf("postgres store: usage store not initialized")
	}
	var upstreamID sql.NullInt64
	var clientReqH, clientReqB, clientRespH, clientRespB, upReq, upResp sql.NullString
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT request_id, provider, upstream_provider_id,
			client_request_headers, client_request_body,
			client_response_headers, client_response_body,
			upstream_request, upstream_response, truncated, created_at
		FROM %s WHERE request_id = $1`, s.requestBodiesTable), requestID,
	).Scan(&rb.RequestID, &rb.Provider, &upstreamID,
		&clientReqH, &clientReqB, &clientRespH, &clientRespB,
		&upReq, &upResp, &rb.Truncated, &rb.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return rb, ErrRequestBodyNotFound
	}
	if err != nil {
		return rb, fmt.Errorf("postgres store: get request body: %w", err)
	}
	open := func(v sql.NullString) string {
		if !v.Valid {
			return ""
		}
		if s.sealer == nil {
			return v.String
		}
		out, errOpen := s.sealer.Open(v.String)
		if errOpen != nil {
			return v.String
		}
		return out
	}
	if upstreamID.Valid {
		rb.UpstreamProviderID = upstreamID.Int64
	}
	rb.ClientRequestHeaders = clientReqH.String
	rb.ClientRequestBody = open(clientReqB)
	rb.ClientResponseHeaders = clientRespH.String
	rb.ClientResponseBody = open(clientRespB)
	rb.UpstreamRequest = open(upReq)
	rb.UpstreamResponse = open(upResp)
	return rb, nil
}

// DeleteRequestBodiesBefore purges captured bodies older than cutoff. Called
// alongside the usage-events retention sweep.
func (s *UsageStore) DeleteRequestBodiesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE created_at < $1`, s.requestBodiesTable), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: delete old request bodies: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres store: request body delete rows affected: %w", err)
	}
	return n, nil
}
