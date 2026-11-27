package sink

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClickHouseCreatesTableAndInserts(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("query"))
		bodies = append(bodies, string(b))
		mu.Unlock()
		if r.Header.Get("X-ClickHouse-User") != "u" || r.Header.Get("X-ClickHouse-Key") != "secret" {
			w.WriteHeader(401)
			return
		}
	}))
	defer srv.Close()
	t.Setenv("CH_PW", "secret")
	cfg, err := Decode([]byte("clickhouse:\n  url: " + srv.URL + "\n  username: u\n  password_env: CH_PW\n  flush_interval: 1s\n  batch_size: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg)
	c.Add(Row{TS: time.Unix(1_800_000_000, 0), Provider: "claude", Model: "claude-opus-5-5", InputTokens: 10, CostUSD: 0.5})
	c.Add(Row{TS: time.Unix(1_800_000_001, 0), Provider: "codex", Model: "gpt-6-luna"})
	c.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 3 || !strings.HasPrefix(queries[0], "CREATE DATABASE IF NOT EXISTS prox") || !strings.Contains(queries[1], "CREATE TABLE IF NOT EXISTS prox.requests") || queries[2] != "INSERT INTO prox.requests FORMAT JSONEachRow" {
		t.Fatalf("queries: %q", queries)
	}
	lines := strings.Split(strings.TrimSpace(bodies[2]), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"provider":"claude"`) || !strings.Contains(lines[0], `"cost_usd":0.5`) {
		t.Fatalf("insert body: %s", bodies[2])
	}
	if st := c.Status(); st.Sent != 2 || st.Error != "" {
		t.Fatalf("status: %+v", st)
	}
}

func TestDecodeValidation(t *testing.T) {
	if cfg, err := Decode(nil); err != nil || cfg.URL != "" {
		t.Fatalf("empty: %+v %v", cfg, err)
	}
	if _, err := Decode([]byte("clickhouse:\n  url: http://x:8123\n  table: \"bad;drop\"\n")); err == nil {
		t.Fatal("expected identifier error")
	}
}
