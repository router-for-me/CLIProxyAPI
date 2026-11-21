package auth

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
)

func TestCopilotCatalogDoesNotDriveSharedCredentialRefresh(t *testing.T) {
	start := time.Date(2026, 8, 20, 18, 9, 0, 0, time.UTC)
	lead := 5 * time.Minute
	setRefreshLeadFactory(t, copilot.Provider, func() *time.Duration { return &lead })
	a := &Auth{ID: "account", Provider: copilot.Provider, LastRefreshedAt: start, Metadata: map[string]any{"expired": start.Add(24 * time.Hour).Format(time.RFC3339)}}
	m := &Manager{}
	for _, catalog := range []string{"", `{"fetched_at":"2020-01-01T00:00:00Z","data":[]}`} {
		a.Metadata[copilot.CatalogKey] = catalog
		next, scheduled := nextRefreshCheckAt(start, a, time.Second)
		want := start.Add(24*time.Hour - lead)
		if !scheduled || !next.Equal(want) || m.shouldRefresh(a, start) {
			t.Fatalf("catalog affected credential clock: next=%s scheduled=%v", next, scheduled)
		}
		if !m.shouldRefresh(a, want) {
			t.Fatal("token refresh was not scheduled")
		}
	}
}
