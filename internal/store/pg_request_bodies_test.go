package store

import (
	"context"
	"errors"
	"strings"
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

// failingReader makes Seal's nonce read fail so the seal-error path is
// exercised without touching the database's encryption key.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("random source unavailable")
}

// TestInsertRequestBodySealErrorFailsClosed guards the fail-closed contract:
// when the sealer errors, InsertRequestBody must return the error and persist
// nothing, rather than falling back to writing plaintext.
func TestInsertRequestBodySealErrorFailsClosed(t *testing.T) {
	st := newTestPostgresStore(t, "test_request_bodies")
	ctx := context.Background()
	usage := NewUsageStore(st)
	sealer, err := NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	usage.SetSealer(sealer)

	orig := sealRandReader
	sealRandReader = failingReader{}
	defer func() { sealRandReader = orig }()

	err = usage.InsertRequestBody(ctx, RequestBody{
		RequestID:         "req-seal-fail",
		Provider:          "claude",
		ClientRequestBody: `{"prompt":"secret"}`,
	})
	if err == nil {
		t.Fatal("expected seal error to propagate; got nil")
	}
	if !strings.Contains(err.Error(), "seal request body") {
		t.Fatalf("error = %v; want seal context", err)
	}
	if _, getErr := usage.GetRequestBodyByRequestID(ctx, "req-seal-fail"); !errors.Is(getErr, ErrRequestBodyNotFound) {
		t.Fatalf("row should not have been written, got %v", getErr)
	}
}

// TestGetRequestBodyOpenFailureReturnsEmpty guards that a corrupt/unreadable
// sealed column never surfaces ciphertext to callers: the column reads as an
// empty string and the row is still returned without error.
func TestGetRequestBodyOpenFailureReturnsEmpty(t *testing.T) {
	st := newTestPostgresStore(t, "test_request_bodies")
	ctx := context.Background()
	usage := NewUsageStore(st)
	sealer, err := NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	usage.SetSealer(sealer)

	if err := usage.InsertRequestBody(ctx, RequestBody{
		RequestID:             "req-open-fail",
		ClientRequestBody:     `{"prompt":"secret"}`,
		ClientResponseBody:    `data: hello`,
		UpstreamRequest:       `POST /v1/messages {"prompt":"secret"}`,
		UpstreamResponse:      `{"content":[{"text":"hi"}]}`,
		ClientResponseHeaders: `{"Content-Type":["text/event-stream"]}`,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx,
		"UPDATE "+st.RequestBodiesTable()+" SET client_request_body = $1 WHERE request_id = $2",
		"v1:!!!not-valid-base64", "req-open-fail"); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}

	got, err := usage.GetRequestBodyByRequestID(ctx, "req-open-fail")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClientRequestBody != "" {
		t.Fatalf("ClientRequestBody = %q; want empty on decrypt failure", got.ClientRequestBody)
	}
	if got.ClientResponseHeaders != `{"Content-Type":["text/event-stream"]}` {
		t.Fatalf("non-body columns should still load: %+v", got)
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
