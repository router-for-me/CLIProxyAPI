package cmd

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Minimal E2E without network: fake GitHub/Copilot endpoints and assert a file is saved.
func TestDoCopilotAuthLogin_SavesFile(t *testing.T) {
    t.Parallel()

    mux := http.NewServeMux()
    mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "device_code": "dev-xyz",
            "user_code": "ABCD-EFGH",
            "verification_uri": "https://github.com/login/device",
            "expires_in": 5,
            "interval": 0,
        })
    })
    mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token": "gh_pat_fake",
        })
    })
    mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "token": "copilot_token_fake",
            "expires_at": time.Now().Add(1 * time.Hour).UnixMilli(),
            "refresh_in": 3600,
        })
    })
    fake := httptest.NewServer(mux)
    defer fake.Close()

    dir := t.TempDir()
    cfg := &config.Config{AuthDir: dir}
    cfg.Copilot.GitHubBaseURL = fake.URL
    cfg.Copilot.GitHubAPIBaseURL = fake.URL
    cfg.Copilot.GitHubClientID = "test-client"

    DoCopilotAuthLogin(cfg, &LoginOptions{NoBrowser: true})

    // Expect at least one json file written into auth dir
    entries, err := os.ReadDir(dir)
    if err != nil {
        t.Fatalf("readdir: %v", err)
    }
    found := false
    for _, e := range entries {
        if e.IsDir() { continue }
        if filepath.Ext(e.Name()) == ".json" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("expected a json auth file to be saved in %s", dir)
    }
}

