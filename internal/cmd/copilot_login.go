package cmd

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "time"

    copilot "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/util"
    sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
    coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    log "github.com/sirupsen/logrus"
)

// DoCopilotAuthLogin performs Copilot authentication via GitHub Device Flow.
// It guides the user to enter a verification code at GitHub and polls until
// an access token is issued, then exchanges for a Copilot token and persists
// it into the configured auth directory as type=copilot.
func DoCopilotAuthLogin(cfg *config.Config, options *LoginOptions) {
    if cfg == nil {
        cfg = &config.Config{}
    }
    ctx := context.Background()

    ghBase := strings.TrimSuffix(strings.TrimSpace(cfg.Copilot.GitHubBaseURL), "/")
    if ghBase == "" { ghBase = strings.TrimSuffix(copilot.DefaultGitHubBaseURL, "/") }
    ghAPIBase := strings.TrimSuffix(strings.TrimSpace(cfg.Copilot.GitHubAPIBaseURL), "/")
    if ghAPIBase == "" { ghAPIBase = strings.TrimSuffix(copilot.DefaultGitHubAPIBaseURL, "/") }
    clientID := strings.TrimSpace(cfg.Copilot.GitHubClientID)
    if clientID == "" { clientID = copilot.DefaultGitHubClientID }

    deviceCodeURL := ghBase + copilot.DefaultGitHubDeviceCodePath
    tokenURL := ghBase + copilot.DefaultGitHubAccessTokenPath
    copilotTokenURL := ghAPIBase + copilot.DefaultCopilotTokenPath

    // 1) Request device code
    form := url.Values{
        "client_id": {clientID},
        "scope":     {copilot.DefaultGitHubScope},
    }
    httpClient := util.SetProxy(&cfg.SDKConfig, &http.Client{})
    req, _ := http.NewRequestWithContext(ctx, http.MethodPost, deviceCodeURL, strings.NewReader(form.Encode()))
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    req.Header.Set("Accept", "application/json")
    resp, err := httpClient.Do(req)
    if err != nil {
        fmt.Printf("Copilot login failed: request device code error: %v\n", err)
        return
    }
    defer func(){ _ = resp.Body.Close() }()
    if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
        fmt.Printf("Copilot login failed: device code status %d\n", resp.StatusCode)
        return
    }
    var dc struct {
        DeviceCode     string `json:"device_code"`
        UserCode       string `json:"user_code"`
        VerificationURI string `json:"verification_uri"`
        ExpiresIn      int    `json:"expires_in"`
        Interval       int    `json:"interval"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
        fmt.Printf("Copilot login failed: invalid device code response: %v\n", err)
        return
    }

    // Print instructions
    fmt.Printf("Visit %s and enter the code: %s\n", dc.VerificationURI, dc.UserCode)

    // 2) Poll for GitHub access token
    interval := dc.Interval + 1
    if interval <= 0 { interval = 5 }
    deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
    var ghToken string
    for time.Now().Before(deadline) {
        form := url.Values{
            "client_id":  {clientID},
            "device_code":{dc.DeviceCode},
            "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
        }
        req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
        req.Header.Set("Accept", "application/json")
        req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
        resp, err := httpClient.Do(req)
        if err == nil && resp != nil {
            var t struct{ AccessToken string `json:"access_token"` }
            _ = json.NewDecoder(resp.Body).Decode(&t)
            _ = resp.Body.Close()
            if s := strings.TrimSpace(t.AccessToken); s != "" {
                ghToken = s
                break
            }
        }
        time.Sleep(time.Duration(interval) * time.Second)
    }
    if ghToken == "" {
        fmt.Println("Copilot login failed: timeout waiting for approval")
        return
    }

    // 3) Exchange Copilot token
    req, _ = http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
    req.Header.Set("Authorization", "token "+ghToken)
    req.Header.Set("Accept", "application/json")
    // Provide headers commonly required by Copilot token endpoint
    req.Header.Set("User-Agent", "cli-proxy-copilot")
    req.Header.Set("OpenAI-Intent", "copilot-cli-login")
    req.Header.Set("Editor-Plugin-Name", "cli-proxy")
    req.Header.Set("Editor-Plugin-Version", "1.0.0")
    req.Header.Set("Editor-Version", "cli/1.0")
    req.Header.Set("X-GitHub-Api-Version", "2023-07-07")
    resp, err = httpClient.Do(req)
    if err != nil {
        fmt.Printf("Copilot login failed: token request error: %v\n", err)
        return
    }
    defer func(){ _ = resp.Body.Close() }()
    if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
        fmt.Printf("Copilot login failed: token status %d\n", resp.StatusCode)
        return
    }
    var out struct{ Token string `json:"token"`; ExpiresAt int64 `json:"expires_at"`; RefreshIn int `json:"refresh_in"` }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        fmt.Printf("Copilot login failed: invalid token response: %v\n", err)
        return
    }

    // 4) Persist auth
    var expTime time.Time
    if out.ExpiresAt > 1_000_000_000_000 { // ms
        expTime = time.UnixMilli(out.ExpiresAt)
    } else if out.ExpiresAt > 0 { // s
        expTime = time.Unix(out.ExpiresAt, 0)
    }
    storage := &copilot.TokenStorage{
        AccessToken: out.Token,
        LastRefresh: time.Now().Format(time.RFC3339),
        Expire:      expTime.Format(time.RFC3339),
        ExpiresAt:   out.ExpiresAt,
        RefreshIn:   out.RefreshIn,
    }
    // derive proxy endpoint from token (key: proxy-ep)
    deriveBaseURL := func(tok string) string {
        parts := strings.Split(tok, ";")
        var ep string
        for _, p := range parts {
            p = strings.TrimSpace(p)
            if strings.HasPrefix(p, "proxy-ep=") {
                ep = strings.TrimPrefix(p, "proxy-ep=")
                break
            }
        }
        if ep == "" { return "" }
        // if full URL provided, keep scheme/host, else prefix https://
        if strings.Contains(ep, "://") {
            ep = strings.TrimRight(ep, "/")
        } else {
            ep = "https://" + strings.TrimRight(ep, "/")
        }
        // ensure codex base path suffix
        if !strings.HasSuffix(ep, "/backend-api/codex") {
            ep = ep + "/backend-api/codex"
        }
        return ep
    }
    baseURL := deriveBaseURL(out.Token)

    id := fmt.Sprintf("copilot-%d.json", time.Now().UnixMilli())
    record := &coreauth.Auth{ ID: id, Provider: "copilot", FileName: id, Storage: storage, Metadata: map[string]any{"access_token": out.Token, "expires_at": out.ExpiresAt, "refresh_in": out.RefreshIn, "expired": storage.Expire}, CreatedAt: time.Now(), UpdatedAt: time.Now(), Status: coreauth.StatusActive }
    if baseURL != "" {
        record.Attributes = map[string]string{"base_url": baseURL}
    }

    store := sdkAuth.GetTokenStore()
    if setter, ok := store.(interface{ SetBaseDir(string) }); ok {
        base := cfg.AuthDir
        if strings.TrimSpace(base) == "" {
            base, _ = os.UserHomeDir()
            base = filepath.Join(base, ".cli-proxy-api")
        }
        setter.SetBaseDir(base)
    }
    savedPath, err := store.Save(ctx, record)
    if err != nil {
        fmt.Printf("Failed to save token to file: %v\n", err)
        return
    }
    if savedPath != "" {
        fmt.Printf("Authentication saved to %s\n", savedPath)
    }
    log.Info("Copilot authentication successful!")
}
