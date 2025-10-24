package auth_test

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    executor "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
    coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// memStore is a minimal in-memory Store to assert persistence.
type memStore struct{ saved []*coreauth.Auth }

func (m *memStore) List(ctx context.Context) ([]*coreauth.Auth, error) { return append([]*coreauth.Auth(nil), m.saved...), nil }
func (m *memStore) Save(ctx context.Context, a *coreauth.Auth) (string, error) {
    if a != nil {
        // emulate replace by id
        replaced := false
        for i := range m.saved {
            if m.saved[i] != nil && m.saved[i].ID == a.ID {
                m.saved[i] = a.Clone()
                replaced = true
                break
            }
        }
        if !replaced {
            m.saved = append(m.saved, a.Clone())
        }
    }
    return a.ID, nil
}
func (m *memStore) Delete(ctx context.Context, id string) error {
    out := make([]*coreauth.Auth, 0, len(m.saved))
    for i := range m.saved {
        if m.saved[i] != nil && m.saved[i].ID != id {
            out = append(out, m.saved[i])
        }
    }
    m.saved = out
    return nil
}

// End-to-end over real CodexExecutor(copilot) hitting a fake /copilot_internal/v2/token
func TestManager_Copilot_HTTP_End2End(t *testing.T) {
    t.Parallel()

    // Fake upstream Copilot token endpoint; verify Authorization header
    var authHeaderOK bool
    mux := http.NewServeMux()
    mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
        if got := r.Header.Get("Authorization"); got == "token gh_pat_test" {
            authHeaderOK = true
        }
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
            "token":      "copilot_token_http_e2e",
            "expires_at": time.Now().Add(45 * time.Minute).UnixMilli(),
            "refresh_in": 2700,
        })
    })
    fake := httptest.NewServer(mux)
    defer fake.Close()

    // Build a real copilot executor with GitHubAPIBaseURL pointing to fake server
    cfg := &config.Config{}
    cfg.Copilot.GitHubAPIBaseURL = fake.URL
    exec := executor.NewCodexExecutorWithID(cfg, "copilot")

    // Manager with in-memory store to verify persistence
    store := &memStore{}
    m := coreauth.NewManager(store, nil, nil)
    m.RegisterExecutor(exec)

    now := time.Now().UTC()
    m_timeNow := func() time.Time { return now }
    // override private field via helper: not exported, so use internal method by updating a copy of Manager with monkey patch pattern is not available.
    // Workaround: rely on immediate tick since LastRefreshedAt is sufficiently in the past and refresh_in small.
    // The existing Manager uses time.Now() internally but we wait enough.

    a := &coreauth.Auth{
        ID:              "copilot-http-e2e",
        Provider:        "copilot",
        LastRefreshedAt: now.Add(-2 * time.Minute),
        Metadata: map[string]any{
            "refresh_in":          60,              // preemptive window reached
            "github_access_token": "gh_pat_test",  // PAT required by server
            "access_token":        "old",
            "last_refresh":        now.Add(-2 * time.Minute).Format(time.RFC3339),
        },
        Status:    coreauth.StatusActive,
        CreatedAt: now.Add(-10 * time.Minute),
        UpdatedAt: now.Add(-5 * time.Minute),
    }
    _ = m_timeNow // silence unused

    if _, err := m.Register(context.Background(), a); err != nil {
        t.Fatalf("register: %v", err)
    }

    // Start auto-refresh: it runs an immediate check, then periodic ticks
    m.StartAutoRefresh(context.Background(), time.Second)
    // Wait briefly for immediate check to run
    time.Sleep(200 * time.Millisecond)
    m.StopAutoRefresh()

    // Assert header was validated by server
    if !authHeaderOK {
        t.Fatalf("expected Authorization header 'token gh_pat_test' to be used")
    }

    // Assert in-memory state updated
    got, ok := m.GetByID(a.ID)
    if !ok || got == nil { t.Fatalf("auth not found") }
    if got.Metadata["access_token"] != "copilot_token_http_e2e" {
        t.Fatalf("expected access_token updated, got %v", got.Metadata["access_token"])
    }
    if _, ok := got.Metadata["refresh_in"]; !ok { t.Fatalf("expected refresh_in present") }
    if _, ok := got.Metadata["expires_at"]; !ok { t.Fatalf("expected expires_at present") }

    // Assert persistence captured updated record
    if len(store.saved) == 0 {
        t.Fatalf("expected store to have saved records")
    }
    var persisted *coreauth.Auth
    for i := range store.saved {
        if store.saved[i] != nil && store.saved[i].ID == a.ID {
            persisted = store.saved[i]
            break
        }
    }
    if persisted == nil { t.Fatalf("expected persisted record for %s", a.ID) }
    if persisted.Metadata["access_token"] != "copilot_token_http_e2e" {
        t.Fatalf("expected persisted access_token updated, got %v", persisted.Metadata["access_token"])
    }
}
