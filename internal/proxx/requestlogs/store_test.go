package requestlogs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

var baseTime = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// fixtureCopy writes a testdata fixture under name with its request timestamp set to ts and the
// file mtime set to ts+100ms.
func fixtureCopy(t *testing.T, dir, fixture, name string, ts time.Time) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitN(string(data), "\n", 8)
	for i, l := range lines[:7] {
		if strings.HasPrefix(l, "Timestamp: ") {
			lines[i] = "Timestamp: " + ts.Format(time.RFC3339Nano)
			break
		}
	}
	path := writeTemp(t, dir, name, strings.Join(lines, "\n"))
	mt := ts.Add(100 * time.Millisecond)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
	return path
}

func logName(prefix string, i int) string {
	return fmt.Sprintf("%sv1-chat-completions-2026-11-16T120000-%08x.log", prefix, i)
}

func newTestStore(dir string, maxBytes int64) *Store {
	s := NewStore()
	s.Configure(dir, maxBytes, true)
	s.SetClock(func() time.Time { return baseTime.Add(time.Hour) })
	return s
}

func TestListPagingAndFilters(t *testing.T) {
	dir := t.TempDir()
	fixtures := []string{"openai-nonstream.log", "openai-stream.log", "failover.log"}
	for i := 0; i < 9; i++ {
		fixtureCopy(t, dir, fixtures[i%3], logName("", i), baseTime.Add(time.Duration(i)*time.Second))
	}
	fixtureCopy(t, dir, "error-400.log", logName("error-", 9), baseTime.Add(9*time.Second))
	writeTemp(t, dir, "main.log", "app log\n")
	writeTemp(t, dir, "request-body-123.tmp", "spool\n")
	s := newTestStore(dir, 0)

	var all []Row
	cursor := ""
	for page := 0; page < 10; page++ {
		p, err := s.List(Query{Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, p.Items...)
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(all) != 10 {
		t.Fatalf("paged %d rows, want 10", len(all))
	}
	for i, r := range all {
		want := fmt.Sprintf("%08x", 9-i)
		if r.ID != want {
			t.Fatalf("row %d id %s, want %s (newest first)", i, r.ID, want)
		}
	}
	// A file that appears between pages must not shift later pages.
	p1, _ := s.List(Query{Limit: 4})
	fixtureCopy(t, dir, "openai-nonstream.log", logName("", 20), baseTime.Add(time.Minute))
	p2, _ := s.List(Query{Limit: 4, Cursor: p1.NextCursor})
	if p2.Items[0].ID != fmt.Sprintf("%08x", 5) {
		t.Fatalf("page 2 starts at %s", p2.Items[0].ID)
	}

	count := func(q Query) int {
		q.Limit = 500
		p, err := s.List(q)
		if err != nil {
			t.Fatal(err)
		}
		return len(p.Items)
	}
	if n := count(Query{Status: "error"}); n != 1 {
		t.Fatalf("status=error -> %d", n)
	}
	if n := count(Query{Status: "4xx"}); n != 1 {
		t.Fatalf("status=4xx -> %d", n)
	}
	if n := count(Query{Status: "2xx"}); n != 10 {
		t.Fatalf("status=2xx -> %d", n)
	}
	if n := count(Query{Model: "AUTO"}); n != 3 {
		t.Fatalf("model=auto -> %d", n)
	}
	if n := count(Query{Model: "model-ok"}); n != 10 { // requested model-ok (7) + served model-ok via group (3)
		t.Fatalf("model=model-ok -> %d", n)
	}
	if n := count(Query{Provider: "openai-compatible-p400"}); n != 1 {
		t.Fatalf("provider=p400 -> %d", n)
	}
	if n := count(Query{Q: "RATE LIMITED"}); n != 3 {
		t.Fatalf("q=rate limited -> %d", n)
	}
	if n := count(Query{Since: baseTime.Add(5 * time.Second).UnixMilli(), Until: baseTime.Add(7 * time.Second).UnixMilli()}); n != 3 {
		t.Fatalf("since/until -> %d", n)
	}
	if _, err := s.List(Query{Cursor: "!!!"}); err != ErrBadCursor {
		t.Fatalf("bad cursor err = %v", err)
	}
	st := s.Status()
	if st.TotalBytes == 0 || st.Dir != dir {
		t.Fatalf("status = %+v", st)
	}

	row := all[len(all)-1]
	if row.Group != "" || row.Provider != "openai-compatible-pok" || row.Status != 200 || row.DurationMs == nil || *row.DurationMs != 100 || i64(row.InputTokens) != 3 {
		t.Fatalf("row = %+v", row)
	}
	failover := all[len(all)-3]
	if failover.Group != "auto" || failover.ServedModel != "model-ok" || failover.Attempts != 2 {
		t.Fatalf("failover row = %+v", failover)
	}
}

func TestUsageJoin(t *testing.T) {
	dir := t.TempDir()
	fixtureCopy(t, dir, "failover.log", logName("", 0xabc), baseTime)
	s := newTestStore(dir, 0)
	cost := 0.0012
	s.usage.HandleUsage(context.Background(), usage.Record{TraceID: "01a1e31d-9191-74e2-8fbc-148200000abc", Provider: "openai-compatible-p429", Model: "up-429", Failed: true})
	s.usage.HandleUsage(context.Background(), usage.Record{
		TraceID: "01a1e31d-9191-74e2-8fbc-148200000abc", Provider: "openai-compatible-pok", Model: "up-ok",
		Detail: usage.Detail{InputTokens: 3, OutputTokens: 4, CostDetails: &usage.CostDetails{FinalCost: &cost, CostSource: "catalog"}},
	})
	p, _ := s.List(Query{})
	r := p.Items[0]
	if r.Provider != "openai-compatible-pok" || r.ServedModel != "model-ok" || i64(r.InputTokens) != 3 || i64(r.OutputTokens) != 4 ||
		r.CostUSD == nil || *r.CostUSD != cost || r.CostSource != "catalog" || r.Attempts != 2 {
		t.Fatalf("joined row = %+v", r)
	}
	d, err := s.Detail("01a1e31d-9191-74e2-8fbc-148200000abc")
	if err != nil || d.Row.ID != "00000abc" || len(d.Attempts) != 2 || d.RawSizeBytes == 0 {
		t.Fatalf("detail by full id: %v %+v", err, d)
	}
}

func TestRetentionDeletesOldestRequestLogsOnly(t *testing.T) {
	dir := t.TempDir()
	var sizes []int64
	for i := 0; i < 5; i++ {
		path := fixtureCopy(t, dir, "openai-nonstream.log", logName("", i), baseTime.Add(time.Duration(i)*time.Minute))
		info, _ := os.Stat(path)
		sizes = append(sizes, info.Size())
	}
	fixtureCopy(t, dir, "error-400.log", logName("error-", 9), baseTime.Add(-time.Hour)) // oldest
	appLog := writeTemp(t, dir, "main.log", strings.Repeat("x", 100000))
	old := baseTime.Add(-48 * time.Hour)
	_ = os.Chtimes(appLog, old, old)

	s := newTestStore(dir, 0)
	s.Refresh()
	total := s.Status().TotalBytes
	// Cap that fits only the three newest normal logs.
	limit := sizes[2] + sizes[3] + sizes[4]
	s.Configure(dir, limit, true)
	s.Refresh()
	p, _ := s.List(Query{Limit: 50})
	if len(p.Items) != 3 || p.Items[2].ID != fmt.Sprintf("%08x", 2) {
		t.Fatalf("after retention: %d rows (total before %d)", len(p.Items), total)
	}
	if _, err := os.Stat(appLog); err != nil {
		t.Fatal("retention removed main.log")
	}
	if s.Status().TotalBytes > limit {
		t.Fatalf("total %d > cap %d", s.Status().TotalBytes, limit)
	}
	// Unlimited cap keeps everything.
	s.Configure(dir, 0, true)
	fixtureCopy(t, dir, "openai-stream.log", logName("", 7), baseTime.Add(10*time.Minute))
	p, _ = s.List(Query{})
	if len(p.Items) != 4 {
		t.Fatalf("unlimited: %d rows", len(p.Items))
	}
}

func TestSubscribeEmitsOnlyCompletedNewFiles(t *testing.T) {
	dir := t.TempDir()
	fixtureCopy(t, dir, "openai-nonstream.log", logName("", 1), baseTime)
	s := NewStore()
	s.Configure(dir, 0, true)
	now := baseTime.Add(time.Hour)
	s.SetClock(func() time.Time { return now })
	rows, cancel := s.Subscribe()
	defer cancel()
	s.Refresh() // initial scan: existing files are not announced

	// A new file modified "just now" is not complete yet.
	path := fixtureCopy(t, dir, "openai-stream.log", logName("", 2), now)
	_ = os.Chtimes(path, now, now)
	s.Refresh()
	select {
	case r := <-rows:
		t.Fatalf("emitted incomplete file %s", r.ID)
	default:
	}
	now = now.Add(2 * time.Second)
	s.Refresh()
	select {
	case r := <-rows:
		if r.ID != "00000002" || !r.Stream {
			t.Fatalf("row = %+v", r)
		}
	default:
		t.Fatal("completed file not emitted")
	}
	s.Refresh()
	select {
	case r := <-rows:
		t.Fatalf("emitted twice: %s", r.ID)
	default:
	}
}

func TestDeleteAndDeleteAll(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		fixtureCopy(t, dir, "openai-nonstream.log", logName("", i), baseTime.Add(time.Duration(i)*time.Second))
	}
	appLog := writeTemp(t, dir, "main.log", "app\n")
	s := newTestStore(dir, 0)
	if err := s.Delete("00000001"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("00000001"); err != ErrNotFound {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := s.Detail("../etc/passwd"); err != ErrNotFound {
		t.Fatalf("traversal err = %v", err)
	}
	n, err := s.DeleteAll()
	if err != nil || n != 2 {
		t.Fatalf("delete all = %d %v", n, err)
	}
	if _, err := os.Stat(appLog); err != nil {
		t.Fatal("delete all removed main.log")
	}
}

func TestUsageJoinPrefersFullRequestID(t *testing.T) {
	dir := t.TempDir()
	// The fixture's X-Cpa-Trace-Id ends in the full request id 01a1e31d-9191-74e2-8fbc-148299c2cb23.
	fixtureCopy(t, dir, "failover.log", "v1-chat-completions-2026-11-16T121848-99c2cb23.log", baseTime)
	s := newTestStore(dir, 0)
	s.usage.HandleUsage(context.Background(), usage.Record{TraceID: "01a1e31d-9191-74e2-8fbc-148299c2cb23", Provider: "real", Detail: usage.Detail{InputTokens: 3}})
	// A different request whose short id collides is published later.
	s.usage.HandleUsage(context.Background(), usage.Record{TraceID: "ffffffff-0000-74e2-8fbc-148299c2cb23", Provider: "decoy", Detail: usage.Detail{InputTokens: 99}})
	p, _ := s.List(Query{})
	if r := p.Items[0]; r.Provider != "real" || i64(r.InputTokens) != 3 {
		t.Fatalf("joined the colliding request: %+v", r)
	}
}

func TestWebsocketTranscript(t *testing.T) {
	content := strings.Join([]string{
		"=== REQUEST INFO ===",
		"Version: dev",
		"URL: /v1/responses",
		"Method: GET",
		"Downstream Transport: websocket",
		"Upstream Transport: websocket+http",
		"Timestamp: 2026-11-16T12:00:00Z",
		"", "",
		"=== HEADERS ===",
		"Upgrade: websocket",
		"Authorization: Bearer te...ey",
		"", "",
		"=== WEBSOCKET TIMELINE ===",
		"Timestamp: 2026-11-16T12:00:00.1Z",
		"Event: websocket.request",
		`{"type":"response.create","model":"gpt-5"}`,
		"",
		"Timestamp: 2026-11-16T12:00:01Z",
		"Event: websocket.response",
		`{"type":"response.completed"}`,
		"", "",
		"=== API WEBSOCKET TIMELINE ===",
		"Timestamp: 2026-11-16T12:00:00.2Z",
		"Event: api.websocket.request",
		"Upstream URL: wss://example.test/v1/responses",
		"Auth: provider=codex, auth_id=x, type=oauth",
		"Headers:",
		"Authorization: Bearer sk-...xx",
		"",
		"Body:",
		`{"type":"response.create"}`,
		"",
		"Timestamp: 2026-11-16T12:00:00.3Z",
		"Event: api.websocket.handshake",
		"Status: 101",
		"Headers:",
		"Upgrade: websocket",
		"", "",
		"=== API REQUEST 1 ===",
		"Timestamp: 2026-11-16T12:00:00.2Z",
		"Upstream URL: https://example.test/v1/responses",
		"HTTP Method: POST",
		"",
		"Headers:",
		"Content-Type: application/json",
		"",
		"Body:",
		`{"model":"gpt-5"}`,
		"", "",
		"=== API RESPONSE 1 ===",
		"Timestamp: 2026-11-16T12:00:00.4Z",
		"",
		"Status: 200",
		"Headers:",
		"Content-Type: application/json",
		"",
		"Body:",
		`{"ok":true}`,
		"", "",
	}, "\n")
	dir := t.TempDir()
	path := writeTemp(t, dir, "v1-responses-2026-11-16T120000-0000beef.log", content)
	mt := baseTime.Add(time.Second)
	_ = os.Chtimes(path, mt, mt)
	s := newTestStore(dir, 0)
	p, _ := s.List(Query{})
	if len(p.Items) != 1 || p.Items[0].Status != 0 || p.Items[0].Error || p.Items[0].Path != "/v1/responses" || p.Items[0].Attempts != 1 {
		t.Fatalf("websocket row = %+v", p.Items)
	}
	d, err := s.Detail("0000beef")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.WebsocketTimeline, "websocket.request") || !strings.HasSuffix(d.WebsocketTimeline, `{"type":"response.completed"}`) {
		t.Fatalf("websocket_timeline = %q", d.WebsocketTimeline)
	}
	if !strings.Contains(d.APIWebsocketTimeline, "api.websocket.handshake") || !strings.Contains(d.APIWebsocketTimeline, "Status: 101") {
		t.Fatalf("api_websocket_timeline = %q", d.APIWebsocketTimeline)
	}
	if len(d.Attempts) != 1 || *d.Attempts[0].Status != 200 || d.Attempts[0].Body != `{"model":"gpt-5"}` || d.Response.Status != 0 || d.Response.SSEEvents != nil {
		t.Fatalf("attempts/response = %+v %+v", d.Attempts, d.Response)
	}
}

func TestFailedRequestWithoutUsageHasUnknownTokensAndCost(t *testing.T) {
	dir := t.TempDir()
	fixtureCopy(t, dir, "error-400.log", "error-v1-chat-completions-2026-11-16T121848-2ddaa355.log", baseTime)
	s := newTestStore(dir, 0)
	zero := 0.0
	s.usage.HandleUsage(context.Background(), usage.Record{
		TraceID:  "01a1e31d-9196-74f5-9752-76e52ddaa355",
		Provider: "openai-compatible-p400",
		Model:    "up-400",
		Failed:   true,
		Detail:   usage.Detail{CostDetails: &usage.CostDetails{FinalCost: &zero, CostSource: "catalog"}},
	})
	p, _ := s.List(Query{})
	r := p.Items[0]
	if !r.Error || r.Provider != "openai-compatible-p400" {
		t.Fatalf("row = %+v", r)
	}
	if r.InputTokens != nil || r.OutputTokens != nil || r.CostUSD != nil || r.CostSource != "" {
		t.Fatalf("failed request without usage must have unknown tokens/cost, got in=%v out=%v cost=%v source=%q", r.InputTokens, r.OutputTokens, r.CostUSD, r.CostSource)
	}
}
