package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequestBodyRoundTripAndSealing(t *testing.T) {
	st := newTestPostgresStore(t, "test_request_bodies")
	ctx := context.Background()
	usage := NewUsageStore(st)
	if usage == nil {
		t.Fatal("usage store nil")
	}
	sealer, err := NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	usage.SetSealer(sealer)

	rb := RequestBody{
		RequestID:             "req-body-1",
		Provider:              "claude",
		UpstreamProviderID:    42,
		ClientRequestHeaders:  `{"Content-Type":["application/json"]}`,
		ClientRequestBody:     `{"prompt":"secret"}`,
		ClientResponseHeaders: `{"Content-Type":["text/event-stream"]}`,
		ClientResponseBody:    `data: hello`,
		UpstreamRequest:       `POST /v1/messages {"prompt":"secret"}`,
		UpstreamResponse:      `{"content":[{"text":"hi"}]}`,
		Truncated:             true,
	}
	if err := usage.InsertRequestBody(ctx, rb); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := usage.GetRequestBodyByRequestID(ctx, "req-body-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClientRequestBody != rb.ClientRequestBody || got.UpstreamResponse != rb.UpstreamResponse {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got.UpstreamProviderID != 42 || !got.Truncated {
		t.Fatalf("metadata mismatch: %+v", got)
	}

	// The sealed column must not be readable as plaintext in the DB.
	var raw string
	if err := st.DB().QueryRowContext(ctx,
		"SELECT client_request_body FROM "+st.RequestBodiesTable()+" WHERE request_id = $1",
		"req-body-1").Scan(&raw); err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if raw == rb.ClientRequestBody {
		t.Fatal("client_request_body was stored in plaintext despite a configured sealer")
	}
}

func TestGetRequestBodyNotFound(t *testing.T) {
	st := newTestPostgresStore(t, "test_request_bodies")
	usage := NewUsageStore(st)
	if _, err := usage.GetRequestBodyByRequestID(context.Background(), "nope"); !errors.Is(err, ErrRequestBodyNotFound) {
		t.Fatalf("want ErrRequestBodyNotFound, got %v", err)
	}
}

func TestDeleteRequestBodiesBefore(t *testing.T) {
	st := newTestPostgresStore(t, "test_request_bodies")
	ctx := context.Background()
	usage := NewUsageStore(st)
	_ = usage.InsertRequestBody(ctx, RequestBody{RequestID: "old", CreatedAt: time.Now().Add(-48 * time.Hour)})
	_ = usage.InsertRequestBody(ctx, RequestBody{RequestID: "new", CreatedAt: time.Now()})
	n, err := usage.DeleteRequestBodiesBefore(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least one deleted row, got %d", n)
	}
	if _, err := usage.GetRequestBodyByRequestID(ctx, "old"); !errors.Is(err, ErrRequestBodyNotFound) {
		t.Fatalf("old row should be deleted, got %v", err)
	}
}
