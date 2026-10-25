package handlers

import (
	"net/http"
	"testing"
)

func TestWriteRateLimitHeaders(t *testing.T) {
	src := http.Header{
		"Retry-After":                       {"3600"},
		"Anthropic-Ratelimit-Unified-Reset": {"1800000000"},
		"X-Ratelimit-Remaining-Requests":    {"0"},
		"Authorization":                     {"secret"},
		"Set-Cookie":                        {"secret"},
	}
	dst := make(http.Header)
	WriteRateLimitHeaders(dst, 429, src)
	if dst.Get("Retry-After") != "3600" || dst.Get("Anthropic-Ratelimit-Unified-Reset") != "1800000000" || dst.Get("X-Ratelimit-Remaining-Requests") != "0" {
		t.Fatalf("lost recovery headers: %v", dst)
	}
	if dst.Get("Authorization") != "" || dst.Get("Set-Cookie") != "" {
		t.Fatal("forwarded sensitive headers")
	}
	dst = make(http.Header)
	WriteRateLimitHeaders(dst, 200, src)
	if len(dst) != 0 {
		t.Fatal("forwarded failed attempt headers on success")
	}
}
