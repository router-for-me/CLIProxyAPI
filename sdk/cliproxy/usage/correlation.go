package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const (
	// InferenceSessionHeader is the authenticated Studio integration header.
	InferenceSessionHeader = "X-CLIProxy-Studio-Inference-Session-ID"
	// TraceParentHeader is the W3C trace context header.
	TraceParentHeader = "traceparent"
	// MaxCorrelationIDLength bounds opaque identifiers accepted at the gateway boundary.
	MaxCorrelationIDLength = 256
)

var correlationSequence atomic.Uint64

type contextKey uint8

const (
	inferenceSessionIDContextKey contextKey = iota
	gatewayRequestIDContextKey
	providerRequestIDContextKey
	attemptIDContextKey
	eventIDContextKey
	traceIDContextKey
)

// Correlation contains identifiers with deliberately different scopes.
type Correlation struct {
	InferenceSessionID string
	GatewayRequestID   string
	ProviderRequestID  string
	AttemptID          string
	EventID            string
	TraceID            string
}

// ValidateInferenceSessionID validates a Studio-issued opaque session identifier
// without changing its value.
func ValidateInferenceSessionID(value string) error {
	return validateOpaqueID(value, "inference session ID")
}

// ValidateOpaqueID validates an opaque identifier received from an integration.
func ValidateOpaqueID(value, name string) error {
	return validateOpaqueID(value, name)
}

func validateOpaqueID(value, name string) error {
	if name == "" {
		name = "identifier"
	}
	if value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if len(value) > MaxCorrelationIDLength {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxCorrelationIDLength)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is blank", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return fmt.Errorf("%s contains whitespace or control characters", name)
		}
	}
	return nil
}

// ParseInferenceSessionID validates and returns the exact supplied value.
func ParseInferenceSessionID(value string) (string, error) {
	if err := ValidateInferenceSessionID(value); err != nil {
		return "", err
	}
	return value, nil
}

// ParseTraceParent returns the W3C trace ID from a valid traceparent header.
// Invalid or unsupported traceparent values return an error so callers can
// generate a fresh trace rather than forwarding an untrusted value.
func ParseTraceParent(value string) (string, error) {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return "", errors.New("invalid traceparent format")
	}
	if parts[1] != strings.ToLower(parts[1]) || !isLowerHex(parts[1]) || !isLowerHex(parts[2]) || !isLowerHex(parts[3]) {
		return "", errors.New("traceparent must use lowercase hexadecimal values")
	}
	if strings.Trim(parts[1], "0") == "" || strings.Trim(parts[2], "0") == "" {
		return "", errors.New("traceparent contains an all-zero identifier")
	}
	return parts[1], nil
}

func isLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// NewGatewayRequestID creates an identifier for one inbound gateway request.
func NewGatewayRequestID() string { return newUUID() }

// NewAttemptID creates an identifier for one physical provider attempt.
func NewAttemptID() string { return newUUID() }

// NewEventID creates an identifier that must remain stable for one usage event.
func NewEventID() string { return newUUID() }

// NewTraceID creates a W3C-compatible 32-character lowercase trace ID.
func NewTraceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		if strings.Trim(hex.EncodeToString(raw[:]), "0") != "" {
			return hex.EncodeToString(raw[:])
		}
	}
	sequence := correlationSequence.Add(1)
	return fmt.Sprintf("%032x", uint64(time.Now().UnixNano())^sequence)
}

func newUUID() string {
	if generated, err := uuid.NewRandom(); err == nil {
		return generated.String()
	}
	sequence := correlationSequence.Add(1)
	return fmt.Sprintf("fallback-%d-%d", time.Now().UnixNano(), sequence)
}

// WithInferenceSessionID stores a validated Studio session ID in ctx. Invalid
// values are ignored; gateway boundaries should use ParseInferenceSessionID to
// return a client-visible validation error.
func WithInferenceSessionID(ctx context.Context, value string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ValidateInferenceSessionID(value) != nil {
		return ctx
	}
	return context.WithValue(ctx, inferenceSessionIDContextKey, value)
}

// InferenceSessionIDFromContext returns the exact validated Studio session ID.
func InferenceSessionIDFromContext(ctx context.Context) string {
	return stringFromContext(ctx, inferenceSessionIDContextKey)
}

// WithoutInferenceSessionID hides any inherited Studio session ID while
// preserving the rest of the context and its cancellation behavior.
func WithoutInferenceSessionID(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return withoutInferenceSessionIDContext{Context: ctx}
}

type withoutInferenceSessionIDContext struct {
	context.Context
}

func (ctx withoutInferenceSessionIDContext) Value(key any) any {
	if key == inferenceSessionIDContextKey {
		return nil
	}
	return ctx.Context.Value(key)
}

// WithGatewayRequestID stores a gateway request ID in ctx.
func WithGatewayRequestID(ctx context.Context, value string) context.Context {
	return withOpaqueContext(ctx, gatewayRequestIDContextKey, value, "gateway request ID")
}

// GatewayRequestIDFromContext returns the gateway request ID stored in ctx.
func GatewayRequestIDFromContext(ctx context.Context) string {
	return stringFromContext(ctx, gatewayRequestIDContextKey)
}

// WithProviderRequestID stores a validated provider request ID in ctx.
func WithProviderRequestID(ctx context.Context, value string) context.Context {
	return withOpaqueContext(ctx, providerRequestIDContextKey, value, "provider request ID")
}

// ProviderRequestIDFromContext returns the provider request ID stored in ctx.
func ProviderRequestIDFromContext(ctx context.Context) string {
	return stringFromContext(ctx, providerRequestIDContextKey)
}

// WithAttemptID stores an attempt ID in ctx.
func WithAttemptID(ctx context.Context, value string) context.Context {
	return withOpaqueContext(ctx, attemptIDContextKey, value, "attempt ID")
}

// AttemptIDFromContext returns the provider attempt ID stored in ctx.
func AttemptIDFromContext(ctx context.Context) string {
	return stringFromContext(ctx, attemptIDContextKey)
}

// WithEventID stores an event ID in ctx.
func WithEventID(ctx context.Context, value string) context.Context {
	return withOpaqueContext(ctx, eventIDContextKey, value, "event ID")
}

// EventIDFromContext returns the usage event ID stored in ctx.
func EventIDFromContext(ctx context.Context) string {
	return stringFromContext(ctx, eventIDContextKey)
}

// WithTraceID stores a valid W3C trace ID in ctx.
func WithTraceID(ctx context.Context, value string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(value) != 32 || value != strings.ToLower(value) || !isLowerHex(value) || strings.Trim(value, "0") == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDContextKey, value)
}

// TraceIDFromContext returns the W3C trace ID stored in ctx.
func TraceIDFromContext(ctx context.Context) string {
	return stringFromContext(ctx, traceIDContextKey)
}

func withOpaqueContext(ctx context.Context, key contextKey, value, name string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if validateOpaqueID(value, name) != nil {
		return ctx
	}
	return context.WithValue(ctx, key, value)
}

func stringFromContext(ctx context.Context, key contextKey) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(key).(string)
	return value
}

// CorrelationFromContext copies the currently known identifiers from ctx.
func CorrelationFromContext(ctx context.Context) Correlation {
	return Correlation{
		InferenceSessionID: InferenceSessionIDFromContext(ctx),
		GatewayRequestID:   GatewayRequestIDFromContext(ctx),
		ProviderRequestID:  ProviderRequestIDFromContext(ctx),
		AttemptID:          AttemptIDFromContext(ctx),
		EventID:            EventIDFromContext(ctx),
		TraceID:            TraceIDFromContext(ctx),
	}
}

// EnsureRequestCorrelation returns a context with stable gateway and trace IDs.
// Existing values are preserved, so retries and nested execution do not create
// new request or trace identities.
func EnsureRequestCorrelation(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if GatewayRequestIDFromContext(ctx) == "" {
		ctx = WithGatewayRequestID(ctx, NewGatewayRequestID())
	}
	if TraceIDFromContext(ctx) == "" {
		ctx = WithTraceID(ctx, NewTraceID())
	}
	return ctx
}

// ProviderRequestIDFromHeaders extracts only provider-assigned response IDs.
// x-client-request-id is intentionally excluded because it is a caller-facing
// request header and has different semantics.
func ProviderRequestIDFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for _, name := range []string{
		"X-Provider-Request-Id",
		"X-Request-Id",
		"Request-Id",
		"Anthropic-Request-Id",
		"X-Amzn-RequestId",
		"X-Goog-Request-Id",
	} {
		for _, value := range headers.Values(name) {
			if ValidateOpaqueID(value, "provider request ID") == nil {
				return value
			}
		}
	}
	return ""
}

// NormalizeRecord fills identifiers from ctx and creates IDs that are missing
// from legacy callers. It returns a new record value and never mutates input
// maps or headers.
func NormalizeRecord(ctx context.Context, record Record) Record {
	correlation := CorrelationFromContext(ctx)
	if record.InferenceSessionID == "" {
		record.InferenceSessionID = correlation.InferenceSessionID
	}
	if record.GatewayRequestID == "" {
		record.GatewayRequestID = correlation.GatewayRequestID
	}
	if record.ProviderRequestID == "" {
		record.ProviderRequestID = correlation.ProviderRequestID
	}
	if record.AttemptID == "" && hasProviderAttempt(record) {
		record.AttemptID = correlation.AttemptID
		if record.AttemptID == "" {
			record.AttemptID = NewAttemptID()
		}
	}
	if record.EventID == "" {
		record.EventID = correlation.EventID
		if record.EventID == "" {
			record.EventID = NewEventID()
		}
	}
	if record.TraceID == "" {
		record.TraceID = correlation.TraceID
		if record.TraceID == "" {
			record.TraceID = NewTraceID()
		}
	}
	if record.GatewayRequestID == "" {
		record.GatewayRequestID = NewGatewayRequestID()
	}
	if record.ProviderRequestID == "" {
		record.ProviderRequestID = ProviderRequestIDFromHeaders(record.ResponseHeaders)
	}
	return record
}

func hasProviderAttempt(record Record) bool {
	return strings.TrimSpace(record.Provider) != "" ||
		strings.TrimSpace(record.ExecutorType) != "" ||
		strings.TrimSpace(record.Model) != ""
}
