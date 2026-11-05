package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// testConfig builds a config with one Ollama Cloud connection.
func testConfig() *config.Config {
	return &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "ollama-cloud",
			BaseURL: "https://ollama.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "ollama-secret-key",
			}},
		}},
	}
}

func TestResolveConnectionMatchesNameCaseInsensitively(t *testing.T) {
	t.Parallel()

	svc := NewQuotaService()
	conn, err := svc.ResolveConnection(testConfig(), "  Ollama-Cloud ")
	if err != nil {
		t.Fatalf("ResolveConnection: %v", err)
	}
	if conn.Name != "ollama-cloud" || conn.APIKey != "ollama-secret-key" {
		t.Fatalf("connection = %+v, want name ollama-cloud with configured key", conn)
	}
	if !strings.HasPrefix(conn.BaseURL, "https://ollama.com") {
		t.Fatalf("base URL = %q, want ollama.com origin", conn.BaseURL)
	}
}

func TestResolveConnectionRejectsMisconfiguredGroups(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		group  config.OpenAICompatibility
		id     string
		wantWr error
	}{
		{
			name:   "unknown name",
			group:  config.OpenAICompatibility{Name: "other", BaseURL: "https://ollama.com/v1"},
			id:     "missing",
			wantWr: ErrConnectionNotFound,
		},
		{
			name:   "empty provider id",
			group:  config.OpenAICompatibility{Name: "ollama-cloud"},
			id:     "   ",
			wantWr: ErrConnectionNotFound,
		},
		{
			name:  "disabled group",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "https://ollama.com/v1", Disabled: true, APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}}},
			id:    "ollama-cloud",
		},
		{
			name:  "http scheme",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "http://ollama.com/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}}},
			id:    "ollama-cloud",
		},
		{
			name:  "spoofed host",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "https://evil.example.com/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}}},
			id:    "ollama-cloud",
		},
		{
			name:  "subdomain is not the origin",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "https://api.ollama.com/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}}},
			id:    "ollama-cloud",
		},
		{
			name:  "non-standard port",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "https://ollama.com:8443/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}}},
			id:    "ollama-cloud",
		},
		{
			name:  "no keys",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "https://ollama.com/v1"},
			id:    "ollama-cloud",
		},
		{
			name:  "all keys excluded by weight",
			group: config.OpenAICompatibility{Name: "ollama-cloud", BaseURL: "https://ollama.com/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k", Weight: intPtr(0)}}},
			id:    "ollama-cloud",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{tc.group}}
			svc := NewQuotaService()
			_, err := svc.ResolveConnection(cfg, tc.id)
			if err == nil {
				t.Fatal("expected resolution failure")
			}
			if tc.wantWr != nil && !errors.Is(err, tc.wantWr) {
				t.Fatalf("error = %v, want wrapping %v", err, tc.wantWr)
			}
			if strings.Contains(err.Error(), "ollama-secret-key") {
				t.Fatal("error leaked credential material")
			}
		})
	}
}

func TestResolveConnectionSelectsFirstUsableKey(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "ollama-cloud",
			BaseURL: "https://ollama.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "excluded-key", Weight: intPtr(0)},
				{APIKey: ""},
				{APIKey: "usable-key"},
			},
		}},
	}

	svc := NewQuotaService()
	conn, err := svc.ResolveConnection(cfg, "ollama-cloud")
	if err != nil {
		t.Fatalf("ResolveConnection: %v", err)
	}
	if conn.APIKey != "usable-key" {
		t.Fatalf("selected key = %q, want usable-key", conn.APIKey)
	}
}

func intPtr(value int) *int { return &value }

// usageBalanceServer serves canned usage and balance responses, counting hits.
type usageBalanceServer struct {
	server      *httptest.Server
	usageHits   int64
	balanceHits int64
	mu          sync.Mutex
}

func newUsageBalanceServer(usageStatus int, usageBody string, balanceStatus int, balanceBody string) *usageBalanceServer {
	s := &usageBalanceServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case usagePath:
			s.mu.Lock()
			s.usageHits++
			s.mu.Unlock()
			w.WriteHeader(usageStatus)
			body := strings.Replace(usageBody, `"range":"7d"`, `"range":"`+r.URL.Query().Get("range")+`"`, 1)
			if r.URL.Query().Get("range") == "24h" {
				body = strings.Replace(body, `"granularity":"day"`, `"granularity":"hour"`, 1)
			}
			fmt.Fprint(w, body)
		case balancePath:
			s.mu.Lock()
			s.balanceHits++
			s.mu.Unlock()
			w.WriteHeader(balanceStatus)
			fmt.Fprint(w, balanceBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return s
}

func (s *usageBalanceServer) close() { s.server.Close() }

const usageBody = `{"range":"7d","scope":"self","granularity":"day","from":"2026-10-29T00:00:00Z","until":"2026-11-05T00:00:00Z","totals":{"request_count":3,"usage_usd":1.5},"buckets":[]}`
const balanceBody = `{"included":{"balance_usd":12.5,"allowance_usd":20,"period":{"from":"2026-11-01T00:00:00Z","until":"2026-12-01T00:00:00Z"}},"purchased":{"balance_usd":3}}`

func newTestService(ub *usageBalanceServer, now func() time.Time) *QuotaService {
	client := NewClient()
	client.EndpointBase = ub.server.URL
	return NewQuotaServiceWithClient(client, now)
}

func TestFetchQuotaSuccess(t *testing.T) {
	t.Parallel()

	ub := newUsageBalanceServer(http.StatusOK, usageBody, http.StatusOK, balanceBody)
	defer ub.close()

	svc := newTestService(ub, time.Now)
	resp, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
	if err != nil {
		t.Fatalf("FetchQuota: %v", err)
	}
	if resp.Provider != ProviderKey || resp.ConnectionID != credentialID("ollama-cloud", "ollama-secret-key") {
		t.Fatalf("identity = %s/%s, want %s/ollama-cloud", resp.Provider, resp.ConnectionID, ProviderKey)
	}
	if resp.Usage == nil || resp.Balance == nil {
		t.Fatalf("sections = usage:%v balance:%v, want both", resp.Usage, resp.Balance)
	}
	if resp.UsageError != "" || resp.BalanceError != "" {
		t.Fatalf("errors = %q/%q, want empty", resp.UsageError, resp.BalanceError)
	}
	if !resp.FetchedAt.IsZero() && resp.FetchedAt.Location() != time.UTC {
		t.Fatalf("fetched_at location = %v, want UTC", resp.FetchedAt.Location())
	}
	if got := ub.usageHits + ub.balanceHits; got != 2 {
		t.Fatalf("upstream hits = %d, want 2", got)
	}
}

func TestFetchQuotaPartialSuccess(t *testing.T) {
	t.Parallel()

	ub := newUsageBalanceServer(http.StatusOK, usageBody, http.StatusUnauthorized, `{"error":"Invalid credentials"}`)
	defer ub.close()

	svc := newTestService(ub, time.Now)
	resp, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
	if err != nil {
		t.Fatalf("FetchQuota: %v, want partial success", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage = nil, want populated")
	}
	if resp.Balance != nil {
		t.Fatal("balance = non-nil, want omitted on failure")
	}
	if resp.BalanceError == "" {
		t.Fatal("balance_error = empty, want sanitized message")
	}
	if strings.Contains(resp.BalanceError, "ollama-secret-key") {
		t.Fatal("balance error leaked credential material")
	}
}

func TestFetchQuotaBothFail(t *testing.T) {
	t.Parallel()

	ub := newUsageBalanceServer(http.StatusServiceUnavailable, `{"error":"down"}`, http.StatusServiceUnavailable, `{"error":"down"}`)
	defer ub.close()

	svc := newTestService(ub, time.Now)
	_, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
	if err == nil {
		t.Fatal("expected error when both upstream calls fail")
	}

	// Failed fetches must not be cached: the next call retries upstream.
	before := ub.usageHits + ub.balanceHits
	if _, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf); err == nil {
		t.Fatal("expected error on retry")
	}
	if got := ub.usageHits + ub.balanceHits; got != before+2 {
		t.Fatalf("upstream hits = %d, want %d (failed fetch must not be cached)", got, before+2)
	}
}

func TestFetchQuotaCachesSuccessUntilTTL(t *testing.T) {
	t.Parallel()

	ub := newUsageBalanceServer(http.StatusOK, usageBody, http.StatusOK, balanceBody)
	defer ub.close()

	var now time.Time = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	svc := newTestService(ub, func() time.Time { return now })

	if _, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if got := ub.usageHits + ub.balanceHits; got != 2 {
		t.Fatalf("upstream hits = %d, want 2 (second call served from cache)", got)
	}

	now = now.Add(cacheTTL + time.Second)
	if _, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf); err != nil {
		t.Fatalf("post-TTL fetch: %v", err)
	}
	if got := ub.usageHits + ub.balanceHits; got != 4 {
		t.Fatalf("upstream hits = %d, want 4 (cache expired)", got)
	}
}

func TestFetchQuotaCacheIsScopedByRangeScopeAndConnection(t *testing.T) {
	t.Parallel()

	ub := newUsageBalanceServer(http.StatusOK, usageBody, http.StatusOK, balanceBody)
	defer ub.close()

	cfg := testConfig()
	// A second connection sharing the same key: one account, two cards.
	cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, config.OpenAICompatibility{
		Name:          "ollama-cloud-alias",
		BaseURL:       "https://ollama.com/v1",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "ollama-secret-key"}},
	})

	svc := newTestService(ub, time.Now)
	ctx := context.Background()
	for _, call := range []struct {
		id    string
		rng   UsageRange
		scope UsageScope
	}{
		{"ollama-cloud", UsageRange7d, UsageScopeSelf},
		{"ollama-cloud", UsageRange24h, UsageScopeSelf},      // different range
		{"ollama-cloud-alias", UsageRange7d, UsageScopeSelf}, // different connection
	} {
		if _, err := svc.FetchQuota(ctx, cfg, call.id, call.rng, call.scope); err != nil {
			t.Fatalf("fetch %s/%s: %v", call.id, call.rng, err)
		}
	}
	if got := ub.usageHits + ub.balanceHits; got != 6 {
		t.Fatalf("upstream hits = %d, want 6 (no cross-entry cache sharing)", got)
	}
}

func TestFetchQuotaDeduplicatesInFlight(t *testing.T) {
	t.Parallel()

	// Slow upstream so both callers overlap in the in-flight window.
	release := make(chan struct{})
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == usagePath {
			fmt.Fprint(w, usageBody)
			return
		}
		fmt.Fprint(w, balanceBody)
	}))
	defer server.Close()

	client := NewClient()
	client.EndpointBase = server.URL
	svc := NewQuotaServiceWithClient(client, time.Now)

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
		}(i)
	}

	time.Sleep(50 * time.Millisecond) // let both callers enter the in-flight window
	close(release)
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 2 { // one usage + one balance call, deduplicated across callers
		t.Fatalf("upstream hits = %d, want 2 (in-flight dedup)", got)
	}
}

func TestFetchQuotaContextCancellation(t *testing.T) {
	t.Parallel()

	ub := newUsageBalanceServer(http.StatusOK, usageBody, http.StatusOK, balanceBody)
	defer ub.close()

	svc := newTestService(ub, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.FetchQuota(ctx, testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestSanitizeErrorMessage(t *testing.T) {
	t.Parallel()

	upstreamErr := &UpstreamError{StatusCode: http.StatusForbidden, Message: "Ollama Cloud: Account suspended"}
	if got := sanitizeErrorMessage(upstreamErr); got != "Ollama Cloud: Account suspended" {
		t.Fatalf("sanitize = %q, want upstream message", got)
	}

	long := errors.New(strings.Repeat("x", 500))
	if got := sanitizeErrorMessage(long); len(got) > 230 {
		t.Fatalf("sanitize length = %d, want bounded", len(got))
	}

	if got := sanitizeErrorMessage(nil); got != "" {
		t.Fatalf("sanitize(nil) = %q, want empty", got)
	}
}
