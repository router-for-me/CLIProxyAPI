package multiuser

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// balancedFixtureAt keeps every fixture on one journal day so append order
// decides file position, not the clock.
var balancedFixtureAt = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func appendUsageEvent(t *testing.T, s *Store, u UsageEvent) {
	t.Helper()
	if err := s.appendEntry(u.At, journalEntry{Usage: &u}); err != nil {
		t.Fatal(err)
	}
}

func balancedUsage(at time.Time, trace, id string, failed bool) UsageEvent {
	return UsageEvent{
		ID: id, RequestID: id, TraceID: trace, At: at,
		Consumer: "bob", Owner: "alice", ChannelID: "ch-one", Channel: "route-one",
		CredentialID: "cred-one", Model: "gpt-5", Failed: failed, Balanced: true,
	}
}

func TestBalancedCollapseHidesFailedAttemptsBehindFinalSuccess(t *testing.T) {
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	trace := "trace-success"
	first := balancedUsage(at, trace, "att-1", true)
	first.Error = "boom-1"
	second := balancedUsage(at.Add(time.Millisecond), trace, "att-2", true)
	second.Error = "boom-2"
	final := balancedUsage(at.Add(2*time.Millisecond), trace, "att-3", false)
	appendUsageEvent(t, s, first)
	appendUsageEvent(t, s, second)
	appendUsageEvent(t, s, final)

	items, total, err := s.Usage(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("collapsed list: total=%d items=%d", total, len(items))
	}
	if items[0].ID != "att-3" || items[0].Failed || !items[0].Balanced || items[0].Error != "" {
		t.Fatalf("wrong surviving row: %+v", items[0])
	}
	if items[0].Attempts != 3 {
		t.Fatalf("attempts not reported: %+v", items[0])
	}

	sum, groups, err := s.Summary(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 1 || sum.Executions != 1 || sum.Failures != 0 || sum.Failovers != 2 {
		t.Fatalf("collapsed summary: %+v", sum)
	}
	for _, group := range groups {
		if group.Failovers != 0 {
			t.Fatalf("failovers leaked into a group row: %+v", group)
		}
	}

	attempts, err := s.UsageAttemptsContext(context.Background(), trace)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"att-1", "att-2", "att-3"}
	wantErrors := []string{"boom-1", "boom-2", ""}
	if len(attempts) != len(wantIDs) {
		t.Fatalf("attempt chain length: %d", len(attempts))
	}
	for i := range attempts {
		if attempts[i].ID != wantIDs[i] || attempts[i].Error != wantErrors[i] {
			t.Fatalf("attempt %d: got id=%s error=%q want id=%s error=%q", i, attempts[i].ID, attempts[i].Error, wantIDs[i], wantErrors[i])
		}
	}
}

func TestBalancedCollapseKeepsOnlyLastFailureWhenAllAttemptsFail(t *testing.T) {
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	trace := "trace-all-fail"
	for i := 1; i <= 3; i++ {
		u := balancedUsage(at.Add(time.Duration(i)*time.Millisecond), trace, fmt.Sprintf("att-%d", i), true)
		u.Error = fmt.Sprintf("boom-%d", i)
		appendUsageEvent(t, s, u)
	}

	items, total, err := s.Usage(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "att-3" || !items[0].Failed || items[0].Attempts != 3 {
		t.Fatalf("all-failed collapse: total=%d items=%+v", total, items)
	}

	sum, _, err := s.Summary(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 1 || sum.Executions != 1 || sum.Failures != 1 || sum.Failovers != 2 {
		t.Fatalf("all-failed summary: %+v", sum)
	}
}

func TestNonBalancedAttemptsStaySeparate(t *testing.T) {
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	trace := "trace-unbalanced"
	first := balancedUsage(at, trace, "nb-1", true)
	first.Balanced = false
	first.Error = "boom"
	second := balancedUsage(at.Add(time.Millisecond), trace, "nb-2", false)
	second.Balanced = false
	appendUsageEvent(t, s, first)
	appendUsageEvent(t, s, second)

	items, total, err := s.Usage(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("unbalanced attempts collapsed: total=%d items=%d", total, len(items))
	}
	if items[0].ID != "nb-2" || items[0].Attempts != 0 || items[1].ID != "nb-1" || items[1].Error != "boom" || items[1].Attempts != 0 {
		t.Fatalf("unbalanced rows: %+v", items)
	}

	sum, _, err := s.Summary(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Calls != 1 || sum.Executions != 2 || sum.Failures != 1 || sum.Failovers != 0 {
		t.Fatalf("unbalanced summary: %+v", sum)
	}

	attempts, err := s.UsageAttemptsContext(context.Background(), trace)
	if err != nil || len(attempts) != 2 || attempts[0].ID != "nb-1" || attempts[1].ID != "nb-2" {
		t.Fatalf("unbalanced attempt chain: %+v err=%v", attempts, err)
	}
}

func TestUpstreamFiltersExposeSupersededBalancedAttempts(t *testing.T) {
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	trace := "trace-filtered"
	channels := []string{"ch-a", "ch-b", "ch-c"}
	ids := []string{"att-1", "att-2", "att-3"}
	for i := range channels {
		u := balancedUsage(at.Add(time.Duration(i)*time.Millisecond), trace, ids[i], i < 2)
		u.ChannelID = channels[i]
		if i < 2 {
			u.Error = "boom"
		}
		appendUsageEvent(t, s, u)
	}

	if items, total, err := s.Usage(Filter{}); err != nil || total != 1 || len(items) != 1 || items[0].ID != "att-3" {
		t.Fatalf("default view must still collapse: total=%d items=%+v err=%v", total, items, err)
	}
	sum, _, err := s.Summary(Filter{})
	if err != nil || sum.Failovers != 2 {
		t.Fatalf("default summary failovers: %+v err=%v", sum, err)
	}

	// Channel-scoped views keep every real upstream attempt visible.
	for i := range channels {
		filter := Filter{ChannelID: channels[i]}
		items, total, err := s.Usage(filter)
		if err != nil || total != 1 || len(items) != 1 || items[0].ID != ids[i] {
			t.Fatalf("channel %s: total=%d items=%+v err=%v", channels[i], total, items, err)
		}
		if items[0].Attempts != 3 {
			t.Fatalf("channel %s attempts: %+v", channels[i], items[0])
		}
		channelSum, _, err := s.Summary(filter)
		if err != nil || channelSum.Executions != 1 || channelSum.Failovers != 0 {
			t.Fatalf("channel %s summary: %+v err=%v", channels[i], channelSum, err)
		}
	}

	// A pinned serving model also keeps the failover chain visible.
	items, total, err := s.Usage(Filter{Model: "gpt-5（route-one/gpt-5）"})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("pinned model view: total=%d items=%+v err=%v", total, items, err)
	}
	if items[0].ID != "att-3" || items[2].ID != "att-1" || items[2].Error != "boom" {
		t.Fatalf("pinned model rows: %+v", items)
	}
}

func TestHandleUsageStoresBalancedFlagAndTruncatedError(t *testing.T) {
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	base := usage.Record{
		Attribution: usage.Attribution{
			SiteID: s.SiteID(), Consumer: "bob", Owner: "alice",
			ChannelID: "ch-one", Channel: "route-one", CredentialName: "cred.json",
		},
		RequestedAt: at, AuthID: "cred.json", Alias: "route-one/gpt-5", Model: "gpt-5",
		TraceID: "trace-handle", RequestID: "att-1",
	}

	// 2000 three-byte runes = 6000 bytes, so the 4096-byte limit falls mid-rune.
	longBody := strings.Repeat("错", 2000)
	failed := base
	failed.Failed = true
	failed.Fail = usage.Failure{StatusCode: 429, Body: "  " + longBody + "\t"}
	s.HandleUsage(context.Background(), failed)

	success := base
	success.RequestID = "att-2"
	success.RequestedAt = at.Add(time.Second)
	success.Attribution.Balanced = true
	success.Detail = usage.Detail{InputTokens: 5, OutputTokens: 7, TotalTokens: 12}
	s.HandleUsage(context.Background(), success)

	// Records from another site or without a consumer are dropped.
	foreign := success
	foreign.RequestID = "att-3"
	foreign.Attribution.SiteID = "another-site"
	s.HandleUsage(context.Background(), foreign)
	anonymous := success
	anonymous.RequestID = "att-4"
	anonymous.Attribution.Consumer = ""
	s.HandleUsage(context.Background(), anonymous)

	items, total, err := s.Usage(Filter{})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("handle usage rows: total=%d items=%d err=%v", total, len(items), err)
	}
	// Newest first: the balanced success, then the unbalanced failure.
	if !items[0].Balanced || items[0].Error != "" || items[0].Attempts != 2 {
		t.Fatalf("success row: %+v", items[0])
	}
	if items[1].Balanced || items[1].Status != 429 {
		t.Fatalf("failure row flags: %+v", items[1])
	}
	// 1365 whole runes (4095 bytes) plus the ellipsis rune.
	want := strings.Repeat("错", 1365) + "…"
	if items[1].Error != want {
		t.Fatalf("truncated error mismatch: len=%d want=%d", len(items[1].Error), len(want))
	}
	if !utf8.ValidString(items[1].Error) || len(items[1].Error) != 4098 {
		t.Fatalf("truncated error not rune-safe: len=%d valid=%v", len(items[1].Error), utf8.ValidString(items[1].Error))
	}
}

func TestJournalIndexRebuildsOldSchemaWithoutSupersededColumn(t *testing.T) {
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	trace := "trace-migration"
	first := balancedUsage(at, trace, "att-1", true)
	first.Error = "boom"
	final := balancedUsage(at.Add(time.Millisecond), trace, "att-2", false)
	appendUsageEvent(t, s, first)
	appendUsageEvent(t, s, final)

	// Simulate an index built by an older column layout: no superseded column,
	// and a sources row claiming the journal file is already fully imported, so
	// a missing rebuild would leave the index empty.
	journalPath := filepath.Join(s.dir, "journal", at.Format("2006-01-02")+".jsonl")
	info, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(s.dir, "journal-index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := `CREATE TABLE events (
 source TEXT NOT NULL, position INTEGER NOT NULL, kind INTEGER NOT NULL,
 at TEXT NOT NULL, trace TEXT NOT NULL, PRIMARY KEY (source, position, kind)
);
CREATE TABLE sources (
 name TEXT PRIMARY KEY, offset INTEGER NOT NULL, modified INTEGER NOT NULL, signature TEXT NOT NULL
);`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO sources VALUES(?,?,?,?)", info.Name(), info.Size(), info.ModTime().UnixNano(), "stale"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	items, total, err := s.Usage(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != "att-2" || items[0].Attempts != 2 {
		t.Fatalf("post-migration list: total=%d items=%+v", total, items)
	}
	sum, _, err := s.Summary(Filter{})
	if err != nil || sum.Executions != 1 || sum.Failovers != 1 {
		t.Fatalf("post-migration summary: %+v err=%v", sum, err)
	}
	attempts, err := s.UsageAttemptsContext(context.Background(), trace)
	if err != nil || len(attempts) != 2 || attempts[0].ID != "att-1" || attempts[0].Error != "boom" {
		t.Fatalf("post-migration attempt chain: %+v err=%v", attempts, err)
	}
}
