package usagestats

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// insertAgentSessions stores a main session that spans two models and two
// days, a subagent of it, a second session, and rows outside any session.
func insertAgentSessions(t *testing.T, store *Store) {
	t.Helper()
	const opus, haiku = "claude-opus-5-5", "claude-haiku-5-5"
	day1, day2 := testDay.Add(time.Hour), testDay.Add(25*time.Hour)
	mustInsert(t, store,
		Row{Timestamp: day1, Model: opus, SessionID: "main", StopReason: coreusage.StopReasonToolUse},
		Row{Timestamp: day1, Model: opus, SessionID: "main", StopReason: coreusage.StopReasonEndTurn},
		Row{Timestamp: day2, Model: haiku, SessionID: "main", StopReason: coreusage.StopReasonEndTurn},
		Row{Timestamp: day1, Model: haiku, SessionID: "subagent", ParentSessionID: "main", StopReason: coreusage.StopReasonEndTurn},
		Row{Timestamp: day2, Model: opus, SessionID: "other", StopReason: coreusage.StopReasonMaxTokens},
		Row{Timestamp: day2, Model: opus, StopReason: coreusage.StopReasonRefusal},
		Row{Timestamp: day2, Model: opus, StopReason: coreusage.StopReasonPause},
		Row{Timestamp: day2, Model: opus},
		Row{Timestamp: day2, Model: opus, StopReason: coreusage.StopReasonEndTurn, Failed: true},
	)
}

func aggregateOf(t *testing.T, store *Store, q Query) Aggregate {
	t.Helper()
	q.From, q.To = testDay, testDay.Add(48*time.Hour)
	aggregate, errAggregate := store.Aggregate(context.Background(), q)
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	return aggregate
}

func TestAggregateCountsTurnsOfFinishedReplies(t *testing.T) {
	store := openTestStore(t)
	insertAgentSessions(t, store)

	total := aggregateOf(t, store, Query{})
	if len(total.Groups) != 1 || total.Groups[0].Requests != 9 || total.Groups[0].Turns != 5 {
		t.Fatalf("total = %+v, want 9 requests and 5 turns: three end_turn, one max_tokens, one refusal", total.Groups)
	}
	byReason := groupByKey(t, aggregateOf(t, store, Query{GroupBy: []string{"stop_reason"}}), "stop_reason")
	for reason, want := range map[coreusage.StopReason][2]int64{
		coreusage.StopReasonEndTurn:   {4, 3},
		coreusage.StopReasonToolUse:   {1, 0},
		coreusage.StopReasonMaxTokens: {1, 1},
		coreusage.StopReasonRefusal:   {1, 1},
		coreusage.StopReasonPause:     {1, 0},
		"":                            {1, 0},
	} {
		group := byReason[string(reason)]
		if group.Requests != want[0] || group.Turns != want[1] {
			t.Errorf("stop_reason %q: requests %d turns %d, want %d and %d", reason, group.Requests, group.Turns, want[0], want[1])
		}
	}
	filtered := aggregateOf(t, store, Query{Filters: map[string]string{"stop_reason": "tool_use"}})
	if len(filtered.Groups) != 1 || filtered.Groups[0].Requests != 1 {
		t.Fatalf("filter stop_reason=tool_use = %+v, want one request", filtered.Groups)
	}
}

func TestAggregateCountsDistinctSessionsPerGroup(t *testing.T) {
	store := openTestStore(t)
	insertAgentSessions(t, store)

	sessionsBy := func(q Query, dimension string) map[string]int64 {
		out := map[string]int64{}
		for _, group := range aggregateOf(t, store, q).Groups {
			out[group.Dimensions[dimension]] = group.Sessions
		}
		return out
	}
	if got := sessionsBy(Query{}, "")[""]; got != 3 {
		t.Errorf("sessions over the range = %d, want 3: main, subagent, other", got)
	}
	if got := sessionsBy(Query{GroupBy: []string{"month"}}, "month"); len(got) != 1 || got["2026-11"] != 3 {
		t.Errorf("sessions by month = %v, want 3 in 2026-11", got)
	}
	// main is active on both days and with both models, so the groups add up
	// to more than the range.
	if got := sessionsBy(Query{GroupBy: []string{"day"}}, "day"); got["2026-11-12"] != 2 || got["2026-11-13"] != 2 {
		t.Errorf("sessions by day = %v, want 2 on each day", got)
	}
	if got := sessionsBy(Query{GroupBy: []string{"model"}}, "model"); got["claude-opus-5-5"] != 2 || got["claude-haiku-5-5"] != 2 {
		t.Errorf("sessions by model = %v, want 2 for each model", got)
	}
	topLevel := Query{Filters: map[string]string{"parent_session_id": ""}}
	if got := sessionsBy(topLevel, "")[""]; got != 2 {
		t.Errorf("sessions without a parent = %d, want 2: main and other", got)
	}
}

func TestRowsFromBeforeStopReasonsCountAsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	db, errOpen := sql.Open("sqlite", path)
	if errOpen != nil {
		t.Fatalf("open database: %v", errOpen)
	}
	for _, statement := range append(migrations[:2:2], "PRAGMA user_version = 2") {
		if _, errExec := db.Exec(statement); errExec != nil {
			t.Fatalf("build a version 2 database: %v", errExec)
		}
	}
	columns := map[string]any{"ts_ms": testDay.UnixMilli(), "model": "claude-opus-5-5", "session_id": "old"}
	rows, errColumns := db.Query("SELECT name, type FROM pragma_table_info('requests') WHERE name != 'id'")
	if errColumns != nil {
		t.Fatalf("read columns: %v", errColumns)
	}
	for rows.Next() {
		var name, kind string
		if errScan := rows.Scan(&name, &kind); errScan != nil {
			t.Fatalf("scan column: %v", errScan)
		}
		if _, set := columns[name]; !set {
			columns[name] = map[string]any{"TEXT": "", "INTEGER": 0, "REAL": 0.0}[kind]
		}
	}
	if errRows := rows.Err(); errRows != nil {
		t.Fatalf("read columns: %v", errRows)
	}
	names := make([]string, 0, len(columns))
	values := make([]any, 0, len(columns))
	for name, value := range columns {
		names = append(names, name)
		values = append(values, value)
	}
	statement := fmt.Sprintf("INSERT INTO requests (%s) VALUES (?%s)", strings.Join(names, ", "), strings.Repeat(", ?", len(names)-1))
	if _, errInsert := db.Exec(statement, values...); errInsert != nil {
		t.Fatalf("insert a version 2 row: %v", errInsert)
	}
	if errClose := db.Close(); errClose != nil {
		t.Fatalf("close database: %v", errClose)
	}

	store, errStore := Open(path)
	if errStore != nil {
		t.Fatalf("open store: %v", errStore)
	}
	t.Cleanup(func() { _ = store.Close() })
	aggregate := aggregateOf(t, store, Query{GroupBy: []string{"stop_reason", "parent_session_id"}})
	if len(aggregate.Groups) != 1 {
		t.Fatalf("groups = %+v, want the old row", aggregate.Groups)
	}
	group := aggregate.Groups[0]
	if group.Dimensions["stop_reason"] != "" || group.Dimensions["parent_session_id"] != "" || group.Requests != 1 || group.Turns != 0 || group.Sessions != 1 {
		t.Fatalf("old row = %+v, want an unknown stop reason that is not a turn", group)
	}
}

func TestLimitValueCountsTurnsAndSessionsOfCountedIntervals(t *testing.T) {
	store := openTestStore(t)
	key := QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "5h"}
	resets := testDay.Add(5 * time.Hour)
	mustRecordQuota(t, store,
		samplePoint(key, testDay, 10, resets),
		samplePoint(key, testDay.Add(time.Hour), 12, resets),
		samplePoint(key, testDay.Add(2*time.Hour), 12, resets),
	)
	request := func(at time.Duration, session string, reason coreusage.StopReason) Row {
		return Row{
			Timestamp: testDay.Add(at), Model: "claude-opus-5-5", AuthIndex: "auth-1", SessionID: session, StopReason: reason,
			CacheWriteTokens: 300, CacheWrite5mTokens: 100, CacheWrite1hTokens: 200, TotalTokens: 300,
		}
	}
	mustInsert(t, store,
		request(10*time.Minute, "a", coreusage.StopReasonToolUse),
		request(20*time.Minute, "a", coreusage.StopReasonEndTurn),
		request(30*time.Minute, "b", coreusage.StopReasonEndTurn),
		// The second interval does not rise, so its request is not counted.
		request(90*time.Minute, "c", coreusage.StopReasonEndTurn),
	)

	result, errValue := store.LimitValue(context.Background(), Query{From: testDay, To: testDay.Add(24 * time.Hour)})
	if errValue != nil {
		t.Fatalf("limit value: %v", errValue)
	}
	if len(result.Series) != 1 {
		t.Fatalf("series = %+v", result.Series)
	}
	value := result.Series[0]
	if value.Requests != 3 || value.Turns != 2 || value.Sessions != 2 || value.CacheWrite5mTokens != 300 || value.CacheWrite1hTokens != 600 {
		t.Fatalf("value = %+v, want 3 requests, 2 turns, 2 sessions, and 300/600 cache writes", value.LimitSpend)
	}
	if len(value.Models) != 1 || value.Models[0].Sessions != 2 || value.Models[0].Turns != 2 {
		t.Fatalf("models = %+v, want opus with the same turns and sessions", value.Models)
	}
}
