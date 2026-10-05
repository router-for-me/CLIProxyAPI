package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// newTestProviderBudgetHandler opens a PG-backed Handler with pgControl wired
// so GetProviderBudget's gate passes. Skips when PGSTORE_TEST_DSN is unset.
func newTestProviderBudgetHandler(t *testing.T, schema string) (*Handler, *store.PostgresStore) {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{DSN: pgTestDSN(), Schema: schema})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Migrate adds the idempotent ALTERs (budget_usd, entry_provider_key, ...)
	// that EnsureSchema's CREATE TABLEs don't cover on pre-existing tables.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, table := range []string{
		pg.UsageEventsTable(), pg.UpstreamProvidersTable(), pg.UpstreamProviderEntriesTable(),
		pg.UpstreamProviderModelsTable(), pg.UpstreamProviderHeadersTable(), pg.UpstreamProviderExcludedTable(),
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+table); err != nil {
			_ = err
		}
	}
	h := &Handler{}
	h.SetPGControl(pg)
	return h, pg
}

// runProviderBudget dispatches GET /provider-budget on a minimal router.
func runProviderBudget(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/provider-budget", h.GetProviderBudget)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/provider-budget", nil)
	r.ServeHTTP(w, req)
	return w
}

// TestProviderBudgetAttributesSpendViaEntryProviderKey is the end-to-end guard
// for the provider-budget attribution chain: usage_events rows carrying the
// synthesizer's entry_provider_key ("<provider-key>:key-<entryID>") must be
// summed into the budget row of the exact upstream entry whose ID the key
// encodes — regardless of the usage row's provider value (the executor channel
// key) and without any client API-key principal matching.
func TestProviderBudgetAttributesSpendViaEntryProviderKey(t *testing.T) {
	h, pg := newTestProviderBudgetHandler(t, "provider_budget_attr")
	ctx := context.Background()

	// Seed one provider with two budgeted entries.
	var providerID, entryA, entryB int64
	if err := pg.DB().QueryRowContext(ctx,
		`INSERT INTO `+pg.UpstreamProvidersTable()+` (provider_type, name, api_key) VALUES ('openai-compatibility', 'AKF-Zhipu', 'row-level-key') RETURNING id`,
	).Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := pg.DB().QueryRowContext(ctx,
		`INSERT INTO `+pg.UpstreamProviderEntriesTable()+` (provider_id, api_key, name, budget_usd) VALUES ($1, 'sk-entry-a', 'entry-a', 10.0) RETURNING id`, providerID,
	).Scan(&entryA); err != nil {
		t.Fatalf("seed entry a: %v", err)
	}
	if err := pg.DB().QueryRowContext(ctx,
		`INSERT INTO `+pg.UpstreamProviderEntriesTable()+` (provider_id, api_key, name, budget_usd) VALUES ($1, 'sk-entry-b', 'entry-b', 5.0) RETURNING id`, providerID,
	).Scan(&entryB); err != nil {
		t.Fatalf("seed entry b: %v", err)
	}

	us := store.NewUsageStore(pg)
	now := time.Now().UTC()
	// Successful spend on entry A: provider value is the executor channel key
	// (NOT the row name/type) and the principal is the CLIENT key — the exact
	// shape the old broken join failed to match.
	seed := []store.UsageEvent{}
	addSeed := func(entryID int64, cost float64, failed bool) {
		seed = append(seed, store.UsageEvent{
			Provider: "openai-compatible-akf-zhipu", Model: "glm-5.2",
			EntryProviderKey: "openai-compatible-akf-zhipu:key-" + strconv.FormatInt(entryID, 10),
			APIKeyPrincipal:  "sk-client-key", CostUSD: cost, Failed: failed, RequestedAt: now,
		})
	}
	// Successful spend on entry A: provider value is the executor channel key
	// (NOT the row name/type) and the principal is the CLIENT key — the exact
	// shape the old broken join failed to match.
	addSeed(entryA, 2.5, false)
	addSeed(entryA, 1.0, false)
	// Spend on entry B: same provider, different entry.
	addSeed(entryB, 0.75, false)
	// Failed attempt on entry A must NOT count (failed = true).
	addSeed(entryA, 99.0, true)
	if err := us.BatchInsertEvents(ctx, seed); err != nil {
		t.Fatalf("seed usage events: %v", err)
	}

	w := runProviderBudget(t, h)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Rows []struct {
			EntryID   int64   `json:"entry_id"`
			BudgetUSD float64 `json:"budget_usd"`
			SpentUSD  float64 `json:"spent_usd"`
			Remaining float64 `json:"remaining_usd"`
			UsagePct  float64 `json:"usage_pct"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Rows) != 2 {
		t.Fatalf("rows = %d; want 2; body=%s", len(resp.Rows), w.Body.String())
	}
	byEntry := map[int64]float64{}
	for _, row := range resp.Rows {
		byEntry[row.EntryID] = row.SpentUSD
	}
	if got := byEntry[entryA]; got != 3.5 {
		t.Errorf("entry A spent = %v; want 3.5 (failed attempt excluded)", got)
	}
	if got := byEntry[entryB]; got != 0.75 {
		t.Errorf("entry B spent = %v; want 0.75", got)
	}
}
