package quotatraffic

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCaptureRejectsInvalidFieldsAndKeepsEqualObservation(t *testing.T) {
	resetCache()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := headers("Primary", "0")
	h.Set("X-Codex-Primary-Reset-At", "secret-text")
	h.Set("X-Codex-Primary-Window-Minutes", "-1")
	Capture("a", "codex", h, at, "api_response_headers")
	Capture("a", "codex", headers("Primary", "99"), at, "websocket_event")
	Capture("a", "codex", headers("Primary", "NaN"), at.Add(time.Hour), "websocket_event")
	Capture("a", "codex", headers("Primary", "Inf"), at.Add(time.Hour), "websocket_event")
	Capture("a", "codex", headers("Primary", "-1"), at.Add(time.Hour), "websocket_event")
	Capture("a", "codex", headers("Primary", "101"), at.Add(time.Hour), "websocket_event")
	rows := Read(map[string]bool{"a": true})
	if len(rows) != 1 || rows[0].Headers.Get("X-Codex-Primary-Used-Percent") != "0" || rows[0].Headers.Get("X-Codex-Primary-Reset-At") != "" || rows[0].Headers.Get("X-Codex-Primary-Window-Minutes") != "" || !rows[0].ObservedAt.Equal(at) {
		t.Fatalf("invalid fields or equal observations changed the cache: %+v", rows)
	}
	Capture("b", "claude", http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"1.5"}}, at, "api_response_headers")
	if len(Read(map[string]bool{"b": true})) != 0 {
		t.Fatal("Claude utilization outside 0..1 accepted")
	}
}

func TestCaptureEvictsOldestAndHonorsReadScope(t *testing.T) {
	resetCache()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= maxObservations; i++ {
		Capture(fmt.Sprint(i), "codex", headers("Primary", "10"), at.Add(time.Duration(i)*time.Second), "api_response_headers")
	}
	if len(cache.items) != maxObservations || len(Read(map[string]bool{"0": true})) != 0 || len(Read(map[string]bool{fmt.Sprint(maxObservations): true})) != 1 || len(Read(nil)) != 0 {
		t.Fatal("cache eviction or scope is incorrect")
	}
}

func resetCache() { cache.Lock(); cache.items = map[string]Observation{}; cache.Unlock() }
func headers(window, used string) http.Header {
	return http.Header{"X-Codex-" + window + "-Used-Percent": {used}, "X-Codex-" + window + "-Window-Minutes": {"300"}, "X-Codex-" + window + "-Reset-After-Seconds": {"120"}, "Authorization": {"must-never-be-reported"}, "Set-Cookie": {"must-never-be-reported"}}
}
func TestCaptureSeparatesAccountsAndWindowsRejectsLateData(t *testing.T) {
	resetCache()
	at := time.Now()
	Capture("a", "codex", headers("Primary", "10"), at, "api_response_headers")
	Capture("a", "codex", headers("Secondary", "20"), at.Add(-time.Minute), "websocket_event")
	Capture("b", "codex", headers("Primary", "90"), at, "websocket_event")
	Capture("a", "codex", headers("Primary", "99"), at.Add(-time.Second), "websocket_event")
	Capture("a", "codex", http.Header{"Server": {"anything"}}, at.Add(time.Hour), "api_response_headers")
	rows := Read(map[string]bool{"a": true, "b": true})
	if len(rows) != 3 {
		t.Fatalf("wanted 3 distinct windows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.Headers.Get("Authorization") != "" || row.Headers.Get("Set-Cookie") != "" {
			t.Fatal("credential leak")
		}
		if row.AuthIndex == "a" && row.Window == "x-codex-primary" {
			if row.Headers.Get("X-Codex-Primary-Used-Percent") != "10" || !row.ObservedAt.Equal(at) || row.Source != "api_response_headers" {
				t.Fatal("late or missing observations changed cache")
			}
		}
	}
	rows[0].Headers.Set("Authorization", "caller mutation")
	again := Read(map[string]bool{"a": true})
	if len(again) != 2 || again[0].Headers.Get("Authorization") != "" {
		t.Fatal("cache read mutated cache or expanded scope")
	}
}
func TestProviderQueriesHaveCaptureTimeAndSource(t *testing.T) {
	resetCache()
	at := time.Now()
	CaptureQuery("a", "codex", []byte(`{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_after_seconds":60},"secondary_window":{"used_percent":42,"limit_window_seconds":604800,"reset_at":1800000000}},"access_token":"secret"}`), at, "scheduled_provider_query")
	rows := Read(map[string]bool{"a": true})
	if len(rows) != 2 {
		t.Fatal("missing queried windows")
	}
	for _, row := range rows {
		if row.Source != "scheduled_provider_query" || !row.ObservedAt.Equal(at) {
			t.Fatal("query provenance changed")
		}
	}
	Capture("a", "codex", headers("Primary", "11"), at.Add(time.Second), "websocket_event")
	rows = Read(map[string]bool{"a": true})
	if rows[0].Source != "websocket_event" || rows[1].Source != "scheduled_provider_query" {
		t.Fatal("independent sources lost")
	}
}

func TestQueryAdditionalWindowsAndHeaderAllowlist(t *testing.T) {
	resetCache()
	at := time.Now()
	CaptureQuery("a", "codex", []byte(`{"code_review_rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_after_seconds":60}},"additional_rate_limits":[{"limit_name":"Spark","rate_limit":{"secondary_window":{"used_percent":32,"limit_window_seconds":604800,"reset_after_seconds":120}}}]}`), at, "manual_provider_query")
	rows := Read(map[string]bool{"a": true})
	if len(rows) != 2 {
		t.Fatal("optional windows lost")
	}
	h := headers("Primary", "10")
	h.Set("X-Codex-Primary-Authorization", "secret")
	Capture("b", "codex", h, at, "api_response_headers")
	for _, row := range Read(map[string]bool{"b": true}) {
		if row.Headers.Get("X-Codex-Primary-Authorization") != "" {
			t.Fatal("unknown header leaked")
		}
	}
}

func TestCaptureRejectsOverflowTimeFields(t *testing.T) {
	resetCache()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	h := headers("Primary", "10")
	for _, suffix := range []string{"Reset-After-Seconds", "Window-Minutes", "Reset-At"} {
		h.Set("X-Codex-Primary-"+suffix, "9223372036854775807")
	}
	Capture("synthetic", "codex", h, at, "api_response_headers")
	rows := Read(map[string]bool{"synthetic": true})
	if len(rows) != 1 {
		t.Fatal("valid percentage lost")
	}
	for _, suffix := range []string{"Reset-After-Seconds", "Window-Minutes", "Reset-At"} {
		if rows[0].Headers.Get("X-Codex-Primary-"+suffix) != "" {
			t.Fatal("overflow time retained")
		}
	}
}
