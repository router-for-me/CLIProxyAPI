package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
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
	// Seal the four body columns. A seal failure aborts the write: storing the
	// plaintext body would defeat encryption, so we fail closed and let the
	// best-effort capture caller log and drop the row.
	seal := func(v string) (string, error) {
		if v == "" || s.sealer == nil {
			return v, nil
		}
		out, err := s.sealer.Seal(v)
		if err != nil {
			return "", fmt.Errorf("postgres store: seal request body: %w", err)
		}
		return out, nil
	}
	clientReqBody, err := seal(rb.ClientRequestBody)
	if err != nil {
		return err
	}
	clientRespBody, err := seal(rb.ClientResponseBody)
	if err != nil {
		return err
	}
	upReq, err := seal(rb.UpstreamRequest)
	if err != nil {
		return err
	}
	upResp, err := seal(rb.UpstreamResponse)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`
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
		nullableString(rb.ClientRequestHeaders), nullableString(clientReqBody),
		nullableString(rb.ClientResponseHeaders), nullableString(clientRespBody),
		nullableString(upReq), nullableString(upResp),
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
	// open decrypts a sealed body column. A nil sealer passes plaintext rows
	// through unchanged (encryption disabled). A decrypt failure for a sealed
	// payload logs a warning and yields an empty string: ciphertext must never
	// be surfaced to API callers as if it were the body.
	open := func(column string, v sql.NullString) string {
		if !v.Valid {
			return ""
		}
		if s.sealer == nil {
			return v.String
		}
		out, errOpen := s.sealer.Open(v.String)
		if errOpen != nil {
			log.WithError(errOpen).WithFields(log.Fields{
				"request_id": requestID,
				"column":     column,
			}).Warn("postgres store: decrypt request body column failed; returning empty")
			return ""
		}
		return out
	}
	if upstreamID.Valid {
		rb.UpstreamProviderID = upstreamID.Int64
	}
	rb.ClientRequestHeaders = clientReqH.String
	rb.ClientRequestBody = open("client_request_body", clientReqB)
	rb.ClientResponseHeaders = clientRespH.String
	rb.ClientResponseBody = open("client_response_body", clientRespB)
	rb.UpstreamRequest = open("upstream_request", upReq)
	rb.UpstreamResponse = open("upstream_response", upResp)
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
