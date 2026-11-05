package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestInvalidQuotaDocumentsRejected(t *testing.T) {
	for _, payload := range []string{`{"included":null}`, `{"included":{}}`, `{"purchased":{}}`, `{"included":{"session":{"remaining_percent":900}}}`, `null`, `[]`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }))
		_, err := newTestClient(server).FetchBalance(context.Background(), "test-key")
		server.Close()
		if err == nil {
			t.Fatalf("accepted invalid balance %s", payload)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"range":"7d","scope":"self","granularity":"hour","from":"bad","until":"bad","buckets":[]}`)
	}))
	defer server.Close()
	if _, err := newTestClient(server).FetchUsage(context.Background(), "test", UsageRange7d, UsageScopeSelf); err == nil {
		t.Fatal("accepted invalid usage")
	}
}

func TestRetryAfterEnforcedAcrossRanges(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
	}))
	defer server.Close()
	now := time.Now()
	svc := NewQuotaServiceWithClient(newTestClient(server), func() time.Time { return now })
	for _, rng := range []UsageRange{UsageRange7d, UsageRange24h} {
		_, _ = svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", rng, UsageScopeSelf)
	}
	if hits.Load() != 2 {
		t.Fatal("retry wait was bypassed")
	}
	now = now.Add(31 * time.Second)
	_, _ = svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
	if hits.Load() != 4 {
		t.Fatal("expired retry wait did not allow fetch")
	}
}

func TestPartialFailureIsRetried(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == usagePath {
			fmt.Fprint(w, usageBody)
			return
		}
		if hits.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, balanceBody)
	}))
	defer server.Close()
	svc := NewQuotaServiceWithClient(newTestClient(server), time.Now)
	for i := 0; i < 2; i++ {
		data, err := svc.FetchQuota(context.Background(), testConfig(), "ollama-cloud", UsageRange7d, UsageScopeSelf)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && data.Balance == nil {
			t.Fatal("manual retry retained failed balance")
		}
	}
}

func TestDiscoverySeparatesAndRotatesCredentials(t *testing.T) {
	cfg := testConfig()
	cfg.OpenAICompatibility[0].APIKeyEntries = append(cfg.OpenAICompatibility[0].APIKeyEntries, cfg.OpenAICompatibility[0].APIKeyEntries[0])
	cfg.OpenAICompatibility[0].APIKeyEntries[1].APIKey = "second"
	svc := NewQuotaService()
	before := svc.Connections(cfg)
	if len(before) != 2 || before[0].ID == before[1].ID {
		t.Fatal("credentials not separated")
	}
	cfg.OpenAICompatibility[0].APIKeyEntries[0].APIKey = "replacement"
	if svc.Connections(cfg)[0].ID == before[0].ID {
		t.Fatal("replacement retained identity")
	}
}
