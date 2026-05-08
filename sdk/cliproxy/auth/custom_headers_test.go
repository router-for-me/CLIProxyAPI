package auth

import (
	"reflect"
	"testing"
)

func TestExtractCustomHeadersFromMetadata(t *testing.T) {
	meta := map[string]any{
		"headers": map[string]any{
			" X-Test ": " value ",
			"":         "ignored",
			"X-Empty":  "   ",
			"X-Num":    float64(1),
		},
	}

	got := ExtractCustomHeadersFromMetadata(meta)
	want := map[string]string{"X-Test": "value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractCustomHeadersFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestApplyCustomHeadersFromMetadata(t *testing.T) {
	auth := &Auth{
		Metadata: map[string]any{
			"headers": map[string]string{
				"X-Test":  "new",
				"X-Empty": "   ",
			},
		},
		Attributes: map[string]string{
			"header:X-Test": "old",
			"keep":          "1",
		},
	}

	ApplyCustomHeadersFromMetadata(auth)

	if got := auth.Attributes["header:X-Test"]; got != "new" {
		t.Fatalf("header:X-Test = %q, want %q", got, "new")
	}
	if _, ok := auth.Attributes["header:X-Empty"]; ok {
		t.Fatalf("expected header:X-Empty to be absent, got %#v", auth.Attributes["header:X-Empty"])
	}
	if got := auth.Attributes["keep"]; got != "1" {
		t.Fatalf("keep = %q, want %q", got, "1")
	}
}
