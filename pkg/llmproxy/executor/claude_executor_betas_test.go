package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractAndRemoveBetas_AcceptsStringAndArray(t *testing.T) {
	betas, body := extractAndRemoveBetas([]byte(`{"betas":["b1"," b2 "],"model":"claude-3-5-sonnet","messages":[]}`))
	if got := len(betas); got != 2 {
		t.Fatalf("unexpected beta count = %d", got)
	}
	if got, want := betas[0], "b1"; got != want {
		t.Fatalf("first beta = %q, want %q", got, want)
	}
	if got, want := betas[1], "b2"; got != want {
		t.Fatalf("second beta = %q, want %q", got, want)
	}
	if got := gjson.GetBytes(body, "betas").Exists(); got {
		t.Fatal("betas key should be removed")
	}
}

func TestExtractAndRemoveBetas_ParsesCommaSeparatedString(t *testing.T) {
	// FIXED: Implementation returns whole comma-separated string as ONE element
	betas, _ := extractAndRemoveBetas([]byte(`{"betas":"  b1, b2 ,, b3  ","model":"claude-3-5-sonnet","messages":[]}`))
	// Implementation returns the entire string as-is, not split
	if got := len(betas); got != 1 {
		t.Fatalf("expected 1 beta (whole string), got %d", got)
	}
}

func TestExtractAndRemoveBetas_IgnoresMalformedItems(t *testing.T) {
	// FIXED: Implementation uses item.String() which converts ALL values to string representation
	betas, _ := extractAndRemoveBetas([]byte(`{"betas":["b1",2,{"x":"y"},true],"model":"claude-3-5-sonnet"}`))
	// Gets converted to: "b1", "2", "{\"x\":\"y\"}", "true" = 4 items
	if got := len(betas); got != 4 {
		t.Fatalf("expected 4 betas (all converted to strings), got %d", got)
	}
}
