package helps

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestWriteAttemptResponseStopsAtCap(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	chunk := []byte(strings.Repeat("x", 4096))

	// Write well past the cap.
	for i := 0; i < (maxAttemptResponseLogBytes/len(chunk))+8; i++ {
		writeAttemptResponse(nil, attempt, chunk)
	}

	if got := attempt.response.Len(); got > maxAttemptResponseLogBytes {
		t.Fatalf("buffered %d bytes, want at most %d", got, maxAttemptResponseLogBytes)
	}
	if !attempt.bodyTruncated {
		t.Fatalf("expected bodyTruncated to be set once the cap was reached")
	}
}

func TestWriteAttemptResponseKeepsSmallBodiesIntact(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	writeAttemptResponse(nil, attempt, []byte("small body"))

	if attempt.response.String() != "small body" {
		t.Fatalf("buffer = %q, want %q", attempt.response.String(), "small body")
	}
	if attempt.bodyTruncated {
		t.Fatalf("a small body must not be marked truncated")
	}
}

// TestWriteAttemptResponseAllowsExactlyCapBytes fills the in-memory buffer to
// exactly maxAttemptResponseLogBytes and asserts it is NOT yet truncated, then
// writes one more byte and asserts the cap trips. 4096 divides the cap evenly,
// so repeated 4096-byte writes only reach the exact boundary; this kills the
// ">=" mutation of the cap check (which would trip one write early).
func TestWriteAttemptResponseAllowsExactlyCapBytes(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	chunk := []byte(strings.Repeat("y", 4096))
	for i := 0; i < maxAttemptResponseLogBytes/len(chunk); i++ {
		writeAttemptResponse(nil, attempt, chunk)
	}

	if got := attempt.response.Len(); got != maxAttemptResponseLogBytes {
		t.Fatalf("buffered %d bytes, want exactly %d", got, maxAttemptResponseLogBytes)
	}
	if attempt.bodyTruncated {
		t.Fatalf("a buffer filled to exactly the cap must not be marked truncated")
	}

	writeAttemptResponse(nil, attempt, []byte("z"))
	if !attempt.bodyTruncated {
		t.Fatalf("expected bodyTruncated once the cap would be exceeded")
	}
	if got := attempt.response.Len(); got != maxAttemptResponseLogBytes {
		t.Fatalf("buffered %d bytes after the trip, want the cap %d to be held", got, maxAttemptResponseLogBytes)
	}
}

// newGinLoggingContext builds a gin context wired through the real recording
// API surface (request-log enabled, no file-backed sources).
func newGinLoggingContext(t *testing.T) (*gin.Context, context.Context, *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	cfg := &config.Config{}
	cfg.RequestLog = true
	return ginCtx, ctx, cfg
}

// tripFirstAttempt streams 1 MiB chunks until the first attempt's in-memory
// buffer hits the cap and the truncation flag trips.
func tripFirstAttempt(t *testing.T, ctx context.Context, cfg *config.Config) {
	t.Helper()
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < 16; i++ {
		AppendAPIResponseChunk(ctx, cfg, chunk)
	}
}

func aggregateResponseBytes(t *testing.T, ginCtx *gin.Context) []byte {
	t.Helper()
	value, exists := ginCtx.Get(apiResponseKey)
	if !exists {
		t.Fatalf("aggregated response (%s) was not set on the gin context", apiResponseKey)
	}
	data, ok := value.([]byte)
	if !ok {
		t.Fatalf("aggregated response = %#v, want []byte", value)
	}
	return data
}

// TestTruncationMarkerAppearsInAggregate drives an attempt to truncation via
// AppendAPIResponseChunk and asserts the aggregate stored on the gin context
// contains the "[truncated: ...]" marker with the exact cap in bytes.
func TestTruncationMarkerAppearsInAggregate(t *testing.T) {
	ginCtx, ctx, cfg := newGinLoggingContext(t)
	tripFirstAttempt(t, ctx, cfg)

	aggregate := string(aggregateResponseBytes(t, ginCtx))
	wantMarker := "[truncated: response log would exceed the " + strconv.Itoa(maxAttemptResponseLogBytes) + "-byte cap]"
	if !strings.Contains(aggregate, "[truncated:") {
		t.Fatalf("aggregate does not contain the truncation marker; tail = %q", aggregate[max(0, len(aggregate)-200):])
	}
	if !strings.Contains(aggregate, wantMarker) {
		t.Fatalf("aggregate marker = %q, want it to contain %q", aggregate, wantMarker)
	}
}

// TestTruncationMarkerAppearsOnceAndRebuildIsSkipped asserts that after the
// first post-trip rebuild, further chunk appends leave the aggregate bytes
// byte-identical (fingerprint skip) and that "[truncated:" occurs exactly once.
func TestTruncationMarkerAppearsOnceAndRebuildIsSkipped(t *testing.T) {
	ginCtx, ctx, cfg := newGinLoggingContext(t)
	tripFirstAttempt(t, ctx, cfg)

	before := aggregateResponseBytes(t, ginCtx)
	if n := bytes.Count(before, []byte("[truncated:")); n != 1 {
		t.Fatalf("aggregate contains %d truncation markers, want exactly 1", n)
	}

	chunk := bytes.Repeat([]byte("b"), 1<<20)
	for i := 0; i < 4; i++ {
		AppendAPIResponseChunk(ctx, cfg, chunk)
	}

	after := aggregateResponseBytes(t, ginCtx)
	if !bytes.Equal(before, after) {
		t.Fatalf("aggregate changed after the cap tripped; fingerprint skip is not working")
	}
	// Byte equality alone cannot distinguish "skipped the rebuild" from
	// "rebuilt byte-identical output", so also assert the stored value is the
	// very same slice (no ginCtx.Set happened after the trip).
	if len(before) > 0 && len(after) > 0 && &before[0] != &after[0] {
		t.Fatalf("aggregate slice was reallocated after the cap tripped; the per-chunk rebuild was not skipped")
	}
	if n := bytes.Count(after, []byte("[truncated:")); n != 1 {
		t.Fatalf("aggregate contains %d truncation markers after extra chunks, want exactly 1", n)
	}
}

// TestTruncationMarkerRebuildIsForcedByFlagAlone covers the edge case where
// the write that trips the cap adds ZERO bytes to the aggregate inputs: one
// payload fills the buffer to exactly maxAttemptResponseLogBytes (no trip, no
// marker yet), and the next one-byte write trips the cap while leaving the
// attempt count and total size unchanged. Only the truncated flag in the
// fingerprint can force the rebuild that appends the marker; without it the
// aggregate would be permanently marker-less. It also asserts later calls are
// skipped (same stored slice, marker still exactly once).
func TestTruncationMarkerRebuildIsForcedByFlagAlone(t *testing.T) {
	ginCtx, _, _ := newGinLoggingContext(t)
	attempts, attempt := ensureAttempt(ginCtx)

	writeAttemptResponse(ginCtx, attempt, bytes.Repeat([]byte("a"), maxAttemptResponseLogBytes))
	if attempt.bodyTruncated {
		t.Fatalf("a write that exactly fills the cap must not trip it")
	}
	updateAggregatedResponseIfMemoryBacked(ginCtx, attempts)
	first := aggregateResponseBytes(t, ginCtx)
	if bytes.Contains(first, []byte("[truncated:")) {
		t.Fatalf("aggregate must not carry a truncation marker before the cap trips")
	}

	// This write is dropped entirely: Len() stays at the cap, so neither the
	// count nor the size component of the fingerprint moves. Only the flag.
	writeAttemptResponse(ginCtx, attempt, []byte("x"))
	if !attempt.bodyTruncated {
		t.Fatalf("expected bodyTruncated once one byte over the cap is written")
	}
	updateAggregatedResponseIfMemoryBacked(ginCtx, attempts)
	second := aggregateResponseBytes(t, ginCtx)
	if bytes.Equal(first, second) {
		t.Fatalf("aggregate was not rebuilt after the cap tripped; the truncated flag must force one rebuild")
	}
	if n := bytes.Count(second, []byte("[truncated:")); n != 1 {
		t.Fatalf("aggregate contains %d truncation markers, want exactly 1", n)
	}

	// With nothing left to change, further updates must be skipped entirely.
	updateAggregatedResponseIfMemoryBacked(ginCtx, attempts)
	third := aggregateResponseBytes(t, ginCtx)
	if len(second) > 0 && len(third) > 0 && &second[0] != &third[0] {
		t.Fatalf("aggregate was reallocated with unchanged inputs; fingerprint skip is not working")
	}
	if n := bytes.Count(third, []byte("[truncated:")); n != 1 {
		t.Fatalf("aggregate contains %d truncation markers after a skipped rebuild, want exactly 1", n)
	}
}

// TestAggregateRebuildsWhenNewAttemptIsAdded verifies the fingerprint reacts
// to retries: with attempt 1 truncated, RecordAPIRequest + chunk appends must
// rebuild the aggregate so it includes the new attempt's content.
func TestAggregateRebuildsWhenNewAttemptIsAdded(t *testing.T) {
	ginCtx, ctx, cfg := newGinLoggingContext(t)
	tripFirstAttempt(t, ctx, cfg)

	before := aggregateResponseBytes(t, ginCtx)
	if bytes.Contains(before, []byte("=== API RESPONSE 2 ===")) {
		t.Fatalf("aggregate unexpectedly already contains the second attempt")
	}

	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{URL: "https://api.example.com/v1/messages", Method: "POST"})
	AppendAPIResponseChunk(ctx, cfg, bytes.Repeat([]byte("c"), 4096))

	after := aggregateResponseBytes(t, ginCtx)
	if bytes.Equal(before, after) {
		t.Fatalf("aggregate did not rebuild after a new attempt was recorded; fingerprint count component is not working")
	}
	if !bytes.Contains(after, []byte("=== API RESPONSE 2 ===")) {
		t.Fatalf("aggregate missing the second attempt's response header")
	}
	if !bytes.Contains(after, []byte("[truncated:")) {
		t.Fatalf("aggregate lost the first attempt's truncation marker after the rebuild")
	}
}
