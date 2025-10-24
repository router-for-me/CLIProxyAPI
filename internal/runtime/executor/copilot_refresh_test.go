package executor

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// Verify CodexExecutor.Refresh implements Copilot refresh using github_access_token
func TestCopilot_Refresh_UsesGitHubTokenAndUpdatesMetadata(t *testing.T) {
    // Fake GitHub API /copilot_internal/v2/token returning new token
    mux := http.NewServeMux()
    mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
        if got := r.Header.Get("Authorization"); got != "token gh_pat_test" {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
            "token":      "copilot_new_token",
            "expires_at": time.Now().Add(30 * time.Minute).UnixMilli(),
            "refresh_in": 1800,
        })
    })
    fake := httptest.NewServer(mux)
    defer fake.Close()

    cfg := &config.Config{}
    cfg.Copilot.GitHubAPIBaseURL = fake.URL

    auth := &cliproxyauth.Auth{
        ID:       "copilot:refresh-test",
        Provider: "copilot",
        Metadata: map[string]any{
            "github_access_token": "gh_pat_test",
            // older token present
            "access_token": "old",
        },
        Status: cliproxyauth.StatusActive,
    }

    exec := NewCodexExecutorWithID(cfg, "copilot")
    updated, err := exec.Refresh(context.Background(), auth)
    if err != nil {
        t.Fatalf("refresh error: %v", err)
    }
    if updated == nil || updated.Metadata == nil {
        t.Fatalf("updated metadata missing")
    }
    if got := updated.Metadata["access_token"]; got != "copilot_new_token" {
        t.Fatalf("expected access_token updated, got %v", got)
    }
    if _, ok := updated.Metadata["refresh_in"]; !ok {
        t.Fatalf("expected refresh_in present in metadata")
    }
    if _, ok := updated.Metadata["expires_at"]; !ok {
        t.Fatalf("expected expires_at present in metadata")
    }
}
