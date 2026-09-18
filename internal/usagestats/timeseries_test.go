package usagestats

import (
	"testing"
	"time"
)

func TestQueryTimeseriesHourlyZeroFillingAndAggregation(t *testing.T) {
	dir := t.TempDir()

	// Fixture data on 2026-09-18
	writeFixtureFile(t, dir, "2026-09-18", []string{
		`{"timestamp":"2026-09-18T10:15:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":150,"input":{"uncached_tokens":100},"output":{"non_reasoning_tokens":50}}}`,
		`{"timestamp":"2026-09-18T10:45:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":true,"status_code":429,"token_breakdown":{"total_tokens":0}}`,
		`{"timestamp":"2026-09-18T12:05:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":200,"input":{"uncached_tokens":150},"output":{"non_reasoning_tokens":50}}}`,
	})

	// Query from 10:00 to 13:00 (4 hourly buckets: 10:00, 11:00, 12:00, 13:00)
	from := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 18, 13, 0, 0, 0, time.UTC)

	buckets, err := QueryTimeseries(dir, from, to, "hour", Filter{})
	if err != nil {
		t.Fatalf("QueryTimeseries: %v", err)
	}

	if len(buckets) != 4 {
		t.Fatalf("expected 4 hourly buckets, got %d", len(buckets))
	}

	// 10:00 bucket: 2 records (1 success, 1 fail)
	if buckets[0].Timestamp != "2026-09-18T10:00:00Z" {
		t.Errorf("bucket[0] ts = %s, want 10:00", buckets[0].Timestamp)
	}
	if buckets[0].Records != 2 || buckets[0].Failures != 1 || buckets[0].TotalTokens != 150 {
		t.Errorf("bucket[0] = %+v, want records=2 failures=1 tokens=150", buckets[0])
	}

	// 11:00 bucket: zero-filled
	if buckets[1].Timestamp != "2026-09-18T11:00:00Z" {
		t.Errorf("bucket[1] ts = %s, want 11:00", buckets[1].Timestamp)
	}
	if buckets[1].Records != 0 || buckets[1].TotalTokens != 0 {
		t.Errorf("bucket[1] should be zero-filled, got %+v", buckets[1])
	}

	// 12:00 bucket: 1 record
	if buckets[2].Timestamp != "2026-09-18T12:00:00Z" {
		t.Errorf("bucket[2] ts = %s, want 12:00", buckets[2].Timestamp)
	}
	if buckets[2].Records != 1 || buckets[2].TotalTokens != 200 {
		t.Errorf("bucket[2] = %+v, want records=1 tokens=200", buckets[2])
	}

	// 13:00 bucket: zero-filled
	if buckets[3].Timestamp != "2026-09-18T13:00:00Z" {
		t.Errorf("bucket[3] ts = %s, want 13:00", buckets[3].Timestamp)
	}
	if buckets[3].Records != 0 {
		t.Errorf("bucket[3] should be zero-filled, got %+v", buckets[3])
	}
}

func TestQueryTimeseriesDailyBucketing(t *testing.T) {
	dir := t.TempDir()

	writeFixtureFile(t, dir, "2026-09-16", []string{
		`{"timestamp":"2026-09-16T08:00:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":100}}`,
	})
	writeFixtureFile(t, dir, "2026-09-18", []string{
		`{"timestamp":"2026-09-18T09:00:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":300}}`,
	})

	from := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	buckets, err := QueryTimeseries(dir, from, to, "day", Filter{})
	if err != nil {
		t.Fatalf("QueryTimeseries: %v", err)
	}

	if len(buckets) != 3 {
		t.Fatalf("expected 3 daily buckets (16, 17, 18), got %d", len(buckets))
	}
	if buckets[0].Records != 1 || buckets[0].TotalTokens != 100 {
		t.Errorf("day 16 = %+v, want records=1 tokens=100", buckets[0])
	}
	if buckets[1].Records != 0 || buckets[1].TotalTokens != 0 {
		t.Errorf("day 17 = %+v, want records=0 tokens=0 (zero-filled)", buckets[1])
	}
	if buckets[2].Records != 1 || buckets[2].TotalTokens != 300 {
		t.Errorf("day 18 = %+v, want records=1 tokens=300", buckets[2])
	}
}

func TestQueryTimeseriesWithFilter(t *testing.T) {
	dir := t.TempDir()

	writeFixtureFile(t, dir, "2026-09-18", []string{
		`{"timestamp":"2026-09-18T10:15:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":100}}`,
		`{"timestamp":"2026-09-18T10:30:00Z","provider":"claude","model":"claude-sonnet-5","account":"alice","failed":false,"status_code":200,"token_breakdown":{"total_tokens":500}}`,
	})

	from := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	// Filter for antigravity only
	buckets, err := QueryTimeseries(dir, from, to, "hour", Filter{Provider: "antigravity"})
	if err != nil {
		t.Fatalf("QueryTimeseries: %v", err)
	}

	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(buckets))
	}
	if buckets[0].Records != 1 || buckets[0].TotalTokens != 100 {
		t.Errorf("filtered bucket = %+v, want records=1 tokens=100", buckets[0])
	}
}
