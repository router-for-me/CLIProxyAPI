package usagestats

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQueryRecordsReturnsNewestFirstWithPagination(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-09-18T%02d:00:00Z","provider":"antigravity","model":"gemini","account":"a","failed":false,"status_code":200,"token_breakdown":{"total_tokens":%d}}`, 10+i, i+1))
	}
	writeFixtureFile(t, dir, "2026-09-18", lines)

	from := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 18, 23, 59, 59, 0, time.UTC)
	page, err := QueryRecords(dir, from, to, RecordFilter{}, 1, 2)
	if err != nil {
		t.Fatalf("QueryRecords: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(page.Records))
	}
	if page.Records[0].Timestamp.Hour() != 13 || page.Records[1].Timestamp.Hour() != 12 {
		t.Fatalf("timestamps = %v, want 13:00 then 12:00", []time.Time{page.Records[0].Timestamp, page.Records[1].Timestamp})
	}
	if !page.HasMore || page.NextOff != 3 {
		t.Fatalf("page metadata = %+v, want has_more=true next_offset=3", page)
	}
}

func TestQueryRecordsFiltersAndHandlesMalformedLines(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "2026-09-18", []string{
		`{"timestamp":"2026-09-18T10:00:00Z","provider":"antigravity","model":"gemini","account":"a","failed":false,"status_code":200,"token_breakdown":{"total_tokens":10}}`,
		`not-json`,
		`{"timestamp":"2026-09-18T11:00:00Z","provider":"claude","model":"sonnet","account":"b","failed":true,"status_code":429,"error_message":"quota","token_breakdown":{"total_tokens":20}}`,
	})

	failed := true
	page, err := QueryRecords(
		dir,
		time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 18, 23, 59, 59, 0, time.UTC),
		RecordFilter{Provider: "claude", Failed: &failed},
		0,
		50,
	)
	if err != nil {
		t.Fatalf("QueryRecords: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ErrorMessage != "quota" {
		t.Fatalf("filtered records = %+v, want one quota failure", page.Records)
	}
}

func TestQueryRecordsReadsLinesSplitAcrossReverseChunks(t *testing.T) {
	dir := t.TempDir()
	longModel := "model-" + strings.Repeat("x", 70*1024)
	writeFixtureFile(t, dir, "2026-09-18", []string{
		fmt.Sprintf(`{"timestamp":"2026-09-18T10:00:00Z","provider":"antigravity","model":"%s","account":"a","failed":false,"status_code":200,"token_breakdown":{"total_tokens":10}}`, longModel),
		`{"timestamp":"2026-09-18T11:00:00Z","provider":"antigravity","model":"gemini","account":"a","failed":false,"status_code":200,"token_breakdown":{"total_tokens":20}}`,
	})

	page, err := QueryRecords(
		dir,
		time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 18, 23, 59, 59, 0, time.UTC),
		RecordFilter{},
		0,
		50,
	)
	if err != nil {
		t.Fatalf("QueryRecords: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(page.Records))
	}
	if page.Records[1].Model != longModel {
		t.Fatalf("long record model was not reconstructed, got length %d", len(page.Records[1].Model))
	}
}
