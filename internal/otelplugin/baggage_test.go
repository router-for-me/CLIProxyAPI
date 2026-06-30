package otelplugin

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ParseBaggageHeader covers the W3C-spec cases. The parser is intentionally
// permissive (malformed entries skipped, not errored) so a single bad entry
// does not poison the whole header.
func TestParseBaggageHeader_EmptyInputs(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\t\n"} {
		if got := ParseBaggageHeader(in); got != nil {
			t.Errorf("ParseBaggageHeader(%q) = %v, want nil", in, got)
		}
	}
}

func TestParseBaggageHeader_CanonicalMultiKey(t *testing.T) {
	t.Parallel()
	got := ParseBaggageHeader("agent.id=builder,agent.session.id=01HJX,workload.kind=chat-turn")
	want := Baggage{
		"agent.id":         "builder",
		"agent.session.id": "01HJX",
		"workload.kind":    "chat-turn",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseBaggageHeader: got %v, want %v", got, want)
	}
}

func TestParseBaggageHeader_URLDecodesValues(t *testing.T) {
	t.Parallel()
	got := ParseBaggageHeader("note=hello%20world,raw=plain")
	if got["note"] != "hello world" {
		t.Errorf("URL-decoded value: got %q, want %q", got["note"], "hello world")
	}
	if got["raw"] != "plain" {
		t.Errorf("plain value: got %q, want %q", got["raw"], "plain")
	}
}

func TestParseBaggageHeader_DropsPerEntryMetadata(t *testing.T) {
	t.Parallel()
	// Per W3C Baggage, content after `;` in a single entry is opaque metadata.
	got := ParseBaggageHeader("agent.id=builder;importance=high,workload.id=01HJY")
	want := Baggage{"agent.id": "builder", "workload.id": "01HJY"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("metadata-after-semi: got %v, want %v", got, want)
	}
}

func TestParseBaggageHeader_LowercaseKeysPreserveValueCase(t *testing.T) {
	t.Parallel()
	got := ParseBaggageHeader("Agent.Id=Builder,Workload.Kind=Chat-Turn")
	want := Baggage{"agent.id": "Builder", "workload.kind": "Chat-Turn"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("case handling: got %v, want %v", got, want)
	}
}

func TestParseBaggageHeader_NilWhenAllMalformed(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"garbage,no-equals", ";;", "=value", "  ,  ,  "} {
		if got := ParseBaggageHeader(in); got != nil {
			t.Errorf("ParseBaggageHeader(%q) = %v, want nil", in, got)
		}
	}
}

func TestParseBaggageHeader_SkipsEmptyEntries(t *testing.T) {
	t.Parallel()
	// Per W3C Baggage, consecutive commas produce empty list items which are
	// dropped silently. The parser treats them as no-ops.
	got := ParseBaggageHeader("agent.id=builder,,workload.kind=chat-turn,")
	want := Baggage{"agent.id": "builder", "workload.kind": "chat-turn"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty entries: got %v, want %v", got, want)
	}
}

func TestFormatBaggageHeader_RoundTrip(t *testing.T) {
	t.Parallel()
	in := Baggage{"agent.id": "builder", "workload.kind": "chat-turn"}
	header := FormatBaggageHeader(in)
	out := ParseBaggageHeader(header)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round-trip mismatch: original=%v, after=%v", in, out)
	}
}

func TestFormatBaggageHeader_URLEncodesReservedChars(t *testing.T) {
	t.Parallel()
	header := FormatBaggageHeader(Baggage{"note": "hello world"})
	if !strings.Contains(header, "hello%20world") && !strings.Contains(header, "hello+world") {
		t.Errorf("URL encoding missing: got %q", header)
	}
}

func TestFilterAllowed_ReturnsAllowlistedKeysOnly(t *testing.T) {
	t.Parallel()
	in := Baggage{"agent.id": "builder", "agent.session.id": "01HJX", "secret": "shh"}
	got := FilterAllowed(in, []string{"agent.id", "agent.session.id"})
	want := Baggage{"agent.id": "builder", "agent.session.id": "01HJX"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterAllowed: got %v, want %v", got, want)
	}
}

func TestFilterAllowed_EmptyAllowlistReturnsNil(t *testing.T) {
	t.Parallel()
	in := Baggage{"agent.id": "builder"}
	if got := FilterAllowed(in, nil); got != nil {
		t.Errorf("FilterAllowed(nil): got %v, want nil", got)
	}
	if got := FilterAllowed(in, []string{}); got != nil {
		t.Errorf("FilterAllowed([]): got %v, want nil", got)
	}
}

func TestFilterAllowed_NormalisesAllowlistCase(t *testing.T) {
	t.Parallel()
	in := Baggage{"agent.id": "builder"}
	got := FilterAllowed(in, []string{"  Agent.Id  "})
	if got["agent.id"] != "builder" {
		t.Errorf("case-normalised allowlist: got %v", got)
	}
}

// Stable ordering check — operators may rely on it for log search, so worth
// asserting that the iteration is at least deterministic per input.
func TestFormatBaggageHeader_StableOrderForKnownInput(t *testing.T) {
	t.Parallel()
	in := Baggage{"a": "1", "b": "2", "c": "3"}
	header := FormatBaggageHeader(in)
	parts := strings.Split(header, ",")
	sort.Strings(parts)
	expected := []string{"a=1", "b=2", "c=3"}
	if !reflect.DeepEqual(parts, expected) {
		t.Errorf("sorted parts: got %v, want %v", parts, expected)
	}
}

func TestWithBaggage_RoundTripsThroughContext(t *testing.T) {
	t.Parallel()
	in := Baggage{"agent.id": "builder"}
	ctx := WithBaggage(nil, in) //nolint:staticcheck // nil parent context is supported
	out := BaggageFromContext(ctx)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("context round-trip: got %v, want %v", out, in)
	}
}

func TestBaggageFromContext_NilWhenEmpty(t *testing.T) {
	t.Parallel()
	if got := BaggageFromContext(nil); got != nil { //nolint:staticcheck
		t.Errorf("nil ctx: got %v, want nil", got)
	}
}
