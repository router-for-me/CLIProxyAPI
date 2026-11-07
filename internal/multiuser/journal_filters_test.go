package multiuser

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func filterFixture(t *testing.T) *Store {
	t.Helper()
	s := openJournalTest(t, "")
	at := balancedFixtureAt
	events := []UsageEvent{
		{ID: "a", Provider: "kimi", Stream: true, Status: 200, Endpoint: "POST /v1/chat/completions", ReasoningEffort: "high", TotalTokens: 30, LatencyMS: 900},
		{ID: "b", Provider: "openai-compatible-hub-ch-abc-vanchin", Stream: false, Status: 429, Failed: true, Endpoint: "POST /v1/messages", LatencyMS: 300},
		{ID: "c", Provider: "kimi", Stream: true, Status: 502, Failed: true, Endpoint: "POST /v1/messages", ReasoningEffort: "low", TotalTokens: 5, LatencyMS: 5000},
		{ID: "d", Provider: "codex", Stream: false, Status: 200, Endpoint: "POST /v1/chat/completions", TotalTokens: 90, LatencyMS: 100, Balanced: true},
	}
	for i, u := range events {
		u.RequestID, u.TraceID, u.At = u.ID, u.ID, at.Add(time.Duration(i)*time.Second)
		u.Consumer, u.Owner, u.Channel, u.Model, u.TokensKnown = "bob", "alice", "route-one", "gpt-5", true
		appendUsageEvent(t, s, u)
	}
	return s
}

func usageIDs(t *testing.T, s *Store, filter Filter) []string {
	t.Helper()
	items, total, err := s.Usage(filter)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if total != len(ids) {
		t.Fatalf("total %d != listed %d for %+v", total, len(ids), filter)
	}
	return ids
}

func TestUsageAttributeFiltersMatchReference(t *testing.T) {
	s := filterFixture(t)
	cases := []struct {
		filter Filter
		want   []string
	}{
		{Filter{Provider: "kimi"}, []string{"c", "a"}},
		// Mounted providers group by their base type, not the hub route ID.
		{Filter{Provider: "openai-compatible"}, []string{"b"}},
		{Filter{Stream: "stream"}, []string{"c", "a"}},
		{Filter{Stream: "nonstream"}, []string{"d", "b"}},
		{Filter{Status: "200"}, []string{"d", "a"}},
		{Filter{Status: "4xx"}, []string{"b"}},
		{Filter{Status: "5xx"}, []string{"c"}},
		{Filter{Endpoint: "POST /v1/messages"}, []string{"c", "b"}},
		{Filter{Effort: "high"}, []string{"a"}},
		{Filter{Effort: "none"}, []string{"d", "b"}},
		{Filter{Balanced: "yes"}, []string{"d"}},
		{Filter{Balanced: "no", Provider: "kimi", Status: "2xx"}, []string{"a"}},
	}
	for _, tc := range cases {
		if got := usageIDs(t, s, tc.filter); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%+v: got %v want %v", tc.filter, got, tc.want)
		}
	}
	// Summary statistics apply the same filters as the list.
	for _, tc := range cases {
		sum, _, err := s.Summary(tc.filter)
		if err != nil || int(sum.Executions) != len(tc.want) {
			t.Fatalf("summary %+v: %+v err=%v", tc.filter, sum, err)
		}
	}
}

func TestUsageSortOrders(t *testing.T) {
	s := filterFixture(t)
	if got := usageIDs(t, s, Filter{}); !reflect.DeepEqual(got, []string{"d", "c", "b", "a"}) {
		t.Fatalf("default order %v", got)
	}
	if got := usageIDs(t, s, Filter{Sort: "tokens"}); !reflect.DeepEqual(got, []string{"d", "a", "c", "b"}) {
		t.Fatalf("token order %v", got)
	}
	if got := usageIDs(t, s, Filter{Sort: "latency"}); !reflect.DeepEqual(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("latency order %v", got)
	}
}

func TestUsageFacetsListPresentValues(t *testing.T) {
	s := filterFixture(t)
	facets, err := s.UsageFacetsContext(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := UsageFacets{
		Providers: []string{"codex", "kimi", "openai-compatible"},
		Endpoints: []string{"POST /v1/chat/completions", "POST /v1/messages"},
		Statuses:  []int{200, 429, 502},
		Efforts:   []string{"high", "low", "none"},
	}
	if !reflect.DeepEqual(facets, want) {
		t.Fatalf("facets %+v", facets)
	}
	narrowed, err := s.UsageFacetsContext(context.Background(), Filter{Provider: "kimi"})
	if err != nil || !reflect.DeepEqual(narrowed.Statuses, []int{200, 502}) {
		t.Fatalf("narrowed facets %+v err=%v", narrowed, err)
	}
}

func TestStatusRangeParsing(t *testing.T) {
	for input, want := range map[string][3]int{"200": {200, 200, 1}, "4xx": {400, 499, 1}, "5XX": {500, 599, 1}, "abc": {}, "6xx": {}, "99": {}} {
		low, high, ok := statusRange(input)
		got := [3]int{low, high, 0}
		if ok {
			got[2] = 1
		}
		if got != want {
			t.Fatalf("statusRange(%q) = %v want %v", input, got, want)
		}
	}
}

// An index built before the attribute filter columns existed is rebuilt from
// the journal instead of failing every query.
func TestJournalIndexRebuildsLayoutWithoutFilterColumns(t *testing.T) {
	s := openJournalTest(t, "")
	u := UsageEvent{ID: "x", RequestID: "x", TraceID: "x", At: balancedFixtureAt, Consumer: "bob", Owner: "alice", Model: "gpt-5", Provider: "kimi", Status: 200}
	appendUsageEvent(t, s, u)
	journalPath := filepath.Join(s.dir, "journal", balancedFixtureAt.Format("2006-01-02")+".jsonl")
	info, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(s.dir, "journal-index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	// The previous layout: every column up to superseded, no provider/effort.
	if _, err := db.Exec(`CREATE TABLE events (
 source TEXT NOT NULL, position INTEGER NOT NULL, length INTEGER NOT NULL, kind INTEGER NOT NULL,
 at TEXT NOT NULL, day TEXT NOT NULL, hour INTEGER NOT NULL,
 consumer TEXT NOT NULL, owner TEXT NOT NULL, channel_id TEXT NOT NULL, credential_id TEXT NOT NULL,
 key_id TEXT NOT NULL, channel TEXT NOT NULL, model TEXT NOT NULL, model_lower TEXT NOT NULL, serving_lower TEXT NOT NULL,
 actor TEXT NOT NULL, action TEXT NOT NULL, trace TEXT NOT NULL,
 failed INTEGER NOT NULL, known INTEGER NOT NULL, input INTEGER NOT NULL, output INTEGER NOT NULL,
 reasoning INTEGER NOT NULL, cache_read INTEGER NOT NULL, cache_create INTEGER NOT NULL, total INTEGER NOT NULL,
 latency INTEGER NOT NULL, ttft INTEGER NOT NULL, balanced INTEGER NOT NULL, superseded INTEGER NOT NULL,
 PRIMARY KEY (source, position, kind));
CREATE INDEX call_trace ON events(trace) WHERE kind=2;
CREATE TABLE sources (name TEXT PRIMARY KEY, offset INTEGER NOT NULL, modified INTEGER NOT NULL, signature TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO sources VALUES(?,?,?,?)", info.Name(), info.Size(), info.ModTime().UnixNano(), "stale"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := usageIDs(t, s, Filter{Provider: "kimi"}); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("rebuilt index lost the record: %v", got)
	}
}
