package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedirectNeverReceivesCredential(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := newTestClient(source).FetchBalance(context.Background(), "do-not-forward")
	if err == nil || hits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestUpstreamErrorCannotEchoSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":%q}`, r.Header.Get("Authorization"))
	}))
	defer server.Close()
	_, err := newTestClient(server).FetchBalance(context.Background(), "sensitive-value")
	if err == nil || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatal("unsafe error")
	}
}

func TestCredentialRotationInvalidatesCache(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == usagePath {
			fmt.Fprint(w, usageBody)
		} else {
			fmt.Fprint(w, balanceBody)
		}
	}))
	defer server.Close()
	svc := NewQuotaServiceWithClient(newTestClient(server), time.Now)
	cfg := testConfig()
	if _, err := svc.FetchQuota(context.Background(), cfg, "ollama-cloud", UsageRange7d, UsageScopeSelf); err != nil {
		t.Fatal(err)
	}
	cfg.OpenAICompatibility[0].APIKeyEntries[0].APIKey = "new-account"
	if _, err := svc.FetchQuota(context.Background(), cfg, "ollama-cloud", UsageRange7d, UsageScopeSelf); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 4 {
		t.Fatal("credential rotation reused account cache")
	}
}
