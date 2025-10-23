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

// Ensure we send Accept: application/json to the device code endpoint; otherwise GitHub returns form-encoded body.
func TestDoCopilotAuthLogin_DeviceCodeAcceptJSON(t *testing.T) {
    t.Parallel()

    mux := http.NewServeMux()
    mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
        if r.Header.Get("Accept") != "application/json" {
            w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
            _, _ = w.Write([]byte("device_code=dev-xyz&user_code=ABCD-EFGH&verification_uri=https%3A%2F%2Fgithub.com%2Flogin%2Fdevice&expires_in=5&interval=0"))
            return
        }
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

// Require specific headers for copilot token; respond 403 if missing.
func TestDoCopilotAuthLogin_TokenHeaders(t *testing.T) {
    t.Parallel()

    mux := http.NewServeMux()
    mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{
            "device_code": "dev-abc",
            "user_code": "WXYZ-1234",
            "verification_uri": "https://github.com/login/device",
            "expires_in": 5,
            "interval": 0,
        })
    })
    mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]any{"access_token": "gh_pat_fake"})
    })
    mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
        // Check required headers
        if r.Header.Get("Authorization") != "token gh_pat_fake" ||
           r.Header.Get("Accept") != "application/json" ||
           r.Header.Get("User-Agent") == "" ||
           r.Header.Get("OpenAI-Intent") == "" ||
           r.Header.Get("Editor-Plugin-Name") == "" ||
           r.Header.Get("Editor-Plugin-Version") == "" ||
           r.Header.Get("Editor-Version") == "" ||
           r.Header.Get("X-GitHub-Api-Version") == "" {
            w.WriteHeader(http.StatusForbidden)
            _, _ = w.Write([]byte(`{"error":"missing headers"}`))
            return
        }
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
