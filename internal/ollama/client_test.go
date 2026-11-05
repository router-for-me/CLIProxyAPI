package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient returns a client pointed at the test server.
func newTestClient(server *httptest.Server) *Client {
	client := NewClient()
	client.EndpointBase = server.URL
	return client
}

func TestNewClientKeepsFixedOrigin(t *testing.T) {
	t.Parallel()

	client := NewClient()
	if got, want := client.endpoint(usagePath), BaseURL+usagePath; got != want {
		t.Fatalf("endpoint = %q, want fixed origin %q", got, want)
	}
	if got, want := client.endpoint(balancePath), BaseURL+balancePath; got != want {
		t.Fatalf("endpoint = %q, want fixed origin %q", got, want)
	}
	if !strings.HasPrefix(BaseURL, "https://") {
		t.Fatalf("BaseURL = %q, want https origin", BaseURL)
	}
}

func TestFetchUsageSendsKeyAndParameters(t *testing.T) {
	t.Parallel()

	var gotAuth, gotRange, gotScope string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotRange = r.URL.Query().Get("range")
		gotScope = r.URL.Query().Get("scope")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"range":"7d","scope":"self","granularity":"day","from":"2026-10-29T00:00:00Z","until":"2026-11-05T00:00:00Z","totals":{"request_count":3,"usage_usd":1.5},"buckets":[{"from":"2026-11-04T00:00:00Z","until":"2026-11-05T00:00:00Z","partial":true,"request_count":1}]}`)
	}))
	defer server.Close()

	client := newTestClient(server)
	resp, err := client.FetchUsage(context.Background(), "ollama-test-key", UsageRange7d, UsageScopeSelf)
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if gotAuth != "Bearer ollama-test-key" {
		t.Fatalf("Authorization = %q, want bearer test key", gotAuth)
	}
	if gotRange != "7d" || gotScope != "self" {
		t.Fatalf("query = %q/%q, want 7d/self", gotRange, gotScope)
	}
	if resp.Range != "7d" || resp.Granularity != "day" {
		t.Fatalf("response = %+v, want range 7d granularity day", resp)
	}
	if resp.Totals.RequestCount != 3 || resp.Totals.UsageUSD == nil || *resp.Totals.UsageUSD != 1.5 {
		t.Fatalf("totals = %+v, want 3 requests / $1.50", resp.Totals)
	}
	if len(resp.Buckets) != 1 || !resp.Buckets[0].Partial {
		t.Fatalf("buckets = %+v, want one partial bucket", resp.Buckets)
	}
	if resp.Buckets[0].UsageUSD != nil {
		t.Fatalf("absent usage_usd must stay null, got %v", *resp.Buckets[0].UsageUSD)
	}
}

func TestFetchUsageRejectsInvalidRangeAndScope(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not be called for invalid parameters")
	}))
	defer server.Close()

	client := newTestClient(server)
	if _, err := client.FetchUsage(context.Background(), "key", UsageRange("1h"), UsageScopeSelf); err == nil {
		t.Fatal("invalid range accepted")
	}
	if _, err := client.FetchUsage(context.Background(), "key", UsageRange7d, UsageScope("team")); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestFetchBalanceModernShape(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"included":{"balance_usd":12.5,"allowance_usd":20,"period":{"from":"2026-11-01T00:00:00Z","until":"2026-12-01T00:00:00Z"}},"purchased":{"balance_usd":3.25}}`)
	}))
	defer server.Close()

	client := newTestClient(server)
	resp, err := client.FetchBalance(context.Background(), "key")
	if err != nil {
		t.Fatalf("FetchBalance: %v", err)
	}
	if resp.Included == nil {
		t.Fatalf("included = nil, want modern shape: %+v", resp)
	}
	if *resp.Included.BalanceUSD != 12.5 || *resp.Included.AllowanceUSD != 20 {
		t.Fatalf("included = %+v, want $12.50 of $20", resp.Included)
	}
	if resp.Included.Period == nil || resp.Included.Period.Until != "2026-12-01T00:00:00Z" {
		t.Fatalf("period = %+v, want reset 2026-12-01", resp.Included.Period)
	}
	if resp.Purchased == nil || *resp.Purchased.BalanceUSD != 3.25 {
		t.Fatalf("purchased = %+v, want $3.25", resp.Purchased)
	}
	if resp.Legacy != nil {
		t.Fatalf("legacy = %+v, want nil for modern shape", resp.Legacy)
	}

	// The wire format must keep the upstream's single included field.
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"included"`) || strings.Contains(string(raw), "legacy_included") {
		t.Fatalf("wire format = %s, want upstream included field", raw)
	}
}

func TestFetchBalanceLegacyShape(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"included":{"session":{"remaining_percent":42,"resets_at":"2026-11-05T08:00:00Z"},"weekly":{"remaining_percent":71,"resets_at":"2026-11-09T08:00:00Z"}},"purchased":{"balance_usd":0}}`)
	}))
	defer server.Close()

	client := newTestClient(server)
	resp, err := client.FetchBalance(context.Background(), "key")
	if err != nil {
		t.Fatalf("FetchBalance: %v", err)
	}
	if resp.Legacy == nil || resp.Included != nil {
		t.Fatalf("shapes = included:%v legacy:%v, want legacy only", resp.Included, resp.Legacy)
	}
	if *resp.Legacy.Session.RemainingPercent != 42 || *resp.Legacy.Weekly.RemainingPercent != 71 {
		t.Fatalf("legacy = %+v, want 42%% session / 71%% weekly", resp.Legacy)
	}
	if *resp.Purchased.BalanceUSD != 0 {
		t.Fatalf("purchased = %v, want zero", *resp.Purchased.BalanceUSD)
	}
}

func TestFetchBalancePurchasedOnly(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"included":{"balance_usd":0,"allowance_usd":0,"period":{"from":"2026-11-01T00:00:00Z","until":"2026-12-01T00:00:00Z"}},"purchased":{"balance_usd":9.99}}`)
	}))
	defer server.Close()

	client := newTestClient(server)
	resp, err := client.FetchBalance(context.Background(), "key")
	if err != nil {
		t.Fatalf("FetchBalance: %v", err)
	}
	if resp.Included == nil || *resp.Included.BalanceUSD != 0 {
		t.Fatalf("included = %+v, want zeroed modern shape", resp.Included)
	}
	if *resp.Purchased.BalanceUSD != 9.99 {
		t.Fatalf("purchased = %v, want $9.99", *resp.Purchased.BalanceUSD)
	}
}

func TestUpstreamErrorsAreSanitized(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		wantMsg    string
	}{
		{"invalid key", http.StatusUnauthorized, `{"error":"Invalid credentials"}`, "", "Ollama Cloud: Invalid credentials"},
		{"suspended", http.StatusForbidden, `{"error":"Account suspended"}`, "", "Ollama Cloud: Account suspended or access denied"},
		{"throttled", http.StatusTooManyRequests, `{"error":"Too many requests"}`, "30", "Ollama Cloud: Too many requests (retry after 30s)"},
		{"outage", http.StatusServiceUnavailable, `{"error":"temporarily unavailable"}`, "", "Ollama Cloud: temporarily unavailable"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()

			client := newTestClient(server)
			_, err := client.FetchBalance(context.Background(), "secret-key-value")
			if err == nil {
				t.Fatal("expected upstream error")
			}
			upstreamErr, ok := err.(*UpstreamError)
			if !ok {
				t.Fatalf("error type = %T, want *UpstreamError", err)
			}
			if upstreamErr.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", upstreamErr.StatusCode, tc.status)
			}
			if upstreamErr.Message != tc.wantMsg {
				t.Fatalf("message = %q, want %q", upstreamErr.Message, tc.wantMsg)
			}
			if strings.Contains(err.Error(), "secret-key-value") {
				t.Fatal("error message leaked the API key")
			}
		})
	}
}

func TestUpstreamInvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not-json`)
	}))
	defer server.Close()

	client := newTestClient(server)
	_, err := client.FetchBalance(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	upstreamErr, ok := err.(*UpstreamError)
	if !ok || upstreamErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v, want 502 upstream error", err)
	}
	if strings.Contains(upstreamErr.Message, "not-json") {
		t.Fatal("raw upstream body leaked into the error message")
	}
}

func TestUpstreamEmptyBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
	}))
	defer server.Close()

	client := newTestClient(server)
	if _, err := client.FetchBalance(context.Background(), "key"); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestUpstreamOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"purchased":{"balance_usd":1}`)
		for i := 0; i < MaxResponseBytes+4096; i++ {
			fmt.Fprint(w, "x")
		}
	}))
	defer server.Close()

	client := newTestClient(server)
	if _, err := client.FetchBalance(context.Background(), "key"); err == nil {
		t.Fatal("expected error for oversized response")
	}
}

func TestUpstreamNetworkFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	client := newTestClient(server)
	server.Close()

	_, err := client.FetchBalance(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for unreachable upstream")
	}
	upstreamErr, ok := err.(*UpstreamError)
	if !ok || upstreamErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v, want 502 upstream error", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error must not contain credential material")
	}
}

func TestUpstreamTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	client := newTestClient(server)
	client.HTTPClient = &http.Client{Timeout: 20 * time.Millisecond}

	start := time.Now()
	if _, err := client.FetchBalance(context.Background(), "key"); err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request took %v, want bounded by client timeout", elapsed)
	}
}

func TestRequestContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	client := newTestClient(server)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.FetchBalance(ctx, "key")
	if err == nil {
		t.Fatal("expected context error")
	}
	if _, ok := err.(*UpstreamError); ok {
		t.Fatal("cancelled context must not be reported as an upstream failure")
	}
}
