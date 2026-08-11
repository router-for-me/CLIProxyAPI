package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestFetchLiteLLMSpendLogsUsesPaginatedEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"request_id":"req-1","spend":0.1}],"total":1,"page":1,"page_size":25,"total_pages":1}`))
	}))
	defer server.Close()

	oldClient := defaultSyncClient
	defaultSyncClient = server.Client()
	defer func() { defaultSyncClient = oldClient }()

	h := &Handler{}
	logs, err := h.fetchLiteLLMSpendLogs(context.Background(), server.URL, "master")
	if err != nil {
		t.Fatalf("fetchLiteLLMSpendLogs: %v", err)
	}
	if len(logs) != 1 || logs[0].Event.RequestID != "req-1" {
		t.Fatalf("logs = %+v; want one parsed log", logs)
	}
	if !strings.HasPrefix(gotPath, "/spend/logs/v2?") {
		t.Fatalf("request path = %q; want paginated /spend/logs/v2 endpoint", gotPath)
	}
	if !strings.Contains(gotPath, "page_size=25") {
		t.Fatalf("request path = %q; want page_size=25", gotPath)
	}
	if !strings.Contains(gotPath, "start_date=") || !strings.Contains(gotPath, "end_date=") {
		t.Fatalf("request path = %q; want bounded date range", gotPath)
	}
}

func TestFetchLiteLLMSpendLogsCollectsAllPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		// Two pages of 25 rows each. Build a lean JSON body by hand.
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i := 0; i < 25; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"request_id":"req-`)
			b.WriteString(page)
			b.WriteString(`-`)
			b.WriteString(strconv.Itoa(i))
			b.WriteString(`","spend":0.1}`)
		}
		b.WriteString(`],"total":50,"page":`)
		b.WriteString(page)
		b.WriteString(`,"page_size":25,"total_pages":2}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer server.Close()

	oldClient := defaultSyncClient
	defaultSyncClient = server.Client()
	defer func() { defaultSyncClient = oldClient }()

	h := &Handler{}
	logs, err := h.fetchLiteLLMSpendLogs(context.Background(), server.URL, "master")
	if err != nil {
		t.Fatalf("fetchLiteLLMSpendLogs: %v", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d; want 2 (both pages)", calls)
	}
	if len(logs) != 50 {
		t.Fatalf("collected %d logs; want 50 (2 pages x 25)", len(logs))
	}
	seen := map[string]bool{}
	for _, lg := range logs {
		if seen[lg.Event.RequestID] {
			t.Fatalf("duplicate request_id %q across pages", lg.Event.RequestID)
		}
		seen[lg.Event.RequestID] = true
	}
}
