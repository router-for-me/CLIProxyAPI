package usage

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestParseInferenceSessionIDPreservesExactValue(t *testing.T) {
	want := "studio-session_01"
	got, err := ParseInferenceSessionID(want)
	if err != nil {
		t.Fatalf("ParseInferenceSessionID() error = %v", err)
	}
	if got != want {
		t.Fatalf("ParseInferenceSessionID() = %q, want %q", got, want)
	}
}

func TestParseInferenceSessionIDRejectsMalformedValues(t *testing.T) {
	cases := []string{"", " ", "studio session", "studio\nsession", strings.Repeat("x", MaxCorrelationIDLength+1)}
	for _, value := range cases {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseInferenceSessionID(value); err == nil {
				t.Fatalf("ParseInferenceSessionID(%q) returned nil error", value)
			}
		})
	}
}

func TestParseTraceParentReturnsW3CTraceID(t *testing.T) {
	got, err := ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if err != nil {
		t.Fatalf("ParseTraceParent() error = %v", err)
	}
	if got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("ParseTraceParent() = %q", got)
	}
}

func TestEnsureRequestCorrelationPreservesExistingIDs(t *testing.T) {
	ctx := WithInferenceSessionID(context.Background(), "studio-session")
	ctx = WithGatewayRequestID(ctx, "gateway-request")
	ctx = WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	ensured := EnsureRequestCorrelation(ctx)
	correlation := CorrelationFromContext(ensured)
	if correlation.InferenceSessionID != "studio-session" {
		t.Fatalf("inference session ID = %q", correlation.InferenceSessionID)
	}
	if correlation.GatewayRequestID != "gateway-request" {
		t.Fatalf("gateway request ID = %q", correlation.GatewayRequestID)
	}
	if correlation.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace ID = %q", correlation.TraceID)
	}
}

func TestWithoutInferenceSessionIDPreservesOtherCorrelation(t *testing.T) {
	ctx := WithInferenceSessionID(context.Background(), "studio-session")
	ctx = WithGatewayRequestID(ctx, "gateway-request")
	ctx = WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	cleared := WithoutInferenceSessionID(ctx)
	correlation := CorrelationFromContext(cleared)
	if correlation.InferenceSessionID != "" {
		t.Fatalf("inference session ID = %q, want empty", correlation.InferenceSessionID)
	}
	if correlation.GatewayRequestID != "gateway-request" || correlation.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("other correlation fields changed: %+v", correlation)
	}
}

func TestNormalizeRecordCreatesDistinctAttemptAndStableEventIDs(t *testing.T) {
	ctx := WithInferenceSessionID(context.Background(), "studio-session")
	first := NormalizeRecord(ctx, Record{Provider: "openai", Model: "gpt-5.4"})
	if first.InferenceSessionID != "studio-session" {
		t.Fatalf("inference session ID = %q", first.InferenceSessionID)
	}
	if first.GatewayRequestID == "" || first.AttemptID == "" || first.EventID == "" || first.TraceID == "" {
		t.Fatalf("missing generated correlation IDs: %+v", first)
	}
	second := NormalizeRecord(ctx, first)
	if second.EventID != first.EventID {
		t.Fatalf("event ID changed from %q to %q", first.EventID, second.EventID)
	}
	if second.AttemptID != first.AttemptID {
		t.Fatalf("attempt ID changed from %q to %q", first.AttemptID, second.AttemptID)
	}
}

func TestNormalizeRecordDoesNotFabricateAttemptBeforeProviderDispatch(t *testing.T) {
	record := NormalizeRecord(context.Background(), Record{})
	if record.AttemptID != "" {
		t.Fatalf("attempt ID = %q, want empty", record.AttemptID)
	}
	if record.EventID == "" || record.GatewayRequestID == "" || record.TraceID == "" {
		t.Fatalf("missing non-attempt IDs: %+v", record)
	}
}

func TestProviderRequestIDFromHeadersExcludesClientRequestID(t *testing.T) {
	headers := http.Header{
		"X-Client-Request-Id": []string{"client-request"},
		"X-Request-Id":        []string{"provider-request"},
		"Authorization":       []string{"Bearer secret"},
	}
	if got := ProviderRequestIDFromHeaders(headers); got != "provider-request" {
		t.Fatalf("ProviderRequestIDFromHeaders() = %q", got)
	}
}
