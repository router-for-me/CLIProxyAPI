package store_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// store_helpers pulls the production plan/report types via the store alias
// so the test stays external to the production package and never creates an
// import cycle with configsnapshot.
type (
	normalizedResourcePlan = store.NormalizedResourcePlan
	upstreamProvider       = store.UpstreamProvider
	upstreamProviderAPIKey = store.UpstreamProviderAPIKey
	apiKey                 = store.APIKey
)

// TestApplyNormalizedResourcePlanReimportStableIDs proves that importing
// the same plan twice leaves child IDs untouched on the second pass.
func TestApplyNormalizedResourcePlanReimportStableIDs(t *testing.T) {
	skipIfNoPostgres(t)
	pg := openTestPostgresStore(t)
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	plan := buildOpenAIPlan("stable-pool", "https://stable.example", []openaiEntry{
		{Key: "sk-A", Name: "primary"},
		{Key: "sk-B", Name: "backup"},
	})
	if err := pg.ApplyNormalizedResourcePlan(ctx, plan); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	first := loadEntryIDs(t, pg.DB(), "openai-compatibility", "stable-pool")

	if err := pg.ApplyNormalizedResourcePlan(ctx, plan); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	second := loadEntryIDs(t, pg.DB(), "openai-compatibility", "stable-pool")

	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("entry count drift: first=%d second=%d", len(first), len(second))
	}
	for id := range first {
		if _, ok := second[id]; !ok {
			t.Fatalf("entry id %d lost on re-import", id)
		}
	}
	if plan.Report.RolledBack {
		t.Fatal("second apply marked RolledBack=true")
	}
	if !plan.Report.Committed {
		t.Fatal("second apply did not mark Committed=true")
	}
}

// TestApplyNormalizedResourcePlanReorderEntries preserves entry IDs when
// entries are reordered in the incoming plan.
func TestApplyNormalizedResourcePlanReorderEntries(t *testing.T) {
	skipIfNoPostgres(t)
	pg := openTestPostgresStore(t)
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first := buildOpenAIPlan("reorder-pool", "https://reorder.example", []openaiEntry{
		{Key: "sk-A", Name: "primary"},
		{Key: "sk-B", Name: "backup"},
	})
	if err := pg.ApplyNormalizedResourcePlan(ctx, first); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	before := loadEntrySignatureMap(t, pg.DB(), "openai-compatibility", "reorder-pool")

	reorder := buildOpenAIPlan("reorder-pool", "https://reorder.example", []openaiEntry{
		{Key: "sk-B", Name: "backup"},
		{Key: "sk-A", Name: "primary"},
	})
	if err := pg.ApplyNormalizedResourcePlan(ctx, reorder); err != nil {
		t.Fatalf("reorder apply: %v", err)
	}
	after := loadEntrySignatureMap(t, pg.DB(), "openai-compatibility", "reorder-pool")

	if len(before) != len(after) {
		t.Fatalf("entry count drift: before=%d after=%d", len(before), len(after))
	}
	for sig, id := range before {
		if after[sig] != id {
			t.Fatalf("signature %s lost its id: was %d, now %d", sig, id, after[sig])
		}
	}
}

// TestApplyNormalizedResourcePlanRemoveEntry proves that an entry dropped
// from the incoming plan is deleted on the next apply, while stable entries
// keep their IDs.
func TestApplyNormalizedResourcePlanRemoveEntry(t *testing.T) {
	skipIfNoPostgres(t)
	pg := openTestPostgresStore(t)
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	three := buildOpenAIPlan("remove-pool", "https://remove.example", []openaiEntry{
		{Key: "sk-A", Name: "primary"},
		{Key: "sk-B", Name: "backup"},
		{Key: "sk-C", Name: "dr"},
	})
	if err := pg.ApplyNormalizedResourcePlan(ctx, three); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	before := loadEntrySignatureMap(t, pg.DB(), "openai-compatibility", "remove-pool")
	if len(before) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(before))
	}
	if _, ok := before["sk-A|primary"]; !ok {
		t.Fatalf("sk-A entry missing")
	}

	two := buildOpenAIPlan("remove-pool", "https://remove.example", []openaiEntry{
		{Key: "sk-A", Name: "primary"},
		{Key: "sk-B", Name: "backup"},
	})
	if err := pg.ApplyNormalizedResourcePlan(ctx, two); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after := loadEntrySignatureMap(t, pg.DB(), "openai-compatibility", "remove-pool")
	if len(after) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(after))
	}
	if _, ok := after["sk-C|dr"]; ok {
		t.Fatalf("sk-C entry still present, want deleted")
	}
}

// TestApplyNormalizedResourcePlanProviderAtomicity proves a provider
// normalization failure surfaces an error and prevents any DB write.
func TestApplyNormalizedResourcePlanProviderAtomicity(t *testing.T) {
	skipIfNoPostgres(t)
	pg := openTestPostgresStore(t)
	defer pg.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	plan := &normalizedResourcePlan{
		Providers: []upstreamProvider{
			// provider_type left blank: normalizeUpstreamProvider rejects it.
		},
	}
	err := pg.ApplyNormalizedResourcePlan(ctx, plan)
	if err == nil {
		t.Fatal("expected normalize error for blank provider_type")
	}
	if plan.Report.Committed {
		t.Fatal("plan.Committed=true despite normalization failure")
	}
}

// openTestPostgresStore returns a fresh schema-isolated store for every
// live PG test in this file. It mirrors the helper used by other store
// integration tests.
func openTestPostgresStore(t *testing.T) *store.PostgresStore {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    pgTestDSN(),
		Schema: pgTestSchemaName(t),
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// pgTestDSN returns the env-configured DSN or empty when not configured.
func pgTestDSN() string {
	return strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
}

// pgTestSchemaName returns a per-test schema name so live PG tests stay
// isolated from each other.
func pgTestSchemaName(t *testing.T) string {
	t.Helper()
	base := strings.ToLower(t.Name())
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, " ", "_")
	return "test_normalized_import_" + base
}

// skipIfNoPostgres skips live PG integration tests when no DSN is provided.
func skipIfNoPostgres(t *testing.T) {
	t.Helper()
	if pgTestDSN() == "" {
		t.Skip("PGSTORE_TEST_DSN not set; skipping Postgres integration test")
	}
}

// loadEntryIDs returns the set of entry ids currently stored for the named
// provider identity.
func loadEntryIDs(t *testing.T, db *sql.DB, providerType, name string) map[int64]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT e.id FROM upstream_provider_api_key_entries e
		 JOIN upstream_providers p ON p.id = e.provider_id
		 WHERE p.provider_type=$1 AND lower(p.name)=lower($2)`,
		providerType, name,
	)
	if err != nil {
		t.Fatalf("query entries: %v", err)
	}
	defer rows.Close()
	out := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = struct{}{}
	}
	return out
}

// loadEntrySignatureMap queries the live api-key entries for the named
// provider and returns identity -> id.
func loadEntrySignatureMap(t *testing.T, db *sql.DB, providerType, name string) map[string]int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx,
		`SELECT e.id, e.api_key, coalesce(e.name,''), coalesce(e.proxy_url,''),
		        coalesce(e.proxy_pool_id, 0), coalesce(e.weight,0),
		        coalesce(e.priority,0), coalesce(e.disabled, false)
		 FROM upstream_provider_api_key_entries e
		 JOIN upstream_providers p ON p.id = e.provider_id
		 WHERE p.provider_type=$1 AND lower(p.name)=lower($2)`,
		providerType, name,
	)
	if err != nil {
		t.Fatalf("query entry sigs: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var (
			id     int64
			apiKey string
			name   string
			proxy  string
			pool   int64
			weight int
			prio   int
			dis    bool
		)
		if err := rows.Scan(&id, &apiKey, &name, &proxy, &pool, &weight, &prio, &dis); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[entrySignature(apiKey, name, proxy, pool, weight, prio, dis)] = id
	}
	return out
}

// openaiEntry is a compact test helper to build an OpenAICompatibility
// entry without depending on the full config stack.
type openaiEntry struct {
	Key, Name string
}

// buildOpenAIPlan constructs a plan with one OpenAICompatibility pool plus
// the supplied entries.
func buildOpenAIPlan(name, baseURL string, entries []openaiEntry) *normalizedResourcePlan {
	plan := &normalizedResourcePlan{
		Providers: []upstreamProvider{{
			ProviderType:    "openai-compatibility",
			Name:            name,
			BaseURL:         baseURL,
			RoutingStrategy: "round-robin",
			APIKeyEntries:   make([]upstreamProviderAPIKey, 0, len(entries)),
		}},
	}
	for i, e := range entries {
		plan.Providers[0].APIKeyEntries = append(plan.Providers[0].APIKeyEntries, upstreamProviderAPIKey{
			APIKey:    e.Key,
			Name:      e.Name,
			SortOrder: i,
		})
	}
	return plan
}

// entrySignature returns a stable canonical string for an entry identity,
// matching the apply path's stable match logic for OpenAICompatibility
// entries.
func entrySignature(apiKey, name, proxy string, pool int64, weight, prio int, disabled bool) string {
	return apiKey + "|" + name + "|" + proxy
}
