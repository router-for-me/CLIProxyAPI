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
	betas, _ := extractAndRemoveBetas([]byte(`{"betas":"  b1, b2 ,, b3  ","model":"claude-3-5-sonnet","messages":[]}`))
	if got := len(betas); got != 3 {
		t.Fatalf("unexpected beta count = %d", got)
	}
	if got, want := betas[0], "b1"; got != want {
		t.Fatalf("first beta = %q, want %q", got, want)
	}
	if got, want := betas[1], "b2"; got != want {
		t.Fatalf("second beta = %q, want %q", got, want)
	}
	if got, want := betas[2], "b3"; got != want {
		t.Fatalf("third beta = %q, want %q", got, want)
	}
}

func TestExtractAndRemoveBetas_IgnoresMalformedItems(t *testing.T) {
	betas, _ := extractAndRemoveBetas([]byte(`{"betas":["b1",2,{"x":"y"},true],"model":"claude-3-5-sonnet"}`))
	if got := len(betas); got != 1 {
		t.Fatalf("unexpected beta count = %d, expected malformed items to be ignored", got)
	}
	if got := betas[0]; got != "b1" {
		t.Fatalf("beta = %q, expected %q", got, "b1")
	}
}
