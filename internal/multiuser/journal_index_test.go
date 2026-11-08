package multiuser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func openJournalTest(t *testing.T, dir string) *Store {
	t.Helper()
	if dir == "" {
		dir = filepath.Join(t.TempDir(), "中文索引")
	}
	s, err := Open(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func referenceJournal(t *testing.T, s *Store) []journalEntry {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(s.dir, "journal", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := []journalEntry{}
	for _, path := range files {
		file, errOpen := os.Open(path)
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var entry journalEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			out = append(out, entry)
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	return out
}

func referenceSummary(entries []journalEntry, filter Filter) (Summary, []Summary) {
	var total Summary
	groups := map[string]*Summary{}
	traces := map[string]bool{}
	groupTraces := map[string]map[string]bool{}
	for _, entry := range entries {
		u := entry.Usage
		if u == nil || !matchUsage(*u, filter) {
			continue
		}
		day := u.At.UTC().Format("2006-01-02")
		key := strings.Join([]string{day, u.Consumer, u.Owner, u.Channel, u.Model, u.CredentialID}, "|")
		group := groups[key]
		if group == nil {
			group = &Summary{Day: day, Consumer: u.Consumer, Owner: u.Owner, Channel: u.Channel, Model: u.Model, CredentialID: u.CredentialID}
			groups[key] = group
			groupTraces[key] = map[string]bool{}
		}
		for _, sum := range []*Summary{&total, group} {
			sum.Executions++
			if u.Failed {
				sum.Failures++
			}
			if !u.TokensKnown {
				sum.UnknownTokenRecords++
			}
			sum.InputTokens += u.InputTokens
			sum.OutputTokens += u.OutputTokens
			sum.ReasoningTokens += u.ReasoningTokens
			sum.CacheReadTokens += u.CacheReadTokens
			sum.CacheCreationTokens += u.CacheCreationTokens
			sum.TotalTokens += u.TotalTokens
			if !u.Failed && u.LatencyMS > 0 {
				sum.LatencyMS += u.LatencyMS
				sum.LatencyCount++
			}
			if !u.Failed && u.TTFTMS > 0 {
				sum.TTFTMS += u.TTFTMS
				sum.TTFTCount++
			}
		}
		trace := u.TraceID
		if trace == "" {
			trace = u.RequestID
		}
		if trace == "" {
			trace = u.ID
		}
		if !traces[trace] {
			total.Calls++
			traces[trace] = true
		}
		if !groupTraces[key][trace] {
			group.Calls++
			groupTraces[key][trace] = true
		}
	}
	out := []Summary{}
	for _, group := range groups {
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day > out[j].Day
		}
		return out[i].Channel+out[i].Consumer+out[i].Model < out[j].Channel+out[j].Consumer+out[j].Model
	})
	return total, out
}

func TestJournalIndexMatchesReference(t *testing.T) {
	s := openJournalTest(t, "")
	now := seedJournal(t, s, 700)
	for i, model := range []string{"厂商/Model-A", "route-0/厂商/Model-A", "MODEL_%", ""} {
		u := UsageEvent{At: now, ID: fmt.Sprint(i), RequestID: "fallback-request", Consumer: "user-0", Channel: "route-0", Model: model}
		if err := s.appendEntry(now, journalEntry{Usage: &u}); err != nil {
			t.Fatal(err)
		}
	}
	entries := referenceJournal(t, s)
	for _, filter := range []Filter{
		{}, {Page: 2, PageSize: 17}, {Page: 1_000_000}, {Consumer: "user-3"}, {Owner: "owner-1"},
		{KeyID: "key-3"}, {ChannelID: "channel-2"}, {CredentialID: "credential-7"},
		{From: now.Add(-24 * time.Hour), To: now}, {From: now, To: now.Add(time.Nanosecond)},
		{Model: " MoDeL-1 "}, {Model: "Model-A（route-0/厂商/Model-A）"},
		{Model: "Model-A(route-0/厂商/Model-A)"}, {Model: "_%"}, {Consumer: "missing"},
		{Provider: "kimi"}, {Provider: "openai-compatible"}, {Stream: "stream"}, {Stream: "nonstream"},
		{Status: "200"}, {Status: "4xx"}, {Status: "5xx", Owner: "owner-2"}, {Endpoint: "POST /v1/messages"},
		{Effort: "none"}, {Effort: "high", Stream: "stream"}, {Balanced: "no"},
	} {
		t.Run(fmt.Sprintf("%+v", filter), func(t *testing.T) {
			want := []UsageEvent{}
			for _, entry := range entries {
				if entry.Usage != nil && matchUsage(*entry.Usage, filter) {
					want = append(want, *entry.Usage)
				}
			}
			sort.SliceStable(want, func(i, k int) bool { return want[i].At.After(want[k].At) })
			got, total, err := s.Usage(filter)
			start, end := pageBounds(filter, len(want))
			if err != nil || total != len(want) || !reflect.DeepEqual(got, want[start:end]) {
				t.Fatalf("page mismatch: got %d/%d want %d/%d err=%v", len(got), total, end-start, len(want), err)
			}
			wantTotal, wantGroups := referenceSummary(entries, filter)
			gotTotal, gotGroups, err := s.Summary(filter)
			if err != nil || gotTotal != wantTotal || !reflect.DeepEqual(gotGroups, wantGroups) {
				t.Fatalf("summary mismatch: got %+v want %+v; groups %d/%d err=%v", gotTotal, wantTotal, len(gotGroups), len(wantGroups), err)
			}
		})
	}
}

func TestJournalIndexIncrementalRecoveryAndFileChanges(t *testing.T) {
	s := openJournalTest(t, "")
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	appendUsage := func(store *Store, when time.Time, id string) {
		t.Helper()
		u := UsageEvent{ID: id, At: when, TraceID: id, Consumer: "alice", Model: "model"}
		if err := store.appendEntry(when, journalEntry{Usage: &u}); err != nil {
			t.Fatal(err)
		}
	}
	appendUsage(s, at, "first")
	if _, count, err := s.Usage(Filter{}); err != nil || count != 1 {
		t.Fatalf("initial index: count=%d err=%v", count, err)
	}
	if _, _, err := s.Summary(Filter{}); err != nil {
		t.Fatal(err)
	}
	appendUsage(s, at.Add(-48*time.Hour), "late-old-day")
	appendUsage(s, at, "retry")
	// Summary analytics tolerate a few seconds of staleness; advancing the
	// store clock past the TTL proves the new rows do land in the index.
	s.now = func() time.Time { return time.Now().Add(aggregateTTL + time.Second) }
	total, _, err := s.Summary(Filter{})
	if err != nil || total.Executions != 3 {
		t.Fatalf("incremental/cached total: %+v err=%v", total, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openJournalTest(t, s.dir)
	if _, count, err := reopened.Usage(Filter{}); err != nil || count != 3 {
		t.Fatalf("restart duplicated or lost events: %d %v", count, err)
	}
	path := filepath.Join(s.dir, "journal", at.Format("2006-01-02")+".jsonl")
	entry := journalEntry{Usage: &UsageEvent{ID: "replacement", At: at, Model: "new"}}
	raw, _ := json.Marshal(entry)
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if items, count, err := reopened.Usage(Filter{Model: "new"}); err != nil || count != 1 || items[0].ID != "replacement" {
		t.Fatalf("replacement not rebuilt: %d %v", count, err)
	}
	oldPath := filepath.Join(s.dir, "journal", at.Add(-48*time.Hour).Format("2006-01-02")+".jsonl")
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if _, count, err := reopened.Usage(Filter{}); err != nil || count != 1 {
		t.Fatalf("deleted source left stale rows: %d %v", count, err)
	}
	if err := reopened.RecordCall(CallEvent{At: at, Model: "Case_%", Consumer: "alice", KeyID: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Audit(AuditEvent{At: at, Actor: "alice", Action: "credential.update"}); err != nil {
		t.Fatal(err)
	}
	if calls, count, err := reopened.Calls(Filter{Consumer: "alice", KeyID: "key", Model: "_%"}); err != nil || count != 1 || calls[0].Model != "Case_%" {
		t.Fatalf("call filter: %d %v", count, err)
	}
	if _, count, err := reopened.Calls(Filter{Model: "case"}); err != nil || count != 0 {
		t.Fatalf("call matching lost case sensitivity: %d %v", count, err)
	}
	if _, count, err := reopened.Audits(Filter{Actor: "alice", Action: "update"}); err != nil || count != 1 {
		t.Fatalf("audit filter: %d %v", count, err)
	}
}

func TestJournalIndexResumesCommittedBatchesWithoutHidingCorruption(t *testing.T) {
	s := openJournalTest(t, "")
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(s.dir, "journal", "2026-10-24.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriter(file)
	for i := 0; i < 5000; i++ {
		_ = json.NewEncoder(writer).Encode(journalEntry{Usage: &UsageEvent{ID: fmt.Sprint(i), At: at}})
	}
	_ = writer.Flush()
	position, _ := file.Seek(0, 1)
	_, _ = file.WriteString("{broken\n")
	_ = file.Close()
	if _, _, err := s.Usage(Filter{}); err == nil || !strings.Contains(err.Error(), "invalid journal") {
		t.Fatalf("corruption returned incomplete success: %v", err)
	}
	var offset int64
	if err := s.index.db.QueryRow("SELECT offset FROM sources WHERE name='2026-10-24.jsonl'").Scan(&offset); err != nil || offset == 0 || offset >= position {
		t.Fatalf("expected committed intermediate batch: %d %v", offset, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, position); err != nil {
		t.Fatal(err)
	}
	reopened := openJournalTest(t, s.dir)
	if _, count, err := reopened.Usage(Filter{}); err != nil || count != 5000 {
		t.Fatalf("checkpoint recovery: %d %v", count, err)
	}
}

func TestJournalReadersDoNotHoldAppendLockAndCanCancel(t *testing.T) {
	s := openJournalTest(t, "")
	if _, _, err := s.Usage(Filter{}); err != nil {
		t.Fatal(err)
	}
	s.index.syncMu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.RecordCall(CallEvent{Model: "model"}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("index synchronization blocked durable append")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.UsageContext(ctx, Filter{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation not propagated: %v", err)
	}
	s.index.syncMu.Unlock()
}

func TestJournalColdHTTPDoesNotReturnPartialTotals(t *testing.T) {
	s := openJournalTest(t, "")
	s.logMu.Lock()
	s.PrepareJournal()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/v8/hub/usage", nil)
	(&Controller{Store: s}).usage(ctx)
	s.logMu.Unlock()
	if recorder.Code != 503 || recorder.Header().Get("Retry-After") != "2" || !strings.Contains(recorder.Body.String(), `"code":"journal_index_building"`) {
		t.Fatalf("cold index response: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"total"`) {
		t.Fatal("cold history was presented as a complete empty result")
	}
	if _, _, err := s.Usage(Filter{}); err != nil {
		t.Fatal(err)
	}
	if err := s.JournalReady(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalAggregateSingleFlightAndLastReaderCancellation(t *testing.T) {
	s := openJournalTest(t, "")
	j, err := s.queryJournal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Work that has not acquired a slot is dropped when its last reader leaves.
	for i := 0; i < aggregateSlots; i++ {
		j.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	ran := make(chan struct{}, 1)
	go func() {
		_, err := j.aggregate(ctx, "queued", func(context.Context) (any, int, error) {
			ran <- struct{}{}
			return 1, 1, nil
		})
		result <- err
	}()
	for {
		j.cacheMu.Lock()
		queued := j.flights["queued"] != nil
		j.cacheMu.Unlock()
		if queued {
			break
		}
		runtime.Gosched()
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for i := 0; i < aggregateSlots; i++ {
		<-j.slots
	}
	select {
	case <-ran:
		t.Fatal("abandoned queued aggregate still ran")
	default:
	}
	// Work already running outlives its reader and lands in the cache.
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		_, err := j.aggregate(ctx, "running", func(context.Context) (any, int, error) {
			close(started)
			<-release
			return "done", 1, nil
		})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	value, err := j.aggregate(context.Background(), "running", func(context.Context) (any, int, error) {
		t.Fatal("recomputed instead of sharing the finished result")
		return nil, 0, nil
	})
	if err != nil || value != "done" {
		t.Fatalf("value=%v err=%v", value, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Summary(Filter{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	j.cacheMu.Lock()
	defer j.cacheMu.Unlock()
	summaries := 0
	for key := range j.cache {
		if strings.HasPrefix(key, "summary:") {
			summaries++
		}
	}
	if summaries != 1 {
		t.Fatalf("identical aggregates not cached: %d", summaries)
	}
}

// Requests waiting for an aggregate slot must not hold database connections:
// with every slot busy, more waiting lists than the pool has connections still
// finish once the slots free up.
func TestJournalListsWaitingForSlotsDoNotExhaustConnections(t *testing.T) {
	s := openJournalTest(t, "")
	appendUsageEvent(t, s, UsageEvent{ID: "a", TraceID: "a", At: balancedFixtureAt, Consumer: "bob", Model: "gpt-5"})
	j, err := s.queryJournal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < aggregateSlots; i++ {
		j.slots <- struct{}{}
	}
	const waiting = 24
	var wg sync.WaitGroup
	errs := make(chan error, waiting)
	for i := 0; i < waiting; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.UsageContext(context.Background(), Filter{Consumer: fmt.Sprintf("user-%d", i), PageSize: 10})
			errs <- err
		}(i)
	}
	// Give the requests time to queue, then prove the pool is still usable.
	time.Sleep(100 * time.Millisecond)
	if err := j.db.QueryRow("SELECT 1").Scan(new(int)); err != nil {
		t.Fatalf("pool exhausted by queued lists: %v", err)
	}
	for i := 0; i < aggregateSlots; i++ {
		<-j.slots
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
