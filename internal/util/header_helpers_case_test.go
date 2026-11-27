package util

import (
	"net/http"
	"testing"
)

func TestApplyCustomHeadersFromAttrs_CaseVariantsResolveDeterministically(t *testing.T) {
	attrs := map[string]string{
		"header:anthropic-beta": "lower",
		"header:Anthropic-Beta": "canonical",
		"header:ANTHROPIC-BETA": "upper",
	}
	for i := 0; i < 200; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		ApplyCustomHeadersFromAttrs(req, attrs, nil)
		if got := req.Header.Values("Anthropic-Beta"); len(got) != 1 || got[0] != "upper" {
			t.Fatalf("iteration %d: Anthropic-Beta = %q, want [upper] from the first sorted attribute key", i, got)
		}
	}
}

func TestApplyCustomHeadersFromAttrs_CaseVariantSkipsUnresolvedValue(t *testing.T) {
	attrs := map[string]string{
		"header:ANTHROPIC-BETA": "$X-Missing",
		"header:anthropic-beta": "fallback",
	}
	for i := 0; i < 200; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		ApplyCustomHeadersFromAttrs(req, attrs, http.Header{})
		if got := req.Header.Get("Anthropic-Beta"); got != "fallback" {
			t.Fatalf("iteration %d: Anthropic-Beta = %q, want fallback", i, got)
		}
	}
}
