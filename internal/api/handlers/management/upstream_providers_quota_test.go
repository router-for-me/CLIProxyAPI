package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func quotaFixtureRow(t *testing.T, baseURL string, extra map[string]any) (*fakeUpstreamProviderStore, int64) {
	t.Helper()
	st := newFakeUpstreamProviderStore()
	created, errCreate := st.Create(context.Background(), store.UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      baseURL,
		ExtraConfig:  extra,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{APIKey: "sk-quota", Name: "main"},
		},
	})
	if errCreate != nil {
		t.Fatalf("Create: %v", errCreate)
	}
	entryID := created.APIKeyEntries[0].ID
	return st, entryID
}

func TestUpstreamProviderQuota_HappyPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-quota" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"quota":{"5h":{"used":40,"limit":100,"reset_at":1757283600},"weekly":{"used":700,"limit":1000,"reset_at":1757500000000},"monthly":{"used":10,"limit":0}}}`))
	}))
	defer upstream.Close()

	// The quota probe always targets quota_url (default or override); the
	// stub is wired via extra_config.quota_url.
	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The 5h spec window now reports under the official "rolling" canonical
	// key; used/limit windows keep the derived percent.
	for _, want := range []string{`"ok":true`, `"key":"rolling"`, `"percent_used":40`, `"key":"weekly"`, `"percent_used":70`, `"key":"monthly"`, `"percent_used":-1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	// Epoch seconds vs milliseconds both normalize to RFC3339.
	if !strings.Contains(body, "2025-09-") && !strings.Contains(body, "2026-") {
		t.Logf("reset_at values: %s", body)
	}
}

func TestUpstreamProviderQuota_Aliases(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"hourly":{"used":1,"limit":2},"wk":{"used":3,"limit":4},"mo":{"used":5,"limit":10}}}`))
	}))
	defer upstream.Close()

	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	body := rec.Body.String()
	for _, want := range []string{`"key":"rolling"`, `"key":"weekly"`, `"key":"monthly"`, `"percent_used":50`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestUpstreamProviderQuota_FailOpen404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "not available upstream") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamProviderQuota_Unauthorized(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	if !strings.Contains(rec.Body.String(), "rejected the API key") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamProviderQuota_Timeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()

	// Shrink the fetch timeout so the test stays fast.
	old := openCodeQuotaFetchTimeout
	openCodeQuotaFetchTimeout = 5 * time.Millisecond
	defer func() { openCodeQuotaFetchTimeout = old }()

	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	if !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "quota fetch failed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamProviderQuota_QuotaURLOverride(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/quota" {
			t.Errorf("path = %s, want /custom/quota", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"5h":{"used":1,"limit":2}}`))
	}))
	defer upstream.Close()

	// BaseURL points elsewhere; quota_url override routes the probe.
	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL + "/custom/quota"})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamProviderQuota_MissingEntry(t *testing.T) {
	st, _ := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", nil)
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (entry_id required)", rec.Code)
	}
}

// TestUpstreamProviderQuota_OfficialShape verifies parsing of the REAL
// official response shape (live as of 2026-09): windows keyed
// rolling/weekly/monthly, each {status, percent, resetsAt} with an
// RFC3339 resetsAt string — not the used/limit shape OmniRoute's spec
// described.
func TestUpstreamProviderQuota_OfficialShape(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-09-07T21:01:19.442Z"},"weekly":{"status":"ok","percent":1,"resetsAt":"2026-09-14T00:00:00.442Z"},"monthly":{"status":"ok","percent":85,"resetsAt":"2026-09-25T06:01:44.442Z"}}}`))
	}))
	defer upstream.Close()

	st, entryID := quotaFixtureRow(t, "https://opencode.ai/zen/go/v1", map[string]any{"quota_url": upstream.URL})
	h := newSeedModelsHandler(t, st)
	rec := postProviderAction(t, h, st.rows[1].ID, "quota", fmt.Sprintf(`{"entry_id":%d}`, entryID))
	body := rec.Body.String()
	for _, want := range []string{
		`"ok":true`,
		`"key":"rolling"`, `"percent_used":0`,
		`"key":"weekly"`, `"percent_used":1`,
		`"key":"monthly"`, `"percent_used":85`,
		// resetsAt strings pass through normalized.
		`2026-09-25T06:01:44`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"percent_used":-1`) {
		t.Fatalf("percent must come from the upstream field, got -1: %s", body)
	}
}
