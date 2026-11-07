package multiuser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedJournal(tb testing.TB, s *Store, count int) time.Time {
	tb.Helper()
	end := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(s.dir, "journal")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		tb.Fatal(err)
	}
	writers := map[string]*bufio.Writer{}
	files := map[string]*os.File{}
	for i := 0; i < count; i++ {
		request := i / 3
		at := end.Add(-time.Duration((request*7919)%864000) * time.Second)
		day := at.Format("2006-01-02")
		writer := writers[day]
		if writer == nil {
			file, err := os.Create(filepath.Join(dir, day+".jsonl"))
			if err != nil {
				tb.Fatal(err)
			}
			files[day] = file
			writer = bufio.NewWriterSize(file, 128*1024)
			writers[day] = writer
		}
		u := UsageEvent{
			ID: fmt.Sprintf("event-%d", i), RequestID: fmt.Sprintf("attempt-%d", i), TraceID: fmt.Sprintf("trace-%d", request), At: at,
			Consumer: fmt.Sprintf("user-%d", request%20), Owner: fmt.Sprintf("owner-%d", request%5),
			KeyID: fmt.Sprintf("key-%d", request%100), KeyName: "工作用量",
			ChannelID: fmt.Sprintf("channel-%d", request%10), Channel: fmt.Sprintf("route-%d", request%10),
			CredentialID: fmt.Sprintf("credential-%d", request%50), Model: fmt.Sprintf("model-%d", request%8),
			Provider: []string{"openai", "kimi", "openai-compatible-hub-ch-x-y"}[i%3], Failed: i%3 == 0,
			Status: []int{502, 200, 200, 429}[i%4], Stream: i%2 == 0, Endpoint: []string{"POST /v1/chat/completions", "POST /v1/messages"}[i%2],
			ReasoningEffort: []string{"", "high", "low"}[i%3], TokensKnown: true,
			InputTokens: 1000, OutputTokens: 100, ReasoningTokens: 50, CacheReadTokens: 500, TotalTokens: 1100,
			LatencyMS: 2000, TTFTMS: 200,
		}
		if err := json.NewEncoder(writer).Encode(journalEntry{Usage: &u}); err != nil {
			tb.Fatal(err)
		}
	}
	for day, writer := range writers {
		if err := writer.Flush(); err != nil {
			tb.Fatal(err)
		}
		if err := files[day].Close(); err != nil {
			tb.Fatal(err)
		}
	}
	return end
}

func BenchmarkJournalQueries(b *testing.B) {
	for _, count := range []int{100_000, 1_000_000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s, err := Open(b.TempDir(), time.Hour)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = s.Close() })
			now := seedJournal(b, s, count)
			started := time.Now()
			if _, _, err := s.Usage(Filter{PageSize: 1}); err != nil {
				b.Fatal(err)
			}
			b.Logf("index construction: %s", time.Since(started))
			var bytes int64
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if info, err := os.Stat(filepath.Join(s.dir, "journal-index.sqlite"+suffix)); err == nil {
					bytes += info.Size()
				}
			}
			b.Logf("index and sidecar size: %.1f MiB", float64(bytes)/(1024*1024))
			queries := []struct {
				name string
				run  func() error
			}{
				{"UsageFirst", func() error { _, _, err := s.Usage(Filter{PageSize: 50}); return err }},
				{"UsageFiltered", func() error {
					_, _, err := s.Usage(Filter{Consumer: "user-3", KeyID: "key-3", PageSize: 50})
					return err
				}},
				{"UsageDeep", func() error { _, _, err := s.Usage(Filter{Page: 500, PageSize: 50}); return err }},
				{"SummarySite", func() error { _, _, err := s.Summary(Filter{}); return err }},
				{"SummarySiteCached", func() error { _, _, err := s.Summary(Filter{}); return err }},
				{"SummaryWindow", func() error {
					_, _, err := s.Summary(Filter{From: now.Add(-10 * 24 * time.Hour), To: now.Add(time.Second)})
					return err
				}},
				{"SummaryMember", func() error { _, _, err := s.Summary(Filter{Consumer: "user-3"}); return err }},
				{"Performance24h", func() error { _, err := s.ModelPerformance(now); return err }},
			}
			for _, query := range queries {
				b.Run(query.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if err := query.run(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
			b.Run("AppendDuringSummary", func(b *testing.B) {
				var sum, longest time.Duration
				for b.Loop() {
					started := make(chan struct{})
					done := make(chan error, 1)
					go func() {
						close(started)
						_, _, err := s.SummaryContext(context.Background(), Filter{Model: "model-"})
						done <- err
					}()
					<-started
					for i := 0; i < 100; i++ {
						begin := time.Now()
						if err := s.RecordCall(CallEvent{At: now, Model: "benchmark"}); err != nil {
							b.Fatal(err)
						}
						elapsed := time.Since(begin)
						sum += elapsed
						longest = max(longest, elapsed)
					}
					if err := <-done; err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(sum.Nanoseconds())/float64(b.N*100)/1e6, "append-mean-ms")
				b.ReportMetric(float64(longest.Nanoseconds())/1e6, "append-max-ms")
			})
		})
	}
}
